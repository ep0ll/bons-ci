package handler

import (
	"io"
	"net/http"

	"github.com/bons/bons-ci/plugins/byoc/internal/github"
	"github.com/bons/bons-ci/plugins/byoc/internal/observability"
	"github.com/bons/bons-ci/plugins/byoc/internal/orchestrator"
	"github.com/bons/bons-ci/plugins/byoc/internal/store"
	"github.com/gin-gonic/gin"
	"github.com/rs/zerolog"
)

// WebhookHandler handles POST /webhooks/github/:tenant_id.
type WebhookHandler struct {
	store        store.Store
	githubClient github.Client
	orchestrator *orchestrator.Orchestrator
	metrics      *observability.Metrics
	logger       zerolog.Logger
}

// NewWebhookHandler constructs a WebhookHandler.
func NewWebhookHandler(
	s store.Store,
	gh github.Client,
	orch *orchestrator.Orchestrator,
	m *observability.Metrics,
	logger zerolog.Logger,
) *WebhookHandler {
	return &WebhookHandler{
		store:        s,
		githubClient: gh,
		orchestrator: orch,
		metrics:      m,
		logger:       logger.With().Str("component", "webhook_handler").Logger(),
	}
}

// Handle processes an incoming GitHub webhook for a specific tenant.
// It validates the HMAC-SHA256 signature before any processing.
func (h *WebhookHandler) Handle(c *gin.Context) {
	tenantID := c.Param("tenant_id")

	// Read the raw body for signature validation before JSON decoding.
	body, err := io.ReadAll(io.LimitReader(c.Request.Body, 1<<20)) // 1 MB cap
	if err != nil {
		respondError(c, http.StatusBadRequest, "READ_ERROR", "failed to read request body")
		return
	}

	// Fetch tenant to get webhook secret.
	t, err := h.store.GetTenant(c.Request.Context(), tenantID)
	if err != nil {
		if store.IsNotFound(err) {
			// Return 404 but do not reveal whether the tenant exists — prevents enumeration.
			h.metrics.WebhookTotal.WithLabelValues("workflow_job", "unknown", "not_found").Inc()
			respondError(c, http.StatusNotFound, "NOT_FOUND", "tenant not found")
			return
		}
		h.logger.Error().Str("tenant_id", tenantID).Err(err).Msg("get tenant for webhook")
		respondError(c, http.StatusInternalServerError, "INTERNAL_ERROR", "")
		return
	}

	// Validate HMAC signature.
	sigHeader := c.GetHeader("X-Hub-Signature-256")
	if err := h.githubClient.ValidateWebhookSignature(body, sigHeader, t.WebhookSecret); err != nil {
		h.logger.Warn().Str("tenant_id", tenantID).Msg("webhook signature validation failed")
		h.metrics.WebhookTotal.WithLabelValues("workflow_job", "unknown", "invalid_signature").Inc()
		respondError(c, http.StatusUnauthorized, "INVALID_SIGNATURE", "webhook signature validation failed")
		return
	}

	// Verify this is a workflow_job event.
	eventType := c.GetHeader("X-GitHub-Event")
	if eventType != "workflow_job" {
		// Acknowledge non-workflow_job events (e.g. ping) without processing.
		h.metrics.WebhookTotal.WithLabelValues(eventType, "", "ignored").Inc()
		c.JSON(http.StatusOK, gin.H{"message": "event ignored"})
		return
	}

	event, err := h.githubClient.ParseWorkflowJobEvent(body)
	if err != nil {
		h.logger.Error().Str("tenant_id", tenantID).Err(err).Msg("parse workflow_job event")
		respondError(c, http.StatusBadRequest, "PARSE_ERROR", "failed to parse webhook payload")
		return
	}

	h.logger.Info().
		Str("tenant_id", tenantID).
		Str("action", string(event.Action)).
		Int64("job_id", event.WorkflowJob.ID).
		Strs("labels", event.WorkflowJob.Labels).
		Msg("workflow_job webhook received")

	// Respond 202 immediately — processing is async.
	c.JSON(http.StatusAccepted, gin.H{"message": "accepted"})

	switch event.Action {
	case github.ActionQueued:
		h.metrics.WebhookTotal.WithLabelValues("workflow_job", "queued", "accepted").Inc()
		h.orchestrator.EnqueueJob(t, event.WorkflowJob.ID, event.WorkflowJob.Labels)

	case github.ActionCompleted:
		h.metrics.WebhookTotal.WithLabelValues("workflow_job", "completed", "accepted").Inc()
		go func() {
			// Runner name set by cloud-init as "byoc-<runner_id>"; parse back.
			runnerID := parseRunnerID(event.WorkflowJob.RunnerName)
			if runnerID == "" {
				return
			}
			if err := h.orchestrator.HandleJobCompleted(c.Request.Context(), tenantID, runnerID); err != nil {
				h.logger.Error().Str("tenant_id", tenantID).Err(err).Msg("handle job completed")
			}
		}()

	default:
		h.metrics.WebhookTotal.WithLabelValues("workflow_job", string(event.Action), "ignored").Inc()
	}
}

// parseRunnerID extracts the platform runner UUID from a GitHub runner name.
// Runner names follow the pattern "byoc-<uuid>".
func parseRunnerID(runnerName string) string {
	const prefix = "byoc-"
	if len(runnerName) > len(prefix) && runnerName[:len(prefix)] == prefix {
		return runnerName[len(prefix):]
	}
	return ""
}
