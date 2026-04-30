package llb

import (
	"context"

	digest "github.com/opencontainers/go-digest"
)

// ─────────────────────────────────────────────────────────────────────────────
// Resolver — strategy interface for async state resolution
// ─────────────────────────────────────────────────────────────────────────────

// Resolver is the strategy interface for resolving lazy/deferred states.
// It decouples the resolution mechanism from the State type, allowing
// different backends (registry lookup, local cache, federation) to provide
// state resolution without the State type knowing about them.
//
// All implementations must be safe for concurrent use.
type Resolver interface {
	// Resolve resolves the given state into a fully materialized state.
	// The constraints provide context for resolution (platform, caps, etc.).
	Resolve(ctx context.Context, s State, c *Constraints) (State, error)
}

// ResolverFunc adapts a plain function to the Resolver interface.
type ResolverFunc func(ctx context.Context, s State, c *Constraints) (State, error)

// Resolve implements Resolver.
func (f ResolverFunc) Resolve(ctx context.Context, s State, c *Constraints) (State, error) {
	return f(ctx, s, c)
}

// NoopResolver returns the state unchanged. Useful when no async resolution
// is needed.
type NoopResolver struct{}

// Resolve implements Resolver.
func (NoopResolver) Resolve(_ context.Context, s State, _ *Constraints) (State, error) {
	return s, nil
}

// ChainResolver applies resolvers in sequence. Each resolver receives the
// output of the previous one.
type ChainResolver []Resolver

// Resolve implements Resolver.
func (c ChainResolver) Resolve(ctx context.Context, s State, co *Constraints) (State, error) {
	var err error
	for _, r := range c {
		s, err = r.Resolve(ctx, s, co)
		if err != nil {
			return State{}, err
		}
	}
	return s, nil
}

// ─────────────────────────────────────────────────────────────────────────────
// MarshalStrategy — strategy interface for definition emission
// ─────────────────────────────────────────────────────────────────────────────

// MarshalStrategy is the strategy interface for customizing how a DAG is
// serialized into a Definition. The default implementation performs depth-first
// traversal with digest-based deduplication. Custom strategies can add
// instrumentation, filtering, or alternative wire formats.
type MarshalStrategy interface {
	// MarshalGraph serializes the given output into a Definition. The
	// constraints provide serialization context (platform, caps).
	MarshalGraph(ctx context.Context, out Output, c *Constraints) (*Definition, error)
}

// MarshalStrategyFunc adapts a plain function to the MarshalStrategy interface.
type MarshalStrategyFunc func(ctx context.Context, out Output, c *Constraints) (*Definition, error)

// MarshalGraph implements MarshalStrategy.
func (f MarshalStrategyFunc) MarshalGraph(ctx context.Context, out Output, c *Constraints) (*Definition, error) {
	return f(ctx, out, c)
}

// DefaultMarshalStrategy performs the standard depth-first, digest-deduplicated
// traversal used by State.Marshal.
type DefaultMarshalStrategy struct{}

// MarshalGraph implements MarshalStrategy.
func (DefaultMarshalStrategy) MarshalGraph(ctx context.Context, out Output, c *Constraints) (*Definition, error) {
	if out == nil {
		return &Definition{Metadata: make(map[digest.Digest]OpMetadata)}, nil
	}

	def := &Definition{
		Metadata: make(map[digest.Digest]OpMetadata),
	}
	smc := newSourceMapCollector()

	v := out.Vertex(ctx, c)
	if v == nil {
		return def, nil
	}

	vertexCache := make(map[Vertex]struct{})
	digestCache := make(map[digest.Digest]struct{})

	var err error
	def, err = marshalVertex(ctx, v, def, smc, digestCache, vertexCache, c)
	if err != nil {
		return nil, err
	}

	src, err := smc.Marshal(ctx)
	if err != nil {
		return nil, err
	}
	def.Source = src
	def.Constraints = c

	return def, nil
}

// InstrumentedMarshalStrategy wraps another strategy with a VertexVisitor
// that is invoked for each vertex during traversal. This enables logging,
// metrics, progress reporting, or validation without modifying the core
// marshal logic.
type InstrumentedMarshalStrategy struct {
	Inner   MarshalStrategy
	Visitor VertexVisitor
}

// MarshalGraph implements MarshalStrategy.
func (s InstrumentedMarshalStrategy) MarshalGraph(ctx context.Context, out Output, c *Constraints) (*Definition, error) {
	if s.Visitor != nil {
		if err := Walk(ctx, out, s.Visitor, c); err != nil {
			return nil, err
		}
	}
	return s.Inner.MarshalGraph(ctx, out, c)
}
