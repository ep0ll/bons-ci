package reactdag_test

import (
	"context"
	"fmt"
	"testing"
	"time"

	dag "github.com/bons/bons-ci/plugins/dag"
)

// ---------------------------------------------------------------------------
// Middleware chain benchmarks
// ---------------------------------------------------------------------------

func BenchmarkChain_Noop_Depth1(b *testing.B) {
	base := dag.NewDefaultExecutorForTest()
	chain := dag.Chain(base, dag.DryRunMiddleware())
	v := dag.NewVertex("x", noopOp{id: "x"})
	ctx := context.Background()
	b.ResetTimer()
	for range b.N {
		chain.Execute(ctx, v) //nolint:errcheck
	}
}

func BenchmarkChain_Noop_Depth5(b *testing.B) {
	base := dag.NewDefaultExecutorForTest()
	chain := dag.Chain(base,
		dag.PanicRecoveryMiddleware(),
		dag.DryRunMiddleware(),
		dag.ContextValueMiddleware("k", "v"),
		dag.ContextValueMiddleware("k2", "v2"),
		dag.MetricsMiddleware(dag.NewInMemoryMetricsSink()),
	)
	v := dag.NewVertex("x", noopOp{id: "x"})
	ctx := context.Background()
	b.ResetTimer()
	for range b.N {
		chain.Execute(ctx, v) //nolint:errcheck
	}
}

func BenchmarkMetricsSink_InMemory_Record(b *testing.B) {
	sink := dag.NewInMemoryMetricsSink()
	sample := dag.ExecutionSample{
		VertexID: "vertex-bench",
		Duration: 10 * time.Millisecond,
	}
	ctx := context.Background()
	b.ResetTimer()
	for range b.N {
		sink.RecordExecution(ctx, sample)
	}
}

// ---------------------------------------------------------------------------
// Group scheduler benchmarks
// ---------------------------------------------------------------------------

func BenchmarkGroupScheduler_BuildGroup(b *testing.B) {
	sizes := []int{4, 16, 64}
	for _, n := range sizes {
		b.Run(fmt.Sprintf("members=%d", n), func(b *testing.B) {
			d := dag.NewDAG()
			root := dag.NewVertex("root", noopOp{id: "root"})
			if err := d.AddVertex(root); err != nil {
				b.Fatal(err)
			}
			ids := make([]string, n)
			for i := range n {
				id := fmt.Sprintf("m%d", i)
				v := dag.NewVertex(id, noopOp{id: id})
				if err := d.AddVertex(v); err != nil {
					b.Fatal(err)
				}
				if err := d.LinkVertices("root", id); err != nil {
					b.Fatal(err)
				}
				ids[i] = id
			}
			if err := d.Seal(); err != nil {
				b.Fatal(err)
			}

			reg := dag.NewGroupRegistry()
			reg.Register(dag.NewGroup("all", ids...))
			gs := dag.NewGroupScheduler(d, reg, dag.WithWorkerCount(8))
			ctx := context.Background()

			b.ResetTimer()
			for range b.N {
				for _, v := range d.All() {
					v.Reset()
				}
				gs.BuildGroup(ctx, "all", nil) //nolint:errcheck
			}
		})
	}
}

// ---------------------------------------------------------------------------
// Graph analysis benchmarks
// ---------------------------------------------------------------------------

func BenchmarkAnalyse_Small(b *testing.B) {
	d, _ := buildLinearDAGBench(b)
	b.ResetTimer()
	for range b.N {
		dag.Analyse(d) //nolint:errcheck
	}
}

func BenchmarkAnalyse_Wide(b *testing.B) {
	d, _ := buildWideDAG(b, 32)
	b.ResetTimer()
	for range b.N {
		dag.Analyse(d) //nolint:errcheck
	}
}

func BenchmarkAnalyse_Deep(b *testing.B) {
	d, _ := buildDeepDAG(b, 50)
	b.ResetTimer()
	for range b.N {
		dag.Analyse(d) //nolint:errcheck
	}
}

func BenchmarkAnalyseParallelism_Wide(b *testing.B) {
	d, _ := buildWideDAG(b, 64)
	b.ResetTimer()
	for range b.N {
		dag.AnalyseParallelism(d) //nolint:errcheck
	}
}

// ---------------------------------------------------------------------------
// Eviction policy benchmarks
// ---------------------------------------------------------------------------

func BenchmarkManagedStore_Set_LRU(b *testing.B) {
	store := dag.NewManagedStore(1000, 0, dag.LRUPolicy{})
	ctx := context.Background()
	entry := &dag.CacheEntry{CachedAt: time.Now()}
	b.ResetTimer()
	for i := range b.N {
		key := dag.CacheKey{byte(i), byte(i >> 8), byte(i >> 16), byte(i >> 24)}
		store.Set(ctx, key, entry) //nolint:errcheck
	}
}

func BenchmarkManagedStore_Get_Hit_LRU(b *testing.B) {
	store := dag.NewManagedStore(0, 0, dag.LRUPolicy{})
	ctx := context.Background()
	key := dag.CacheKey{42}
	store.Set(ctx, key, &dag.CacheEntry{CachedAt: time.Now()}) //nolint:errcheck
	b.ResetTimer()
	for range b.N {
		store.Get(ctx, key) //nolint:errcheck
	}
}

func BenchmarkManagedStore_Set_LFU_Eviction(b *testing.B) {
	// Cap at 100 entries to force frequent evictions.
	store := dag.NewManagedStore(100, 0, dag.LFUPolicy{})
	ctx := context.Background()
	entry := &dag.CacheEntry{CachedAt: time.Now()}
	b.ResetTimer()
	for i := range b.N {
		key := dag.CacheKey{byte(i), byte(i >> 8)}
		store.Set(ctx, key, entry) //nolint:errcheck
	}
}

func BenchmarkManagedStore_Set_SizePolicy(b *testing.B) {
	store := dag.NewManagedStore(0, 1<<20, dag.SizePolicy{}) // 1MB cap
	ctx := context.Background()
	entry := &dag.CacheEntry{
		OutputFiles: []dag.FileRef{{Size: 1024}},
		CachedAt:    time.Now(),
	}
	b.ResetTimer()
	for i := range b.N {
		key := dag.CacheKey{byte(i), byte(i >> 8)}
		store.Set(ctx, key, entry) //nolint:errcheck
	}
}

// ---------------------------------------------------------------------------
// DAG snapshot + diff benchmarks
// ---------------------------------------------------------------------------

func BenchmarkTakeSnapshot(b *testing.B) {
	d, _ := buildWideDAG(b, 32)
	b.ResetTimer()
	for range b.N {
		dag.TakeSnapshot(d)
	}
}

func BenchmarkDiff_NoChanges(b *testing.B) {
	d, _ := buildWideDAG(b, 32)
	snap := dag.TakeSnapshot(d)
	b.ResetTimer()
	for range b.N {
		dag.Diff(snap, snap)
	}
}

func BenchmarkDiff_AllStateChanged(b *testing.B) {
	d, _ := buildWideDAG(b, 32)
	snap1 := dag.TakeSnapshot(d)

	// Advance all states.
	for _, v := range d.All() {
		v.SetState(dag.StateCompleted, "bench") //nolint:errcheck
	}
	snap2 := dag.TakeSnapshot(d)

	b.ResetTimer()
	for range b.N {
		dag.Diff(snap1, snap2)
	}
}

// ---------------------------------------------------------------------------
// Build history benchmarks
// ---------------------------------------------------------------------------

func BenchmarkBuildHistory_Record(b *testing.B) {
	h := dag.NewBuildHistory(0, "")
	m := &dag.BuildMetrics{TotalDuration: 50 * time.Millisecond, Executed: 10}
	start := time.Now()
	b.ResetTimer()
	for range b.N {
		h.Record("target", start, m, nil, nil)
	}
}

func BenchmarkBuildHistory_Trend_1000(b *testing.B) {
	h := dag.NewBuildHistory(0, "")
	m := &dag.BuildMetrics{TotalDuration: 100 * time.Millisecond}
	start := time.Now()
	for range 1000 {
		h.Record("A", start, m, nil, nil)
	}
	b.ResetTimer()
	for range b.N {
		h.Trend("A", 1000)
	}
}

// ---------------------------------------------------------------------------
// Vertex gate benchmark (concurrent group builds)
// ---------------------------------------------------------------------------

func BenchmarkScheduler_ConcurrentGroupBuilds(b *testing.B) {
	d, _ := buildWideDAG(b, 16)
	reg := dag.NewGroupRegistry()
	ids := make([]string, 0, 16)
	for _, v := range d.All() {
		if len(v.Parents()) > 0 { // non-root leaves
			ids = append(ids, v.ID())
		}
	}
	reg.Register(dag.NewGroup("all", ids...))
	gs := dag.NewGroupScheduler(d, reg, dag.WithWorkerCount(8))
	ctx := context.Background()

	b.ResetTimer()
	for range b.N {
		for _, v := range d.All() {
			v.Reset()
		}
		gs.BuildGroup(ctx, "all", nil) //nolint:errcheck
	}
}

// ---------------------------------------------------------------------------
// Invalidation engine benchmark
// ---------------------------------------------------------------------------

func BenchmarkInvalidation_WideDAG(b *testing.B) {
	d, _ := buildWideDAG(b, 64)

	// Pre-populate output files so invalidation can match paths.
	for _, v := range d.All() {
		v.SetOutputFiles([]dag.FileRef{
			{Path: fmt.Sprintf("/out/%s.o", v.ID())},
		})
		v.SetState(dag.StateCompleted, "bench") //nolint:errcheck
	}

	engine := dag.NewInvalidationEngine(d, nil)
	changedFiles := []dag.FileRef{{Path: "/out/root.o"}}
	ctx := context.Background()

	b.ResetTimer()
	for range b.N {
		// Reset to completed before each invalidation.
		for _, v := range d.All() {
			if v.State() == dag.StateInitial {
				v.SetState(dag.StateCompleted, "bench") //nolint:errcheck
			}
		}
		engine.Invalidate(ctx, changedFiles) //nolint:errcheck
	}
}

// buildLinearDAGBench builds A→B→C for benchmarks.
func buildLinearDAGBench(b *testing.B) (*dag.DAG, map[string]*dag.Vertex) {
	b.Helper()
	d := dag.NewDAG()
	vC := dag.NewVertex("C", noopOp{id: "C"})
	vB := dag.NewVertex("B", noopOp{id: "B"})
	vA := dag.NewVertex("A", noopOp{id: "A"})
	for _, v := range []*dag.Vertex{vC, vB, vA} {
		if err := d.AddVertex(v); err != nil {
			b.Fatal(err)
		}
	}
	if err := d.LinkVertices("C", "B"); err != nil {
		b.Fatal(err)
	}
	if err := d.LinkVertices("B", "A"); err != nil {
		b.Fatal(err)
	}
	if err := d.Seal(); err != nil {
		b.Fatal(err)
	}
	return d, map[string]*dag.Vertex{"A": vA, "B": vB, "C": vC}
}
