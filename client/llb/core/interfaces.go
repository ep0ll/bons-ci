// Package core defines every interface and primitive that the rest of the
// library depends on. It has zero dependencies on other llb sub-packages.
package core

import (
	"context"

	"github.com/moby/buildkit/solver/pb"
	digest "github.com/opencontainers/go-digest"
)

// ─── VertexType ───────────────────────────────────────────────────────────────

// VertexType identifies the operation category of a Vertex.
type VertexType string

const (
	VertexTypeSource      VertexType = "source"
	VertexTypeExec        VertexType = "exec"
	VertexTypeFile        VertexType = "file"
	VertexTypeMerge       VertexType = "merge"
	VertexTypeDiff        VertexType = "diff"
	VertexTypeBuild       VertexType = "build"
	VertexTypeConditional VertexType = "conditional"
	VertexTypeMatrix      VertexType = "matrix"
	VertexTypeGate        VertexType = "gate"
	VertexTypeSelector    VertexType = "selector"
	VertexTypeCustom      VertexType = "custom"
)

// VertexID is the content-address (SHA256 digest) of a serialised Vertex.
// It changes whenever any field of the vertex or any of its inputs changes.
type VertexID = digest.Digest

// ─── Vertex ───────────────────────────────────────────────────────────────────

// Vertex is a node in the build DAG. Implementations MUST be safe for
// concurrent read access. All mutations MUST produce new instances (immutability
// contract), so the caller can keep old snapshots for rollback.
type Vertex interface {
	// Type returns the op category.
	Type() VertexType

	// Inputs returns the ordered slice of edges feeding into this vertex.
	// Callers must not mutate the returned slice.
	Inputs() []Edge

	// Outputs describes each output slot the vertex exposes.
	Outputs() []OutputSlot

	// Validate checks internal consistency without serialising.
	Validate(ctx context.Context, c *Constraints) error

	// Marshal serialises the vertex. The result is deterministic and cached
	// per-Constraints pointer; changing any field must invalidate the cache.
	Marshal(ctx context.Context, c *Constraints) (*MarshaledVertex, error)
}

// MutatingVertex is an optional interface for vertices that support structural
// rewiring (changing which vertices feed into them).
type MutatingVertex interface {
	Vertex
	// WithInputs returns a new vertex whose inputs are replaced by the given
	// edges. Returns ErrIncompatibleInputs if the new edges are invalid.
	WithInputs(inputs []Edge) (Vertex, error)
}

// ─── Edge ─────────────────────────────────────────────────────────────────────

// Edge connects one output slot of a producer vertex to a consumer.
type Edge struct {
	Vertex Vertex // the producing vertex
	Index  int    // which output slot of Vertex is consumed
}

// ─── OutputSlot ───────────────────────────────────────────────────────────────

// OutputSlot describes one output produced by a vertex.
type OutputSlot struct {
	Index       int
	Description string // e.g., the mount path for exec ops
}

// ─── MarshaledVertex ──────────────────────────────────────────────────────────

// MarshaledVertex is the result of serialising a vertex.
type MarshaledVertex struct {
	Digest          digest.Digest
	Bytes           []byte
	Metadata        *pb.OpMetadata
	SourceLocations []*SourceLocation
}

// ─── SourceMap / SourceLocation ───────────────────────────────────────────────

// SourceMap associates a human-readable source file with part of the graph.
type SourceMap struct {
	Filename string // e.g. "Dockerfile"
	Language string // linguist name
	Data     []byte // raw source content
}

// SourceLocation pins a vertex to a specific range within a SourceMap.
type SourceLocation struct {
	SourceMap *SourceMap
	Ranges    []*pb.Range
}

// ─── Output ───────────────────────────────────────────────────────────────────

// Output is a typed reference to a specific output slot of a vertex. States
// pass Outputs as inputs when constructing new vertices.
type Output interface {
	Vertex(ctx context.Context, c *Constraints) Vertex
	ToInput(ctx context.Context, c *Constraints) (*pb.Input, error)
}

// ─── Labels ───────────────────────────────────────────────────────────────────

// Labels is an arbitrary string→string map attached to a vertex for use by
// the selector and gate operations.
type Labels map[string]string

// Match reports whether every key/value in selector is present in l.
func (l Labels) Match(selector Labels) bool {
	for k, v := range selector {
		if l[k] != v {
			return false
		}
	}
	return true
}

// ─── Serializer ───────────────────────────────────────────────────────────────

// Serializer converts a graph rooted at a given Output into a pb.Definition.
type Serializer interface {
	Serialize(ctx context.Context, root Output, c *Constraints) (*SerializedDefinition, error)
}

// SerializedDefinition is the wire-ready representation of a build graph.
type SerializedDefinition struct {
	Def      [][]byte
	Metadata map[digest.Digest]OpMetadata
	Source   *pb.Source
}

// ToPB converts to the protobuf Definition.
func (d *SerializedDefinition) ToPB() *pb.Definition {
	metas := make(map[string]*pb.OpMetadata, len(d.Metadata))
	for dgst, m := range d.Metadata {
		metas[string(dgst)] = m.ToPB()
	}
	return &pb.Definition{
		Def:      d.Def,
		Source:   d.Source,
		Metadata: metas,
	}
}
