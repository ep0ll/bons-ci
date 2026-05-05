package cache_test

import (
	"fmt"
	"testing"

	"github.com/user/layermerkle/cache"
	"github.com/user/layermerkle/layer"
)

// ─── ShardedCache benchmarks ──────────────────────────────────────────────────

func BenchmarkShardedCache_Set(b *testing.B) {
	c := cache.NewShardedCache()
	l := layer.Digest("bench-layer")
	entry := makeEntry([]byte{1, 2, 3, 4, 5, 6, 7, 8})
	b.ResetTimer()
	b.RunParallel(func(pb *testing.PB) {
		i := 0
		for pb.Next() {
			c.Set(makeKey(fmt.Sprintf("/bench/%d", i), l), entry)
			i++
		}
	})
}

func BenchmarkShardedCache_Get_Hit(b *testing.B) {
	c := cache.NewShardedCache()
	l := layer.Digest("bench-layer")
	for i := 0; i < 10000; i++ {
		c.Set(makeKey(fmt.Sprintf("/bench/%d", i), l), makeEntry([]byte{1}))
	}
	b.ResetTimer()
	b.RunParallel(func(pb *testing.PB) {
		i := 0
		for pb.Next() {
			c.Get(makeKey(fmt.Sprintf("/bench/%d", i%10000), l))
			i++
		}
	})
}

func BenchmarkShardedCache_Get_Miss(b *testing.B) {
	c := cache.NewShardedCache()
	l := layer.Digest("bench-layer")
	b.ResetTimer()
	b.RunParallel(func(pb *testing.PB) {
		i := 0
		for pb.Next() {
			c.Get(makeKey(fmt.Sprintf("/miss/%d", i), l))
			i++
		}
	})
}

func BenchmarkShardedCache_SetIfAbsent(b *testing.B) {
	c := cache.NewShardedCache()
	l := layer.Digest("bench-layer")
	entry := makeEntry([]byte{1})
	// Pre-fill half the keys so we get a mix of inserts and "already exists".
	for i := 0; i < 5000; i++ {
		c.Set(makeKey(fmt.Sprintf("/bench/%d", i), l), entry)
	}
	b.ResetTimer()
	b.RunParallel(func(pb *testing.PB) {
		i := 0
		for pb.Next() {
			c.SetIfAbsent(makeKey(fmt.Sprintf("/bench/%d", i%10000), l), entry)
			i++
		}
	})
}

func BenchmarkShardedCache_WalkLayer(b *testing.B) {
	c := cache.NewShardedCache()
	l := layer.Digest("walk-layer")
	for i := 0; i < 1000; i++ {
		c.Set(makeKey(fmt.Sprintf("/f/%d", i), l), makeEntry([]byte{1}))
	}
	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		c.WalkLayer(l, func(_ cache.CacheKey, _ cache.CacheEntry) bool { return true })
	}
}

// ─── BoundedCache benchmarks ─────────────────────────────────────────────────

func BenchmarkBoundedCache_Set(b *testing.B) {
	bc := cache.NewBoundedCache(cache.WithMaxEntriesPerShard(100))
	l := layer.Digest("bench-layer")
	entry := makeEntry([]byte{1, 2, 3, 4, 5, 6, 7, 8})
	b.ResetTimer()
	b.RunParallel(func(pb *testing.PB) {
		i := 0
		for pb.Next() {
			bc.Set(makeKey(fmt.Sprintf("/bench/%d", i), l), entry)
			i++
		}
	})
}

func BenchmarkBoundedCache_Get_Hit(b *testing.B) {
	bc := cache.NewBoundedCache(cache.WithMaxEntriesPerShard(200))
	l := layer.Digest("bench-layer")
	for i := 0; i < 10000; i++ {
		bc.Set(makeKey(fmt.Sprintf("/bench/%d", i), l), makeEntry([]byte{1}))
	}
	b.ResetTimer()
	b.RunParallel(func(pb *testing.PB) {
		i := 0
		for pb.Next() {
			bc.Get(makeKey(fmt.Sprintf("/bench/%d", i%10000), l))
			i++
		}
	})
}

// ─── ShardedCache: worker scaling ─────────────────────────────────────────────

func BenchmarkShardedCache_Workers(b *testing.B) {
	for _, workers := range []int{1, 2, 4, 8, 16, 32} {
		workers := workers
		b.Run(fmt.Sprintf("w%d", workers), func(b *testing.B) {
			c := cache.NewShardedCache()
			l := layer.Digest("bench")
			entry := makeEntry([]byte{1})
			b.SetParallelism(workers)
			b.ResetTimer()
			b.RunParallel(func(pb *testing.PB) {
				i := 0
				for pb.Next() {
					key := makeKey(fmt.Sprintf("/f/%d", i%1000), l)
					if i%3 == 0 {
						c.Set(key, entry)
					} else {
						c.Get(key)
					}
					i++
				}
			})
		})
	}
}
