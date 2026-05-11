package dirsync

import (
	"context"
	"fmt"
	"runtime"
	"sync"
	"sync/atomic"
)

// ─────────────────────────────────────────────────────────────────────────────
// PipelineResult
// ─────────────────────────────────────────────────────────────────────────────

// PipelineResult summarises a completed [Pipeline.Run] call.
type PipelineResult struct {
	// ExclusiveHandled is the total count of exclusive paths for which the
	// handler returned nil and the batcher accepted the op.
	ExclusiveHandled int64

	// CommonHandled is the total count of common paths for which the handler
	// returned nil and (when applicable) the batcher accepted the op.
	CommonHandled int64

	// Err holds all errors joined via errors.Join. nil means success.
	Err error
}

// OK returns true when Err is nil — the pipeline completed without errors.
func (r PipelineResult) OK() bool { return r.Err == nil }

// ─────────────────────────────────────────────────────────────────────────────
// Pipeline
// ─────────────────────────────────────────────────────────────────────────────

// Pipeline is the central orchestrator connecting all processing stages:
//
//	Classifier ──exclusive──► ExclusiveWorkerPool ──► ExclusiveHandler ──► ExclusiveBatcher
//	           ──common───► HashPipeline ──► CommonWorkerPool ──► CommonHandler ──► CommonBatcher
//	           ──errs────► error accumulator
//
// Each stage is independently replaceable via its interface. Stages communicate
// only through typed channels — no stage holds a direct reference to any other.
//
// # Concurrency topology
//
//   - One goroutine drives the Classifier (walkBoth).
//   - Up to HashPipeline.Workers goroutines hash common paths in parallel.
//   - Up to ExclusiveWorkers goroutines dispatch exclusive ops concurrently.
//   - Up to CommonWorkers goroutines dispatch common ops concurrently.
//
// The exclusive and common worker pools are independent: lower-path deletions
// are never gated on hash completion, so the two streams make progress in parallel.
//
// # Error policy
//
// When [WithAbortOnError] is set the pipeline cancels its internal context on
// the first error, causing all stages to drain and exit as quickly as possible.
// Without it (default), every error is collected and the full set is returned
// via [PipelineResult.Err] after the pipeline drains completely.
type Pipeline struct {
	classifier Classifier
	excHandler ExclusiveHandler
	comHandler CommonHandler
	cfg        pipelineConfig
}

// NewPipeline constructs a [Pipeline].
//
// classifier must be non-nil. Nil excHandler and comHandler are replaced with
// [NoopExclusiveHandler] and [NoopCommonHandler] respectively so callers never
// need to check for nil before passing.
func NewPipeline(
	classifier Classifier,
	excHandler ExclusiveHandler,
	comHandler CommonHandler,
	opts ...PipelineOption,
) *Pipeline {
	cfg := defaultPipelineConfig()
	for _, o := range opts {
		o(&cfg)
	}

	if excHandler == nil {
		excHandler = NoopExclusiveHandler{}
	}
	if comHandler == nil {
		comHandler = NoopCommonHandler{}
	}
	if cfg.hashPipeline == nil {
		cfg.hashPipeline = NewHashPipeline()
	}
	if cfg.exclusiveBatcher == nil {
		cfg.exclusiveBatcher = NopBatcher{}
	}
	if cfg.commonBatcher == nil {
		cfg.commonBatcher = NopBatcher{}
	}

	return &Pipeline{
		classifier: classifier,
		excHandler: excHandler,
		comHandler: comHandler,
		cfg:        cfg,
	}
}

// Run executes the full pipeline synchronously and returns a [PipelineResult].
//
// An internal cancel context is layered on top of ctx so stage errors can
// abort the pipeline without affecting the caller's own context. The caller's
// ctx is still respected: if the caller cancels ctx, the pipeline stops.
func (p *Pipeline) Run(ctx context.Context) PipelineResult {
	// Internal cancel context: allows stages to abort each other independently
	// of the caller's context.
	pCtx, pCancel := context.WithCancel(ctx)
	defer pCancel()

	excWorkers := p.workerCount(p.cfg.exclusiveWorkers)
	comWorkers := p.workerCount(p.cfg.commonWorkers)

	var (
		errsMu sync.Mutex
		errs   []error

		excHandled atomic.Int64
		comHandled atomic.Int64
	)

	// addErr collects a non-nil, non-context error and optionally aborts.
	addErr := func(err error) {
		if err == nil || isContextErr(err) {
			return
		}
		errsMu.Lock()
		errs = append(errs, err)
		errsMu.Unlock()
		if p.cfg.abortOnError {
			pCancel() // signal all other stages to stop
		}
	}

	// Pull the root paths from the classifier if it exposes them.
	// The hash pipeline needs these to construct absolute file paths.
	lowerRoot, upperRoot := p.classifierRoots()

	// ── Stage 1: Classify ───────────────────────────────────────────────────
	exclusiveCh, rawCommonCh, classErrCh := p.classifier.Classify(pCtx)

	// ── Stage 2: Hash enrichment (common stream only) ───────────────────────
	hashErrCh := make(chan error, 64)
	enrichedCommonCh := p.cfg.hashPipeline.Run(pCtx, lowerRoot, upperRoot, rawCommonCh, hashErrCh)

	// ── Stage 3: Dispatch workers ────────────────────────────────────────────
	var stageWg sync.WaitGroup

	// Forward hash errors.
	stageWg.Add(1)
	go func() {
		defer stageWg.Done()
		for err := range hashErrCh {
			addErr(fmt.Errorf("hash stage: %w", err))
		}
	}()

	// Exclusive worker pool.
	stageWg.Add(1)
	go func() {
		defer stageWg.Done()
		p.runExclusivePool(pCtx, exclusiveCh, excWorkers, &excHandled, p.cfg.exclusiveBatcher, addErr)
	}()

	// Common worker pool.
	stageWg.Add(1)
	go func() {
		defer stageWg.Done()
		p.runCommonPool(pCtx, enrichedCommonCh, comWorkers, &comHandled, p.cfg.commonBatcher, addErr)
	}()

	// Forward classifier errors.
	stageWg.Add(1)
	go func() {
		defer stageWg.Done()
		for err := range classErrCh {
			addErr(fmt.Errorf("classifier stage: %w", err))
		}
	}()

	stageWg.Wait()

	// Flush with pCtx (the internal context), not the caller's ctx.
	// If WithAbortOnError fired, pCtx is already cancelled, which prevents
	// further ops from executing on the merged view after the pipeline aborted.
	// Using the caller's ctx here would allow ops to proceed after an abort.
	if err := p.cfg.exclusiveBatcher.Flush(pCtx); err != nil {
		addErr(fmt.Errorf("exclusive batcher flush: %w", err))
	}
	if err := p.cfg.commonBatcher.Flush(pCtx); err != nil {
		addErr(fmt.Errorf("common batcher flush: %w", err))
	}

	errsMu.Lock()
	combined := joinErrors(errs)
	errsMu.Unlock()

	return PipelineResult{
		ExclusiveHandled: excHandled.Load(),
		CommonHandled:    comHandled.Load(),
		Err:              combined,
	}
}

// ─────────────────────────────────────────────────────────────────────────────
// Worker pools
// ─────────────────────────────────────────────────────────────────────────────

// runExclusivePool drains exclusiveCh using a bounded goroutine pool.
//
// For each exclusive path, the pool goroutine calls the exclusive handler,
// submits a corresponding BatchOp to the batcher on success, and increments
// the counter. Errors from either the handler or the batcher are forwarded
// through addErr.
//
// Cancellation safety: the semaphore acquire uses select+ctx.Done() so a
// cancelled context unblocks the acquire even when all slots are occupied.
// Without this, the goroutine would block until a slot freed — potentially
// after the pipeline was supposed to have terminated.
func (p *Pipeline) runExclusivePool(
	ctx context.Context,
	exclusiveCh <-chan ExclusivePath,
	workers int,
	counter *atomic.Int64,
	batcher Batcher,
	addErr func(error),
) {
	sem := make(chan struct{}, workers)
	var wg sync.WaitGroup

	for ep := range exclusiveCh {
		if ctx.Err() != nil {
			continue // drain without dispatching; let classifier exit cleanly
		}
		ep := ep // capture loop variable

		select {
		case sem <- struct{}{}:
		case <-ctx.Done():
			continue
		}

		wg.Add(1)
		go func() {
			defer wg.Done()
			defer func() { <-sem }()

			if err := p.excHandler.HandleExclusive(ctx, ep); err != nil {
				addErr(fmt.Errorf("exclusive handler %q: %w", ep.Path, err))
				return
			}

			op := exclusivePathToBatchOp(ep)
			if err := batcher.Submit(ctx, op); err != nil {
				addErr(fmt.Errorf("exclusive batcher submit %q: %w", ep.Path, err))
				return
			}

			counter.Add(1)
		}()
	}
	wg.Wait()
}

// runCommonPool drains enrichedCh using a bounded goroutine pool.
//
// For each enriched common path, the pool goroutine calls the common handler,
// submits a BatchOp to the batcher only when content is confirmed equal
// (redundant in merged), and increments the counter.
func (p *Pipeline) runCommonPool(
	ctx context.Context,
	enrichedCh <-chan CommonPath,
	workers int,
	counter *atomic.Int64,
	batcher Batcher,
	addErr func(error),
) {
	sem := make(chan struct{}, workers)
	var wg sync.WaitGroup

	for cp := range enrichedCh {
		if ctx.Err() != nil {
			continue
		}
		cp := cp // capture loop variable

		select {
		case sem <- struct{}{}:
		case <-ctx.Done():
			continue
		}

		wg.Add(1)
		go func() {
			defer wg.Done()
			defer func() { <-sem }()

			if err := p.comHandler.HandleCommon(ctx, cp); err != nil {
				addErr(fmt.Errorf("common handler %q: %w", cp.Path, err))
				return
			}

			// Only submit a removal op when the file is confirmed equal
			// between lower and upper — the merged copy is then redundant.
			if op, shouldRemove := commonPathToBatchOp(cp); shouldRemove {
				if err := batcher.Submit(ctx, op); err != nil {
					addErr(fmt.Errorf("common batcher submit %q: %w", cp.Path, err))
					return
				}
			}

			counter.Add(1)
		}()
	}
	wg.Wait()
}

// ─────────────────────────────────────────────────────────────────────────────
// Op conversion helpers
// ─────────────────────────────────────────────────────────────────────────────

// exclusivePathToBatchOp converts an [ExclusivePath] to a [BatchOp].
// Collapsed directories use OpRemoveAll; leaf entries use OpRemove.
func exclusivePathToBatchOp(ep ExclusivePath) BatchOp {
	kind := OpRemove
	if ep.Collapsed {
		kind = OpRemoveAll
	}
	return BatchOp{Kind: kind, RelPath: ep.Path, Tag: ep}
}

// commonPathToBatchOp converts a [CommonPath] to a [BatchOp] when the entry
// should be removed from the merged view.
//
// Returns (op, true) only when hash comparison confirmed equality — the merged
// copy is then redundant because the overlay already serves it from lower.
// Returns (BatchOp{}, false) for changed, unchecked, or type-mismatched entries.
func commonPathToBatchOp(cp CommonPath) (BatchOp, bool) {
	eq, checked := cp.IsContentEqual()
	if !checked || !eq {
		return BatchOp{}, false
	}
	return BatchOp{Kind: OpRemove, RelPath: cp.Path, Tag: cp}, true
}

// ─────────────────────────────────────────────────────────────────────────────
// Internal helpers
// ─────────────────────────────────────────────────────────────────────────────

// workerCount returns configured when > 0, otherwise runtime.NumCPU().
func (p *Pipeline) workerCount(configured int) int {
	if configured > 0 {
		return configured
	}
	return runtime.NumCPU()
}

// classifierRoots extracts the lower and upper root paths from the classifier.
// Uses a type assertion to [DirsyncClassifier]; returns empty strings when the
// classifier is a custom implementation that does not expose roots.
// The hash pipeline uses these to build absolute file paths.
func (p *Pipeline) classifierRoots() (lowerRoot, upperRoot string) {
	if dc, ok := p.classifier.(*DirsyncClassifier); ok {
		return dc.LowerRoot(), dc.UpperRoot()
	}
	return "", ""
}
