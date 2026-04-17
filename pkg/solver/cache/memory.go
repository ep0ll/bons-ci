package cache

import (
	"context"
	"sync"
	"sync/atomic"
	"time"
)

// Memory is a thread-safe in-memory cache store optimised for the
// read-heavy probe pattern. It uses a RWMutex so concurrent probes proceed
// without contention; writes acquire an exclusive lock only.
//
// Memory implements Store.
type Memory struct {
	mu      sync.RWMutex
	entries map[Key]*Record

	// Instrumentation counters — safe to read without mu.
	hits   atomic.Int64
	misses atomic.Int64
}

// NewMemory creates a new in-memory cache store.
func NewMemory() *Memory {
	return &Memory{
		entries: make(map[Key]*Record),
	}
}

// Probe checks for a cached result. This is the hot path — uses a read lock.
func (m *Memory) Probe(_ context.Context, key Key) (string, bool, error) {
	m.mu.RLock()
	rec, ok := m.entries[key]
	m.mu.RUnlock()
	if !ok {
		m.misses.Add(1)
		return "", false, nil
	}
	m.hits.Add(1)
	return rec.ResultID, true, nil
}

// Save stores a result under the given key. Overwrites any existing entry.
func (m *Memory) Save(_ context.Context, key Key, resultID string, size int) error {
	m.mu.Lock()
	m.entries[key] = &Record{
		Key:       key,
		ResultID:  resultID,
		Size:      size,
		CreatedAt: time.Now(),
	}
	m.mu.Unlock()
	return nil
}

// SaveWithPriority stores a result with an explicit priority value.
func (m *Memory) SaveWithPriority(_ context.Context, key Key, resultID string, size, priority int) error {
	m.mu.Lock()
	m.entries[key] = &Record{
		Key:       key,
		ResultID:  resultID,
		Size:      size,
		CreatedAt: time.Now(),
		Priority:  priority,
	}
	m.mu.Unlock()
	return nil
}

// Load retrieves the full Record for a key.
func (m *Memory) Load(_ context.Context, key Key) (Record, error) {
	m.mu.RLock()
	rec, ok := m.entries[key]
	m.mu.RUnlock()
	if !ok {
		return Record{}, &ErrNotFound{Key: key}
	}
	return *rec, nil
}

// Release removes an entry. Silently succeeds if absent.
func (m *Memory) Release(_ context.Context, key Key) error {
	m.mu.Lock()
	delete(m.entries, key)
	m.mu.Unlock()
	return nil
}

// Walk iterates over all entries. It snapshots the map under a read lock and
// then calls fn without holding the lock, so the store can be safely modified
// concurrently during iteration.
func (m *Memory) Walk(_ context.Context, fn func(Record) error) error {
	m.mu.RLock()
	snapshot := make([]Record, 0, len(m.entries))
	for _, rec := range m.entries {
		snapshot = append(snapshot, *rec)
	}
	m.mu.RUnlock()

	for _, rec := range snapshot {
		if err := fn(rec); err != nil {
			return err
		}
	}
	return nil
}

// Stats returns current hit/miss counters and entry count.
func (m *Memory) Stats(_ context.Context) (Stats, error) {
	m.mu.RLock()
	n := len(m.entries)
	var total int64
	for _, r := range m.entries {
		total += int64(r.Size)
	}
	m.mu.RUnlock()
	return Stats{
		Entries:   n,
		TotalSize: total,
		Hits:      m.hits.Load(),
		Misses:    m.misses.Load(),
	}, nil
}

// Len returns the number of entries without allocating a snapshot.
func (m *Memory) Len() int {
	m.mu.RLock()
	n := len(m.entries)
	m.mu.RUnlock()
	return n
}
