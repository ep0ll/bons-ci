// Package cache provides the ShardedCache — a concurrent, sharded hash cache
// keyed by (filePath, layerDigest) pairs. It is the central data structure
// that enables deduplication of hash computations across stacked layers.
//
// # Design
//
// The cache is sharded into numShards buckets selected by FNV-32a of the
// cache key. Each shard owns an independent sync.RWMutex so readers on
// different shards never block each other.
//
// Tombstone entries represent files that were deleted in a given layer. The
// dedup engine treats tombstones as stack-walk terminators: if a file has a
// tombstone in layer L, it was deleted there and must not be looked up in
// lower layers.
//
// # Thread Safety
//
// All exported methods are safe for concurrent use.
package cache

import (
	"fmt"
	"time"

	"github.com/user/layermerkle/layer"
)

// ─────────────────────────────────────────────────────────────────────────────
// CacheKey
// ─────────────────────────────────────────────────────────────────────────────

// CacheKey uniquely identifies a (filePath, layerDigest) pair in the cache.
// The pair is the minimal unit of deduplication: a file may have different
// content in different layers, so the layer must be part of the key.
type CacheKey struct {
	// FilePath is the absolute path within the merged filesystem view.
	// Must be non-empty. Normalised to forward-slash form.
	FilePath string

	// LayerDigest is the layer this file access is attributed to.
	LayerDigest layer.Digest
}

// String returns a compact, human-readable key representation.
// Format: "<layerDigest>|<filePath>".
func (k CacheKey) String() string {
	return fmt.Sprintf("%s|%s", k.LayerDigest, k.FilePath)
}

// ─────────────────────────────────────────────────────────────────────────────
// CacheEntry
// ─────────────────────────────────────────────────────────────────────────────

// CacheEntry is the value stored for a CacheKey.
//
// A Tombstone entry (Tombstone == true) signals that the file was deleted
// in the associated layer. Hash and Algorithm are empty for tombstones.
type CacheEntry struct {
	// Hash is the raw digest bytes of the file content. Nil for tombstones.
	Hash []byte

	// Algorithm identifies the hash function (e.g., "sha256").
	Algorithm string

	// SourceLayer is the layer the file content was physically read from.
	// For files written/created in the keyed layer, SourceLayer == key.LayerDigest.
	// For files inherited from a lower layer, SourceLayer is that lower layer.
	SourceLayer layer.Digest

	// Tombstone is true when the file was deleted in this layer.
	// If Tombstone is true, Hash and Algorithm are always empty.
	Tombstone bool

	// CachedAt records when this entry was inserted. Used for TTL and metrics.
	CachedAt time.Time

	// HitCount tracks how many times this entry has been reused (cache hits).
	// Updated atomically by the cache — do not read/write externally.
	HitCount uint64
}

// IsValid reports whether the entry holds a usable (non-tombstone) hash.
func (e CacheEntry) IsValid() bool {
	return !e.Tombstone && len(e.Hash) > 0
}

// String returns a compact display string.
func (e CacheEntry) String() string {
	if e.Tombstone {
		return "tombstone"
	}
	if len(e.Hash) == 0 {
		return "<empty>"
	}
	const maxHexLen = 16
	hex := fmt.Sprintf("%x", e.Hash)
	if len(hex) > maxHexLen {
		hex = hex[:maxHexLen] + "…"
	}
	return fmt.Sprintf("%s:%s (src=%s)", e.Algorithm, hex, e.SourceLayer)
}

// ─────────────────────────────────────────────────────────────────────────────
// Stats
// ─────────────────────────────────────────────────────────────────────────────

// Stats holds runtime statistics for the cache.
type Stats struct {
	// Entries is the total number of live (non-evicted) entries.
	Entries int64
	// Tombstones is the number of tombstone entries.
	Tombstones int64
	// Hits is the cumulative cache hit count since creation.
	Hits int64
	// Misses is the cumulative cache miss count since creation.
	Misses int64
	// Shards is the number of shards.
	Shards int
}

// HitRate returns the cache hit rate as a fraction in [0, 1].
// Returns 0 if no lookups have been performed.
func (s Stats) HitRate() float64 {
	total := s.Hits + s.Misses
	if total == 0 {
		return 0
	}
	return float64(s.Hits) / float64(total)
}

// String returns a human-readable statistics summary.
func (s Stats) String() string {
	return fmt.Sprintf(
		"entries=%d tombstones=%d hits=%d misses=%d hit_rate=%.2f%% shards=%d",
		s.Entries, s.Tombstones, s.Hits, s.Misses, s.HitRate()*100, s.Shards,
	)
}
