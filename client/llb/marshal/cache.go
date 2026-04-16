// Package marshal handles the conversion of the llbx graph into BuildKit's
// wire format (pb.Definition). The cache layer prevents redundant serialisation
// of unchanged vertices.
package marshal

import (
	"sync"

	"github.com/bons/bons-ci/client/llb/core"
	cerrdefs "github.com/containerd/errdefs"
	"github.com/moby/buildkit/solver/pb"
	digest "github.com/opencontainers/go-digest"
)

// ─── Cache ────────────────────────────────────────────────────────────────────

// Cache is a per-vertex, per-constraints result store. It is safe for
// concurrent access. The zero value is ready to use.
type Cache struct {
	mu      sync.Mutex
	entries map[*core.Constraints]*cacheEntry
}

type cacheEntry struct {
	digest  digest.Digest
	bytes   []byte
	meta    *pb.OpMetadata
	sources []*core.SourceLocation
}

// Load retrieves a cached result. Returns ErrNotFound if absent.
func (c *Cache) Load(constraints *core.Constraints) (
	digest.Digest, []byte, *pb.OpMetadata, []*core.SourceLocation, error,
) {
	c.mu.Lock()
	defer c.mu.Unlock()
	if c.entries == nil {
		return "", nil, nil, nil, cerrdefs.ErrNotFound
	}
	e, ok := c.entries[constraints]
	if !ok {
		return "", nil, nil, nil, cerrdefs.ErrNotFound
	}
	return e.digest, e.bytes, e.meta, e.sources, nil
}

// Store persists a serialised result and returns the same values for
// convenient chaining in Marshal implementations.
func (c *Cache) Store(
	bytes []byte,
	meta *pb.OpMetadata,
	sources []*core.SourceLocation,
	constraints *core.Constraints,
) (digest.Digest, []byte, *pb.OpMetadata, []*core.SourceLocation, error) {
	dgst := digest.FromBytes(bytes)
	e := &cacheEntry{
		digest:  dgst,
		bytes:   bytes,
		meta:    meta,
		sources: sources,
	}
	c.mu.Lock()
	if c.entries == nil {
		c.entries = make(map[*core.Constraints]*cacheEntry)
	}
	c.entries[constraints] = e
	c.mu.Unlock()
	return dgst, bytes, meta, sources, nil
}

// Invalidate removes all cached results. Call after any field-level mutation.
func (c *Cache) Invalidate() {
	c.mu.Lock()
	c.entries = nil
	c.mu.Unlock()
}

// ─── CacheHandle ─────────────────────────────────────────────────────────────

// CacheHandle provides a locked view of a Cache. Acquire it at the start of
// Marshal and Release it when done to prevent interleaved writes.
type CacheHandle struct {
	cache *Cache
}

// Acquire takes the Cache lock and returns a handle for the current Marshal call.
func Acquire(c *Cache) *CacheHandle {
	c.mu.Lock()
	return &CacheHandle{cache: c}
}

// Release unlocks the Cache.
func (h *CacheHandle) Release() { h.cache.mu.Unlock() }

// Load is like Cache.Load but does not re-lock (caller holds the lock).
func (h *CacheHandle) Load(c *core.Constraints) (
	digest.Digest, []byte, *pb.OpMetadata, []*core.SourceLocation, error,
) {
	if h.cache.entries == nil {
		return "", nil, nil, nil, cerrdefs.ErrNotFound
	}
	e, ok := h.cache.entries[c]
	if !ok {
		return "", nil, nil, nil, cerrdefs.ErrNotFound
	}
	return e.digest, e.bytes, e.meta, e.sources, nil
}

// Store is like Cache.Store but does not re-lock.
func (h *CacheHandle) Store(
	bytes []byte,
	meta *pb.OpMetadata,
	sources []*core.SourceLocation,
	c *core.Constraints,
) (digest.Digest, []byte, *pb.OpMetadata, []*core.SourceLocation, error) {
	dgst := digest.FromBytes(bytes)
	if h.cache.entries == nil {
		h.cache.entries = make(map[*core.Constraints]*cacheEntry)
	}
	h.cache.entries[c] = &cacheEntry{
		digest:  dgst,
		bytes:   bytes,
		meta:    meta,
		sources: sources,
	}
	return dgst, bytes, meta, sources, nil
}
