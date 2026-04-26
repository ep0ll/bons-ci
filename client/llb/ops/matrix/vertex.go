package matrix

import (
	"context"
	"fmt"

	"github.com/bons/bons-ci/client/llb/core"
	"github.com/bons/bons-ci/client/llb/marshal"
	mergeop "github.com/bons/bons-ci/client/llb/ops/merge"
	"github.com/moby/buildkit/solver/pb"
)

// ─── Vertex ──────────────────────────────────────────────────────────────────

// Vertex is a first-class DAG node that stores axes and a template vertex.
// At marshal time it expands the cartesian product of all axes, serialises
// the template under each parameter configuration, and produces a merge of
// all expansions. This makes the matrix visible to DAG traversal, reactive
// events, and graph mutation.
//
// For eager (non-vertex) expansion, use Expand() or ExplicitExpand().
//
// Example
//
//	template, _ := image.New(image.WithRef("alpine:3.20"))
//	mv, _ := matrix.NewVertex(
//	    matrix.VertexWithTemplate(template),
//	    matrix.VertexWithAxes(
//	        matrix.NewAxis("GO_VERSION", "1.21", "1.22"),
//	        matrix.NewAxis("GOOS", "linux", "darwin"),
//	    ),
//	)
type Vertex struct {
	template    core.Vertex
	axes        []Axis
	configs     []ParamMap // explicit configs; if set, axes are ignored
	description string
	constraints core.Constraints
	cache       marshal.Cache
}

// VertexOption is a functional option for Vertex.
type VertexOption func(*Vertex)

func VertexWithTemplate(v core.Vertex) VertexOption {
	return func(m *Vertex) { m.template = v }
}

func VertexWithAxes(axes ...Axis) VertexOption {
	return func(m *Vertex) { m.axes = axes }
}

func VertexWithConfigs(configs []ParamMap) VertexOption {
	return func(m *Vertex) { m.configs = configs }
}

func VertexWithDescription(desc string) VertexOption {
	return func(m *Vertex) { m.description = desc }
}

func VertexWithConstraintsOption(opt core.ConstraintsOption) VertexOption {
	return func(m *Vertex) { opt(&m.constraints) }
}

// NewVertex constructs a matrix vertex.
func NewVertex(opts ...VertexOption) (*Vertex, error) {
	v := &Vertex{}
	for _, o := range opts {
		o(v)
	}
	if v.template == nil {
		return nil, fmt.Errorf("matrix.NewVertex: Template is required")
	}
	if len(v.axes) == 0 && len(v.configs) == 0 {
		return nil, fmt.Errorf("matrix.NewVertex: at least one Axis or Config is required")
	}
	return v, nil
}

// ─── core.Vertex ─────────────────────────────────────────────────────────────

func (v *Vertex) Type() core.VertexType { return core.VertexTypeMatrix }

func (v *Vertex) Inputs() []core.Edge {
	if v.template == nil {
		return nil
	}
	return []core.Edge{{Vertex: v.template, Index: 0}}
}

func (v *Vertex) Outputs() []core.OutputSlot {
	return []core.OutputSlot{{Index: 0, Description: "matrix expansion result"}}
}

func (v *Vertex) Validate(_ context.Context, _ *core.Constraints) error {
	if v.template == nil {
		return &core.ValidationError{Field: "Template", Cause: fmt.Errorf("must not be nil")}
	}
	return nil
}

// Marshal expands the matrix and serialises a merge of all configurations.
// Each configuration is applied as build arguments to a clone of the
// constraints, producing distinct content addresses per expansion.
func (v *Vertex) Marshal(ctx context.Context, c *core.Constraints) (*core.MarshaledVertex, error) {
	h := v.cache.Acquire()
	defer h.Release()
	if dgst, bytes, meta, srcs, err := h.Load(c); err == nil {
		return &core.MarshaledVertex{Digest: dgst, Bytes: bytes, Metadata: meta, SourceLocations: srcs}, nil
	}

	if err := v.Validate(ctx, c); err != nil {
		return nil, err
	}

	configs := v.configs
	if len(configs) == 0 {
		configs = cartesian(v.axes)
	}
	if len(configs) == 0 {
		configs = []ParamMap{{}}
	}

	// Create one configOutput per expansion.
	outputs := make([]core.Output, 0, len(configs))
	for _, params := range configs {
		cc := c.Clone()
		for k, val := range params {
			cc.BuildArgs[k] = val
		}
		outputs = append(outputs, &configOutput{vertex: v.template, c: cc})
	}

	// If only one expansion, return it directly.
	if len(outputs) == 1 {
		outVtx := outputs[0].Vertex(ctx, c)
		if outVtx == nil {
			return v.marshalScratch(c, h)
		}
		mv, err := outVtx.Marshal(ctx, outputs[0].(*configOutput).c)
		if err != nil {
			return nil, fmt.Errorf("matrix.Marshal single expansion: %w", err)
		}
		dgst, bytes, meta, srcs, _ := h.Store(mv.Bytes, mv.Metadata, mv.SourceLocations, c)
		return &core.MarshaledVertex{Digest: dgst, Bytes: bytes, Metadata: meta, SourceLocations: srcs}, nil
	}

	// Multiple expansions → produce a merge vertex.
	mergeV, err := mergeop.New(mergeop.WithInputs(outputs...))
	if err != nil {
		return nil, fmt.Errorf("matrix.Marshal: merge expansions: %w", err)
	}
	mv, err := mergeV.Marshal(ctx, c)
	if err != nil {
		return nil, fmt.Errorf("matrix.Marshal: marshal merge: %w", err)
	}
	dgst, bytes, meta, srcs, _ := h.Store(mv.Bytes, mv.Metadata, mv.SourceLocations, c)
	return &core.MarshaledVertex{Digest: dgst, Bytes: bytes, Metadata: meta, SourceLocations: srcs}, nil
}

func (v *Vertex) marshalScratch(c *core.Constraints, h *marshal.CacheHandle) (*core.MarshaledVertex, error) {
	pop, md := marshal.MarshalConstraints(c, &v.constraints)
	pop.Platform = nil
	pop.Op = &pb.Op_Source{Source: &pb.SourceOp{Identifier: "scratch://"}}
	bytes, err := marshal.DeterministicMarshal(pop)
	if err != nil {
		return nil, err
	}
	dgst, bytes, meta, srcs, _ := h.Store(bytes, md, c.SourceLocations, c)
	return &core.MarshaledVertex{Digest: dgst, Bytes: bytes, Metadata: meta, SourceLocations: srcs}, nil
}

func (v *Vertex) WithInputs(inputs []core.Edge) (core.Vertex, error) {
	if len(inputs) > 1 {
		return nil, &core.IncompatibleInputsError{
			VertexType: v.Type(), Got: len(inputs), Want: "0 or 1",
		}
	}
	nv := *v
	nv.cache = marshal.Cache{}
	if len(inputs) == 1 {
		nv.template = inputs[0].Vertex
	}
	return &nv, nil
}

// Output returns a core.Output for the matrix result.
func (v *Vertex) Output() core.Output { return &core.SimpleOutput{V: v, Slot: 0} }

// Template returns the template vertex.
func (v *Vertex) Template() core.Vertex { return v.template }

// Axes returns the configured axes.
func (v *Vertex) Axes() []Axis { return v.axes }

// ExpandedConfigs returns the concrete parameter maps that will be expanded.
func (v *Vertex) ExpandedConfigs() []ParamMap {
	if len(v.configs) > 0 {
		return v.configs
	}
	return cartesian(v.axes)
}

var (
	_ core.Vertex         = (*Vertex)(nil)
	_ core.MutatingVertex = (*Vertex)(nil)
)
