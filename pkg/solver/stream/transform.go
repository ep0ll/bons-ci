// Package stream provides streaming transformation pipelines for solver
// results. Transformations execute as data becomes available, enabling
// export-while-solving patterns analogous to BuildKit's lazy evaluation model.
package stream

import (
	"context"
	"fmt"
	"sync"
	"time"
)

// Transformer applies a transformation to an arbitrary value. Implementations
// must be safe for concurrent use if the pipeline is shared across goroutines.
type Transformer interface {
	// Transform processes input and returns the transformed output.
	Transform(ctx context.Context, input any) (output any, err error)

	// Name returns a human-readable label used in error messages and metrics.
	Name() string
}

// TransformFunc adapts a plain function into a Transformer.
type TransformFunc struct {
	Fn    func(ctx context.Context, input any) (any, error)
	Label string
}

func (f TransformFunc) Transform(ctx context.Context, input any) (any, error) {
	return f.Fn(ctx, input)
}

func (f TransformFunc) Name() string {
	if f.Label != "" {
		return f.Label
	}
	return "anonymous-transform"
}

// StageMetrics records timing for a single pipeline stage.
type StageMetrics struct {
	Name     string
	Duration time.Duration
	Err      error
}

// Pipeline chains multiple Transformers in sequence. Each stage's output feeds
// the next stage. Pipelines are safe for concurrent use (each Process call
// maintains its own state).
type Pipeline struct {
	mu     sync.RWMutex
	stages []Transformer
	// OnStageComplete is called after each stage completes. Optional.
	OnStageComplete func(StageMetrics)
}

// NewPipeline creates a pipeline from the given transformers, applied in order.
func NewPipeline(stages ...Transformer) *Pipeline {
	return &Pipeline{stages: append([]Transformer(nil), stages...)}
}

// Process runs input through all pipeline stages in order. Context cancellation
// is checked between every stage — not only at the start — so long pipelines
// remain responsive to cancellation.
//
// If any stage returns an error, processing halts and the error is returned
// with a descriptive wrapper identifying the failing stage.
func (p *Pipeline) Process(ctx context.Context, input any) (any, error) {
	p.mu.RLock()
	stages := make([]Transformer, len(p.stages))
	copy(stages, p.stages)
	p.mu.RUnlock()

	current := input
	for i, stage := range stages {
		// Check cancellation BEFORE each stage, not just once at the top.
		select {
		case <-ctx.Done():
			return nil, fmt.Errorf("pipeline canceled before stage %d (%s): %w",
				i, stage.Name(), ctx.Err())
		default:
		}

		start := time.Now()
		var err error
		current, err = stage.Transform(ctx, current)
		elapsed := time.Since(start)

		if p.OnStageComplete != nil {
			p.OnStageComplete(StageMetrics{Name: stage.Name(), Duration: elapsed, Err: err})
		}

		if err != nil {
			return nil, fmt.Errorf("pipeline stage %d (%s): %w", i, stage.Name(), err)
		}
	}
	return current, nil
}

// ProcessAsync runs the pipeline in a goroutine and sends the result on the
// returned channel. The channel is always closed, even on error. This enables
// streaming: the caller can start consuming other results while the pipeline
// runs concurrently.
func (p *Pipeline) ProcessAsync(ctx context.Context, input any) <-chan AsyncResult {
	ch := make(chan AsyncResult, 1)
	go func() {
		defer close(ch)
		out, err := p.Process(ctx, input)
		ch <- AsyncResult{Value: out, Err: err}
	}()
	return ch
}

// AsyncResult carries the output of an async pipeline stage.
type AsyncResult struct {
	Value any
	Err   error
}

// Len returns the number of stages.
func (p *Pipeline) Len() int {
	p.mu.RLock()
	n := len(p.stages)
	p.mu.RUnlock()
	return n
}

// Append adds one or more stages to the end of the pipeline.
// Safe to call concurrently with Process.
func (p *Pipeline) Append(stages ...Transformer) {
	p.mu.Lock()
	p.stages = append(p.stages, stages...)
	p.mu.Unlock()
}

// Prepend inserts stages at the beginning of the pipeline.
func (p *Pipeline) Prepend(stages ...Transformer) {
	p.mu.Lock()
	p.stages = append(append([]Transformer(nil), stages...), p.stages...)
	p.mu.Unlock()
}
