package core

import (
	"context"

	"github.com/moby/buildkit/solver/pb"
	"github.com/moby/buildkit/util/apicaps"
	digest "github.com/opencontainers/go-digest"
)

// ─── Capability helpers ───────────────────────────────────────────────────────

// ConstraintsAddCap adds a capability requirement to c. Safe to call on a nil c.
func ConstraintsAddCap(c *Constraints, id apicaps.CapID) {
	if c == nil {
		return
	}
	if c.Metadata.Caps == nil {
		c.Metadata.Caps = make(map[apicaps.CapID]bool)
	}
	c.Metadata.Caps[id] = true
}

// ─── SimpleOutput ─────────────────────────────────────────────────────────────

// SimpleOutput wraps a Vertex and a fixed output-slot index as an Output.
// Most vertex implementations return one of these from their Output() accessor.
type SimpleOutput struct {
	V     Vertex
	Slot  int
}

func (o *SimpleOutput) Vertex(_ context.Context, _ *Constraints) Vertex { return o.V }

func (o *SimpleOutput) ToInput(ctx context.Context, c *Constraints) (*pb.Input, error) {
	mv, err := o.V.Marshal(ctx, c)
	if err != nil {
		return nil, err
	}
	return &pb.Input{Digest: string(mv.Digest), Index: int64(o.Slot)}, nil
}

// ─── EdgeOutput ───────────────────────────────────────────────────────────────

// EdgeOutput wraps a core.Edge as a core.Output, used during graph rewiring.
type EdgeOutput struct{ E Edge }

func (e *EdgeOutput) Vertex(_ context.Context, _ *Constraints) Vertex { return e.E.Vertex }

func (e *EdgeOutput) ToInput(ctx context.Context, c *Constraints) (*pb.Input, error) {
	mv, err := e.E.Vertex.Marshal(ctx, c)
	if err != nil {
		return nil, err
	}
	return &pb.Input{Digest: string(mv.Digest), Index: int64(e.E.Index)}, nil
}

// ─── NilOutput (scratch) ──────────────────────────────────────────────────────

// NilOutput is a sentinel Output for scratch (empty filesystem) states.
type NilOutput struct{}

func (NilOutput) Vertex(_ context.Context, _ *Constraints) Vertex { return nil }
func (NilOutput) ToInput(_ context.Context, _ *Constraints) (*pb.Input, error) {
	return nil, nil
}

// ─── DigestFromBytes ─────────────────────────────────────────────────────────

// DigestFromBytes is a convenience wrapper.
func DigestFromBytes(b []byte) digest.Digest {
	return digest.FromBytes(b)
}
