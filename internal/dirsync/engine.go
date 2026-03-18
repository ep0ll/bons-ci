package differ

import "context"

// Engine is a thin facade over [Pipeline] that provides named convenience
// constructors for the most common usage patterns. All orchestration is
// handled by the Pipeline.
//
// Direct use of [Pipeline] is recommended for advanced scenarios (custom
// batchers, composite handlers, multiple MergedViews). Engine is intended for
// straightforward "apply diff to merged view" use cases.
type Engine struct {
	pipeline *Pipeline
}

// EngineResult is an alias for [PipelineResult] exposed at the Engine level.
type EngineResult = PipelineResult

// Run executes the pipeline and returns the result.
func (e *Engine) Run(ctx context.Context) EngineResult {
	return e.pipeline.Run(ctx)
}

// NewDeleteEngine builds an Engine that applies a lower-vs-upper diff to a
// merged directory view using the best available batcher on the current platform.
//
// Specifically:
//   - Exclusive paths (lower-only) are removed from mergedView (OpRemoveAll
//     for collapsed dirs, OpRemove for leaf files).
//   - Common paths where content is identical between lower and upper are
//     removed from mergedView (they are redundant; the overlay already serves
//     them from lower).
//   - Common paths where content differs are left untouched (upper is
//     authoritative).
//
// The batcher is selected via [NewBestBatcher]: io_uring on Linux 5.11+,
// goroutine pool elsewhere.
func NewDeleteEngine(
	lower, upper, mergedDir string,
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

	classifier := NewClassifier(lower, upper, classOpts...)
	combined := append(
		[]PipelineOption{
			WithExclusiveBatcher(batcher),
			WithCommonBatcher(batcher),
		},
		pipeOpts...,
	)
	pl := NewPipeline(classifier, NoopExclusiveHandler{}, NoopCommonHandler{}, combined...)
	return &Engine{pipeline: pl}, nil
}

// NewObserveEngine builds an Engine that performs no mutations. It counts
// exclusive and common paths for audit purposes without touching the merged view.
func NewObserveEngine(
	lower, upper string,
	excCounter *CountingExclusiveHandler,
	comCounter *CountingCommonHandler,
	classOpts []ClassifierOption,
	pipeOpts []PipelineOption,
) *Engine {
	classifier := NewClassifier(lower, upper, classOpts...)
	pl := NewPipeline(classifier, excCounter, comCounter, pipeOpts...)
	return &Engine{pipeline: pl}
}

// NewCustomEngine builds an Engine from pre-constructed components. It is the
// most flexible constructor; every component is supplied by the caller.
func NewCustomEngine(
	classifier Classifier,
	excHandler ExclusiveHandler,
	comHandler CommonHandler,
	opts ...PipelineOption,
) *Engine {
	pl := NewPipeline(classifier, excHandler, comHandler, opts...)
	return &Engine{pipeline: pl}
}
