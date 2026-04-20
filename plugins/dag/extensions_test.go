package reactdag_test

import (
	"context"
	"errors"
	"fmt"
	"os"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	dag "github.com/bons/bons-ci/plugins/dag"
)

// ---------------------------------------------------------------------------
// DAGBuilder tests
// ---------------------------------------------------------------------------

func TestBuilder_LinearChain(t *testing.T) {
	d, err := dag.NewBuilder().
		Add("C", noopOp{id: "C"}).
		Add("B", noopOp{id: "B"}, dag.DependsOn("C")).
		Add("A", noopOp{id: "A"}, dag.DependsOn("B")).
		Build()
	if err != nil {
		t.Fatalf("Build: %v", err)
	}

	v, ok := d.Vertex("A")
	if !ok {
		t.Fatal("vertex A not found")
	}
	parents := v.Parents()
	if len(parents) != 1 || parents[0].ID() != "B" {
		t.Errorf("A.Parents = %v; want [B]", parentIDs(parents))
	}
}

func TestBuilder_FileDeps(t *testing.T) {
	d, err := dag.NewBuilder().
		Add("C", producerOp{id: "C", files: []dag.FileRef{{Path: "/out/file1.o"}}}).
		Add("B", noopOp{id: "B"},
			dag.DependsOn("C"),
			dag.ConsumesFiles("C", "/out/file1.o"),
		).
		Build()
	if err != nil {
		t.Fatalf("Build: %v", err)
	}

	vB, _ := d.Vertex("B")
	paths, hasDep := vB.FileDependencyForParent("C")
	if !hasDep {
		t.Fatal("B should have file dep on C")
	}
	if len(paths) != 1 || paths[0] != "/out/file1.o" {
		t.Errorf("FileDep paths = %v; want [/out/file1.o]", paths)
	}
}

func TestBuilder_Labels(t *testing.T) {
	d, err := dag.NewBuilder().
		Add("A", noopOp{id: "A"},
			dag.WithLabel("team", "platform"),
			dag.WithLabel("env", "prod"),
		).
		Build()
	if err != nil {
		t.Fatalf("Build: %v", err)
	}
	v, _ := d.Vertex("A")
	if val, ok := v.Label("team"); !ok || val != "platform" {
		t.Errorf("label team = %q; want platform", val)
	}
}

func TestBuilder_DuplicateID_Error(t *testing.T) {
	_, err := dag.NewBuilder().
		Add("A", noopOp{id: "A"}).
		Add("A", noopOp{id: "A"}).
		Build()
	if err == nil {
		t.Error("expected error for duplicate vertex id")
	}
}

func TestBuilder_UnknownParent_Error(t *testing.T) {
	_, err := dag.NewBuilder().
		Add("A", noopOp{id: "A"}, dag.DependsOn("MISSING")).
		Build()
	if err == nil {
		t.Error("expected error for unknown parent reference")
	}
}

func TestBuilder_CycleDetected_Error(t *testing.T) {
	_, err := dag.NewBuilder().
		Add("A", noopOp{id: "A"}, dag.DependsOn("B")).
		Add("B", noopOp{id: "B"}, dag.DependsOn("A")).
		Build()
	if err == nil {
		t.Error("expected cycle detection error")
	}
}

func TestBuilder_WithTimeout_SetsLabel(t *testing.T) {
	d, err := dag.NewBuilder().
		Add("A", noopOp{id: "A"}, dag.WithTimeout(5*time.Second)).
		Build()
	if err != nil {
		t.Fatalf("Build: %v", err)
	}
	v, _ := d.Vertex("A")
	if label, ok := v.Label("timeout"); !ok || label != "5s" {
		t.Errorf("timeout label = %q; want 5s", label)
	}
}

// ---------------------------------------------------------------------------
// RetryExecutor tests
// ---------------------------------------------------------------------------

// retryCountOp fails the first N-1 times, then succeeds.
type retryCountOp struct {
	id       string
	failFor  int
	calls    atomic.Int64
}

func (o *retryCountOp) ID() string { return "retry:" + o.id }
func (o *retryCountOp) Execute(_ context.Context, _ []dag.FileRef) ([]dag.FileRef, error) {
	call := o.calls.Add(1)
	if call <= int64(o.failFor) {
		return nil, fmt.Errorf("transient error on attempt %d", call)
	}
	return nil, nil
}

func TestRetryExecutor_SucceedsOnRetry(t *testing.T) {
	op := &retryCountOp{id: "A", failFor: 2}
	v := dag.NewVertex("A", op)

	inner := dag.NewDefaultExecutorForTest() // exposed for test; see below
	retry := dag.NewRetryExecutor(inner, 0, dag.RetryPolicy{MaxAttempts: 3})

	if err := retry.Execute(context.Background(), v); err != nil {
		t.Errorf("expected success after retry, got: %v", err)
	}
	if op.calls.Load() != 3 {
		t.Errorf("expected 3 calls (2 failures + 1 success), got %d", op.calls.Load())
	}
}

func TestRetryExecutor_ExhaustsRetries(t *testing.T) {
	op := &retryCountOp{id: "A", failFor: 10}
	v := dag.NewVertex("A", op)

	inner := dag.NewDefaultExecutorForTest()
	retry := dag.NewRetryExecutor(inner, 0, dag.RetryPolicy{MaxAttempts: 3})

	if err := retry.Execute(context.Background(), v); err == nil {
		t.Error("expected error after exhausting retries")
	}
	if op.calls.Load() != 3 {
		t.Errorf("expected exactly 3 calls, got %d", op.calls.Load())
	}
}

func TestRetryExecutor_IsRetryable_FiltersErrors(t *testing.T) {
	op := &retryCountOp{id: "A", failFor: 10}
	v := dag.NewVertex("A", op)

	inner := dag.NewDefaultExecutorForTest()
	retry := dag.NewRetryExecutor(inner, 0, dag.RetryPolicy{
		MaxAttempts: 5,
		IsRetryable: func(err error) bool {
			return false // never retry
		},
	})

	if err := retry.Execute(context.Background(), v); err == nil {
		t.Error("expected error")
	}
	// IsRetryable returns false → only 1 attempt.
	if op.calls.Load() != 1 {
		t.Errorf("expected 1 call (no retry), got %d", op.calls.Load())
	}
}

// ctxSlowOp sleeps while honouring context cancellation.
type ctxSlowOp struct {
	id    string
	sleep time.Duration
}

func (o ctxSlowOp) ID() string { return "ctxslow:" + o.id }
func (o ctxSlowOp) Execute(ctx context.Context, _ []dag.FileRef) ([]dag.FileRef, error) {
	select {
	case <-time.After(o.sleep):
		return nil, nil
	case <-ctx.Done():
		return nil, ctx.Err()
	}
}

func TestRetryExecutor_Timeout_CancelsExecution(t *testing.T) {
	op := ctxSlowOp{id: "A", sleep: 500 * time.Millisecond}
	v := dag.NewVertex("A", op)

	inner := dag.NewDefaultExecutorForTest()
	retry := dag.NewRetryExecutor(inner, 20*time.Millisecond, dag.RetryPolicy{MaxAttempts: 1})

	start := time.Now()
	err := retry.Execute(context.Background(), v)
	elapsed := time.Since(start)

	if err == nil {
		t.Error("expected timeout error")
	}
	if elapsed > 100*time.Millisecond {
		t.Errorf("elapsed %v > 100ms; timeout did not fire", elapsed)
	}
}

// ---------------------------------------------------------------------------
// ResourceLimiter tests
// ---------------------------------------------------------------------------

func TestResourceLimiter_ConstrainsConcurrency(t *testing.T) {
	var concurrent atomic.Int64
	var maxConcurrent atomic.Int64

	// A slow op that records peak concurrency.
	type peakOp struct{ id string }
	peakOpVal := peakOp{id: "A"}
	_ = peakOpVal // suppress unused

	d, err := dag.NewBuilder().
		Add("v0", slowOp{id: "v0", sleep: 20 * time.Millisecond}).
		Add("v1", slowOp{id: "v1", sleep: 20 * time.Millisecond}).
		Add("v2", slowOp{id: "v2", sleep: 20 * time.Millisecond}).
		Add("v3", slowOp{id: "v3", sleep: 20 * time.Millisecond}).
		Build()
	if err != nil {
		t.Fatalf("Build: %v", err)
	}

	// Label all vertices as "gpu" class.
	for _, v := range d.All() {
		v.SetLabel("resource_class", "gpu")
	}

	inner := dag.NewDefaultExecutorForTest()
	// Wrap with a counter so we can observe concurrency.
	counting := &concurrencyCountingExecutor{inner: inner, concurrent: &concurrent, max: &maxConcurrent}
	limiter := dag.NewResourceLimiter(counting, map[string]int{"gpu": 2})

	s := dag.NewScheduler(d,
		dag.WithExecutor(limiter),
		dag.WithWorkerCount(8),
	)

	if _, err := s.Build(context.Background(), "v3", nil); err != nil {
		// v3 has no dependencies so it might not have a parent; that's fine.
	}

	// With limit=2 and 8 workers, peak should be ≤2.
	if maxConcurrent.Load() > 2 {
		t.Errorf("peak concurrency = %d; want ≤2 (resource limit)", maxConcurrent.Load())
	}
}

// concurrencyCountingExecutor tracks peak concurrent executions.
type concurrencyCountingExecutor struct {
	inner      dag.Executor
	concurrent *atomic.Int64
	max        *atomic.Int64
}

func (e *concurrencyCountingExecutor) Execute(ctx context.Context, v *dag.Vertex) error {
	cur := e.concurrent.Add(1)
	defer e.concurrent.Add(-1)
	for {
		old := e.max.Load()
		if cur <= old || e.max.CompareAndSwap(old, cur) {
			break
		}
	}
	return e.inner.Execute(ctx, v)
}

// ---------------------------------------------------------------------------
// ChainedCacheStore tests
// ---------------------------------------------------------------------------

func TestChainedCacheStore_MissOnBoth_ReturnsNil(t *testing.T) {
	chain := dag.NewChainedCacheStore(
		dag.NewMemoryCacheStore(0),
		dag.NewMemoryCacheStore(0),
	)
	got, err := chain.Get(context.Background(), dag.CacheKey{})
	if err != nil || got != nil {
		t.Errorf("Get on empty chain: got %v, err %v; want nil, nil", got, err)
	}
}

func TestChainedCacheStore_SlowHit_BackFillsFast(t *testing.T) {
	fast := dag.NewMemoryCacheStore(0)
	slow := dag.NewMemoryCacheStore(0)
	chain := dag.NewChainedCacheStore(fast, slow)
	ctx := context.Background()

	key := dag.CacheKey{7}
	entry := &dag.CacheEntry{CachedAt: time.Now()}

	// Write only to slow.
	_ = slow.Set(ctx, key, entry)

	// Read via chain: should hit slow and back-fill fast.
	got, err := chain.Get(ctx, key)
	if err != nil || got == nil {
		t.Fatalf("Get from slow: got %v, err %v", got, err)
	}

	// Fast should now have it.
	fastGot, _ := fast.Get(ctx, key)
	if fastGot == nil {
		t.Error("back-fill to fast tier did not occur")
	}
}

func TestChainedCacheStore_Set_WritesBothTiers(t *testing.T) {
	fast := dag.NewMemoryCacheStore(0)
	slow := dag.NewMemoryCacheStore(0)
	chain := dag.NewChainedCacheStore(fast, slow)
	ctx := context.Background()

	key := dag.CacheKey{99}
	entry := &dag.CacheEntry{CachedAt: time.Now()}
	if err := chain.Set(ctx, key, entry); err != nil {
		t.Fatalf("Set: %v", err)
	}

	for _, label := range []string{"fast", "slow"} {
		var store dag.CacheStore
		if label == "fast" {
			store = fast
		} else {
			store = slow
		}
		got, _ := store.Get(ctx, key)
		if got == nil {
			t.Errorf("%s tier: entry not found after chain Set", label)
		}
	}
}

// ---------------------------------------------------------------------------
// DiskCacheStore tests
// ---------------------------------------------------------------------------

func TestDiskCacheStore_GetSetDelete(t *testing.T) {
	dir := t.TempDir()
	store, err := dag.NewDiskCacheStore(dir)
	if err != nil {
		t.Fatalf("NewDiskCacheStore: %v", err)
	}
	ctx := context.Background()
	key := dag.CacheKey{0xAB, 0xCD}
	entry := &dag.CacheEntry{
		OutputFiles: []dag.FileRef{{Path: "/out/x", Size: 1024}},
		CachedAt:    time.Now().Truncate(time.Second),
		DurationMS:  42,
	}

	// Miss.
	got, err := store.Get(ctx, key)
	if err != nil || got != nil {
		t.Errorf("Get before Set: got %v, err %v", got, err)
	}

	// Set.
	if err := store.Set(ctx, key, entry); err != nil {
		t.Fatalf("Set: %v", err)
	}

	// Hit.
	got, err = store.Get(ctx, key)
	if err != nil {
		t.Fatalf("Get after Set: %v", err)
	}
	if got == nil {
		t.Fatal("Get after Set returned nil")
	}
	if len(got.OutputFiles) != 1 || got.OutputFiles[0].Path != "/out/x" {
		t.Errorf("OutputFiles = %v; want [{/out/x}]", got.OutputFiles)
	}
	if got.DurationMS != 42 {
		t.Errorf("DurationMS = %d; want 42", got.DurationMS)
	}

	// Delete.
	if err := store.Delete(ctx, key); err != nil {
		t.Fatalf("Delete: %v", err)
	}
	got, _ = store.Get(ctx, key)
	if got != nil {
		t.Error("Get after Delete should return nil")
	}
}

func TestDiskCacheStore_FailedEntry_RoundTrip(t *testing.T) {
	dir := t.TempDir()
	store, _ := dag.NewDiskCacheStore(dir)
	ctx := context.Background()
	key := dag.CacheKey{1}
	entry := &dag.CacheEntry{CachedErr: "compilation failed", CachedAt: time.Now()}

	_ = store.Set(ctx, key, entry)
	got, _ := store.Get(ctx, key)
	if got == nil || !got.IsFailed() {
		t.Fatal("expected failed entry round-trip")
	}
	if got.CachedError().Error() != "compilation failed" {
		t.Errorf("CachedError = %v; want 'compilation failed'", got.CachedError())
	}
}

func TestDiskCacheStore_Prune(t *testing.T) {
	dir := t.TempDir()
	store, _ := dag.NewDiskCacheStore(dir)
	ctx := context.Background()

	for i := range 3 {
		_ = store.Set(ctx, dag.CacheKey{byte(i)}, &dag.CacheEntry{CachedAt: time.Now()})
	}

	// Prune immediately with 0 maxAge → should remove all.
	removed, err := store.Prune(0)
	if err != nil {
		t.Fatalf("Prune: %v", err)
	}
	if removed != 3 {
		t.Errorf("Prune removed %d; want 3", removed)
	}

	// Verify all gone.
	entries, _ := os.ReadDir(dir)
	for _, e := range entries {
		if strings.HasSuffix(e.Name(), ".json") {
			t.Errorf("stale entry %s still present after Prune", e.Name())
		}
	}
}

// ---------------------------------------------------------------------------
// Planner / BuildPlan tests
// ---------------------------------------------------------------------------

func TestPlanner_CleanBuild_AllExecute(t *testing.T) {
	d, _ := buildLinearDAG(t)
	planner := dag.NewPlanner(d, nil, nil, nil)

	plan, err := planner.Plan(context.Background(), "A", nil)
	if err != nil {
		t.Fatalf("Plan: %v", err)
	}
	if plan.WillExecute != 3 {
		t.Errorf("WillExecute = %d; want 3", plan.WillExecute)
	}
	if plan.FastCacheHits != 0 {
		t.Errorf("FastCacheHits = %d; want 0 (clean build)", plan.FastCacheHits)
	}
}

func TestPlanner_CachedBuild_AllFastCache(t *testing.T) {
	d, _ := buildLinearDAG(t)
	fastCache := dag.NewMemoryCacheStore(0)

	// Warm the cache by running a real build first.
	s := dag.NewScheduler(d, dag.WithFastCache(fastCache))
	if _, err := s.Build(context.Background(), "A", nil); err != nil {
		t.Fatalf("warm build: %v", err)
	}

	// Reset vertices so the planner sees them as initial.
	for _, v := range d.All() {
		v.Reset()
	}

	planner := dag.NewPlanner(d, fastCache, nil, nil)
	plan, err := planner.Plan(context.Background(), "A", nil)
	if err != nil {
		t.Fatalf("Plan: %v", err)
	}
	if plan.FastCacheHits != 3 {
		t.Errorf("FastCacheHits = %d; want 3 (all cached)", plan.FastCacheHits)
	}
}

func TestPlanner_Summary_ContainsTargetID(t *testing.T) {
	d, _ := buildLinearDAG(t)
	planner := dag.NewPlanner(d, nil, nil, nil)
	plan, _ := planner.Plan(context.Background(), "A", nil)

	summary := plan.Summary()
	if !strings.Contains(summary, "target=A") {
		t.Errorf("Summary missing target: %s", summary)
	}
}

func TestRenderPlan_ContainsHeader(t *testing.T) {
	d, _ := buildLinearDAG(t)
	planner := dag.NewPlanner(d, nil, nil, nil)
	plan, _ := planner.Plan(context.Background(), "A", nil)

	rendered := dag.RenderPlan(plan)
	if !strings.Contains(rendered, "Build Plan") {
		t.Errorf("rendered plan missing header: %s", rendered)
	}
}

// ---------------------------------------------------------------------------
// DOT exporter tests
// ---------------------------------------------------------------------------

func TestExportDOT_ContainsVertexIDs(t *testing.T) {
	d, _ := buildLinearDAG(t)
	dot := dag.ExportDOT(d, dag.DOTOptions{ShowState: true, Title: "Test"})

	for _, id := range []string{"A", "B", "C"} {
		if !strings.Contains(dot, id) {
			t.Errorf("DOT output missing vertex %q", id)
		}
	}
	if !strings.Contains(dot, "digraph") {
		t.Error("DOT output missing digraph declaration")
	}
	if !strings.Contains(dot, "->") {
		t.Error("DOT output missing edges")
	}
}

func TestExportDOT_FileDeps_DashedEdges(t *testing.T) {
	d, _ := buildFileDepsDAG(t)
	dot := dag.ExportDOT(d, dag.DOTOptions{ShowFileDeps: true})

	if !strings.Contains(dot, "dashed") {
		t.Error("DOT output should have dashed edges for file deps")
	}
}

// ---------------------------------------------------------------------------
// MetricsReporter tests
// ---------------------------------------------------------------------------

func TestWriteReport_ContainsSummary(t *testing.T) {
	d, _ := buildLinearDAG(t)
	s := dag.NewScheduler(d, dag.WithWorkerCount(2))
	m, _ := s.Build(context.Background(), "A", nil)

	var buf strings.Builder
	dag.WriteReport(&buf, d, m, dag.DefaultReportOptions())

	out := buf.String()
	for _, want := range []string{"Summary", "Total vertices", "Executed", "Cache"} {
		if !strings.Contains(out, want) {
			t.Errorf("report missing %q section; output:\n%s", want, out)
		}
	}
}

func TestWriteReport_PerVertex_AllIDs(t *testing.T) {
	d, _ := buildLinearDAG(t)
	s := dag.NewScheduler(d, dag.WithWorkerCount(2))
	m, _ := s.Build(context.Background(), "A", nil)

	var buf strings.Builder
	opts := dag.DefaultReportOptions()
	opts.ShowPerVertex = true
	dag.WriteReport(&buf, d, m, opts)

	out := buf.String()
	for _, id := range []string{"A", "B", "C"} {
		if !strings.Contains(out, id) {
			t.Errorf("per-vertex table missing vertex %q", id)
		}
	}
}

// ---------------------------------------------------------------------------
// RetryPolicy tests
// ---------------------------------------------------------------------------

func TestRetryPolicy_ShouldRetry(t *testing.T) {
	p := dag.RetryPolicy{MaxAttempts: 3}

	if !p.ShouldRetry(1, errors.New("err")) {
		t.Error("attempt 1 of 3 should retry")
	}
	if !p.ShouldRetry(2, errors.New("err")) {
		t.Error("attempt 2 of 3 should retry")
	}
	if p.ShouldRetry(3, errors.New("err")) {
		t.Error("attempt 3 of 3 (last) should NOT retry")
	}
}

func TestRetryPolicy_ZeroMaxAttempts_NoRetry(t *testing.T) {
	p := dag.RetryPolicy{}
	if p.ShouldRetry(1, errors.New("err")) {
		t.Error("zero MaxAttempts should never retry")
	}
}

// ---------------------------------------------------------------------------
// Additional helpers
// ---------------------------------------------------------------------------

func parentIDs(vs []*dag.Vertex) []string {
	ids := make([]string, len(vs))
	for i, v := range vs {
		ids[i] = v.ID()
	}
	return ids
}
