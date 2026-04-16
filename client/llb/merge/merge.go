// Package merge provides the BuildKit merge operation for llbx.
//
// Merge overlays multiple filesystem states on top of each other, producing
// a combined state. It is most useful with Diff to create independent patches
// that can be applied in parallel without re-copying shared content.
//
// Example
//
//	combined := merge.New(merge.WithInputs(base, patchA, patchB))
package merge

import (
	"context"
	"fmt"

	"github.com/bons/bons-ci/client/llb/core"
	"github.com/bons/bons-ci/client/llb/marshal"
	"github.com/moby/buildkit/solver/pb"
)

// ─── Config ───────────────────────────────────────────────────────────────────

// Config holds parameters for the merge op.
type Config struct {
	// Inputs is the ordered list of states to merge.
	// The first input forms the base; subsequent inputs are overlaid in order.
	// Scratch (nil Output) inputs are silently filtered out.
	// Requires at least 2 non-scratch inputs.
	Inputs []core.Output

	// Constraints are per-vertex LLB constraints.
	Constraints core.Constraints
}

// ─── Option ───────────────────────────────────────────────────────────────────

// Option is a functional option for Config.
type Option func(*Config)

// WithInputs sets the merge inputs.
func WithInputs(inputs ...core.Output) Option {
	return func(c *Config) { c.Inputs = inputs }
}

// WithConstraintsOption applies a core.ConstraintsOption.
func WithConstraintsOption(opt core.ConstraintsOption) Option {
	return func(c *Config) { opt(&c.Constraints) }
}

// ─── Vertex ───────────────────────────────────────────────────────────────────

// Vertex is the llbx implementation of the merge op.
type Vertex struct {
	config Config
	cache  marshal.Cache
	// filtered holds only the non-nil inputs after scratch removal.
	filtered []core.Output
}

// New constructs a merge Vertex. Returns ErrIncompatibleInputs if fewer than
// 2 non-scratch inputs remain after filtering.
func New(opts ...Option) (*Vertex, error) {
	cfg := Config{}
	for _, o := range opts {
		o(&cfg)
	}
	v := &Vertex{config: cfg}
	v.filter()
	if len(v.filtered) < 2 {
		return nil, &core.IncompatibleInputsError{
			VertexType: core.VertexTypeMerge,
			Got:        len(v.filtered),
			Want:       "at least 2 non-scratch",
			Detail:     "scratch inputs are filtered out",
		}
	}
	return v, nil
}

// filter removes nil (scratch) outputs from the inputs.
func (v *Vertex) filter() {
	v.filtered = v.filtered[:0]
	for _, inp := range v.config.Inputs {
		if inp != nil {
			v.filtered = append(v.filtered, inp)
		}
	}
}

// ─── core.Vertex ──────────────────────────────────────────────────────────────

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
			VertexType: v.Type(),
			Got:        len(v.filtered),
			Want:       "at least 2 non-scratch",
		}
	}
	return nil
}

func (v *Vertex) Marshal(ctx context.Context, c *core.Constraints) (*core.MarshaledVertex, error) {
	h := marshal.Acquire(&v.cache)
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
	pop.Platform = nil // merge is not platform specific

	op := &pb.MergeOp{}
	for i, inp := range v.filtered {
		pbInp, err := inp.ToInput(ctx, c)
		if err != nil {
			return nil, fmt.Errorf("merge.Vertex.Marshal input[%d]: %w", i, err)
		}
		op.Inputs = append(op.Inputs, &pb.MergeInput{Input: int64(len(pop.Inputs))})
		pop.Inputs = append(pop.Inputs, pbInp)
	}
	pop.Op = &pb.Op_Merge{Merge: op}

	bytes, err := marshal.DeterministicMarshal(pop)
	if err != nil {
		return nil, fmt.Errorf("merge.Vertex.Marshal: %w", err)
	}
	dgst, bytes, meta, srcs, _ := h.Store(bytes, md, c.SourceLocations, c)
	return &core.MarshaledVertex{Digest: dgst, Bytes: bytes, Metadata: meta, SourceLocations: srcs}, nil
}

func (v *Vertex) WithInputs(inputs []core.Edge) (core.Vertex, error) {
	if len(inputs) < 2 {
		return nil, &core.IncompatibleInputsError{
			VertexType: v.Type(),
			Got:        len(inputs),
			Want:       "at least 2",
		}
	}
	outputs := make([]core.Output, len(inputs))
	for i, edge := range inputs {
		outputs[i] = &edgeOutput{edge: edge}
	}
	newCfg := v.config
	newCfg.Inputs = outputs
	nv := &Vertex{config: newCfg}
	nv.filter()
	return nv, nil
}

// Output returns a core.Output for the merge result.
func (v *Vertex) Output() core.Output { return &mergeOutput{vertex: v} }

type mergeOutput struct{ vertex *Vertex }

func (o *mergeOutput) Vertex(_ context.Context, _ *core.Constraints) core.Vertex {
	return o.vertex
}
func (o *mergeOutput) ToInput(ctx context.Context, c *core.Constraints) (*pb.Input, error) {
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
