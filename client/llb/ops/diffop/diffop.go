// Package diffop implements the DiffOp vertex for LLB graph construction.
// A diff computes the filesystem difference between a lower and upper state.
package diffop

import (
	"context"

	"github.com/bons/bons-ci/client/llb"
	"github.com/moby/buildkit/solver/pb"
	digest "github.com/opencontainers/go-digest"
)

// ─────────────────────────────────────────────────────────────────────────────
// DiffOp
// ─────────────────────────────────────────────────────────────────────────────

// DiffOp computes the filesystem difference between a lower base state and an
// upper state. The result is a minimal changeset that, when merged with the
// lower state, reproduces the upper state.
type DiffOp struct {
	cache       llb.MarshalCache
	lower       llb.Output
	upper       llb.Output
	output      llb.Output
	constraints llb.Constraints
}

var _ llb.Vertex = (*DiffOp)(nil)

// NewDiff creates a DiffOp between two states.
func NewDiff(lower, upper llb.State, c llb.Constraints) *DiffOp {
	llb.AddCap(&c, pb.CapDiffOp)
	op := &DiffOp{
		lower:       lower.Output(),
		upper:       upper.Output(),
		constraints: c,
	}
	op.output = llb.NewOutput(op)
	return op
}

// Validate is a no-op for DiffOp — nil lower/upper represent scratch.
func (d *DiffOp) Validate(_ context.Context, _ *llb.Constraints) error {
	return nil
}

// Marshal serializes the DiffOp into a pb.Op with a pb.DiffOp payload.
func (d *DiffOp) Marshal(ctx context.Context, constraints *llb.Constraints) (digest.Digest, []byte, *pb.OpMetadata, []*llb.SourceLocation, error) {
	cache := d.cache.Acquire()
	defer cache.Release()

	if dgst, dt, md, srcs, err := cache.Load(constraints); err == nil {
		return dgst, dt, md, srcs, nil
	}

	if err := d.Validate(ctx, constraints); err != nil {
		return "", nil, nil, nil, err
	}

	pop, md := llb.MarshalConstraints(constraints, &d.constraints)
	pop.Platform = nil // diff is not platform-specific

	op := &pb.DiffOp{}

	op.Lower = &pb.LowerDiffInput{Input: int64(len(pop.Inputs))}
	if d.lower == nil {
		op.Lower.Input = int64(pb.Empty)
	} else {
		pbLowerInput, err := d.lower.ToInput(ctx, constraints)
		if err != nil {
			return "", nil, nil, nil, err
		}
		pop.Inputs = append(pop.Inputs, pbLowerInput)
	}

	op.Upper = &pb.UpperDiffInput{Input: int64(len(pop.Inputs))}
	if d.upper == nil {
		op.Upper.Input = int64(pb.Empty)
	} else {
		pbUpperInput, err := d.upper.ToInput(ctx, constraints)
		if err != nil {
			return "", nil, nil, nil, err
		}
		pop.Inputs = append(pop.Inputs, pbUpperInput)
	}

	pop.Op = &pb.Op_Diff{Diff: op}

	dt, err := llb.DeterministicMarshal(pop)
	if err != nil {
		return "", nil, nil, nil, err
	}

	return cache.Store(dt, md, d.constraints.SourceLocations, constraints)
}

// Output returns the single output of this diff.
func (d *DiffOp) Output() llb.Output { return d.output }

// Inputs returns the non-nil inputs.
func (d *DiffOp) Inputs() []llb.Output {
	var out []llb.Output
	if d.lower != nil {
		out = append(out, d.lower)
	}
	if d.upper != nil {
		out = append(out, d.upper)
	}
	return out
}

// ─────────────────────────────────────────────────────────────────────────────
// Convenience constructor
// ─────────────────────────────────────────────────────────────────────────────

// Diff creates a state representing the filesystem difference between lower
// and upper. Edge cases:
//   - scratch vs scratch → scratch
//   - scratch vs upper  → upper (no diff needed)
func Diff(lower, upper llb.State, opts ...llb.ConstraintsOpt) llb.State {
	if lower.Output() == nil {
		if upper.Output() == nil {
			return llb.Scratch()
		}
		return upper
	}

	var c llb.Constraints
	for _, o := range opts {
		o.SetConstraintsOption(&c)
	}

	return lower.WithOutput(NewDiff(lower, upper, c).Output())
}
