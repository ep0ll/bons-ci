package cache

import (
	"context"
	"sync"
	"time"
)

// Memory is a thread-safe in-memory cache store. It uses a sync.RWMutex
// for concurrent read access with exclusive writes, optimized for the
// read-heavy cache probe pattern.
type Memory struct {
	mu      sync.RWMutex
	entries map[Key]*Record
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
		return "", false, nil
	}
	return rec.ResultID, true, nil
}

// Save stores a result under the given key.
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

// Load retrieves the full record for a key.
func (m *Memory) Load(_ context.Context, key Key) (Record, error) {
	m.mu.RLock()
	rec, ok := m.entries[key]
	m.mu.RUnlock()
	if !ok {
		return Record{}, &ErrNotFound{Key: key}
	}
	return *rec, nil
}

// Release removes a cache entry.
func (m *Memory) Release(_ context.Context, key Key) error {
	m.mu.Lock()
	delete(m.entries, key)
	m.mu.Unlock()
	return nil
}

// Walk iterates over all entries. The callback receives a snapshot of each
// record; modifications to the store during Walk are permitted.
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

// Size returns the number of entries in the store.
func (m *Memory) Size() int {
	m.mu.RLock()
	defer m.mu.RUnlock()
	return len(m.entries)
}
