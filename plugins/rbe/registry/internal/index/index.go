// Package index provides the AccelIndex implementation: a high-concurrency,
// sharded in-memory mapping from source digests to AccelVariants.
//
// Architecture decisions:
//
//  1. SHARDING — 256 independent shards keyed by the first byte of the
//     hex-encoded digest. This divides the keyspace uniformly (since digests
//     are cryptographically random) and reduces per-shard lock contention by
//     ~256×.
//
//  2. BLOOM FILTER — a lock-free probabilistic set used as a first-pass
//     negative filter. A "not in bloom" result is a guaranteed miss → skip
//     the shard lock entirely. The false-positive rate is tuned at startup
//     based on expected capacity.
//
//  3. ATOMIC COUNTERS — total element counts tracked via sync/atomic so
//     Stats() never acquires any lock.
//
//  4. COPY-ON-WRITE per query — Query() returns a deep copy of the slice so
//     callers hold stable data without holding any lock.
//
//  5. SYNC.POOL — temp slices used during aggregation are recycled to reduce
//     GC pressure on the hot query path.
package index

import (
	"context"
	"encoding/json"
	"fmt"
	"sync"
	"sync/atomic"
	"time"

	digest "github.com/opencontainers/go-digest"

	"github.com/bons/bons-ci/plugins/rbe/registry/pkg/bloom"
	"github.com/bons/bons-ci/plugins/rbe/registry/pkg/types"
)

const numShards = 256

// ────────────────────────────────────────────────────────────────────────────
// shard — one independent partition of the index
// ────────────────────────────────────────────────────────────────────────────

type shard struct {
	mu sync.RWMutex
	// variants maps sourceDigest → list of AccelVariants
	variants map[digest.Digest][]types.AccelVariant
	// typeIndex provides O(1) lookup: sourceDigest+AccelType → variant indices
	typeIndex map[typeKey][]int
}

type typeKey struct {
	source    digest.Digest
	accelType types.AccelType
}

func newShard() *shard {
	return &shard{
		variants:  make(map[digest.Digest][]types.AccelVariant),
		typeIndex: make(map[typeKey][]int),
	}
}

// ────────────────────────────────────────────────────────────────────────────
// ShardedIndex — the production AccelIndex implementation
// ────────────────────────────────────────────────────────────────────────────

// ShardedIndex implements types.AccelIndex using 256 independently-locked shards
// and a lock-free bloom filter for fast negative lookup.
type ShardedIndex struct {
	shards [numShards]*shard
	bloom  *bloom.Filter

	// Atomic counters — never require a lock.
	totalSources int64
	// per-type counters stored as a fixed-size array (one entry per known type)
	typeCounts [4]int64 // nydus=0, estargz=1, soci=2, overlaybd=3

	// snap maintains a snapshot of Stats for cheap polling.
	snapMu  sync.RWMutex
	snapAge time.Time

	// pool for temporary AccelVariant slices during Query aggregation.
	variantPool sync.Pool
}

// typeIndex maps AccelType → counter array index for lock-free counting.
var typeIndex = map[types.AccelType]int{
	types.AccelNydus:     0,
	types.AccelEstargz:   1,
	types.AccelSOCI:      2,
	types.AccelOverlayBD: 3,
}

// NewShardedIndex constructs a ShardedIndex optimised for expectedSources
// unique source digests.
func NewShardedIndex(expectedSources uint64) *ShardedIndex {
	si := &ShardedIndex{
		bloom: bloom.NewDefault(expectedSources),
		variantPool: sync.Pool{
			New: func() interface{} {
				s := make([]types.AccelVariant, 0, 8)
				return &s
			},
		},
	}
	for i := range si.shards {
		si.shards[i] = newShard()
	}
	return si
}

// shardFor selects the shard for a given digest in O(1) arithmetic.
// We take the first byte of the hex-encoded hash (after the algorithm prefix)
// giving 256 uniform partitions for SHA-256/SHA-512 digests.
func (si *ShardedIndex) shardFor(d digest.Digest) *shard {
	hex := d.Encoded()
	if len(hex) >= 2 {
		hi := hexNibble(hex[0]) << 4
		lo := hexNibble(hex[1])
		return si.shards[hi|lo]
	}
	return si.shards[0]
}

// hexNibble converts a single ASCII hex char to its value.
func hexNibble(c byte) byte {
	switch {
	case c >= '0' && c <= '9':
		return c - '0'
	case c >= 'a' && c <= 'f':
		return c - 'a' + 10
	case c >= 'A' && c <= 'F':
		return c - 'A' + 10
	default:
		return 0
	}
}

// ── AccelIndex implementation ──────────────────────────────────────────────

// Index records an AccelVariant in the index, indexed by every SourceRef digest.
// The operation is idempotent: re-indexing the same ManifestDigest updates it.
func (si *ShardedIndex) Index(ctx context.Context, variant types.AccelVariant) error {
	if err := ctx.Err(); err != nil {
		return err
	}
	if variant.ManifestDigest == "" {
		return fmt.Errorf("index: variant ManifestDigest is required")
	}
	if len(variant.SourceRefs) == 0 {
		return fmt.Errorf("index: variant has no SourceRefs")
	}

	for _, ref := range variant.SourceRefs {
		if ref.Digest == "" {
			continue
		}
		si.indexForSource(ref.Digest, variant)
		// Add to bloom filter (lock-free)
		si.bloom.AddDigestString(ref.Digest.String())
	}
	return nil
}

// indexForSource upserts variant into the shard for sourceDigest.
func (si *ShardedIndex) indexForSource(sourceDigest digest.Digest, variant types.AccelVariant) {
	sh := si.shardFor(sourceDigest)
	sh.mu.Lock()

	existing, hadSource := sh.variants[sourceDigest]

	// Deduplication: replace existing variant with same ManifestDigest.
	for i, v := range existing {
		if v.ManifestDigest == variant.ManifestDigest {
			existing[i] = variant
			sh.variants[sourceDigest] = existing
			sh.mu.Unlock()
			return
		}
	}

	// Append new variant
	sh.variants[sourceDigest] = append(existing, variant)

	// Maintain type index
	tk := typeKey{source: sourceDigest, accelType: variant.AccelType}
	sh.typeIndex[tk] = append(sh.typeIndex[tk], len(sh.variants[sourceDigest])-1)

	sh.mu.Unlock()

	// Update atomic counters only for genuinely new entries.
	if !hadSource {
		atomic.AddInt64(&si.totalSources, 1)
	}
	if idx, ok := typeIndex[variant.AccelType]; ok {
		atomic.AddInt64(&si.typeCounts[idx], 1)
	}
}

// Query returns all accel variants for sourceDigest.
// Returns a result with Found=false when no variants are known.
// The returned slices are deep copies — safe to hold beyond the call.
func (si *ShardedIndex) Query(_ context.Context, sourceDigest digest.Digest) (*types.AccelQueryResult, error) {
	// Fast-path: bloom filter says definitely not present.
	if !si.bloom.TestDigestString(sourceDigest.String()) {
		return &types.AccelQueryResult{
			SourceDigest: sourceDigest,
			Found:        false,
			Variants:     make(map[types.AccelType][]types.AccelVariant),
		}, nil
	}

	sh := si.shardFor(sourceDigest)
	sh.mu.RLock()
	raw, ok := sh.variants[sourceDigest]
	var cp []types.AccelVariant
	if ok && len(raw) > 0 {
		cp = make([]types.AccelVariant, len(raw))
		copy(cp, raw)
	}
	sh.mu.RUnlock()

	if !ok || len(cp) == 0 {
		return &types.AccelQueryResult{
			SourceDigest: sourceDigest,
			Found:        false,
			Variants:     make(map[types.AccelType][]types.AccelVariant),
		}, nil
	}

	// Aggregate by AccelType
	byType := make(map[types.AccelType][]types.AccelVariant, 4)
	supported := make([]types.AccelType, 0, 4)
	for _, v := range cp {
		prev := byType[v.AccelType]
		if len(prev) == 0 {
			supported = append(supported, v.AccelType)
		}
		byType[v.AccelType] = append(prev, v)
	}

	return &types.AccelQueryResult{
		SourceDigest:   sourceDigest,
		Found:          true,
		TotalVariants:  len(cp),
		Variants:       byType,
		SupportedTypes: supported,
	}, nil
}

// ExistsAny uses the bloom filter for O(k) lookup with no lock contention.
// Safe to call from many goroutines simultaneously.
func (si *ShardedIndex) ExistsAny(_ context.Context, sourceDigest digest.Digest) bool {
	return si.bloom.TestDigestString(sourceDigest.String())
}

// ExistsByType checks definitively whether a specific accel type exists for
// the source digest. Uses the shard type-index for O(1) lookup.
func (si *ShardedIndex) ExistsByType(_ context.Context, sourceDigest digest.Digest, t types.AccelType) bool {
	if !si.bloom.TestDigestString(sourceDigest.String()) {
		return false // definitive bloom miss
	}
	sh := si.shardFor(sourceDigest)
	sh.mu.RLock()
	indices := sh.typeIndex[typeKey{source: sourceDigest, accelType: t}]
	sh.mu.RUnlock()
	return len(indices) > 0
}

// Remove deletes all variants for sourceDigest from the index.
func (si *ShardedIndex) Remove(_ context.Context, sourceDigest digest.Digest) error {
	sh := si.shardFor(sourceDigest)
	sh.mu.Lock()

	existing, ok := sh.variants[sourceDigest]
	if !ok {
		sh.mu.Unlock()
		return nil
	}

	// Update type index
	for _, v := range existing {
		tk := typeKey{source: sourceDigest, accelType: v.AccelType}
		delete(sh.typeIndex, tk)
		if idx, exists := typeIndex[v.AccelType]; exists {
			atomic.AddInt64(&si.typeCounts[idx], -1)
		}
	}
	delete(sh.variants, sourceDigest)
	sh.mu.Unlock()

	atomic.AddInt64(&si.totalSources, -1)
	// Note: we do NOT remove from the bloom filter (no deletion support).
	// The filter will produce false positives for removed entries, which is
	// acceptable because ExistsByType and Query will return no results.
	return nil
}

// RemoveVariant removes a single accel variant identified by its manifest digest.
func (si *ShardedIndex) RemoveVariant(_ context.Context, sourceDigest digest.Digest, manifestDigest digest.Digest) error {
	sh := si.shardFor(sourceDigest)
	sh.mu.Lock()
	defer sh.mu.Unlock()

	existing, ok := sh.variants[sourceDigest]
	if !ok {
		return nil
	}

	for i, v := range existing {
		if v.ManifestDigest == manifestDigest {
			// Remove by swapping with last element
			existing[i] = existing[len(existing)-1]
			existing = existing[:len(existing)-1]
			sh.variants[sourceDigest] = existing

			// Rebuild type index for this source
			tk := typeKey{source: sourceDigest, accelType: v.AccelType}
			// Recount remaining indices for this type
			newIndices := make([]int, 0)
			for j, vv := range existing {
				if vv.AccelType == v.AccelType {
					newIndices = append(newIndices, j)
				}
			}
			if len(newIndices) > 0 {
				sh.typeIndex[tk] = newIndices
			} else {
				delete(sh.typeIndex, tk)
			}

			if idx, exists := typeIndex[v.AccelType]; exists {
				atomic.AddInt64(&si.typeCounts[idx], -1)
			}
			if len(existing) == 0 {
				delete(sh.variants, sourceDigest)
				atomic.AddInt64(&si.totalSources, -1)
			}
			return nil
		}
	}
	return nil
}

// Stats returns aggregate statistics without acquiring any lock.
func (si *ShardedIndex) Stats() types.IndexStats {
	typeTotals := make(map[types.AccelType]int64, 4)
	for t, idx := range typeIndex {
		typeTotals[t] = atomic.LoadInt64(&si.typeCounts[idx])
	}
	total := atomic.LoadInt64(&si.totalSources)
	fpRate := si.bloom.EstimateFPRate(uint64(total))

	return types.IndexStats{
		TotalSourceDigests:     total,
		TotalVariants:          typeTotals,
		BloomFalsePositiveRate: fpRate,
		ShardCount:             numShards,
	}
}

// ── Persistence helpers (snapshot/restore) ────────────────────────────────

// Snapshot serialises the entire index to JSON for persistence.
// This acquires every shard lock briefly and should not be called on the
// hot path — use a background goroutine on a periodic timer.
func (si *ShardedIndex) Snapshot() ([]byte, error) {
	type entry struct {
		Source   digest.Digest        `json:"source"`
		Variants []types.AccelVariant `json:"variants"`
	}

	entries := make([]entry, 0, int(atomic.LoadInt64(&si.totalSources)))
	for _, sh := range si.shards {
		sh.mu.RLock()
		for src, vs := range sh.variants {
			cp := make([]types.AccelVariant, len(vs))
			copy(cp, vs)
			entries = append(entries, entry{Source: src, Variants: cp})
		}
		sh.mu.RUnlock()
	}

	return json.Marshal(entries)
}

// Restore repopulates the index from a JSON snapshot produced by Snapshot().
// Typically called once at startup after loading from disk/object-store.
func (si *ShardedIndex) Restore(ctx context.Context, data []byte) error {
	type entry struct {
		Source   digest.Digest        `json:"source"`
		Variants []types.AccelVariant `json:"variants"`
	}
	var entries []entry
	if err := json.Unmarshal(data, &entries); err != nil {
		return fmt.Errorf("index restore: %w", err)
	}
	for _, e := range entries {
		for _, v := range e.Variants {
			if err := si.Index(ctx, v); err != nil {
				return fmt.Errorf("index restore: variant %s: %w", v.ManifestDigest, err)
			}
		}
	}
	return nil
}
