package cache

import (
	"sync"

	"github.com/bons/bons-ci/pkg/fshash/internal/core"
)

// Store is the interface for content hash caching.
type Store interface {
	Get(layerID core.LayerID, path string) (core.FileHash, bool)
	Put(layerID core.LayerID, path string, hash core.FileHash)
	Invalidate(layerID core.LayerID, path string)
	InvalidateLayer(layerID core.LayerID)
	Stats() Stats
}

// Stats reports cache performance metrics.
type Stats struct {
	Hits       uint64
	Misses     uint64
	Evictions  uint64
	Size       int
	ShardCount int
}

type lruEntry struct {
	key   string
	value core.FileHash
	prev  *lruEntry
	next  *lruEntry
}

type shard struct {
	mu       sync.Mutex
	entries  map[string]*lruEntry
	head     *lruEntry
	tail     *lruEntry
	capacity int
	hits     uint64
	misses   uint64
	evicts   uint64
}

func newShard(capacity int) *shard {
	return &shard{
		entries:  make(map[string]*lruEntry, capacity),
		capacity: capacity,
	}
}

func (s *shard) get(key string) (core.FileHash, bool) {
	s.mu.Lock()
	defer s.mu.Unlock()

	entry, ok := s.entries[key]
	if !ok {
		s.misses++
		return core.FileHash{}, false
	}
	s.hits++
	s.moveToFront(entry)
	return entry.value, true
}

func (s *shard) put(key string, value core.FileHash) {
	s.mu.Lock()
	defer s.mu.Unlock()

	if entry, ok := s.entries[key]; ok {
		entry.value = value
		s.moveToFront(entry)
		return
	}

	entry := &lruEntry{key: key, value: value}
	s.entries[key] = entry
	s.addToFront(entry)

	if len(s.entries) > s.capacity {
		s.evictLRU()
	}
}

func (s *shard) invalidate(key string) {
	s.mu.Lock()
	defer s.mu.Unlock()

	entry, ok := s.entries[key]
	if !ok {
		return
	}
	s.removeEntry(entry)
	delete(s.entries, key)
}

func (s *shard) invalidateByPrefix(prefix string) {
	s.mu.Lock()
	defer s.mu.Unlock()

	var toDelete []string
	for key := range s.entries {
		if len(key) >= len(prefix) && key[:len(prefix)] == prefix {
			toDelete = append(toDelete, key)
		}
	}
	for _, key := range toDelete {
		entry := s.entries[key]
		s.removeEntry(entry)
		delete(s.entries, key)
	}
}

func (s *shard) size() int {
	s.mu.Lock()
	defer s.mu.Unlock()
	return len(s.entries)
}

func (s *shard) moveToFront(entry *lruEntry) {
	if s.head == entry {
		return
	}
	s.removeEntry(entry)
	s.addToFront(entry)
}

func (s *shard) addToFront(entry *lruEntry) {
	entry.prev = nil
	entry.next = s.head
	if s.head != nil {
		s.head.prev = entry
	}
	s.head = entry
	if s.tail == nil {
		s.tail = entry
	}
}

func (s *shard) removeEntry(entry *lruEntry) {
	if entry.prev != nil {
		entry.prev.next = entry.next
	} else {
		s.head = entry.next
	}
	if entry.next != nil {
		entry.next.prev = entry.prev
	} else {
		s.tail = entry.prev
	}
	entry.prev = nil
	entry.next = nil
}

func (s *shard) evictLRU() {
	if s.tail == nil {
		return
	}
	victim := s.tail
	s.removeEntry(victim)
	delete(s.entries, victim.key)
	s.evicts++
}

// ShardedStore distributes entries across N shards using FNV-1a hashing.
type ShardedStore struct {
	shards    []*shard
	shardMask uint64
}

// NewShardedStore creates a sharded LRU cache.
func NewShardedStore(shardCount, maxEntries int) *ShardedStore {
	if shardCount <= 0 {
		shardCount = 16
	}
	if maxEntries <= 0 {
		maxEntries = 1 << 16
	}

	n := uint64(1)
	for n < uint64(shardCount) {
		n <<= 1
	}

	perShard := maxEntries / int(n)
	if perShard < 16 {
		perShard = 16
	}

	shards := make([]*shard, n)
	for i := range shards {
		shards[i] = newShard(perShard)
	}

	return &ShardedStore{
		shards:    shards,
		shardMask: n - 1,
	}
}

func (ss *ShardedStore) Get(layerID core.LayerID, path string) (core.FileHash, bool) {
	key := compositeKey(layerID, path)
	return ss.getShard(key).get(key)
}

func (ss *ShardedStore) Put(layerID core.LayerID, path string, hash core.FileHash) {
	key := compositeKey(layerID, path)
	ss.getShard(key).put(key, hash)
}

func (ss *ShardedStore) Invalidate(layerID core.LayerID, path string) {
	key := compositeKey(layerID, path)
	ss.getShard(key).invalidate(key)
}

func (ss *ShardedStore) InvalidateLayer(layerID core.LayerID) {
	prefix := layerID.String() + "\x00"
	for _, s := range ss.shards {
		s.invalidateByPrefix(prefix)
	}
}

func (ss *ShardedStore) Stats() Stats {
	var st Stats
	st.ShardCount = len(ss.shards)
	for _, s := range ss.shards {
		s.mu.Lock()
		st.Hits += s.hits
		st.Misses += s.misses
		st.Evictions += s.evicts
		st.Size += len(s.entries)
		s.mu.Unlock()
	}
	return st
}

func (ss *ShardedStore) getShard(key string) *shard {
	h := fnvHash(key)
	return ss.shards[h&ss.shardMask]
}

func compositeKey(layerID core.LayerID, path string) string {
	return layerID.String() + "\x00" + path
}

func fnvHash(s string) uint64 {
	const (
		offset64 = 14695981039346656037
		prime64  = 1099511628211
	)
	h := uint64(offset64)
	for i := 0; i < len(s); i++ {
		h ^= uint64(s[i])
		h *= prime64
	}
	return h
}
