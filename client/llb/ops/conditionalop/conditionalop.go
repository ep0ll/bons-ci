// Package conditionalop implements the ConditionalOp vertex — a control-flow
// vertex that selects one of N branches based on a condition predicate
// evaluated at marshal time.
package conditionalop

import (
	"context"

	"github.com/bons/bons-ci/client/llb"
	"github.com/moby/buildkit/solver/pb"
	digest "github.com/opencontainers/go-digest"
	"github.com/pkg/errors"
)

// ─────────────────────────────────────────────────────────────────────────────
// ConditionalOp
// ─────────────────────────────────────────────────────────────────────────────

// ConditionalOp selects one of N branches based on a Condition. The condition
// is evaluated at marshal time, making the branch selection static in the
// emitted definition. This allows the solver to only build the selected branch.
type ConditionalOp struct {
	cache       llb.MarshalCache
	condition   Condition
	branches    []Branch
	defaultBr   *llb.State
	constraints llb.Constraints
	output      llb.Output

	// resolved is the lazily-resolved selected vertex, set during first marshal.
	resolved llb.Vertex
}

var _ llb.Vertex = (*ConditionalOp)(nil)

// Branch maps a key to a State. When the condition evaluates to the key,
// the corresponding state is selected.
type Branch struct {
	Key   string
	State llb.State
}

// NewConditionalOp creates a ConditionalOp.
func NewConditionalOp(cond Condition, branches []Branch, defaultState *llb.State, c llb.Constraints) *ConditionalOp {
	op := &ConditionalOp{
		condition:   cond,
		branches:    branches,
		defaultBr:   defaultState,
		constraints: c,
	}
	op.output = llb.NewOutput(op)
	return op
}

// Validate checks the conditional op.
func (c *ConditionalOp) Validate(_ context.Context, _ *llb.Constraints) error {
	if c.condition == nil {
		return errors.New("conditional op requires a condition")
	}
	if len(c.branches) == 0 && c.defaultBr == nil {
		return errors.New("conditional op requires at least one branch or a default")
	}
	return nil
}

// Marshal evaluates the condition, selects a branch, and marshals the selected
// vertex.
func (c *ConditionalOp) Marshal(ctx context.Context, constraints *llb.Constraints) (digest.Digest, []byte, *pb.OpMetadata, []*llb.SourceLocation, error) {
	cache := c.cache.Acquire()
	defer cache.Release()

	if dgst, dt, md, srcs, err := cache.Load(constraints); err == nil {
		return dgst, dt, md, srcs, nil
	}

	if err := c.Validate(ctx, constraints); err != nil {
		return "", nil, nil, nil, err
	}

	// Evaluate condition.
	key, err := c.condition.Evaluate(ctx)
	if err != nil {
		return "", nil, nil, nil, errors.Wrap(err, "conditional evaluation failed")
	}

	// Select branch.
	selected := c.selectBranch(key)
	if selected == nil {
		return "", nil, nil, nil, errors.Errorf("no branch matched key %q and no default provided", key)
	}

	// The conditional op delegates to the selected branch's vertex.
	v := selected.Output()
	if v == nil {
		// selected is scratch — emit an empty op
		pop, md := llb.MarshalConstraints(constraints, &c.constraints)
		pop.Op = &pb.Op_Source{Source: &pb.SourceOp{
			Identifier: "conditional://" + key,
			Attrs:      map[string]string{"conditional.key": key},
		}}
		dt, err := llb.DeterministicMarshal(pop)
		if err != nil {
			return "", nil, nil, nil, err
		}
		return cache.Store(dt, md, c.constraints.SourceLocations, constraints)
	}

	vertex := v.Vertex(ctx, constraints)
	if vertex == nil {
		return "", nil, nil, nil, errors.New("selected branch has nil vertex")
	}
	c.resolved = vertex

	return vertex.Marshal(ctx, constraints)
}

// selectBranch returns the state matching the condition key, or the default.
func (c *ConditionalOp) selectBranch(key string) *llb.State {
	for _, br := range c.branches {
		if br.Key == key {
			return &br.State
		}
	}
	return c.defaultBr
}

// Output returns the output — this delegates to the selected branch at
// marshal time.
func (c *ConditionalOp) Output() llb.Output { return c.output }

// Inputs returns all possible branch outputs so the DAG traversal can
// discover them.
func (c *ConditionalOp) Inputs() []llb.Output {
	var inputs []llb.Output
	for _, br := range c.branches {
		if br.State.Output() != nil {
			inputs = append(inputs, br.State.Output())
		}
	}
	if c.defaultBr != nil && c.defaultBr.Output() != nil {
		inputs = append(inputs, c.defaultBr.Output())
	}
	return inputs
}

// ─────────────────────────────────────────────────────────────────────────────
// Convenience constructors
// ─────────────────────────────────────────────────────────────────────────────

// Switch creates a conditional state that selects a branch based on the
// condition. Usage:
//
//	Switch(
//	    EnvCondition("CI_PLATFORM"),
//	    NewBranch("linux", linuxState),
//	    NewBranch("darwin", macState),
//	).WithDefault(fallbackState)
func Switch(cond Condition, branches ...Branch) *ConditionalBuilder {
	return &ConditionalBuilder{
		condition: cond,
		branches:  branches,
	}
}

// ConditionalBuilder provides a fluent API for building conditional states.
type ConditionalBuilder struct {
	condition  Condition
	branches   []Branch
	defaultBr  *llb.State
	opts       []llb.ConstraintsOpt
}

// WithDefault sets the fallback state.
func (cb *ConditionalBuilder) WithDefault(s llb.State) *ConditionalBuilder {
	cb.defaultBr = &s
	return cb
}

// WithConstraints adds constraints.
func (cb *ConditionalBuilder) WithConstraints(opts ...llb.ConstraintsOpt) *ConditionalBuilder {
	cb.opts = append(cb.opts, opts...)
	return cb
}

// Build constructs the final State.
func (cb *ConditionalBuilder) Build() llb.State {
	var c llb.Constraints
	for _, o := range cb.opts {
		o.SetConstraintsOption(&c)
	}
	op := NewConditionalOp(cb.condition, cb.branches, cb.defaultBr, c)
	return llb.NewState(op.Output())
}

// NewBranch creates a Branch.
func NewBranch(key string, state llb.State) Branch {
	return Branch{Key: key, State: state}
}
