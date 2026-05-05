package cache

import (
	"hash/fnv"
	"sync"
	"sync/atomic"
	"time"

	"github.com/user/layermerkle/layer"
)

// numShards is the number of cache shards. Must be a power of 2.
// 256 shards provides excellent concurrency with minimal memory overhead.
// At typical key sizes (~100 bytes), 256 empty shards cost <1 KB.
const numShards = 256

// ─────────────────────────────────────────────────────────────────────────────
// Cache interface
// ─────────────────────────────────────────────────────────────────────────────

// Cache is the interface for the sharded hash cache. It is the sole
// persistence layer for deduplicated file hashes across ExecOp layers.
//
// All methods are goroutine-safe.
type Cache interface {
	// Get retrieves the cache entry for the given key.
	// Returns (entry, true) on hit; (zero, false) on miss.
	Get(key CacheKey) (CacheEntry, bool)

	// Set stores or overwrites the entry for the given key.
	// Callers must use SetIfAbsent for non-mutating reads to avoid
	// overwriting a more recent (mutating) entry.
	Set(key CacheKey, entry CacheEntry)

	// SetIfAbsent stores the entry only if no entry already exists for key.
	// Returns (existing, false) if the key was already present;
	// (entry, true) if the entry was newly inserted.
	SetIfAbsent(key CacheKey, entry CacheEntry) (CacheEntry, bool)

	// Delete removes the entry for the given key.
	Delete(key CacheKey)

	// WalkLayer calls fn for every entry whose CacheKey.LayerDigest == layerDigest.
	// Iteration stops if fn returns false.
	// WalkLayer acquires a read lock per shard; it is safe to call concurrently
	// but must not modify the cache from within fn.
	WalkLayer(layerDigest layer.Digest, fn func(CacheKey, CacheEntry) bool)

	// EvictLayer removes all entries (including tombstones) associated with
	// layerDigest. Used when a layer is garbage-collected.
	EvictLayer(layerDigest layer.Digest)

	// Stats returns a snapshot of runtime statistics.
	Stats() Stats
}

// ─────────────────────────────────────────────────────────────────────────────
// ShardedCache
// ─────────────────────────────────────────────────────────────────────────────

// ShardedCache implements Cache with 256 independently-locked shards.
// Each shard is an RWMutex-protected map. Key-to-shard mapping uses FNV-32a
// of the key's string representation, providing a uniform distribution.
//
// Concurrency model:
//   - Reads (Get, WalkLayer, Stats) acquire per-shard read locks.
//   - Writes (Set, SetIfAbsent, Delete, EvictLayer) acquire per-shard write locks.
//   - Global hit/miss counters are updated via atomic operations.
type ShardedCache struct {
	shards [numShards]cacheShard

	// atomic counters — updated with sync/atomic, not under shard locks
	hits       atomic.Int64
	misses     atomic.Int64
	entries    atomic.Int64
	tombstones atomic.Int64
}

// cacheShard is a single cache bucket with an independent RWMutex.
type cacheShard struct {
	mu      sync.RWMutex
	entries map[CacheKey]CacheEntry
}

// NewShardedCache allocates a ShardedCache. The cache is ready to use
// immediately; no further initialization is required.
func NewShardedCache() *ShardedCache {
	sc := &ShardedCache{}
	for i := range sc.shards {
		sc.shards[i].entries = make(map[CacheKey]CacheEntry)
	}
	return sc
}

// ─── Core Operations ────────────────────────────────────────────────────────

// Get retrieves the cache entry for the given key.
func (sc *ShardedCache) Get(key CacheKey) (CacheEntry, bool) {
	s := sc.shard(key)
	s.mu.RLock()
	entry, ok := s.entries[key]
	s.mu.RUnlock()

	if ok {
		sc.hits.Add(1)
	} else {
		sc.misses.Add(1)
	}
	return entry, ok
}

// Set stores or overwrites the entry for key. Use for mutating operations
// (write, create, chmod, rename) where stale entries must be replaced.
func (sc *ShardedCache) Set(key CacheKey, entry CacheEntry) {
	if entry.CachedAt.IsZero() {
		entry.CachedAt = time.Now()
	}

	s := sc.shard(key)
	s.mu.Lock()
	old, existed := s.entries[key]
	s.entries[key] = entry
	s.mu.Unlock()

	if !existed {
		sc.entries.Add(1)
		if entry.Tombstone {
			sc.tombstones.Add(1)
		}
	} else {
		// Adjust tombstone counter if type changed
		switch {
		case !old.Tombstone && entry.Tombstone:
			sc.tombstones.Add(1)
		case old.Tombstone && !entry.Tombstone:
			sc.tombstones.Add(-1)
		}
	}
}

// SetIfAbsent stores the entry only when no entry already exists for key.
// This is the correct operation for non-mutating (read) access events:
// it avoids overwriting a WRITE entry that arrived concurrently.
//
// Returns (existing, false) if the key was present; (entry, true) if inserted.
func (sc *ShardedCache) SetIfAbsent(key CacheKey, entry CacheEntry) (CacheEntry, bool) {
	if entry.CachedAt.IsZero() {
		entry.CachedAt = time.Now()
	}

	s := sc.shard(key)
	s.mu.Lock()
	existing, ok := s.entries[key]
	if ok {
		s.mu.Unlock()
		return existing, false
	}
	s.entries[key] = entry
	s.mu.Unlock()

	sc.entries.Add(1)
	if entry.Tombstone {
		sc.tombstones.Add(1)
	}
	return entry, true
}

// Delete removes the entry for key. A no-op if the key does not exist.
func (sc *ShardedCache) Delete(key CacheKey) {
	s := sc.shard(key)
	s.mu.Lock()
	old, existed := s.entries[key]
	if existed {
		delete(s.entries, key)
	}
	s.mu.Unlock()

	if existed {
		sc.entries.Add(-1)
		if old.Tombstone {
			sc.tombstones.Add(-1)
		}
	}
}

// WalkLayer iterates over all entries for layerDigest across every shard.
// Iteration order is undefined. Returning false from fn stops the walk.
//
// Caveat: WalkLayer holds each shard's read lock only while iterating that
// shard. Entries may be concurrently modified in other shards during the walk.
func (sc *ShardedCache) WalkLayer(layerDigest layer.Digest, fn func(CacheKey, CacheEntry) bool) {
	for i := range sc.shards {
		s := &sc.shards[i]
		s.mu.RLock()
		stop := false
		for k, v := range s.entries {
			if k.LayerDigest == layerDigest {
				if !fn(k, v) {
					stop = true
					break
				}
			}
		}
		s.mu.RUnlock()
		if stop {
			return
		}
	}
}

// EvictLayer removes all entries for layerDigest from every shard.
// This is an O(N) operation and should be called only during GC.
func (sc *ShardedCache) EvictLayer(layerDigest layer.Digest) {
	for i := range sc.shards {
		s := &sc.shards[i]
		s.mu.Lock()
		for k, v := range s.entries {
			if k.LayerDigest == layerDigest {
				delete(s.entries, k)
				sc.entries.Add(-1)
				if v.Tombstone {
					sc.tombstones.Add(-1)
				}
			}
		}
		s.mu.Unlock()
	}
}

// Stats returns a snapshot of current cache statistics.
func (sc *ShardedCache) Stats() Stats {
	return Stats{
		Entries:    sc.entries.Load(),
		Tombstones: sc.tombstones.Load(),
		Hits:       sc.hits.Load(),
		Misses:     sc.misses.Load(),
		Shards:     numShards,
	}
}

// ─── Internal helpers ────────────────────────────────────────────────────────

// shard returns the shard responsible for the given key using FNV-32a.
// The FNV-32a hash is very fast (~2 ns/key on modern hardware) and provides
// a sufficiently uniform distribution for 256 buckets.
func (sc *ShardedCache) shard(key CacheKey) *cacheShard {
	h := fnv.New32a()
	// Write both components separately to avoid accidental collisions between
	// keys where filePath and layerDigest swap values.
	_, _ = h.Write([]byte(key.LayerDigest))
	h.Write([]byte{0}) // separator byte
	_, _ = h.Write([]byte(key.FilePath))
	return &sc.shards[h.Sum32()%numShards]
}

// Verify interface compliance at compile time.
var _ Cache = (*ShardedCache)(nil)
