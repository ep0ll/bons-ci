// Package main is a complete example binary demonstrating the reactdag build
// engine with all production features wired together.
package main

import (
	"context"
	"fmt"
	"os"
	"time"

	dag "github.com/bons/bons-ci/plugins/dag"
)

// ── Example operations ────────────────────────────────────────────────────

type fetchOp struct{}

func (fetchOp) ID() string { return "fetch:sources" }
func (fetchOp) Execute(_ context.Context, _ []dag.FileRef) ([]dag.FileRef, error) {
	time.Sleep(8 * time.Millisecond)
	return []dag.FileRef{
		{Path: "/src/main.go", Size: 2048},
		{Path: "/src/lib.go", Size: 1024},
		{Path: "/src/util.go", Size: 512},
	}, nil
}

type compileOp struct{ pkg string }

func (o compileOp) ID() string { return "compile:" + o.pkg }
func (o compileOp) Execute(_ context.Context, inputs []dag.FileRef) ([]dag.FileRef, error) {
	time.Sleep(15 * time.Millisecond)
	return []dag.FileRef{
		{Path: fmt.Sprintf("/out/%s.o", o.pkg), Size: int64(len(inputs)) * 512},
	}, nil
}

type linkOp struct{}

func (linkOp) ID() string { return "link:binary" }
func (linkOp) Execute(_ context.Context, inputs []dag.FileRef) ([]dag.FileRef, error) {
	time.Sleep(10 * time.Millisecond)
	return []dag.FileRef{{Path: "/out/binary", Size: int64(len(inputs)) * 2048}}, nil
}

type testOp struct{}

func (testOp) ID() string { return "test:all" }
func (testOp) Execute(_ context.Context, _ []dag.FileRef) ([]dag.FileRef, error) {
	time.Sleep(20 * time.Millisecond)
	return nil, nil
}

type packageOp struct{}

func (packageOp) ID() string { return "package:dist" }
func (packageOp) Execute(_ context.Context, inputs []dag.FileRef) ([]dag.FileRef, error) {
	time.Sleep(5 * time.Millisecond)
	return []dag.FileRef{{Path: "/dist/app.tar.gz", Size: int64(len(inputs)) * 1024}}, nil
}

// ── main ──────────────────────────────────────────────────────────────────

func main() {
	fmt.Println("╔══════════════════════════════════════════════════════╗")
	fmt.Println("║        reactdag — complete example build              ║")
	fmt.Println("╚══════════════════════════════════════════════════════╝")
	fmt.Println()

	// 1. Build the DAG ─────────────────────────────────────────────────────
	d, err := dag.NewBuilder().
		Add("fetch", fetchOp{}).
		Add("compile-main", compileOp{pkg: "main"},
			dag.DependsOn("fetch"),
			dag.ConsumesFiles("fetch", "/src/main.go"),
			dag.WithTimeout(30*time.Second),
		).
		Add("compile-lib", compileOp{pkg: "lib"},
			dag.DependsOn("fetch"),
			dag.ConsumesFiles("fetch", "/src/lib.go", "/src/util.go"),
			dag.WithTimeout(30*time.Second),
		).
		Add("link", linkOp{},
			dag.DependsOn("compile-main", "compile-lib"),
			dag.WithTimeout(20*time.Second),
		).
		Add("test", testOp{},
			dag.DependsOn("compile-main", "compile-lib"),
			dag.WithTimeout(60*time.Second),
		).
		Add("package", packageOp{},
			dag.DependsOn("link", "test"),
			dag.WithLabel("resource_class", "io"),
		).
		Build()
	if err != nil {
		fmt.Fprintln(os.Stderr, "DAG error:", err)
		os.Exit(1)
	}

	// 2. Graph analysis ────────────────────────────────────────────────────
	analysis, _ := dag.Analyse(d)
	fmt.Print(dag.RenderAnalysis(analysis))
	fmt.Println()

	parallelism, _ := dag.AnalyseParallelism(d)
	fmt.Print(dag.RenderParallelismReport(parallelism))
	fmt.Println()

	// 3. Wire production components ────────────────────────────────────────
	fastCache := dag.NewManagedStore(1024, 0, dag.LRUPolicy{})
	diskCache, _ := dag.NewDiskCacheStore(os.TempDir() + "/reactdag-example")

	bus := dag.NewEventBus()
	history := dag.NewBuildHistory(100, "")
	sink := dag.NewInMemoryMetricsSink()

	jsonLog := dag.NewJSONLogger(os.Stderr) // structured logs to stderr
	jsonLog.Subscribe(bus)
	defer jsonLog.Unsubscribe()

	// Compose middleware: outermost applied first.
	baseExec := dag.NewDefaultExecutorForTest()
	retryExec := dag.NewRetryExecutor(baseExec, 30*time.Second,
		dag.RetryPolicy{MaxAttempts: 2})
	exec := dag.Chain(retryExec,
		dag.PanicRecoveryMiddleware(),
		dag.PerVertexTimeoutMiddleware(0), // use per-vertex label
		dag.MetricsMiddleware(sink),
	)

	sched := dag.NewScheduler(d,
		dag.WithWorkerCount(parallelism.MaxWidth+1),
		dag.WithFastCache(fastCache),
		dag.WithSlowCache(diskCache),
		dag.WithExecutor(exec),
		dag.WithEventBus(bus),
	)

	// 4. Dry-run plan ──────────────────────────────────────────────────────
	planner := dag.NewPlanner(d, fastCache, diskCache, nil)
	plan, _ := planner.Plan(context.Background(), "package", nil)
	fmt.Println(dag.RenderPlan(plan))

	// 5. Observer ──────────────────────────────────────────────────────────
	obs := sched.Observe(
		dag.WithFilter(dag.ForEventTypes(dag.EventStateChanged)),
		dag.WithBufferSize(64),
	)
	go func() {
		for e := range obs.Events() {
			to, _ := e.Payload["to"].(string)
			fmt.Printf("  [obs] %-20s → %s\n", e.VertexID, to)
		}
	}()

	// 6. Progress tracker ──────────────────────────────────────────────────
	tracker := dag.NewProgressTracker(bus)
	defer tracker.Unsubscribe()

	// 7. Build 1: clean ────────────────────────────────────────────────────
	fmt.Println("━━━ Build 1: clean ━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━")
	snap1 := dag.TakeSnapshot(d)
	buildStart := time.Now()
	ctx, cancel := dag.WithBuildTimeout(context.Background(), 5*time.Minute)
	m1, err1 := sched.Build(ctx, "package", nil)
	cancel()
	if err1 != nil {
		fmt.Fprintln(os.Stderr, "Build 1 failed:", err1)
	}
	history.Record("package", buildStart, m1, err1, map[string]string{"run": "1"})

	renderer := dag.NewProgressRenderer(os.Stdout, tracker, false)
	fmt.Println()
	renderer.Render()
	fmt.Println()
	dag.WriteReport(os.Stdout, d, m1, dag.DefaultReportOptions())
	fmt.Println()

	// 8. Build 2: all cached ───────────────────────────────────────────────
	for _, v := range d.All() {
		v.Reset()
	}
	fmt.Println("━━━ Build 2: all cached ━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━")
	snap2before := dag.TakeSnapshot(d)
	ctx2, cancel2 := dag.WithBuildTimeout(context.Background(), 5*time.Minute)
	bstart2 := time.Now()
	m2, err2 := sched.Build(ctx2, "package", nil)
	cancel2()
	if err2 != nil {
		fmt.Fprintln(os.Stderr, "Build 2 failed:", err2)
	}
	history.Record("package", bstart2, m2, err2, map[string]string{"run": "2"})
	snap2after := dag.TakeSnapshot(d)

	diffs := dag.Diff(snap2before, snap2after)
	fmt.Println(dag.RenderDiff(diffs))
	fmt.Println("Diff summary:", dag.DiffSummary(diffs))
	fmt.Println()

	// 9. Build 3: file change ──────────────────────────────────────────────
	for _, v := range d.All() {
		v.Reset()
	}
	fmt.Println("━━━ Build 3: /src/main.go changed ━━━━━━━━━━━━━━━━━━━━━")
	changed := []dag.FileRef{{Path: "/src/main.go", Hash: [32]byte{0xFF}}}
	ctx3, cancel3 := dag.WithBuildTimeout(context.Background(), 5*time.Minute)
	bstart3 := time.Now()
	m3, err3 := sched.Build(ctx3, "package", changed)
	cancel3()
	if err3 != nil {
		fmt.Fprintln(os.Stderr, "Build 3 failed:", err3)
	}
	history.Record("package", bstart3, m3, err3, map[string]string{"run": "3"})

	snap3 := dag.TakeSnapshot(d)
	diffs3 := dag.Diff(snap1, snap3)
	fmt.Printf("Changed from clean baseline: %s\n\n", dag.DiffSummary(diffs3))

	// 10. History trend ────────────────────────────────────────────────────
	fmt.Println("━━━ Build history ━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━")
	var sb stringer
	dag.WriteTrendReport(&sb, history, "package", 10)
	fmt.Print(sb.String())
	fmt.Println()

	// 11. Cache stats ──────────────────────────────────────────────────────
	fmt.Println("━━━ Cache stats ━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━")
	fmt.Println(" ", dag.RenderCacheStats(fastCache.Stats()))
	if top := fastCache.TopN(3); len(top) > 0 {
		fmt.Println("  Top 3 by hit count:")
		for _, e := range top {
			fmt.Printf("    key=%x  hits=%d  bytes=%s\n",
				e.Key[:4], e.Managed.HitCount,
				formatBytes(e.Managed.SizeBytes))
		}
	}
	fmt.Println()

	// 12. Execution metrics ────────────────────────────────────────────────
	samples := sink.Samples()
	fmt.Printf("MetricsMiddleware: %d samples, total=%s, errors=%d\n\n",
		len(samples), sink.TotalDuration(), sink.ErrorCount())

	// 13. DOT export ───────────────────────────────────────────────────────
	dot := dag.ExportDOT(d, dag.DOTOptions{
		Title: "example-build", ShowState: true,
		ShowMetrics: true, ShowFileDeps: true,
	})
	dotPath := os.TempDir() + "/reactdag-example.dot"
	if err := os.WriteFile(dotPath, []byte(dot), 0o644); err == nil {
		fmt.Printf("DOT written to %s\n", dotPath)
		fmt.Println("  Render: dot -Tsvg -o graph.svg " + dotPath)
	}

	obs.Unsubscribe()
	obs.Drain()
	fmt.Println("\n✓ Example complete.")
}

func formatBytes(b int64) string {
	switch {
	case b >= 1<<20:
		return fmt.Sprintf("%.1fMB", float64(b)/float64(1<<20))
	case b >= 1<<10:
		return fmt.Sprintf("%.1fKB", float64(b)/float64(1<<10))
	default:
		return fmt.Sprintf("%dB", b)
	}
}

// stringer adapts strings.Builder to WriteTrendReport's interface.
type stringer struct{ b []byte }

func (s *stringer) WriteString(str string) (int, error) {
	s.b = append(s.b, str...)
	return len(str), nil
}
func (s *stringer) String() string { return string(s.b) }
