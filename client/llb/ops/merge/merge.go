// Package merge provides the BuildKit overlay-merge operation.
package merge

import (
	"context"
	"fmt"

	"github.com/bons/bons-ci/client/llb/core"
	"github.com/bons/bons-ci/client/llb/marshal"
	"github.com/moby/buildkit/solver/pb"
)

// Config holds parameters for the merge op.
type Config struct {
	// Inputs is the ordered list of states to merge.
	// Nil (scratch) inputs are silently filtered out.
	// At least 2 non-scratch inputs are required.
	Inputs      []core.Output
	Constraints core.Constraints
}

// Option is a functional option for Config.
type Option func(*Config)

func WithInputs(inputs ...core.Output) Option {
	return func(c *Config) { c.Inputs = inputs }
}
func WithConstraintsOption(opt core.ConstraintsOption) Option {
	return func(c *Config) { opt(&c.Constraints) }
}

// Vertex is the merge op vertex.
type Vertex struct {
	config   Config
	cache    marshal.Cache
	filtered []core.Output // scratch-filtered inputs
}

// New constructs a merge vertex.
func New(opts ...Option) (*Vertex, error) {
	cfg := Config{}
	for _, o := range opts {
		o(&cfg)
	}
	v := &Vertex{config: cfg}
	v.filterScratch()
	if len(v.filtered) < 2 {
		return nil, &core.IncompatibleInputsError{
			VertexType: core.VertexTypeMerge,
			Got:        len(v.filtered),
			Want:       "at least 2 non-scratch",
			Detail:     "scratch inputs are silently dropped",
		}
	}
	return v, nil
}

func (v *Vertex) filterScratch() {
	v.filtered = v.filtered[:0]
	for _, inp := range v.config.Inputs {
		if inp != nil {
			v.filtered = append(v.filtered, inp)
		}
	}
}

func (v *Vertex) Type() core.VertexType { return core.VertexTypeMerge }

func (v *Vertex) Inputs() []core.Edge {
	edges := make([]core.Edge, len(v.filtered))
	for i, inp := range v.filtered {
		edges[i] = core.Edge{Vertex: inp.Vertex(context.Background(), nil), Index: 0}
	}
	return edges
}

func (v *Vertex) Outputs() []core.OutputSlot {
	return []core.OutputSlot{{Index: 0, Description: "merged filesystem"}}
}

func (v *Vertex) Validate(_ context.Context, _ *core.Constraints) error {
	if len(v.filtered) < 2 {
		return &core.IncompatibleInputsError{
			VertexType: v.Type(), Got: len(v.filtered), Want: "at least 2 non-scratch",
		}
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

	cfg := &v.config
	core.ConstraintsAddCap(&cfg.Constraints, pb.CapMergeOp)
	pop, md := marshal.MarshalConstraints(c, &cfg.Constraints)
	pop.Platform = nil

	op := &pb.MergeOp{}
	for i, inp := range v.filtered {
		pbInp, err := inp.ToInput(ctx, c)
		if err != nil {
			return nil, fmt.Errorf("merge.Marshal input[%d]: %w", i, err)
		}
		op.Inputs = append(op.Inputs, &pb.MergeInput{Input: int64(len(pop.Inputs))})
		pop.Inputs = append(pop.Inputs, pbInp)
	}
	pop.Op = &pb.Op_Merge{Merge: op}

	bytes, err := marshal.DeterministicMarshal(pop)
	if err != nil {
		return nil, fmt.Errorf("merge.Marshal: %w", err)
	}
	dgst, bytes, meta, srcs, _ := h.Store(bytes, md, c.SourceLocations, c)
	return &core.MarshaledVertex{Digest: dgst, Bytes: bytes, Metadata: meta, SourceLocations: srcs}, nil
}

func (v *Vertex) WithInputs(inputs []core.Edge) (core.Vertex, error) {
	if len(inputs) < 2 {
		return nil, &core.IncompatibleInputsError{
			VertexType: v.Type(), Got: len(inputs), Want: "at least 2",
		}
	}
	outputs := make([]core.Output, len(inputs))
	for i, e := range inputs {
		outputs[i] = &edgeOutput{edge: e}
	}
	newCfg := v.config
	newCfg.Inputs = outputs
	nv := &Vertex{config: newCfg}
	nv.filterScratch()
	return nv, nil
}

func (v *Vertex) Output() core.Output { return &mergeOutput{v: v} }

type mergeOutput struct{ v *Vertex }

func (o *mergeOutput) Vertex(_ context.Context, _ *core.Constraints) core.Vertex { return o.v }
func (o *mergeOutput) ToInput(ctx context.Context, c *core.Constraints) (*pb.Input, error) {
	mv, err := o.v.Marshal(ctx, c)
	if err != nil {
		return nil, err
	}
	return &pb.Input{Digest: string(mv.Digest), Index: 0}, nil
}

type edgeOutput struct{ edge core.Edge }

func (e *edgeOutput) Vertex(_ context.Context, _ *core.Constraints) core.Vertex { return e.edge.Vertex }
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
