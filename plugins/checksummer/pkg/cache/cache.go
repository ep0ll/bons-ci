// Package cache provides a sharded, concurrent LRU cache with TTL eviction.
//
// Sharding eliminates lock contention: N shards each have their own RWMutex.
// Within each shard an intrusive doubly-linked list tracks LRU order without
// additional heap allocations beyond the entry itself.
package cache

import (
	"fmt"
	"sync"
	"time"
)

const (
	// DefaultShards is the number of independent LRU shards (must be power of 2).
	DefaultShards = 64
	// defaultMax is the default maximum entries per shard.
	defaultMax = 1024
)

// ─────────────────────────── entry ───────────────────────────────────────────

type entry struct {
	key        string
	hash       []byte
	expiresAt  int64 // Unix ns; 0 = no expiry
	prev, next *entry
}

func (e *entry) expired(now int64) bool {
	return e.expiresAt != 0 && now > e.expiresAt
}

// ─────────────────────────── shard ───────────────────────────────────────────

type shard struct {
	mu      sync.RWMutex
	entries map[string]*entry
	head    *entry // MRU
	tail    *entry // LRU
	count   int
	maxSize int
	evicts  int64
}

func newShard(maxSize int) *shard {
	return &shard{
		entries: make(map[string]*entry, maxSize/2),
		maxSize: maxSize,
	}
}

func (s *shard) get(key string, now int64) ([]byte, bool) {
	s.mu.Lock()
	defer s.mu.Unlock()
	e, ok := s.entries[key]
	if !ok {
		return nil, false
	}
	if e.expired(now) {
		s.remove(e)
		return nil, false
	}
	s.moveToFront(e)
	cp := make([]byte, len(e.hash))
	copy(cp, e.hash)
	return cp, true
}

func (s *shard) set(key string, hash []byte, ttl time.Duration) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if e, ok := s.entries[key]; ok {
		cp := make([]byte, len(hash))
		copy(cp, hash)
		e.hash = cp
		if ttl > 0 {
			e.expiresAt = time.Now().Add(ttl).UnixNano()
		} else {
			e.expiresAt = 0
		}
		s.moveToFront(e)
		return
	}
	for s.count >= s.maxSize && s.tail != nil {
		s.remove(s.tail)
		s.evicts++
	}
	cp := make([]byte, len(hash))
	copy(cp, hash)
	e := &entry{key: key, hash: cp}
	if ttl > 0 {
		e.expiresAt = time.Now().Add(ttl).UnixNano()
	}
	s.entries[key] = e
	s.prepend(e)
	s.count++
}

func (s *shard) delete(key string) bool {
	s.mu.Lock()
	defer s.mu.Unlock()
	e, ok := s.entries[key]
	if !ok {
		return false
	}
	s.remove(e)
	return true
}

func (s *shard) prepend(e *entry) {
	e.prev = nil
	e.next = s.head
	if s.head != nil {
		s.head.prev = e
	}
	s.head = e
	if s.tail == nil {
		s.tail = e
	}
}

func (s *shard) moveToFront(e *entry) {
	if s.head == e {
		return
	}
	s.unlink(e)
	s.prepend(e)
}

func (s *shard) unlink(e *entry) {
	if e.prev != nil {
		e.prev.next = e.next
	} else {
		s.head = e.next
	}
	if e.next != nil {
		e.next.prev = e.prev
	} else {
		s.tail = e.prev
	}
	e.prev, e.next = nil, nil
}

func (s *shard) remove(e *entry) {
	s.unlink(e)
	delete(s.entries, e.key)
	s.count--
}

// ─────────────────────────── Cache ───────────────────────────────────────────

// Cache is a sharded concurrent LRU cache for hash results.
// Safe for concurrent use.
type Cache struct {
	shards [DefaultShards]*shard
	mask   uint64
	ttl    time.Duration
}

// Option configures a Cache.
type Option func(*Cache)

// WithTTL sets a time-to-live for all cache entries.
func WithTTL(d time.Duration) Option {
	return func(c *Cache) { c.ttl = d }
}

// WithMaxEntries sets the max entries per shard.
// Total capacity = n × DefaultShards.
func WithMaxEntries(n int) Option {
	return func(c *Cache) {
		for i := range c.shards {
			c.shards[i] = newShard(n)
		}
	}
}

// New creates a Cache.
func New(opts ...Option) *Cache {
	c := &Cache{mask: DefaultShards - 1}
	for i := range c.shards {
		c.shards[i] = newShard(defaultMax)
	}
	for _, o := range opts {
		o(c)
	}
	return c
}

// Get retrieves a hash by key.  Returns (nil, false) on miss or expiry.
func (c *Cache) Get(key string) ([]byte, bool) {
	return c.shard(key).get(key, time.Now().UnixNano())
}

// Set stores a hash with the cache's default TTL.
func (c *Cache) Set(key string, hash []byte) {
	c.shard(key).set(key, hash, c.ttl)
}

// SetWithTTL stores a hash with an explicit TTL.
func (c *Cache) SetWithTTL(key string, hash []byte, ttl time.Duration) {
	c.shard(key).set(key, hash, ttl)
}

// Delete removes a key.  Returns true if it existed.
func (c *Cache) Delete(key string) bool { return c.shard(key).delete(key) }

// Stats returns aggregate statistics across all shards.
func (c *Cache) Stats() Stats {
	var st Stats
	for _, s := range c.shards {
		s.mu.RLock()
		st.Entries += int64(s.count)
		st.Evictions += s.evicts
		s.mu.RUnlock()
	}
	return st
}

func (c *Cache) shard(key string) *shard {
	return c.shards[fnv64a(key)&c.mask]
}

func fnv64a(s string) uint64 {
	const (
		offset = 14695981039346656037
		prime  = 1099511628211
	)
	h := uint64(offset)
	for i := 0; i < len(s); i++ {
		h ^= uint64(s[i])
		h *= prime
	}
	return h
}

// Stats is a point-in-time view of cache internals.
type Stats struct {
	Entries   int64
	Evictions int64
}

func (s Stats) String() string {
	return fmt.Sprintf("entries=%d evictions=%d", s.Entries, s.Evictions)
}
