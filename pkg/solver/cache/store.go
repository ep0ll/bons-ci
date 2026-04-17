// Package cache provides pluggable cache stores for the solver. The Store
// interface is intentionally minimal: probe, save, load, release, walk. More
// sophisticated policies (eviction, replication, content-addressable linking)
// are implemented in concrete types that embed or compose Store.
package cache

import (
	"context"
	"fmt"
	"time"

	digest "github.com/opencontainers/go-digest"
)

// Key uniquely identifies a cached result within a store. It is derived from
// the CacheMap digest combined with all dependency cache keys — stable across
// solver sessions.
type Key struct {
	// Digest is the content-addressable identifier.
	Digest digest.Digest
	// Output is the output index within the vertex.
	Output int
}

// String returns a compact human-readable representation.
func (k Key) String() string {
	return fmt.Sprintf("%s[%d]", k.Digest, k.Output)
}

// Record represents a stored cache entry with associated metadata.
type Record struct {
	Key       Key
	ResultID  string
	Size      int
	CreatedAt time.Time
	// Priority is used when multiple records match: higher priority records
	// are preferred. Analogous to BuildKit's CacheRecord.Priority.
	Priority int
	// TTL, when > 0, is the suggested time-to-live for this entry.
	// Implementations may use this for eviction scheduling.
	TTL time.Duration
}

// Store is the pluggable cache backend interface. All methods must be safe for
// concurrent use. The interface maps directly onto BuildKit's CacheKeyStorage
// + CacheResultStorage but simplified for readability.
type Store interface {
	// Probe checks whether a result is cached for the given key.
	// Returns (resultID, true, nil) on hit; ("", false, nil) on miss.
	Probe(ctx context.Context, key Key) (resultID string, found bool, err error)

	// Save stores a result under the given key. Idempotent.
	Save(ctx context.Context, key Key, resultID string, size int) error

	// Load retrieves the full Record. Returns ErrNotFound if absent.
	Load(ctx context.Context, key Key) (Record, error)

	// Release removes a cache entry and frees any associated resources.
	// Silently succeeds if the entry does not exist.
	Release(ctx context.Context, key Key) error

	// Walk iterates over all entries in an unspecified order. The callback
	// receives a copy of each Record. Modifications to the store during
	// Walk are permitted (walk operates on a snapshot).
	Walk(ctx context.Context, fn func(Record) error) error

	// Stats returns basic store metrics. Optional; may return zero values.
	Stats(ctx context.Context) (Stats, error)
}

// Stats holds basic cache store metrics.
type Stats struct {
	// Entries is the current number of cached entries.
	Entries int
	// TotalSize is the sum of all record sizes in bytes.
	TotalSize int64
	// Hits and Misses track probe outcomes since creation.
	Hits   int64
	Misses int64
}

// ─── Errors ───────────────────────────────────────────────────────────────────

// ErrNotFound is returned by Load when a cache entry does not exist.
type ErrNotFound struct {
	Key Key
}

func (e *ErrNotFound) Error() string {
	return "cache: entry not found: " + e.Key.String()
}

// IsNotFound reports whether err is a cache miss.
func IsNotFound(err error) bool {
	_, ok := err.(*ErrNotFound)
	return ok
}
