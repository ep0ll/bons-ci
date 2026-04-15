// Package runner implements the runner lifecycle finite state machine and
// orchestrates the sequence: Provision → Register → Idle/Busy → Terminate.
package runner

import (
	"context"
	"fmt"
	"time"

	"github.com/bons/bons-ci/plugins/byoc/internal/github"
	"github.com/bons/bons-ci/plugins/byoc/internal/observability"
	"github.com/bons/bons-ci/plugins/byoc/internal/provisioner"
	"github.com/bons/bons-ci/plugins/byoc/internal/store"
	"github.com/google/uuid"
	"github.com/rs/zerolog"
)

// validTransitions defines the legal state machine edges.
// Any transition not in this map is illegal and returns ErrInvalidTransition.
var validTransitions = map[store.RunnerStatus][]store.RunnerStatus{
	store.RunnerStatusProvisioning: {store.RunnerStatusRegistering, store.RunnerStatusTerminated},
	store.RunnerStatusRegistering:  {store.RunnerStatusIdle, store.RunnerStatusTerminated},
	store.RunnerStatusIdle:         {store.RunnerStatusBusy, store.RunnerStatusTerminating},
	store.RunnerStatusBusy:         {store.RunnerStatusTerminating, store.RunnerStatusIdle},
	store.RunnerStatusTerminating:  {store.RunnerStatusTerminated},
	store.RunnerStatusTerminated:   {}, // terminal state
}

// ErrInvalidTransition is returned when a state machine transition is illegal.
var ErrInvalidTransition = fmt.Errorf("invalid runner status transition")

// Service manages the runner lifecycle. It is the single point of truth for
// runner state transitions — all other packages call Service methods rather
// than mutating the store directly.
type Service struct {
	store       store.Store
	provisioner provisioner.Provisioner
	github      github.Client
	metrics     *observability.Metrics
	logger      zerolog.Logger
}

// NewService constructs a runner.Service via constructor injection.
func NewService(
	s store.Store,
	p provisioner.Provisioner,
	gh github.Client,
	m *observability.Metrics,
	logger zerolog.Logger,
) *Service {
	return &Service{
		store:       s,
		provisioner: p,
		github:      gh,
		metrics:     m,
		logger:      logger.With().Str("component", "runner_service").Logger(),
	}
}

// CreateAndProvision creates a runner DB record and launches the OCI compute resource.
// It returns the newly created runner ID. The runner transitions:
//
//	(new) → Provisioning → Registering (via cloud-init on the instance)
//
// This method returns quickly; the instance registers itself asynchronously.
func (s *Service) CreateAndProvision(ctx context.Context, tenant *store.Tenant, jobID int64) (*store.Runner, error) {
	provisionStart := time.Now()

	// Fetch a GitHub runner registration token — valid for ~1h.
	regToken, err := s.github.CreateRegistrationToken(ctx, tenant.ID)
	if err != nil {
		return nil, fmt.Errorf("create registration token: %w", err)
	}

	// Construct labels: tenant-defined + system labels.
	labels := append([]string{"oci", "ephemeral", tenant.RunnerShape}, tenant.RunnerLabels...)

	runner := &store.Runner{
		ID:              uuid.New().String(),
		TenantID:        tenant.ID,
		JobID:           jobID,
		Status:          store.RunnerStatusProvisioning,
		Labels:          labels,
		ProvisionerType: s.provisioner.Type(),
		ProvisionedAt:   time.Now(),
		CreatedAt:       time.Now(),
		UpdatedAt:       time.Now(),
	}

	if err := s.store.CreateRunner(ctx, runner); err != nil {
		return nil, fmt.Errorf("create runner record: %w", err)
	}

	s.logger.Info().
		Str("tenant_id", tenant.ID).
		Str("runner_id", runner.ID).
		Int64("job_id", jobID).
		Msg("provisioning runner")

	spec := provisioner.RunnerSpec{
		TenantID:          tenant.ID,
		RunnerID:          runner.ID,
		RegistrationToken: regToken.Token,
		GitHubOrgName:     tenant.GitHubOrgName,
		CompartmentID:     tenant.OCICompartmentID,
		SubnetID:          tenant.OCISubnetID,
		Shape:             tenant.RunnerShape,
		OCPUs:             tenant.RunnerOCPUs,
		MemoryGB:          tenant.RunnerMemoryGB,
		Labels:            labels,
		Ephemeral:         true,
	}

	ociID, err := s.provisioner.Provision(ctx, spec)
	if err != nil {
		// Mark runner as terminated so the reconciler does not wait for it.
		_ = s.transition(ctx, runner.ID, store.RunnerStatusTerminated, store.RunnerUpdateOpts{})
		return nil, fmt.Errorf("provision OCI resource: %w", err)
	}

	now := time.Now()
	if err := s.transition(ctx, runner.ID, store.RunnerStatusRegistering, store.RunnerUpdateOpts{
		OCIInstanceID: &ociID,
		RegisteredAt:  &now,
	}); err != nil {
		return nil, err
	}

	runner.OCIInstanceID = ociID
	runner.Status = store.RunnerStatusRegistering

	// Record provision latency from webhook receipt to OCI instance launched.
	s.metrics.ProvisionLatency.WithLabelValues(tenant.ID, s.provisioner.Type()).
		Observe(time.Since(provisionStart).Seconds())
	s.metrics.RunnersActive.WithLabelValues(tenant.ID).Inc()

	return runner, nil
}

// MarkIdle transitions a runner from Registering → Idle (called by the runner
// self-registration webhook or a polling reconciler once GitHub confirms registration).
func (s *Service) MarkIdle(ctx context.Context, runnerID string, githubRunnerID int64) error {
	now := time.Now()
	if err := s.transition(ctx, runnerID, store.RunnerStatusIdle, store.RunnerUpdateOpts{
		GitHubRunnerID: &githubRunnerID,
		IdleSince:      &now,
	}); err != nil {
		return err
	}
	r, err := s.store.GetRunner(ctx, runnerID)
	if err == nil {
		s.metrics.RunnersIdle.WithLabelValues(r.TenantID).Inc()
	}
	return nil
}

// MarkBusy transitions a runner from Idle → Busy when GitHub assigns a job.
func (s *Service) MarkBusy(ctx context.Context, runnerID string) error {
	if err := s.transition(ctx, runnerID, store.RunnerStatusBusy, store.RunnerUpdateOpts{}); err != nil {
		return err
	}
	r, err := s.store.GetRunner(ctx, runnerID)
	if err == nil {
		s.metrics.RunnersIdle.WithLabelValues(r.TenantID).Dec()
	}
	return nil
}

// Terminate performs a graceful runner shutdown:
//  1. Transition → Terminating.
//  2. Best-effort deregister from GitHub (timeout: 10 s).
//  3. Terminate the OCI compute resource.
//  4. Transition → Terminated.
func (s *Service) Terminate(ctx context.Context, runnerID, reason string) error {
	r, err := s.store.GetRunner(ctx, runnerID)
	if err != nil {
		return fmt.Errorf("get runner before terminate: %w", err)
	}

	if r.Status == store.RunnerStatusTerminated {
		return nil // idempotent
	}

	if err := s.transition(ctx, runnerID, store.RunnerStatusTerminating, store.RunnerUpdateOpts{}); err != nil {
		return err
	}

	// Best-effort GitHub deregistration (10 s timeout).
	deregCtx, cancel := context.WithTimeout(ctx, 10*time.Second)
	defer cancel()
	if r.GitHubRunnerID > 0 {
		if err := s.github.RemoveRunner(deregCtx, r.TenantID, r.GitHubRunnerID); err != nil {
			s.logger.Warn().
				Str("runner_id", runnerID).
				Int64("github_runner_id", r.GitHubRunnerID).
				Err(err).
				Msg("GitHub deregistration failed (non-fatal)")
		}
	}

	// Terminate OCI resource.
	if r.OCIInstanceID != "" {
		if err := s.provisioner.Terminate(ctx, r.OCIInstanceID); err != nil {
			s.logger.Error().
				Str("runner_id", runnerID).
				Str("oci_instance_id", r.OCIInstanceID).
				Err(err).
				Msg("OCI termination failed")
		}
	}

	now := time.Now()
	if err := s.transition(ctx, runnerID, store.RunnerStatusTerminated, store.RunnerUpdateOpts{
		TerminatedAt: &now,
	}); err != nil {
		return err
	}

	s.metrics.RunnersActive.WithLabelValues(r.TenantID).Dec()
	s.metrics.RunnerTerminations.WithLabelValues(r.TenantID, reason).Inc()

	s.logger.Info().
		Str("runner_id", runnerID).
		Str("tenant_id", r.TenantID).
		Str("reason", reason).
		Msg("runner terminated")

	return nil
}

// transition validates the FSM edge and persists the new status.
func (s *Service) transition(ctx context.Context, runnerID string, to store.RunnerStatus, opts store.RunnerUpdateOpts) error {
	r, err := s.store.GetRunner(ctx, runnerID)
	if err != nil {
		return fmt.Errorf("get runner for transition: %w", err)
	}

	if !isValidTransition(r.Status, to) {
		return fmt.Errorf("%w: %s → %s", ErrInvalidTransition, r.Status, to)
	}

	if err := s.store.UpdateRunnerStatus(ctx, runnerID, to, opts); err != nil {
		return fmt.Errorf("persist transition %s → %s: %w", r.Status, to, err)
	}

	s.logger.Debug().
		Str("runner_id", runnerID).
		Str("from", string(r.Status)).
		Str("to", string(to)).
		Msg("runner state transition")

	return nil
}

func isValidTransition(from, to store.RunnerStatus) bool {
	targets, ok := validTransitions[from]
	if !ok {
		return false
	}
	for _, t := range targets {
		if t == to {
			return true
		}
	}
	return false
}
