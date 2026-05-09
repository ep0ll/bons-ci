package layermerkle

import (
	"container/list"
	"fmt"
	"sync"
	"sync/atomic"
	"time"
)

// ─────────────────────────────────────────────────────────────────────────────
// HashCache interface
// ─────────────────────────────────────────────────────────────────────────────

// HashCache stores and retrieves file hashes keyed by (layerID, relPath).
// Implementations must be safe for concurrent use.
type HashCache interface {
	// Get returns the cached hash for (layerID, relPath), if present.
	Get(layerID LayerID, relPath string) (FileHash, bool)

	// Set stores a hash keyed by (layerID, relPath).
	Set(layerID LayerID, relPath string, h FileHash)

	// Delete removes the entry for (layerID, relPath).
	Delete(layerID LayerID, relPath string)

	// DeleteLayer removes all entries for the given layer.
	DeleteLayer(layerID LayerID)

	// Len returns the total number of cached entries.
	Len() int

	// Stats returns a point-in-time snapshot of cache performance.
	Stats() CacheStats
}

// CacheStats is a point-in-time snapshot of HashCache performance counters.
type CacheStats struct {
	Hits      int64
	Misses    int64
	Evictions int64
	Capacity  int
	Used      int
}

// HitRate returns the cache hit ratio in [0.0, 1.0].
func (s CacheStats) HitRate() float64 {
	total := s.Hits + s.Misses
	if total == 0 {
		return 0
	}
	return float64(s.Hits) / float64(total)
}

// ─────────────────────────────────────────────────────────────────────────────
// cacheKey — composite key for the cache map
// ─────────────────────────────────────────────────────────────────────────────

type cacheKey struct {
	layerID LayerID
	relPath string
}

// ─────────────────────────────────────────────────────────────────────────────
// cacheEntry — value stored in the LRU
// ─────────────────────────────────────────────────────────────────────────────

type cacheEntry struct {
	key       cacheKey
	hash      FileHash
	createdAt time.Time
}

// ─────────────────────────────────────────────────────────────────────────────
// shardedLRUCache — 256-shard LRU hash cache
// ─────────────────────────────────────────────────────────────────────────────

// shardedLRUCache is a sharded, size-bounded LRU cache.
// 256 independent shards reduce lock contention by ~256× under high concurrency.
// Each shard maintains its own LRU list for fair, bounded eviction.
type shardedLRUCache struct {
	shards   [256]cacheShard
	capacity int // per-shard capacity
	hits     atomic.Int64
	misses   atomic.Int64
}

type cacheShard struct {
	mu      sync.Mutex
	entries map[cacheKey]*list.Element
	lru     list.List
	cap     int
	evicted atomic.Int64
}

// NewShardedLRUCache creates a sharded LRU cache with the given total capacity.
// The capacity is distributed evenly across 256 shards.
func NewShardedLRUCache(totalCapacity int) HashCache {
	perShard := totalCapacity / 256
	if perShard < 1 {
		perShard = 1
	}
	c := &shardedLRUCache{capacity: perShard}
	for i := range c.shards {
		c.shards[i] = cacheShard{
			entries: make(map[cacheKey]*list.Element, perShard),
			cap:     perShard,
		}
	}
	return c
}

// Get implements HashCache.
func (c *shardedLRUCache) Get(layerID LayerID, relPath string) (FileHash, bool) {
	k := cacheKey{layerID: layerID, relPath: relPath}
	s := c.shard(k)
	s.mu.Lock()
	h, ok := s.get(k)
	s.mu.Unlock()
	if ok {
		c.hits.Add(1)
	} else {
		c.misses.Add(1)
	}
	return h, ok
}

func (s *cacheShard) get(k cacheKey) (FileHash, bool) {
	el, ok := s.entries[k]
	if !ok {
		return "", false
	}
	s.lru.MoveToFront(el)
	return el.Value.(*cacheEntry).hash, true
}

// Set implements HashCache.
func (c *shardedLRUCache) Set(layerID LayerID, relPath string, h FileHash) {
	k := cacheKey{layerID: layerID, relPath: relPath}
	s := c.shard(k)
	s.mu.Lock()
	s.set(k, h)
	s.mu.Unlock()
}

func (s *cacheShard) set(k cacheKey, h FileHash) {
	if el, ok := s.entries[k]; ok {
		s.lru.MoveToFront(el)
		el.Value.(*cacheEntry).hash = h
		return
	}
	if s.lru.Len() >= s.cap {
		s.evictOldest()
	}
	entry := &cacheEntry{key: k, hash: h, createdAt: time.Now()}
	el := s.lru.PushFront(entry)
	s.entries[k] = el
}

func (s *cacheShard) evictOldest() {
	el := s.lru.Back()
	if el == nil {
		return
	}
	entry := el.Value.(*cacheEntry)
	delete(s.entries, entry.key)
	s.lru.Remove(el)
	s.evicted.Add(1)
}

// Delete implements HashCache.
func (c *shardedLRUCache) Delete(layerID LayerID, relPath string) {
	k := cacheKey{layerID: layerID, relPath: relPath}
	s := c.shard(k)
	s.mu.Lock()
	s.delete(k)
	s.mu.Unlock()
}

func (s *cacheShard) delete(k cacheKey) {
	el, ok := s.entries[k]
	if !ok {
		return
	}
	delete(s.entries, k)
	s.lru.Remove(el)
}

// DeleteLayer removes all entries belonging to layerID.
func (c *shardedLRUCache) DeleteLayer(layerID LayerID) {
	for i := range c.shards {
		s := &c.shards[i]
		s.mu.Lock()
		for k, el := range s.entries {
			if k.layerID == layerID {
				delete(s.entries, k)
				s.lru.Remove(el)
			}
		}
		s.mu.Unlock()
	}
}

// Len implements HashCache.
func (c *shardedLRUCache) Len() int {
	total := 0
	for i := range c.shards {
		s := &c.shards[i]
		s.mu.Lock()
		total += s.lru.Len()
		s.mu.Unlock()
	}
	return total
}

// Stats implements HashCache.
func (c *shardedLRUCache) Stats() CacheStats {
	var evictions int64
	for i := range c.shards {
		evictions += c.shards[i].evicted.Load()
	}
	return CacheStats{
		Hits:      c.hits.Load(),
		Misses:    c.misses.Load(),
		Evictions: evictions,
		Capacity:  c.capacity * 256,
		Used:      c.Len(),
	}
}

// shard maps a cache key to its owning shard using FNV hash.
func (c *shardedLRUCache) shard(k cacheKey) *cacheShard {
	h := fnv32(string(k.layerID) + "\x00" + k.relPath)
	return &c.shards[h&0xff]
}

// fnv32 is a fast FNV-1a 32-bit hash of a string for shard selection.
func fnv32(s string) uint32 {
	const (
		offset = uint32(2166136261)
		prime  = uint32(16777619)
	)
	h := offset
	for i := 0; i < len(s); i++ {
		h ^= uint32(s[i])
		h *= prime
	}
	return h
}

// ─────────────────────────────────────────────────────────────────────────────
// NoopHashCache — always a cache miss; useful for benchmarks and testing
// ─────────────────────────────────────────────────────────────────────────────

// NoopHashCache is a HashCache implementation that never stores or retrieves
// anything. Use in benchmarks to measure raw hasher throughput.
type NoopHashCache struct{}

func (NoopHashCache) Get(LayerID, string) (FileHash, bool) { return "", false }
func (NoopHashCache) Set(LayerID, string, FileHash)        {}
func (NoopHashCache) Delete(LayerID, string)               {}
func (NoopHashCache) DeleteLayer(LayerID)                  {}
func (NoopHashCache) Len() int                             { return 0 }
func (NoopHashCache) Stats() CacheStats                    { return CacheStats{} }

// ─────────────────────────────────────────────────────────────────────────────
// InstrumentedHashCache — decorator that records cache operations
// ─────────────────────────────────────────────────────────────────────────────

// InstrumentedHashCache wraps another HashCache and calls hook functions on
// Get and Set operations. Used by the OTEL integration without coupling the
// core cache to the otel SDK.
type InstrumentedHashCache struct {
	inner  HashCache
	onHit  func(layerID LayerID, relPath string)
	onMiss func(layerID LayerID, relPath string)
	onSet  func(layerID LayerID, relPath string, h FileHash)
}

// NewInstrumentedHashCache wraps inner. Nil hook functions are ignored.
func NewInstrumentedHashCache(
	inner HashCache,
	onHit func(LayerID, string),
	onMiss func(LayerID, string),
	onSet func(LayerID, string, FileHash),
) *InstrumentedHashCache {
	return &InstrumentedHashCache{
		inner: inner, onHit: onHit, onMiss: onMiss, onSet: onSet,
	}
}

// Get implements HashCache.
func (c *InstrumentedHashCache) Get(layerID LayerID, relPath string) (FileHash, bool) {
	h, ok := c.inner.Get(layerID, relPath)
	if ok {
		if c.onHit != nil {
			c.onHit(layerID, relPath)
		}
	} else {
		if c.onMiss != nil {
			c.onMiss(layerID, relPath)
		}
	}
	return h, ok
}

// Set implements HashCache.
func (c *InstrumentedHashCache) Set(layerID LayerID, relPath string, h FileHash) {
	c.inner.Set(layerID, relPath, h)
	if c.onSet != nil {
		c.onSet(layerID, relPath, h)
	}
}

func (c *InstrumentedHashCache) Delete(l LayerID, p string) { c.inner.Delete(l, p) }
func (c *InstrumentedHashCache) DeleteLayer(l LayerID)      { c.inner.DeleteLayer(l) }
func (c *InstrumentedHashCache) Len() int                   { return c.inner.Len() }
func (c *InstrumentedHashCache) Stats() CacheStats          { return c.inner.Stats() }

// ─────────────────────────────────────────────────────────────────────────────
// TTLHashCache — evicts entries older than TTL
// ─────────────────────────────────────────────────────────────────────────────

// TTLHashCache wraps a HashCache and adds time-to-live expiry.
// Stale entries are detected on Get; background sweeping is done lazily.
type TTLHashCache struct {
	inner HashCache
	ttl   time.Duration
	times sync.Map // cacheKey → time.Time
}

// NewTTLHashCache wraps inner with a TTL. Entries are considered stale after ttl.
func NewTTLHashCache(inner HashCache, ttl time.Duration) *TTLHashCache {
	return &TTLHashCache{inner: inner, ttl: ttl}
}

// Get implements HashCache — returns miss for stale entries.
func (c *TTLHashCache) Get(layerID LayerID, relPath string) (FileHash, bool) {
	k := fmt.Sprintf("%s\x00%s", layerID, relPath)
	if t, ok := c.times.Load(k); ok {
		if time.Since(t.(time.Time)) > c.ttl {
			c.inner.Delete(layerID, relPath)
			c.times.Delete(k)
			return "", false
		}
	}
	return c.inner.Get(layerID, relPath)
}

// Set implements HashCache.
func (c *TTLHashCache) Set(layerID LayerID, relPath string, h FileHash) {
	k := fmt.Sprintf("%s\x00%s", layerID, relPath)
	c.inner.Set(layerID, relPath, h)
	c.times.Store(k, time.Now())
}

func (c *TTLHashCache) Delete(l LayerID, p string) { c.inner.Delete(l, p) }
func (c *TTLHashCache) DeleteLayer(l LayerID)      { c.inner.DeleteLayer(l) }
func (c *TTLHashCache) Len() int                   { return c.inner.Len() }
func (c *TTLHashCache) Stats() CacheStats          { return c.inner.Stats() }
