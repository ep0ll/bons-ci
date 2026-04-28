package access_test

import (
	"context"
	"testing"

	"github.com/bons/bons-ci/pkg/fshash/access"
	"github.com/bons/bons-ci/pkg/fshash/cache"
	"github.com/bons/bons-ci/pkg/fshash/internal/core"
	"github.com/bons/bons-ci/pkg/fshash/layer"
)

func setupDedup(t *testing.T) (*access.Deduplicator, layer.Store, cache.Store, *layer.Chain) {
	t.Helper()
	ctx := context.Background()

	layers := layer.NewMemoryStore()
	hashCache := cache.NewShardedStore(4, 128)
	resolver := layer.NewResolver(layers)
	dedup := access.NewDeduplicator(layers, hashCache, resolver, 1024, 0.001)

	base := core.NewLayerID("sha256:base")
	upper := core.NewLayerID("sha256:upper")

	layers.Register(ctx, base, core.LayerID{})
	layers.Register(ctx, upper, base)

	builder := layer.NewChainBuilder()
	builder.Push(base)
	builder.Push(upper)
	chain := builder.Build()

	return dedup, layers, hashCache, chain
}

func TestDedupCompute(t *testing.T) {
	dedup, _, _, chain := setupDedup(t)
	ctx := context.Background()

	event := core.AccessEvent{
		LayerID: core.NewLayerID("sha256:base"),
		Path:    "/etc/passwd",
		Op:      core.OpRead,
	}

	result := dedup.Process(ctx, event, chain)
	if result.Action != core.ActionCompute {
		t.Errorf("Action = %s, want compute", result.Action)
	}
}

func TestDedupSkipDuplicate(t *testing.T) {
	dedup, _, _, chain := setupDedup(t)
	ctx := context.Background()

	event := core.AccessEvent{
		LayerID: core.NewLayerID("sha256:base"),
		Path:    "/etc/passwd",
		Op:      core.OpRead,
	}

	// First: compute
	r1 := dedup.Process(ctx, event, chain)
	if r1.Action != core.ActionCompute {
		t.Errorf("first call: Action = %s, want compute", r1.Action)
	}

	// Second: skip (bloom filter)
	r2 := dedup.Process(ctx, event, chain)
	if r2.Action != core.ActionSkip {
		t.Errorf("second call: Action = %s, want skip", r2.Action)
	}
}

func TestDedupReuseAcrossLayers(t *testing.T) {
	dedup, layers, _, chain := setupDedup(t)
	ctx := context.Background()

	base := core.NewLayerID("sha256:base")
	upper := core.NewLayerID("sha256:upper")

	// Record a computed hash in the base layer.
	dedup.RecordComputed(core.FileHash{
		Path:      "/etc/passwd",
		Hash:      []byte{0xAA, 0xBB},
		Algorithm: "blake3",
		LayerID:   base,
	})

	// Reset session so bloom filter doesn't interfere.
	dedup.ResetSession()

	// File NOT modified in upper layer — should reuse.
	event := core.AccessEvent{
		LayerID: upper,
		Path:    "/etc/passwd",
		Op:      core.OpRead,
	}

	result := dedup.Process(ctx, event, chain)
	if result.Action != core.ActionReuse {
		t.Errorf("Action = %s, want reuse", result.Action)
	}
	if result.SourceLayer != base {
		t.Errorf("SourceLayer = %s, want %s", result.SourceLayer, base)
	}

	// Now mark the file as modified in upper layer.
	layers.MarkModified(upper, "/etc/passwd")
	dedup.ResetSession()

	// Should now compute because the file was modified.
	result = dedup.Process(ctx, event, chain)
	if result.Action != core.ActionCompute {
		t.Errorf("after modification: Action = %s, want compute", result.Action)
	}
}

func TestDedupStats(t *testing.T) {
	dedup, _, _, chain := setupDedup(t)
	ctx := context.Background()

	base := core.NewLayerID("sha256:base")

	for i := 0; i < 5; i++ {
		dedup.Process(ctx, core.AccessEvent{
			LayerID: base,
			Path:    "/file",
			Op:      core.OpRead,
		}, chain)
	}

	stats := dedup.Stats()
	if stats.EventsReceived != 5 {
		t.Errorf("EventsReceived = %d, want 5", stats.EventsReceived)
	}
	// 1 compute + 4 skips = 5
	if stats.EventsComputed != 1 {
		t.Errorf("EventsComputed = %d, want 1", stats.EventsComputed)
	}
	if stats.EventsSkipped != 4 {
		t.Errorf("EventsSkipped = %d, want 4", stats.EventsSkipped)
	}
}

func TestBloomFilterFalsePositiveRate(t *testing.T) {
	bf := access.NewBloomFilter(10000, 0.01)

	// Add 10K items.
	for i := 0; i < 10000; i++ {
		bf.Add(core.NewLayerID("sha256:x"), string(rune(i)))
	}

	// Test 10K non-existent items.
	fps := 0
	for i := 10000; i < 20000; i++ {
		if bf.Test(core.NewLayerID("sha256:x"), "miss-"+string(rune(i))) {
			fps++
		}
	}

	rate := float64(fps) / 10000.0
	// Allow 5% margin over the configured 1% FP rate.
	if rate > 0.05 {
		t.Errorf("FP rate = %.3f, expected < 0.05", rate)
	}

	t.Logf("Bloom filter: FP rate = %.4f, fill = %.3f", rate, bf.FillRatio())
}

func TestBloomFilterReset(t *testing.T) {
	bf := access.NewBloomFilter(100, 0.001)

	l := core.NewLayerID("sha256:r")
	bf.Add(l, "/a")

	if !bf.Test(l, "/a") {
		t.Error("expected /a to be present")
	}

	bf.Reset()

	if bf.Test(l, "/a") {
		t.Error("expected /a to be absent after reset")
	}
	if bf.Count() != 0 {
		t.Errorf("Count after reset = %d, want 0", bf.Count())
	}
}

func BenchmarkDedupProcess(b *testing.B) {
	ctx := context.Background()
	layers := layer.NewMemoryStore()
	hashCache := cache.NewShardedStore(16, 65536)
	resolver := layer.NewResolver(layers)
	dedup := access.NewDeduplicator(layers, hashCache, resolver, 65536, 0.001)

	base := core.NewLayerID("sha256:bench-base")
	layers.Register(ctx, base, core.LayerID{})

	builder := layer.NewChainBuilder()
	builder.Push(base)
	chain := builder.Build()

	b.ResetTimer()
	b.ReportAllocs()

	for i := 0; i < b.N; i++ {
		dedup.Process(ctx, core.AccessEvent{
			LayerID: base,
			Path:    "/bench/file",
			Op:      core.OpRead,
		}, chain)
	}
}
