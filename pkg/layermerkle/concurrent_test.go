package layermerkle_test

import (
	"context"
	"sync"
	"testing"
	"time"

	"github.com/bons/bons-ci/pkg/layermerkle"
	"github.com/bons/bons-ci/pkg/layermerkle/testutil"
)

// TestVertexProcessor_ConcurrentFinalizeVertex_OnlyOneSucceeds verifies that
// when two goroutines call FinalizeVertex for the same vertex simultaneously,
// exactly one succeeds and one gets ErrVertexClosed.
func TestVertexProcessor_ConcurrentFinalizeVertex_OnlyOneSucceeds(t *testing.T) {
	resolver := testutil.NewFakeResolver()
	layerID := testutil.MakeLayerStack("l").Top()
	resolver.AddFile("bin/sh", layerID, "/diffs/l")

	engine := layermerkle.NewEngine(
		layermerkle.WithFileHasher(testutil.NewFakeHasher()),
		layermerkle.WithResolver(resolver),
		layermerkle.WithWorkers(1),
	)

	ctx := context.Background()
	_ = engine.Start(ctx)

	vertexID := testutil.MakeVertexID("concurrent-vertex")
	ev := testutil.NewAccessEvent().
		WithVertexDigest(vertexID).
		WithLayerStack("l").
		WithRelPath("bin/sh").
		Build()

	_ = engine.Submit(ctx, ev)
	time.Sleep(30 * time.Millisecond)

	var (
		wg      sync.WaitGroup
		success int
		errored int
		mu      sync.Mutex
	)

	for range 5 {
		wg.Add(1)
		go func() {
			defer wg.Done()
			_, err := engine.FinalizeVertex(vertexID)
			mu.Lock()
			if err == nil {
				success++
			} else {
				errored++
			}
			mu.Unlock()
		}()
	}
	wg.Wait()
	engine.Stop()

	if success != 1 {
		t.Errorf("exactly one FinalizeVertex should succeed, got %d successes", success)
	}
	if errored != 4 {
		t.Errorf("remaining 4 calls should error, got %d errors", errored)
	}
}

// TestEngine_HighConcurrency_NoRaceConditions runs many goroutines submitting
// events and finalizing vertices simultaneously to catch races under -race.
func TestEngine_HighConcurrency_NoRaceConditions(t *testing.T) {
	resolver := testutil.NewFakeResolver()
	layerID := testutil.MakeLayerStack("base").Top()
	for i := range 50 {
		resolver.AddFile(
			testutil.NewAccessEvent().WithRelPath(t.Name()).Build().RelPath[:1]+string(rune('a'+i%26)),
			layerID, "/diffs/base",
		)
	}

	engine := layermerkle.NewEngine(
		layermerkle.WithFileHasher(testutil.NewFakeHasher()),
		layermerkle.WithResolver(resolver),
		layermerkle.WithWorkers(4),
		layermerkle.WithEventBufferSize(512),
	)
	ctx := context.Background()
	_ = engine.Start(ctx)

	const numVertices = 10
	var wg sync.WaitGroup

	for v := range numVertices {
		wg.Add(1)
		go func(vtxIndex int) {
			defer wg.Done()
			vtxID := testutil.MakeVertexID(t.Name() + string(rune('A'+vtxIndex)))
			for j := range 5 {
				ev := testutil.NewAccessEvent().
					WithVertexDigest(vtxID).
					WithLayerStack("base").
					WithRelPath("lib/file" + string(rune('a'+j))).
					Build()
				_ = engine.Submit(ctx, ev)
			}
			time.Sleep(20 * time.Millisecond)
			engine.FinalizeVertex(vtxID) //nolint:errcheck
		}(v)
	}

	wg.Wait()
	engine.Stop()
}

// TestEngine_SubmitBatch_DropsWriteEvents verifies SubmitBatch skips non-read events.
func TestEngine_SubmitBatch_DropsWriteEvents(t *testing.T) {
	engine := layermerkle.NewEngine(
		layermerkle.WithFileHasher(testutil.NewFakeHasher()),
		layermerkle.WithResolver(testutil.NewFakeResolver()),
	)
	ctx := context.Background()
	_ = engine.Start(ctx)
	defer engine.Stop()

	events := []*layermerkle.AccessEvent{
		testutil.NewAccessEvent().WithMask(0x00000001).Build(), // ACCESS — read
		testutil.NewAccessEvent().WithMask(0x00000002).Build(), // MODIFY — write
		testutil.NewAccessEvent().WithMask(0x00000020).Build(), // OPEN — read
		testutil.NewAccessEvent().WithMask(0x00000100).Build(), // CREATE — write
	}

	result := engine.SubmitBatch(ctx, events)
	if result.Submitted != 2 {
		t.Errorf("Submitted = %d, want 2 (read events only)", result.Submitted)
	}
	if result.Dropped != 2 {
		t.Errorf("Dropped = %d, want 2 (write events)", result.Dropped)
	}
}

// TestHardLinkAwareHasher_InterfaceCompliance verifies HardLinkAwareHasher
// satisfies the FileHasher interface.
func TestHardLinkAwareHasher_InterfaceCompliance(t *testing.T) {
	var _ layermerkle.FileHasher = layermerkle.NewHardLinkAwareHasher(testutil.NewFakeHasher())
}

// TestSlogHook_InterfaceCompliance verifies SlogHook satisfies EngineHook.
func TestSlogHook_InterfaceCompliance(t *testing.T) {
	var _ layermerkle.EngineHook = layermerkle.NewSlogHook(nil)
}

// TestEngine_HookChain_AllHooksReceiveEvents verifies HookChain fans out to
// all registered hooks.
func TestEngine_HookChain_AllHooksReceiveEvents(t *testing.T) {
	h1 := &layermerkle.CountingHook{}
	h2 := &layermerkle.CountingHook{}
	chain := layermerkle.NewHookChain(h1, h2)

	ctx := context.Background()
	chain.OnHashStart(ctx, layermerkle.HashRequest{})
	chain.OnHashStart(ctx, layermerkle.HashRequest{})

	if h1.Snapshot().HashStarts != 2 {
		t.Errorf("h1.HashStarts = %d, want 2", h1.Snapshot().HashStarts)
	}
	if h2.Snapshot().HashStarts != 2 {
		t.Errorf("h2.HashStarts = %d, want 2", h2.Snapshot().HashStarts)
	}
}

// TestEngine_CacheStats_AfterActivity verifies stats update after real activity.
func TestEngine_CacheStats_AfterActivity(t *testing.T) {
	resolver := testutil.NewFakeResolver()
	layerID := testutil.MakeLayerStack("stats-layer").Top()
	resolver.AddFile("bin/ls", layerID, "/diffs")

	engine := layermerkle.NewEngine(
		layermerkle.WithFileHasher(testutil.NewFakeHasher()),
		layermerkle.WithResolver(resolver),
		layermerkle.WithWorkers(1),
	)
	ctx := context.Background()
	_ = engine.Start(ctx)

	// Submit same event twice — second should be a cache hit.
	ev := testutil.NewAccessEvent().WithLayerStack("stats-layer").WithRelPath("bin/ls").Build()
	_ = engine.Submit(ctx, ev)
	_ = engine.Submit(ctx, ev)
	time.Sleep(60 * time.Millisecond)

	stats := engine.CacheStats()
	if stats.Hits+stats.Misses == 0 {
		t.Error("cache should have recorded activity")
	}
	engine.Stop()
}
