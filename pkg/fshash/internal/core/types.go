// Package core defines the shared domain types for the fshash package family.
// All sub-packages (layer, cache, access, merkle, chunk) depend on this
// package for type definitions, ensuring zero import cycles.
package core

import (
	"errors"
	"fmt"
	"time"
)

// ---- Layer & Vertex Identifiers ----

// LayerID uniquely identifies a filesystem layer by its content digest.
type LayerID struct {
	digest string
}

// NewLayerID creates a LayerID from a digest string.
func NewLayerID(digest string) LayerID { return LayerID{digest: digest} }

// String returns the layer's digest string.
func (l LayerID) String() string { return l.digest }

// IsZero reports whether the LayerID is unset.
func (l LayerID) IsZero() bool { return l.digest == "" }

// Equal reports whether two LayerIDs are identical.
func (l LayerID) Equal(other LayerID) bool { return l.digest == other.digest }

// VertexID uniquely identifies an execution vertex (ExecOp) by its digest.
type VertexID struct {
	digest string
}

// NewVertexID creates a VertexID from a digest string.
func NewVertexID(digest string) VertexID { return VertexID{digest: digest} }

// String returns the vertex digest string.
func (v VertexID) String() string { return v.digest }

// IsZero reports whether the VertexID is unset.
func (v VertexID) IsZero() bool { return v.digest == "" }

// ---- Access Operation ----

// AccessOp classifies the type of filesystem operation observed.
type AccessOp uint8

const (
	OpRead  AccessOp = iota + 1 // File was read
	OpWrite                     // File was written
	OpClose                     // File descriptor closed after write
	OpDelete                    // File was deleted (whiteout)
)

// String returns a human-readable representation.
func (op AccessOp) String() string {
	switch op {
	case OpRead:
		return "read"
	case OpWrite:
		return "write"
	case OpClose:
		return "close"
	case OpDelete:
		return "delete"
	default:
		return fmt.Sprintf("unknown(%d)", op)
	}
}

// ---- Access Event ----

// AccessEvent represents a single filesystem access event from fanotify.
type AccessEvent struct {
	LayerID   LayerID
	VertexID  VertexID
	Path      string
	Op        AccessOp
	Offset    uint64
	Count     uint64
	Data      []byte
	Timestamp time.Time
}

// HasRange reports whether this event carries byte-range information.
func (e *AccessEvent) HasRange() bool { return e.Count > 0 }

// Validate performs basic validation of the event fields.
func (e *AccessEvent) Validate() error {
	if e.LayerID.IsZero() {
		return fmt.Errorf("%w: missing layer ID", ErrInvalidEvent)
	}
	if e.Path == "" {
		return fmt.Errorf("%w: missing file path", ErrInvalidEvent)
	}
	if e.Op == 0 {
		return fmt.Errorf("%w: missing access operation", ErrInvalidEvent)
	}
	return nil
}

// ---- File Hash ----

// FileHash represents the computed content hash of a file.
type FileHash struct {
	Path      string
	Hash      []byte
	Algorithm string
	LayerID   LayerID
	Size      int64
}

// ---- Deduplication Action & Result ----

// Action describes the deduplication decision for an access event.
type Action uint8

const (
	ActionCompute Action = iota + 1 // Unique work: compute hash
	ActionReuse                     // Cached hash still valid
	ActionSkip                      // Duplicate event in session
	ActionExclude                   // File deleted/hidden — exclude from tree
)

// String returns a human-readable action name.
func (a Action) String() string {
	switch a {
	case ActionCompute:
		return "compute"
	case ActionReuse:
		return "reuse"
	case ActionSkip:
		return "skip"
	case ActionExclude:
		return "exclude"
	default:
		return fmt.Sprintf("unknown(%d)", a)
	}
}

// DeduplicationResult captures the outcome of processing an AccessEvent.
type DeduplicationResult struct {
	Event       AccessEvent
	Action      Action
	CachedHash  FileHash
	SourceLayer LayerID
}

// ---- Statistics ----

// ProcessorStats aggregates operational statistics.
type ProcessorStats struct {
	EventsReceived   uint64
	EventsComputed   uint64
	EventsReused     uint64
	EventsSkipped    uint64
	EventsExcluded   uint64
	CacheHits        uint64
	CacheMisses      uint64
	LayersRegistered uint64
	TreesFinalized   uint64
}

// DeduplicationRate returns the proportion of events that avoided recomputation.
func (s ProcessorStats) DeduplicationRate() float64 {
	if s.EventsReceived == 0 {
		return 0
	}
	return float64(s.EventsReused+s.EventsSkipped+s.EventsExcluded) / float64(s.EventsReceived)
}

// ---- Sentinel Errors ----

var (
	ErrLayerNotFound = errors.New("fshash: layer not found")
	ErrLayerExists   = errors.New("fshash: layer already exists")
	ErrCacheMiss     = errors.New("fshash: cache miss")
	ErrTreeEmpty     = errors.New("fshash: tree has no leaves")
	ErrInvalidEvent  = errors.New("fshash: invalid access event")
	ErrClosed        = errors.New("fshash: processor is closed")
	ErrInvalidChain  = errors.New("fshash: invalid layer chain")
)
