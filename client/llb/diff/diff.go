// Package diff provides the BuildKit diff operation for llbx.
//
// Diff computes the filesystem delta between a lower (base) and upper state.
// The result is typically used with Merge to apply independent patches.
//
// Special cases (matching BuildKit behaviour):
//   - Diff(scratch, upper) == upper
//   - Diff(scratch, scratch) == scratch
//
// Example
//
//	patch := diff.New(diff.WithLower(base), diff.WithUpper(modified))
package diff

import (
	"context"
	"fmt"

	"github.com/bons/bons-ci/client/llb/core"
	"github.com/bons/bons-ci/client/llb/marshal"
	"github.com/moby/buildkit/solver/pb"
)

// ─── Config ───────────────────────────────────────────────────────────────────

// Config holds parameters for the diff op.
type Config struct {
	// Lower is the base filesystem state (may be nil for scratch).
	Lower core.Output
	// Upper is the modified filesystem state (may be nil for scratch).
	Upper core.Output
	// Constraints are per-vertex LLB constraints.
	Constraints core.Constraints
}

// ─── Option ───────────────────────────────────────────────────────────────────

// Option is a functional option for Config.
type Option func(*Config)

// WithLower sets the base (lower) state.
func WithLower(out core.Output) Option { return func(c *Config) { c.Lower = out } }

// WithUpper sets the modified (upper) state.
func WithUpper(out core.Output) Option { return func(c *Config) { c.Upper = out } }

// WithConstraintsOption applies a core.ConstraintsOption.
func WithConstraintsOption(opt core.ConstraintsOption) Option {
	return func(c *Config) { opt(&c.Constraints) }
}

// ─── Vertex ───────────────────────────────────────────────────────────────────

// Vertex is the llbx implementation of the diff op.
type Vertex struct {
	config Config
	cache  marshal.Cache
}

// New constructs a diff Vertex.
func New(opts ...Option) *Vertex {
	cfg := Config{}
	for _, o := range opts {
		o(&cfg)
	}
	return &Vertex{config: cfg}
}

// ─── core.Vertex ──────────────────────────────────────────────────────────────

func (v *Vertex) Type() core.VertexType { return core.VertexTypeDiff }

func (v *Vertex) Inputs() []core.Edge {
	var edges []core.Edge
	if v.config.Lower != nil {
		edges = append(edges, core.Edge{Vertex: v.config.Lower.Vertex(context.Background(), nil), Index: 0})
	}
	if v.config.Upper != nil {
		edges = append(edges, core.Edge{Vertex: v.config.Upper.Vertex(context.Background(), nil), Index: 0})
	}
	return edges
}

func (v *Vertex) Outputs() []core.OutputSlot {
	return []core.OutputSlot{{Index: 0, Description: "filesystem diff"}}
}

func (v *Vertex) Validate(_ context.Context, _ *core.Constraints) error { return nil }

func (v *Vertex) Marshal(ctx context.Context, c *core.Constraints) (*core.MarshaledVertex, error) {
	h := marshal.Acquire(&v.cache)
	defer h.Release()
	if dgst, bytes, meta, srcs, err := h.Load(c); err == nil {
		return &core.MarshaledVertex{Digest: dgst, Bytes: bytes, Metadata: meta, SourceLocations: srcs}, nil
	}

	cfg := &v.config
	core.ConstraintsAddCap(&cfg.Constraints, pb.CapDiffOp)

	pop, md := marshal.MarshalConstraints(c, &cfg.Constraints)
	pop.Platform = nil

	op := &pb.DiffOp{}

	op.Lower = &pb.LowerDiffInput{Input: int64(pb.Empty)}
	if cfg.Lower != nil {
		pbInp, err := cfg.Lower.ToInput(ctx, c)
		if err != nil {
			return nil, fmt.Errorf("diff.Vertex.Marshal lower: %w", err)
		}
		op.Lower.Input = int64(len(pop.Inputs))
		pop.Inputs = append(pop.Inputs, pbInp)
	}

	op.Upper = &pb.UpperDiffInput{Input: int64(pb.Empty)}
	if cfg.Upper != nil {
		pbInp, err := cfg.Upper.ToInput(ctx, c)
		if err != nil {
			return nil, fmt.Errorf("diff.Vertex.Marshal upper: %w", err)
		}
		op.Upper.Input = int64(len(pop.Inputs))
		pop.Inputs = append(pop.Inputs, pbInp)
	}

	pop.Op = &pb.Op_Diff{Diff: op}

	bytes, err := marshal.DeterministicMarshal(pop)
	if err != nil {
		return nil, fmt.Errorf("diff.Vertex.Marshal: %w", err)
	}
	dgst, bytes, meta, srcs, _ := h.Store(bytes, md, c.SourceLocations, c)
	return &core.MarshaledVertex{Digest: dgst, Bytes: bytes, Metadata: meta, SourceLocations: srcs}, nil
}

func (v *Vertex) WithInputs(inputs []core.Edge) (core.Vertex, error) {
	newCfg := v.config
	if len(inputs) > 0 {
		newCfg.Lower = &edgeOutput{edge: inputs[0]}
	}
	if len(inputs) > 1 {
		newCfg.Upper = &edgeOutput{edge: inputs[1]}
	}
	return &Vertex{config: newCfg}, nil
}

// Output returns a core.Output for the diff result.
func (v *Vertex) Output() core.Output { return &diffOutput{vertex: v} }

type diffOutput struct{ vertex *Vertex }

func (o *diffOutput) Vertex(_ context.Context, _ *core.Constraints) core.Vertex {
	return o.vertex
}
func (o *diffOutput) ToInput(ctx context.Context, c *core.Constraints) (*pb.Input, error) {
	mv, err := o.vertex.Marshal(ctx, c)
	if err != nil {
		return nil, err
	}
	return &pb.Input{Digest: string(mv.Digest), Index: 0}, nil
}

type edgeOutput struct{ edge core.Edge }

func (e *edgeOutput) Vertex(_ context.Context, _ *core.Constraints) core.Vertex {
	return e.edge.Vertex
}
func (e *edgeOutput) ToInput(ctx context.Context, c *core.Constraints) (*pb.Input, error) {
	mv, err := e.edge.Vertex.Marshal(ctx, c)
	if err != nil {
		return nil, err
	}
	return &pb.Input{Digest: string(mv.Digest), Index: int64(e.edge.Index)}, nil
}

var (
	_ core.Vertex         = (*Vertex)(nil)
	_ core.MutatingVertex = (*Vertex)(nil)
)
