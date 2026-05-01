package fanwatch

import (
	"context"
	"fmt"
	"path/filepath"
	"runtime"
	"sync"
	"sync/atomic"
)

// ─────────────────────────────────────────────────────────────────────────────
// PipelineResult — summary of a completed pipeline run
// ─────────────────────────────────────────────────────────────────────────────

// PipelineResult summarises a completed pipeline run.
type PipelineResult struct {
	// Received is the total number of raw events read from the input channel.
	Received int64
	// Filtered is the count of events dropped by the filter stage.
	Filtered int64
	// Handled is the count of events successfully delivered to the handler.
	Handled int64
	// Errors is the count of transform or handler errors encountered.
	Errors int64
}

// ─────────────────────────────────────────────────────────────────────────────
// Pipeline — the central event processing engine
// ─────────────────────────────────────────────────────────────────────────────

// Pipeline connects a [Watcher] to a [Handler] through a chain of [Filter]s
// and [Transformer]s.
//
// Topology:
//
//	RawEvent channel
//	    │
//	    ▼
//	toEnriched (path split, WatcherID)
//	    │
//	    ▼
//	Filter stage (short-circuit AND — drop on first rejection)
//	    │
//	    ▼
//	Worker pool ──► Transform stage ──► Handler
//	    │
//	    ▼
//	Error channel (non-fatal errors forwarded to caller)
//
// Run or RunSync are the two entry points. Both stop when ctx is cancelled
// or the input channel is closed.
type Pipeline struct {
	filters      AllFilters
	transformers ChainTransformer
	handler      Handler
	middlewares  []Middleware
	cfg          pipelineConfig
}

// pipelineConfig holds tunable Pipeline parameters.
type pipelineConfig struct {
	workers    int
	errBufSize int
}

func defaultPipelineConfig() pipelineConfig {
	return pipelineConfig{
		workers:    runtime.NumCPU(),
		errBufSize: 64,
	}
}

// NewPipeline constructs a [Pipeline] with the provided options.
// Middlewares are applied after the handler is set; they wrap in registration
// order so the last-registered middleware is the outermost wrapper.
func NewPipeline(opts ...PipelineOption) *Pipeline {
	cfg := defaultPipelineConfig()
	p := &Pipeline{
		handler: NoopHandler{},
		cfg:     cfg,
	}
	for _, o := range opts {
		o(p)
	}
	for i := len(p.middlewares) - 1; i >= 0; i-- {
		p.handler = p.middlewares[i].Wrap(p.handler)
	}
	return p
}

// RunSync runs the pipeline to completion, blocking until ctx is cancelled or
// in is closed. Non-fatal errors are forwarded to onError (may be nil).
// Returns a [PipelineResult] with final counters.
//
// This is the primary entry point for most use cases.
func (p *Pipeline) RunSync(ctx context.Context, in <-chan *RawEvent, onError func(error)) PipelineResult {
	errCh := make(chan error, p.cfg.errBufSize)

	var (
		received atomic.Int64
		filtered atomic.Int64
		handled  atomic.Int64
		errCount atomic.Int64
	)

	addErr := func(err error) {
		if err == nil {
			return
		}
		errCount.Add(1)
		select {
		case errCh <- err:
		default:
			// Buffer full — drop to avoid blocking worker goroutines.
			// Increase WithErrorBufferSize if this occurs frequently.
		}
	}

	// Start error consumer before the event producer so no errors are missed.
	var errWg sync.WaitGroup
	errWg.Add(1)
	go func() {
		defer errWg.Done()
		for err := range errCh {
			if onError != nil {
				onError(err)
			}
		}
	}()

	sem := make(chan struct{}, p.numWorkers())
	var wg sync.WaitGroup

	for {
		select {
		case raw, ok := <-in:
			if !ok {
				goto done
			}
			if ctx.Err() != nil {
				// Context cancelled — drain the channel without processing.
				continue
			}

			received.Add(1)
			enriched := p.toEnriched(raw)

			if !p.passesFilters(ctx, enriched) {
				filtered.Add(1)
				continue
			}

			// Acquire a worker slot; respect context cancellation.
			select {
			case sem <- struct{}{}:
			case <-ctx.Done():
				continue
			}

			wg.Add(1)
			go func(e *EnrichedEvent) {
				defer wg.Done()
				defer func() { <-sem }()

				if err := p.applyTransformers(ctx, e); err != nil {
					// Transform errors are non-fatal — the event may be partially
					// enriched but we still attempt to deliver it.
					addErr(fmt.Errorf("pipeline: transform %q: %w", e.Path, err))
				}

				if err := p.handler.Handle(ctx, e); err != nil {
					addErr(fmt.Errorf("pipeline: handle %q: %w", e.Path, err))
					return
				}
				handled.Add(1)
			}(enriched)

		case <-ctx.Done():
			goto done
		}
	}

done:
	wg.Wait()
	close(errCh)
	errWg.Wait()

	return PipelineResult{
		Received: received.Load(),
		Filtered: filtered.Load(),
		Handled:  handled.Load(),
		Errors:   errCount.Load(),
	}
}

// Run starts the pipeline asynchronously and returns immediately.
//
// FIX Bug 1: the previous Run() returned a stale zero PipelineResult and
// created a goroutine race where two goroutines competed to drain the same
// internal error channel. It is now implemented as a thin goroutine wrapper
// around RunSync with correct result delivery via a buffered channel.
//
// Returns:
//   - resultCh: closed after the pipeline finishes; receives exactly one value.
//   - errCh:    non-fatal errors as they occur; closed when the pipeline finishes.
//
// Callers must drain errCh to avoid blocking worker goroutines.
func (p *Pipeline) Run(ctx context.Context, in <-chan *RawEvent) (<-chan PipelineResult, <-chan error) {
	errCh := make(chan error, p.cfg.errBufSize)
	resultCh := make(chan PipelineResult, 1)

	go func() {
		result := p.RunSync(ctx, in, func(err error) {
			select {
			case errCh <- err:
			case <-ctx.Done():
			default:
				// Drop if errCh is full.
			}
		})
		close(errCh)
		resultCh <- result
		close(resultCh)
	}()

	return resultCh, errCh
}

// toEnriched lifts a [RawEvent] into an [EnrichedEvent] with path components split.
func (p *Pipeline) toEnriched(raw *RawEvent) *EnrichedEvent {
	return &EnrichedEvent{
		Event: Event{
			RawEvent:  *raw,
			Dir:       filepath.Dir(raw.Path),
			Name:      filepath.Base(raw.Path),
			WatcherID: raw.WatcherID,
		},
	}
}

// passesFilters returns true when the event passes all configured filters.
func (p *Pipeline) passesFilters(ctx context.Context, e *EnrichedEvent) bool {
	return p.filters.Allow(ctx, e)
}

// applyTransformers runs all transformers in sequence.
func (p *Pipeline) applyTransformers(ctx context.Context, e *EnrichedEvent) error {
	return p.transformers.Transform(ctx, e)
}

// numWorkers returns the configured worker count, clamping to NumCPU minimum.
func (p *Pipeline) numWorkers() int {
	if p.cfg.workers > 0 {
		return p.cfg.workers
	}
	return runtime.NumCPU()
}

// ─────────────────────────────────────────────────────────────────────────────
// Middleware — wraps Handler for cross-cutting concerns
// ─────────────────────────────────────────────────────────────────────────────

// Middleware wraps a [Handler] to add cross-cutting behaviour such as tracing,
// metrics, or logging without modifying the handler implementation.
// Middlewares are applied in registration order; the last registered is the
// outermost wrapper and therefore runs first on every event.
type Middleware interface {
	// Wrap returns a new Handler that decorates next.
	Wrap(next Handler) Handler
}

// MiddlewareFunc is a function that implements [Middleware].
type MiddlewareFunc func(next Handler) Handler

// Wrap implements [Middleware].
func (m MiddlewareFunc) Wrap(next Handler) Handler { return m(next) }

// ─────────────────────────────────────────────────────────────────────────────
// EventStream — channel type alias
// ─────────────────────────────────────────────────────────────────────────────

// EventStream is a receive-only channel of [RawEvent] pointers.
// Produced by [Watcher.Watch] and consumed by [Pipeline.RunSync] or [Pipeline.Run].
type EventStream = <-chan *RawEvent
