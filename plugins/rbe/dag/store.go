package dagstore

import (
	"context"
	"io"
)

// ——— sub-interfaces ——————————————————————————————————————————————————————————

// DAGStore manages DAG-level records.
type DAGStore interface {
	// PutDAG creates or fully replaces a DAG metadata record.
	PutDAG(ctx context.Context, dag *DAGMeta) error

	// GetDAG retrieves a DAG by its user-supplied ID.
	GetDAG(ctx context.Context, dagID string) (*DAGMeta, error)

	// GetDAGByHash retrieves a DAG by its content-addressed hash.
	GetDAGByHash(ctx context.Context, hash string) (*DAGMeta, error)

	// ListDAGs returns a paginated list of DAGs.
	ListDAGs(ctx context.Context, opts ListOptions) (*ListResult[*DAGMeta], error)

	// DeleteDAG removes the DAG metadata record.
	// If cascade is true, all vertices, streams, and index entries belonging
	// to this DAG are also removed.
	DeleteDAG(ctx context.Context, dagID string, cascade bool) error
}

// VertexStore manages individual vertex records.
type VertexStore interface {
	// PutVertex creates or fully replaces a vertex metadata record.
	PutVertex(ctx context.Context, vertex *VertexMeta) error

	// GetVertex retrieves a vertex by its content hash within a DAG.
	GetVertex(ctx context.Context, dagID, vertexHash string) (*VertexMeta, error)

	// GetVertexByID retrieves a vertex by its human-readable ID.
	// IDs are global: they resolve to a (dagID, vertexHash) pair.
	GetVertexByID(ctx context.Context, id string) (*VertexMeta, error)

	// GetVertexByTreeHash retrieves a vertex by its tree hash within a specific DAG.
	// For most vertices Hash == TreeHash, but the explicit tree-hash index
	// supports future extensions where they differ.
	GetVertexByTreeHash(ctx context.Context, dagID, treeHash string) (*VertexMeta, error)

	// ListVertices returns a paginated list of vertices in a DAG.
	ListVertices(ctx context.Context, dagID string, opts ListOptions) (*ListResult[*VertexMeta], error)

	// DeleteVertex removes a vertex's metadata and its ID/tree-hash index entries.
	// Streams are NOT automatically removed; call DeleteStream separately.
	DeleteVertex(ctx context.Context, dagID, vertexHash string) error
}

// StreamStore manages the binary stream blobs (stdin/stdout/stderr).
type StreamStore interface {
	// PutStream stores a stream for a vertex.
	// size is a hint for multipart-upload decisions (use -1 if unknown).
	// r is consumed entirely and closed if it implements io.Closer.
	PutStream(ctx context.Context, dagID, vertexHash string, st StreamType, r io.Reader, size int64) error

	// GetStream opens a stream for reading.  The caller MUST close the returned
	// ReadCloser when done.
	GetStream(ctx context.Context, dagID, vertexHash string, st StreamType) (io.ReadCloser, error)

	// DeleteStream removes a stored stream blob.
	DeleteStream(ctx context.Context, dagID, vertexHash string, st StreamType) error
}

// ——— combined Store ——————————————————————————————————————————————————————————

// Store is the primary access point.  Embed all sub-interfaces plus lifecycle.
type Store interface {
	DAGStore
	VertexStore
	StreamStore

	// PutVertexWithStreams atomically (best-effort) stores a vertex's metadata
	// and all its streams.  Implementations may use parallel uploads.
	PutVertexWithStreams(ctx context.Context, vertex *VertexMeta, streams map[StreamType]StreamPayload) error

	// GetVertexWithStreams returns a VertexStream with open readers for all
	// stored streams.  The caller MUST call VertexStream.Close when done.
	GetVertexWithStreams(ctx context.Context, dagID, vertexHash string) (*VertexStream, error)

	// VerifyVertex re-hashes the vertex's metadata and streams and returns
	// ErrIntegrityViolation if anything has been tampered with.
	VerifyVertex(ctx context.Context, dagID, vertexHash string, h Hasher) error

	// Ping tests connectivity to the backend.
	Ping(ctx context.Context) error

	io.Closer
}

// StreamPayload bundles the data and size hint for a single stream upload.
type StreamPayload struct {
	// Reader provides the raw bytes.
	Reader io.Reader
	// Size is the total byte count, or -1 if unknown (triggers chunked upload).
	Size int64
	// Hash is the expected content hash for integrity verification.
	// Leave zero to skip pre-verification.
	Hash Hash
}
