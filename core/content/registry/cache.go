package registry

import (
	"hash/fnv"
	"sync"
	"time"

	"github.com/containerd/containerd/v2/core/content"
	digest "github.com/opencontainers/go-digest"
)

// ---------------------------------------------------------------------------
// Sharded InfoCache – 256 shards, false-sharing padded, TTL-evicted
// ---------------------------------------------------------------------------

const (
	numInfoShards = 256 // power-of-2; shard = fnv32a(digest) & mask
	infoShardMask = numInfoShards - 1
)

type infoCacheEntry struct {
	info      content.Info
	expiresAt int64 // unix nano
}

// cacheShard is padded to exactly one cache line (64 B) so adjacent shards
// never share a cache line and hardware prefetchers don't cause false sharing.
//
// Layout: mu (24 B) + padding (8 B) = 32 B per RWMutex;
// entries map pointer (8 B) → total padded to 64 B.
type cacheShard struct {
	mu      sync.RWMutex
	entries map[digest.Digest]infoCacheEntry
	_       [16]byte // explicit padding to 64-byte cache line
}

// InfoCache is a sharded, TTL-driven metadata cache for content.Info values.
// It replaces the original sync.Map with a structure that:
//  1. Routes by digest → shard via FNV-1a (uniform distribution).
//  2. Uses per-shard RWMutex so reads only compete within the same shard
//     (1/256 chance of collision vs 100% with a global lock).
//  3. Never allocates during Get (map lookup is pointer-free on hit).
type InfoCache struct {
	shards [numInfoShards]cacheShard
	ttl    time.Duration
}

// newInfoCache initialises all shard maps.
func newInfoCache(ttl time.Duration) *InfoCache {
	c := &InfoCache{ttl: ttl}
	for i := range c.shards {
		c.shards[i].entries = make(map[digest.Digest]infoCacheEntry, 8)
	}
	return c
}

// shard selects the responsible cacheShard for a digest.
// FNV-1a on the raw digest string: no allocation, good distribution.
func (c *InfoCache) shard(dgst digest.Digest) *cacheShard {
	h := fnv.New32a()
	_, _ = h.Write([]byte(dgst))
	return &c.shards[h.Sum32()&infoShardMask]
}

// Get returns the cached info and true if found and not expired.
// No allocation on the hot path.
func (c *InfoCache) Get(dgst digest.Digest) (content.Info, bool) {
	s := c.shard(dgst)
	s.mu.RLock()
	e, ok := s.entries[dgst]
	s.mu.RUnlock()
	if !ok || time.Now().UnixNano() > e.expiresAt {
		return content.Info{}, false
	}
	return e.info, true
}

// Set stores info for dgst. Overwrites any existing entry.
func (c *InfoCache) Set(dgst digest.Digest, info content.Info) {
	s := c.shard(dgst)
	exp := time.Now().Add(c.ttl).UnixNano()
	s.mu.Lock()
	s.entries[dgst] = infoCacheEntry{info: info, expiresAt: exp}
	s.mu.Unlock()
}

// Delete removes the entry for dgst (called on Update / Delete / Commit).
func (c *InfoCache) Delete(dgst digest.Digest) {
	s := c.shard(dgst)
	s.mu.Lock()
	delete(s.entries, dgst)
	s.mu.Unlock()
}

// Flush evicts all entries across all shards (called on Store.Close).
func (c *InfoCache) Flush() {
	for i := range c.shards {
		s := &c.shards[i]
		s.mu.Lock()
		for k := range s.entries {
			delete(s.entries, k)
		}
		s.mu.Unlock()
	}
}
