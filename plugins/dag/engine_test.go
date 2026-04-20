package reactdag_test

import (
	"context"
	"strings"
	"testing"
	"time"

	dag "github.com/bons/bons-ci/plugins/dag"
)

// =========================================================================
// Engine tests
// =========================================================================

func TestEngine_Build_CleanSuccess(t *testing.T) {
	d, _ := buildLinearDAG(t)
	eng, err := dag.NewEngine(d, dag.EngineConfig{WorkerCount: 2})
	if err != nil {
		t.Fatalf("NewEngine: %v", err)
	}
	ctx := context.Background()
	eng.Start(ctx)
	defer eng.Stop()

	result := eng.Build(ctx, "A", nil)
	if result.Error != nil {
		t.Fatalf("Build: %v", result.Error)
	}
	if result.Metrics.Executed != 3 {
		t.Errorf("Executed=%d; want 3", result.Metrics.Executed)
	}
	if result.Duration <= 0 {
		t.Error("Duration should be positive")
	}
}

func TestEngine_Build_CachedOnSecondRun(t *testing.T) {
	d, _ := buildLinearDAG(t)
	eng, err := dag.NewEngine(d, dag.EngineConfig{WorkerCount: 2})
	if err != nil {
		t.Fatalf("NewEngine: %v", err)
	}
	ctx := context.Background()
	eng.Start(ctx)
	defer eng.Stop()

	eng.Build(ctx, "A", nil)
	eng.ResetAll()
	r2 := eng.Build(ctx, "A", nil)

	if r2.Metrics.FastCacheHits != 3 {
		t.Errorf("build 2: FastCacheHits=%d; want 3", r2.Metrics.FastCacheHits)
	}
	if r2.Metrics.Executed != 0 {
		t.Errorf("build 2: Executed=%d; want 0", r2.Metrics.Executed)
	}
}

func TestEngine_History_TracksBuilds(t *testing.T) {
	d, _ := buildLinearDAG(t)
	eng, _ := dag.NewEngine(d, dag.EngineConfig{WorkerCount: 2})
	ctx := context.Background()
	eng.Start(ctx)
	defer eng.Stop()

	for range 3 {
		eng.Build(ctx, "A", nil)
		eng.ResetAll()
	}

	stats := eng.History().Trend("A", 10)
	if stats.Count != 3 {
		t.Errorf("history count=%d; want 3", stats.Count)
	}
	if stats.PassRate != 1.0 {
		t.Errorf("pass rate=%.2f; want 1.0", stats.PassRate)
	}
}

func TestEngine_Exporter_ProducesPrometheusOutput(t *testing.T) {
	d, _ := buildLinearDAG(t)
	eng, _ := dag.NewEngine(d, dag.EngineConfig{
		WorkerCount:      2,
		PrometheusLabels: map[string]string{"env": "test"},
	})
	ctx := context.Background()
	eng.Start(ctx)
	defer eng.Stop()

	eng.Build(ctx, "A", nil)

	var sb strings.Builder
	eng.WriteMetrics(&sb)
	out := sb.String()

	if !strings.Contains(out, "reactdag_builds_total") {
		t.Errorf("missing reactdag_builds_total: %s", out)
	}
	if !strings.Contains(out, `env="test"`) {
		t.Errorf("missing const label: %s", out)
	}
}

func TestEngine_Plan_DryRun(t *testing.T) {
	d, _ := buildLinearDAG(t)
	eng, _ := dag.NewEngine(d, dag.EngineConfig{WorkerCount: 2})
	ctx := context.Background()

	plan, err := eng.Plan(ctx, "A", nil)
	if err != nil {
		t.Fatalf("Plan: %v", err)
	}
	if plan.WillExecute != 3 {
		t.Errorf("WillExecute=%d; want 3 (clean DAG)", plan.WillExecute)
	}
}

func TestEngine_Analyse_ReturnsMetrics(t *testing.T) {
	d, _ := buildLinearDAG(t)
	eng, _ := dag.NewEngine(d, dag.EngineConfig{WorkerCount: 2})

	a, err := eng.Analyse()
	if err != nil {
		t.Fatalf("Analyse: %v", err)
	}
	if a.VertexCount != 3 {
		t.Errorf("VertexCount=%d; want 3", a.VertexCount)
	}
}

func TestEngine_Snapshot_And_Diff(t *testing.T) {
	d, _ := buildLinearDAG(t)
	eng, _ := dag.NewEngine(d, dag.EngineConfig{WorkerCount: 2})
	ctx := context.Background()
	eng.Start(ctx)
	defer eng.Stop()

	snap1 := eng.Snapshot()
	eng.Build(ctx, "A", nil)
	snap2 := eng.Snapshot()

	diffs := dag.Diff(snap1, snap2)
	if len(diffs) == 0 {
		t.Error("expected diffs between pre and post build snapshots")
	}
}

func TestEngine_ExportDOT_ContainsVertices(t *testing.T) {
	d, _ := buildLinearDAG(t)
	eng, _ := dag.NewEngine(d, dag.EngineConfig{})

	dot := eng.ExportDOT("test-build")
	for _, id := range []string{"A", "B", "C"} {
		if !strings.Contains(dot, id) {
			t.Errorf("DOT missing vertex %s", id)
		}
	}
}

func TestEngine_DetectStalls_ReturnEmpty_WhenDone(t *testing.T) {
	d, _ := buildLinearDAG(t)
	eng, _ := dag.NewEngine(d, dag.EngineConfig{WorkerCount: 2})
	ctx := context.Background()
	eng.Start(ctx)
	defer eng.Stop()

	eng.Build(ctx, "A", nil)
	stalls := eng.DetectStalls(0)
	if len(stalls) != 0 {
		t.Errorf("expected no stalls after build completion; got %v", stalls)
	}
}

func TestEngine_Observe_ReceivesEvents(t *testing.T) {
	d, _ := buildLinearDAG(t)
	eng, _ := dag.NewEngine(d, dag.EngineConfig{WorkerCount: 2})
	ctx := context.Background()
	eng.Start(ctx)
	defer eng.Stop()

	obs := eng.Observe(dag.WithBufferSize(32))
	defer obs.Unsubscribe()

	var count int
	done := make(chan struct{})
	go func() {
		defer close(done)
		for range obs.Events() {
			count++
		}
	}()

	eng.Build(ctx, "A", nil)
	obs.Unsubscribe()
	<-done

	if count == 0 {
		t.Error("Observer received no events from Engine")
	}
}

func TestEngine_DiskCache_PersistsAcrossInstances(t *testing.T) {
	d, _ := buildLinearDAG(t)
	cacheDir := t.TempDir()

	// Instance 1: build and populate disk cache.
	eng1, err := dag.NewEngine(d, dag.EngineConfig{
		WorkerCount: 2,
		CacheDir:    cacheDir,
	})
	if err != nil {
		t.Fatalf("NewEngine 1: %v", err)
	}
	ctx := context.Background()
	eng1.Start(ctx)
	eng1.Build(ctx, "A", nil)
	eng1.Stop()

	// Reset vertex states (simulating process restart).
	for _, v := range d.All() {
		v.Reset()
	}

	// Instance 2: same disk cache, all should be slow-cache hits.
	eng2, err := dag.NewEngine(d, dag.EngineConfig{
		WorkerCount: 2,
		CacheDir:    cacheDir,
	})
	if err != nil {
		t.Fatalf("NewEngine 2: %v", err)
	}
	eng2.Start(ctx)
	defer eng2.Stop()

	r2 := eng2.Build(ctx, "A", nil)
	if r2.Metrics.SlowCacheHits == 0 {
		t.Errorf("expected slow cache hits from disk; got %d", r2.Metrics.SlowCacheHits)
	}
	if r2.Metrics.Executed != 0 {
		t.Errorf("expected 0 executions (all disk cached); got %d", r2.Metrics.Executed)
	}
}

func TestEngine_JSONLog_WritesStructuredOutput(t *testing.T) {
	d, _ := buildLinearDAG(t)
	var buf strings.Builder
	eng, _ := dag.NewEngine(d, dag.EngineConfig{
		WorkerCount:   2,
		JSONLogOutput: &buf,
	})
	ctx := context.Background()
	eng.Start(ctx)
	defer eng.Stop()

	eng.Build(ctx, "A", nil)

	out := buf.String()
	if !strings.Contains(out, `"event"`) {
		t.Errorf("JSON log missing event field: %s", out)
	}
	if !strings.Contains(out, `"ts"`) {
		t.Errorf("JSON log missing ts field: %s", out)
	}
}

func TestEngine_PruneInterval_RemovesExpiredEntries(t *testing.T) {
	d, _ := buildLinearDAG(t)
	eng, _ := dag.NewEngine(d, dag.EngineConfig{
		WorkerCount:   2,
		PruneInterval: 20 * time.Millisecond,
		PruneMaxAge:   5 * time.Millisecond,
	})
	ctx, cancel := context.WithTimeout(context.Background(), 500*time.Millisecond)
	defer cancel()

	eng.Start(ctx)
	defer eng.Stop()

	eng.Build(ctx, "A", nil)

	// Wait long enough for at least one prune cycle.
	time.Sleep(60 * time.Millisecond)

	stats := eng.CacheStats()
	// After pruning entries older than 5ms, the cache should be empty.
	if stats.Entries != 0 {
		t.Errorf("expected empty cache after pruning; got %d entries", stats.Entries)
	}
}

// =========================================================================
// ScenarioOp WithDeriveOutputHashes tests
// =========================================================================

func TestScenarioOp_DeriveHashes_ChangesWithInput(t *testing.T) {
	op := dag.NewScenarioOp("derive",
		dag.WithOutputs(dag.FileRef{Path: "/out/x"}),
		dag.WithDeriveOutputHashes(),
	)

	inputs1 := []dag.FileRef{{Path: "/src/a", Hash: [32]byte{1}}}
	inputs2 := []dag.FileRef{{Path: "/src/a", Hash: [32]byte{2}}}

	out1, _ := op.Execute(context.Background(), inputs1)
	out2, _ := op.Execute(context.Background(), inputs2)

	if out1[0].Hash == out2[0].Hash {
		t.Error("output hashes should differ when input hashes differ")
	}
}

func TestScenarioOp_DeriveHashes_SameInputSameOutput(t *testing.T) {
	op := dag.NewScenarioOp("derive",
		dag.WithOutputs(dag.FileRef{Path: "/out/x"}),
		dag.WithDeriveOutputHashes(),
	)

	inputs := []dag.FileRef{{Path: "/src/a", Hash: [32]byte{42}}}

	out1, _ := op.Execute(context.Background(), inputs)
	out2, _ := op.Execute(context.Background(), inputs)

	if out1[0].Hash != out2[0].Hash {
		t.Error("same inputs should produce same output hash (deterministic)")
	}
}

func TestScenarioOp_NoDeriveHashes_AlwaysSameOutput(t *testing.T) {
	op := dag.NewScenarioOp("stable",
		dag.WithOutputs(dag.FileRef{Path: "/out/x", Hash: [32]byte{7}}),
	)

	inputs1 := []dag.FileRef{{Path: "/src/a", Hash: [32]byte{1}}}
	inputs2 := []dag.FileRef{{Path: "/src/a", Hash: [32]byte{2}}}

	out1, _ := op.Execute(context.Background(), inputs1)
	out2, _ := op.Execute(context.Background(), inputs2)

	if out1[0].Hash != out2[0].Hash {
		t.Error("without DeriveOutputHashes, output hash should be constant")
	}
}

// =========================================================================
// Engine benchmarks
// =========================================================================

func BenchmarkEngine_Build_Clean(b *testing.B) {
	d, _ := buildWideDAG(b, 16)
	eng, err := dag.NewEngine(d, dag.EngineConfig{WorkerCount: 8})
	if err != nil {
		b.Fatal(err)
	}
	ctx := context.Background()
	eng.Start(ctx)
	defer eng.Stop()

	b.ResetTimer()
	for range b.N {
		for _, v := range d.All() {
			v.Reset()
		}
		eng.Build(ctx, "target", nil)
	}
}

func BenchmarkEngine_Build_Cached(b *testing.B) {
	d, _ := buildWideDAG(b, 16)
	eng, err := dag.NewEngine(d, dag.EngineConfig{WorkerCount: 8})
	if err != nil {
		b.Fatal(err)
	}
	ctx := context.Background()
	eng.Start(ctx)
	defer eng.Stop()

	// Warm the cache.
	eng.Build(ctx, "target", nil)

	b.ResetTimer()
	for range b.N {
		for _, v := range d.All() {
			v.Reset()
		}
		eng.Build(ctx, "target", nil)
	}
}

func BenchmarkEngine_Plan_DryRun(b *testing.B) {
	d, _ := buildWideDAG(b, 32)
	eng, _ := dag.NewEngine(d, dag.EngineConfig{WorkerCount: 4})
	ctx := context.Background()

	b.ResetTimer()
	for range b.N {
		eng.Plan(ctx, "target", nil) //nolint:errcheck
	}
}

func BenchmarkEngine_Snapshot_And_Diff(b *testing.B) {
	d, _ := buildWideDAG(b, 32)
	eng, _ := dag.NewEngine(d, dag.EngineConfig{WorkerCount: 4})
	ctx := context.Background()
	eng.Start(ctx)
	defer eng.Stop()

	eng.Build(ctx, "target", nil)
	snap1 := eng.Snapshot()
	eng.ResetAll()
	eng.Build(ctx, "target", nil)
	snap2 := eng.Snapshot()

	b.ResetTimer()
	for range b.N {
		dag.Diff(snap1, snap2)
	}
}

func BenchmarkEngine_WriteMetrics(b *testing.B) {
	d, _ := buildWideDAG(b, 16)
	eng, _ := dag.NewEngine(d, dag.EngineConfig{WorkerCount: 4})
	ctx := context.Background()
	eng.Start(ctx)
	defer eng.Stop()

	for range 100 {
		eng.Build(ctx, "target", nil)
		eng.ResetAll()
	}

	var discard strings.Builder
	b.ResetTimer()
	for range b.N {
		discard.Reset()
		eng.WriteMetrics(&discard)
	}
}
