// Package scaler implements the autoscaling decision logic for the BYOC platform.
// It combines a per-tenant token bucket rate limiter with the scale-up/scale-down
// algorithm described in the SKILL.md §6.
package scaler

import (
	"context"
	"fmt"
	"sync"
	"time"

	"github.com/bons/bons-ci/plugins/byoc/internal/observability"
	"github.com/bons/bons-ci/plugins/byoc/internal/store"
	"github.com/rs/zerolog"
	"golang.org/x/time/rate"
)

// Decision represents the scaler's recommendation for a given job event.
type Decision int

const (
	// DecisionProvision means a new runner should be provisioned immediately.
	DecisionProvision Decision = iota
	// DecisionAssignIdle means an existing idle runner should be assigned.
	DecisionAssignIdle
	// DecisionEnqueue means the job should be queued (runner limit reached).
	DecisionEnqueue
	// DecisionRateLimited means the provision call should be deferred (rate limit hit).
	DecisionRateLimited
	// DecisionDuplicate means the event was already processed (idempotency).
	DecisionDuplicate
)

// ScaleUpResult is returned by ScaleUp and conveys both the decision and,
// when DecisionAssignIdle, the runner that was selected.
type ScaleUpResult struct {
	Decision     Decision
	IdleRunnerID string // set only for DecisionAssignIdle
}

// Config holds scaler configuration defaults (overridden per-tenant where applicable).
type Config struct {
	// DefaultProvisionRate is the token bucket refill rate (provisions per second).
	// Default corresponds to 10 provisions/minute.
	DefaultProvisionRate rate.Limit
	// DefaultProvisionBurst is the token bucket burst size.
	DefaultProvisionBurst int
	// IdempotencyTTL is how long idempotency locks are retained.
	IdempotencyTTL time.Duration
}

// Scaler makes scaling decisions for incoming GitHub workflow_job events.
type Scaler struct {
	store   store.Store
	metrics *observability.Metrics
	logger  zerolog.Logger
	cfg     Config

	// rateLimiters holds one token bucket per tenant, created lazily.
	mu           sync.Mutex
	rateLimiters map[string]*rate.Limiter
}

// New constructs a Scaler. Call with sensible defaults for production:
//
//	Config{DefaultProvisionRate: rate.Every(6*time.Second), DefaultProvisionBurst: 5}
func New(s store.Store, m *observability.Metrics, cfg Config, logger zerolog.Logger) *Scaler {
	if cfg.DefaultProvisionRate == 0 {
		// 10 provisions per minute
		cfg.DefaultProvisionRate = rate.Every(6 * time.Second)
	}
	if cfg.DefaultProvisionBurst == 0 {
		cfg.DefaultProvisionBurst = 5
	}
	if cfg.IdempotencyTTL == 0 {
		cfg.IdempotencyTTL = 24 * time.Hour
	}
	return &Scaler{
		store:        s,
		metrics:      m,
		logger:       logger.With().Str("component", "scaler").Logger(),
		cfg:          cfg,
		rateLimiters: make(map[string]*rate.Limiter),
	}
}

// ScaleUp evaluates a workflow_job.queued event and returns a scaling decision.
// The caller (orchestrator) acts on the decision — the scaler does not provision.
//
// Algorithm (from SKILL.md §6):
//  1. Acquire idempotency lock on job_id; return DecisionDuplicate if already held.
//  2. Check tenant is active.
//  3. If idle runners exist → DecisionAssignIdle.
//  4. If active_count >= MaxRunners → DecisionEnqueue.
//  5. Apply rate limiter → DecisionRateLimited if exhausted.
//  6. Otherwise → DecisionProvision.
func (sc *Scaler) ScaleUp(ctx context.Context, tenant *store.Tenant, jobID int64) (ScaleUpResult, error) {
	// Step 1: idempotency check.
	lockKey := fmt.Sprintf("job:%s:%d", tenant.ID, jobID)
	acquired, err := sc.store.AcquireIdempotencyLock(ctx, lockKey, sc.cfg.IdempotencyTTL)
	if err != nil {
		return ScaleUpResult{}, fmt.Errorf("acquire idempotency lock: %w", err)
	}
	if !acquired {
		sc.logger.Debug().Str("tenant_id", tenant.ID).Int64("job_id", jobID).Msg("duplicate job event — skipping")
		return ScaleUpResult{Decision: DecisionDuplicate}, nil
	}

	// Step 2: tenant must be active.
	if tenant.Status != store.TenantStatusActive {
		return ScaleUpResult{}, fmt.Errorf("tenant %s is not active (status: %s)", tenant.ID, tenant.Status)
	}

	// Step 3: check for idle runners.
	idleStatus := store.RunnerStatusIdle
	idle, err := sc.store.ListRunners(ctx, store.RunnerFilter{
		TenantID: &tenant.ID,
		Status:   &idleStatus,
		Limit:    1,
	})
	if err != nil {
		return ScaleUpResult{}, fmt.Errorf("list idle runners: %w", err)
	}
	if len(idle) > 0 {
		sc.logger.Info().Str("tenant_id", tenant.ID).Str("runner_id", idle[0].ID).Msg("assigning idle runner to job")
		return ScaleUpResult{Decision: DecisionAssignIdle, IdleRunnerID: idle[0].ID}, nil
	}

	// Step 4: enforce MaxRunners ceiling.
	activeCount, err := sc.countActiveRunners(ctx, tenant.ID)
	if err != nil {
		return ScaleUpResult{}, fmt.Errorf("count active runners: %w", err)
	}
	if activeCount >= int64(tenant.MaxRunners) {
		sc.logger.Warn().
			Str("tenant_id", tenant.ID).
			Int64("active", activeCount).
			Int("max", tenant.MaxRunners).
			Msg("runner limit reached — enqueueing job")
		sc.metrics.JobQueueDepth.WithLabelValues(tenant.ID).Inc()
		return ScaleUpResult{Decision: DecisionEnqueue}, nil
	}

	// Step 5: rate limit.
	limiter := sc.limiterFor(tenant.ID)
	if !limiter.Allow() {
		sc.logger.Warn().Str("tenant_id", tenant.ID).Msg("provision rate limit exceeded")
		sc.metrics.RateLimitHits.WithLabelValues(tenant.ID).Inc()
		return ScaleUpResult{Decision: DecisionRateLimited}, nil
	}

	// Step 6: all clear — provision a new runner.
	sc.logger.Info().
		Str("tenant_id", tenant.ID).
		Int64("job_id", jobID).
		Int64("active_runners", activeCount).
		Msg("scaling up: provisioning new runner")

	return ScaleUpResult{Decision: DecisionProvision}, nil
}

// ScaleDown evaluates whether idle runners should be terminated based on
// the tenant's IdleTimeoutSec policy. Call this on a periodic tick (e.g. 30 s).
// Returns the IDs of runners that should be terminated.
func (sc *Scaler) ScaleDown(ctx context.Context, tenant *store.Tenant) ([]string, error) {
	idleStatus := store.RunnerStatusIdle
	idleRunners, err := sc.store.ListRunners(ctx, store.RunnerFilter{
		TenantID: &tenant.ID,
		Status:   &idleStatus,
	})
	if err != nil {
		return nil, fmt.Errorf("list idle runners for scale-down: %w", err)
	}

	cutoff := time.Now().Add(-time.Duration(tenant.IdleTimeoutSec) * time.Second)
	var toTerminate []string
	for _, r := range idleRunners {
		if r.IdleSince != nil && r.IdleSince.Before(cutoff) {
			toTerminate = append(toTerminate, r.ID)
		}
	}

	if len(toTerminate) > 0 {
		sc.logger.Info().
			Str("tenant_id", tenant.ID).
			Int("count", len(toTerminate)).
			Msg("scaling down: terminating idle runners past idle_timeout")
	}

	return toTerminate, nil
}

// countActiveRunners returns runners in provisioning | registering | idle | busy states.
func (sc *Scaler) countActiveRunners(ctx context.Context, tenantID string) (int64, error) {
	activeStatuses := []store.RunnerStatus{
		store.RunnerStatusProvisioning,
		store.RunnerStatusRegistering,
		store.RunnerStatusIdle,
		store.RunnerStatusBusy,
	}
	var total int64
	for _, s := range activeStatuses {
		status := s
		count, err := sc.store.CountRunners(ctx, store.RunnerFilter{
			TenantID: &tenantID,
			Status:   &status,
		})
		if err != nil {
			return 0, err
		}
		total += count
	}
	return total, nil
}

// limiterFor returns (or lazily creates) a token bucket rate limiter for the tenant.
func (sc *Scaler) limiterFor(tenantID string) *rate.Limiter {
	sc.mu.Lock()
	defer sc.mu.Unlock()
	if l, ok := sc.rateLimiters[tenantID]; ok {
		return l
	}
	l := rate.NewLimiter(sc.cfg.DefaultProvisionRate, sc.cfg.DefaultProvisionBurst)
	sc.rateLimiters[tenantID] = l
	return l
}
