// Package core defines the foundational interfaces and primitives for the llbx
// graph system. All other packages depend on these contracts; nothing in core
// depends on any other llbx sub-package.
package core

import (
	"context"

	"github.com/moby/buildkit/solver/pb"
	digest "github.com/opencontainers/go-digest"
)

// ─── Vertex ──────────────────────────────────────────────────────────────────

// VertexType identifies the kind of operation a vertex represents.
type VertexType string

const (
	VertexTypeSource VertexType = "source"
	VertexTypeExec   VertexType = "exec"
	VertexTypeFile   VertexType = "file"
	VertexTypeMerge  VertexType = "merge"
	VertexTypeDiff   VertexType = "diff"
	VertexTypeBuild  VertexType = "build"
	VertexTypeCustom VertexType = "custom"
)

// VertexID is the content-addressed identity of a vertex. It is derived from
// the deterministic protobuf serialisation of the vertex's Op.
type VertexID = digest.Digest

// Vertex is the fundamental node in the build graph. Implementations must be
// safe for concurrent read access; mutations must produce new instances.
type Vertex interface {
	// Type returns the operation category.
	Type() VertexType

	// Inputs returns the ordered set of edges feeding into this vertex.
	// The slice must not be mutated by callers.
	Inputs() []Edge

	// Marshal serialises the vertex into its wire format. The returned
	// MarshaledVertex is cached by the marshal layer; the vertex itself must
	// remain immutable after the first successful call.
	Marshal(ctx context.Context, c *Constraints) (*MarshaledVertex, error)

	// Validate checks the vertex's internal consistency without serialising.
	// Returns a descriptive error if the vertex is not executable.
	Validate(ctx context.Context, c *Constraints) error

	// Outputs returns descriptions of each output slot this vertex exposes.
	// Most vertices have a single output; multi-output ops (e.g., ExecOp with
	// several writable mounts) return one entry per slot.
	Outputs() []OutputSlot
}

// MutatingVertex is an optional interface for vertices that can return a new
// version of themselves after a field-level mutation. Implementations must
// recompute any cached digest after mutation.
type MutatingVertex interface {
	Vertex
	// WithInputs returns a new vertex identical to the receiver except with
	// the supplied edges as inputs. Returns ErrIncompatibleInputs if the
	// new edges cannot satisfy the vertex's requirements.
	WithInputs(inputs []Edge) (Vertex, error)
}

// ─── Edge ─────────────────────────────────────────────────────────────────────

// Edge connects one vertex's output slot to a consumer vertex's input slot.
type Edge struct {
	// Vertex is the producer.
	Vertex Vertex
	// Index selects which output of Vertex is consumed.
	Index int
}

// OutputSlot describes a single output produced by a vertex.
type OutputSlot struct {
	// Index is the zero-based output index within the vertex.
	Index int
	// Description is a human-readable label (e.g., mount path for exec ops).
	Description string
}

// ─── MarshaledVertex ─────────────────────────────────────────────────────────

// MarshaledVertex carries the result of serialising a vertex.
type MarshaledVertex struct {
	// Digest is the content-address of Bytes.
	Digest digest.Digest
	// Bytes is the deterministically-serialised pb.Op.
	Bytes []byte
	// Metadata holds per-vertex LLB metadata (caps, cache opts, etc.).
	Metadata *pb.OpMetadata
	// SourceLocations maps ranges in source files to this vertex.
	SourceLocations []*SourceLocation
}

// ─── SourceLocation ──────────────────────────────────────────────────────────

// SourceMap associates an LLB sub-graph with a human-readable source file.
type SourceMap struct {
	// Filename is the path to the source file (e.g., "Dockerfile").
	Filename string
	// Language identifies the file format (linguist name convention).
	Language string
	// Data holds the raw source content for display in error messages.
	Data []byte
}

// SourceLocation pins a vertex to a specific range within a SourceMap.
type SourceLocation struct {
	SourceMap *SourceMap
	Ranges    []*pb.Range
}

// ─── Output ───────────────────────────────────────────────────────────────────

// Output is a reference to a specific output slot of a vertex. It is the
// currency of state composition: states pass Outputs as inputs to new vertices.
type Output interface {
	// Vertex returns the producing vertex.
	Vertex(ctx context.Context, c *Constraints) Vertex
	// ToInput converts the output to a pb.Input for wire serialisation.
	ToInput(ctx context.Context, c *Constraints) (*pb.Input, error)
}

// ─── Serializer ───────────────────────────────────────────────────────────────

// Serializer converts a complete build graph into a protobuf Definition.
type Serializer interface {
	Serialize(ctx context.Context, root Output, c *Constraints) (*pb.Definition, error)
}

// ─── Observer ─────────────────────────────────────────────────────────────────

// Observer is notified when a vertex's digest changes, enabling reactive
// propagation of changes through the graph.
type Observer[T any] interface {
	OnNext(value T)
	OnError(err error)
	OnComplete()
}

// Subscription represents an active observation. Calling Cancel removes the
// subscriber from the publisher's list without blocking.
type Subscription interface {
	Cancel()
}
