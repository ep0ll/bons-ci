// Package orchestrator connects webhook events to the runner lifecycle.
// It owns the job dispatch queue and acts on scaler decisions to provision,
// assign, or enqueue runners. The orchestrator is the main "coordinator"
// in the hexagonal architecture — it talks to scaler, runner, and store.
package orchestrator

import (
	"context"
	"fmt"
	"sync"
	"time"

	"github.com/bons/bons-ci/plugins/byoc/internal/observability"
	"github.com/bons/bons-ci/plugins/byoc/internal/runner"
	"github.com/bons/bons-ci/plugins/byoc/internal/scaler"
	"github.com/bons/bons-ci/plugins/byoc/internal/store"
	"github.com/rs/zerolog"
)

// jobEvent is an internal representation of a workflow_job.queued event
// that has been validated and enriched with tenant context.
type jobEvent struct {
	Tenant       *store.Tenant
	JobID        int64
	RunnerLabels []string
	ReceivedAt   time.Time
}

// waitingJob is a job that could not be dispatched immediately (runner limit reached)
// and is waiting in the per-tenant queue for a runner to become free.
type waitingJob struct {
	jobEvent
	EnqueuedAt time.Time
}

// Orchestrator coordinates runner provisioning in response to GitHub job events.
// It is safe for concurrent use from multiple webhook goroutines.
type Orchestrator struct {
	store         store.Store
	runnerService *runner.Service
	scaler        *scaler.Scaler
	metrics       *observability.Metrics
	logger        zerolog.Logger

	// jobCh receives enriched job events from the webhook handler.
	// Buffered to absorb bursts without blocking HTTP handlers.
	jobCh chan jobEvent

	// waitQueues holds per-tenant FIFO queues of jobs waiting for a free runner.
	// Access is serialised through the single dispatch goroutine.
	mu         sync.Mutex
	waitQueues map[string][]waitingJob

	// completedCh receives tenant IDs when a runner becomes free (job completed),
	// so the dispatch loop can dequeue waiting jobs.
	completedCh chan string
}

// Config holds orchestrator tuning parameters.
type Config struct {
	// JobQueueBuffer is the size of the buffered job channel. Default: 512.
	JobQueueBuffer int
	// WorkerCount is the number of concurrent dispatch workers. Default: 16.
	WorkerCount int
	// ScaleDownInterval controls how often the idle-runner cleanup runs. Default: 30 s.
	ScaleDownInterval time.Duration
}

// New constructs an Orchestrator. Call Run to start the dispatch loop.
func New(
	s store.Store,
	rs *runner.Service,
	sc *scaler.Scaler,
	m *observability.Metrics,
	cfg Config,
	logger zerolog.Logger,
) *Orchestrator {
	if cfg.JobQueueBuffer == 0 {
		cfg.JobQueueBuffer = 512
	}
	if cfg.WorkerCount == 0 {
		cfg.WorkerCount = 16
	}
	if cfg.ScaleDownInterval == 0 {
		cfg.ScaleDownInterval = 30 * time.Second
	}
	return &Orchestrator{
		store:         s,
		runnerService: rs,
		scaler:        sc,
		metrics:       m,
		logger:        logger.With().Str("component", "orchestrator").Logger(),
		jobCh:         make(chan jobEvent, cfg.JobQueueBuffer),
		waitQueues:    make(map[string][]waitingJob),
		completedCh:   make(chan string, 128),
	}
}

// EnqueueJob is called by the webhook handler for every workflow_job.queued event.
// It is non-blocking — if the channel is full it drops the event and logs a warning.
// The caller should respond 202 Accepted regardless.
func (o *Orchestrator) EnqueueJob(tenant *store.Tenant, jobID int64, labels []string) {
	evt := jobEvent{
		Tenant:       tenant,
		JobID:        jobID,
		RunnerLabels: labels,
		ReceivedAt:   time.Now(),
	}
	select {
	case o.jobCh <- evt:
	default:
		o.logger.Warn().
			Str("tenant_id", tenant.ID).
			Int64("job_id", jobID).
			Msg("job channel full — dropping event; consider increasing JobQueueBuffer")
	}
}

// NotifyJobComplete is called when a workflow_job.completed event arrives.
// It signals the dispatch loop to dequeue waiting jobs for that tenant.
func (o *Orchestrator) NotifyJobComplete(tenantID string) {
	select {
	case o.completedCh <- tenantID:
	default:
	}
}

// Run starts the dispatch workers and the scale-down loop.
// It blocks until ctx is cancelled, then drains gracefully.
func (o *Orchestrator) Run(ctx context.Context) {
	var wg sync.WaitGroup

	// Launch N concurrent dispatch workers.
	for i := 0; i < 16; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			o.dispatchWorker(ctx)
		}()
	}

	// Dequeue waiting jobs when a runner becomes free.
	wg.Add(1)
	go func() {
		defer wg.Done()
		o.dequeueLoop(ctx)
	}()

	// Periodic scale-down for idle runners.
	wg.Add(1)
	go func() {
		defer wg.Done()
		o.scaleDownLoop(ctx, 30*time.Second)
	}()

	wg.Wait()
	o.logger.Info().Msg("orchestrator shut down")
}

// dispatchWorker reads from jobCh and acts on scaler decisions.
func (o *Orchestrator) dispatchWorker(ctx context.Context) {
	for {
		select {
		case <-ctx.Done():
			return
		case evt := <-o.jobCh:
			o.dispatch(ctx, evt)
		}
	}
}

func (o *Orchestrator) dispatch(ctx context.Context, evt jobEvent) {
	log := o.logger.With().
		Str("tenant_id", evt.Tenant.ID).
		Int64("job_id", evt.JobID).
		Logger()

	result, err := o.scaler.ScaleUp(ctx, evt.Tenant, evt.JobID)
	if err != nil {
		log.Error().Err(err).Msg("scaler.ScaleUp failed")
		return
	}

	switch result.Decision {
	case scaler.DecisionDuplicate:
		log.Debug().Msg("duplicate job event — no action")

	case scaler.DecisionAssignIdle:
		// An idle runner is available — mark it busy and let GitHub route the job.
		if err := o.runnerService.MarkBusy(ctx, result.IdleRunnerID); err != nil {
			log.Error().Str("runner_id", result.IdleRunnerID).Err(err).Msg("mark runner busy failed")
		} else {
			log.Info().Str("runner_id", result.IdleRunnerID).Msg("assigned idle runner to job")
		}

	case scaler.DecisionProvision:
		provCtx, cancel := context.WithTimeout(ctx, 2*time.Minute)
		defer cancel()
		r, err := o.runnerService.CreateAndProvision(provCtx, evt.Tenant, evt.JobID)
		if err != nil {
			log.Error().Err(err).Msg("provision runner failed")
			return
		}
		log.Info().Str("runner_id", r.ID).Msg("runner provisioned and registering")

	case scaler.DecisionEnqueue:
		o.mu.Lock()
		o.waitQueues[evt.Tenant.ID] = append(o.waitQueues[evt.Tenant.ID], waitingJob{
			jobEvent:   evt,
			EnqueuedAt: time.Now(),
		})
		o.mu.Unlock()
		log.Info().Msg("job enqueued in tenant wait queue")

	case scaler.DecisionRateLimited:
		// Re-queue after a short back-off so we don't lose the event.
		time.AfterFunc(10*time.Second, func() {
			o.EnqueueJob(evt.Tenant, evt.JobID, evt.RunnerLabels)
		})
		log.Warn().Msg("rate limited — retrying job dispatch in 10 s")
	}
}

// dequeueLoop drains the per-tenant wait queue when a completion event arrives.
func (o *Orchestrator) dequeueLoop(ctx context.Context) {
	for {
		select {
		case <-ctx.Done():
			return
		case tenantID := <-o.completedCh:
			o.mu.Lock()
			queue := o.waitQueues[tenantID]
			if len(queue) == 0 {
				o.mu.Unlock()
				continue
			}
			next := queue[0]
			o.waitQueues[tenantID] = queue[1:]
			o.mu.Unlock()

			o.metrics.JobQueueDepth.WithLabelValues(tenantID).Dec()
			o.logger.Info().
				Str("tenant_id", tenantID).
				Int64("job_id", next.JobID).
				Dur("wait_time", time.Since(next.EnqueuedAt)).
				Msg("dequeuing waiting job")

			// Re-dispatch (the runner limit may now have room).
			o.EnqueueJob(next.Tenant, next.JobID, next.RunnerLabels)
		}
	}
}

// scaleDownLoop periodically checks for idle runners past their IdleTimeoutSec.
func (o *Orchestrator) scaleDownLoop(ctx context.Context, interval time.Duration) {
	ticker := time.NewTicker(interval)
	defer ticker.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case <-ticker.C:
			o.runScaleDown(ctx)
		}
	}
}

func (o *Orchestrator) runScaleDown(ctx context.Context) {
	activeStatus := store.TenantStatusActive
	tenants, err := o.store.ListTenants(ctx, store.TenantFilter{Status: &activeStatus})
	if err != nil {
		o.logger.Error().Err(err).Msg("list tenants for scale-down")
		return
	}
	for _, t := range tenants {
		toTerminate, err := o.scaler.ScaleDown(ctx, t)
		if err != nil {
			o.logger.Error().Str("tenant_id", t.ID).Err(err).Msg("scaler.ScaleDown failed")
			continue
		}
		for _, runnerID := range toTerminate {
			if err := o.runnerService.Terminate(ctx, runnerID, "idle_timeout"); err != nil {
				o.logger.Error().Str("runner_id", runnerID).Err(err).Msg("scale-down terminate failed")
			}
		}
	}
}

// HandleJobCompleted processes a workflow_job.completed event:
// terminates the runner and notifies the dequeue loop.
func (o *Orchestrator) HandleJobCompleted(ctx context.Context, tenantID string, runnerID string) error {
	if runnerID == "" {
		return fmt.Errorf("HandleJobCompleted: empty runner_id for tenant %s", tenantID)
	}
	if err := o.runnerService.Terminate(ctx, runnerID, "job_completed"); err != nil {
		return fmt.Errorf("terminate runner after job complete: %w", err)
	}
	o.NotifyJobComplete(tenantID)
	return nil
}
