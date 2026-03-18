package differ

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
	// ExclusiveHandled is the total count of exclusive paths successfully
	// processed (handler returned nil + batcher accepted the op).
	ExclusiveHandled int64
	// CommonHandled is the total count of common paths successfully processed.
	CommonHandled int64
	// Err holds all errors joined via errors.Join. nil on success.
	Err error
}

// OK returns true when Err is nil.
func (r PipelineResult) OK() bool { return r.Err == nil }

// ─────────────────────────────────────────────────────────────────────────────
// Pipeline
// ─────────────────────────────────────────────────────────────────────────────

// Pipeline is the central orchestrator that connects:
//
//	Classifier → [exclusive channel] → ExclusiveHandler → ExclusiveBatcher → MergedView
//	           → [common channel]   → HashPipeline → CommonHandler → CommonBatcher → MergedView
//	           → [error channel]    → error accumulator
//
// Each stage is independently replaceable via its interface. No stage holds a
// reference to any other stage — they communicate only through typed channels,
// preserving the Single Responsibility Principle and Open/Closed Principle.
//
// # Concurrency topology
//
//   - One goroutine drives the Classifier (walkBoth).
//   - Up to HashPipeline.Workers goroutines hash common paths in parallel.
//   - Up to ExclusiveWorkers goroutines dispatch exclusive ops concurrently.
//   - Up to CommonWorkers goroutines dispatch common ops concurrently.
//
// The exclusive and common worker pools are independent, so deletions of
// exclusive-lower paths in the merged view are never gated on hash completion.
//
// # Error policy
//
// When WithAbortOnError is set the pipeline cancels its internal context on
// the first error, causing all stages to drain and exit. When not set (default)
// all errors are accumulated and returned via PipelineResult.Err.
type Pipeline struct {
	classifier Classifier
	excHandler ExclusiveHandler
	comHandler CommonHandler
	cfg        pipelineConfig
}

// NewPipeline constructs a [Pipeline].
//
// classifier must be non-nil. excHandler and comHandler default to
// [NoopExclusiveHandler] and [NoopCommonHandler] respectively when nil.
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
	// Wire in a default HashPipeline when none is configured.
	if cfg.hashPipeline == nil {
		cfg.hashPipeline = NewHashPipeline()
	}
	// Default batcher to nop when not configured; callers may inject real ones.
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
// ctx is used as the parent context. An internal cancel is layered on top so
// that stage errors can abort the pipeline without affecting the caller's
// context.
func (p *Pipeline) Run(ctx context.Context) PipelineResult {
	pCtx, pCancel := context.WithCancel(ctx)
	defer pCancel()

	excWorkers := p.cfg.exclusiveWorkers
	comWorkers := p.cfg.commonWorkers
	if excWorkers <= 0 {
		excWorkers = runtime.NumCPU()
	}
	if comWorkers <= 0 {
		comWorkers = runtime.NumCPU()
	}

	var (
		errsMu sync.Mutex
		errs   []error

		excHandled atomic.Int64
		comHandled atomic.Int64
	)

	addErr := func(err error) {
		if err == nil || isContextErr(err) {
			return
		}
		errsMu.Lock()
		errs = append(errs, err)
		errsMu.Unlock()
		if p.cfg.abortOnError {
			pCancel()
		}
	}

	// ── Stage 1: Classifier ───────────────────────────────────────────────
	// The classifier needs the lower root for hash-path resolution.
	// We extract it via type assertion if available; otherwise use empty string.
	lowerRoot, upperRoot := p.extractRoots()

	exclusiveCh, rawCommonCh, classErrCh := p.classifier.Classify(pCtx)

	// ── Stage 2: Hash pipeline (concurrent enrichment) ─────────────────────
	// Reads raw CommonPath values from rawCommonCh, runs up to
	// HashPipeline.Workers goroutines in parallel to compute hash equality,
	// and streams enriched values to hashedCh. Errors are forwarded to
	// hashErrors which is drained by a dedicated forwarder goroutine.
	hashErrors := make(chan error, 64)
	hashedCh := p.cfg.hashPipeline.Run(pCtx, lowerRoot, upperRoot, rawCommonCh, hashErrors)

	var stageWg sync.WaitGroup

	// ── Hash error forwarder ──────────────────────────────────────────────
	stageWg.Add(1)
	go func() {
		defer stageWg.Done()
		for err := range hashErrors {
			addErr(fmt.Errorf("hash: %w", err))
		}
	}()

	// ── Stage 3a: Exclusive worker pool ───────────────────────────────────
	stageWg.Add(1)
	go func() {
		defer stageWg.Done()
		p.runExclusivePool(pCtx, exclusiveCh, excWorkers, &excHandled,
			p.cfg.exclusiveBatcher, addErr)
	}()

	// ── Stage 3b: Common worker pool ──────────────────────────────────────
	stageWg.Add(1)
	go func() {
		defer stageWg.Done()
		p.runCommonPool(pCtx, hashedCh, comWorkers, &comHandled,
			p.cfg.commonBatcher, addErr)
	}()

	// ── Classifier error forwarder ────────────────────────────────────────
	stageWg.Add(1)
	go func() {
		defer stageWg.Done()
		for err := range classErrCh {
			addErr(fmt.Errorf("classifier: %w", err))
		}
	}()

	stageWg.Wait()

	// Flush any remaining ops from both batchers.
	if err := p.cfg.exclusiveBatcher.Flush(ctx); err != nil {
		addErr(fmt.Errorf("exclusive batcher flush: %w", err))
	}
	if err := p.cfg.commonBatcher.Flush(ctx); err != nil {
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

// runExclusivePool drains exclusiveCh using a bounded worker pool.
// Each worker: calls excHandler, submits to batcher, increments counter.
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
			continue // drain without dispatching
		}
		ep := ep
		sem <- struct{}{}
		wg.Add(1)
		go func() {
			defer wg.Done()
			defer func() { <-sem }()

			if err := p.excHandler.HandleExclusive(ctx, ep); err != nil {
				addErr(fmt.Errorf("exclusive handler %q: %w", ep.Path, err))
				return
			}

			op := exclusiveToOp(ep)
			if err := batcher.Submit(ctx, op); err != nil {
				addErr(fmt.Errorf("exclusive batcher %q: %w", ep.Path, err))
				return
			}
			counter.Add(1)
		}()
	}
	wg.Wait()
}

// runCommonPool drains hashedCh using a bounded worker pool.
// Each worker: calls comHandler, submits to batcher, increments counter.
func (p *Pipeline) runCommonPool(
	ctx context.Context,
	hashedCh <-chan CommonPath,
	workers int,
	counter *atomic.Int64,
	batcher Batcher,
	addErr func(error),
) {
	sem := make(chan struct{}, workers)
	var wg sync.WaitGroup

	for cp := range hashedCh {
		if ctx.Err() != nil {
			continue
		}
		cp := cp
		sem <- struct{}{}
		wg.Add(1)
		go func() {
			defer wg.Done()
			defer func() { <-sem }()

			if err := p.comHandler.HandleCommon(ctx, cp); err != nil {
				addErr(fmt.Errorf("common handler %q: %w", cp.Path, err))
				return
			}

			if op, ok := commonToOp(cp); ok {
				if err := batcher.Submit(ctx, op); err != nil {
					addErr(fmt.Errorf("common batcher %q: %w", cp.Path, err))
					return
				}
			}
			counter.Add(1)
		}()
	}
	wg.Wait()
}

// exclusiveToOp converts an ExclusivePath to a BatchOp.
// Collapsed directories use OpRemoveAll; leaf entries use OpRemove.
func exclusiveToOp(ep ExclusivePath) BatchOp {
	kind := OpRemove
	if ep.Collapsed {
		kind = OpRemoveAll
	}
	return BatchOp{Kind: kind, RelPath: ep.Path, Tag: ep}
}

// commonToOp converts a CommonPath to a BatchOp when an operation is needed.
// Returns (op, true) when hash comparison confirms equality (content identical
// → the entry is redundant in merged and should be removed).
// Returns (BatchOp{}, false) when no operation is warranted (e.g. content
// changed, or comparison not performed for directories).
func commonToOp(cp CommonPath) (BatchOp, bool) {
	eq, checked := cp.IsContentEqual()
	if !checked || !eq {
		return BatchOp{}, false
	}
	return BatchOp{Kind: OpRemove, RelPath: cp.Path, Tag: cp}, true
}

// extractRoots is a best-effort type-assertion to extract lower/upper roots
// from the classifier for hash path resolution.
func (p *Pipeline) extractRoots() (lowerRoot, upperRoot string) {
	if dc, ok := p.classifier.(*DirsyncClassifier); ok {
		return dc.LowerRoot(), dc.UpperRoot()
	}
	return "", ""
}
