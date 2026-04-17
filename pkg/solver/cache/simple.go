package cache

import (
	"context"
	"fmt"
	"sync"
	"time"
)

// Memory is a simple in-memory Store implementation that wraps
// InMemoryKeyStorage. Provides backward compatibility with the
// solver coordinator's Store interface.
type Memory struct {
	mu      sync.RWMutex
	entries map[string]*Record // keyed by "digest:output"
}

// NewMemory creates a new simple in-memory cache store.
func NewMemory() *Memory {
	return &Memory{
		entries: make(map[string]*Record),
	}
}

func makeSimpleKey(key Key) string {
	return fmt.Sprintf("%s:%d", key.Digest, key.Output)
}

func (m *Memory) Probe(_ context.Context, key Key) (string, bool, error) {
	m.mu.RLock()
	defer m.mu.RUnlock()
	r, ok := m.entries[makeSimpleKey(key)]
	if !ok {
		return "", false, nil
	}
	return r.ResultID, true, nil
}

func (m *Memory) Save(_ context.Context, key Key, resultID string, size int) error {
	m.mu.Lock()
	defer m.mu.Unlock()
	m.entries[makeSimpleKey(key)] = &Record{
		ResultID:  resultID,
		Size:      size,
		CreatedAt: time.Now(),
	}
	return nil
}

func (m *Memory) Load(_ context.Context, key Key) (*Record, error) {
	m.mu.RLock()
	defer m.mu.RUnlock()
	r, ok := m.entries[makeSimpleKey(key)]
	if !ok {
		return nil, ErrNotFound
	}
	return r, nil
}

func (m *Memory) Release(_ context.Context, key Key) error {
	m.mu.Lock()
	defer m.mu.Unlock()
	delete(m.entries, makeSimpleKey(key))
	return nil
}
