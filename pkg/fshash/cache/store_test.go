package cache_test

import (
	"fmt"
	"sync"
	"testing"

	"github.com/bons/bons-ci/pkg/fshash/cache"
	"github.com/bons/bons-ci/pkg/fshash/internal/core"
)

func TestShardedStorePutGet(t *testing.T) {
	store := cache.NewShardedStore(4, 128)

	l := core.NewLayerID("sha256:test")
	h := core.FileHash{
		Path:      "/foo",
		Hash:      []byte{1, 2, 3},
		Algorithm: "blake3",
		LayerID:   l,
	}

	store.Put(l, "/foo", h)

	got, ok := store.Get(l, "/foo")
	if !ok {
		t.Fatal("expected cache hit")
	}
	if got.Path != "/foo" {
		t.Errorf("got.Path = %s, want /foo", got.Path)
	}

	// Miss for different path.
	_, ok = store.Get(l, "/bar")
	if ok {
		t.Error("expected cache miss for /bar")
	}

	// Miss for different layer.
	_, ok = store.Get(core.NewLayerID("sha256:other"), "/foo")
	if ok {
		t.Error("expected cache miss for different layer")
	}
}

func TestShardedStoreInvalidate(t *testing.T) {
	store := cache.NewShardedStore(4, 128)
	l := core.NewLayerID("sha256:inv")

	store.Put(l, "/a", core.FileHash{Path: "/a", LayerID: l})
	store.Put(l, "/b", core.FileHash{Path: "/b", LayerID: l})

	store.Invalidate(l, "/a")

	_, ok := store.Get(l, "/a")
	if ok {
		t.Error("expected miss after invalidation of /a")
	}

	_, ok = store.Get(l, "/b")
	if !ok {
		t.Error("expected /b to still be cached")
	}
}

func TestShardedStoreInvalidateLayer(t *testing.T) {
	store := cache.NewShardedStore(4, 128)
	l := core.NewLayerID("sha256:layer-inv")

	for i := 0; i < 10; i++ {
		path := fmt.Sprintf("/file/%d", i)
		store.Put(l, path, core.FileHash{Path: path, LayerID: l})
	}

	store.InvalidateLayer(l)

	for i := 0; i < 10; i++ {
		_, ok := store.Get(l, fmt.Sprintf("/file/%d", i))
		if ok {
			t.Errorf("expected miss for /file/%d after layer invalidation", i)
		}
	}
}

func TestShardedStoreLRUEviction(t *testing.T) {
	// Use capacity exactly at the per-shard minimum boundary.
	// NewShardedStore(1, 16) → 1 shard, 16 entries per shard.
	store := cache.NewShardedStore(1, 16)
	l := core.NewLayerID("sha256:evict")

	// Fill to capacity.
	for i := 0; i < 16; i++ {
		path := fmt.Sprintf("/file/%d", i)
		store.Put(l, path, core.FileHash{Path: path, LayerID: l})
	}

	// Next insert should evict the least-recently-used (/file/0).
	store.Put(l, "/new", core.FileHash{Path: "/new", LayerID: l})

	_, ok := store.Get(l, "/file/0")
	if ok {
		t.Error("expected /file/0 to be evicted")
	}

	_, ok = store.Get(l, "/new")
	if !ok {
		t.Error("expected /new to be cached")
	}

	stats := store.Stats()
	if stats.Evictions == 0 {
		t.Error("expected at least 1 eviction")
	}
}

func TestShardedStoreStats(t *testing.T) {
	store := cache.NewShardedStore(4, 128)
	l := core.NewLayerID("sha256:stats")

	store.Put(l, "/hit", core.FileHash{Path: "/hit", LayerID: l})
	store.Get(l, "/hit")   // hit
	store.Get(l, "/miss")  // miss

	stats := store.Stats()
	if stats.Hits != 1 {
		t.Errorf("Hits = %d, want 1", stats.Hits)
	}
	if stats.Misses != 1 {
		t.Errorf("Misses = %d, want 1", stats.Misses)
	}
}

func TestShardedStoreConcurrent(t *testing.T) {
	store := cache.NewShardedStore(16, 1024)

	var wg sync.WaitGroup
	const goroutines = 64
	const opsPerGoroutine = 100

	for g := 0; g < goroutines; g++ {
		wg.Add(1)
		go func(id int) {
			defer wg.Done()
			l := core.NewLayerID(fmt.Sprintf("sha256:g%d", id))
			for i := 0; i < opsPerGoroutine; i++ {
				path := fmt.Sprintf("/file/%d/%d", id, i)
				store.Put(l, path, core.FileHash{Path: path, LayerID: l})
				store.Get(l, path)
			}
		}(g)
	}

	wg.Wait()

	stats := store.Stats()
	if stats.Hits < goroutines*opsPerGoroutine/2 {
		t.Errorf("expected significant hits, got %d", stats.Hits)
	}
}

func BenchmarkShardedStorePutGet(b *testing.B) {
	store := cache.NewShardedStore(16, 65536)
	l := core.NewLayerID("sha256:bench")

	b.ResetTimer()
	b.ReportAllocs()

	b.RunParallel(func(pb *testing.PB) {
		i := 0
		for pb.Next() {
			path := fmt.Sprintf("/bench/%d", i)
			store.Put(l, path, core.FileHash{Path: path, LayerID: l})
			store.Get(l, path)
			i++
		}
	})
}
