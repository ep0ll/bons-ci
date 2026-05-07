package layermerkle_test

import (
	"context"
	"fmt"
	"log/slog"
	"os"
	"time"

	"github.com/bons/bons-ci/pkg/layermerkle"
	"github.com/bons/bons-ci/pkg/layermerkle/testutil"
)

// ExampleEngine_basicUsage demonstrates the minimal setup to collect file
// access hashes from a single ExecOp vertex with two stacked layers.
func ExampleEngine_basicUsage() {
	// ── 1. Build fake dependencies for the example ──────────────────────────
	hasher := testutil.NewFakeHasher()
	resolver := testutil.NewFakeResolver()
	collector := &testutil.TreeCollector{}

	layerBaseID := testutil.MakeLayerStack("layer-base").Top()
	layerDiffID := testutil.MakeLayerStack("layer-diff").Top()

	// Register file ownership: python3 lives in base, script.py in diff.
	hasher.AddHash("/diffs/base/usr/bin/python3", testutil.MakeFileHash("python3-v3.11"))
	resolver.AddFile("usr/bin/python3", layerBaseID, "/diffs/base")
	hasher.AddHash("/diffs/diff/app/script.py", testutil.MakeFileHash("script-v1"))
	resolver.AddFile("app/script.py", layerDiffID, "/diffs/diff")

	// ── 2. Construct the engine ──────────────────────────────────────────────
	engine := layermerkle.NewEngine(
		layermerkle.WithFileHasher(layermerkle.NewSingleflightHasher(hasher)),
		layermerkle.WithResolver(resolver),
		layermerkle.WithWorkers(2),
		layermerkle.WithCacheCapacity(4096),
		layermerkle.WithOnTree(collector.Collect()),
	)

	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()

	if err := engine.Start(ctx); err != nil {
		fmt.Fprintln(os.Stderr, "Start:", err)
		return
	}

	// ── 3. Submit access events (simulating fanotify events) ─────────────────
	vertexID := testutil.MakeVertexID("exec-op-run-python")
	stack := layermerkle.LayerStack{layerBaseID, layerDiffID}

	events := []*layermerkle.AccessEvent{
		{VertexID: vertexID, LayerStack: stack, RelPath: "usr/bin/python3",
			AbsPath: "/merged/usr/bin/python3", Mask: 0x00001000 /* OPEN_EXEC */, Timestamp: time.Now()},
		{VertexID: vertexID, LayerStack: stack, RelPath: "app/script.py",
			AbsPath: "/merged/app/script.py", Mask: 0x00000001 /* ACCESS */, Timestamp: time.Now()},
		// Duplicate access — must not produce a duplicate leaf.
		{VertexID: vertexID, LayerStack: stack, RelPath: "usr/bin/python3",
			AbsPath: "/merged/usr/bin/python3", Mask: 0x00000020 /* OPEN */, Timestamp: time.Now()},
	}

	result := engine.SubmitBatch(ctx, events)
	_ = result

	// ── 4. Drain workers and finalize ────────────────────────────────────────
	time.Sleep(50 * time.Millisecond)

	tree, err := engine.FinalizeVertex(vertexID)
	if err != nil {
		fmt.Fprintln(os.Stderr, "FinalizeVertex:", err)
		engine.Stop()
		return
	}

	engine.Stop()

	fmt.Printf("leaves=%d root=%s cache_hit=%v\n",
		tree.LeafCount,
		tree.Root.Algorithm(),
		tree.CacheHitCount > 0,
	)
	// Output:
	// leaves=3 root=sha256 cache_hit=true
}

// ExampleEngine_deduplicationAcrossExecOps shows that the same file in a
// shared base layer is hashed only once across two different ExecOps.
func ExampleEngine_deduplicationAcrossExecOps() {
	hasher := testutil.NewFakeHasher()
	resolver := testutil.NewFakeResolver()

	baseLayer := testutil.MakeLayerStack("base").Top()
	resolver.AddFile("lib/libssl.so", baseLayer, "/diffs/base")
	hasher.AddHash("/diffs/base/lib/libssl.so", testutil.MakeFileHash("libssl-content"))

	engine := layermerkle.NewEngine(
		layermerkle.WithFileHasher(layermerkle.NewSingleflightHasher(hasher)),
		layermerkle.WithResolver(resolver),
		layermerkle.WithWorkers(1),
	)
	ctx := context.Background()
	_ = engine.Start(ctx)

	// ExecOp-1 accesses libssl.so.
	op1 := testutil.MakeVertexID("op-1")
	op2 := testutil.MakeVertexID("op-2")
	stack := layermerkle.LayerStack{baseLayer}

	makeEvent := func(vtx layermerkle.VertexID) *layermerkle.AccessEvent {
		return &layermerkle.AccessEvent{
			VertexID: vtx, LayerStack: stack,
			RelPath: "lib/libssl.so", Mask: 0x00000001, Timestamp: time.Now(),
		}
	}
	_ = engine.Submit(ctx, makeEvent(op1))
	time.Sleep(30 * time.Millisecond)
	_ = engine.Submit(ctx, makeEvent(op2))
	time.Sleep(30 * time.Millisecond)

	_, _ = engine.FinalizeVertex(op1)
	_, _ = engine.FinalizeVertex(op2)
	engine.Stop()

	// Hasher should have been called at most once (cache hit for op-2).
	calls := 0
	for _, c := range hasher.Calls() {
		if c == "/diffs/base/lib/libssl.so" {
			calls++
		}
	}
	fmt.Printf("hasher_calls_for_libssl=%d\n", calls)
	// Output:
	// hasher_calls_for_libssl=1
}

// ExampleNewHookChain shows composing multiple hooks for observability.
func ExampleNewHookChain() {
	counting := &layermerkle.CountingHook{}
	logging := layermerkle.NewSlogHook(slog.New(slog.NewTextHandler(os.Stderr, &slog.HandlerOptions{
		Level: slog.LevelDebug,
	})))

	chain := layermerkle.NewHookChain(counting, logging)

	engine := layermerkle.NewEngine(
		layermerkle.WithFileHasher(testutil.NewFakeHasher()),
		layermerkle.WithResolver(testutil.NewFakeResolver()),
		layermerkle.WithHook(chain),
	)
	ctx := context.Background()
	_ = engine.Start(ctx)
	engine.Stop()

	snap := counting.Snapshot()
	fmt.Printf("finalized=%d\n", snap.Finalized)
	// Output:
	// finalized=0
}

// ExampleAccessEventAttrs demonstrates building the fanwatch attribute map
// for use with fanwatch.DynamicAttrTransformer.
func ExampleAccessEventAttrs() {
	vertexID := testutil.MakeVertexID("exec-op-1")
	stack := testutil.MakeLayerStack("layer-base", "layer-app")
	relPath := "usr/bin/python3"

	attrs := layermerkle.AccessEventAttrs(vertexID, stack, relPath)

	fmt.Printf("keys=%d has_vertex=%v has_stack=%v has_path=%v\n",
		len(attrs),
		attrs[layermerkle.AttrVertexID] != "",
		attrs[layermerkle.AttrLayerStack] != "",
		attrs[layermerkle.AttrRelPath] == relPath,
	)
	// Output:
	// keys=3 has_vertex=true has_stack=true has_path=true
}
