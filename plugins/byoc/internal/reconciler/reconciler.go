// Package reconciler implements the background loop that detects and terminates
// orphaned OCI resources — runners whose DB records exist but whose OCI instances
// have silently failed, been manually deleted, or stalled in Provisioning/Registering
// beyond a configurable timeout.
package reconciler

import (
	"context"
	"time"

	"github.com/bons/bons-ci/plugins/byoc/internal/observability"
	"github.com/bons/bons-ci/plugins/byoc/internal/provisioner"
	"github.com/bons/bons-ci/plugins/byoc/internal/store"
	"github.com/rs/zerolog"
)

// Config holds reconciler tuning parameters.
type Config struct {
	// Interval is how often the reconciliation loop runs. Default: 2 minutes.
	Interval time.Duration
	// ProvisioningTimeout is the max time a runner may stay in "provisioning"
	// before being declared orphaned. Default: 5 minutes.
	ProvisioningTimeout time.Duration
	// RegisteringTimeout is the max time a runner may stay in "registering"
	// before being declared orphaned. Default: 10 minutes.
	RegisteringTimeout time.Duration
}

// Reconciler detects and terminates orphaned OCI compute resources.
type Reconciler struct {
	store       store.Store
	provisioner provisioner.Provisioner
	metrics     *observability.Metrics
	logger      zerolog.Logger
	cfg         Config
}

// New constructs a Reconciler.
func New(s store.Store, p provisioner.Provisioner, m *observability.Metrics, cfg Config, logger zerolog.Logger) *Reconciler {
	if cfg.Interval == 0 {
		cfg.Interval = 2 * time.Minute
	}
	if cfg.ProvisioningTimeout == 0 {
		cfg.ProvisioningTimeout = 5 * time.Minute
	}
	if cfg.RegisteringTimeout == 0 {
		cfg.RegisteringTimeout = 10 * time.Minute
	}
	return &Reconciler{
		store:       s,
		provisioner: p,
		metrics:     m,
		logger:      logger.With().Str("component", "reconciler").Logger(),
		cfg:         cfg,
	}
}

// Run starts the reconciliation loop. It blocks until ctx is cancelled.
func (r *Reconciler) Run(ctx context.Context) {
	ticker := time.NewTicker(r.cfg.Interval)
	defer ticker.Stop()

	r.logger.Info().Dur("interval", r.cfg.Interval).Msg("reconciler started")

	for {
		select {
		case <-ctx.Done():
			r.logger.Info().Msg("reconciler stopping")
			return
		case <-ticker.C:
			r.reconcile(ctx)
		}
	}
}

func (r *Reconciler) reconcile(ctx context.Context) {
	r.logger.Debug().Msg("reconciliation cycle started")

	r.cleanStuckProvisioning(ctx)
	r.cleanStuckRegistering(ctx)
	r.cleanOCIOrphans(ctx)

	r.logger.Debug().Msg("reconciliation cycle complete")
}

// cleanStuckProvisioning terminates runners that have been in "provisioning"
// beyond ProvisioningTimeout — likely the OCI instance failed to launch.
func (r *Reconciler) cleanStuckProvisioning(ctx context.Context) {
	status := store.RunnerStatusProvisioning
	runners, err := r.store.ListRunners(ctx, store.RunnerFilter{Status: &status, Limit: 200})
	if err != nil {
		r.logger.Error().Err(err).Msg("list provisioning runners")
		return
	}

	cutoff := time.Now().Add(-r.cfg.ProvisioningTimeout)
	for _, runner := range runners {
		if runner.ProvisionedAt.Before(cutoff) {
			r.logger.Warn().
				Str("runner_id", runner.ID).
				Str("tenant_id", runner.TenantID).
				Dur("stuck_for", time.Since(runner.ProvisionedAt)).
				Msg("runner stuck in provisioning — marking terminated")

			r.terminateOrphan(ctx, runner, "stuck_provisioning")
		}
	}
}

// cleanStuckRegistering terminates runners that have been in "registering"
// beyond RegisteringTimeout — likely the cloud-init script failed.
func (r *Reconciler) cleanStuckRegistering(ctx context.Context) {
	status := store.RunnerStatusRegistering
	runners, err := r.store.ListRunners(ctx, store.RunnerFilter{Status: &status, Limit: 200})
	if err != nil {
		r.logger.Error().Err(err).Msg("list registering runners")
		return
	}

	cutoff := time.Now().Add(-r.cfg.RegisteringTimeout)
	for _, runner := range runners {
		if runner.ProvisionedAt.Before(cutoff) {
			r.logger.Warn().
				Str("runner_id", runner.ID).
				Str("tenant_id", runner.TenantID).
				Msg("runner stuck in registering — marking terminated")

			r.terminateOrphan(ctx, runner, "stuck_registering")
		}
	}
}

// cleanOCIOrphans checks OCI for instances tagged as BYOC runners that
// do not have an active record in the DB (race between DB and OCI API).
// This is a best-effort sweep — errors are logged but do not block the loop.
func (r *Reconciler) cleanOCIOrphans(ctx context.Context) {
	// This would use the OCI Search Service or ListInstances with freeformTag filter.
	// For brevity the OCI query is stubbed; the shape of the full implementation is:
	//   ociInstances := r.provisioner.ListByTag(ctx, "byoc:managed_by", "byoc-oci-runners")
	//   for each ociInstance, check if runner ID in DB; if not → Terminate
	r.logger.Debug().Msg("OCI orphan sweep (stub — implement with OCI Search API)")
}

// terminateOrphan best-effort terminates both the OCI resource and the DB record.
func (r *Reconciler) terminateOrphan(ctx context.Context, runner *store.Runner, reason string) {
	if runner.OCIInstanceID != "" {
		termCtx, cancel := context.WithTimeout(ctx, 30*time.Second)
		defer cancel()
		if err := r.provisioner.Terminate(termCtx, runner.OCIInstanceID); err != nil {
			r.logger.Error().
				Str("runner_id", runner.ID).
				Str("oci_instance_id", runner.OCIInstanceID).
				Err(err).
				Msg("reconciler: OCI terminate failed")
		}
	}

	now := time.Now()
	if err := r.store.UpdateRunnerStatus(ctx, runner.ID, store.RunnerStatusTerminated, store.RunnerUpdateOpts{
		TerminatedAt: &now,
	}); err != nil {
		r.logger.Error().Str("runner_id", runner.ID).Err(err).Msg("reconciler: mark terminated in DB failed")
	}

	r.metrics.RunnerTerminations.WithLabelValues(runner.TenantID, reason).Inc()
}
