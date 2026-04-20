package reactdag

import (
	"context"
	"fmt"
	"sort"
	"sync"
	"time"
)

// ---------------------------------------------------------------------------
// EvictionPolicy — pluggable cache eviction strategy
// ---------------------------------------------------------------------------

// EvictionPolicy determines which cache entries to remove when the store is full.
type EvictionPolicy interface {
	// Candidate receives all entries and returns the key to evict.
	// It must return one of the provided keys.
	Candidate(entries map[CacheKey]*ManagedEntry) CacheKey
}

// ManagedEntry wraps a CacheEntry with bookkeeping metadata used by eviction
// policies (access time, hit count, size estimate).
type ManagedEntry struct {
	Entry       *CacheEntry
	LastAccess  time.Time
	HitCount    int
	SizeBytes   int64 // estimated: sum of output file sizes
	InsertedAt  time.Time
}

// ---------------------------------------------------------------------------
// Built-in policies
// ---------------------------------------------------------------------------

// LRUPolicy evicts the least recently accessed entry.
type LRUPolicy struct{}

func (LRUPolicy) Candidate(entries map[CacheKey]*ManagedEntry) CacheKey {
	var oldest CacheKey
	var oldestTime time.Time
	first := true
	for k, e := range entries {
		if first || e.LastAccess.Before(oldestTime) {
			oldest = k
			oldestTime = e.LastAccess
			first = false
		}
	}
	return oldest
}

// LFUPolicy evicts the least frequently accessed entry (fewest hits).
// Ties are broken by oldest insertion time.
type LFUPolicy struct{}

func (LFUPolicy) Candidate(entries map[CacheKey]*ManagedEntry) CacheKey {
	var winner CacheKey
	winHits := -1
	var winInsert time.Time
	for k, e := range entries {
		if winHits < 0 || e.HitCount < winHits ||
			(e.HitCount == winHits && e.InsertedAt.Before(winInsert)) {
			winner = k
			winHits = e.HitCount
			winInsert = e.InsertedAt
		}
	}
	return winner
}

// SizePolicy evicts the entry with the largest estimated byte size.
// Use this to bound total cache footprint by data volume.
type SizePolicy struct{}

func (SizePolicy) Candidate(entries map[CacheKey]*ManagedEntry) CacheKey {
	var biggest CacheKey
	var biggestSize int64 = -1
	for k, e := range entries {
		if e.SizeBytes > biggestSize {
			biggest = k
			biggestSize = e.SizeBytes
		}
	}
	return biggest
}

// TTLPolicy evicts the entry that has been in the cache the longest.
// Combined with a background pruner, this implements time-based expiry.
type TTLPolicy struct{ MaxAge time.Duration }

func (p TTLPolicy) Candidate(entries map[CacheKey]*ManagedEntry) CacheKey {
	cutoff := time.Now().Add(-p.MaxAge)
	// Find oldest expired entry; if none expired, fall back to oldest.
	var candidate CacheKey
	var candidateInsert time.Time
	first := true
	for k, e := range entries {
		if e.InsertedAt.Before(cutoff) {
			if first || e.InsertedAt.Before(candidateInsert) {
				candidate = k
				candidateInsert = e.InsertedAt
				first = false
			}
		}
	}
	if !first {
		return candidate
	}
	// No expired entries: evict oldest.
	return LRUPolicy{}.Candidate(entries)
}

// ---------------------------------------------------------------------------
// ManagedMemoryCacheStore — eviction-policy-aware in-memory store
// ---------------------------------------------------------------------------

// ManagedMemoryCacheStore is a MemoryCacheStore replacement that supports
// pluggable eviction policies and bounded total byte capacity.
type ManagedMemoryCacheStore struct {
	mu       sync.Mutex
	entries  map[CacheKey]*ManagedEntry
	maxCount int   // 0 = unlimited
	maxBytes int64 // 0 = unlimited
	policy   EvictionPolicy
}

// NewManagedStore constructs a ManagedMemoryCacheStore.
//
//	maxCount — maximum number of cache entries (0 = unlimited)
//	maxBytes — maximum total byte footprint (0 = unlimited)
//	policy   — eviction policy (nil defaults to LRU)
func NewManagedStore(maxCount int, maxBytes int64, policy EvictionPolicy) *ManagedMemoryCacheStore {
	if policy == nil {
		policy = LRUPolicy{}
	}
	return &ManagedMemoryCacheStore{
		entries:  make(map[CacheKey]*ManagedEntry),
		maxCount: maxCount,
		maxBytes: maxBytes,
		policy:   policy,
	}
}

// Get retrieves an entry and updates its bookkeeping.
func (s *ManagedMemoryCacheStore) Get(_ context.Context, key CacheKey) (*CacheEntry, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	e, ok := s.entries[key]
	if !ok {
		return nil, nil
	}
	e.HitCount++
	e.LastAccess = time.Now()
	return e.Entry, nil
}

// Set stores an entry, evicting as necessary to respect capacity limits.
func (s *ManagedMemoryCacheStore) Set(_ context.Context, key CacheKey, entry *CacheEntry) error {
	s.mu.Lock()
	defer s.mu.Unlock()

	managed := &ManagedEntry{
		Entry:      entry,
		LastAccess: time.Now(),
		InsertedAt: time.Now(),
		SizeBytes:  estimateBytes(entry),
	}

	// Replace existing.
	if _, exists := s.entries[key]; exists {
		s.entries[key] = managed
		return nil
	}

	// Evict until within limits.
	for s.shouldEvict(managed.SizeBytes) {
		if len(s.entries) == 0 {
			break
		}
		victim := s.policy.Candidate(s.entries)
		delete(s.entries, victim)
	}

	s.entries[key] = managed
	return nil
}

// Delete removes an entry.
func (s *ManagedMemoryCacheStore) Delete(_ context.Context, key CacheKey) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	delete(s.entries, key)
	return nil
}

// Exists reports presence without updating bookkeeping.
func (s *ManagedMemoryCacheStore) Exists(_ context.Context, key CacheKey) (bool, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	_, ok := s.entries[key]
	return ok, nil
}

// Stats returns live store statistics.
func (s *ManagedMemoryCacheStore) Stats() ManagedStoreStats {
	s.mu.Lock()
	defer s.mu.Unlock()
	var totalBytes int64
	var totalHits int
	for _, e := range s.entries {
		totalBytes += e.SizeBytes
		totalHits += e.HitCount
	}
	return ManagedStoreStats{
		Entries:    len(s.entries),
		TotalBytes: totalBytes,
		TotalHits:  totalHits,
	}
}

// PruneExpired removes all entries older than maxAge.
// Returns the number of entries removed.
func (s *ManagedMemoryCacheStore) PruneExpired(maxAge time.Duration) int {
	s.mu.Lock()
	defer s.mu.Unlock()
	cutoff := time.Now().Add(-maxAge)
	removed := 0
	for k, e := range s.entries {
		if e.InsertedAt.Before(cutoff) {
			delete(s.entries, k)
			removed++
		}
	}
	return removed
}

// TopN returns the N most-accessed entries sorted by hit count descending.
func (s *ManagedMemoryCacheStore) TopN(n int) []ManagedEntryView {
	s.mu.Lock()
	defer s.mu.Unlock()
	views := make([]ManagedEntryView, 0, len(s.entries))
	for k, e := range s.entries {
		views = append(views, ManagedEntryView{Key: k, Managed: *e})
	}
	sort.Slice(views, func(i, j int) bool {
		return views[i].Managed.HitCount > views[j].Managed.HitCount
	})
	if n > 0 && len(views) > n {
		views = views[:n]
	}
	return views
}

// shouldEvict reports whether a new entry of size newBytes would exceed limits.
// Caller must hold the mutex.
func (s *ManagedMemoryCacheStore) shouldEvict(newBytes int64) bool {
	if s.maxCount > 0 && len(s.entries) >= s.maxCount {
		return true
	}
	if s.maxBytes > 0 {
		var used int64
		for _, e := range s.entries {
			used += e.SizeBytes
		}
		if used+newBytes > s.maxBytes {
			return true
		}
	}
	return false
}

// ---------------------------------------------------------------------------
// Supporting types
// ---------------------------------------------------------------------------

// ManagedStoreStats is a snapshot of ManagedMemoryCacheStore counters.
type ManagedStoreStats struct {
	Entries    int
	TotalBytes int64
	TotalHits  int
}

// ManagedEntryView is a read-only snapshot of one managed entry.
type ManagedEntryView struct {
	Key     CacheKey
	Managed ManagedEntry
}

// estimateBytes sums the Size fields of all output files in an entry.
func estimateBytes(e *CacheEntry) int64 {
	var total int64
	for _, f := range e.OutputFiles {
		total += f.Size
	}
	return total
}

// ---------------------------------------------------------------------------
// BackgroundPruner — periodically removes expired entries
// ---------------------------------------------------------------------------

// BackgroundPruner runs a background goroutine that calls PruneExpired on a
// ManagedMemoryCacheStore at a fixed interval.
type BackgroundPruner struct {
	store    *ManagedMemoryCacheStore
	maxAge   time.Duration
	interval time.Duration
	stopCh   chan struct{}
	once     sync.Once
}

// NewBackgroundPruner constructs a pruner.
// interval is how often to run; maxAge is the entry expiry cutoff.
func NewBackgroundPruner(store *ManagedMemoryCacheStore, interval, maxAge time.Duration) *BackgroundPruner {
	return &BackgroundPruner{
		store:    store,
		maxAge:   maxAge,
		interval: interval,
		stopCh:   make(chan struct{}),
	}
}

// Start launches the background pruning goroutine.
func (p *BackgroundPruner) Start(ctx context.Context) {
	go func() {
		ticker := time.NewTicker(p.interval)
		defer ticker.Stop()
		for {
			select {
			case <-ctx.Done():
				return
			case <-p.stopCh:
				return
			case <-ticker.C:
				p.store.PruneExpired(p.maxAge)
			}
		}
	}()
}

// Stop terminates the background goroutine.
func (p *BackgroundPruner) Stop() {
	p.once.Do(func() { close(p.stopCh) })
}

// RenderCacheStats formats ManagedStoreStats as a one-liner.
func RenderCacheStats(stats ManagedStoreStats) string {
	return fmt.Sprintf("entries=%d bytes=%s hits=%d",
		stats.Entries, formatBytes(stats.TotalBytes), stats.TotalHits)
}

// formatBytes converts a byte count to a human-readable string (KB/MB/GB).
func formatBytes(b int64) string {
	switch {
	case b >= 1<<30:
		return fmt.Sprintf("%.2fGB", float64(b)/float64(1<<30))
	case b >= 1<<20:
		return fmt.Sprintf("%.2fMB", float64(b)/float64(1<<20))
	case b >= 1<<10:
		return fmt.Sprintf("%.2fKB", float64(b)/float64(1<<10))
	default:
		return fmt.Sprintf("%dB", b)
	}
}
