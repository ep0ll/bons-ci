// Package cache provides Cache implementations.
package cache

import (
	"context"
	"sync"
	"time"

	"github.com/bons/bons-ci/pkg/sbomkit/core/domain"
	"github.com/bons/bons-ci/pkg/sbomkit/core/ports"
)

// entry wraps a cached SBOM with its expiry timestamp.
type entry struct {
	sbom      *domain.SBOM
	expiresAt time.Time
}

// expired returns true if this entry has passed its TTL.
func (e *entry) expired() bool {
	return !e.expiresAt.IsZero() && time.Now().After(e.expiresAt)
}

// MemoryCache is a thread-safe, TTL-bounded, in-memory SBOM cache.
//
// It uses a two-level shard map to reduce mutex contention when many goroutines
// access the cache concurrently. The number of shards is fixed at construction.
type MemoryCache struct {
	shards  []*shard
	nShards uint32
	ttl     time.Duration
}

type shard struct {
	mu      sync.RWMutex
	entries map[string]*entry
}

const defaultShards = 16

// MemoryCacheOption configures a MemoryCache.
type MemoryCacheOption func(*MemoryCache)

// WithTTL sets the time-to-live for cache entries. Zero means entries never expire.
func WithTTL(ttl time.Duration) MemoryCacheOption {
	return func(c *MemoryCache) { c.ttl = ttl }
}

// WithShards sets the number of shards. Must be a power of two for efficient
// hash routing; values that are not powers of two are rounded up.
func WithShards(n int) MemoryCacheOption {
	return func(c *MemoryCache) {
		if n <= 0 {
			n = defaultShards
		}
		c.nShards = uint32(nextPow2(n))
		c.shards = makeShards(int(c.nShards))
	}
}

// NewMemoryCache constructs a MemoryCache.
func NewMemoryCache(opts ...MemoryCacheOption) *MemoryCache {
	c := &MemoryCache{
		nShards: defaultShards,
		shards:  makeShards(defaultShards),
		ttl:     24 * time.Hour,
	}
	for _, o := range opts {
		o(c)
	}
	return c
}

// Get implements ports.Cache.
// Returns (nil, nil) on cache miss or expired entry.
func (c *MemoryCache) Get(_ context.Context, key string) (*domain.SBOM, error) {
	s := c.shardFor(key)
	s.mu.RLock()
	e, ok := s.entries[key]
	s.mu.RUnlock()

	if !ok || e.expired() {
		if ok {
			// Promote to write lock to evict the stale entry.
			s.mu.Lock()
			// Re-check under write lock (another goroutine may have refreshed it).
			if e2, still := s.entries[key]; still && e2.expired() {
				delete(s.entries, key)
			}
			s.mu.Unlock()
		}
		return nil, nil // cache miss
	}
	return e.sbom, nil
}

// Set implements ports.Cache.
func (c *MemoryCache) Set(_ context.Context, key string, sbom *domain.SBOM) error {
	e := &entry{sbom: sbom}
	if c.ttl > 0 {
		e.expiresAt = time.Now().Add(c.ttl)
	}
	s := c.shardFor(key)
	s.mu.Lock()
	s.entries[key] = e
	s.mu.Unlock()
	return nil
}

// Delete implements ports.Cache.
func (c *MemoryCache) Delete(_ context.Context, key string) error {
	s := c.shardFor(key)
	s.mu.Lock()
	delete(s.entries, key)
	s.mu.Unlock()
	return nil
}

// Flush removes all entries from all shards. Useful in tests.
func (c *MemoryCache) Flush() {
	for _, s := range c.shards {
		s.mu.Lock()
		s.entries = make(map[string]*entry)
		s.mu.Unlock()
	}
}

// Size returns the total number of (possibly expired) entries across all shards.
func (c *MemoryCache) Size() int {
	total := 0
	for _, s := range c.shards {
		s.mu.RLock()
		total += len(s.entries)
		s.mu.RUnlock()
	}
	return total
}

// ── internal ─────────────────────────────────────────────────────────────────

// shardFor selects the shard for a given key using FNV-1a hashing.
func (c *MemoryCache) shardFor(key string) *shard {
	h := fnv1a(key)
	return c.shards[h&(c.nShards-1)]
}

// fnv1a computes a 32-bit FNV-1a hash of s.
func fnv1a(s string) uint32 {
	const (
		offset32 uint32 = 2166136261
		prime32  uint32 = 16777619
	)
	h := offset32
	for i := 0; i < len(s); i++ {
		h ^= uint32(s[i])
		h *= prime32
	}
	return h
}

func makeShards(n int) []*shard {
	s := make([]*shard, n)
	for i := range s {
		s[i] = &shard{entries: make(map[string]*entry)}
	}
	return s
}

// nextPow2 rounds n up to the next power of two.
func nextPow2(n int) int {
	if n <= 1 {
		return 1
	}
	n--
	n |= n >> 1
	n |= n >> 2
	n |= n >> 4
	n |= n >> 8
	n |= n >> 16
	return n + 1
}

// Ensure MemoryCache implements ports.Cache at compile time.
var _ ports.Cache = (*MemoryCache)(nil)
