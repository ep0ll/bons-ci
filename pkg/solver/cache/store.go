// Package cache provides the cache storage interfaces and implementations
// for the solver. It defines two storage levels matching BuildKit's design:
//   - KeyStorage: manages cache key identities, links between them, and
//     backlinks for cache chain walking (used by cache export)
//   - ResultStorage: manages the actual result data persistence
package cache

import (
	"context"
	"time"

	digest "github.com/opencontainers/go-digest"
	"github.com/pkg/errors"
)

// ErrNotFound is returned when a cache key or result is not found.
var ErrNotFound = errors.New("not found")

// Index is a zero-based output index within a vertex.
type Index int

// Key is the primary lookup key for the simple Store interface.
type Key struct {
	Digest digest.Digest
	Output int
}

// Record is the result of a cache lookup via the simple Store.
type Record struct {
	ResultID  string
	Size      int
	CreatedAt time.Time
}

// Store is the simple cache interface used by the solver coordinator.
// For advanced use cases (link walking, export), use KeyStorage + ResultStorage.
type Store interface {
	Probe(ctx context.Context, key Key) (resultID string, found bool, err error)
	Save(ctx context.Context, key Key, resultID string, size int) error
	Load(ctx context.Context, key Key) (*Record, error)
	Release(ctx context.Context, key Key) error
}

// ─── Advanced storage interfaces (BuildKit parity) ───────────────────────────

// CacheInfoLink describes a directed edge in the cache key graph.
// It connects a parent cache key to a child via a specific input index,
// op digest, and output index. Optional Selector narrows the match.
type CacheInfoLink struct {
	Input    Index
	Digest   digest.Digest
	Output   Index
	Selector digest.Digest
}

// CacheResult is a lightweight value referencing an actual result.
type CacheResult struct {
	ID        string
	CreatedAt time.Time
}

// KeyStorage manages the cache key graph. It supports:
//   - Key existence checks and enumeration
//   - Result associations per key
//   - Forward links (parent → child) and backlinks (child → parent)
//   - Cascading cleanup when keys become empty
//
// This is equivalent to BuildKit's CacheKeyStorage interface.
type KeyStorage interface {
	// Exists returns true if the key has links or results.
	Exists(id string) bool

	// Walk enumerates all known key IDs.
	Walk(fn func(id string) error) error

	// WalkResults enumerates results associated with a key.
	WalkResults(id string, fn func(CacheResult) error) error

	// Load retrieves a specific result by key ID and result ID.
	Load(id string, resultID string) (CacheResult, error)

	// AddResult associates a result with a key.
	AddResult(id string, res CacheResult) error

	// Release removes a result and cleans up empty branches.
	Release(resultID string) error

	// AddLink adds a directed edge from id to target via the given link.
	AddLink(id string, link CacheInfoLink, target string) error

	// WalkLinks enumerates targets reachable from id via the given link.
	WalkLinks(id string, link CacheInfoLink, fn func(id string) error) error

	// HasLink checks if a specific link exists.
	HasLink(id string, link CacheInfoLink, target string) bool

	// WalkBacklinks enumerates parent keys that link to id.
	WalkBacklinks(id string, fn func(id string, link CacheInfoLink) error) error

	// WalkIDsByResult enumerates all key IDs associated with a result.
	WalkIDsByResult(resultID string, fn func(string) error) error
}

// ResultStorage manages the actual result data.
// This is equivalent to BuildKit's CacheResultStorage.
type ResultStorage interface {
	// Save persists a result and returns a lightweight reference.
	Save(id string, createdAt time.Time) (CacheResult, error)

	// Load retrieves a result by its cache reference.
	Load(ctx context.Context, res CacheResult) (any, error)

	// Exists checks if a result exists.
	Exists(ctx context.Context, id string) bool
}
