package conditional

import (
	"context"
	"fmt"

	"github.com/bons/bons-ci/client/llb/core"
	"github.com/bons/bons-ci/client/llb/marshal"
	"github.com/moby/buildkit/solver/pb"
)

// ─── Case ────────────────────────────────────────────────────────────────────

// Case pairs a predicate with an output branch for use in SwitchVertex.
type Case struct {
	Predicate   Predicate
	Output      core.Output
	Description string
}

// ─── SwitchVertex ────────────────────────────────────────────────────────────

// SwitchVertex evaluates multiple Case predicates in order and resolves to the
// first matching branch at definition (marshal) time. If no case matches, the
// optional Default is used. Like Vertex, the switch is transparent – only the
// chosen branch appears in the wire format.
//
// Example
//
//	sw, _ := conditional.NewSwitch(
//	    conditional.SwitchCase(conditional.PlatformOS("linux"), linuxImg.Output(), "linux"),
//	    conditional.SwitchCase(conditional.PlatformOS("windows"), winImg.Output(), "windows"),
//	    conditional.SwitchCase(conditional.PlatformOS("darwin"), macImg.Output(), "darwin"),
//	).WithDefault(fallbackImg.Output()).Build()
type SwitchVertex struct {
	cases       []Case
	defaultOut  core.Output
	description string
	constraints core.Constraints
	cache       marshal.Cache
}

// SwitchCase is a convenience constructor for Case.
func SwitchCase(pred Predicate, out core.Output, desc string) Case {
	return Case{Predicate: pred, Output: out, Description: desc}
}

// SwitchBuilder provides a fluent API for constructing a SwitchVertex.
type SwitchBuilder struct {
	cases       []Case
	defaultOut  core.Output
	description string
	constraints core.Constraints
}

// NewSwitch starts building a switch with the given cases.
func NewSwitch(cases ...Case) *SwitchBuilder {
	return &SwitchBuilder{cases: cases}
}

// WithDefault sets the fallback output when no case matches.
func (b *SwitchBuilder) WithDefault(out core.Output) *SwitchBuilder {
	b.defaultOut = out
	return b
}

// WithDescription sets a human-readable label.
func (b *SwitchBuilder) WithDescription(desc string) *SwitchBuilder {
	b.description = desc
	return b
}

// WithConstraints sets build constraints.
func (b *SwitchBuilder) WithConstraints(c core.Constraints) *SwitchBuilder {
	b.constraints = c
	return b
}

// Build validates and produces the SwitchVertex.
func (b *SwitchBuilder) Build() (*SwitchVertex, error) {
	if len(b.cases) == 0 {
		return nil, fmt.Errorf("conditional.NewSwitch: at least one Case is required")
	}
	for i, c := range b.cases {
		if c.Predicate == nil {
			return nil, fmt.Errorf("conditional.NewSwitch: Case[%d] has nil Predicate", i)
		}
		if c.Output == nil {
			return nil, fmt.Errorf("conditional.NewSwitch: Case[%d] has nil Output", i)
		}
	}
	return &SwitchVertex{
		cases:       b.cases,
		defaultOut:  b.defaultOut,
		description: b.description,
		constraints: b.constraints,
	}, nil
}

// ─── core.Vertex ─────────────────────────────────────────────────────────────

func (v *SwitchVertex) Type() core.VertexType { return core.VertexTypeConditional }

// Inputs returns all branch outputs so graph traversal can discover all vertices.
func (v *SwitchVertex) Inputs() []core.Edge {
	var edges []core.Edge
	for _, c := range v.cases {
		if c.Output != nil {
			edges = append(edges, core.Edge{
				Vertex: c.Output.Vertex(context.Background(), nil), Index: 0,
			})
		}
	}
	if v.defaultOut != nil {
		edges = append(edges, core.Edge{
			Vertex: v.defaultOut.Vertex(context.Background(), nil), Index: 0,
		})
	}
	return edges
}

func (v *SwitchVertex) Outputs() []core.OutputSlot {
	return []core.OutputSlot{{Index: 0, Description: "switch branch result"}}
}

func (v *SwitchVertex) Validate(_ context.Context, _ *core.Constraints) error {
	if len(v.cases) == 0 {
		return &core.ValidationError{Field: "Cases", Cause: fmt.Errorf("must not be empty")}
	}
	return nil
}

// Marshal evaluates each case predicate in order and delegates to the first
// matching branch. Falls back to defaultOut (or scratch) if none match.
func (v *SwitchVertex) Marshal(ctx context.Context, c *core.Constraints) (*core.MarshaledVertex, error) {
	h := v.cache.Acquire()
	defer h.Release()
	if dgst, bytes, meta, srcs, err := h.Load(c); err == nil {
		return &core.MarshaledVertex{Digest: dgst, Bytes: bytes, Metadata: meta, SourceLocations: srcs}, nil
	}

	chosen, err := v.resolve(c)
	if err != nil {
		return nil, fmt.Errorf("conditional.Switch.Marshal: %w", err)
	}
	if chosen == nil {
		return v.marshalScratch(c, h)
	}

	branchVtx := chosen.Vertex(ctx, c)
	if branchVtx == nil {
		return v.marshalScratch(c, h)
	}
	mv, err := branchVtx.Marshal(ctx, c)
	if err != nil {
		return nil, fmt.Errorf("conditional.Switch.Marshal: branch: %w", err)
	}
	dgst, bytes, meta, srcs, _ := h.Store(mv.Bytes, mv.Metadata, mv.SourceLocations, c)
	return &core.MarshaledVertex{Digest: dgst, Bytes: bytes, Metadata: meta, SourceLocations: srcs}, nil
}

func (v *SwitchVertex) resolve(c *core.Constraints) (core.Output, error) {
	for _, cas := range v.cases {
		ok, err := cas.Predicate(c)
		if err != nil {
			return nil, err
		}
		if ok {
			return cas.Output, nil
		}
	}
	return v.defaultOut, nil
}

// SelectedBranch returns the output that would be chosen for the given
// constraints, without serialising. Returns nil if no case matches and no
// default is set.
func (v *SwitchVertex) SelectedBranch(c *core.Constraints) (core.Output, error) {
	return v.resolve(c)
}

func (v *SwitchVertex) marshalScratch(c *core.Constraints, h *marshal.CacheHandle) (*core.MarshaledVertex, error) {
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

func (v *SwitchVertex) WithInputs(inputs []core.Edge) (core.Vertex, error) {
	n := len(v.cases)
	maxInputs := n
	if v.defaultOut != nil {
		maxInputs++
	}
	if len(inputs) > maxInputs {
		return nil, &core.IncompatibleInputsError{
			VertexType: v.Type(),
			Got:        len(inputs),
			Want:       fmt.Sprintf("at most %d", maxInputs),
		}
	}
	newCases := make([]Case, len(v.cases))
	copy(newCases, v.cases)
	var newDefault core.Output = v.defaultOut
	for i, edge := range inputs {
		if i < n {
			newCases[i].Output = &core.EdgeOutput{E: edge}
		} else {
			newDefault = &core.EdgeOutput{E: edge}
		}
	}
	return &SwitchVertex{
		cases:       newCases,
		defaultOut:  newDefault,
		description: v.description,
		constraints: v.constraints,
	}, nil
}

// Output returns a core.Output for the switch result.
func (v *SwitchVertex) Output() core.Output { return &core.SimpleOutput{V: v, Slot: 0} }

var (
	_ core.Vertex         = (*SwitchVertex)(nil)
	_ core.MutatingVertex = (*SwitchVertex)(nil)
)
