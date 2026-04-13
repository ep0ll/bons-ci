package models

import "time"

type DAGStatus string

const (
	DAGStatusPending   DAGStatus = "pending"
	DAGStatusRunning   DAGStatus = "running"
	DAGStatusSucceeded DAGStatus = "succeeded"
	DAGStatusFailed    DAGStatus = "failed"
	DAGStatusCancelled DAGStatus = "cancelled"
)

type VertexStatus string

const (
	VertexStatusPending   VertexStatus = "pending"
	VertexStatusRunning   VertexStatus = "running"
	VertexStatusSucceeded VertexStatus = "succeeded"
	VertexStatusFailed    VertexStatus = "failed"
	VertexStatusCancelled VertexStatus = "cancelled"
	VertexStatusCached    VertexStatus = "cached"
	VertexStatusSkipped   VertexStatus = "skipped"
)

type FDType int

const (
	FDStdin    FDType = 0
	FDStdout   FDType = 1
	FDStderr   FDType = 2
	FDProgress FDType = 3
	FDOther    FDType = 99
)

// Platform identifies the execution environment.
type Platform struct {
	OS         string            `json:"os"`
	Arch       string            `json:"arch"`
	Variant    string            `json:"variant,omitempty"`
	Properties map[string]string `json:"properties,omitempty"`
}

// FileRef is a content-addressed file reference within a vertex filesystem.
type FileRef struct {
	Path       string    `json:"path"`
	Digest     string    `json:"digest"` // sha256:hex or blake3:hex
	Size       int64     `json:"size"`
	Mode       uint32    `json:"mode"`
	IsDir      bool      `json:"is_dir,omitempty"`
	IsSymlink  bool      `json:"is_symlink,omitempty"`
	LinkTarget string    `json:"link_target,omitempty"`
	ModifiedAt time.Time `json:"modified_at"`
}

// VertexInput represents a dependency on another vertex.
type VertexInput struct {
	VertexID  string    `json:"vertex_id"`
	OutputIdx int       `json:"output_idx"`
	// Specific files from this dep vertex that this vertex reads.
	Files     []FileRef `json:"files,omitempty"`
}

// VertexOutput is a produced artifact reference.
type VertexOutput struct {
	Index     int    `json:"index"`
	MediaType string `json:"media_type,omitempty"`
	Digest    string `json:"digest,omitempty"`
	Size      int64  `json:"size,omitempty"`
}

// MountSpec describes a BuildKit-style mount.
type MountSpec struct {
	Type     string            `json:"type"` // cache, bind, secret, tmpfs, ssh
	Target   string            `json:"target"`
	Source   string            `json:"source,omitempty"`
	CacheID  string            `json:"cache_id,omitempty"`
	Sharing  string            `json:"sharing,omitempty"` // shared, private, locked
	ReadOnly bool              `json:"readonly,omitempty"`
	Platform *Platform         `json:"platform,omitempty"`
	Options  map[string]string `json:"options,omitempty"`
}

// ResourceUsage captures execution metrics for a vertex.
type ResourceUsage struct {
	CPUNanos       int64         `json:"cpu_nanos"`
	MemoryBytes    int64         `json:"memory_bytes"`
	NetworkRxBytes int64         `json:"network_rx_bytes"`
	NetworkTxBytes int64         `json:"network_tx_bytes"`
	IOReadBytes    int64         `json:"io_read_bytes"`
	IOWriteBytes   int64         `json:"io_write_bytes"`
	WallTime       time.Duration `json:"wall_time"`
}

// DAG represents a build graph.
type DAG struct {
	ID            string            `json:"id"`
	BuildID       string            `json:"build_id"`
	Name          string            `json:"name"`
	Status        DAGStatus         `json:"status"`
	RootVertexIDs []string          `json:"root_vertex_ids"`
	Labels        map[string]string `json:"labels,omitempty"`
	Description   string            `json:"description,omitempty"`
	Platform      *Platform         `json:"platform,omitempty"`
	CreatedBy     string            `json:"created_by,omitempty"`
	CreatedAt     time.Time         `json:"created_at"`
	UpdatedAt     time.Time         `json:"updated_at"`
	CompletedAt   *time.Time        `json:"completed_at,omitempty"`
	Error         string            `json:"error,omitempty"`
}

// Vertex represents a single step in the build graph.
type Vertex struct {
	ID           string            `json:"id"`
	DAGID        string            `json:"dag_id"`
	Name         string            `json:"name"`
	OpType       string            `json:"op_type"` // exec, copy, merge, diff, context
	OpPayload    []byte            `json:"op_payload,omitempty"` // serialized op
	Inputs       []VertexInput     `json:"inputs,omitempty"`
	Outputs      []VertexOutput    `json:"outputs,omitempty"`
	InputFiles   []FileRef         `json:"input_files,omitempty"`
	OutputFiles  []FileRef         `json:"output_files,omitempty"`
	Status       VertexStatus      `json:"status"`
	CacheKey     string            `json:"cache_key,omitempty"`
	CacheHit     bool              `json:"cache_hit,omitempty"`
	Error        string            `json:"error,omitempty"`
	ErrorDetails string            `json:"error_details,omitempty"`
	Platform     *Platform         `json:"platform,omitempty"`
	Labels       map[string]string `json:"labels,omitempty"`
	Env          map[string]string `json:"env,omitempty"`
	Mounts       []MountSpec       `json:"mounts,omitempty"`
	Resources    *ResourceUsage    `json:"resource_usage,omitempty"`
	CreatedAt    time.Time         `json:"created_at"`
	StartedAt    *time.Time        `json:"started_at,omitempty"`
	CompletedAt  *time.Time        `json:"completed_at,omitempty"`
}

// LogStream is a single FD stream for a vertex.
type LogStream struct {
	ID         string            `json:"id"`
	VertexID   string            `json:"vertex_id"`
	DAGID      string            `json:"dag_id"`
	FDType     FDType            `json:"fd_type"`
	FDNum      int               `json:"fd_num"`
	Closed     bool              `json:"closed"`
	TotalBytes int64             `json:"total_bytes"`
	ChunkCount int64             `json:"chunk_count"`
	Metadata   map[string]string `json:"metadata,omitempty"`
	CreatedAt  time.Time         `json:"created_at"`
	ClosedAt   *time.Time        `json:"closed_at,omitempty"`
}

// LogChunk is a single chunk of log data.
type LogChunk struct {
	StreamID  string    `json:"stream_id"`
	Sequence  int64     `json:"sequence"`
	Data      []byte    `json:"data"`
	Timestamp time.Time `json:"timestamp"`
	FDType    FDType    `json:"fd_type"`
	FDNum     int       `json:"fd_num"`
}

// DependencyNode is a node in a vertex dependency tree.
type DependencyNode struct {
	Vertex        *Vertex          `json:"vertex"`
	Deps          []DependencyNode `json:"deps,omitempty"`
	ProvidedFiles []FileRef        `json:"provided_files,omitempty"`
}

// CacheKey fully describes the inputs to a vertex for cache lookup.
type CacheKey struct {
	OpDigest        string   `json:"op_digest"`
	InputFileHashes []string `json:"input_file_hashes"`
	DepCacheKeys    []string `json:"dep_cache_keys"`
	Platform        Platform `json:"platform"`
	Selector        string   `json:"selector,omitempty"`
}

type CacheEntryKind string

const (
	CacheEntryKindBlob     CacheEntryKind = "blob"
	CacheEntryKindInline   CacheEntryKind = "inline"
	CacheEntryKindManifest CacheEntryKind = "manifest"
)

// CacheEntry stores the result of a previously executed vertex.
type CacheEntry struct {
	ID            string            `json:"id"`
	CacheKey      string            `json:"cache_key"` // sha256 of serialized CacheKey
	VertexID      string            `json:"vertex_id"`
	DAGID         string            `json:"dag_id"`
	OutputDigests []string          `json:"output_digests,omitempty"`
	OutputFiles   []FileRef         `json:"output_files,omitempty"`
	Kind          CacheEntryKind    `json:"kind"`
	Platform      *Platform         `json:"platform,omitempty"`
	InlineData    []byte            `json:"inline_data,omitempty"`
	HitCount      int64             `json:"hit_count"`
	Metadata      map[string]string `json:"metadata,omitempty"`
	CreatedAt     time.Time         `json:"created_at"`
	ExpiresAt     *time.Time        `json:"expires_at,omitempty"`
	LastUsedAt    time.Time         `json:"last_used_at"`
}
