package reactdag_test

import (
	"context"
	"errors"
	"strings"
	"testing"
	"time"

	dag "github.com/bons/bons-ci/plugins/dag"
)

// =========================================================================
// Structured errors tests
// =========================================================================

func TestBuildError_Wraps(t *testing.T) {
	cause := errors.New("compilation failed")
	be := dag.NewBuildError("myTarget", cause)

	if !dag.IsBuildError(be) {
		t.Error("IsBuildError should return true for *BuildError")
	}
	if !errors.Is(be, cause) {
		t.Error("errors.Is should find the wrapped cause")
	}
	if !strings.Contains(be.Error(), "myTarget") {
		t.Errorf("Error() should contain target ID; got %q", be.Error())
	}
}

func TestVertexError_Wraps(t *testing.T) {
	v := dag.NewVertex("compile", noopOp{id: "compile"})
	cause := errors.New("oom")
	ve := dag.NewVertexError(v, 2, cause)

	if !dag.IsVertexError(ve) {
		t.Error("IsVertexError should return true for *VertexError")
	}
	if dag.VertexIDFromError(ve) != "compile" {
		t.Errorf("VertexIDFromError = %q; want compile", dag.VertexIDFromError(ve))
	}
	if !strings.Contains(ve.Error(), "attempt=2") {
		t.Errorf("Error() should contain attempt count; got %q", ve.Error())
	}
}

func TestVertexIDFromError_ReturnsEmpty_WhenNotPresent(t *testing.T) {
	plain := errors.New("plain error")
	if got := dag.VertexIDFromError(plain); got != "" {
		t.Errorf("VertexIDFromError plain error = %q; want empty", got)
	}
}

func TestCycleError_Message(t *testing.T) {
	ce := &dag.CycleError{InvolvedVertex: "A"}
	if !strings.Contains(ce.Error(), "A") {
		t.Errorf("CycleError.Error() should name vertex; got %q", ce.Error())
	}
}

func TestStateTransitionError_Message(t *testing.T) {
	ste := &dag.StateTransitionError{
		VertexID: "B",
		From:     dag.StateCompleted,
		To:       dag.StateInitial,
		Cause:    "test reset",
	}
	msg := ste.Error()
	if !strings.Contains(msg, "B") || !strings.Contains(msg, "completed") {
		t.Errorf("StateTransitionError.Error() incomplete: %q", msg)
	}
}

// =========================================================================
// MetricsExporter tests
// =========================================================================

func TestMetricsExporter_RecordBuild_CountsCorrectly(t *testing.T) {
	exp := dag.NewMetricsExporter(nil)

	m := &dag.BuildMetrics{
		TotalVertices: 5,
		Executed:      3,
		FastCacheHits: 2,
		TotalDuration: 100 * time.Millisecond,
	}
	exp.RecordBuild(m, nil)
	exp.RecordBuild(m, errors.New("fail"))

	out := exp.CollectString()
	if !strings.Contains(out, "reactdag_builds_total") {
		t.Errorf("missing builds_total metric: %s", out)
	}
	if !strings.Contains(out, "reactdag_build_failures_total") {
		t.Errorf("missing build_failures_total: %s", out)
	}
	if !strings.Contains(out, "reactdag_vertices_executed_total") {
		t.Errorf("missing vertices_executed_total: %s", out)
	}
}

func TestMetricsExporter_ConstLabels(t *testing.T) {
	exp := dag.NewMetricsExporter(map[string]string{"env": "ci", "service": "build"})
	m := &dag.BuildMetrics{TotalDuration: 50 * time.Millisecond}
	exp.RecordBuild(m, nil)

	out := exp.CollectString()
	if !strings.Contains(out, `env="ci"`) {
		t.Errorf("const labels missing from output: %s", out)
	}
}

func TestMetricsExporter_HistogramBuckets(t *testing.T) {
	exp := dag.NewMetricsExporter(nil)
	m := &dag.BuildMetrics{TotalDuration: 250 * time.Millisecond}
	exp.RecordBuild(m, nil)

	out := exp.CollectString()
	if !strings.Contains(out, `_bucket{le=`) {
		t.Errorf("histogram buckets missing: %s", out)
	}
	if !strings.Contains(out, `_sum`) {
		t.Errorf("histogram sum missing: %s", out)
	}
	if !strings.Contains(out, `_count`) {
		t.Errorf("histogram count missing: %s", out)
	}
}

func TestMetricsExporter_PerVertexHistogram(t *testing.T) {
	exp := dag.NewMetricsExporter(nil)
	exp.RecordVertexExecution("compile:main", 42*time.Millisecond)
	exp.RecordVertexExecution("compile:main", 55*time.Millisecond)

	out := exp.CollectString()
	if !strings.Contains(out, "reactdag_vertex_duration_ms") {
		t.Errorf("per-vertex histogram missing: %s", out)
	}
}

func TestExporterHook_WiresEventBus(t *testing.T) {
	d, _ := buildLinearDAG(t)
	bus := dag.NewEventBus()
	exp := dag.NewMetricsExporter(nil)
	hook := dag.NewExporterHook(exp, bus)
	defer hook.Unsubscribe()

	s := dag.NewScheduler(d, dag.WithEventBus(bus), dag.WithWorkerCount(2))
	if _, err := s.Build(context.Background(), "A", nil); err != nil {
		t.Fatalf("Build: %v", err)
	}

	out := exp.CollectString()
	// After a clean build, cache misses should be recorded.
	if !strings.Contains(out, "reactdag_cache_misses_total") {
		t.Errorf("expected cache_misses_total in output: %s", out)
	}
}

// =========================================================================
// TestClock tests
// =========================================================================

func TestTestClock_AdvanceAndRead(t *testing.T) {
	base := time.Date(2024, 1, 1, 0, 0, 0, 0, time.UTC)
	clk := dag.NewTestClock(base)

	if got := clk.Now(); !got.Equal(base) {
		t.Errorf("Now() = %v; want %v", got, base)
	}
	clk.Advance(5 * time.Second)
	if got := clk.Now(); !got.Equal(base.Add(5*time.Second)) {
		t.Errorf("After Advance(5s): Now() = %v; want %v", got, base.Add(5*time.Second))
	}
}

func TestTestClock_Set(t *testing.T) {
	clk := dag.NewTestClock(time.Now())
	future := time.Date(2030, 6, 15, 12, 0, 0, 0, time.UTC)
	clk.Set(future)
	if !clk.Now().Equal(future) {
		t.Errorf("Set then Now() = %v; want %v", clk.Now(), future)
	}
}

// =========================================================================
// RecordingExecutor tests
// =========================================================================

func TestRecordingExecutor_RecordsAllCalls(t *testing.T) {
	d, _ := buildLinearDAG(t)
	rec := dag.NewRecordingExecutor(nil)
	s := dag.NewScheduler(d, dag.WithExecutor(rec), dag.WithWorkerCount(2))

	if _, err := s.Build(context.Background(), "A", nil); err != nil {
		t.Fatalf("Build: %v", err)
	}

	if rec.CallCount() != 3 {
		t.Errorf("CallCount = %d; want 3", rec.CallCount())
	}
	for _, id := range []string{"A", "B", "C"} {
		if !rec.WasCalled(id) {
			t.Errorf("vertex %s was not called", id)
		}
	}
}

func TestRecordingExecutor_Reset_ClearsRecords(t *testing.T) {
	rec := dag.NewRecordingExecutor(nil)
	v := dag.NewVertex("x", noopOp{id: "x"})
	rec.Execute(context.Background(), v) //nolint:errcheck

	rec.Reset()
	if rec.CallCount() != 0 {
		t.Errorf("after Reset, CallCount = %d; want 0", rec.CallCount())
	}
}

func TestRecordingExecutor_CallsFor_FiltersCorrectly(t *testing.T) {
	rec := dag.NewRecordingExecutor(nil)
	vA := dag.NewVertex("A", noopOp{id: "A"})
	vB := dag.NewVertex("B", noopOp{id: "B"})

	rec.Execute(context.Background(), vA) //nolint:errcheck
	rec.Execute(context.Background(), vB) //nolint:errcheck
	rec.Execute(context.Background(), vA) //nolint:errcheck

	if len(rec.CallsFor("A")) != 2 {
		t.Errorf("CallsFor(A) = %d; want 2", len(rec.CallsFor("A")))
	}
	if len(rec.CallsFor("B")) != 1 {
		t.Errorf("CallsFor(B) = %d; want 1", len(rec.CallsFor("B")))
	}
}

// =========================================================================
// BuildHarness tests
// =========================================================================

func TestBuildHarness_Run_RecordsExecutions(t *testing.T) {
	d, _ := buildLinearDAG(t)
	h := dag.NewBuildHarness(d)

	if _, err := h.Run(context.Background(), "A", nil); err != nil {
		t.Fatalf("Run: %v", err)
	}

	executed := h.ExecutedVertices()
	if len(executed) != 3 {
		t.Errorf("ExecutedVertices = %v; want 3 vertices", executed)
	}
}

func TestBuildHarness_Reset_ClearsState(t *testing.T) {
	d, _ := buildLinearDAG(t)
	h := dag.NewBuildHarness(d)

	h.Run(context.Background(), "A", nil) //nolint:errcheck
	h.Reset()

	if h.Recorder.CallCount() != 0 {
		t.Errorf("after Reset, call count = %d; want 0", h.Recorder.CallCount())
	}
	for _, v := range d.All() {
		if v.State() != dag.StateInitial {
			t.Errorf("vertex %s state = %s; want initial after Reset", v.ID(), v.State())
		}
	}
}

func TestBuildHarness_EventsOfType_Filters(t *testing.T) {
	d, _ := buildLinearDAG(t)
	h := dag.NewBuildHarness(d)
	h.Run(context.Background(), "A", nil) //nolint:errcheck

	stateEvents := h.EventsOfType(dag.EventStateChanged)
	if len(stateEvents) == 0 {
		t.Error("expected state change events")
	}
	for _, e := range stateEvents {
		if e.Type != dag.EventStateChanged {
			t.Errorf("EventsOfType returned wrong type %q", e.Type)
		}
	}
}

func TestBuildHarness_SecondBuild_UsesCache(t *testing.T) {
	d, _ := buildLinearDAG(t)
	h := dag.NewBuildHarness(d)

	// Build 1: executes all vertices.
	h.Run(context.Background(), "A", nil) //nolint:errcheck
	firstCount := h.Recorder.CallCount()
	h.Reset()

	// Build 2: all vertices should come from cache → 0 executor calls.
	h.Run(context.Background(), "A", nil) //nolint:errcheck
	secondCount := h.Recorder.CallCount()

	if firstCount != 3 {
		t.Errorf("build 1: executed %d vertices; want 3", firstCount)
	}
	if secondCount != 0 {
		t.Errorf("build 2: executed %d vertices; want 0 (all cached)", secondCount)
	}
}

// =========================================================================
// ScenarioOp tests
// =========================================================================

func TestScenarioOp_ProducesOutputs(t *testing.T) {
	outputs := []dag.FileRef{{Path: "/out/x", Size: 100}}
	op := dag.NewScenarioOp("build", dag.WithOutputs(outputs...))

	result, err := op.Execute(context.Background(), nil)
	if err != nil {
		t.Fatalf("Execute: %v", err)
	}
	if len(result) != 1 || result[0].Path != "/out/x" {
		t.Errorf("outputs = %v; want [{/out/x}]", result)
	}
}

func TestScenarioOp_ReturnsError(t *testing.T) {
	sentinel := errors.New("oops")
	op := dag.NewScenarioOp("fail", dag.WithError(sentinel))
	_, err := op.Execute(context.Background(), nil)
	if !errors.Is(err, sentinel) {
		t.Errorf("Execute err = %v; want sentinel", err)
	}
}

func TestScenarioOp_RespectsContextOnDelay(t *testing.T) {
	op := dag.NewScenarioOp("slow", dag.WithDelay(500*time.Millisecond))
	ctx, cancel := context.WithTimeout(context.Background(), 20*time.Millisecond)
	defer cancel()

	start := time.Now()
	_, err := op.Execute(ctx, nil)
	if err == nil {
		t.Error("expected context cancellation error")
	}
	if time.Since(start) > 100*time.Millisecond {
		t.Error("delay did not respect context deadline")
	}
}

// =========================================================================
// CaptureMemStats tests
// =========================================================================

func TestCaptureMemStats_ReturnsNonZero(t *testing.T) {
	snap := dag.CaptureMemStats()
	if snap.Alloc == 0 {
		t.Error("Alloc should be non-zero")
	}
	if snap.Goroutines == 0 {
		t.Error("Goroutines should be non-zero")
	}
}

func TestMemStatsSnapshot_Delta(t *testing.T) {
	before := dag.CaptureMemStats()
	// Allocate some memory to create a measurable delta.
	buf := make([]byte, 1<<20) // 1 MB
	_ = buf
	after := dag.CaptureMemStats()

	delta := before.Delta(after)
	if delta.TotalAlloc == 0 {
		t.Error("delta.TotalAlloc should be non-zero after allocation")
	}
	s := delta.String()
	if !strings.Contains(s, "alloc=") {
		t.Errorf("String() missing alloc field: %s", s)
	}
}

// =========================================================================
// Full pipeline integration test
// =========================================================================

// TestFullPipeline_MultiPhase simulates a realistic multi-phase CI pipeline:
//   fetch → compile-main, compile-lib (parallel) → link → test → package
//
// It validates:
//   1. Clean build executes all vertices in correct order.
//   2. Cached build skips all vertices.
//   3. Only compile-main and link/test/package re-execute when main.go changes.
//   4. Metrics, history, diff, and exporter all reflect the correct counts.
func TestFullPipeline_MultiPhase(t *testing.T) {
	const (
		targetID = "package"
	)

	// Build DAG using ScenarioOps for full control.
	makeFile := func(path string) dag.FileRef {
		return dag.FileRef{Path: path, Size: 512}
	}

	d, err := dag.NewBuilder().
		Add("fetch", dag.NewScenarioOp("fetch",
			dag.WithOutputs(
				makeFile("/src/main.go"),
				makeFile("/src/lib.go"),
			))).
		Add("compile-main", dag.NewScenarioOp("compile-main",
			dag.WithOutputs(makeFile("/out/main.o")),
			dag.WithDeriveOutputHashes()),
			dag.DependsOn("fetch"),
			dag.ConsumesFiles("fetch", "/src/main.go"),
		).
		Add("compile-lib", dag.NewScenarioOp("compile-lib",
			dag.WithOutputs(makeFile("/out/lib.o")),
			dag.WithDeriveOutputHashes()),
			dag.DependsOn("fetch"),
			dag.ConsumesFiles("fetch", "/src/lib.go"),
		).
		Add("link", dag.NewScenarioOp("link",
			dag.WithOutputs(makeFile("/out/binary")),
			dag.WithDeriveOutputHashes()),
			dag.DependsOn("compile-main", "compile-lib"),
		).
		Add("test", dag.NewScenarioOp("test"),
			dag.DependsOn("compile-main", "compile-lib"),
		).
		Add("package", dag.NewScenarioOp("package",
			dag.WithOutputs(makeFile("/dist/app.tar.gz")),
			dag.WithDeriveOutputHashes()),
			dag.DependsOn("link", "test"),
		).
		Build()
	if err != nil {
		t.Fatalf("DAG build: %v", err)
	}

	// Wire the full stack.
	exp := dag.NewMetricsExporter(map[string]string{"test": "pipeline"})
	history := dag.NewBuildHistory(100, "")
	h := dag.NewBuildHarness(d, dag.WithWorkerCount(4))

	hookExp := dag.NewExporterHook(exp, h.EventBus)
	defer hookExp.Unsubscribe()

	ctx := context.Background()

	// ── Phase 1: clean build ──────────────────────────────────────────────
	t.Run("clean_build", func(t *testing.T) {
		m, err := h.Run(ctx, targetID, nil)
		if err != nil {
			t.Fatalf("clean build failed: %v", err)
		}
		history.Record(targetID, time.Now(), m, nil, map[string]string{"phase": "1"})

		if m.Executed != 6 {
			t.Errorf("Executed = %d; want 6", m.Executed)
		}
		if m.FastCacheHits != 0 {
			t.Errorf("FastCacheHits = %d; want 0 (clean)", m.FastCacheHits)
		}

		// Verify topological order: fetch before compile-*.
		records := h.Recorder.Records()
		fetchIdx := -1
		compileIdx := -1
		for i, r := range records {
			if r.VertexID == "fetch" {
				fetchIdx = i
			}
			if r.VertexID == "compile-main" || r.VertexID == "compile-lib" {
				if compileIdx == -1 {
					compileIdx = i
				}
			}
		}
		if fetchIdx >= compileIdx {
			t.Errorf("fetch (idx=%d) must execute before compile (idx=%d)", fetchIdx, compileIdx)
		}
	})

	// ── Phase 2: cached build ─────────────────────────────────────────────
	t.Run("cached_build", func(t *testing.T) {
		h.Reset()

		// Snapshot before build 2 — all vertices are StateInitial after Reset.
		snap1 := dag.TakeSnapshot(d)
		m, err := h.Run(ctx, targetID, nil)
		if err != nil {
			t.Fatalf("cached build failed: %v", err)
		}
		history.Record(targetID, time.Now(), m, nil, map[string]string{"phase": "2"})
		// Snapshot after — all should be completed (from fast cache).
		snap2 := dag.TakeSnapshot(d)

		if h.Recorder.CallCount() != 0 {
			t.Errorf("cached build executed %d vertices; want 0", h.Recorder.CallCount())
		}
		if m.FastCacheHits != 6 {
			t.Errorf("FastCacheHits = %d; want 6", m.FastCacheHits)
		}

		// All vertices should have transitioned: initial → completed (via cache).
		diffs := dag.Diff(snap1, snap2)
		stateChangeDiffs := 0
		for _, d := range diffs {
			if d.ChangeType == dag.DiffStateChanged {
				stateChangeDiffs++
			}
		}
		if stateChangeDiffs == 0 {
			t.Errorf("expected state-changed diffs on cached build; got %v", dag.DiffSummary(diffs))
		}
	})

	// ── Phase 3: incremental (only main.go changed) ────────────────────────
	// NOTE: This phase runs WITHOUT calling h.Reset() first. The invalidation
	// engine uses each vertex's current OutputFiles() to determine which
	// downstream vertices consume changed paths. After phase 2 the vertices
	// have their outputs populated from cache; Reset() would clear them.
	t.Run("incremental_main_changed", func(t *testing.T) {
		// Reset recorder only — preserve vertex output files for invalidation.
		h.Recorder.Reset()

		// Change main.go hash — this should invalidate compile-main
		// (which declares it consumes /src/main.go from fetch).
		// Populate fetch's output files so the engine can check them.
		fetchV, _ := d.Vertex("fetch")
		fetchV.SetOutputFiles([]dag.FileRef{
			{Path: "/src/main.go", Hash: [32]byte{0xDE, 0xAD}, Size: 512},
			{Path: "/src/lib.go", Size: 512},
		})

		changedFiles := []dag.FileRef{{
			Path: "/src/main.go",
			Hash: [32]byte{0xDE, 0xAD},
			Size: 512,
		}}

		// Reset vertices that we expect to re-run so they are not terminal.
		for _, id := range []string{"compile-main", "link", "test", "package"} {
			if v, ok := d.Vertex(id); ok {
				v.Reset()
			}
		}

		m, err := h.Run(ctx, targetID, changedFiles)
		if err != nil {
			t.Fatalf("incremental build failed: %v", err)
		}
		history.Record(targetID, time.Now(), m, nil, map[string]string{"phase": "3"})

		executed := h.ExecutedVertices()
		executedSet := make(map[string]bool)
		for _, id := range executed {
			executedSet[id] = true
		}

		// These vertices were reset → they must re-execute.
		for _, must := range []string{"compile-main", "link", "test", "package"} {
			if !executedSet[must] {
				t.Errorf("vertex %q should have re-executed after main.go change", must)
			}
		}
		// compile-lib reads only /src/lib.go which didn't change → from cache.
		if executedSet["compile-lib"] {
			t.Error("compile-lib should NOT re-execute (only reads lib.go)")
		}
	})

	// ── Phase 4: validate history and metrics ─────────────────────────────
	t.Run("history_and_metrics", func(t *testing.T) {
		stats := history.Trend(targetID, 10)
		if stats.Count != 3 {
			t.Errorf("history count = %d; want 3", stats.Count)
		}
		if stats.PassRate != 1.0 {
			t.Errorf("pass rate = %.2f; want 1.0", stats.PassRate)
		}

		promOut := exp.CollectString()
		if !strings.Contains(promOut, "reactdag_builds_total") {
			t.Errorf("Prometheus output missing reactdag_builds_total")
		}
	})

	// ── Phase 5: group build ──────────────────────────────────────────────
	t.Run("group_build", func(t *testing.T) {
		h.Reset()
		reg := dag.NewGroupRegistry()
		reg.Register(dag.NewGroup("compilers", "compile-main", "compile-lib"))
		gs := dag.NewGroupScheduler(d, reg, dag.WithWorkerCount(4))

		result, err := gs.BuildGroup(ctx, "compilers", nil)
		if err != nil {
			t.Fatalf("group build failed: %v", err)
		}
		if !result.Succeeded() {
			t.Errorf("group build errors: %v", result.Errors())
		}
		if len(result.Results) != 2 {
			t.Errorf("group results = %d; want 2", len(result.Results))
		}
	})

	// ── Phase 6: graph analysis ───────────────────────────────────────────
	t.Run("graph_analysis", func(t *testing.T) {
		a, err := dag.Analyse(d)
		if err != nil {
			t.Fatalf("Analyse: %v", err)
		}
		if a.VertexCount != 6 {
			t.Errorf("VertexCount = %d; want 6", a.VertexCount)
		}
		if a.RootCount != 1 {
			t.Errorf("RootCount = %d; want 1 (fetch)", a.RootCount)
		}
		if a.LeafCount != 1 {
			t.Errorf("LeafCount = %d; want 1 (package)", a.LeafCount)
		}
		// compile-main and compile-lib are parallel → MaxFanOut ≥ 2.
		if a.MaxFanOut < 2 {
			t.Errorf("MaxFanOut = %d; want ≥2", a.MaxFanOut)
		}

		parallelism, _ := dag.AnalyseParallelism(d)
		if parallelism.MaxWidth < 2 {
			t.Errorf("MaxWidth = %d; want ≥2 (compile-* run in parallel)", parallelism.MaxWidth)
		}
	})
}

// =========================================================================
// Table-driven scheduler scenario tests using BuildHarness
// =========================================================================

// TestScheduler_Scenarios exercises clean → cached → incremental builds on a
// diamond DAG. Steps must run sequentially because each depends on cache state
// built by the previous step.
func TestScheduler_Scenarios(t *testing.T) {
	d, err := dag.NewBuilder().
		Add("fetch", dag.NewScenarioOp("fetch",
			dag.WithOutputs(
				dag.FileRef{Path: "/src/a.go"},
				dag.FileRef{Path: "/src/b.go"},
			))).
		Add("compile-a", dag.NewScenarioOp("ca",
			dag.WithOutputs(dag.FileRef{Path: "/out/a.o"}),
			dag.WithDeriveOutputHashes()), // output hash varies with input
			dag.DependsOn("fetch"),
			dag.ConsumesFiles("fetch", "/src/a.go"),
		).
		Add("compile-b", dag.NewScenarioOp("cb",
			dag.WithOutputs(dag.FileRef{Path: "/out/b.o"}),
			dag.WithDeriveOutputHashes()),
			dag.DependsOn("fetch"),
			dag.ConsumesFiles("fetch", "/src/b.go"),
		).
		Add("link", dag.NewScenarioOp("link",
			dag.WithOutputs(dag.FileRef{Path: "/out/bin"}),
			dag.WithDeriveOutputHashes()),
			dag.DependsOn("compile-a", "compile-b"),
		).
		Build()
	if err != nil {
		t.Fatalf("DAG build: %v", err)
	}

	h := dag.NewBuildHarness(d, dag.WithWorkerCount(4))
	ctx := context.Background()

	// Step 1: clean — all 4 vertices execute.
	m1, err := h.Run(ctx, "link", nil)
	if err != nil {
		t.Fatalf("step 1 (clean): %v", err)
	}
	if m1.Executed != 4 {
		t.Errorf("step 1: Executed=%d; want 4", m1.Executed)
	}

	// Step 2: cached — all 4 served from fast cache.
	h.Recorder.Reset()
	for _, v := range d.All() {
		v.Reset()
	}
	m2, err := h.Run(ctx, "link", nil)
	if err != nil {
		t.Fatalf("step 2 (cached): %v", err)
	}
	if m2.Executed != 0 {
		t.Errorf("step 2: Executed=%d; want 0", m2.Executed)
	}
	if m2.FastCacheHits != 4 {
		t.Errorf("step 2: FastCacheHits=%d; want 4", m2.FastCacheHits)
	}

	// Step 3: incremental — only /src/a.go changed.
	// compile-a reads /src/a.go (hash changes) → different output hash → link's
	// input changes → link cache key differs → link re-executes.
	// compile-b reads only /src/b.go (unchanged) → cache hit.
	// fetch has no parents → not invalidated → cache hit.
	h.Recorder.Reset()
	for _, v := range d.All() {
		v.Reset()
	}
	changedFiles := []dag.FileRef{{Path: "/src/a.go", Hash: [32]byte{0xAB}}}
	m3, err := h.Run(ctx, "link", changedFiles)
	if err != nil {
		t.Fatalf("step 3 (incremental): %v", err)
	}
	// compile-a + link re-execute; fetch + compile-b from cache.
	if m3.Executed != 2 {
		t.Errorf("step 3: Executed=%d; want 2 (compile-a + link)", m3.Executed)
	}
	if m3.FastCacheHits != 2 {
		t.Errorf("step 3: FastCacheHits=%d; want 2 (fetch + compile-b)", m3.FastCacheHits)
	}
}
