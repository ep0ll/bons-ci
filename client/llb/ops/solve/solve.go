// Package solve provides the SolveOp vertex that wraps an entire sub-DAG as a
// single build vertex, mirroring BuildKit's llbbuild.Build. At marshal time the
// sub-graph is serialised into a nested pb.BuildOp definition, enabling
// independently-constructed DAGs to be composed as inputs to other operations.
//
// Example
//
//	subgraph, _ := image.New(image.WithRef("alpine:3.20"))
//	solve, _ := solve.New(solve.WithInput(subgraph.Output()))
//	result := state.From(solve.Output())
package solve

import (
	"context"
	"fmt"

	"github.com/bons/bons-ci/client/llb/core"
	"github.com/bons/bons-ci/client/llb/marshal"
	"github.com/moby/buildkit/solver/pb"
)

// ─── Config ──────────────────────────────────────────────────────────────────

// Config holds all parameters for the solve op.
type Config struct {
	// Input is the root output of the sub-graph to solve. Required.
	Input core.Output
	// EntryPoint selects a specific output from a multi-output sub-graph.
	// Empty string selects the default (index 0).
	EntryPoint string
	// Description is a human-readable label attached to the vertex.
	Description string
	Constraints core.Constraints
}

// Option is a functional option for Config.
type Option func(*Config)

func WithInput(out core.Output) Option       { return func(c *Config) { c.Input = out } }
func WithEntryPoint(ep string) Option        { return func(c *Config) { c.EntryPoint = ep } }
func WithDescription(desc string) Option     { return func(c *Config) { c.Description = desc } }
func WithConstraintsOption(opt core.ConstraintsOption) Option {
	return func(c *Config) { opt(&c.Constraints) }
}

// ─── Vertex ──────────────────────────────────────────────────────────────────

// Vertex wraps a sub-DAG as a single BuildOp vertex. The sub-graph is fully
// serialised at marshal time into a nested definition, making it opaque to
// the parent graph while preserving content-addressability.
type Vertex struct {
	config Config
	cache  marshal.Cache
}

// New constructs a solve vertex.
func New(opts ...Option) (*Vertex, error) {
	cfg := Config{}
	for _, o := range opts {
		o(&cfg)
	}
	if cfg.Input == nil {
		return nil, fmt.Errorf("solve.New: Input is required")
	}
	return &Vertex{config: cfg}, nil
}

// ─── core.Vertex ─────────────────────────────────────────────────────────────

func (v *Vertex) Type() core.VertexType { return core.VertexTypeSolve }

func (v *Vertex) Inputs() []core.Edge {
	if v.config.Input == nil {
		return nil
	}
	vtx := v.config.Input.Vertex(context.Background(), nil)
	if vtx == nil {
		return nil
	}
	return []core.Edge{{Vertex: vtx, Index: 0}}
}

func (v *Vertex) Outputs() []core.OutputSlot {
	return []core.OutputSlot{{Index: 0, Description: "solve result"}}
}

func (v *Vertex) Validate(_ context.Context, _ *core.Constraints) error {
	if v.config.Input == nil {
		return &core.ValidationError{Field: "Input", Cause: fmt.Errorf("must not be nil")}
	}
	return nil
}

func (v *Vertex) Marshal(ctx context.Context, c *core.Constraints) (*core.MarshaledVertex, error) {
	h := v.cache.Acquire()
	defer h.Release()
	if dgst, bytes, meta, srcs, err := h.Load(c); err == nil {
		return &core.MarshaledVertex{Digest: dgst, Bytes: bytes, Metadata: meta, SourceLocations: srcs}, nil
	}

	if err := v.Validate(ctx, c); err != nil {
		return nil, err
	}

	// Serialise the sub-graph into a nested Definition.
	subDef, err := marshal.NewSerializer().Serialize(ctx, v.config.Input, c)
	if err != nil {
		return nil, &core.SolveError{Cause: fmt.Errorf("serialize sub-graph: %w", err)}
	}
	pbDef := subDef.ToPB()

	cfg := &v.config
	pop, md := marshal.MarshalConstraints(c, &cfg.Constraints)

	attrs := make(map[string]string)
	if cfg.EntryPoint != "" {
		attrs["entrypoint"] = cfg.EntryPoint
	}
	if cfg.Description != "" {
		attrs["description"] = cfg.Description
	}

	pop.Op = &pb.Op_Build{Build: &pb.BuildOp{
		Builder: int64(pb.LLBBuilder),
		Inputs: map[string]*pb.BuildInput{
			pb.LLBDefinitionInput: {Input: int64(pb.Empty)},
		},
		Def:   pbDef,
		Attrs: attrs,
	}}

	bytes, err := marshal.DeterministicMarshal(pop)
	if err != nil {
		return nil, &core.SolveError{Cause: fmt.Errorf("marshal: %w", err)}
	}
	dgst, bytes, meta, srcs, _ := h.Store(bytes, md, c.SourceLocations, c)
	return &core.MarshaledVertex{Digest: dgst, Bytes: bytes, Metadata: meta, SourceLocations: srcs}, nil
}

func (v *Vertex) WithInputs(inputs []core.Edge) (core.Vertex, error) {
	switch len(inputs) {
	case 0:
		return v, nil
	case 1:
		newCfg := v.config
		newCfg.Input = &core.EdgeOutput{E: inputs[0]}
		return &Vertex{config: newCfg}, nil
	default:
		return nil, &core.IncompatibleInputsError{
			VertexType: v.Type(), Got: len(inputs), Want: "0 or 1",
		}
	}
}

// Output returns a core.Output for output slot 0.
func (v *Vertex) Output() core.Output { return &core.SimpleOutput{V: v, Slot: 0} }

// Config returns a copy of the configuration.
func (v *Vertex) Config() Config { return v.config }

var (
	_ core.Vertex         = (*Vertex)(nil)
	_ core.MutatingVertex = (*Vertex)(nil)
)
