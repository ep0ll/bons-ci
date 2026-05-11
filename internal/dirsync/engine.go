package dirsync

import "context"

// ─────────────────────────────────────────────────────────────────────────────
// Engine
// ─────────────────────────────────────────────────────────────────────────────

// Engine is a thin facade over [Pipeline] that provides named convenience
// constructors for the most common usage patterns.
//
// # When to use Engine vs. Pipeline directly
//
// Engine is intended for straightforward scenarios: delete redundant files
// from merged, or observe what would be deleted without mutating anything.
//
// Use [Pipeline] directly when you need:
//   - Custom batchers (e.g. remote storage, transactional deletes).
//   - Composite handlers (ChainExclusiveHandler, PredicateCommonHandler).
//   - Multiple MergedViews updated in one pass.
//   - Fine-grained control over worker counts or hash pipeline parameters.
type Engine struct {
	pipeline *Pipeline
}

// EngineResult is an alias for [PipelineResult] exposed at the Engine level.
// Using an alias rather than embedding allows callers to accept either type
// without any conversion.
type EngineResult = PipelineResult

// Run executes the underlying pipeline and returns the result.
func (e *Engine) Run(ctx context.Context) EngineResult {
	return e.pipeline.Run(ctx)
}

// ─────────────────────────────────────────────────────────────────────────────
// NewDeleteEngine — primary mutation engine
// ─────────────────────────────────────────────────────────────────────────────

// NewDeleteEngine builds an Engine that applies a lower-vs-upper diff to a
// merged directory view, using the best available batcher on the current platform.
//
// # Semantics
//
//   - Exclusive paths (lower-only) are removed from mergedView:
//     collapsed directory entries use OpRemoveAll, leaf files use OpRemove.
//   - Common paths where content is identical (lower == upper) are removed
//     from mergedView: the overlay already serves them from lower, so the
//     merged copy is redundant.
//   - Common paths where content differs are left untouched: the upper version
//     is authoritative and the merged copy reflects it.
//
// # Batcher selection
//
// [NewBestBatcher] is used: IOURingBatcher on Linux 5.11+, GoroutineBatcher
// on all other platforms or when io_uring setup fails.
//
// # Parameters
//
//   - classOpts: options forwarded to [NewClassifier] (filters, buffer sizes, etc.)
//   - pipeOpts:  options forwarded to [NewPipeline] (worker counts, abort policy, etc.)
func NewDeleteEngine(
	lowerDir, upperDir, mergedDir string,
	classOpts []ClassifierOption,
	pipeOpts []PipelineOption,
) (*Engine, error) {
	view, err := NewFSMergedView(mergedDir)
	if err != nil {
		return nil, err
	}

	batcher, err := NewBestBatcher(view)
	if err != nil {
		return nil, err
	}

	classifier := NewClassifier(lowerDir, upperDir, classOpts...)

	// Prepend the batcher options so callers can override them via pipeOpts.
	combinedOpts := append(
		[]PipelineOption{
			WithExclusiveBatcher(batcher),
			WithCommonBatcher(batcher),
		},
		pipeOpts...,
	)

	pl := NewPipeline(classifier, NoopExclusiveHandler{}, NoopCommonHandler{}, combinedOpts...)
	return &Engine{pipeline: pl}, nil
}

// ─────────────────────────────────────────────────────────────────────────────
// NewObserveEngine — read-only audit engine
// ─────────────────────────────────────────────────────────────────────────────

// NewObserveEngine builds an Engine that performs no filesystem mutations.
// It counts exclusive and common paths for auditing and reporting purposes.
//
// Both counters are passed in rather than constructed internally so that
// callers can share counter instances across multiple engine runs (e.g. for
// aggregated stats across many merged directories).
//
// # Parameters
//
//   - excCounter: receives all exclusive paths and tallies them by kind.
//   - comCounter: receives all common paths and tallies them by comparison outcome.
//   - classOpts, pipeOpts: forwarded to [NewClassifier] and [NewPipeline].
func NewObserveEngine(
	lowerDir, upperDir string,
	excCounter *CountingExclusiveHandler,
	comCounter *CountingCommonHandler,
	classOpts []ClassifierOption,
	pipeOpts []PipelineOption,
) *Engine {
	classifier := NewClassifier(lowerDir, upperDir, classOpts...)
	pl := NewPipeline(classifier, excCounter, comCounter, pipeOpts...)
	return &Engine{pipeline: pl}
}

// ─────────────────────────────────────────────────────────────────────────────
// NewCustomEngine — fully flexible engine for advanced scenarios
// ─────────────────────────────────────────────────────────────────────────────

// NewCustomEngine builds an Engine from pre-constructed components.
// It is the most flexible constructor: every component is supplied by the caller,
// enabling injection of custom classifiers, handlers, and options.
//
// This is the recommended entry point when [NewDeleteEngine] or
// [NewObserveEngine] do not cover your use case.
func NewCustomEngine(
	classifier Classifier,
	excHandler ExclusiveHandler,
	comHandler CommonHandler,
	opts ...PipelineOption,
) *Engine {
	pl := NewPipeline(classifier, excHandler, comHandler, opts...)
	return &Engine{pipeline: pl}
}
