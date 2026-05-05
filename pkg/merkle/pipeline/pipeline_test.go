package pipeline_test

import (
	"context"
	"sync"
	"testing"
	"time"

	"github.com/user/layermerkle/dedup"
	"github.com/user/layermerkle/event"
	"github.com/user/layermerkle/hash"
	"github.com/user/layermerkle/hook"
	"github.com/user/layermerkle/layer"
	"github.com/user/layermerkle/pipeline"
)

// ─── helpers ─────────────────────────────────────────────────────────────────

func mkEvent(path string, stack layer.Stack, at event.AccessType) *event.FileAccessEvent {
	top, _ := stack.Top()
	return &event.FileAccessEvent{
		FilePath:     path,
		LayerStack:   stack,
		VertexDigest: "vtx:" + string(top),
		AccessType:   at,
		Timestamp:    time.Now(),
	}
}

// newTestPipeline creates a pipeline with sensible test defaults.
// opts are appended AFTER the defaults so callers can override workers, etc.
func newTestPipeline(t *testing.T, opts ...pipeline.Option) *pipeline.Pipeline {
	t.Helper()
	base := []pipeline.Option{
		pipeline.WithHashProvider(hash.NewSyntheticProvider()),
		pipeline.WithWorkers(2),
		pipeline.WithBufferSize(64),
		pipeline.WithResultBuffer(128),
	}
	p, err := pipeline.New(append(base, opts...)...)
	if err != nil {
		t.Fatalf("pipeline.New: %v", err)
	}
	return p
}

// runBatch sends all events to p, closes the channel, and blocks until Run
// returns. This models one ExecOp: all events are fully processed before the
// function returns, guaranteeing that any cache entries from this batch are
// visible to subsequent batches.
func runBatch(t *testing.T, p *pipeline.Pipeline, events ...*event.FileAccessEvent) {
	t.Helper()
	ctx := context.Background()
	ch := make(chan *event.FileAccessEvent, len(events)+1)
	for _, ev := range events {
		ch <- ev
	}
	close(ch)
	if err := p.Run(ctx, ch); err != nil {
		t.Fatalf("pipeline.Run: %v", err)
	}
}

// ─── Construction ────────────────────────────────────────────────────────────

func TestNew_MissingProvider(t *testing.T) {
	_, err := pipeline.New()
	if err == nil {
		t.Fatal("expected error when HashProvider is missing")
	}
}

func TestNew_InvalidWorkers(t *testing.T) {
	_, err := pipeline.New(
		pipeline.WithHashProvider(hash.NewSyntheticProvider()),
		pipeline.WithWorkers(0),
	)
	if err == nil {
		t.Fatal("expected error for workers=0")
	}
}

func TestNew_InvalidBuffer(t *testing.T) {
	_, err := pipeline.New(
		pipeline.WithHashProvider(hash.NewSyntheticProvider()),
		pipeline.WithBufferSize(-1),
	)
	if err == nil {
		t.Fatal("expected error for negative buffer")
	}
}

func TestNew_InvalidResultBuffer(t *testing.T) {
	_, err := pipeline.New(
		pipeline.WithHashProvider(hash.NewSyntheticProvider()),
		pipeline.WithResultBuffer(-1),
	)
	if err == nil {
		t.Fatal("expected error for negative result buffer")
	}
}

// ─── Basic processing ────────────────────────────────────────────────────────

func TestBasicReadProcessed(t *testing.T) {
	rec := hook.NewRecordingHook()
	p := newTestPipeline(t, pipeline.WithHook(rec))
	l := layer.Digest("l0")
	runBatch(t, p, mkEvent("/bin/sh", layer.MustNew(l), event.AccessRead))

	total := rec.CountByType(hook.HookCacheHit) + rec.CountByType(hook.HookHashComputed)
	if total != 1 {
		t.Fatalf("expected 1 processed event, got %d", total)
	}
}

func TestDuplicateReadDeduped(t *testing.T) {
	rec := hook.NewRecordingHook()
	// Single worker guarantees FIFO processing so the second read always
	// sees the cache entry from the first.
	p := newTestPipeline(t, pipeline.WithHook(rec), pipeline.WithWorkers(1))
	l := layer.Digest("ldup")
	stack := layer.MustNew(l)

	events := make([]*event.FileAccessEvent, 10)
	for i := range events {
		events[i] = mkEvent("/bin/sh", stack, event.AccessRead)
	}
	runBatch(t, p, events...)

	if got := rec.CountByType(hook.HookHashComputed); got != 1 {
		t.Fatalf("expected exactly 1 hash computation for 10 duplicate reads, got %d", got)
	}
	if got := rec.CountByType(hook.HookCacheHit); got != 9 {
		t.Fatalf("expected 9 cache hits, got %d", got)
	}
}

// TestCrossLayerPromotion verifies that a file hashed in the base layer is
// promoted (not recomputed) when a higher-layer ExecOp reads it.
//
// Correctness requires sequential ordering: the base ExecOp must complete
// fully before the exec1 events are dispatched. We achieve this by running
// two separate batches through the SAME pipeline instance, which shares its
// cache between runs. Single-worker mode guarantees deterministic ordering
// within each batch.
func TestCrossLayerPromotion(t *testing.T) {
	rec := hook.NewRecordingHook()
	p := newTestPipeline(t,
		pipeline.WithHook(rec),
		pipeline.WithWorkers(1), // deterministic FIFO within each batch
	)

	base  := layer.Digest("base-cross")
	exec1 := layer.Digest("exec1-cross")

	// Batch 1: ExecOp-1 seeds the base layer cache.
	runBatch(t, p, mkEvent("/etc/passwd", layer.MustNew(base), event.AccessRead))

	// Batch 2: ExecOp-2 reads the same file through the full stack.
	// The engine must find the cached base entry and promote it — no recompute.
	rec.Reset()
	runBatch(t, p, mkEvent("/etc/passwd", layer.MustNew(base, exec1), event.AccessRead))

	if got := rec.CountByType(hook.HookHashComputed); got != 0 {
		t.Fatalf("cross-layer: expected 0 recomputations after promotion, got %d", got)
	}
	if got := rec.CountByType(hook.HookCacheHit); got != 1 {
		t.Fatalf("cross-layer: expected 1 cache hit (promotion), got %d", got)
	}
}

func TestTombstonePropagates(t *testing.T) {
	rec := hook.NewRecordingHook()
	p := newTestPipeline(t, pipeline.WithHook(rec), pipeline.WithWorkers(1))

	base  := layer.Digest("base-tomb")
	exec1 := layer.Digest("exec1-tomb")
	exec2 := layer.Digest("exec2-tomb")

	// ExecOp-1: seed base, exec1 deletes the file.
	runBatch(t, p,
		mkEvent("/etc/passwd", layer.MustNew(base), event.AccessRead),
		mkEvent("/etc/passwd", layer.MustNew(base, exec1), event.AccessDelete),
	)

	// ExecOp-2: exec2 reads the file through the full stack — must get tombstone.
	rec.Reset()
	runBatch(t, p, mkEvent("/etc/passwd", layer.MustNew(base, exec1, exec2), event.AccessRead))

	if got := rec.CountByType(hook.HookTombstone); got != 1 {
		t.Fatalf("expected 1 tombstone event, got %d", got)
	}
	if got := rec.CountByType(hook.HookCacheHit); got != 0 {
		t.Fatalf("expected 0 cache hits (tombstone should block), got %d", got)
	}
}

// ─── Multi-run (same pipeline, different ExecOps) ────────────────────────────

func TestMultipleRunsShareCache(t *testing.T) {
	p := newTestPipeline(t, pipeline.WithWorkers(1))
	l := layer.Digest("lmulti")

	// Run 1: compute the hash.
	runBatch(t, p, mkEvent("/shared", layer.MustNew(l), event.AccessRead))

	// Run 2: must be a cache hit — no re-computation.
	rec := hook.NewRecordingHook()
	// Re-create pipeline option to add recorder — we can't add hooks after New.
	// Instead just check stats: misses must stay at 1 total across both runs.
	runBatch(t, p, mkEvent("/shared", layer.MustNew(l), event.AccessRead))

	stats := p.Stats()
	if stats.CacheMisses != 1 {
		t.Fatalf("expected 1 total cache miss across two runs, got %d", stats.CacheMisses)
	}
	if stats.CacheHits < 1 {
		t.Fatalf("expected at least 1 cache hit on second run, got %d", stats.CacheHits)
	}
	_ = rec
}

// ─── Seal / Merkle ───────────────────────────────────────────────────────────

func TestSealReturnsRoot(t *testing.T) {
	p := newTestPipeline(t)
	ctx := context.Background()
	l := layer.Digest("lseal")

	runBatch(t, p,
		mkEvent("/a", layer.MustNew(l), event.AccessRead),
		mkEvent("/b", layer.MustNew(l), event.AccessCreate),
	)

	root, err := p.Seal(ctx, l)
	if err != nil {
		t.Fatalf("Seal: %v", err)
	}
	if len(root) != 32 {
		t.Fatalf("expected 32-byte root, got %d bytes", len(root))
	}
}

func TestSealIdempotent(t *testing.T) {
	p := newTestPipeline(t)
	ctx := context.Background()
	l := layer.Digest("lsealid")
	runBatch(t, p, mkEvent("/a", layer.MustNew(l), event.AccessRead))

	r1, _ := p.Seal(ctx, l)
	r2, _ := p.Seal(ctx, l)
	if string(r1) != string(r2) {
		t.Fatal("Seal must be idempotent")
	}
}

func TestSealAllCoversAllLayers(t *testing.T) {
	p := newTestPipeline(t)
	ctx := context.Background()
	layers := []layer.Digest{"la", "lb", "lc"}
	for _, d := range layers {
		runBatch(t, p, mkEvent("/file", layer.MustNew(d), event.AccessRead))
	}
	roots := p.SealAll(ctx)
	for _, d := range layers {
		if roots[d] == nil {
			t.Fatalf("SealAll missing root for layer %s", d)
		}
	}
}

func TestProofValid(t *testing.T) {
	p := newTestPipeline(t)
	ctx := context.Background()
	l := layer.Digest("lproof")
	runBatch(t, p,
		mkEvent("/x", layer.MustNew(l), event.AccessRead),
		mkEvent("/y", layer.MustNew(l), event.AccessCreate),
		mkEvent("/z", layer.MustNew(l), event.AccessWrite),
	)
	p.Seal(ctx, l)

	for _, path := range []string{"/x", "/y", "/z"} {
		proof, err := p.Proof(l, path)
		if err != nil {
			t.Fatalf("Proof(%q): %v", path, err)
		}
		if err := proof.Verify(); err != nil {
			t.Fatalf("Proof.Verify(%q): %v", path, err)
		}
	}
}

func TestLeavesAfterSeal(t *testing.T) {
	p := newTestPipeline(t)
	ctx := context.Background()
	l := layer.Digest("lleaves")
	runBatch(t, p,
		mkEvent("/a", layer.MustNew(l), event.AccessRead),
		mkEvent("/b", layer.MustNew(l), event.AccessCreate),
	)
	p.Seal(ctx, l)

	leaves, err := p.Leaves(l)
	if err != nil {
		t.Fatalf("Leaves: %v", err)
	}
	if len(leaves) != 2 {
		t.Fatalf("expected 2 leaves, got %d", len(leaves))
	}
}

// ─── Result channel ──────────────────────────────────────────────────────────

func TestResultChannelReceivesResults(t *testing.T) {
	p := newTestPipeline(t)
	l := layer.Digest("lres")
	ctx := context.Background()

	ch := make(chan *event.FileAccessEvent, 3)
	ch <- mkEvent("/a", layer.MustNew(l), event.AccessRead)
	ch <- mkEvent("/b", layer.MustNew(l), event.AccessCreate)
	ch <- mkEvent("/c", layer.MustNew(l), event.AccessDelete)
	close(ch)

	var wg sync.WaitGroup
	wg.Add(1)
	go func() { defer wg.Done(); p.Run(ctx, ch) }()

	var results []dedup.Result
	resCh := p.Results()
	if resCh == nil {
		t.Fatal("Results() must return non-nil channel when resultBuffer > 0")
	}

	wg.Wait()
	p.Close() // close the result channel so we can drain it

	for r := range resCh {
		results = append(results, r)
	}
	if len(results) != 3 {
		t.Fatalf("expected 3 results, got %d", len(results))
	}
}

func TestResultChannelNilWhenDisabled(t *testing.T) {
	p, err := pipeline.New(
		pipeline.WithHashProvider(hash.NewSyntheticProvider()),
		pipeline.WithResultBuffer(0),
	)
	if err != nil {
		t.Fatalf("New: %v", err)
	}
	if p.Results() != nil {
		t.Fatal("Results() must be nil when resultBuffer=0")
	}
}

func TestCloseIdempotent(t *testing.T) {
	p := newTestPipeline(t)
	// Calling Close multiple times must not panic.
	p.Close()
	p.Close()
	p.Close()
}

// ─── Hooks ───────────────────────────────────────────────────────────────────

func TestMerkleLeafAddedHookFires(t *testing.T) {
	rec := hook.NewRecordingHook()
	p := newTestPipeline(t, pipeline.WithHook(rec))
	l := layer.Digest("lmerkle")
	runBatch(t, p, mkEvent("/a", layer.MustNew(l), event.AccessRead))

	if got := rec.CountByType(hook.HookMerkleLeafAdded); got < 1 {
		t.Fatalf("HookMerkleLeafAdded: expected >= 1, got %d", got)
	}
}

func TestHookFiringOrder(t *testing.T) {
	var order []string
	var mu sync.Mutex

	makeHook := func(name string) hook.Hook {
		return hook.HookFunc(func(_ context.Context, e hook.HookEvent) error {
			if e.Type == hook.HookHashComputed || e.Type == hook.HookCacheHit {
				mu.Lock()
				order = append(order, name)
				mu.Unlock()
			}
			return nil
		})
	}

	p, err := pipeline.New(
		pipeline.WithHashProvider(hash.NewSyntheticProvider()),
		pipeline.WithWorkers(1),
		pipeline.WithBufferSize(8),
		pipeline.WithHook(makeHook("first")),
		pipeline.WithHook(makeHook("second")),
	)
	if err != nil {
		t.Fatalf("New: %v", err)
	}

	l := layer.Digest("lhook")
	runBatch(t, p, mkEvent("/f", layer.MustNew(l), event.AccessRead))

	mu.Lock()
	defer mu.Unlock()
	if len(order) < 2 {
		t.Fatalf("expected both hooks to fire, got: %v", order)
	}
	if order[0] != "first" || order[1] != "second" {
		t.Fatalf("expected [first second], got %v", order)
	}
}

// ─── Stats ───────────────────────────────────────────────────────────────────

func TestStats(t *testing.T) {
	p := newTestPipeline(t, pipeline.WithWorkers(1))
	l := layer.Digest("lstats")

	runBatch(t, p,
		mkEvent("/a", layer.MustNew(l), event.AccessRead),
		mkEvent("/b", layer.MustNew(l), event.AccessRead),
		mkEvent("/a", layer.MustNew(l), event.AccessRead), // duplicate → cache hit
		mkEvent("/c", layer.MustNew(l), event.AccessDelete),
	)

	stats := p.Stats()
	if stats.EventsReceived != 4 {
		t.Fatalf("EventsReceived: expected 4, got %d", stats.EventsReceived)
	}
	if stats.EventsProcessed != 4 {
		t.Fatalf("EventsProcessed: expected 4, got %d", stats.EventsProcessed)
	}
	if stats.CacheMisses < 1 {
		t.Fatal("expected at least 1 cache miss")
	}
	if stats.Tombstones != 1 {
		t.Fatalf("Tombstones: expected 1, got %d", stats.Tombstones)
	}
}

// ─── Context cancellation ────────────────────────────────────────────────────

func TestContextCancellation(t *testing.T) {
	p := newTestPipeline(t)
	ch := make(chan *event.FileAccessEvent) // never sends

	ctx, cancel := context.WithTimeout(context.Background(), 20*time.Millisecond)
	defer cancel()

	err := p.Run(ctx, ch)
	if err == nil {
		t.Fatal("expected error on context cancellation")
	}
}
