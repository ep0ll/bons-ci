package llb

import (
	"context"

	digest "github.com/opencontainers/go-digest"

	"github.com/moby/buildkit/solver/pb"

	"golang.org/x/sync/singleflight"
)

// ─────────────────────────────────────────────────────────────────────────────
// asyncState
// ─────────────────────────────────────────────────────────────────────────────

// asyncState implements Output with deferred resolution. The wrapped function
// is called at most once per group key using singleflight to deduplicate
// concurrent resolves. This enables lazy graph construction where a State is
// only fully built when it is marshalled.
type asyncState struct {
	f    func(context.Context, State, *Constraints) (State, error)
	prev State
	g    singleflight.Group
}

var _ Output = (*asyncState)(nil)

// Output returns itself — asyncState acts as both Output and the lazy resolver.
func (as *asyncState) Output() Output { return as }

// Vertex resolves the async function and returns the underlying vertex.
func (as *asyncState) Vertex(ctx context.Context, c *Constraints) Vertex {
	target, err := as.Do(ctx, c)
	if err != nil {
		return &errVertex{err: err}
	}
	out := target.Output()
	if out == nil {
		return nil
	}
	return out.Vertex(ctx, c)
}

// ToInput resolves the async function and converts the result to a pb.Input.
func (as *asyncState) ToInput(ctx context.Context, c *Constraints) (*pb.Input, error) {
	target, err := as.Do(ctx, c)
	if err != nil {
		return nil, err
	}
	out := target.Output()
	if out == nil {
		return nil, nil
	}
	return out.ToInput(ctx, c)
}

// Do executes the async function exactly once, deduplicating concurrent calls.
func (as *asyncState) Do(ctx context.Context, c *Constraints) (State, error) {
	v, err, _ := as.g.Do("", func() (any, error) {
		return as.f(ctx, as.prev, c)
	})
	if err != nil {
		return State{}, err
	}
	return v.(State), nil
}

// ─────────────────────────────────────────────────────────────────────────────
// errVertex
// ─────────────────────────────────────────────────────────────────────────────

// errVertex is a sentinel Vertex that propagates an error through the graph.
type errVertex struct {
	err error
}

var _ Vertex = (*errVertex)(nil)

func (v *errVertex) Validate(context.Context, *Constraints) error { return v.err }
func (v *errVertex) Marshal(context.Context, *Constraints) (digest.Digest, []byte, *pb.OpMetadata, []*SourceLocation, error) {
	return "", nil, nil, nil, v.err
}
func (v *errVertex) Output() Output   { return nil }
func (v *errVertex) Inputs() []Output { return nil }
