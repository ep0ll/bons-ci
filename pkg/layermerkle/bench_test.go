package layermerkle_test

import (
	"context"
	"fmt"
	"strings"
	"testing"

	"github.com/bons/bons-ci/pkg/layermerkle"
	"github.com/bons/bons-ci/pkg/layermerkle/testutil"
)

// BenchmarkShardedLRUCache_Get_CacheHit measures the throughput of a
// fully-warm cache with 256 shards under zero contention.
func BenchmarkShardedLRUCache_Get_CacheHit(b *testing.B) {
	c := layermerkle.NewShardedLRUCache(128_000)
	id := testutil.MakeLayerStack("bench-layer").Top()
	hash := testutil.MakeFileHash("bench-content")
	c.Set(id, "lib/libssl.so", hash)

	b.ResetTimer()
	b.ReportAllocs()
	for i := 0; i < b.N; i++ {
		c.Get(id, "lib/libssl.so")
	}
}

// BenchmarkShardedLRUCache_Set_Parallel measures Set throughput with 8
// concurrent goroutines hammering different shards.
func BenchmarkShardedLRUCache_Set_Parallel(b *testing.B) {
	c := layermerkle.NewShardedLRUCache(128_000)
	id := testutil.MakeLayerStack("bench-layer").Top()

	b.ResetTimer()
	b.ReportAllocs()
	b.RunParallel(func(pb *testing.PB) {
		i := 0
		for pb.Next() {
			c.Set(id, fmt.Sprintf("path/%d", i%1000), testutil.MakeFileHash(fmt.Sprintf("v%d", i)))
			i++
		}
	})
}

// BenchmarkDeduplicationEngine_Resolve_CacheHit measures the engine's hot
// path when every request is served from the top-layer cache (L1 hit).
func BenchmarkDeduplicationEngine_Resolve_CacheHit(b *testing.B) {
	cache := layermerkle.NewShardedLRUCache(128_000)
	hasher := testutil.NewFakeHasher()
	resolver := testutil.NewFakeResolver()
	engine := layermerkle.NewDeduplicationEngine(cache, hasher, resolver, nil)

	layerID := testutil.MakeLayerStack("bench-layer").Top()
	hash := testutil.MakeFileHash("content")
	cache.Set(layerID, "lib/libssl.so", hash)

	req := layermerkle.HashRequest{
		VertexID:   testutil.MakeVertexID("bench-vertex"),
		LayerStack: layermerkle.LayerStack{layerID},
		RelPath:    "lib/libssl.so",
	}
	ctx := context.Background()

	b.ResetTimer()
	b.ReportAllocs()
	for i := 0; i < b.N; i++ {
		engine.Resolve(ctx, req)
	}
}

// BenchmarkDeduplicationEngine_Resolve_CrossLayerHit measures the engine's
// L2 path: top-layer miss but owner-layer cache hit (cross-ExecOp reuse).
func BenchmarkDeduplicationEngine_Resolve_CrossLayerHit(b *testing.B) {
	cache := layermerkle.NewShardedLRUCache(128_000)
	hasher := testutil.NewFakeHasher()
	resolver := testutil.NewFakeResolver()
	engine := layermerkle.NewDeduplicationEngine(cache, hasher, resolver, nil)

	baseLayer := testutil.MakeLayerStack("base").Top()
	topLayer := testutil.MakeLayerStack("top").Top()
	hash := testutil.MakeFileHash("content")
	resolver.AddFile("lib/libssl.so", baseLayer, "/diffs/base")
	cache.Set(baseLayer, "lib/libssl.so", hash) // owner-layer hit

	req := layermerkle.HashRequest{
		VertexID:   testutil.MakeVertexID("bench-vertex"),
		LayerStack: layermerkle.LayerStack{baseLayer, topLayer},
		RelPath:    "lib/libssl.so",
	}
	ctx := context.Background()

	b.ResetTimer()
	b.ReportAllocs()
	for i := 0; i < b.N; i++ {
		engine.Resolve(ctx, req)
	}
}

// BenchmarkMerkleTreeBuilder_Add_1000Leaves measures insertion and finalization
// for a tree with 1000 file leaves.
func BenchmarkMerkleTreeBuilder_Add_1000Leaves(b *testing.B) {
	results := make([]*layermerkle.HashResult, 1000)
	for i := range results {
		results[i] = &layermerkle.HashResult{
			VertexID: testutil.MakeVertexID("v"),
			RelPath:  fmt.Sprintf("dir%d/file%d.txt", i/100, i),
			Hash:     testutil.MakeFileHash(fmt.Sprintf("content-%d", i)),
		}
	}

	b.ResetTimer()
	b.ReportAllocs()
	for i := 0; i < b.N; i++ {
		builder := layermerkle.NewMerkleTreeBuilder(testutil.MakeVertexID("v"), testutil.MakeLayerStack("l"))
		for _, r := range results {
			builder.Add(r)
		}
		builder.Finalize()
	}
}

// BenchmarkEngine_Submit_Parallel measures end-to-end event submission
// throughput from 8 concurrent goroutines.
func BenchmarkEngine_Submit_Parallel(b *testing.B) {
	resolver := testutil.NewFakeResolver()
	layerID := testutil.MakeLayerStack("bench").Top()
	for i := range 100 {
		resolver.AddFile(fmt.Sprintf("file%d", i), layerID, "/diffs/bench")
	}

	engine := layermerkle.NewEngine(
		layermerkle.WithFileHasher(testutil.NewFakeHasher()),
		layermerkle.WithResolver(resolver),
		layermerkle.WithWorkers(8),
		layermerkle.WithEventBufferSize(65536),
	)
	ctx := context.Background()
	_ = engine.Start(ctx)
	defer engine.Stop()

	events := make([]*layermerkle.AccessEvent, 1000)
	for i := range events {
		events[i] = testutil.NewAccessEvent().
			WithVertexID("bench-vertex").
			WithLayerStack("bench").
			WithRelPath(fmt.Sprintf("file%d", i%100)).
			Build()
	}

	b.ResetTimer()
	b.ReportAllocs()
	b.RunParallel(func(pb *testing.PB) {
		i := 0
		for pb.Next() {
			_ = engine.Submit(ctx, events[i%len(events)])
			i++
		}
	})
}

// BenchmarkLayerStackKey_New measures LayerStackKey encoding with a 5-layer stack.
func BenchmarkLayerStackKey_New(b *testing.B) {
	stack := testutil.MakeLayerStack("l0", "l1", "l2", "l3", "l4")
	b.ResetTimer()
	b.ReportAllocs()
	for i := 0; i < b.N; i++ {
		_ = layermerkle.NewLayerStackKey(stack)
	}
}

// BenchmarkEncodeLayerStack measures EncodeLayerStack for use in fanwatch attrs.
func BenchmarkEncodeLayerStack(b *testing.B) {
	stack := testutil.MakeLayerStack("l0", "l1", "l2")
	b.ResetTimer()
	b.ReportAllocs()
	for i := 0; i < b.N; i++ {
		_ = layermerkle.EncodeLayerStack(stack)
	}
}

// BenchmarkShardedLRUCache_DeleteLayer measures layer invalidation with
// 10 000 entries across 256 shards.
func BenchmarkShardedLRUCache_DeleteLayer(b *testing.B) {
	id := testutil.MakeLayerStack("evict-bench").Top()

	for i := 0; i < b.N; i++ {
		b.StopTimer()
		c := layermerkle.NewShardedLRUCache(128_000)
		for j := range 10_000 {
			c.Set(id, strings.Repeat("x", j%200+1), testutil.MakeFileHash(fmt.Sprintf("%d", j)))
		}
		b.StartTimer()
		c.DeleteLayer(id)
	}
}
