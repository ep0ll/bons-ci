// Package cache provides pluggable cache stores for the solver. It defines
// the Store interface for probing, saving, and releasing cached results,
// along with concrete implementations.
package cache

import (
	"context"
	"time"

	digest "github.com/opencontainers/go-digest"
)

// Key uniquely identifies a cached result.
type Key struct {
	// Digest is the content-addressable identifier derived from the
	// operation and its dependencies.
	Digest digest.Digest

	// Output is the output index within the vertex.
	Output int
}

// Record represents a stored cache entry with metadata.
type Record struct {
	Key       Key
	ResultID  string
	Size      int
	CreatedAt time.Time
	Priority  int
}

// Store is the pluggable cache backend interface. All methods must be
// safe for concurrent use.
type Store interface {
	// Probe checks if a cached result exists for the given key.
	// Returns the result ID and true if found, empty string and false
	// otherwise.
	Probe(ctx context.Context, key Key) (resultID string, found bool, err error)

	// Save stores a result under the given key.
	Save(ctx context.Context, key Key, resultID string, size int) error

	// Load retrieves the full record for a cached entry.
	Load(ctx context.Context, key Key) (Record, error)

	// Release removes a cache entry.
	Release(ctx context.Context, key Key) error

	// Walk iterates over all cache entries.
	Walk(ctx context.Context, fn func(Record) error) error
}

// ErrNotFound is returned when a cache entry does not exist.
type ErrNotFound struct {
	Key Key
}

func (e *ErrNotFound) Error() string {
	return "cache: entry not found: " + e.Key.Digest.String()
}

// IsNotFound reports whether an error is a cache miss.
func IsNotFound(err error) bool {
	_, ok := err.(*ErrNotFound)
	return ok
}
