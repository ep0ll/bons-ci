package registry

import (
	"hash/fnv"
	"sync"
	"time"

	"github.com/containerd/containerd/v2/core/content"
	v1 "github.com/opencontainers/image-spec/specs-go/v1"
)

// ---------------------------------------------------------------------------
// Sharded ingestion tracker – 64 shards, false-sharing padded
// ---------------------------------------------------------------------------

const (
	numIngShards = 64 // power-of-2
	ingShardMask = numIngShards - 1
)

// activeIngestion tracks a single in-flight write operation.
type activeIngestion struct {
	ref       string
	desc      v1.Descriptor
	writer    content.Writer
	startedAt time.Time
	updatedAt time.Time
}

type ingestionShard struct {
	mu      sync.RWMutex
	entries map[string]*activeIngestion
	_       [16]byte // cache-line padding
}

// ingestionTracker routes ingestion map operations across 64 independent
// shards keyed by FNV-1a hash of the ref string. This eliminates the single
// global RWMutex bottleneck that serialises all concurrent Writer calls.
type ingestionTracker struct {
	shards [numIngShards]ingestionShard
}

func newIngestionTracker() *ingestionTracker {
	t := &ingestionTracker{}
	for i := range t.shards {
		t.shards[i].entries = make(map[string]*activeIngestion, 4)
	}
	return t
}

func (t *ingestionTracker) shard(ref string) *ingestionShard {
	h := fnv.New32a()
	_, _ = h.Write([]byte(ref))
	return &t.shards[h.Sum32()&ingShardMask]
}

// Add registers ing under ref. Returns false if ref is already tracked.
func (t *ingestionTracker) Add(ref string, ing *activeIngestion) bool {
	s := t.shard(ref)
	s.mu.Lock()
	_, exists := s.entries[ref]
	if !exists {
		s.entries[ref] = ing
	}
	s.mu.Unlock()
	return !exists
}

// Get returns the ingestion for ref and whether it was found.
func (t *ingestionTracker) Get(ref string) (*activeIngestion, bool) {
	s := t.shard(ref)
	s.mu.RLock()
	ing, ok := s.entries[ref]
	s.mu.RUnlock()
	return ing, ok
}

// Remove deletes and returns the ingestion for ref (nil if absent).
func (t *ingestionTracker) Remove(ref string) *activeIngestion {
	s := t.shard(ref)
	s.mu.Lock()
	ing := s.entries[ref]
	delete(s.entries, ref)
	s.mu.Unlock()
	return ing
}

// Touch updates the updatedAt timestamp for an active ingestion.
func (t *ingestionTracker) Touch(ref string) {
	s := t.shard(ref)
	now := time.Now()
	s.mu.Lock()
	if ing, ok := s.entries[ref]; ok {
		ing.updatedAt = now
	}
	s.mu.Unlock()
}

// All returns a point-in-time snapshot of all active ingestions.
func (t *ingestionTracker) All() []*activeIngestion {
	var all []*activeIngestion
	for i := range t.shards {
		s := &t.shards[i]
		s.mu.RLock()
		for _, ing := range s.entries {
			all = append(all, ing)
		}
		s.mu.RUnlock()
	}
	return all
}

// RemoveAll atomically drains all shards and returns the removed ingestions.
func (t *ingestionTracker) RemoveAll() []*activeIngestion {
	var all []*activeIngestion
	for i := range t.shards {
		s := &t.shards[i]
		s.mu.Lock()
		for _, ing := range s.entries {
			all = append(all, ing)
		}
		s.entries = make(map[string]*activeIngestion, 4)
		s.mu.Unlock()
	}
	return all
}
