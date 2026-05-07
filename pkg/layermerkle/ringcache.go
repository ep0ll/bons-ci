package layermerkle

import (
	"context"
	"sync"
)

// ─────────────────────────────────────────────────────────────────────────────
// RingStatCache — fixed-size, lock-free-friendly stat result cache
// ─────────────────────────────────────────────────────────────────────────────

// statRingKey is the key stored in each ring slot.
type statRingKey struct {
	layerID LayerID
	relPath string
}

// statRingEntry is one slot in the ring buffer.
type statRingEntry struct {
	key    statRingKey
	result statResult
	valid  bool
}

// RingStatCache is a fixed-capacity, clock-eviction stat result cache.
// It replaces the unbounded map[statKey]statResult in OverlayResolver,
// bounding memory use while retaining high hit rates for typical access
// patterns (temporal locality within a single ExecOp).
//
// Clock eviction is simpler and faster than LRU for this use case because:
//   - Entries are small (two strings + one byte).
//   - The access pattern is nearly sequential per ExecOp.
//   - The cost of eviction is negligible (no list manipulation).
type RingStatCache struct {
	mu      sync.Mutex
	slots   []statRingEntry
	hand    int // clock hand position
	cap     int
	hits    int64
	misses  int64
	evicted int64
}

// NewRingStatCache creates a stat cache with the given capacity.
// Capacity must be ≥ 4; values below 4 are clamped to 4.
func NewRingStatCache(capacity int) *RingStatCache {
	if capacity < 4 {
		capacity = 4
	}
	return &RingStatCache{
		slots: make([]statRingEntry, capacity),
		cap:   capacity,
	}
}

// Get returns the cached stat result for (layerID, relPath).
// Returns (statUnknown, false) on a miss.
func (r *RingStatCache) Get(layerID LayerID, relPath string) (statResult, bool) {
	r.mu.Lock()
	defer r.mu.Unlock()

	k := statRingKey{layerID: layerID, relPath: relPath}
	for i := range r.slots {
		if r.slots[i].valid && r.slots[i].key == k {
			r.hits++
			return r.slots[i].result, true
		}
	}
	r.misses++
	return statUnknown, false
}

// Set stores a stat result, evicting the clock-hand slot if necessary.
func (r *RingStatCache) Set(layerID LayerID, relPath string, result statResult) {
	r.mu.Lock()
	defer r.mu.Unlock()

	k := statRingKey{layerID: layerID, relPath: relPath}

	// Check for existing entry to update in-place.
	for i := range r.slots {
		if r.slots[i].valid && r.slots[i].key == k {
			r.slots[i].result = result
			return
		}
	}

	// Evict at clock hand.
	if r.slots[r.hand].valid {
		r.evicted++
	}
	r.slots[r.hand] = statRingEntry{key: k, result: result, valid: true}
	r.hand = (r.hand + 1) % r.cap
}

// InvalidateLayer removes all entries for the given layer.
func (r *RingStatCache) InvalidateLayer(layerID LayerID) {
	r.mu.Lock()
	defer r.mu.Unlock()
	for i := range r.slots {
		if r.slots[i].valid && r.slots[i].key.layerID == layerID {
			r.slots[i].valid = false
		}
	}
}

// Stats returns (hits, misses, evictions).
func (r *RingStatCache) Stats() (int64, int64, int64) {
	r.mu.Lock()
	h, m, e := r.hits, r.misses, r.evicted
	r.mu.Unlock()
	return h, m, e
}

// Len returns the number of valid entries.
func (r *RingStatCache) Len() int {
	r.mu.Lock()
	n := 0
	for i := range r.slots {
		if r.slots[i].valid {
			n++
		}
	}
	r.mu.Unlock()
	return n
}

// ─────────────────────────────────────────────────────────────────────────────
// OverlayResolverWithRingCache — replaces unbounded map with RingStatCache
// ─────────────────────────────────────────────────────────────────────────────

// OverlayResolverWithRingCache is an OverlayResolver variant that uses a
// RingStatCache instead of the unbounded map, bounding memory growth in
// long-running watcher processes.
type OverlayResolverWithRingCache struct {
	registry  *LayerRegistry
	statCache *RingStatCache
}

// NewOverlayResolverWithRingCache creates a resolver with a bounded stat cache.
func NewOverlayResolverWithRingCache(registry *LayerRegistry, capacity int) *OverlayResolverWithRingCache {
	return &OverlayResolverWithRingCache{
		registry:  registry,
		statCache: NewRingStatCache(capacity),
	}
}

// FindOwnerLayer implements LayerFileResolver.
func (r *OverlayResolverWithRingCache) FindOwnerLayer(
	ctx context.Context,
	stack LayerStack,
	relPath string,
) (LayerID, error) {
	for i := len(stack) - 1; i >= 0; i-- {
		if ctx.Err() != nil {
			return "", ctx.Err()
		}
		layerID := stack[i]
		result, err := r.statInLayer(layerID, relPath)
		if err != nil {
			continue
		}
		switch result {
		case statPresent:
			return layerID, nil
		case statWhiteout:
			return "", ErrWhiteout
		}
	}
	return "", ErrLayerNotFound
}

// DiffAbsPath implements LayerFileResolver.
func (r *OverlayResolverWithRingCache) DiffAbsPath(_ context.Context, layerID LayerID, relPath string) (string, error) {
	info, ok := r.registry.Get(layerID)
	if !ok {
		return "", ErrLayerNotFound
	}
	return info.DiffFilePath(relPath), nil
}

func (r *OverlayResolverWithRingCache) statInLayer(layerID LayerID, relPath string) (statResult, error) {
	if cached, ok := r.statCache.Get(layerID, relPath); ok {
		return cached, nil
	}
	info, ok := r.registry.Get(layerID)
	if !ok {
		return statUnknown, ErrLayerNotFound
	}
	result := (&OverlayResolver{registry: r.registry}).fsStat(info, relPath)
	r.statCache.Set(layerID, relPath, result)
	return result, nil
}

// StatCacheStats returns (hits, misses, evictions) for the ring stat cache.
func (r *OverlayResolverWithRingCache) StatCacheStats() (int64, int64, int64) {
	return r.statCache.Stats()
}

// InvalidateLayer clears stat cache entries for layerID.
func (r *OverlayResolverWithRingCache) InvalidateLayer(layerID LayerID) {
	r.statCache.InvalidateLayer(layerID)
}
