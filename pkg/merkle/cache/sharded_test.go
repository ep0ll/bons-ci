package cache_test

import (
	"fmt"
	"sync"
	"testing"
	"time"

	"github.com/user/layermerkle/cache"
	"github.com/user/layermerkle/layer"
)

// helpers

func makeKey(path string, d layer.Digest) cache.CacheKey {
	return cache.CacheKey{FilePath: path, LayerDigest: d}
}

func makeEntry(hash []byte) cache.CacheEntry {
	return cache.CacheEntry{Hash: hash, Algorithm: "sha256", SourceLayer: "l1", CachedAt: time.Now()}
}

// ─── Get / Set ───────────────────────────────────────────────────────────────

func TestGetMiss(t *testing.T) {
	c := cache.NewShardedCache()
	_, ok := c.Get(makeKey("/bin/sh", "l1"))
	if ok {
		t.Fatal("expected miss for empty cache")
	}
}

func TestSetGet(t *testing.T) {
	c := cache.NewShardedCache()
	key := makeKey("/bin/sh", "l1")
	entry := makeEntry([]byte{1, 2, 3})
	c.Set(key, entry)
	got, ok := c.Get(key)
	if !ok {
		t.Fatal("expected hit after Set")
	}
	if string(got.Hash) != string(entry.Hash) {
		t.Fatalf("hash mismatch: got %v, want %v", got.Hash, entry.Hash)
	}
}

func TestSetOverwrites(t *testing.T) {
	c := cache.NewShardedCache()
	key := makeKey("/bin/sh", "l1")
	c.Set(key, makeEntry([]byte{1}))
	c.Set(key, makeEntry([]byte{2}))
	got, _ := c.Get(key)
	if got.Hash[0] != 2 {
		t.Fatal("second Set must overwrite first")
	}
}

// ─── SetIfAbsent ─────────────────────────────────────────────────────────────

func TestSetIfAbsent_Insert(t *testing.T) {
	c := cache.NewShardedCache()
	key := makeKey("/etc/passwd", "l2")
	entry := makeEntry([]byte{0xAA})
	actual, inserted := c.SetIfAbsent(key, entry)
	if !inserted {
		t.Fatal("expected inserted=true for new key")
	}
	if string(actual.Hash) != string(entry.Hash) {
		t.Fatal("returned entry must match inserted entry")
	}
}

func TestSetIfAbsent_ExistingWins(t *testing.T) {
	c := cache.NewShardedCache()
	key := makeKey("/etc/passwd", "l2")
	first := makeEntry([]byte{0x11})
	second := makeEntry([]byte{0x22})
	c.Set(key, first)
	actual, inserted := c.SetIfAbsent(key, second)
	if inserted {
		t.Fatal("expected inserted=false when key already present")
	}
	if actual.Hash[0] != 0x11 {
		t.Fatal("SetIfAbsent must return the existing entry, not the new one")
	}
}

// ─── Delete ───────────────────────────────────────────────────────────────────

func TestDelete(t *testing.T) {
	c := cache.NewShardedCache()
	key := makeKey("/usr/bin/ls", "l3")
	c.Set(key, makeEntry([]byte{5}))
	c.Delete(key)
	_, ok := c.Get(key)
	if ok {
		t.Fatal("entry must be absent after Delete")
	}
}

func TestDelete_NoOp(t *testing.T) {
	c := cache.NewShardedCache()
	// Should not panic for unknown key.
	c.Delete(makeKey("/nonexistent", "l0"))
}

// ─── Tombstone ────────────────────────────────────────────────────────────────

func TestTombstone(t *testing.T) {
	c := cache.NewShardedCache()
	key := makeKey("/deleted/file", "l1")
	tomb := cache.CacheEntry{Tombstone: true, SourceLayer: "l1", CachedAt: time.Now()}
	c.Set(key, tomb)
	got, ok := c.Get(key)
	if !ok {
		t.Fatal("expected tombstone to be retrievable")
	}
	if !got.Tombstone {
		t.Fatal("expected Tombstone=true")
	}
	if got.IsValid() {
		t.Fatal("tombstone must not be IsValid()")
	}
}

func TestTombstoneStats(t *testing.T) {
	c := cache.NewShardedCache()
	key := makeKey("/del", "l1")
	c.Set(key, cache.CacheEntry{Tombstone: true, CachedAt: time.Now()})
	stats := c.Stats()
	if stats.Tombstones != 1 {
		t.Fatalf("expected 1 tombstone in stats, got %d", stats.Tombstones)
	}
	if stats.Entries != 1 {
		t.Fatalf("expected 1 entry in stats, got %d", stats.Entries)
	}
}

// ─── WalkLayer ────────────────────────────────────────────────────────────────

func TestWalkLayer(t *testing.T) {
	c := cache.NewShardedCache()
	l := layer.Digest("target-layer")
	other := layer.Digest("other-layer")

	// Insert 5 entries for target layer and 3 for other.
	for i := 0; i < 5; i++ {
		c.Set(makeKey(fmt.Sprintf("/file/%d", i), l), makeEntry([]byte{byte(i)}))
	}
	for i := 0; i < 3; i++ {
		c.Set(makeKey(fmt.Sprintf("/other/%d", i), other), makeEntry([]byte{byte(i)}))
	}

	var count int
	c.WalkLayer(l, func(k cache.CacheKey, e cache.CacheEntry) bool {
		if k.LayerDigest != l {
			t.Errorf("WalkLayer yielded entry for wrong layer: %s", k.LayerDigest)
		}
		count++
		return true
	})
	if count != 5 {
		t.Fatalf("WalkLayer: expected 5 entries, got %d", count)
	}
}

func TestWalkLayer_EarlyStop(t *testing.T) {
	c := cache.NewShardedCache()
	l := layer.Digest("layer-x")
	for i := 0; i < 10; i++ {
		c.Set(makeKey(fmt.Sprintf("/f/%d", i), l), makeEntry([]byte{byte(i)}))
	}

	var count int
	c.WalkLayer(l, func(_ cache.CacheKey, _ cache.CacheEntry) bool {
		count++
		return count < 3 // stop after 3
	})
	// Early stop: walked up to 3 (may stop mid-shard)
	if count > 3 {
		t.Fatalf("WalkLayer early stop: expected <=3, got %d", count)
	}
}

// ─── EvictLayer ───────────────────────────────────────────────────────────────

func TestEvictLayer(t *testing.T) {
	c := cache.NewShardedCache()
	l := layer.Digest("evict-me")
	other := layer.Digest("keep-me")

	for i := 0; i < 5; i++ {
		c.Set(makeKey(fmt.Sprintf("/f/%d", i), l), makeEntry([]byte{byte(i)}))
	}
	c.Set(makeKey("/keep/file", other), makeEntry([]byte{99}))

	c.EvictLayer(l)

	var found int
	c.WalkLayer(l, func(_ cache.CacheKey, _ cache.CacheEntry) bool {
		found++
		return true
	})
	if found != 0 {
		t.Fatalf("EvictLayer: expected 0 entries, got %d", found)
	}

	// Other layer must be intact.
	if _, ok := c.Get(makeKey("/keep/file", other)); !ok {
		t.Fatal("EvictLayer must not affect other layers")
	}
}

// ─── Stats ────────────────────────────────────────────────────────────────────

func TestStats_HitMiss(t *testing.T) {
	c := cache.NewShardedCache()
	key := makeKey("/a", "l0")
	c.Set(key, makeEntry([]byte{1}))

	c.Get(key)          // hit
	c.Get(makeKey("/b", "l0")) // miss

	s := c.Stats()
	if s.Hits != 1 {
		t.Fatalf("expected 1 hit, got %d", s.Hits)
	}
	if s.Misses != 1 {
		t.Fatalf("expected 1 miss, got %d", s.Misses)
	}
	if s.HitRate() != 0.5 {
		t.Fatalf("expected hit rate 0.5, got %f", s.HitRate())
	}
}

func TestStats_Shards(t *testing.T) {
	c := cache.NewShardedCache()
	if c.Stats().Shards != 256 {
		t.Fatal("expected 256 shards")
	}
}

// ─── Concurrency ─────────────────────────────────────────────────────────────

func TestConcurrentSetGet(t *testing.T) {
	c := cache.NewShardedCache()
	var wg sync.WaitGroup
	const N = 1000
	for i := 0; i < N; i++ {
		wg.Add(1)
		go func(i int) {
			defer wg.Done()
			key := makeKey(fmt.Sprintf("/concurrent/%d", i), "l1")
			c.Set(key, makeEntry([]byte{byte(i % 256)}))
			c.Get(key)
		}(i)
	}
	wg.Wait()
	if c.Stats().Entries != N {
		t.Fatalf("concurrent Set: expected %d entries, got %d", N, c.Stats().Entries)
	}
}

func TestConcurrentSetIfAbsent_OnlyOneWins(t *testing.T) {
	c := cache.NewShardedCache()
	key := makeKey("/race/file", "lrace")
	const N = 100
	insertedCount := 0
	var mu sync.Mutex
	var wg sync.WaitGroup

	for i := 0; i < N; i++ {
		wg.Add(1)
		go func(i int) {
			defer wg.Done()
			_, inserted := c.SetIfAbsent(key, makeEntry([]byte{byte(i)}))
			if inserted {
				mu.Lock()
				insertedCount++
				mu.Unlock()
			}
		}(i)
	}
	wg.Wait()

	if insertedCount != 1 {
		t.Fatalf("SetIfAbsent: expected exactly 1 winner, got %d", insertedCount)
	}
}
