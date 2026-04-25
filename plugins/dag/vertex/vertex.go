// Package vertex defines the core interfaces and types for all nodes in the DAG.
//
// This package has zero internal dependencies and acts as the shared contract
// that all other packages depend on. Keeping it minimal and stable is critical —
// every package in this module imports it.
//
// Design principle: interfaces, not implementations. All concrete types live in
// the ops package. This separation eliminates import cycles and allows external
// packages to define their own vertex implementations.
package vertex

import "context"

// Kind identifies the category of operation a vertex performs.
// It is intentionally a string type so external packages can define their own
// kinds without forking this library.
type Kind string

const (
	KindSource Kind = "source" // produces content from an external origin
	KindExec   Kind = "exec"   // runs a command in a container
	KindFile   Kind = "file"   // performs filesystem mutations
	KindMerge  Kind = "merge"  // overlays multiple inputs into one
	KindDiff   Kind = "diff"   // computes the delta between two inputs
	KindBuild  Kind = "build"  // triggers a nested build
)

// Vertex is the atomic unit of the DAG. Every node — source, exec, file op,
// merge, diff — implements this interface.
//
// Contracts:
//   - ID must be stable and deterministic for the same logical operation.
//   - Inputs must return the same slice on every call (read-only, no mutation).
//   - Validate must be safe for concurrent calls after construction.
//   - Implementations must be immutable after construction.
type Vertex interface {
	// ID returns a stable, globally unique identifier for this vertex.
	// The ID must be derived solely from the vertex's content (kind + attrs + inputs),
	// not from memory addresses or random values, so the same graph always
	// produces the same IDs.
	ID() string

	// Kind returns the operation category.
	Kind() Kind

	// Inputs returns the direct upstream dependencies of this vertex.
	// The returned slice must not be modified by callers.
	Inputs() []Vertex

	// Validate checks that the vertex is structurally well-formed.
	// It should verify required fields, legal value ranges, and input counts.
	// It must NOT perform cycle detection — that is the graph's responsibility.
	// Validate is called by the graph builder and may be called concurrently.
	Validate(ctx context.Context) error
}

// Named is an optional extension interface for vertices with human-readable names.
// The graph and traversal engines check for this interface to produce better
// diagnostic output, but it is never required.
type Named interface {
	Name() string
}

// Described is an optional extension interface for vertices that carry arbitrary
// key-value metadata (e.g. "description", "llb.customname", progress group hints).
type Described interface {
	Description() map[string]string
}

// Marshaler is an optional extension interface for vertices that know how to
// serialize themselves to a canonical byte representation.
//
// The marshal package uses this when available, falling back to a generic
// reflection-based serializer otherwise. Implement this for performance-critical
// or custom wire formats.
type Marshaler interface {
	MarshalVertex(ctx context.Context) ([]byte, error)
}

// Ref represents a typed reference to one specific output of a vertex.
// A vertex may produce multiple outputs (e.g. an exec op with multiple mounts),
// so Ref = Vertex + Index uniquely identifies a single output.
//
// Ref is the primary currency passed between ops when building the DAG.
// State wraps a Ref and provides a chainable builder API on top.
type Ref struct {
	// Vertex is the producing node.
	Vertex Vertex
	// Index is the zero-based output index within the vertex.
	// For single-output vertices (source, file, merge, diff) this is always 0.
	// For exec ops, index 0 is the root mount; subsequent indices are additional
	// writable mounts in mount-target alphabetical order.
	Index int
}

// IsZero reports whether r is the zero-value Ref (no vertex attached).
// A zero Ref represents "scratch" — an empty filesystem with no content.
func (r Ref) IsZero() bool {
	return r.Vertex == nil
}

// String returns a human-readable representation of the ref for debugging.
func (r Ref) String() string {
	if r.Vertex == nil {
		return "<scratch>"
	}
	if n, ok := r.Vertex.(Named); ok {
		return n.Name()
	}
	id := r.Vertex.ID()
	if len(id) > 12 {
		id = id[:12]
	}
	return string(r.Vertex.Kind()) + ":" + id
}
