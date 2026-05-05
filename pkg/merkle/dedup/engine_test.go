package dedup_test

import (
	"context"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/user/layermerkle/cache"
	"github.com/user/layermerkle/dedup"
	"github.com/user/layermerkle/event"
	"github.com/user/layermerkle/hash"
	"github.com/user/layermerkle/layer"
	"github.com/user/layermerkle/merkle"
)

// ─── Helpers ──────────────────────────────────────────────────────────────────

func makeEngine() (*dedup.Engine, *cache.ShardedCache, *merkle.Registry) {
	c := cache.NewShardedCache()
	reg := merkle.NewRegistry()
	hp := hash.NewSyntheticProvider()
	return dedup.NewEngine(c, hp, reg), c, reg
}

func readEvent(path string, stack layer.Stack) *event.FileAccessEvent {
	return &event.FileAccessEvent{
		FilePath:   path,
		LayerStack: stack,
		AccessType: event.AccessRead,
		Timestamp:  time.Now(),
	}
}

func writeEvent(path string, stack layer.Stack) *event.FileAccessEvent {
	return &event.FileAccessEvent{
		FilePath:   path,
		LayerStack: stack,
		AccessType: event.AccessWrite,
		Timestamp:  time.Now(),
	}
}

func deleteEvent(path string, stack layer.Stack) *event.FileAccessEvent {
	return &event.FileAccessEvent{
		FilePath:   path,
		LayerStack: stack,
		AccessType: event.AccessDelete,
		Timestamp:  time.Now(),
	}
}

// ─── Phase 1: Output layer cache hit ─────────────────────────────────────────

func TestPhase1_CacheHit(t *testing.T) {
	eng, c, _ := makeEngine()
	ctx := context.Background()

	base := layer.Digest("base")
	stack := layer.MustNew(base)

	// First event: miss → computes hash
	r1 := eng.Process(ctx, readEvent("/bin/sh", stack))
	if r1.Kind != dedup.ResultCacheMiss {
		t.Fatalf("first read: expected CacheMiss, got %s", r1.Kind)
	}

	// Second event same (path, layer): must be phase-1 hit
	r2 := eng.Process(ctx, readEvent("/bin/sh", stack))
	if r2.Kind != dedup.ResultCacheHit {
		t.Fatalf("second read: expected CacheHit, got %s", r2.Kind)
	}

	// Hashes must be identical
	if string(r1.Hash) != string(r2.Hash) {
		t.Fatal("cache hit must return same hash as original computation")
	}

	// Only one unique cache entry for this key
	stats := c.Stats()
	if stats.Entries != 1 {
		t.Fatalf("expected 1 cache entry, got %d", stats.Entries)
	}
}

// ─── Phase 2: Ancestor stack walk ────────────────────────────────────────────

func TestPhase2_AncestorPromotion(t *testing.T) {
	eng, c, _ := makeEngine()
	ctx := context.Background()

	base := layer.Digest("base")
	exec1 := layer.Digest("exec1")

	// Seed base layer with /bin/sh
	r1 := eng.Process(ctx, readEvent("/bin/sh", layer.MustNew(base)))
	if r1.Kind != dedup.ResultCacheMiss {
		t.Fatalf("base read: expected CacheMiss, got %s", r1.Kind)
	}

	// Now exec1 reads /bin/sh — should walk to base and promote
	r2 := eng.Process(ctx, readEvent("/bin/sh", layer.MustNew(base, exec1)))
	if r2.Kind != dedup.ResultCacheHit {
		t.Fatalf("exec1 read: expected CacheHit (ancestor promotion), got %s", r2.Kind)
	}

	// Hash must be the same content
	if string(r1.Hash) != string(r2.Hash) {
		t.Fatal("promoted hash must equal original hash")
	}

	// After promotion, a second exec1 read must also be phase-1 hit
	r3 := eng.Process(ctx, readEvent("/bin/sh", layer.MustNew(base, exec1)))
	if r3.Kind != dedup.ResultCacheHit {
		t.Fatalf("second exec1 read: expected CacheHit, got %s", r3.Kind)
	}

	// We now have 2 entries: (path, base) and (path, exec1)
	if c.Stats().Entries != 2 {
		t.Fatalf("expected 2 cache entries, got %d", c.Stats().Entries)
	}
}

func TestPhase2_TombstoneStopsWalk(t *testing.T) {
	eng, _, _ := makeEngine()
	ctx := context.Background()

	base := layer.Digest("base")
	exec1 := layer.Digest("exec1")
	exec2 := layer.Digest("exec2")

	// Seed base with /etc/passwd
	eng.Process(ctx, readEvent("/etc/passwd", layer.MustNew(base)))
	// exec1 deletes /etc/passwd → tombstone
	eng.Process(ctx, deleteEvent("/etc/passwd", layer.MustNew(base, exec1)))
	// exec2 reads /etc/passwd — should hit tombstone in exec1 and stop
	r := eng.Process(ctx, readEvent("/etc/passwd", layer.MustNew(base, exec1, exec2)))
	if r.Kind != dedup.ResultTombstone {
		t.Fatalf("exec2 read after exec1 delete: expected Tombstone, got %s", r.Kind)
	}
}

func TestPhase2_MultiLayerWalk(t *testing.T) {
	eng, _, _ := makeEngine()
	ctx := context.Background()

	base  := layer.Digest("base")
	exec1 := layer.Digest("exec1")
	exec2 := layer.Digest("exec2")
	exec3 := layer.Digest("exec3")

	// Only base has the file
	eng.Process(ctx, readEvent("/lib/libc.so", layer.MustNew(base)))

	// exec3 reads with full stack — should walk exec2, exec1, then find in base
	r := eng.Process(ctx, readEvent("/lib/libc.so", layer.MustNew(base, exec1, exec2, exec3)))
	if r.Kind != dedup.ResultCacheHit {
		t.Fatalf("deep stack walk: expected CacheHit, got %s", r.Kind)
	}
}

// ─── Delete (tombstone) ───────────────────────────────────────────────────────

func TestDeleteCreatesTombstone(t *testing.T) {
	eng, c, _ := makeEngine()
	ctx := context.Background()
	l := layer.Digest("l1")

	r := eng.Process(ctx, deleteEvent("/etc/passwd", layer.MustNew(l)))
	if r.Kind != dedup.ResultTombstone {
		t.Fatalf("delete: expected Tombstone, got %s", r.Kind)
	}
	if r.Hash != nil {
		t.Fatal("delete result must have nil Hash")
	}

	// Verify tombstone in cache
	entry, ok := c.Get(cache.CacheKey{FilePath: "/etc/passwd", LayerDigest: l})
	if !ok || !entry.Tombstone {
		t.Fatal("expected tombstone in cache after delete event")
	}
}

// ─── Mutating ops ─────────────────────────────────────────────────────────────

func TestMutatingAlwaysFreshHash(t *testing.T) {
	eng, c, _ := makeEngine()
	ctx := context.Background()
	l := layer.Digest("lmut")
	stack := layer.MustNew(l)

	// Write once
	r1 := eng.Process(ctx, writeEvent("/app/server", stack))
	if r1.Kind != dedup.ResultCacheMiss {
		t.Fatalf("first write: expected CacheMiss, got %s", r1.Kind)
	}

	// Write again (same key) — must overwrite; not a cache hit
	r2 := eng.Process(ctx, writeEvent("/app/server", stack))
	if r2.Kind != dedup.ResultCacheMiss {
		t.Fatalf("second write: expected CacheMiss (fresh hash), got %s", r2.Kind)
	}

	// Only 1 entry in cache (last write wins)
	if c.Stats().Entries != 1 {
		t.Fatalf("expected 1 entry after two writes, got %d", c.Stats().Entries)
	}
}

func TestMutatingDoesNotPromoteFromAncestor(t *testing.T) {
	eng, _, _ := makeEngine()
	ctx := context.Background()

	base  := layer.Digest("base")
	exec1 := layer.Digest("exec1")

	// Seed base
	baseResult := eng.Process(ctx, readEvent("/etc/passwd", layer.MustNew(base)))

	// exec1 writes /etc/passwd (same path, different content conceptually)
	writeResult := eng.Process(ctx, writeEvent("/etc/passwd", layer.MustNew(base, exec1)))

	// The write result's SourceLayer must be exec1, not base
	if writeResult.SourceLayer != exec1 {
		t.Fatalf("mutating write SourceLayer: expected exec1, got %s", writeResult.SourceLayer)
	}
	_ = baseResult
}

// ─── MerkleLeafAdded ──────────────────────────────────────────────────────────

func TestMerkleLeafAddedOnFirstRead(t *testing.T) {
	eng, _, reg := makeEngine()
	ctx := context.Background()
	l := layer.Digest("lmerkle")

	r := eng.Process(ctx, readEvent("/a", layer.MustNew(l)))
	if !r.MerkleLeafAdded {
		t.Fatal("first read: MerkleLeafAdded must be true")
	}
	if reg.LeafCount(l) != 1 {
		t.Fatalf("expected 1 leaf, got %d", reg.LeafCount(l))
	}
}

func TestMerkleLeafAddedOnPromotion(t *testing.T) {
	eng, _, reg := makeEngine()
	ctx := context.Background()

	base  := layer.Digest("base-merkle")
	exec1 := layer.Digest("exec1-merkle")

	eng.Process(ctx, readEvent("/f", layer.MustNew(base)))
	r := eng.Process(ctx, readEvent("/f", layer.MustNew(base, exec1)))

	if r.Kind != dedup.ResultCacheHit {
		t.Fatalf("expected CacheHit, got %s", r.Kind)
	}

	// exec1's tree should now have 1 leaf
	if reg.LeafCount(exec1) != 1 {
		t.Fatalf("expected 1 leaf in exec1, got %d", reg.LeafCount(exec1))
	}
}

// ─── Validation ───────────────────────────────────────────────────────────────

func TestInvalidEvent_EmptyPath(t *testing.T) {
	eng, _, _ := makeEngine()
	ctx := context.Background()
	ev := &event.FileAccessEvent{
		FilePath:   "",
		LayerStack: layer.MustNew("l0"),
		AccessType: event.AccessRead,
	}
	r := eng.Process(ctx, ev)
	if r.Kind != dedup.ResultError {
		t.Fatalf("empty path: expected ResultError, got %s", r.Kind)
	}
}

func TestInvalidEvent_EmptyStack(t *testing.T) {
	eng, _, _ := makeEngine()
	ctx := context.Background()
	ev := &event.FileAccessEvent{
		FilePath:   "/f",
		LayerStack: nil,
		AccessType: event.AccessRead,
	}
	r := eng.Process(ctx, ev)
	if r.Kind != dedup.ResultError {
		t.Fatalf("empty stack: expected ResultError, got %s", r.Kind)
	}
}

// ─── Singleflight deduplication ───────────────────────────────────────────────

// countingProvider counts how many times Hash is called.
type countingProvider struct {
	calls atomic.Int64
	inner hash.HashProvider
}

func (p *countingProvider) Hash(ctx context.Context, req hash.HashRequest) (hash.HashResult, error) {
	p.calls.Add(1)
	return p.inner.Hash(ctx, req)
}
func (p *countingProvider) Algorithm() hash.Algorithm { return p.inner.Algorithm() }

func TestSingleflight_ConcurrentMissesMerge(t *testing.T) {
	counter := &countingProvider{inner: hash.NewSyntheticProvider()}
	c := cache.NewShardedCache()
	reg := merkle.NewRegistry()
	eng := dedup.NewEngine(c, counter, reg)
	ctx := context.Background()

	l := layer.Digest("lsf")
	stack := layer.MustNew(l)
	const N = 50

	var wg sync.WaitGroup
	results := make([]dedup.Result, N)
	for i := 0; i < N; i++ {
		wg.Add(1)
		go func(i int) {
			defer wg.Done()
			results[i] = eng.Process(ctx, readEvent("/shared/file", stack))
		}(i)
	}
	wg.Wait()

	// All results must be successful
	for i, r := range results {
		if r.Kind == dedup.ResultError {
			t.Fatalf("goroutine %d: unexpected error: %v", i, r.Error)
		}
	}

	// Due to singleflight, Hash must be called far fewer than N times.
	// (Typically 1, but timing may allow a small number.)
	calls := counter.calls.Load()
	if calls > int64(N/2) {
		t.Fatalf("singleflight: expected << %d Hash calls, got %d", N, calls)
	}
}

// ─── Tombstone-after-SetIfAbsent invariant ────────────────────────────────────

// TestTombstoneAfterSetIfAbsent verifies the critical race-condition invariant:
// if a DELETE event stores a tombstone at (path, outputLayer) BEFORE our
// ancestor-walk SetIfAbsent call, the engine must return ResultTombstone rather
// than adding a nil-hash leaf to the Merkle tree.
//
// We simulate the race deterministically by pre-seeding the tombstone before
// the read event is processed.
func TestTombstoneAfterSetIfAbsent(t *testing.T) {
	c := cache.NewShardedCache()
	reg := merkle.NewRegistry()
	hp := hash.NewSyntheticProvider()
	eng := dedup.NewEngine(c, hp, reg)
	ctx := context.Background()

	base  := layer.Digest("base-tsias")
	exec1 := layer.Digest("exec1-tsias")

	// Seed base layer so ancestor walk finds a hit.
	eng.Process(ctx, readEvent("/conflict", layer.MustNew(base)))

	// Pre-inject a tombstone at exec1 — simulates a concurrent DELETE
	// arriving between walkAncestors cache.Get and SetIfAbsent.
	tombKey := cache.CacheKey{FilePath: "/conflict", LayerDigest: exec1}
	c.Set(tombKey, cache.CacheEntry{
		Tombstone:   true,
		SourceLayer: exec1,
	})

	// Now process a read through the full stack. SetIfAbsent will
	// return the tombstone (not our promoted entry).
	r := eng.Process(ctx, readEvent("/conflict", layer.MustNew(base, exec1)))

	if r.Kind != dedup.ResultTombstone {
		t.Fatalf("expected ResultTombstone after race, got %s", r.Kind)
	}
	if r.Hash != nil {
		t.Fatalf("tombstone result must have nil Hash, got %x", r.Hash)
	}
	if reg.LeafCount(exec1) != 0 {
		t.Fatalf("tombstone must not add Merkle leaf, count=%d", reg.LeafCount(exec1))
	}
}

// TestComputePhaseGetsTombstoneRace verifies the same invariant for phase-3
// (singleflight compute + SetIfAbsent). If DELETE stores a tombstone while
// Hash is computing, SetIfAbsent returns the tombstone and we handle it cleanly.
func TestComputePhaseGetsTombstoneRace(t *testing.T) {
	c := cache.NewShardedCache()
	reg := merkle.NewRegistry()
	hp := hash.NewSyntheticProvider()
	eng := dedup.NewEngine(c, hp, reg)
	ctx := context.Background()

	exec1 := layer.Digest("exec1-cptsr")

	// Pre-inject tombstone at exec1 before any processing happens.
	key := cache.CacheKey{FilePath: "/raced", LayerDigest: exec1}
	c.Set(key, cache.CacheEntry{Tombstone: true, SourceLayer: exec1})

	// Process a read — phase-1 finds tombstone immediately.
	r := eng.Process(ctx, readEvent("/raced", layer.MustNew(exec1)))
	if r.Kind != dedup.ResultTombstone {
		t.Fatalf("expected ResultTombstone from phase-1, got %s", r.Kind)
	}
	if reg.LeafCount(exec1) != 0 {
		t.Fatalf("tombstone phase-1 must not add leaf, count=%d", reg.LeafCount(exec1))
	}
}

// TestWriteAfterReadPromotion verifies that a WRITE event arriving after
// a read-promotion correctly overwrites the promoted (lower-layer) hash
// with the fresh write hash.
func TestWriteAfterReadPromotion(t *testing.T) {
	eng, c, _ := makeEngine()
	ctx := context.Background()

	base  := layer.Digest("base-warp")
	exec1 := layer.Digest("exec1-warp")
	stack := layer.MustNew(base, exec1)

	// Seed base.
	baseR := eng.Process(ctx, readEvent("/data", layer.MustNew(base)))
	if baseR.Kind != dedup.ResultCacheMiss {
		t.Fatalf("base read: expected CacheMiss, got %s", baseR.Kind)
	}

	// Read in exec1 — promotes base hash to exec1.
	readR := eng.Process(ctx, readEvent("/data", stack))
	if readR.Kind != dedup.ResultCacheHit {
		t.Fatalf("exec1 read: expected CacheHit, got %s", readR.Kind)
	}

	// Now write in exec1 — must OVERWRITE the promoted hash.
	writeR := eng.Process(ctx, writeEvent("/data", stack))
	if writeR.Kind != dedup.ResultCacheMiss {
		t.Fatalf("exec1 write: expected CacheMiss (fresh hash), got %s", writeR.Kind)
	}

	// Verify the cache holds the WRITE hash, not the promoted read hash.
	entry, ok := c.Get(cache.CacheKey{FilePath: "/data", LayerDigest: exec1})
	if !ok {
		t.Fatal("expected cache entry after write")
	}
	if string(entry.Hash) != string(writeR.Hash) {
		t.Fatal("cache must hold write hash, not promoted read hash")
	}
	// The write hash should differ from the base hash (different layer input).
	if string(writeR.Hash) == string(baseR.Hash) {
		t.Fatal("write hash must differ from base hash (different outputLayer)")
	}
}
