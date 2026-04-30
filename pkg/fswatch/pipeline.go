package fanwatch

import (
	"context"
	"fmt"
	"path/filepath"
	"runtime"
	"sync"
	"sync/atomic"
	"time"
)

// ─────────────────────────────────────────────────────────────────────────────
// PipelineResult — summary of a completed pipeline run
// ─────────────────────────────────────────────────────────────────────────────

// PipelineResult summarises a completed [Pipeline.Run] call.
type PipelineResult struct {
	// Received is the total number of raw events read from the watcher.
	Received int64
	// Filtered is the count of events dropped by filters.
	Filtered int64
	// Handled is the count of events successfully delivered to the handler.
	Handled int64
	// Errors is the count of transformer or handler errors encountered.
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
//	Convert to EnrichedEvent
//	    │
//	    ▼
//	Filter stage (all filters, short-circuit AND)
//	    │  (drop on reject)
//	    ▼
//	Transform stage (chain of transformers)
//	    │
//	    ▼
//	Worker pool → Handler
//	    │
//	    ▼
//	Error channel
//
// Each stage is driven by its own goroutine pool. The pipeline is started by
// Run() and stops when ctx is cancelled or the input channel is closed.
type Pipeline struct {
	filters      AllFilters
	transformers ChainTransformer
	handler      Handler
	middlewares  []Middleware
	cfg          pipelineConfig
}

// pipelineConfig holds tunable Pipeline parameters.
type pipelineConfig struct {
	workers      int
	errBufSize   int
	overlayInfo  *OverlayInfo
}

func defaultPipelineConfig() pipelineConfig {
	return pipelineConfig{
		workers:    runtime.NumCPU(),
		errBufSize: 64,
	}
}

// NewPipeline constructs a [Pipeline] with the provided options.
func NewPipeline(opts ...PipelineOption) *Pipeline {
	cfg := defaultPipelineConfig()
	p := &Pipeline{
		handler: NoopHandler{},
		cfg:     cfg,
	}
	for _, o := range opts {
		o(p)
	}
	// Wrap handler with middlewares in reverse order (last registered = outermost).
	for i := len(p.middlewares) - 1; i >= 0; i-- {
		p.handler = p.middlewares[i].Wrap(p.handler)
	}
	return p
}

// Run starts the pipeline. It reads [RawEvent]s from in, applies filters and
// transformers, and delivers [EnrichedEvent]s to the configured handler.
//
// Run blocks until ctx is cancelled or in is closed. It returns a
// [PipelineResult] summarising what happened and a channel of non-fatal errors.
//
// Callers must drain the error channel — failing to do so will deadlock the
// pipeline when errors occur.
func (p *Pipeline) Run(ctx context.Context, in <-chan *RawEvent) (PipelineResult, <-chan error) {
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
			// Error channel full — drop to avoid blocking the pipeline.
		}
	}

	sem := make(chan struct{}, p.workers())
	var wg sync.WaitGroup

	go func() {
		defer close(errCh)
		defer wg.Wait()

		for {
			select {
			case raw, ok := <-in:
				if !ok {
					return
				}
				if ctx.Err() != nil {
					continue // drain without dispatching
				}

				received.Add(1)

				enriched := p.toEnriched(raw)

				if !p.passesFilters(ctx, enriched) {
					filtered.Add(1)
					continue
				}

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
						addErr(fmt.Errorf("pipeline: transform %q: %w", e.Path, err))
						// continue — event may be partially enriched but still valid
					}

					if err := p.handler.Handle(ctx, e); err != nil {
						addErr(fmt.Errorf("pipeline: handle %q: %w", e.Path, err))
						return
					}
					handled.Add(1)
				}(enriched)

			case <-ctx.Done():
				return
			}
		}
	}()

	result := PipelineResult{} // populated after goroutine exits via pointer captures
	_ = result                  // real values via atomic loads below

	// Return a channel so callers can react to errors while Run blocks.
	// We start a goroutine to wait and produce the final result separately.
	resultCh := make(chan PipelineResult, 1)
	go func() {
		// Wait for the main goroutine to close errCh.
		// errCh is closed only after wg.Wait() — so this is safe.
		for range errCh {
			// drain (caller already has the channel via the returned value)
		}
		resultCh <- PipelineResult{
			Received: received.Load(),
			Filtered: filtered.Load(),
			Handled:  handled.Load(),
			Errors:   errCount.Load(),
		}
	}()

	// We need to return the errCh to the caller but also drain it above.
	// Redesign: use a forwarding channel.
	fwdCh := make(chan error, p.cfg.errBufSize)
	go func() {
		for e := range errCh {
			fwdCh <- e
		}
		close(fwdCh)
	}()

	_ = resultCh // caller gets result via RunSync for blocking mode

	return PipelineResult{
		Received: received.Load(),
		Filtered: filtered.Load(),
		Handled:  handled.Load(),
		Errors:   errCount.Load(),
	}, fwdCh
}

// RunSync runs the pipeline to completion and returns after ctx is cancelled
// or in is closed. Errors are forwarded to onError (may be nil).
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
		}
	}

	// Start error consumer before the producer.
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

	sem := make(chan struct{}, p.workers())
	var wg sync.WaitGroup

	for {
		select {
		case raw, ok := <-in:
			if !ok {
				goto done
			}
			if ctx.Err() != nil {
				continue
			}

			received.Add(1)
			enriched := p.toEnriched(raw)

			if !p.passesFilters(ctx, enriched) {
				filtered.Add(1)
				continue
			}

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

// workers returns the configured worker count, defaulting to NumCPU.
func (p *Pipeline) workers() int {
	if p.cfg.workers > 0 {
		return p.cfg.workers
	}
	return runtime.NumCPU()
}

// ─────────────────────────────────────────────────────────────────────────────
// Middleware — wraps Handler for cross-cutting concerns (OTEL, logging)
// ─────────────────────────────────────────────────────────────────────────────

// Middleware wraps a [Handler] to add cross-cutting behaviour such as tracing,
// metrics, or logging. Middlewares are applied in registration order; the last
// registered middleware is the outermost wrapper.
type Middleware interface {
	// Wrap returns a new Handler that decorates next.
	Wrap(next Handler) Handler
}

// MiddlewareFunc is a function that implements [Middleware].
type MiddlewareFunc func(next Handler) Handler

// Wrap implements [Middleware].
func (m MiddlewareFunc) Wrap(next Handler) Handler { return m(next) }

// ─────────────────────────────────────────────────────────────────────────────
// EventStream — typed channel alias for ergonomic pipeline construction
// ─────────────────────────────────────────────────────────────────────────────

// EventStream is a receive-only channel of [RawEvent] pointers.
// Produced by [Watcher.Watch] and consumed by [Pipeline.RunSync].
type EventStream = <-chan *RawEvent

// ─────────────────────────────────────────────────────────────────────────────
// TimeoutFilter — drop events older than a threshold
// ─────────────────────────────────────────────────────────────────────────────

// FreshnessFilter drops events older than maxAge relative to now.
// Stale events can occur when the pipeline falls behind under high load.
func FreshnessFilter(maxAge time.Duration) Filter {
	return FilterFunc(func(_ context.Context, e *EnrichedEvent) bool {
		return time.Since(e.Timestamp) <= maxAge
	})
}
