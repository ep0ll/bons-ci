package reactdag_test

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	dag "github.com/bons/bons-ci/plugins/dag"
)

// =========================================================================
// Middleware tests
// =========================================================================

func TestChain_OrderIsCorrect(t *testing.T) {
	// The first middleware listed should be outermost (called first).
	var callOrder []string

	makeMiddleware := func(name string) dag.ExecutorMiddleware {
		return func(next dag.Executor) dag.Executor {
			return dag.NewExecutorFn(func(ctx context.Context, v *dag.Vertex) error {
				callOrder = append(callOrder, name+":before")
				err := next.Execute(ctx, v)
				callOrder = append(callOrder, name+":after")
				return err
			})
		}
	}

	base := dag.NewExecutorFn(func(_ context.Context, _ *dag.Vertex) error {
		callOrder = append(callOrder, "base")
		return nil
	})

	chain := dag.Chain(base,
		makeMiddleware("A"),
		makeMiddleware("B"),
		makeMiddleware("C"),
	)

	v := dag.NewVertex("x", noopOp{id: "x"})
	if err := chain.Execute(context.Background(), v); err != nil {
		t.Fatalf("Execute: %v", err)
	}

	want := []string{"A:before", "B:before", "C:before", "base", "C:after", "B:after", "A:after"}
	if len(callOrder) != len(want) {
		t.Fatalf("call order = %v; want %v", callOrder, want)
	}
	for i, got := range callOrder {
		if got != want[i] {
			t.Errorf("callOrder[%d] = %q; want %q", i, got, want[i])
		}
	}
}

func TestLoggingMiddleware_CapturesOutput(t *testing.T) {
	var buf bytes.Buffer
	logger := newTestLogger(&buf)

	d, _ := buildLinearDAG(t)
	s := dag.NewScheduler(d,
		dag.WithExecutor(dag.Chain(
			dag.NewDefaultExecutorForTest(),
			dag.LoggingMiddleware(logger),
		)),
		dag.WithWorkerCount(2),
	)

	if _, err := s.Build(context.Background(), "A", nil); err != nil {
		t.Fatalf("Build: %v", err)
	}

	out := buf.String()
	if !strings.Contains(out, "execute start") {
		t.Errorf("logging output missing 'execute start': %q", out)
	}
	if !strings.Contains(out, "execute done") {
		t.Errorf("logging output missing 'execute done': %q", out)
	}
}

func TestMetricsMiddleware_RecordsSamples(t *testing.T) {
	sink := dag.NewInMemoryMetricsSink()

	d, _ := buildLinearDAG(t)
	s := dag.NewScheduler(d,
		dag.WithExecutor(dag.Chain(
			dag.NewDefaultExecutorForTest(),
			dag.MetricsMiddleware(sink),
		)),
		dag.WithWorkerCount(2),
	)

	if _, err := s.Build(context.Background(), "A", nil); err != nil {
		t.Fatalf("Build: %v", err)
	}

	samples := sink.Samples()
	if len(samples) != 3 {
		t.Errorf("Samples count = %d; want 3", len(samples))
	}
	if sink.ErrorCount() != 0 {
		t.Errorf("ErrorCount = %d; want 0", sink.ErrorCount())
	}
}

func TestMetricsMiddleware_RecordsErrors(t *testing.T) {
	sink := dag.NewInMemoryMetricsSink()

	d := dag.NewDAG()
	if err := d.AddVertex(dag.NewVertex("A", errOp{id: "A", msg: "boom"})); err != nil {
		t.Fatal(err)
	}
	mustSeal(t, d)

	s := dag.NewScheduler(d,
		dag.WithExecutor(dag.Chain(
			dag.NewDefaultExecutorForTest(),
			dag.MetricsMiddleware(sink),
		)),
		dag.WithWorkerCount(1),
	)

	s.Build(context.Background(), "A", nil) //nolint: errcheck

	if sink.ErrorCount() != 1 {
		t.Errorf("ErrorCount = %d; want 1", sink.ErrorCount())
	}
}

func TestPanicRecoveryMiddleware_ConvertsToError(t *testing.T) {
	panicOp := dag.NewExecutorFn(func(_ context.Context, _ *dag.Vertex) error {
		panic("deliberate panic in test")
	})

	wrapped := dag.Chain(panicOp, dag.PanicRecoveryMiddleware())
	v := dag.NewVertex("x", noopOp{id: "x"})

	err := wrapped.Execute(context.Background(), v)
	if err == nil {
		t.Fatal("expected error from panic recovery")
	}
	if !strings.Contains(err.Error(), "panicked") {
		t.Errorf("error does not mention panic: %v", err)
	}
}

func TestDryRunMiddleware_SkipsRealExecution(t *testing.T) {
	var called atomic.Bool
	op := dag.NewExecutorFn(func(_ context.Context, _ *dag.Vertex) error {
		called.Store(true)
		return nil
	})

	// DryRun replaces the inner executor entirely — op should not be called.
	chain := dag.Chain(op, dag.DryRunMiddleware())
	v := dag.NewVertex("x", noopOp{id: "x"})

	if err := chain.Execute(context.Background(), v); err != nil {
		t.Fatalf("Execute: %v", err)
	}
	if called.Load() {
		t.Error("real executor was called despite DryRunMiddleware")
	}
}

func TestContextValueMiddleware_PropagatesValue(t *testing.T) {
	type ctxKey struct{}
	var captured interface{}

	inner := dag.NewExecutorFn(func(ctx context.Context, _ *dag.Vertex) error {
		captured = ctx.Value(ctxKey{})
		return nil
	})
	chain := dag.Chain(inner, dag.ContextValueMiddleware(ctxKey{}, "test-value"))
	v := dag.NewVertex("x", noopOp{id: "x"})
	chain.Execute(context.Background(), v) //nolint:errcheck

	if captured != "test-value" {
		t.Errorf("context value = %v; want test-value", captured)
	}
}

func TestTracingMiddleware_CallsTracer(t *testing.T) {
	tracer := &countingTracer{}
	inner := dag.NewExecutorFn(func(_ context.Context, _ *dag.Vertex) error { return nil })
	chain := dag.Chain(inner, dag.TracingMiddleware(tracer))
	v := dag.NewVertex("x", noopOp{id: "x"})
	chain.Execute(context.Background(), v) //nolint:errcheck

	if tracer.starts.Load() != 1 {
		t.Errorf("tracer.Start called %d times; want 1", tracer.starts.Load())
	}
	if tracer.ends.Load() != 1 {
		t.Errorf("span.End called %d times; want 1", tracer.ends.Load())
	}
}

// =========================================================================
// Watcher tests
// =========================================================================

func TestWatcher_TriggersRebuildOnChange(t *testing.T) {
	d, _ := buildLinearDAG(t)
	s := dag.NewScheduler(d, dag.WithWorkerCount(2))

	var buildCount atomic.Int64
	source := dag.NewManualChangeSource()
	w := dag.NewWatcher(s, dag.WatcherConfig{
		TargetID: "A",
		Debounce: 5 * time.Millisecond,
		OnBuildEnd: func(_ *dag.BuildMetrics, _ error) {
			buildCount.Add(1)
		},
	})

	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
	defer cancel()

	done := make(chan error, 1)
	go func() { done <- w.Run(ctx, source) }()

	// Trigger a change.
	if err := source.Push(dag.FileRef{Path: "/src/main.go"}); err != nil {
		t.Fatalf("Push: %v", err)
	}

	// Wait for the build to complete.
	deadline := time.After(500 * time.Millisecond)
	for buildCount.Load() == 0 {
		select {
		case <-deadline:
			t.Fatal("watcher did not trigger a rebuild within 500ms")
		case <-time.After(5 * time.Millisecond):
		}
	}

	w.Stop()
	source.Close()
	<-done

	if buildCount.Load() < 1 {
		t.Errorf("buildCount = %d; want ≥1", buildCount.Load())
	}
}

func TestWatcher_DebounceCoalescesChanges(t *testing.T) {
	d, _ := buildLinearDAG(t)
	s := dag.NewScheduler(d, dag.WithWorkerCount(2))

	var buildCount atomic.Int64
	source := dag.NewManualChangeSource()

	w := dag.NewWatcher(s, dag.WatcherConfig{
		TargetID: "A",
		Debounce: 50 * time.Millisecond,
		OnBuildEnd: func(_ *dag.BuildMetrics, _ error) {
			buildCount.Add(1)
		},
	})

	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
	defer cancel()

	done := make(chan error, 1)
	go func() { done <- w.Run(ctx, source) }()

	// Push 5 changes in rapid succession — should coalesce to 1 build.
	for i := range 5 {
		source.Push(dag.FileRef{Path: fmt.Sprintf("/src/file%d.go", i)}) //nolint:errcheck
	}

	time.Sleep(200 * time.Millisecond)

	w.Stop()
	source.Close()
	<-done

	// Due to debounce, should be 1 build (possibly 2 if timing is tight).
	if buildCount.Load() == 0 {
		t.Error("expected at least 1 build")
	}
	if buildCount.Load() > 3 {
		t.Errorf("buildCount = %d; expected debounce to coalesce to ≤3 builds", buildCount.Load())
	}
}

func TestManualChangeSource_ClosePreventsNewPush(t *testing.T) {
	source := dag.NewManualChangeSource()
	source.Close()

	if err := source.Push(dag.FileRef{Path: "/x"}); err == nil {
		t.Error("Push after Close should return error")
	}
}

// =========================================================================
// ProgressTracker tests
// =========================================================================

func TestProgressTracker_TracksBuildLifecycle(t *testing.T) {
	d, _ := buildLinearDAG(t)
	bus := dag.NewEventBus()
	tracker := dag.NewProgressTracker(bus)
	defer tracker.Unsubscribe()

	s := dag.NewScheduler(d,
		dag.WithEventBus(bus),
		dag.WithWorkerCount(2),
	)
	if _, err := s.Build(context.Background(), "A", nil); err != nil {
		t.Fatalf("Build: %v", err)
	}

	if !tracker.IsDone() {
		t.Error("tracker should be done after build completes")
	}

	snaps := tracker.Snapshot()
	if len(snaps) == 0 {
		t.Error("tracker should have progress records")
	}

	_, _, fresh, _, failed := tracker.Summary()
	if failed != 0 {
		t.Errorf("failed = %d; want 0", failed)
	}
	if fresh == 0 {
		t.Errorf("fresh = %d; want >0", fresh)
	}
}

func TestProgressTracker_TracksCacheHits(t *testing.T) {
	d, _ := buildLinearDAG(t)
	fastCache := dag.NewMemoryCacheStore(0)
	bus := dag.NewEventBus()
	tracker := dag.NewProgressTracker(bus)
	defer tracker.Unsubscribe()

	s := dag.NewScheduler(d,
		dag.WithEventBus(bus),
		dag.WithFastCache(fastCache),
		dag.WithWorkerCount(2),
	)

	// Build 1: warms cache.
	if _, err := s.Build(context.Background(), "A", nil); err != nil {
		t.Fatalf("Build 1: %v", err)
	}

	// Reset vertices + tracker for build 2.
	for _, v := range d.All() {
		v.Reset()
	}

	// Build 2: all cache hits.
	if _, err := s.Build(context.Background(), "A", nil); err != nil {
		t.Fatalf("Build 2: %v", err)
	}

	_, _, _, cached, _ := tracker.Summary()
	if cached == 0 {
		t.Error("tracker should report cached vertices on build 2")
	}
}

func TestProgressRenderer_WritesOutput(t *testing.T) {
	d, _ := buildLinearDAG(t)
	bus := dag.NewEventBus()
	tracker := dag.NewProgressTracker(bus)
	defer tracker.Unsubscribe()

	s := dag.NewScheduler(d, dag.WithEventBus(bus), dag.WithWorkerCount(2))
	if _, err := s.Build(context.Background(), "A", nil); err != nil {
		t.Fatalf("Build: %v", err)
	}

	var buf strings.Builder
	renderer := dag.NewProgressRenderer(&buf, tracker, false)
	renderer.Render()

	out := buf.String()
	if !strings.Contains(out, "Build") {
		t.Errorf("renderer output missing 'Build' header: %q", out)
	}
}

// =========================================================================
// BuildHistory tests
// =========================================================================

func TestBuildHistory_Record_And_All(t *testing.T) {
	h := dag.NewBuildHistory(0, "")
	m := &dag.BuildMetrics{
		TotalVertices: 3,
		Executed:      3,
		TotalDuration: 100 * time.Millisecond,
	}

	h.Record("A", time.Now(), m, nil, nil)
	h.Record("A", time.Now(), m, errors.New("fail"), nil)

	records := h.All()
	if len(records) != 2 {
		t.Fatalf("All() = %d records; want 2", len(records))
	}
	if !records[0].Succeeded {
		t.Error("records[0] should be succeeded")
	}
	if records[1].Succeeded {
		t.Error("records[1] should be failed")
	}
}

func TestBuildHistory_MaxSize_Evicts(t *testing.T) {
	h := dag.NewBuildHistory(3, "")
	m := &dag.BuildMetrics{TotalDuration: 10 * time.Millisecond}

	for range 5 {
		h.Record("A", time.Now(), m, nil, nil)
	}

	if len(h.All()) > 3 {
		t.Errorf("All() = %d; want ≤3 (maxSize)", len(h.All()))
	}
}

func TestBuildHistory_Trend_PassRate(t *testing.T) {
	h := dag.NewBuildHistory(0, "")
	m := &dag.BuildMetrics{TotalDuration: 50 * time.Millisecond, Executed: 3}
	fail := errors.New("build failure")

	// 8 pass, 2 fail → 80% pass rate.
	for range 8 {
		h.Record("A", time.Now(), m, nil, nil)
	}
	for range 2 {
		h.Record("A", time.Now(), m, fail, nil)
	}

	stats := h.Trend("A", 100)
	if stats.Count != 10 {
		t.Errorf("Count = %d; want 10", stats.Count)
	}
	if stats.PassRate < 0.79 || stats.PassRate > 0.81 {
		t.Errorf("PassRate = %.2f; want ~0.80", stats.PassRate)
	}
}

func TestBuildHistory_Trend_Percentiles(t *testing.T) {
	h := dag.NewBuildHistory(0, "")

	// 10 builds with durations 10..100ms.
	for i := 1; i <= 10; i++ {
		m := &dag.BuildMetrics{TotalDuration: time.Duration(i*10) * time.Millisecond}
		h.Record("A", time.Now(), m, nil, nil)
	}

	stats := h.Trend("A", 10)
	if stats.P50DurationMS == 0 {
		t.Error("P50 should be non-zero")
	}
	if stats.P95DurationMS < stats.P50DurationMS {
		t.Error("P95 should be >= P50")
	}
}

func TestBuildHistory_Persist_And_Load(t *testing.T) {
	logPath := t.TempDir() + "/history.jsonl"
	h := dag.NewBuildHistory(0, logPath)

	m := &dag.BuildMetrics{TotalDuration: 42 * time.Millisecond, Executed: 5}
	h.Record("target", time.Now(), m, nil, map[string]string{"branch": "main"})

	records, err := dag.LoadHistory(logPath)
	if err != nil {
		t.Fatalf("LoadHistory: %v", err)
	}
	if len(records) != 1 {
		t.Fatalf("LoadHistory returned %d records; want 1", len(records))
	}
	if records[0].TargetID != "target" {
		t.Errorf("TargetID = %q; want target", records[0].TargetID)
	}
	if records[0].DurationMS != 42 {
		t.Errorf("DurationMS = %d; want 42", records[0].DurationMS)
	}
	if records[0].Labels["branch"] != "main" {
		t.Errorf("label branch = %q; want main", records[0].Labels["branch"])
	}
}

func TestBuildHistory_LoadHistory_MissingFile_ReturnsNil(t *testing.T) {
	records, err := dag.LoadHistory("/tmp/does_not_exist_reactdag.jsonl")
	if err != nil {
		t.Fatalf("LoadHistory missing file: %v", err)
	}
	if records != nil {
		t.Error("LoadHistory missing file should return nil records")
	}
}

// =========================================================================
// DAGDiff tests
// =========================================================================

func TestDiff_AddedVertex(t *testing.T) {
	prev := dag.DAGSnapshot{
		States:  map[string]dag.State{"A": dag.StateCompleted},
		Outputs: map[string][]dag.FileRef{},
	}
	next := dag.DAGSnapshot{
		States:  map[string]dag.State{"A": dag.StateCompleted, "B": dag.StateCompleted},
		Outputs: map[string][]dag.FileRef{},
	}

	diffs := dag.Diff(prev, next)
	found := false
	for _, d := range diffs {
		if d.VertexID == "B" && d.ChangeType == dag.DiffAdded {
			found = true
		}
	}
	if !found {
		t.Errorf("expected B to be marked as added; diffs = %v", diffs)
	}
}

func TestDiff_RemovedVertex(t *testing.T) {
	prev := dag.DAGSnapshot{
		States:  map[string]dag.State{"A": dag.StateCompleted, "B": dag.StateCompleted},
		Outputs: map[string][]dag.FileRef{},
	}
	next := dag.DAGSnapshot{
		States:  map[string]dag.State{"A": dag.StateCompleted},
		Outputs: map[string][]dag.FileRef{},
	}

	diffs := dag.Diff(prev, next)
	found := false
	for _, d := range diffs {
		if d.VertexID == "B" && d.ChangeType == dag.DiffRemoved {
			found = true
		}
	}
	if !found {
		t.Errorf("expected B to be marked as removed; diffs = %v", diffs)
	}
}

func TestDiff_StateChanged(t *testing.T) {
	prev := dag.DAGSnapshot{
		States:  map[string]dag.State{"A": dag.StateInitial},
		Outputs: map[string][]dag.FileRef{},
	}
	next := dag.DAGSnapshot{
		States:  map[string]dag.State{"A": dag.StateCompleted},
		Outputs: map[string][]dag.FileRef{},
	}

	diffs := dag.Diff(prev, next)
	if len(diffs) == 0 {
		t.Fatal("expected at least one diff")
	}
	var found bool
	for _, d := range diffs {
		if d.VertexID == "A" && d.ChangeType == dag.DiffStateChanged {
			if d.PrevState != dag.StateInitial || d.NextState != dag.StateCompleted {
				t.Errorf("state diff A: prev=%s next=%s; want initial→completed",
					d.PrevState, d.NextState)
			}
			found = true
		}
	}
	if !found {
		t.Error("expected StateChanged diff for A")
	}
}

func TestDiff_OutputsChanged(t *testing.T) {
	prev := dag.DAGSnapshot{
		States: map[string]dag.State{"A": dag.StateCompleted},
		Outputs: map[string][]dag.FileRef{
			"A": {{Path: "/out/x", Hash: [32]byte{1}}},
		},
	}
	next := dag.DAGSnapshot{
		States: map[string]dag.State{"A": dag.StateCompleted},
		Outputs: map[string][]dag.FileRef{
			"A": {{Path: "/out/x", Hash: [32]byte{2}}}, // hash changed
		},
	}

	diffs := dag.Diff(prev, next)
	found := false
	for _, d := range diffs {
		if d.VertexID == "A" && d.ChangeType == dag.DiffOutputsChanged {
			found = true
			if len(d.ModifiedFiles) != 1 || d.ModifiedFiles[0] != "/out/x" {
				t.Errorf("ModifiedFiles = %v; want [/out/x]", d.ModifiedFiles)
			}
		}
	}
	if !found {
		t.Error("expected OutputsChanged diff for A")
	}
}

func TestDiff_NoChanges_EmptySlice(t *testing.T) {
	snap := dag.DAGSnapshot{
		States:  map[string]dag.State{"A": dag.StateCompleted},
		Outputs: map[string][]dag.FileRef{"A": {{Path: "/x", Hash: [32]byte{1}}}},
	}
	diffs := dag.Diff(snap, snap)
	if len(diffs) != 0 {
		t.Errorf("expected no diffs for identical snapshots; got %v", diffs)
	}
}

func TestTakeSnapshot_CapturesDAGState(t *testing.T) {
	d, _ := buildLinearDAG(t)
	s := dag.NewScheduler(d, dag.WithWorkerCount(2))
	if _, err := s.Build(context.Background(), "A", nil); err != nil {
		t.Fatalf("Build: %v", err)
	}

	snap := dag.TakeSnapshot(d)
	for _, id := range []string{"A", "B", "C"} {
		st, ok := snap.States[id]
		if !ok {
			t.Errorf("snapshot missing vertex %s", id)
		}
		if st != dag.StateCompleted {
			t.Errorf("snapshot[%s] = %s; want completed", id, st)
		}
	}
}

func TestDiffSummary_ContainsCounts(t *testing.T) {
	diffs := []dag.VertexDiff{
		{VertexID: "A", ChangeType: dag.DiffAdded},
		{VertexID: "B", ChangeType: dag.DiffStateChanged},
		{VertexID: "C", ChangeType: dag.DiffStateChanged},
	}
	summary := dag.DiffSummary(diffs)
	if !strings.Contains(summary, "1 added") {
		t.Errorf("summary missing '1 added': %s", summary)
	}
	if !strings.Contains(summary, "2 state_changed") {
		t.Errorf("summary missing '2 state_changed': %s", summary)
	}
}

func TestRenderDiff_OutputsTable(t *testing.T) {
	diffs := []dag.VertexDiff{
		{VertexID: "vertex-A", ChangeType: dag.DiffStateChanged, PrevState: dag.StateInitial, NextState: dag.StateCompleted},
	}
	rendered := dag.RenderDiff(diffs)
	if !strings.Contains(rendered, "vertex-A") {
		t.Errorf("rendered diff missing vertex-A: %s", rendered)
	}
}

// =========================================================================
// Integration: full stack with middleware + progress + history + diff
// =========================================================================

func TestIntegration_FullStack(t *testing.T) {
	d, _ := buildLinearDAG(t)

	fastCache := dag.NewMemoryCacheStore(0)
	bus := dag.NewEventBus()
	sink := dag.NewInMemoryMetricsSink()
	history := dag.NewBuildHistory(100, "")
	tracker := dag.NewProgressTracker(bus)
	defer tracker.Unsubscribe()

	base := dag.NewDefaultExecutorForTest()
	exec := dag.Chain(base,
		dag.PanicRecoveryMiddleware(),
		dag.MetricsMiddleware(sink),
		dag.LoggingMiddleware(nil),
	)

	s := dag.NewScheduler(d,
		dag.WithWorkerCount(4),
		dag.WithFastCache(fastCache),
		dag.WithExecutor(exec),
		dag.WithEventBus(bus),
	)

	buildStart := time.Now()
	snap1 := dag.TakeSnapshot(d)

	m, err := s.Build(context.Background(), "A", nil)
	if err != nil {
		t.Fatalf("Build: %v", err)
	}

	// Record in history.
	history.Record("A", buildStart, m, nil, map[string]string{"run": "1"})

	snap2 := dag.TakeSnapshot(d)
	diffs := dag.Diff(snap1, snap2)

	// Verify diff shows state transitions.
	if len(diffs) == 0 {
		t.Error("expected state diffs after build")
	}

	// Verify metrics sink collected samples.
	if len(sink.Samples()) != 3 {
		t.Errorf("expected 3 execution samples; got %d", len(sink.Samples()))
	}

	// Verify tracker.
	if !tracker.IsDone() {
		t.Error("tracker should be done")
	}

	// Verify history trend.
	stats := history.Trend("A", 10)
	if stats.PassRate != 1.0 {
		t.Errorf("PassRate = %f; want 1.0", stats.PassRate)
	}

	// Render progress.
	var buf strings.Builder
	renderer := dag.NewProgressRenderer(&buf, tracker, false)
	renderer.Render()
	if buf.Len() == 0 {
		t.Error("renderer produced no output")
	}
}

// =========================================================================
// Test helpers
// =========================================================================

func newTestLogger(buf *bytes.Buffer) interface{ Printf(string, ...interface{}) } {
	return testLogger{buf}
}

type testLogger struct{ buf *bytes.Buffer }

func (l testLogger) Printf(format string, args ...interface{}) {
	fmt.Fprintf(l.buf, format+"\n", args...)
}

// countingTracer counts Start calls.
type countingTracer struct {
	starts atomic.Int64
	ends   atomic.Int64
}

type countingSpan struct {
	ends *atomic.Int64
}

func (s *countingSpan) End() {
	if s.ends != nil {
		s.ends.Add(1)
	}
}
func (s *countingSpan) SetAttribute(_ string, _ interface{}) {}
func (s *countingSpan) RecordError(_ error)                  {}

func (tr *countingTracer) Start(ctx context.Context, _ string, _ map[string]interface{}) (context.Context, dag.Span) {
	tr.starts.Add(1)
	return ctx, &countingSpan{ends: &tr.ends}
}
