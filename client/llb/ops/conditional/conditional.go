// Package conditional provides a compile-time if/else vertex that evaluates a
// predicate against build arguments and resolves to one of two branches at
// definition (marshal) time.
//
// Because the branch is selected during graph construction – not at runtime
// inside the BuildKit daemon – the resulting wire definition contains only the
// chosen branch. This keeps the graph minimal and avoids sending unused
// operations to the daemon.
//
// Example
//
//	// Choose alpine for Linux, nanoserver for Windows.
//	src, _ := conditional.New(
//	    conditional.WithPredicate(func(c *core.Constraints) (bool, error) {
//	        return c.Platform != nil && c.Platform.OS == "windows", nil
//	    }),
//	    conditional.WithThen(nanoserver.Output()),
//	    conditional.WithElse(alpine.Output()),
//	)
package conditional

import (
	"context"
	"fmt"

	"github.com/bons/bons-ci/client/llb/core"
	"github.com/bons/bons-ci/client/llb/marshal"
	"github.com/moby/buildkit/solver/pb"
)

// Predicate is a function evaluated at definition time against build constraints.
// It returns true to select the "then" branch, false for the "else" branch.
type Predicate func(c *core.Constraints) (bool, error)

// ─── Predicate helpers ────────────────────────────────────────────────────────

// BuildArgEquals returns a Predicate that checks whether the named build argument
// equals the expected value.
func BuildArgEquals(key, expected string) Predicate {
	return func(c *core.Constraints) (bool, error) {
		v, _ := c.BuildArg(key)
		return v == expected, nil
	}
}

// BuildArgTrue returns a Predicate that is true when the build arg is "1" or "true".
func BuildArgTrue(key string) Predicate {
	return func(c *core.Constraints) (bool, error) {
		v, ok := c.BuildArg(key)
		if !ok {
			return false, nil
		}
		return v == "1" || v == "true" || v == "yes", nil
	}
}

// PlatformOS returns a Predicate that checks the target OS.
func PlatformOS(os string) Predicate {
	return func(c *core.Constraints) (bool, error) {
		if c.Platform == nil {
			return false, nil
		}
		return c.Platform.OS == os, nil
	}
}

// PlatformArch returns a Predicate that checks the target architecture.
func PlatformArch(arch string) Predicate {
	return func(c *core.Constraints) (bool, error) {
		if c.Platform == nil {
			return false, nil
		}
		return c.Platform.Architecture == arch, nil
	}
}

// And combines predicates with logical AND.
func And(predicates ...Predicate) Predicate {
	return func(c *core.Constraints) (bool, error) {
		for _, p := range predicates {
			v, err := p(c)
			if err != nil || !v {
				return v, err
			}
		}
		return true, nil
	}
}

// Or combines predicates with logical OR.
func Or(predicates ...Predicate) Predicate {
	return func(c *core.Constraints) (bool, error) {
		for _, p := range predicates {
			v, err := p(c)
			if err != nil {
				return false, err
			}
			if v {
				return true, nil
			}
		}
		return false, nil
	}
}

// Not negates a predicate.
func Not(p Predicate) Predicate {
	return func(c *core.Constraints) (bool, error) {
		v, err := p(c)
		return !v, err
	}
}

// ─── Config ──────────────────────────────────────────────────────────────────

// Config holds the parameters for the conditional vertex.
type Config struct {
	// Predicate is evaluated at marshal time against the build constraints.
	// Required.
	Predicate Predicate
	// Then is the output selected when Predicate returns true.
	Then core.Output
	// Else is the output selected when Predicate returns false.
	// May be nil (scratch).
	Else core.Output
	// Description is a human-readable label for debugging.
	Description string
	Constraints core.Constraints
}

// Option is a functional option for Config.
type Option func(*Config)

func WithPredicate(p Predicate) Option { return func(c *Config) { c.Predicate = p } }
func WithThen(out core.Output) Option  { return func(c *Config) { c.Then = out } }
func WithElse(out core.Output) Option  { return func(c *Config) { c.Else = out } }
func WithDescription(d string) Option  { return func(c *Config) { c.Description = d } }
func WithConstraintsOption(opt core.ConstraintsOption) Option {
	return func(c *Config) { opt(&c.Constraints) }
}

// ─── Vertex ──────────────────────────────────────────────────────────────────

// Vertex is the conditional op. At marshal time it evaluates the predicate,
// delegates entirely to the selected branch, and returns that branch's
// MarshaledVertex unmodified. The conditional vertex itself leaves no trace
// in the wire format – the chosen branch's op appears directly.
type Vertex struct {
	config Config
	cache  marshal.Cache
}

// New constructs a conditional vertex.
func New(opts ...Option) (*Vertex, error) {
	cfg := Config{}
	for _, o := range opts {
		o(&cfg)
	}
	if cfg.Predicate == nil {
		return nil, fmt.Errorf("conditional.New: Predicate is required")
	}
	if cfg.Then == nil {
		return nil, fmt.Errorf("conditional.New: Then branch is required")
	}
	return &Vertex{config: cfg}, nil
}

// ─── core.Vertex ─────────────────────────────────────────────────────────────

func (v *Vertex) Type() core.VertexType { return core.VertexTypeConditional }

// Inputs returns both branches so graph traversal can discover all vertices.
func (v *Vertex) Inputs() []core.Edge {
	var edges []core.Edge
	if v.config.Then != nil {
		edges = append(edges, core.Edge{
			Vertex: v.config.Then.Vertex(context.Background(), nil), Index: 0,
		})
	}
	if v.config.Else != nil {
		edges = append(edges, core.Edge{
			Vertex: v.config.Else.Vertex(context.Background(), nil), Index: 0,
		})
	}
	return edges
}

func (v *Vertex) Outputs() []core.OutputSlot {
	return []core.OutputSlot{{Index: 0, Description: "conditional branch result"}}
}

func (v *Vertex) Validate(_ context.Context, _ *core.Constraints) error {
	if v.config.Predicate == nil {
		return &core.ValidationError{Field: "Predicate", Cause: fmt.Errorf("must not be nil")}
	}
	if v.config.Then == nil {
		return &core.ValidationError{Field: "Then", Cause: fmt.Errorf("must not be nil")}
	}
	return nil
}

// Marshal evaluates the predicate and delegates to the chosen branch.
// The conditional vertex itself produces no additional op node.
func (v *Vertex) Marshal(ctx context.Context, c *core.Constraints) (*core.MarshaledVertex, error) {
	h := v.cache.Acquire()
	defer h.Release()
	if dgst, bytes, meta, srcs, err := h.Load(c); err == nil {
		return &core.MarshaledVertex{Digest: dgst, Bytes: bytes, Metadata: meta, SourceLocations: srcs}, nil
	}

	chosen, err := v.resolve(c)
	if err != nil {
		return nil, fmt.Errorf("conditional.Marshal: predicate: %w", err)
	}
	if chosen == nil {
		// Both branches are nil (scratch). Return an empty marker vertex.
		return v.marshalScratch(ctx, c, h)
	}

	branchVtx := chosen.Vertex(ctx, c)
	if branchVtx == nil {
		return v.marshalScratch(ctx, c, h)
	}
	mv, err := branchVtx.Marshal(ctx, c)
	if err != nil {
		return nil, fmt.Errorf("conditional.Marshal: branch: %w", err)
	}
	// Store under this vertex's constraints key so subsequent calls hit the cache.
	dgst, bytes, meta, srcs, _ := h.Store(mv.Bytes, mv.Metadata, mv.SourceLocations, c)
	return &core.MarshaledVertex{Digest: dgst, Bytes: bytes, Metadata: meta, SourceLocations: srcs}, nil
}

func (v *Vertex) marshalScratch(
	ctx context.Context,
	c *core.Constraints,
	h *marshal.CacheHandle,
) (*core.MarshaledVertex, error) {
	// Scratch has no canonical op; produce a zero-input source op as placeholder.
	pop, md := marshal.MarshalConstraints(c, &v.config.Constraints)
	pop.Platform = nil
	pop.Op = &pb.Op_Source{Source: &pb.SourceOp{Identifier: "scratch://"}}
	bytes, err := marshal.DeterministicMarshal(pop)
	if err != nil {
		return nil, err
	}
	dgst, bytes, meta, srcs, _ := h.Store(bytes, md, c.SourceLocations, c)
	return &core.MarshaledVertex{Digest: dgst, Bytes: bytes, Metadata: meta, SourceLocations: srcs}, nil
}

func (v *Vertex) resolve(c *core.Constraints) (core.Output, error) {
	ok, err := v.config.Predicate(c)
	if err != nil {
		return nil, err
	}
	if ok {
		return v.config.Then, nil
	}
	return v.config.Else, nil
}

// SelectedBranch returns the output that would be chosen for the given
// constraints, without serialising.
func (v *Vertex) SelectedBranch(c *core.Constraints) (core.Output, error) {
	return v.resolve(c)
}

func (v *Vertex) WithInputs(inputs []core.Edge) (core.Vertex, error) {
	newCfg := v.config
	switch len(inputs) {
	case 0:
	case 1:
		newCfg.Then = &core.EdgeOutput{E: inputs[0]}
	case 2:
		newCfg.Then = &core.EdgeOutput{E: inputs[0]}
		newCfg.Else = &core.EdgeOutput{E: inputs[1]}
	default:
		return nil, &core.IncompatibleInputsError{
			VertexType: v.Type(), Got: len(inputs), Want: "0, 1, or 2",
		}
	}
	return &Vertex{config: newCfg}, nil
}

// Output returns a core.Output that resolves lazily at ToInput time.
func (v *Vertex) Output() core.Output { return &core.SimpleOutput{V: v, Slot: 0} }

var (
	_ core.Vertex         = (*Vertex)(nil)
	_ core.MutatingVertex = (*Vertex)(nil)
)
