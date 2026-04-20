package reactdag

import "context"

// ---------------------------------------------------------------------------
// Operation
// ---------------------------------------------------------------------------

// Operation is the unit of work encapsulated by a Vertex. Implementors
// provide the business logic; the Scheduler owns orchestration.
type Operation interface {
	// ID returns a stable, content-addressable identifier for this operation
	// and its configuration. Two vertices sharing the same OpID and identical
	// file inputs will produce the same cache key and can share a cached result.
	ID() string

	// Execute runs the operation. inputs are the resolved FileRefs consumed
	// from parent vertices. The returned slice is the set of files produced.
	Execute(ctx context.Context, inputs []FileRef) ([]FileRef, error)
}

// ---------------------------------------------------------------------------
// Executor
// ---------------------------------------------------------------------------

// Executor wraps Operation execution with scheduler-level concerns such as
// timeout enforcement, file tracking hooks, and resource accounting.
// The default implementation calls v.Op().Execute directly.
type Executor interface {
	Execute(ctx context.Context, v *Vertex) error
}

// ---------------------------------------------------------------------------
// CacheStore
// ---------------------------------------------------------------------------

// CacheStore is the persistence backend for vertex results.
// Implementations may represent an in-memory map (fast tier) or a remote
// content-addressable store (slow tier).
type CacheStore interface {
	// Get retrieves a cache entry. Returns (nil, nil) on a miss.
	Get(ctx context.Context, key CacheKey) (*CacheEntry, error)
	// Set stores a cache entry.
	Set(ctx context.Context, key CacheKey, entry *CacheEntry) error
	// Delete removes an entry, used during invalidation.
	Delete(ctx context.Context, key CacheKey) error
	// Exists reports presence without deserialising the full entry.
	Exists(ctx context.Context, key CacheKey) (bool, error)
}

// ---------------------------------------------------------------------------
// FileTracker
// ---------------------------------------------------------------------------

// FileTracker abstracts fanotify (or any syscall-level file watcher).
// The scheduler calls StartTracking before forking the child process and
// StopTracking after it exits. The returned FileRefs are the observed
// files, pre-hashed with blake3.
// Note: implementation using fanotify+blake3 is external to this package.
type FileTracker interface {
	// StartTracking begins monitoring file access for the given PID.
	StartTracking(pid int) error
	// StopTracking ends monitoring and returns all accessed FileRefs.
	StopTracking() ([]FileRef, error)
}

// ---------------------------------------------------------------------------
// CacheKeyComputer
// ---------------------------------------------------------------------------

// CacheKeyComputer derives a deterministic CacheKey for a vertex given its
// operation identity and the set of input files it consumes.
type CacheKeyComputer interface {
	Compute(v *Vertex, inputFiles []FileRef) (CacheKey, error)
}

// ---------------------------------------------------------------------------
// Hook types
// ---------------------------------------------------------------------------

// HookType is a named extensibility point in the scheduler lifecycle.
type HookType string

const (
	HookBeforeExecute     HookType = "before_execute"
	HookAfterExecute      HookType = "after_execute"
	HookBeforeCacheLookup HookType = "before_cache_lookup"
	HookAfterCacheLookup  HookType = "after_cache_lookup"
	HookBeforeStateChange HookType = "before_state_change"
	HookAfterStateChange  HookType = "after_state_change"
	HookOnInvalidation    HookType = "on_invalidation"
	HookOnBuildStart      HookType = "on_build_start"
	HookOnBuildEnd        HookType = "on_build_end"
)

// HookPayload is an open map passed to every hook invocation.
type HookPayload map[string]any

// HookFn is the function signature for scheduler hooks.
// Returning a non-nil error aborts the associated operation.
type HookFn func(ctx context.Context, v *Vertex, payload HookPayload) error

// EventHandler is the function signature for EventBus subscribers.
type EventHandler func(ctx context.Context, e Event)
