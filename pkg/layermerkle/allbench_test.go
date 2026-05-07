package layermerkle_test

import (
	"context"
	"fmt"
	"strings"
	"testing"
	"time"

	"github.com/bons/bons-ci/pkg/layermerkle"
	"github.com/bons/bons-ci/pkg/layermerkle/testutil"
)

// ─────────────────────────────────────────────────────────────────────────────
// ShardedLRUCache benchmarks
// ─────────────────────────────────────────────────────────────────────────────

func BenchmarkShardedLRU_Get_WarmSingleKey(b *testing.B) {
	c := layermerkle.NewShardedLRUCache(128_000)
	id := testutil.MakeLayerStack("l").Top()
	h := testutil.MakeFileHash("content")
	c.Set(id, "lib/libssl.so", h)
	b.ResetTimer()
	b.ReportAllocs()
	for i := 0; i < b.N; i++ {
		c.Get(id, "lib/libssl.so")
	}
}

func BenchmarkShardedLRU_Get_Miss(b *testing.B) {
	c := layermerkle.NewShardedLRUCache(128_000)
	id := testutil.MakeLayerStack("l").Top()
	b.ResetTimer()
	b.ReportAllocs()
	for i := 0; i < b.N; i++ {
		c.Get(id, "missing/path")
	}
}

func BenchmarkShardedLRU_Set_NoEviction(b *testing.B) {
	c := layermerkle.NewShardedLRUCache(128_000)
	id := testutil.MakeLayerStack("l").Top()
	b.ResetTimer()
	b.ReportAllocs()
	for i := 0; i < b.N; i++ {
		c.Set(id, fmt.Sprintf("path/%d", i%10000), testutil.MakeFileHash("v"))
	}
}

func BenchmarkShardedLRU_Set_Parallel8(b *testing.B) {
	c := layermerkle.NewShardedLRUCache(128_000)
	id := testutil.MakeLayerStack("l").Top()
	b.ResetTimer()
	b.ReportAllocs()
	b.RunParallel(func(pb *testing.PB) {
		i := 0
		for pb.Next() {
			c.Set(id, fmt.Sprintf("p/%d", i%5000), testutil.MakeFileHash("v"))
			i++
		}
	})
}

func BenchmarkShardedLRU_Get_Parallel8_Warm(b *testing.B) {
	c := layermerkle.NewShardedLRUCache(128_000)
	id := testutil.MakeLayerStack("l").Top()
	for i := range 1000 {
		c.Set(id, fmt.Sprintf("p/%d", i), testutil.MakeFileHash(fmt.Sprintf("v%d", i)))
	}
	b.ResetTimer()
	b.ReportAllocs()
	b.RunParallel(func(pb *testing.PB) {
		i := 0
		for pb.Next() {
			c.Get(id, fmt.Sprintf("p/%d", i%1000))
			i++
		}
	})
}

// ─────────────────────────────────────────────────────────────────────────────
// RingStatCache benchmarks
// ─────────────────────────────────────────────────────────────────────────────

func BenchmarkRingStatCache_Get_Hit(b *testing.B) {
	c := layermerkle.NewRingStatCache(4096)
	id := testutil.MakeLayerStack("l").Top()
	c.Set(id, "lib/x", layermerkle.StatPresent)
	b.ResetTimer()
	b.ReportAllocs()
	for i := 0; i < b.N; i++ {
		c.Get(id, "lib/x")
	}
}

func BenchmarkRingStatCache_Set(b *testing.B) {
	c := layermerkle.NewRingStatCache(4096)
	id := testutil.MakeLayerStack("l").Top()
	b.ResetTimer()
	b.ReportAllocs()
	for i := 0; i < b.N; i++ {
		c.Set(id, fmt.Sprintf("p/%d", i%2000), layermerkle.StatPresent)
	}
}

// ─────────────────────────────────────────────────────────────────────────────
// DeduplicationEngine benchmarks
// ─────────────────────────────────────────────────────────────────────────────

func BenchmarkDeduplicationEngine_L1CacheHit(b *testing.B) {
	cache := layermerkle.NewShardedLRUCache(128_000)
	hasher := testutil.NewFakeHasher()
	resolver := testutil.NewFakeResolver()
	engine := layermerkle.NewDeduplicationEngine(cache, hasher, resolver, nil)

	layerID := testutil.MakeLayerStack("bench").Top()
	cache.Set(layerID, "lib/libssl.so", testutil.MakeFileHash("ssl"))
	req := layermerkle.HashRequest{
		VertexID:   testutil.MakeVertexID("v"),
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

func BenchmarkDeduplicationEngine_L2CrossLayerHit(b *testing.B) {
	cache := layermerkle.NewShardedLRUCache(128_000)
	resolver := testutil.NewFakeResolver()
	hasher := testutil.NewFakeHasher()
	engine := layermerkle.NewDeduplicationEngine(cache, hasher, resolver, nil)

	baseLayer := testutil.MakeLayerStack("base").Top()
	topLayer := testutil.MakeLayerStack("top").Top()
	hash := testutil.MakeFileHash("ssl-content")
	resolver.AddFile("lib/libssl.so", baseLayer, "/diffs/base")
	cache.Set(baseLayer, "lib/libssl.so", hash) // owner-layer warm

	req := layermerkle.HashRequest{
		VertexID:   testutil.MakeVertexID("v"),
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

func BenchmarkDeduplicationEngine_Resolve_Parallel(b *testing.B) {
	cache := layermerkle.NewShardedLRUCache(128_000)
	resolver := testutil.NewFakeResolver()
	hasher := testutil.NewFakeHasher()
	engine := layermerkle.NewDeduplicationEngine(cache, hasher, resolver, nil)

	layerID := testutil.MakeLayerStack("bench-par").Top()
	cache.Set(layerID, "lib/x.so", testutil.MakeFileHash("x"))
	req := layermerkle.HashRequest{
		VertexID:   testutil.MakeVertexID("v"),
		LayerStack: layermerkle.LayerStack{layerID},
		RelPath:    "lib/x.so",
	}
	ctx := context.Background()
	b.ResetTimer()
	b.ReportAllocs()
	b.RunParallel(func(pb *testing.PB) {
		for pb.Next() {
			engine.Resolve(ctx, req)
		}
	})
}

// ─────────────────────────────────────────────────────────────────────────────
// MerkleTreeBuilder benchmarks
// ─────────────────────────────────────────────────────────────────────────────

func BenchmarkMerkleTreeBuilder_100Leaves(b *testing.B) {
	results := makeBenchResults(100)
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

func BenchmarkMerkleTreeBuilder_1000Leaves(b *testing.B) {
	results := makeBenchResults(1000)
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

func BenchmarkMerkleTreeBuilder_10000Leaves(b *testing.B) {
	results := makeBenchResults(10000)
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

// ─────────────────────────────────────────────────────────────────────────────
// Engine submit benchmarks
// ─────────────────────────────────────────────────────────────────────────────

func BenchmarkEngine_Submit_Sequential(b *testing.B) {
	resolver := testutil.NewFakeResolver()
	layerID := testutil.MakeLayerStack("sub").Top()
	for i := range 100 {
		resolver.AddFile(fmt.Sprintf("f%d", i), layerID, "/d")
	}
	engine := layermerkle.NewEngine(
		layermerkle.WithFileHasher(testutil.NewFakeHasher()),
		layermerkle.WithResolver(resolver),
		layermerkle.WithWorkers(4),
		layermerkle.WithEventBufferSize(65536),
	)
	ctx := context.Background()
	_ = engine.Start(ctx)
	defer engine.Stop()

	ev := testutil.NewAccessEvent().WithLayerStack("sub").WithRelPath("f0").Build()
	b.ResetTimer()
	b.ReportAllocs()
	for i := 0; i < b.N; i++ {
		_ = engine.Submit(ctx, ev)
	}
}

func BenchmarkEngine_SubmitBatch_100Events(b *testing.B) {
	resolver := testutil.NewFakeResolver()
	layerID := testutil.MakeLayerStack("batch").Top()
	for i := range 100 {
		resolver.AddFile(fmt.Sprintf("f%d", i), layerID, "/d")
	}
	engine := layermerkle.NewEngine(
		layermerkle.WithFileHasher(testutil.NewFakeHasher()),
		layermerkle.WithResolver(resolver),
		layermerkle.WithWorkers(4),
		layermerkle.WithEventBufferSize(65536),
	)
	ctx := context.Background()
	_ = engine.Start(ctx)
	defer engine.Stop()

	events := make([]*layermerkle.AccessEvent, 100)
	for i := range events {
		events[i] = testutil.NewAccessEvent().
			WithLayerStack("batch").
			WithRelPath(fmt.Sprintf("f%d", i)).
			Build()
	}
	b.ResetTimer()
	b.ReportAllocs()
	for i := 0; i < b.N; i++ {
		engine.SubmitBatch(ctx, events)
	}
}

// ─────────────────────────────────────────────────────────────────────────────
// Serialization benchmarks
// ─────────────────────────────────────────────────────────────────────────────

func BenchmarkMarshalForest_10Trees_100Leaves(b *testing.B) {
	forest := layermerkle.NewMerkleForest()
	for t := range 10 {
		builder := layermerkle.NewMerkleTreeBuilder(
			testutil.MakeVertexID(fmt.Sprintf("v%d", t)),
			testutil.MakeLayerStack("l"),
		)
		for _, r := range makeBenchResults(100) {
			builder.Add(r)
		}
		forest.Add(builder.Finalize())
	}
	b.ResetTimer()
	b.ReportAllocs()
	for i := 0; i < b.N; i++ {
		_, _ = layermerkle.MarshalForest(forest)
	}
}

func BenchmarkUnmarshalForest_10Trees_100Leaves(b *testing.B) {
	forest := layermerkle.NewMerkleForest()
	for t := range 10 {
		builder := layermerkle.NewMerkleTreeBuilder(
			testutil.MakeVertexID(fmt.Sprintf("v%d", t)),
			testutil.MakeLayerStack("l"),
		)
		for _, r := range makeBenchResults(50) {
			builder.Add(r)
		}
		forest.Add(builder.Finalize())
	}
	data, _ := layermerkle.MarshalForest(forest)
	b.ResetTimer()
	b.ReportAllocs()
	for i := 0; i < b.N; i++ {
		_, _ = layermerkle.UnmarshalForest(data)
	}
}

// ─────────────────────────────────────────────────────────────────────────────
// ForestDiff benchmarks
// ─────────────────────────────────────────────────────────────────────────────

func BenchmarkForestDiff_100Vertices(b *testing.B) {
	a := layermerkle.NewMerkleForest()
	bForest := layermerkle.NewMerkleForest()
	for t := range 100 {
		vtxID := testutil.MakeVertexID(fmt.Sprintf("v%d", t))
		ba := layermerkle.NewMerkleTreeBuilder(vtxID, testutil.MakeLayerStack("l"))
		bb := layermerkle.NewMerkleTreeBuilder(vtxID, testutil.MakeLayerStack("l"))
		for _, r := range makeBenchResults(20) {
			ba.Add(r)
			// half the trees change
			if t%2 == 0 {
				bb.Add(r)
			} else {
				bb.Add(&layermerkle.HashResult{
					RelPath: r.RelPath,
					Hash:    testutil.MakeFileHash(r.RelPath + "-changed"),
				})
			}
		}
		a.Add(ba.Finalize())
		bForest.Add(bb.Finalize())
	}
	b.ResetTimer()
	b.ReportAllocs()
	for i := 0; i < b.N; i++ {
		layermerkle.Diff(a, bForest)
	}
}

// ─────────────────────────────────────────────────────────────────────────────
// LayerStackKey benchmarks
// ─────────────────────────────────────────────────────────────────────────────

func BenchmarkLayerStackKey_5Layers(b *testing.B) {
	stack := testutil.MakeLayerStack("l0", "l1", "l2", "l3", "l4")
	b.ResetTimer()
	b.ReportAllocs()
	for i := 0; i < b.N; i++ {
		_ = layermerkle.NewLayerStackKey(stack)
	}
}

func BenchmarkEncodeLayerStack_3Layers(b *testing.B) {
	stack := testutil.MakeLayerStack("l0", "l1", "l2")
	b.ResetTimer()
	b.ReportAllocs()
	for i := 0; i < b.N; i++ {
		_ = layermerkle.EncodeLayerStack(stack)
	}
}

// ─────────────────────────────────────────────────────────────────────────────
// Whiteout detection benchmarks
// ─────────────────────────────────────────────────────────────────────────────

func BenchmarkClassifyWhiteout_RegularFile(b *testing.B) {
	b.ReportAllocs()
	for i := 0; i < b.N; i++ {
		layermerkle.ClassifyWhiteout("libssl.so.3")
	}
}

func BenchmarkClassifyWhiteout_FileWhiteout(b *testing.B) {
	b.ReportAllocs()
	for i := 0; i < b.N; i++ {
		layermerkle.ClassifyWhiteout(".wh.libssl.so.3")
	}
}

func BenchmarkIsWhiteoutPath_DeepPath(b *testing.B) {
	path := "usr/lib/x86_64-linux-gnu/libssl.so.3"
	b.ReportAllocs()
	for i := 0; i < b.N; i++ {
		layermerkle.IsWhiteoutPath(path)
	}
}

// ─────────────────────────────────────────────────────────────────────────────
// ExportProvenance benchmarks
// ─────────────────────────────────────────────────────────────────────────────

func BenchmarkExportProvenance_50Trees_200Leaves(b *testing.B) {
	forest := layermerkle.NewMerkleForest()
	for t := range 50 {
		builder := layermerkle.NewMerkleTreeBuilder(
			testutil.MakeVertexID(fmt.Sprintf("pv%d", t)),
			testutil.MakeLayerStack("l"),
		)
		for _, r := range makeBenchResults(200) {
			builder.Add(r)
		}
		forest.Add(builder.Finalize())
	}
	b.ResetTimer()
	b.ReportAllocs()
	for i := 0; i < b.N; i++ {
		layermerkle.ExportProvenance(forest)
	}
}

// ─────────────────────────────────────────────────────────────────────────────
// DeleteLayer benchmarks
// ─────────────────────────────────────────────────────────────────────────────

func BenchmarkShardedLRU_DeleteLayer_10000Entries(b *testing.B) {
	id := testutil.MakeLayerStack("del").Top()
	for i := 0; i < b.N; i++ {
		b.StopTimer()
		c := layermerkle.NewShardedLRUCache(128_000)
		for j := range 10_000 {
			c.Set(id, strings.Repeat("k", j%200+1), testutil.MakeFileHash("v"))
		}
		b.StartTimer()
		c.DeleteLayer(id)
	}
}

// ─────────────────────────────────────────────────────────────────────────────
// HTML/Markdown report benchmarks
// ─────────────────────────────────────────────────────────────────────────────

func BenchmarkWriteMarkdownReport_50Changes(b *testing.B) {
	a := layermerkle.NewMerkleForest()
	bf := layermerkle.NewMerkleForest()
	for t := range 50 {
		vtx := testutil.MakeVertexID(fmt.Sprintf("mdr%d", t))
		ba := layermerkle.NewMerkleTreeBuilder(vtx, testutil.MakeLayerStack("l"))
		bb := layermerkle.NewMerkleTreeBuilder(vtx, testutil.MakeLayerStack("l"))
		ba.Add(&layermerkle.HashResult{RelPath: "f", Hash: testutil.MakeFileHash("v1")})
		bb.Add(&layermerkle.HashResult{RelPath: "f", Hash: testutil.MakeFileHash("v2")})
		a.Add(ba.Finalize())
		bf.Add(bb.Finalize())
	}
	d := layermerkle.Diff(a, bf)
	b.ResetTimer()
	b.ReportAllocs()
	for i := 0; i < b.N; i++ {
		layermerkle.WriteMarkdownReport(noopWriter{}, d, "bench") //nolint:errcheck
	}
}

// ─────────────────────────────────────────────────────────────────────────────
// helpers
// ─────────────────────────────────────────────────────────────────────────────

func makeBenchResults(n int) []*layermerkle.HashResult {
	results := make([]*layermerkle.HashResult, n)
	for i := range results {
		results[i] = &layermerkle.HashResult{
			VertexID: testutil.MakeVertexID("v"),
			RelPath:  fmt.Sprintf("dir%d/file%d.so", i/100, i),
			Hash:     testutil.MakeFileHash(fmt.Sprintf("content-%d", i)),
		}
	}
	return results
}

type noopWriter struct{}

func (noopWriter) Write(p []byte) (int, error) { return len(p), nil }

// Suppress unused import.
var _ = time.Now
