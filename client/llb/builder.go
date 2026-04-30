package llb

import "context"

// ─────────────────────────────────────────────────────────────────────────────
// GraphBuilder — facade over Pipeline (cf. Engine in dirsync)
// ─────────────────────────────────────────────────────────────────────────────

// GraphBuilder is a thin facade that composes the strategy interfaces
// (Resolver, MarshalStrategy, VertexVisitor, StateTransformer) into a unified
// API for constructing and emitting LLB graphs.
//
// Direct use of the individual strategy interfaces is recommended for advanced
// scenarios (custom resolvers, composite visitors, multiple marshal targets).
// GraphBuilder is intended for straightforward "build a graph and marshal"
// use cases.
//
// GraphBuilder mirrors the [Engine] pattern in dirsync.
type GraphBuilder struct {
	resolver   Resolver
	strategy   MarshalStrategy
	visitor    VertexVisitor
	preEmit    StateTransformer
}

// GraphBuilderResult summarizes a completed Build call.
type GraphBuilderResult struct {
	Definition *Definition
	Err        error
}

// OK returns true when Err is nil.
func (r GraphBuilderResult) OK() bool { return r.Err == nil }

// ─────────────────────────────────────────────────────────────────────────────
// Constructors (cf. NewDeleteEngine, NewObserveEngine, NewCustomEngine)
// ─────────────────────────────────────────────────────────────────────────────

// NewGraphBuilder creates a GraphBuilder with the default strategy and no
// visitor. Use GraphBuilderOption to customize.
func NewGraphBuilder(opts ...GraphBuilderOption) *GraphBuilder {
	gb := &GraphBuilder{
		resolver: NoopResolver{},
		strategy: DefaultMarshalStrategy{},
		visitor:  NoopVisitor{},
		preEmit:  NoopTransformer{},
	}
	for _, o := range opts {
		o(gb)
	}
	return gb
}

// NewInstrumentedBuilder creates a GraphBuilder with a visitor that inspects
// each vertex during marshalling — useful for progress reporting, logging, etc.
func NewInstrumentedBuilder(visitor VertexVisitor, opts ...GraphBuilderOption) *GraphBuilder {
	gb := NewGraphBuilder(opts...)
	gb.strategy = InstrumentedMarshalStrategy{
		Inner:   gb.strategy,
		Visitor: visitor,
	}
	return gb
}

// NewCustomBuilder creates a GraphBuilder from pre-constructed components.
// Every component is supplied by the caller — the most flexible constructor.
func NewCustomBuilder(
	resolver Resolver,
	strategy MarshalStrategy,
	visitor VertexVisitor,
	preEmit StateTransformer,
) *GraphBuilder {
	return &GraphBuilder{
		resolver: resolver,
		strategy: strategy,
		visitor:  visitor,
		preEmit:  preEmit,
	}
}

// ─────────────────────────────────────────────────────────────────────────────
// Build
// ─────────────────────────────────────────────────────────────────────────────

// Build resolves, transforms, and marshals the given State into a Definition.
func (gb *GraphBuilder) Build(ctx context.Context, s State, co ...ConstraintsOpt) GraphBuilderResult {
	c := NewConstraints(co...)

	// 1. Resolve (async/lazy resolution).
	resolved, err := gb.resolver.Resolve(ctx, s, c)
	if err != nil {
		return GraphBuilderResult{Err: err}
	}

	// 2. Pre-emit transform.
	transformed, err := gb.preEmit.Transform(ctx, resolved)
	if err != nil {
		return GraphBuilderResult{Err: err}
	}

	// 3. Marshal via strategy.
	def, err := gb.strategy.MarshalGraph(ctx, transformed.Output(), c)
	if err != nil {
		return GraphBuilderResult{Err: err}
	}

	return GraphBuilderResult{Definition: def}
}

// ─────────────────────────────────────────────────────────────────────────────
// GraphBuilderOption — functional options
// ─────────────────────────────────────────────────────────────────────────────

// GraphBuilderOption configures a GraphBuilder.
type GraphBuilderOption func(*GraphBuilder)

// WithResolver sets the state resolver.
func WithResolver(r Resolver) GraphBuilderOption {
	return func(gb *GraphBuilder) { gb.resolver = r }
}

// WithMarshalStrategy sets the marshalling strategy.
func WithMarshalStrategy(s MarshalStrategy) GraphBuilderOption {
	return func(gb *GraphBuilder) { gb.strategy = s }
}

// WithVisitor sets the vertex visitor for DAG observation.
func WithVisitor(v VertexVisitor) GraphBuilderOption {
	return func(gb *GraphBuilder) { gb.visitor = v }
}

// WithPreEmitTransform sets a pre-emission state transformer.
func WithPreEmitTransform(t StateTransformer) GraphBuilderOption {
	return func(gb *GraphBuilder) { gb.preEmit = t }
}
