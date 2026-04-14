package dagstore

import (
	"io"
	"time"
)

// ——— hash ————————————————————————————————————————————————————————————————————

// HashAlgorithm identifies the hash algorithm used.
type HashAlgorithm string

const (
	HashBlake3 HashAlgorithm = "blake3"
	HashSHA256 HashAlgorithm = "sha256"
	HashSHA512 HashAlgorithm = "sha512"
	// HashCustom allows callers to inject arbitrary algorithms via the Hasher interface.
	HashCustom HashAlgorithm = "custom"
)

// Hash pairs an algorithm name with a hex-encoded digest.
type Hash struct {
	Algorithm HashAlgorithm `json:"algorithm"`
	Value     string        `json:"value"` // lowercase hex
}

func (h Hash) String() string { return string(h.Algorithm) + ":" + h.Value }

// IsZero reports whether the Hash is the zero value.
func (h Hash) IsZero() bool { return h.Value == "" }

// ——— files ———————————————————————————————————————————————————————————————————

// FileRef describes a single file that a vertex produces or depends upon.
type FileRef struct {
	// Path is the file path within the vertex's output/workspace.
	Path string `json:"path"`
	// Hash is the content hash of the file.
	Hash Hash `json:"hash"`
	// Size is the file size in bytes.
	Size int64 `json:"size"`
	// ModTime is the last-modified timestamp, if known.
	ModTime time.Time `json:"mod_time,omitempty"`
}

// ——— vertex input ————————————————————————————————————————————————————————————

// VertexInput describes one parent-vertex dependency: which parent vertex feeds
// into this vertex and which of that parent's files are consumed.
type VertexInput struct {
	// VertexHash is the content-addressed hash of the parent vertex.
	// This is always set.
	VertexHash string `json:"vertex_hash"`

	// VertexID is the optional human-readable ID alias of the parent vertex.
	// If present it resolves to VertexHash; used for display and lookup only.
	VertexID string `json:"vertex_id,omitempty"`

	// Files lists the specific files from the parent vertex that this vertex
	// consumes.  Empty means "all outputs" (wildcard dependency).
	Files []FileRef `json:"files,omitempty"`
}

// ——— streams ————————————————————————————————————————————————————————————————

// StreamType identifies one of the three standard I/O streams.
type StreamType uint8

const (
	StreamStdin  StreamType = iota // 0
	StreamStdout                   // 1
	StreamStderr                   // 2
)

func (s StreamType) String() string {
	switch s {
	case StreamStdin:
		return "stdin"
	case StreamStdout:
		return "stdout"
	case StreamStderr:
		return "stderr"
	default:
		return "unknown"
	}
}

// AllStreams is a convenience slice containing every StreamType.
var AllStreams = []StreamType{StreamStdin, StreamStdout, StreamStderr}

// ——— vertex ——————————————————————————————————————————————————————————————————

// VertexMeta is the full metadata record for a single vertex.
// Streams (stdin/stdout/stderr) are stored separately; this record only
// carries flags indicating their presence and their hashes for integrity.
type VertexMeta struct {
	// Hash is the content-addressed identity of this vertex.
	//   For root vertices (no inputs): hash(OperationHash)
	//   For non-root vertices:        hash(OperationHash ‖ sorted(input.VertexHash...))
	// This encodes the vertex's entire DAG ancestry — the "hash of DAG tree
	// till this vertex's location" described in the design.
	Hash string `json:"hash"`

	// ID is an optional, user-supplied human-readable identifier.
	// It acts as a stable alias for Hash and can replace the computed hash
	// when referring to a well-known vertex.  IDs must be unique per store.
	ID string `json:"id,omitempty"`

	// DAGID is the identifier of the DAG this vertex belongs to.
	DAGID string `json:"dag_id"`

	// OperationHash is the hash of just this vertex's own operation data,
	// independent of its parents.  Together with the input hashes it produces
	// Hash.
	OperationHash string `json:"operation_hash"`

	// TreeHash mirrors Hash for explicitness; callers may use either field.
	// For a vertex with parents, TreeHash == Hash because the hash already
	// captures the full ancestry chain.
	TreeHash string `json:"tree_hash"`

	// Inputs lists the parent vertices this vertex depends on.
	Inputs []VertexInput `json:"inputs,omitempty"`

	// Stream presence & hashes — set by the store after streams are written.
	HasStdin     bool `json:"has_stdin"`
	HasStdout    bool `json:"has_stdout"`
	HasStderr    bool `json:"has_stderr"`
	StdinHash    Hash `json:"stdin_hash,omitempty"`
	StdoutHash   Hash `json:"stdout_hash,omitempty"`
	StderrHash   Hash `json:"stderr_hash,omitempty"`
	StdinSize    int64 `json:"stdin_size,omitempty"`
	StdoutSize   int64 `json:"stdout_size,omitempty"`
	StderrSize   int64 `json:"stderr_size,omitempty"`

	// Labels holds arbitrary user-supplied key-value metadata.
	Labels map[string]string `json:"labels,omitempty"`

	CreatedAt time.Time `json:"created_at"`
	UpdatedAt time.Time `json:"updated_at"`
}

// ——— DAG ————————————————————————————————————————————————————————————————————

// DAGMeta is the metadata record for a whole DAG.
type DAGMeta struct {
	// ID is the unique identifier for this DAG (user-supplied or generated).
	ID string `json:"id"`

	// Hash is the content-addressed hash of the entire graph.
	// Computed as hash(sorted(leaf_vertex_hashes...)) where leaves are the
	// terminal output vertices (vertices with no children).
	Hash string `json:"hash"`

	// RootHashes are the hashes of root vertices (no parents / pure sources).
	RootHashes []string `json:"root_hashes,omitempty"`

	// LeafHashes are the hashes of leaf vertices (no children / final outputs).
	LeafHashes []string `json:"leaf_hashes,omitempty"`

	// VertexCount is the total number of vertices in this DAG.
	VertexCount int `json:"vertex_count"`

	// Labels holds arbitrary user-supplied key-value metadata.
	Labels map[string]string `json:"labels,omitempty"`

	CreatedAt time.Time `json:"created_at"`
	UpdatedAt time.Time `json:"updated_at"`
}

// ——— compound result types ——————————————————————————————————————————————————

// VertexStream bundles a vertex's metadata with open stream readers.
// The caller MUST call Close when done to release the underlying connections.
type VertexStream struct {
	Meta   *VertexMeta
	Stdin  io.ReadCloser // nil when HasStdin == false
	Stdout io.ReadCloser // nil when HasStdout == false
	Stderr io.ReadCloser // nil when HasStderr == false
}

// Close closes all non-nil stream readers, returning the first error encountered.
func (vs *VertexStream) Close() error {
	for _, rc := range []io.ReadCloser{vs.Stdin, vs.Stdout, vs.Stderr} {
		if rc != nil {
			if err := rc.Close(); err != nil {
				return err
			}
		}
	}
	return nil
}

// ——— pagination ——————————————————————————————————————————————————————————————

// ListOptions controls filtering and pagination for list operations.
type ListOptions struct {
	// Prefix narrows results to keys sharing this prefix (backend-specific).
	Prefix string
	// PageToken is the opaque continuation token from a previous ListResult.
	PageToken string
	// PageSize limits how many items are returned (0 = backend default).
	PageSize int
	// Labels filters results to those matching ALL supplied label key-value pairs.
	Labels map[string]string
}

// ListResult is a single page of results with an optional continuation token.
type ListResult[T any] struct {
	Items []T
	// NextPageToken is empty when there are no more results.
	NextPageToken string
	// TotalCount is the total number of matching items across all pages, or -1
	// if the backend cannot compute it efficiently.
	TotalCount int64
}
