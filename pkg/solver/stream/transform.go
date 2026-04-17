// Package stream provides streaming transformation pipelines for solver
// results. Transformations execute as data becomes available, enabling
// export-while-solving patterns.
package stream

import (
	"context"
	"fmt"
)

// Transformer applies a transformation to a result value. Implementations
// must be safe for concurrent use if the pipeline is used concurrently.
type Transformer interface {
	// Transform processes an input value and returns a transformed output.
	Transform(ctx context.Context, input any) (output any, err error)

	// Name returns a human-readable label for instrumentation.
	Name() string
}

// TransformFunc adapts a plain function into a Transformer.
type TransformFunc struct {
	Fn   func(ctx context.Context, input any) (any, error)
	Label string
}

// Transform calls the wrapped function.
func (f TransformFunc) Transform(ctx context.Context, input any) (any, error) {
	return f.Fn(ctx, input)
}

// Name returns the label.
func (f TransformFunc) Name() string {
	if f.Label != "" {
		return f.Label
	}
	return "transform-func"
}

// Pipeline chains multiple transformers in sequence. Each stage's output
// feeds into the next stage's input.
type Pipeline struct {
	stages []Transformer
}

// NewPipeline creates a pipeline from the given transformers, applied
// in order.
func NewPipeline(stages ...Transformer) *Pipeline {
	return &Pipeline{stages: stages}
}

// Process runs the input through all pipeline stages sequentially.
// If any stage fails, processing stops and the error is returned.
func (p *Pipeline) Process(ctx context.Context, input any) (any, error) {
	current := input
	for i, stage := range p.stages {
		select {
		case <-ctx.Done():
			return nil, fmt.Errorf("pipeline canceled at stage %d (%s): %w", i, stage.Name(), ctx.Err())
		default:
		}

		var err error
		current, err = stage.Transform(ctx, current)
		if err != nil {
			return nil, fmt.Errorf("pipeline stage %d (%s): %w", i, stage.Name(), err)
		}
	}
	return current, nil
}

// Len returns the number of stages.
func (p *Pipeline) Len() int {
	return len(p.stages)
}

// Append adds stages to the end of the pipeline.
func (p *Pipeline) Append(stages ...Transformer) {
	p.stages = append(p.stages, stages...)
}
