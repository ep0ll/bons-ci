package reactdag

import (
	"fmt"
	"time"
)

// ---------------------------------------------------------------------------
// Vertex State
// ---------------------------------------------------------------------------

// State is the lifecycle stage of a DAG vertex. Transitions are strictly
// controlled by the state machine in vertex.go.
type State uint8

const (
	// StateInitial: vertex has not been evaluated.
	StateInitial State = iota
	// StateFastCache: result served from fast (in-process / local) cache.
	StateFastCache
	// StateSlowCache: result served from slow (remote / persistent) cache.
	StateSlowCache
	// StateCompleted: vertex executed successfully.
	StateCompleted
	// StateFailed: vertex failed; the error itself may be cached.
	StateFailed
)

var stateNames = [...]string{"initial", "fast_cache", "slow_cache", "completed", "failed"}

func (s State) String() string {
	if int(s) < len(stateNames) {
		return stateNames[s]
	}
	return fmt.Sprintf("state(%d)", s)
}

// IsTerminal returns true when no further transitions are expected.
func (s State) IsTerminal() bool { return s == StateCompleted || s == StateFailed }

// IsCached returns true when the result came from any cache tier.
func (s State) IsCached() bool { return s == StateFastCache || s == StateSlowCache }

// ---------------------------------------------------------------------------
// Content-addressable types
// ---------------------------------------------------------------------------

// CacheKey is a 32-byte digest derived from a vertex's operation identity
// and the hashes of the exact files it consumes from its parents.
type CacheKey [32]byte

// IsZero reports whether the key is unset.
func (k CacheKey) IsZero() bool { return k == CacheKey{} }

// FileRef is a point-in-time snapshot of a file.
// Hash is filled by the FileTracker (fanotify + blake3).
type FileRef struct {
	Path    string
	Hash    [32]byte // blake3 digest
	Size    int64
	ModTime time.Time
}

// Equal reports content equality (hash-based, not path-based).
func (f FileRef) Equal(other FileRef) bool { return f.Hash == other.Hash }

// FileDependency declares the exact subset of a parent's output files that
// this vertex consumes. Tracking at this granularity allows the invalidation
// engine to skip re-execution when only unrelated files changed.
type FileDependency struct {
	ParentID string
	Paths    []string // paths within the parent's output set
}

// ---------------------------------------------------------------------------
// Metrics
// ---------------------------------------------------------------------------

// StateTransition records a single state-machine edge with its cause.
type StateTransition struct {
	From      State
	To        State
	Timestamp time.Time
	Cause     string
}

// VertexMetrics holds all performance counters for one vertex execution.
type VertexMetrics struct {
	QueuedAt     time.Time
	StartedAt    time.Time
	FinishedAt   time.Time
	FilesRead    int
	FilesWritten int
	BytesRead    int64
	BytesWritten int64
	CacheHits    int
	Transitions  []StateTransition
}

// Duration returns wall-clock execution time (zero if not started/finished).
func (m VertexMetrics) Duration() time.Duration {
	if m.StartedAt.IsZero() || m.FinishedAt.IsZero() {
		return 0
	}
	return m.FinishedAt.Sub(m.StartedAt)
}

// WaitTime returns the queue wait time before execution began.
func (m VertexMetrics) WaitTime() time.Duration {
	if m.QueuedAt.IsZero() || m.StartedAt.IsZero() {
		return 0
	}
	return m.StartedAt.Sub(m.QueuedAt)
}

// BuildMetrics is the aggregate result of a complete build execution.
type BuildMetrics struct {
	TotalVertices int
	Executed      int
	FastCacheHits int
	SlowCacheHits int
	CachedErrors  int // errors replayed from cache (no recompute)
	Skipped       int
	Failed        int
	TotalDuration time.Duration
	CriticalPath  []string // vertex IDs along the longest dependency chain
	PerVertex     map[string]VertexMetrics
}

// ---------------------------------------------------------------------------
// Events
// ---------------------------------------------------------------------------

// EventType is a string discriminant for DAG lifecycle events.
type EventType string

const (
	EventStateChanged   EventType = "state_changed"
	EventCacheHit       EventType = "cache_hit"
	EventCacheMiss      EventType = "cache_miss"
	EventExecutionStart EventType = "execution_start"
	EventExecutionEnd   EventType = "execution_end"
	EventInvalidated    EventType = "invalidated"
	EventBuildStart     EventType = "build_start"
	EventBuildEnd       EventType = "build_end"
	EventFileAccessed   EventType = "file_accessed"
)

// Event is emitted on the EventBus for every significant DAG lifecycle moment.
type Event struct {
	Type     EventType
	VertexID string
	Time     time.Time
	Payload  map[string]any
}

func newEvent(t EventType, vertexID string) Event {
	return Event{Type: t, VertexID: vertexID, Time: time.Now(), Payload: map[string]any{}}
}
