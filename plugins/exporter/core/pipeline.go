package core

import (
	"context"
	"fmt"
	"sort"
)

// ─── Pipeline ──────────────────────────────────────────────────────────────

// Pipeline composes a sorted chain of Transformers with a single Exporter,
// turning a raw ExportRequest into a fully-processed ExportResult.
//
// Execution order:
//  1. Pre-export transformers run in ascending Priority order.
//  2. The exporter's Export method runs.
//  3. Post-export middleware (hooks) run in registration order.
//
// Pipeline is NOT safe for concurrent use; create one per goroutine or
// protect with a mutex.
type Pipeline interface {
	// AddTransformer appends a transformer. Duplicate names are rejected.
	AddTransformer(t Transformer) error

	// MustAddTransformer calls AddTransformer and panics on error.
	MustAddTransformer(t Transformer)

	// Execute runs the full pipeline for the given request.
	Execute(ctx context.Context, req *ExportRequest) (*ExportResult, FinalizeFunc, error)

	// Transformers returns the current ordered transformer list (copy).
	Transformers() []Transformer
}

// ─── Middleware ────────────────────────────────────────────────────────────

// Middleware is a function that wraps an ExportFunc to add cross-cutting
// behaviour (tracing, metrics, retries, audit logging, etc.).
// Middlewares are applied in registration order, outermost last.
type Middleware func(next ExportFunc) ExportFunc

// ExportFunc is the signature of the inner export operation that Middleware wraps.
type ExportFunc func(ctx context.Context, req *ExportRequest) (*ExportResult, FinalizeFunc, error)

// ─── pipeline (concrete) ───────────────────────────────────────────────────

type pipeline struct {
	exporter     ExporterInstance
	transformers []Transformer       // sorted by Priority ascending
	nameIndex    map[string]struct{} // for duplicate detection
	middlewares  []Middleware
}

// NewPipeline creates a Pipeline around the given ExporterInstance.
// The pipeline owns the instance; do not share instances across pipelines.
func NewPipeline(exp ExporterInstance, middlewares ...Middleware) Pipeline {
	return &pipeline{
		exporter:    exp,
		nameIndex:   make(map[string]struct{}),
		middlewares: middlewares,
	}
}

func (p *pipeline) AddTransformer(t Transformer) error {
	if t == nil {
		return fmt.Errorf("pipeline: cannot add nil transformer")
	}
	name := t.Name()
	if name == "" {
		return fmt.Errorf("pipeline: transformer must have a non-empty Name()")
	}
	if _, exists := p.nameIndex[name]; exists {
		return fmt.Errorf("pipeline: transformer %q already added", name)
	}
	p.nameIndex[name] = struct{}{}
	p.transformers = append(p.transformers, t)
	// Re-sort after every insertion; N is small (< 20 transformers typical).
	sort.SliceStable(p.transformers, func(i, j int) bool {
		return p.transformers[i].Priority() < p.transformers[j].Priority()
	})
	return nil
}

func (p *pipeline) MustAddTransformer(t Transformer) {
	if err := p.AddTransformer(t); err != nil {
		panic("pipeline: " + err.Error())
	}
}

func (p *pipeline) Transformers() []Transformer {
	cp := make([]Transformer, len(p.transformers))
	copy(cp, p.transformers)
	return cp
}

func (p *pipeline) Execute(ctx context.Context, req *ExportRequest) (*ExportResult, FinalizeFunc, error) {
	if err := req.Validate(); err != nil {
		return nil, nil, fmt.Errorf("pipeline execute: %w", err)
	}

	// Step 1 — run transformers in priority order.
	artifact := req.Artifact.Clone()
	for _, t := range p.transformers {
		var err error
		artifact, err = t.Transform(ctx, artifact)
		if err != nil {
			return nil, nil, NewTransformError(t.Name(), err)
		}
		if artifact == nil {
			return nil, nil, NewTransformError(t.Name(),
				fmt.Errorf("transformer returned nil artifact"))
		}
	}

	// Rebuild request with transformed artifact (keep original immutable).
	transformedReq := *req
	transformedReq.Artifact = artifact

	// Step 2 — build the middleware chain around the core export func.
	exportFn := p.coreExport
	for i := len(p.middlewares) - 1; i >= 0; i-- {
		exportFn = p.middlewares[i](exportFn)
	}

	return exportFn(ctx, &transformedReq)
}

func (p *pipeline) coreExport(ctx context.Context, req *ExportRequest) (*ExportResult, FinalizeFunc, error) {
	return p.exporter.Export(ctx, req)
}

// ─── PipelineBuilder ───────────────────────────────────────────────────────

// PipelineBuilder is a convenience builder that resolves an exporter from a
// Registry and wires up transformers and middleware in one call.
//
// Usage:
//
//	pipeline, err := core.NewPipelineBuilder(registry).
//	    ForExporter(core.ExporterTypeContainerImage, opts).
//	    WithTransformer(epochTransformer).
//	    WithTransformer(attestationTransformer).
//	    WithMiddleware(tracingMiddleware).
//	    Build(ctx)
type PipelineBuilder struct {
	registry     Registry
	exporterType ExporterType
	opts         Options
	transformers []Transformer
	middlewares  []Middleware
	err          error
}

// NewPipelineBuilder creates a PipelineBuilder backed by the given registry.
func NewPipelineBuilder(r Registry) *PipelineBuilder {
	return &PipelineBuilder{registry: r}
}

// ForExporter sets the target exporter type and resolution options.
func (b *PipelineBuilder) ForExporter(t ExporterType, opts Options) *PipelineBuilder {
	b.exporterType = t
	b.opts = opts
	return b
}

// WithTransformer adds a transformer to the pipeline (duplicates are errors at Build time).
func (b *PipelineBuilder) WithTransformer(t Transformer) *PipelineBuilder {
	b.transformers = append(b.transformers, t)
	return b
}

// WithMiddleware appends a middleware wrapper.
func (b *PipelineBuilder) WithMiddleware(m Middleware) *PipelineBuilder {
	b.middlewares = append(b.middlewares, m)
	return b
}

// Build resolves the exporter, wires everything together, and returns a ready Pipeline.
func (b *PipelineBuilder) Build(ctx context.Context) (Pipeline, error) {
	if b.err != nil {
		return nil, b.err
	}
	if b.exporterType == "" {
		return nil, fmt.Errorf("PipelineBuilder: exporter type must be set via ForExporter()")
	}
	instance, err := b.registry.Resolve(ctx, b.exporterType, b.opts)
	if err != nil {
		return nil, fmt.Errorf("PipelineBuilder: %w", err)
	}
	p := NewPipeline(instance, b.middlewares...)
	for _, t := range b.transformers {
		if err := p.AddTransformer(t); err != nil {
			return nil, fmt.Errorf("PipelineBuilder: %w", err)
		}
	}
	return p, nil
}
