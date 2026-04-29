// Package mergeop implements the MergeOp vertex for LLB graph construction.
// A merge overlays N filesystem states into a single combined state.
package mergeop

import (
	"context"

	"github.com/bons/bons-ci/client/llb"
	"github.com/moby/buildkit/solver/pb"
	digest "github.com/opencontainers/go-digest"
	"github.com/pkg/errors"
)

// ─────────────────────────────────────────────────────────────────────────────
// MergeOp
// ─────────────────────────────────────────────────────────────────────────────

// MergeOp overlays multiple filesystem inputs into a single output. This is
// useful in conjunction with DiffOp to create independent patches that can be
// merged without cache invalidation of unrelated layers.
type MergeOp struct {
	cache       llb.MarshalCache
	inputs      []llb.Output
	output      llb.Output
	constraints llb.Constraints
}

var _ llb.Vertex = (*MergeOp)(nil)

// NewMerge creates a MergeOp from the given input states.
func NewMerge(inputs []llb.State, c llb.Constraints) *MergeOp {
	op := &MergeOp{constraints: c}
	for _, input := range inputs {
		op.inputs = append(op.inputs, input.Output())
	}
	op.output = llb.NewOutput(op)
	return op
}

// Validate ensures the merge has at least 2 non-scratch inputs.
func (m *MergeOp) Validate(_ context.Context, _ *llb.Constraints) error {
	if len(m.inputs) < 2 {
		return errors.Errorf("merge must have at least 2 inputs, got %d", len(m.inputs))
	}
	return nil
}

// Marshal serializes the MergeOp into a pb.Op with a pb.MergeOp payload.
func (m *MergeOp) Marshal(ctx context.Context, constraints *llb.Constraints) (digest.Digest, []byte, *pb.OpMetadata, []*llb.SourceLocation, error) {
	cache := m.cache.Acquire()
	defer cache.Release()

	if dgst, dt, md, srcs, err := cache.Load(constraints); err == nil {
		return dgst, dt, md, srcs, nil
	}

	if err := m.Validate(ctx, constraints); err != nil {
		return "", nil, nil, nil, err
	}

	pop, md := llb.MarshalConstraints(constraints, &m.constraints)
	pop.Platform = nil // merge is not platform-specific

	op := &pb.MergeOp{}
	for _, input := range m.inputs {
		op.Inputs = append(op.Inputs, &pb.MergeInput{Input: int64(len(pop.Inputs))})
		pbInput, err := input.ToInput(ctx, constraints)
		if err != nil {
			return "", nil, nil, nil, err
		}
		pop.Inputs = append(pop.Inputs, pbInput)
	}
	pop.Op = &pb.Op_Merge{Merge: op}

	dt, err := llb.DeterministicMarshal(pop)
	if err != nil {
		return "", nil, nil, nil, err
	}

	return cache.Store(dt, md, m.constraints.SourceLocations, constraints)
}

// Output returns the single output of this merge.
func (m *MergeOp) Output() llb.Output { return m.output }

// Inputs returns all input outputs.
func (m *MergeOp) Inputs() []llb.Output { return m.inputs }

// ─────────────────────────────────────────────────────────────────────────────
// Convenience constructor
// ─────────────────────────────────────────────────────────────────────────────

// Merge creates a merged state from multiple input states. Scratch inputs are
// filtered out. A single remaining input is returned as-is. Zero remaining
// inputs return scratch.
func Merge(inputs []llb.State, opts ...llb.ConstraintsOpt) llb.State {
	var filtered []llb.State
	for _, input := range inputs {
		if input.Output() != nil {
			filtered = append(filtered, input)
		}
	}
	if len(filtered) == 0 {
		return llb.Scratch()
	}
	if len(filtered) == 1 {
		return filtered[0]
	}

	var c llb.Constraints
	for _, o := range opts {
		o.SetConstraintsOption(&c)
	}
	llb.AddCap(&c, pb.CapMergeOp)

	return filtered[0].WithOutput(NewMerge(filtered, c).Output())
}
