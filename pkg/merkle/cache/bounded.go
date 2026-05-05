package cache

import (
	"container/list"
	"hash/fnv"
	"sync"
	"sync/atomic"
	"time"

	"github.com/user/layermerkle/layer"
)

// ─────────────────────────────────────────────────────────────────────────────
// BoundedCache — sharded LRU + TTL cache
// ─────────────────────────────────────────────────────────────────────────────

// BoundedCache wraps ShardedCache with per-shard LRU eviction and optional
// TTL expiry. When a shard exceeds its cap, the least-recently-used entry is
// evicted before inserting. TTL entries are expired lazily on Get and
// proactively by a background GC goroutine.
//
// # Lifecycle
//
//	bc := cache.NewBoundedCache(cache.WithMaxEntriesPerShard(1000), cache.WithTTL(5*time.Minute))
//	bc.Start()       // starts background GC (no-op if TTL==0)
//	defer bc.Stop()  // blocks until GC goroutine exits cleanly
//
// BoundedCache satisfies the Cache interface and is safe for concurrent use.
type BoundedCache struct {
	shards      [numShards]boundedShard
	maxPerShard int           // 0 = unlimited
	ttl         time.Duration // 0 = no expiry

	// GC lifecycle — stopGC is closed by Stop() to signal the goroutine.
	// gcDone is closed by the goroutine when it exits.
	// Both are nil if Start() was never called.
	stopGC  chan struct{}
	gcDone  chan struct{}
	gcOnce  sync.Once // ensures Stop() waits for GC exactly once

	hits       atomic.Int64
	misses     atomic.Int64
	entries    atomic.Int64
	tombstones atomic.Int64
	evictions  atomic.Int64
}

// boundedShard is a single cache bucket with an LRU list.
// Uses sync.Mutex (not RWMutex) because every Get promotes the entry to the
// front of the LRU list, making reads also writes to the list.
type boundedShard struct {
	mu      sync.Mutex
	entries map[CacheKey]*list.Element
	lru     *list.List // front = most-recently used; back = LRU candidate
}

// lruEntry is the list element payload.
type lruEntry struct {
	key   CacheKey
	value CacheEntry
}

// ─────────────────────────────────────────────────────────────────────────────
// Options
// ─────────────────────────────────────────────────────────────────────────────

// BoundedCacheOption configures a BoundedCache.
type BoundedCacheOption func(*BoundedCache)

// WithMaxEntriesPerShard caps each shard at n entries. When the cap is reached
// the least-recently-used entry is evicted before insertion.
// 0 means unlimited (behaves identically to ShardedCache for that dimension).
func WithMaxEntriesPerShard(n int) BoundedCacheOption {
	return func(bc *BoundedCache) { bc.maxPerShard = n }
}

// WithTTL sets the time-to-live for cache entries. Get returns a miss for
// entries older than TTL and removes them from the cache. Background GC runs
// at TTL/2 intervals when Start() is called.
// 0 means no expiry.
func WithTTL(d time.Duration) BoundedCacheOption {
	return func(bc *BoundedCache) { bc.ttl = d }
}

// ─────────────────────────────────────────────────────────────────────────────
// Constructor
// ─────────────────────────────────────────────────────────────────────────────

// NewBoundedCache creates a BoundedCache. Call Start() to enable background GC.
func NewBoundedCache(opts ...BoundedCacheOption) *BoundedCache {
	bc := &BoundedCache{}
	for _, o := range opts {
		o(bc)
	}
	for i := range bc.shards {
		bc.shards[i].entries = make(map[CacheKey]*list.Element)
		bc.shards[i].lru = list.New()
	}
	return bc
}

// ─────────────────────────────────────────────────────────────────────────────
// Lifecycle
// ─────────────────────────────────────────────────────────────────────────────

// Start launches the background GC goroutine. Calling Start when TTL == 0 is a
// no-op. Calling Start more than once panics.
func (bc *BoundedCache) Start() {
	if bc.ttl == 0 {
		return
	}
	bc.gcOnce.Do(func() {
		bc.stopGC = make(chan struct{})
		bc.gcDone = make(chan struct{})
		interval := bc.ttl / 2
		if interval < time.Millisecond {
			interval = time.Millisecond
		}
		go func() {
			defer close(bc.gcDone)
			t := time.NewTicker(interval)
			defer t.Stop()
			for {
				select {
				case <-bc.stopGC:
					return
				case <-t.C:
					bc.gcExpired()
				}
			}
		}()
	})
}

// Stop signals the background GC goroutine to exit and blocks until it does.
// Safe to call even if Start() was never called or if TTL == 0.
// After Stop() returns, no further GC sweeps will run.
func (bc *BoundedCache) Stop() {
	if bc.stopGC == nil || bc.gcDone == nil {
		return // never started
	}
	// Close stopGC to signal exit. The channel is buffered=0 so we use close
	// to broadcast to the single goroutine reliably.
	select {
	case <-bc.stopGC:
		// already closed — Stop() was called twice; second call is a no-op.
	default:
		close(bc.stopGC)
	}
	<-bc.gcDone // wait for goroutine to fully exit
}

// ─────────────────────────────────────────────────────────────────────────────
// Cache interface — Get
// ─────────────────────────────────────────────────────────────────────────────

// Get retrieves an entry. Expired entries (TTL) are lazily removed and treated
// as misses. On a hit, the entry is promoted to the front of the LRU list.
func (bc *BoundedCache) Get(key CacheKey) (CacheEntry, bool) {
	s := bc.shardFor(key)
	s.mu.Lock()
	elem, ok := s.entries[key]
	if !ok {
		s.mu.Unlock()
		bc.misses.Add(1)
		return CacheEntry{}, false
	}
	entry := elem.Value.(*lruEntry).value
	if bc.isExpired(entry) {
		s.lru.Remove(elem)
		delete(s.entries, key)
		s.mu.Unlock()
		bc.entries.Add(-1)
		if entry.Tombstone {
			bc.tombstones.Add(-1)
		}
		bc.misses.Add(1)
		return CacheEntry{}, false
	}
	s.lru.MoveToFront(elem)
	s.mu.Unlock()
	bc.hits.Add(1)
	return entry, true
}

// ─────────────────────────────────────────────────────────────────────────────
// Cache interface — Set / SetIfAbsent / Delete
// ─────────────────────────────────────────────────────────────────────────────

// Set stores or overwrites the entry for key. If the shard is at capacity, the
// LRU entry is evicted first. Uses the entry's existing CachedAt if set.
func (bc *BoundedCache) Set(key CacheKey, entry CacheEntry) {
	if entry.CachedAt.IsZero() {
		entry.CachedAt = time.Now()
	}
	s := bc.shardFor(key)
	s.mu.Lock()
	bc.setLocked(s, key, entry)
	s.mu.Unlock()
}

// SetIfAbsent stores the entry only when the key is absent (or expired).
// Returns (existing, false) when the key was already present and not expired;
// returns (entry, true) when the entry was inserted.
func (bc *BoundedCache) SetIfAbsent(key CacheKey, entry CacheEntry) (CacheEntry, bool) {
	if entry.CachedAt.IsZero() {
		entry.CachedAt = time.Now()
	}
	s := bc.shardFor(key)
	s.mu.Lock()
	if elem, ok := s.entries[key]; ok {
		existing := elem.Value.(*lruEntry).value
		if !bc.isExpired(existing) {
			s.lru.MoveToFront(elem)
			s.mu.Unlock()
			return existing, false
		}
		// Expired: remove it, then fall through to insert below.
		s.lru.Remove(elem)
		delete(s.entries, key)
		bc.entries.Add(-1)
		if existing.Tombstone {
			bc.tombstones.Add(-1)
		}
	}
	bc.setLocked(s, key, entry)
	s.mu.Unlock()
	return entry, true
}

// Delete removes the entry for key. No-op if absent.
func (bc *BoundedCache) Delete(key CacheKey) {
	s := bc.shardFor(key)
	s.mu.Lock()
	if elem, ok := s.entries[key]; ok {
		old := elem.Value.(*lruEntry).value
		s.lru.Remove(elem)
		delete(s.entries, key)
		s.mu.Unlock()
		bc.entries.Add(-1)
		if old.Tombstone {
			bc.tombstones.Add(-1)
		}
		return
	}
	s.mu.Unlock()
}

// ─────────────────────────────────────────────────────────────────────────────
// Cache interface — WalkLayer / EvictLayer
// ─────────────────────────────────────────────────────────────────────────────

// WalkLayer iterates over non-expired entries for the given layer. The fn is
// called outside the shard lock so it may perform arbitrary work.
func (bc *BoundedCache) WalkLayer(layerDigest layer.Digest, fn func(CacheKey, CacheEntry) bool) {
	type pair struct {
		k CacheKey
		v CacheEntry
	}
	for i := range bc.shards {
		s := &bc.shards[i]
		s.mu.Lock()
		var batch []pair
		for k, elem := range s.entries {
			if k.LayerDigest == layerDigest {
				v := elem.Value.(*lruEntry).value
				if !bc.isExpired(v) {
					batch = append(batch, pair{k, v})
				}
			}
		}
		s.mu.Unlock()
		for _, p := range batch {
			if !fn(p.k, p.v) {
				return
			}
		}
	}
}

// EvictLayer removes all entries (including tombstones and expired) for layerDigest.
func (bc *BoundedCache) EvictLayer(layerDigest layer.Digest) {
	for i := range bc.shards {
		s := &bc.shards[i]
		s.mu.Lock()
		for k, elem := range s.entries {
			if k.LayerDigest == layerDigest {
				v := elem.Value.(*lruEntry).value
				s.lru.Remove(elem)
				delete(s.entries, k)
				bc.entries.Add(-1)
				if v.Tombstone {
					bc.tombstones.Add(-1)
				}
			}
		}
		s.mu.Unlock()
	}
}

// ─────────────────────────────────────────────────────────────────────────────
// Stats / Evictions
// ─────────────────────────────────────────────────────────────────────────────

// Stats returns a point-in-time snapshot of cache metrics.
func (bc *BoundedCache) Stats() Stats {
	return Stats{
		Entries:    bc.entries.Load(),
		Tombstones: bc.tombstones.Load(),
		Hits:       bc.hits.Load(),
		Misses:     bc.misses.Load(),
		Shards:     numShards,
	}
}

// Evictions returns the cumulative number of LRU-evicted entries.
func (bc *BoundedCache) Evictions() int64 { return bc.evictions.Load() }

// ─────────────────────────────────────────────────────────────────────────────
// Internal helpers
// ─────────────────────────────────────────────────────────────────────────────

// setLocked inserts or replaces key inside shard s. Must be called with s.mu held.
// For an overwrite, the entry count and tombstone count are adjusted as needed.
// For an insert, LRU eviction fires if the shard is at capacity.
func (bc *BoundedCache) setLocked(s *boundedShard, key CacheKey, entry CacheEntry) {
	if elem, ok := s.entries[key]; ok {
		// Overwrite existing entry (may or may not be expired — we honour Set semantics).
		old := elem.Value.(*lruEntry).value
		elem.Value = &lruEntry{key: key, value: entry}
		s.lru.MoveToFront(elem)
		switch {
		case !old.Tombstone && entry.Tombstone:
			bc.tombstones.Add(1)
		case old.Tombstone && !entry.Tombstone:
			bc.tombstones.Add(-1)
		}
		// Entry count stays the same (replace in-place, no net change).
		return
	}
	// Insert: evict LRU if at capacity.
	if bc.maxPerShard > 0 && len(s.entries) >= bc.maxPerShard {
		bc.evictLRULocked(s)
	}
	elem := s.lru.PushFront(&lruEntry{key: key, value: entry})
	s.entries[key] = elem
	bc.entries.Add(1)
	if entry.Tombstone {
		bc.tombstones.Add(1)
	}
}

// evictLRULocked removes the back (least-recently-used) element from shard s.
// Must be called with s.mu held.
func (bc *BoundedCache) evictLRULocked(s *boundedShard) {
	back := s.lru.Back()
	if back == nil {
		return
	}
	le := back.Value.(*lruEntry)
	s.lru.Remove(back)
	delete(s.entries, le.key)
	bc.entries.Add(-1)
	if le.value.Tombstone {
		bc.tombstones.Add(-1)
	}
	bc.evictions.Add(1)
}

// gcExpired scans all shards and evicts entries whose TTL has elapsed.
func (bc *BoundedCache) gcExpired() {
	if bc.ttl == 0 {
		return
	}
	for i := range bc.shards {
		s := &bc.shards[i]
		s.mu.Lock()
		for k, elem := range s.entries {
			v := elem.Value.(*lruEntry).value
			if bc.isExpired(v) {
				s.lru.Remove(elem)
				delete(s.entries, k)
				bc.entries.Add(-1)
				if v.Tombstone {
					bc.tombstones.Add(-1)
				}
			}
		}
		s.mu.Unlock()
	}
}

// isExpired reports whether e has exceeded the configured TTL.
func (bc *BoundedCache) isExpired(e CacheEntry) bool {
	return bc.ttl > 0 && !e.CachedAt.IsZero() && time.Since(e.CachedAt) > bc.ttl
}

// shardFor returns the shard responsible for key using FNV-32a with a zero-byte
// separator between the two key components (prevents ambiguous keys).
func (bc *BoundedCache) shardFor(key CacheKey) *boundedShard {
	h := fnv.New32a()
	_, _ = h.Write([]byte(key.LayerDigest))
	h.Write([]byte{0})
	_, _ = h.Write([]byte(key.FilePath))
	return &bc.shards[h.Sum32()%numShards]
}

// Compile-time assertion.
var _ Cache = (*BoundedCache)(nil)
