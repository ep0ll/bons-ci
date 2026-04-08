// Package service contains the SigningService, which is the application's
// core orchestrator. It subscribes to domain events, applies resilience
// policies, enforces idempotency, delegates to the injected Signer, and
// publishes result events.
//
// Dependency Inversion: SigningService depends only on interfaces. All
// concrete types are injected at bootstrap. This makes the service fully
// testable with mocks and swappable backends.
package service

import (
	"context"
	"fmt"
	"log/slog"
	"time"

	"go.opentelemetry.io/otel/attribute"
	"go.opentelemetry.io/otel/codes"
	"go.opentelemetry.io/otel/trace"

	"github.com/bons/bons-ci/pkg/sigstore/internal/domain"
	"github.com/bons/bons-ci/pkg/sigstore/internal/eventbus"
	"github.com/bons/bons-ci/pkg/sigstore/internal/idempotency"
	"github.com/bons/bons-ci/pkg/sigstore/internal/observability"
	"github.com/bons/bons-ci/pkg/sigstore/internal/resilience"
	"github.com/bons/bons-ci/pkg/sigstore/internal/signing"
)

// Config holds all injectable dependencies and tunables for SigningService.
// No field has a default value: every dependency is explicit and required.
type Config struct {
	Bus              eventbus.EventBus
	Signer           signing.Signer
	ResiliencePolicy resilience.ResiliencePolicy
	IdempotencyStore idempotency.IdempotencyStore
	Tracer           trace.Tracer
	Logger           *slog.Logger
	Metrics          *observability.Metrics

	// IdempotencyTTL is how long a signing result is cached to prevent
	// duplicate processing. Should exceed the maximum retry window.
	IdempotencyTTL time.Duration
}

// SigningService subscribes to SigningRequestedEvent and drives the full
// sign-and-attest workflow. It is the only component that knows the order
// of operations; all other components are stateless collaborators.
type SigningService struct {
	cfg Config
}

// NewSigningService validates cfg and returns a ready-to-use service.
func NewSigningService(cfg Config) (*SigningService, error) {
	if cfg.Bus == nil {
		return nil, fmt.Errorf("signing service: Bus is required")
	}
	if cfg.Signer == nil {
		return nil, fmt.Errorf("signing service: Signer is required")
	}
	if cfg.ResiliencePolicy == nil {
		return nil, fmt.Errorf("signing service: ResiliencePolicy is required")
	}
	if cfg.IdempotencyStore == nil {
		return nil, fmt.Errorf("signing service: IdempotencyStore is required")
	}
	if cfg.Logger == nil {
		cfg.Logger = slog.Default()
	}
	if cfg.IdempotencyTTL == 0 {
		cfg.IdempotencyTTL = 10 * time.Minute
	}
	return &SigningService{cfg: cfg}, nil
}

// RegisterHandlers subscribes the service to all relevant topics on the bus.
// Call this once during bootstrap after all dependencies are wired.
func (s *SigningService) RegisterHandlers() error {
	_, err := s.cfg.Bus.Subscribe(domain.EventTypeSigningRequested, s.handleSigningRequested)
	if err != nil {
		return fmt.Errorf("register signing handler: %w", err)
	}
	return nil
}

// handleSigningRequested is the core event handler.
// It enforces at-most-once semantics via the IdempotencyStore, then delegates
// to the Signer through the ResiliencePolicy.
func (s *SigningService) handleSigningRequested(ctx context.Context, env domain.Envelope) error {
	req, ok := env.Payload.(domain.SigningRequestedEvent)
	if !ok {
		return fmt.Errorf("handleSigningRequested: unexpected payload type %T", env.Payload)
	}

	log := s.cfg.Logger.With(
		"event_id", req.ID,
		"correlation_id", req.CorrelationID,
		"image_ref", req.ImageRef,
	)

	// ── Start OTel span ──────────────────────────────────────────────────────
	ctx, span := s.startSpan(ctx, "signing_service.handle_requested", req)
	defer span.End()

	// ── Idempotency gate ─────────────────────────────────────────────────────
	idempKey := idempotencyKey(req.ImageRef, req.ID)
	claimed, err := s.cfg.IdempotencyStore.TryClaim(ctx, idempKey, s.cfg.IdempotencyTTL)
	if err != nil {
		span.RecordError(err)
		return fmt.Errorf("idempotency claim: %w", err)
	}
	if !claimed {
		log.Info("duplicate signing request detected, skipping")
		s.recordIdempotencyHit("duplicate")
		return s.publishDuplicate(ctx, req)
	}
	s.recordIdempotencyHit("claimed")

	// ── Publish signing started ──────────────────────────────────────────────
	if err = s.publishStarted(ctx, req); err != nil {
		log.Warn("failed to publish signing.started event", "error", err)
		// Non-fatal: observability event failure should not block signing.
	}

	// ── Execute signing through resilience policy ────────────────────────────
	var result domain.SigningResult
	policyErr := s.cfg.ResiliencePolicy.Execute(ctx, func(execCtx context.Context) error {
		var signErr error
		result, signErr = s.cfg.Signer.Sign(execCtx, signing.SignRequest{
			ImageRef: req.ImageRef,
			KeySpec: domain.KeySpec{
				Name:      req.KeyHint,
				IsKeyless: req.KeyHint == "",
			},
			Annotations:   req.Annotations,
			AttachToRekor: true,
		})
		return signErr
	})

	// ── Handle outcome ───────────────────────────────────────────────────────
	if policyErr != nil {
		span.SetStatus(codes.Error, policyErr.Error())
		span.RecordError(policyErr)

		_ = s.cfg.IdempotencyStore.MarkFailed(ctx, idempKey, policyErr.Error())
		s.recordSigningOutcome("failed")

		return s.publishFailed(ctx, req, policyErr)
	}

	_ = s.cfg.IdempotencyStore.MarkSucceeded(ctx, idempKey, result.SignatureRef)
	s.recordSigningOutcome("succeeded")

	span.SetAttributes(
		attribute.String("signature_ref", result.SignatureRef),
		attribute.Int64("rekor_log_index", result.RekorLogIndex),
	)

	return s.publishSucceeded(ctx, req, result)
}

// --- event publishing helpers ───────────────────────────────────────────────

func (s *SigningService) publishStarted(ctx context.Context, req domain.SigningRequestedEvent) error {
	return s.cfg.Bus.Publish(ctx, domain.Envelope{
		Topic: domain.EventTypeSigningStarted,
		Payload: domain.SigningStartedEvent{
			BaseEvent: newBase(domain.EventTypeSigningStarted, req.CorrelationID),
			ImageRef:  req.ImageRef,
			WorkerID:  "signing-service",
		},
	})
}

func (s *SigningService) publishSucceeded(ctx context.Context, req domain.SigningRequestedEvent, r domain.SigningResult) error {
	s.cfg.Logger.Info("signing succeeded",
		"image_ref", r.ImageRef,
		"sig_ref", r.SignatureRef,
		"rekor_index", r.RekorLogIndex,
	)
	return s.cfg.Bus.Publish(ctx, domain.Envelope{
		Topic: domain.EventTypeSigningSucceeded,
		Payload: domain.SigningSucceededEvent{
			BaseEvent:     newBase(domain.EventTypeSigningSucceeded, req.CorrelationID),
			ImageRef:      r.ImageRef,
			SignatureRef:  r.SignatureRef,
			RekorLogIndex: r.RekorLogIndex,
			CertChain:     r.CertChain,
		},
	})
}

func (s *SigningService) publishFailed(ctx context.Context, req domain.SigningRequestedEvent, err error) error {
	s.cfg.Logger.Error("signing failed",
		"image_ref", req.ImageRef,
		"error", err,
	)
	return s.cfg.Bus.Publish(ctx, domain.Envelope{
		Topic: domain.EventTypeSigningFailed,
		Payload: domain.SigningFailedEvent{
			BaseEvent: newBase(domain.EventTypeSigningFailed, req.CorrelationID),
			ImageRef:  req.ImageRef,
			Reason:    err.Error(),
			Retryable: resilience.DefaultIsRetryable(err),
		},
	})
}

func (s *SigningService) publishDuplicate(ctx context.Context, req domain.SigningRequestedEvent) error {
	return s.cfg.Bus.Publish(ctx, domain.Envelope{
		Topic: domain.EventTypeSigningDuplicate,
		Payload: domain.SigningDuplicateEvent{
			BaseEvent:       newBase(domain.EventTypeSigningDuplicate, req.CorrelationID),
			ImageRef:        req.ImageRef,
			OriginalEventID: req.ID,
		},
	})
}

// --- observability helpers ──────────────────────────────────────────────────

func (s *SigningService) startSpan(ctx context.Context, name string, req domain.SigningRequestedEvent) (context.Context, trace.Span) {
	if s.cfg.Tracer == nil {
		return ctx, trace.SpanFromContext(ctx) // noop span
	}
	return s.cfg.Tracer.Start(ctx, name,
		trace.WithAttributes(
			attribute.String("image_ref", req.ImageRef),
			attribute.String("correlation_id", req.CorrelationID),
			attribute.String("event_id", req.ID),
		),
	)
}

func (s *SigningService) recordIdempotencyHit(result string) {
	if s.cfg.Metrics != nil {
		s.cfg.Metrics.IdempotencyHits.WithLabelValues(result).Inc()
	}
}

func (s *SigningService) recordSigningOutcome(outcome string) {
	if s.cfg.Metrics != nil {
		s.cfg.Metrics.SigningTotal.WithLabelValues("service", outcome).Inc()
	}
}

// --- helpers ────────────────────────────────────────────────────────────────

// idempotencyKey produces a deterministic key for a (imageRef, eventID) pair.
// Using both fields ensures that the same image can be re-signed with a new
// event (e.g. after key rotation) while duplicates from network retries are
// still rejected.
func idempotencyKey(imageRef, eventID string) string {
	return fmt.Sprintf("sign:%s:%s", imageRef, eventID)
}

func newBase(t domain.EventType, correlationID string) domain.BaseEvent {
	return domain.BaseEvent{
		ID:            fmt.Sprintf("evt-%d", time.Now().UnixNano()),
		CorrelationID: correlationID,
		OccurredAt:    time.Now().UTC(),
		Type:          t,
		Version:       1,
	}
}
