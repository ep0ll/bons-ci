// Package diff provides the BuildKit filesystem diff operation.
package diff

import (
	"context"
	"fmt"

	"github.com/bons/bons-ci/client/llb/core"
	"github.com/bons/bons-ci/client/llb/marshal"
	"github.com/moby/buildkit/solver/pb"
)

// Config holds parameters for the diff op.
type Config struct {
	Lower       core.Output // base state (nil = scratch)
	Upper       core.Output // modified state (nil = scratch)
	Constraints core.Constraints
}

// Option is a functional option for Config.
type Option func(*Config)

func WithLower(out core.Output) Option { return func(c *Config) { c.Lower = out } }
func WithUpper(out core.Output) Option { return func(c *Config) { c.Upper = out } }
func WithConstraintsOption(opt core.ConstraintsOption) Option {
	return func(c *Config) { opt(&c.Constraints) }
}

// Vertex is the diff op vertex.
type Vertex struct {
	config Config
	cache  marshal.Cache
}

// New constructs a diff vertex.
func New(opts ...Option) *Vertex {
	cfg := Config{}
	for _, o := range opts {
		o(&cfg)
	}
	return &Vertex{config: cfg}
}

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
	return []core.OutputSlot{{Index: 0, Description: "filesystem diff (lower→upper)"}}
}

func (v *Vertex) Validate(_ context.Context, _ *core.Constraints) error { return nil }

func (v *Vertex) Marshal(ctx context.Context, c *core.Constraints) (*core.MarshaledVertex, error) {
	h := v.cache.Acquire()
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
			return nil, fmt.Errorf("diff.Marshal lower: %w", err)
		}
		op.Lower.Input = int64(len(pop.Inputs))
		pop.Inputs = append(pop.Inputs, pbInp)
	}

	op.Upper = &pb.UpperDiffInput{Input: int64(pb.Empty)}
	if cfg.Upper != nil {
		pbInp, err := cfg.Upper.ToInput(ctx, c)
		if err != nil {
			return nil, fmt.Errorf("diff.Marshal upper: %w", err)
		}
		op.Upper.Input = int64(len(pop.Inputs))
		pop.Inputs = append(pop.Inputs, pbInp)
	}
	pop.Op = &pb.Op_Diff{Diff: op}

	bytes, err := marshal.DeterministicMarshal(pop)
	if err != nil {
		return nil, fmt.Errorf("diff.Marshal: %w", err)
	}
	dgst, bytes, meta, srcs, _ := h.Store(bytes, md, c.SourceLocations, c)
	return &core.MarshaledVertex{Digest: dgst, Bytes: bytes, Metadata: meta, SourceLocations: srcs}, nil
}

func (v *Vertex) WithInputs(inputs []core.Edge) (core.Vertex, error) {
	newCfg := v.config
	if len(inputs) > 0 {
		newCfg.Lower = &core.EdgeOutput{E: inputs[0]}
	}
	if len(inputs) > 1 {
		newCfg.Upper = &core.EdgeOutput{E: inputs[1]}
	}
	return &Vertex{config: newCfg}, nil
}

func (v *Vertex) Output() core.Output { return &core.SimpleOutput{V: v, Slot: 0} }

var (
	_ core.Vertex         = (*Vertex)(nil)
	_ core.MutatingVertex = (*Vertex)(nil)
)
