package reactdag

import (
	"context"
	"fmt"
	"sync"
	"time"
)

// ---------------------------------------------------------------------------
// CacheEntry
// ---------------------------------------------------------------------------

// CacheEntry holds the serialised result of a completed vertex execution.
// An entry may represent either a successful result or a cached failure.
type CacheEntry struct {
	Key         CacheKey
	OutputFiles []FileRef
	// CachedErr, if non-nil, means this entry records a cached failure.
	// The scheduler can replay the error without re-executing the operation.
	CachedErr  string
	CachedAt   time.Time
	HitCount   int
	DurationMS int64 // original execution duration in milliseconds
}

// IsFailed reports whether this entry represents a cached failure.
func (e *CacheEntry) IsFailed() bool { return e.CachedErr != "" }

// CachedError returns the reconstructed error, or nil for a success entry.
func (e *CacheEntry) CachedError() error {
	if e.CachedErr == "" {
		return nil
	}
	return fmt.Errorf("%s", e.CachedErr)
}

// ---------------------------------------------------------------------------
// MemoryCacheStore — fast tier
// ---------------------------------------------------------------------------

// MemoryCacheStore is an in-process, LRU-evictable cache store. It is safe
// for concurrent use and suitable as the fast tier in a two-tier setup.
type MemoryCacheStore struct {
	mu      sync.RWMutex
	entries map[CacheKey]*CacheEntry
	maxSize int // 0 = unlimited
}

// NewMemoryCacheStore returns a store with an optional maximum entry count.
// Pass 0 for unlimited capacity.
func NewMemoryCacheStore(maxSize int) *MemoryCacheStore {
	return &MemoryCacheStore{
		entries: make(map[CacheKey]*CacheEntry),
		maxSize: maxSize,
	}
}

// Get retrieves a cache entry. Returns (nil, nil) on a miss.
func (s *MemoryCacheStore) Get(_ context.Context, key CacheKey) (*CacheEntry, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	e, ok := s.entries[key]
	if !ok {
		return nil, nil
	}
	e.HitCount++
	return e, nil
}

// Set stores a cache entry, evicting the oldest entry if the store is full.
func (s *MemoryCacheStore) Set(_ context.Context, key CacheKey, entry *CacheEntry) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.maxSize > 0 && len(s.entries) >= s.maxSize {
		s.evictOldest()
	}
	s.entries[key] = entry
	return nil
}

// Delete removes an entry by key.
func (s *MemoryCacheStore) Delete(_ context.Context, key CacheKey) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	delete(s.entries, key)
	return nil
}

// Exists reports whether a key is present without incrementing hit count.
func (s *MemoryCacheStore) Exists(_ context.Context, key CacheKey) (bool, error) {
	s.mu.RLock()
	defer s.mu.RUnlock()
	_, ok := s.entries[key]
	return ok, nil
}

// evictOldest removes the entry with the oldest CachedAt timestamp.
// Caller must hold write lock.
func (s *MemoryCacheStore) evictOldest() {
	var oldest CacheKey
	var oldestTime time.Time
	first := true
	for k, e := range s.entries {
		if first || e.CachedAt.Before(oldestTime) {
			oldest = k
			oldestTime = e.CachedAt
			first = false
		}
	}
	delete(s.entries, oldest)
}

// ---------------------------------------------------------------------------
// CacheStats
// ---------------------------------------------------------------------------

// CacheStats is a point-in-time snapshot of cache occupancy.
type CacheStats struct {
	Entries   int
	TotalHits int
}

// Stats returns a non-locking snapshot of store statistics.
func (s *MemoryCacheStore) Stats() CacheStats {
	s.mu.RLock()
	defer s.mu.RUnlock()
	var hits int
	for _, e := range s.entries {
		hits += e.HitCount
	}
	return CacheStats{Entries: len(s.entries), TotalHits: hits}
}

// ---------------------------------------------------------------------------
// NoopCacheStore — useful when a tier is disabled
// ---------------------------------------------------------------------------

// NoopCacheStore always returns misses and silently discards writes.
// Use it as the slow-tier placeholder when no remote cache is available.
type NoopCacheStore struct{}

func (NoopCacheStore) Get(_ context.Context, _ CacheKey) (*CacheEntry, error) { return nil, nil }
func (NoopCacheStore) Set(_ context.Context, _ CacheKey, _ *CacheEntry) error { return nil }
func (NoopCacheStore) Delete(_ context.Context, _ CacheKey) error             { return nil }
func (NoopCacheStore) Exists(_ context.Context, _ CacheKey) (bool, error)     { return false, nil }
