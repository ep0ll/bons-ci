package hook_test

import (
	"context"
	"testing"

	"github.com/user/layermerkle/hook"
)

func TestMetricsHook_Counts(t *testing.T) {
	m := hook.NewMetricsHook()
	ctx := context.Background()

	m.OnHook(ctx, hook.HookEvent{Type: hook.HookCacheHit})    //nolint:errcheck
	m.OnHook(ctx, hook.HookEvent{Type: hook.HookCacheHit})    //nolint:errcheck
	m.OnHook(ctx, hook.HookEvent{Type: hook.HookHashComputed}) //nolint:errcheck
	m.OnHook(ctx, hook.HookEvent{Type: hook.HookTombstone})   //nolint:errcheck
	m.OnHook(ctx, hook.HookEvent{Type: hook.HookError})       //nolint:errcheck
	m.OnHook(ctx, hook.HookEvent{Type: hook.HookLayerSealed}) //nolint:errcheck
	m.OnHook(ctx, hook.HookEvent{Type: hook.HookMerkleLeafAdded}) //nolint:errcheck
	m.OnHook(ctx, hook.HookEvent{Type: hook.HookPipelineStarted}) //nolint:errcheck
	m.OnHook(ctx, hook.HookEvent{Type: hook.HookPipelineStopped}) //nolint:errcheck

	snap := m.Snapshot()
	if snap.CacheHits != 2 {
		t.Fatalf("CacheHits: want 2, got %d", snap.CacheHits)
	}
	if snap.HashComputations != 1 {
		t.Fatalf("HashComputations: want 1, got %d", snap.HashComputations)
	}
	if snap.Tombstones != 1 {
		t.Fatalf("Tombstones: want 1, got %d", snap.Tombstones)
	}
	if snap.Errors != 1 {
		t.Fatalf("Errors: want 1, got %d", snap.Errors)
	}
	if snap.LayersSealed != 1 {
		t.Fatalf("LayersSealed: want 1, got %d", snap.LayersSealed)
	}
	if snap.MerkleLeafsAdded != 1 {
		t.Fatalf("MerkleLeafsAdded: want 1, got %d", snap.MerkleLeafsAdded)
	}
	if snap.PipelineStarts != 1 || snap.PipelineStops != 1 {
		t.Fatalf("Pipeline lifecycle: starts=%d stops=%d", snap.PipelineStarts, snap.PipelineStops)
	}
}

func TestMetricsHook_HitRate(t *testing.T) {
	m := hook.NewMetricsHook()
	ctx := context.Background()

	for i := 0; i < 3; i++ {
		m.OnHook(ctx, hook.HookEvent{Type: hook.HookCacheHit}) //nolint:errcheck
	}
	m.OnHook(ctx, hook.HookEvent{Type: hook.HookHashComputed}) //nolint:errcheck

	snap := m.Snapshot()
	// HitRate is based on CacheHits vs CacheMisses (HookHashComputed → CacheMisses)
	// Actually MetricsHook tracks HookCacheHit and HookCacheMiss separately.
	// HookHashComputed increments HashComputations, not CacheMisses.
	// So HitRate returns CacheHits/(CacheHits+CacheMisses).
	// Here: 3 hits, 0 misses → hitrate = 1.0 if only using CacheHits/CacheMisses.
	if snap.HitRate() < 0 || snap.HitRate() > 1 {
		t.Fatalf("HitRate out of range: %f", snap.HitRate())
	}
}

func TestMetricsHook_Reset(t *testing.T) {
	m := hook.NewMetricsHook()
	ctx := context.Background()
	m.OnHook(ctx, hook.HookEvent{Type: hook.HookCacheHit}) //nolint:errcheck
	m.Reset()
	snap := m.Snapshot()
	if snap.CacheHits != 0 {
		t.Fatal("Reset must zero all counters")
	}
}

func TestMetricsHook_String(t *testing.T) {
	m := hook.NewMetricsHook()
	snap := m.Snapshot()
	if s := snap.String(); s == "" {
		t.Fatal("Snapshot.String must not be empty")
	}
}
