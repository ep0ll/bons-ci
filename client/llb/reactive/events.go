package reactive

import digest "github.com/opencontainers/go-digest"

// EventKind categorises a graph change.
type EventKind string

const (
	EventKindVertexAdded      EventKind = "vertex.added"
	EventKindVertexRemoved    EventKind = "vertex.removed"
	EventKindVertexReplaced   EventKind = "vertex.replaced"
	EventKindVertexReparented EventKind = "vertex.reparented"
	EventKindDigestChanged    EventKind = "digest.changed"
	EventKindDAGMerged        EventKind = "dag.merged"
	EventKindDAGPruned        EventKind = "dag.pruned"
)

// GraphEvent is emitted on every structural or content change to a DAG.
type GraphEvent struct {
	Kind EventKind

	// AffectedID is the vertex digest that was changed, added, or removed.
	// For EventKindDigestChanged this is the *new* digest.
	AffectedID digest.Digest

	// PreviousID is the old digest for EventKindDigestChanged and
	// EventKindVertexReplaced.
	PreviousID digest.Digest

	// Count is used by EventKindDAGMerged/Pruned to report the number of
	// vertices added or removed.
	Count int
}

// DigestChangeEvent is a focused event emitted whenever a vertex's serialised
// form changes, enabling fine-grained cache invalidation.
type DigestChangeEvent struct {
	// VertexRef is an opaque stable pointer (e.g., *Vertex) that survives
	// digest changes, allowing correlation of old and new.
	VertexRef interface{}
	OldDigest digest.Digest
	NewDigest digest.Digest
}
