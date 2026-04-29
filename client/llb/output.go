package llb

import (
	"context"

	"github.com/moby/buildkit/solver/pb"
	digest "github.com/opencontainers/go-digest"
	ocispecs "github.com/opencontainers/image-spec/specs-go/v1"
)

// ─────────────────────────────────────────────────────────────────────────────
// Core interfaces
// ─────────────────────────────────────────────────────────────────────────────

// Output represents a single output edge from a Vertex. Vertices may have
// multiple outputs (e.g. ExecOp mount outputs). The default output is index 0.
type Output interface {
	ToInput(ctx context.Context, c *Constraints) (*pb.Input, error)
	Vertex(ctx context.Context, c *Constraints) Vertex
}

// Vertex is a node in the LLB DAG. Every operation (source, exec, merge, etc.)
// implements Vertex. Vertices are connected through Output references forming
// an immutable directed acyclic graph.
type Vertex interface {
	Validate(ctx context.Context, c *Constraints) error
	Marshal(ctx context.Context, c *Constraints) (digest.Digest, []byte, *pb.OpMetadata, []*SourceLocation, error)
	Output() Output
	Inputs() []Output
}

// ─────────────────────────────────────────────────────────────────────────────
// Concrete output
// ─────────────────────────────────────────────────────────────────────────────

// output is the default Output implementation. It wraps a Vertex with an
// optional platform and a lazy output-index resolver.
type output struct {
	vertex   Vertex
	platform *ocispecs.Platform
	getIndex func() (pb.OutputIndex, error)
}

var _ Output = (*output)(nil)

// Vertex returns the underlying Vertex, ignoring the constraints parameter
// since we already have the concrete reference.
func (o *output) Vertex(_ context.Context, _ *Constraints) Vertex {
	return o.vertex
}

// ToInput serializes this output into a pb.Input reference. It marshals the
// parent vertex to obtain its digest, then pairs it with the output index.
func (o *output) ToInput(ctx context.Context, c *Constraints) (*pb.Input, error) {
	var index pb.OutputIndex
	if o.getIndex != nil {
		idx, err := o.getIndex()
		if err != nil {
			return nil, err
		}
		index = idx
	}
	dgst, _, _, _, err := o.vertex.Marshal(ctx, c)
	if err != nil {
		return nil, err
	}
	return &pb.Input{
		Digest: string(dgst),
		Index:  int64(index),
	}, nil
}

// NewOutput constructs an output that always returns output index 0.
// This is the most common case for single-output operations.
func NewOutput(v Vertex) Output {
	return &output{
		vertex: v,
		getIndex: func() (pb.OutputIndex, error) {
			return 0, nil
		},
	}
}

// NewOutputWithIndex constructs an output with a custom index resolver.
func NewOutputWithIndex(v Vertex, platform *ocispecs.Platform, getIndex func() (pb.OutputIndex, error)) Output {
	return &output{
		vertex:   v,
		platform: platform,
		getIndex: getIndex,
	}
}
