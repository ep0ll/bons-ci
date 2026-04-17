// Package ops contains all built-in vertex implementations and the plugin
// registry.
package ops

import (
	"context"

	"github.com/bons/bons-ci/client/llb/core"
	"github.com/bons/bons-ci/client/llb/marshal"
	"github.com/moby/buildkit/solver/pb"
)

// ─── SimpleVertex ─────────────────────────────────────────────────────────────

// SimpleVertex embeds marshal.Cache and implements the boilerplate needed
// by source ops (no inputs, single output). Embed this in concrete source ops.
type SimpleVertex struct {
	Cache marshal.Cache
}

// ─── vertexOutput ─────────────────────────────────────────────────────────────

// VertexOutput wraps a Vertex + slot index as core.Output.
type VertexOutput struct {
	V    core.Vertex
	Slot int
}

func (o *VertexOutput) Vertex(_ context.Context, _ *core.Constraints) core.Vertex { return o.V }
func (o *VertexOutput) ToInput(ctx context.Context, c *core.Constraints) (*pb.Input, error) {
	mv, err := o.V.Marshal(ctx, c)
	if err != nil {
		return nil, err
	}
	return &pb.Input{Digest: string(mv.Digest), Index: int64(o.Slot)}, nil
}

// ─── EdgeOutput ───────────────────────────────────────────────────────────────

// EdgeOutput wraps a core.Edge as a core.Output.
type EdgeOutput struct{ E core.Edge }

func (e *EdgeOutput) Vertex(_ context.Context, _ *core.Constraints) core.Vertex { return e.E.Vertex }
func (e *EdgeOutput) ToInput(ctx context.Context, c *core.Constraints) (*pb.Input, error) {
	mv, err := e.E.Vertex.Marshal(ctx, c)
	if err != nil {
		return nil, err
	}
	return &pb.Input{Digest: string(mv.Digest), Index: int64(e.E.Index)}, nil
}
