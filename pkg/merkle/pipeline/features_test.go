package pipeline_test

import (
	"context"
	"testing"
	"time"

	"github.com/user/layermerkle/event"
	"github.com/user/layermerkle/hash"
	"github.com/user/layermerkle/hook"
	"github.com/user/layermerkle/layer"
	"github.com/user/layermerkle/pipeline"
)

// ─── Submit batch API ────────────────────────────────────────────────────────

func TestSubmit_ProcessesAllEvents(t *testing.T) {
	rec := hook.NewRecordingHook()
	p := newTestPipeline(t, pipeline.WithHook(rec))
	ctx := context.Background()
	l := layer.Digest("lsubmit")
	stack := layer.MustNew(l)

	batch := []*event.FileAccessEvent{
		{FilePath: "/a", LayerStack: stack, AccessType: event.AccessRead, Timestamp: time.Now()},
		{FilePath: "/b", LayerStack: stack, AccessType: event.AccessCreate, Timestamp: time.Now()},
		{FilePath: "/c", LayerStack: stack, AccessType: event.AccessDelete, Timestamp: time.Now()},
	}
	p.Submit(ctx, batch)

	total := rec.CountByType(hook.HookHashComputed) +
		rec.CountByType(hook.HookCacheHit) +
		rec.CountByType(hook.HookTombstone)
	if total != 3 {
		t.Fatalf("Submit: expected 3 processed events, got %d", total)
	}
}

func TestSubmit_DeduplicatesCache(t *testing.T) {
	rec := hook.NewRecordingHook()
	p, err := pipeline.New(
		pipeline.WithHashProvider(hash.NewSyntheticProvider()),
		pipeline.WithWorkers(1),
		pipeline.WithResultBuffer(0),
		pipeline.WithHook(rec),
	)
	if err != nil {
		t.Fatal(err)
	}
	ctx := context.Background()
	l := layer.Digest("lsubdup")
	stack := layer.MustNew(l)

	// First Submit: computes hash for /f.
	p.Submit(ctx, []*event.FileAccessEvent{
		{FilePath: "/f", LayerStack: stack, AccessType: event.AccessRead, Timestamp: time.Now()},
	})
	// Second Submit: must be a cache hit.
	rec.Reset()
	p.Submit(ctx, []*event.FileAccessEvent{
		{FilePath: "/f", LayerStack: stack, AccessType: event.AccessRead, Timestamp: time.Now()},
	})

	if got := rec.CountByType(hook.HookCacheHit); got != 1 {
		t.Fatalf("second Submit: expected 1 cache hit, got %d", got)
	}
	if got := rec.CountByType(hook.HookHashComputed); got != 0 {
		t.Fatalf("second Submit: expected 0 hash computations, got %d", got)
	}
}

func TestSubmit_StatsUpdated(t *testing.T) {
	p := newTestPipeline(t)
	ctx := context.Background()
	l := layer.Digest("lstats2")
	stack := layer.MustNew(l)

	p.Submit(ctx, []*event.FileAccessEvent{
		{FilePath: "/x", LayerStack: stack, AccessType: event.AccessRead, Timestamp: time.Now()},
		{FilePath: "/y", LayerStack: stack, AccessType: event.AccessRead, Timestamp: time.Now()},
	})
	stats := p.Stats()
	if stats.EventsReceived != 2 {
		t.Fatalf("EventsReceived: expected 2, got %d", stats.EventsReceived)
	}
}

// ─── CrossLayerProof ─────────────────────────────────────────────────────────

func TestCrossLayerProof_Valid(t *testing.T) {
	p := newTestPipeline(t, pipeline.WithWorkers(1))
	ctx := context.Background()

	base  := layer.Digest("base-clp")
	exec1 := layer.Digest("exec1-clp")
	exec2 := layer.Digest("exec2-clp")
	stack := layer.MustNew(base, exec1, exec2)

	// Seed base, then promote via ancestor walk for exec1 and exec2.
	runBatch(t, p, mkEvent("/shared", layer.MustNew(base), event.AccessRead))
	runBatch(t, p, mkEvent("/shared", layer.MustNew(base, exec1), event.AccessRead))
	runBatch(t, p, mkEvent("/shared", stack, event.AccessRead))

	// Seal all layers.
	p.SealAll(ctx)

	proofs, err := p.CrossLayerProof(stack, "/shared")
	if err != nil {
		t.Fatalf("CrossLayerProof: %v", err)
	}
	if len(proofs) < 2 {
		t.Fatalf("expected proofs for at least 2 layers, got %d", len(proofs))
	}
	for _, proof := range proofs {
		if err := proof.Verify(); err != nil {
			t.Fatalf("CrossLayerProof.Verify: %v", err)
		}
	}
}

func TestCrossLayerProof_FileNotInAnyLayer(t *testing.T) {
	p := newTestPipeline(t)
	ctx := context.Background()
	l := layer.Digest("lclp-none")
	runBatch(t, p, mkEvent("/exists", layer.MustNew(l), event.AccessRead))
	p.SealAll(ctx)

	stack := layer.MustNew(l)
	_, err := p.CrossLayerProof(stack, "/does-not-exist")
	if err == nil {
		t.Fatal("CrossLayerProof: expected error for unknown file")
	}
}

func TestCrossLayerProof_TombstonedFileOmitted(t *testing.T) {
	p := newTestPipeline(t, pipeline.WithWorkers(1))
	ctx := context.Background()

	base  := layer.Digest("base-tomb-clp")
	exec1 := layer.Digest("exec1-tomb-clp")

	// base has the file, exec1 deletes it.
	runBatch(t, p, mkEvent("/del", layer.MustNew(base), event.AccessRead))
	runBatch(t, p, mkEvent("/del", layer.MustNew(base, exec1), event.AccessDelete))
	p.SealAll(ctx)

	// Only base should have a proof; exec1 has tombstone.
	stack := layer.MustNew(base, exec1)
	proofs, err := p.CrossLayerProof(stack, "/del")
	if err != nil {
		t.Fatalf("CrossLayerProof: %v", err)
	}
	// Should find 1 proof (base only).
	if len(proofs) != 1 {
		t.Fatalf("expected 1 proof (base only, exec1 deleted), got %d", len(proofs))
	}
	if err := proofs[0].Verify(); err != nil {
		t.Fatalf("proof.Verify: %v", err)
	}
}
