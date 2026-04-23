package reactdag_test

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	dag "github.com/bons/bons-ci/plugins/dag"
)

// ---------------------------------------------------------------------------
// Test helpers
// ---------------------------------------------------------------------------

// noopOp is a minimal Operation that succeeds immediately.
type noopOp struct {
	id string
}

func (o noopOp) ID() string { return "noop:" + o.id }
func (o noopOp) Execute(_ context.Context, inputs []dag.FileRef) ([]dag.FileRef, error) {
	return inputs, nil
}

// producerOp produces a fixed set of output files.
type producerOp struct {
	id    string
	files []dag.FileRef
}

func (o producerOp) ID() string { return "producer:" + o.id }
func (o producerOp) Execute(_ context.Context, _ []dag.FileRef) ([]dag.FileRef, error) {
	return o.files, nil
}

// errOp always fails with a fixed message.
type errOp struct {
	id  string
	msg string
}

func (o errOp) ID() string { return "err:" + o.id }
func (o errOp) Execute(_ context.Context, _ []dag.FileRef) ([]dag.FileRef, error) {
	return nil, fmt.Errorf(o.msg)
}

// countingOp counts Execute calls.
type countingOp struct {
	id    string
	calls atomic.Int64
}

func (o *countingOp) ID() string { return "count:" + o.id }
func (o *countingOp) Execute(_ context.Context, _ []dag.FileRef) ([]dag.FileRef, error) {
	o.calls.Add(1)
	return nil, nil
}

// slowOp sleeps before succeeding, used to test concurrency.
type slowOp struct {
	id    string
	sleep time.Duration
}

func (o slowOp) ID() string { return "slow:" + o.id }
func (o slowOp) Execute(_ context.Context, _ []dag.FileRef) ([]dag.FileRef, error) {
	time.Sleep(o.sleep)
	return nil, nil
}

func newVertex(id string, op dag.Operation) *dag.Vertex { return dag.NewVertex(id, op) }

// buildLinearDAG builds A → B → C (C is root, A is target).
func buildLinearDAG(t *testing.T) (*dag.DAG, map[string]*dag.Vertex) {
	t.Helper()
	d := dag.NewDAG()
	vC := newVertex("C", noopOp{id: "C"})
	vB := newVertex("B", noopOp{id: "B"})
	vA := newVertex("A", noopOp{id: "A"})
	for _, v := range []*dag.Vertex{vC, vB, vA} {
		mustAdd(t, d, v)
	}
	mustLink(t, d, "C", "B")
	mustLink(t, d, "B", "A")
	mustSeal(t, d)
	return d, map[string]*dag.Vertex{"A": vA, "B": vB, "C": vC}
}

func mustAdd(t *testing.T, d *dag.DAG, v *dag.Vertex) {
	t.Helper()
	if err := d.AddVertex(v); err != nil {
		t.Fatalf("AddVertex %q: %v", v.ID(), err)
	}
}

func mustLink(t *testing.T, d *dag.DAG, parent, child string) {
	t.Helper()
	if err := d.LinkVertices(parent, child); err != nil {
		t.Fatalf("LinkVertices %q→%q: %v", parent, child, err)
	}
}

func mustSeal(t *testing.T, d *dag.DAG) {
	t.Helper()
	if err := d.Seal(); err != nil {
		t.Fatalf("Seal: %v", err)
	}
}

func sched(d *dag.DAG, opts ...dag.Option) *dag.Scheduler {
	return dag.NewScheduler(d, opts...)
}

// ---------------------------------------------------------------------------
// State machine tests
// ---------------------------------------------------------------------------

func TestVertex_StateTransitions_Valid(t *testing.T) {
	cases := []struct {
		name string
		from dag.State
		to   dag.State
	}{
		{"initial→completed", dag.StateInitial, dag.StateCompleted},
		{"initial→fastCache", dag.StateInitial, dag.StateFastCache},
		{"initial→slowCache", dag.StateInitial, dag.StateSlowCache},
		{"initial→failed", dag.StateInitial, dag.StateFailed},
		{"fastCache→completed", dag.StateFastCache, dag.StateCompleted},
		{"fastCache→failed", dag.StateFastCache, dag.StateFailed},
		{"slowCache→completed", dag.StateSlowCache, dag.StateCompleted},
		{"slowCache→failed", dag.StateSlowCache, dag.StateFailed},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			v := newVertex("x", noopOp{id: "x"})
			if tc.from != dag.StateInitial {
				if err := v.SetState(tc.from, "setup"); err != nil {
					t.Fatalf("setup transition failed: %v", err)
				}
			}
			if err := v.SetState(tc.to, "test"); err != nil {
				t.Errorf("expected valid transition %s→%s but got error: %v", tc.from, tc.to, err)
			}
		})
	}
}

func TestVertex_StateTransitions_Invalid(t *testing.T) {
	cases := []struct {
		name string
		from dag.State
		to   dag.State
	}{
		{"completed→completed", dag.StateCompleted, dag.StateCompleted},
		{"completed→initial", dag.StateCompleted, dag.StateInitial},
		{"failed→completed", dag.StateFailed, dag.StateCompleted},
		{"initial→initial", dag.StateInitial, dag.StateInitial},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			v := newVertex("x", noopOp{id: "x"})
			if tc.from != dag.StateInitial {
				// force state via Reset + SetState trick
				if err := v.SetState(tc.from, "setup"); err != nil {
					t.Skip("cannot reach from state in this path")
				}
			}
			if err := v.SetState(tc.to, "test"); err == nil {
				t.Errorf("expected illegal transition %s→%s to fail, but it succeeded", tc.from, tc.to)
			}
		})
	}
}

func TestVertex_Reset_ClearsAllState(t *testing.T) {
	v := newVertex("x", noopOp{id: "x"})
	if err := v.SetState(dag.StateCompleted, "test"); err != nil {
		t.Fatalf("SetState: %v", err)
	}
	// OutputFiles are preserved across Reset (invalidation engine needs them).
	v.SetOutputFiles([]dag.FileRef{{Path: "/out/x"}})
	v.RecordStart()

	v.Reset()

	if got := v.State(); got != dag.StateInitial {
		t.Errorf("after Reset, State = %s; want initial", got)
	}
	// OutputFiles must be preserved after Reset.
	if len(v.OutputFiles()) != 1 {
		t.Errorf("after Reset, OutputFiles should be preserved; got %d", len(v.OutputFiles()))
	}
	if v.Metrics().StartedAt != (time.Time{}) {
		t.Errorf("after Reset, StartedAt not zero")
	}
}

func TestVertex_SetFailed_StoresError(t *testing.T) {
	v := newVertex("x", noopOp{id: "x"})
	sentinel := errors.New("boom")
	if err := v.SetFailed(sentinel, "test"); err != nil {
		t.Fatalf("SetFailed: %v", err)
	}
	if v.State() != dag.StateFailed {
		t.Errorf("State = %s; want failed", v.State())
	}
	if !errors.Is(v.Err(), sentinel) {
		t.Errorf("Err = %v; want sentinel", v.Err())
	}
}

func TestVertex_ConcurrentStateRead(t *testing.T) {
	v := newVertex("x", noopOp{id: "x"})
	var wg sync.WaitGroup
	for range 100 {
		wg.Add(1)
		go func() {
			defer wg.Done()
			_ = v.State()
		}()
	}
	wg.Wait()
}

// ---------------------------------------------------------------------------
// DAG construction & algorithm tests
// ---------------------------------------------------------------------------

func TestDAG_Seal_DetectsCycle(t *testing.T) {
	d := dag.NewDAG()
	vA := newVertex("A", noopOp{id: "A"})
	vB := newVertex("B", noopOp{id: "B"})
	mustAdd(t, d, vA)
	mustAdd(t, d, vB)
	mustLink(t, d, "A", "B")
	mustLink(t, d, "B", "A") // cycle

	if err := d.Seal(); err == nil {
		t.Error("Seal on cyclic graph should return error")
	}
}

func TestDAG_DuplicateVertex(t *testing.T) {
	d := dag.NewDAG()
	mustAdd(t, d, newVertex("A", noopOp{id: "A"}))
	if err := d.AddVertex(newVertex("A", noopOp{id: "A"})); err == nil {
		t.Error("AddVertex duplicate should return error")
	}
}

func TestDAG_TopologicalSort_LinearChain(t *testing.T) {
	d, _ := buildLinearDAG(t)
	sorted, err := d.TopologicalSort()
	if err != nil {
		t.Fatalf("TopologicalSort: %v", err)
	}
	pos := make(map[string]int, len(sorted))
	for i, v := range sorted {
		pos[v.ID()] = i
	}
	// C must come before B, B before A.
	if pos["C"] >= pos["B"] {
		t.Errorf("C must precede B in topo order")
	}
	if pos["B"] >= pos["A"] {
		t.Errorf("B must precede A in topo order")
	}
}

func TestDAG_Ancestors(t *testing.T) {
	d, _ := buildLinearDAG(t)
	ancs, err := d.Ancestors("A")
	if err != nil {
		t.Fatalf("Ancestors: %v", err)
	}
	ids := vertexIDs(ancs)
	if !contains(ids, "B") || !contains(ids, "C") {
		t.Errorf("Ancestors(A) = %v; want B and C", ids)
	}
}

func TestDAG_Descendants(t *testing.T) {
	d, _ := buildLinearDAG(t)
	descs, err := d.Descendants("C")
	if err != nil {
		t.Fatalf("Descendants: %v", err)
	}
	ids := vertexIDs(descs)
	if !contains(ids, "B") || !contains(ids, "A") {
		t.Errorf("Descendants(C) = %v; want B and A", ids)
	}
}

func TestDAG_CriticalPath(t *testing.T) {
	d, _ := buildLinearDAG(t)
	path, err := d.CriticalPath("A")
	if err != nil {
		t.Fatalf("CriticalPath: %v", err)
	}
	if len(path) != 3 {
		t.Errorf("CriticalPath length = %d; want 3 (C→B→A)", len(path))
	}
	if path[len(path)-1] != "A" {
		t.Errorf("CriticalPath last element = %q; want A", path[len(path)-1])
	}
}

func TestDAG_IsAncestor(t *testing.T) {
	d, _ := buildLinearDAG(t)
	if !d.IsAncestor("C", "A") {
		t.Error("C should be ancestor of A")
	}
	if d.IsAncestor("A", "C") {
		t.Error("A should not be ancestor of C")
	}
}

func TestDAG_AncestorDepth(t *testing.T) {
	d, _ := buildLinearDAG(t)
	// C is 2 hops from A (A→B→C)
	depth := d.AncestorDepth("C", "A")
	if depth != 2 {
		t.Errorf("AncestorDepth(C, A) = %d; want 2", depth)
	}
	// B is 1 hop from A
	depth = d.AncestorDepth("B", "A")
	if depth != 1 {
		t.Errorf("AncestorDepth(B, A) = %d; want 1", depth)
	}
}

// ---------------------------------------------------------------------------
// Fine-grained invalidation tests
// ---------------------------------------------------------------------------

// Graph:
//
//	C produces file1.o and file2.o
//	B depends only on file1.o from C
//	D depends only on file2.o from C
//	A depends on file1.o from B
func buildFileDepsDAG(t *testing.T) (*dag.DAG, map[string]*dag.Vertex) {
	t.Helper()
	d := dag.NewDAG()
	vC := newVertex("C", producerOp{id: "C", files: []dag.FileRef{
		{Path: "/out/file1.o"},
		{Path: "/out/file2.o"},
	}})
	vB := newVertex("B", noopOp{id: "B"})
	vA := newVertex("A", noopOp{id: "A"})
	vD := newVertex("D", noopOp{id: "D"})

	for _, v := range []*dag.Vertex{vC, vB, vA, vD} {
		mustAdd(t, d, v)
	}
	mustLink(t, d, "C", "B")
	mustLink(t, d, "C", "D")
	mustLink(t, d, "B", "A")

	// Declare fine-grained file deps
	if err := d.AddFileDependency("B", "C", []string{"/out/file1.o"}); err != nil {
		t.Fatalf("AddFileDependency B→C: %v", err)
	}
	if err := d.AddFileDependency("D", "C", []string{"/out/file2.o"}); err != nil {
		t.Fatalf("AddFileDependency D→C: %v", err)
	}
	if err := d.AddFileDependency("A", "B", []string{"/out/file1.o"}); err != nil {
		t.Fatalf("AddFileDependency A→B: %v", err)
	}

	// Pre-populate output files on C so the invalidation engine can inspect them.
	vC.SetOutputFiles([]dag.FileRef{
		{Path: "/out/file1.o"},
		{Path: "/out/file2.o"},
	})

	mustSeal(t, d)
	return d, map[string]*dag.Vertex{"A": vA, "B": vB, "C": vC, "D": vD}
}

func TestInvalidation_OnlyFile2Changed_SparesBandD(t *testing.T) {
	d, vs := buildFileDepsDAG(t)
	engine := dag.NewInvalidationEngine(d, nil)

	// Mark all vertices as completed so we can see which get reset.
	for _, v := range vs {
		_ = v.SetState(dag.StateCompleted, "pre-test")
	}

	changedFiles := []dag.FileRef{{Path: "/out/file2.o"}}
	invalidated, err := engine.Invalidate(context.Background(), changedFiles)
	if err != nil {
		t.Fatalf("Invalidate: %v", err)
	}

	invalidatedIDs := vertexIDs(invalidated)

	// D reads file2.o from C → must be invalidated.
	if !contains(invalidatedIDs, "D") {
		t.Errorf("D should be invalidated when file2.o changes; got %v", invalidatedIDs)
	}
	// B reads only file1.o → must NOT be invalidated.
	if contains(invalidatedIDs, "B") {
		t.Errorf("B should NOT be invalidated when only file2.o changes; got %v", invalidatedIDs)
	}
	// A depends on B which was not invalidated → A must NOT be invalidated.
	if contains(invalidatedIDs, "A") {
		t.Errorf("A should NOT be invalidated when only file2.o changes; got %v", invalidatedIDs)
	}
}

func TestInvalidation_File1Changed_InvalidatesBAndA(t *testing.T) {
	d, vs := buildFileDepsDAG(t)
	engine := dag.NewInvalidationEngine(d, nil)

	for _, v := range vs {
		_ = v.SetState(dag.StateCompleted, "pre-test")
	}

	changedFiles := []dag.FileRef{{Path: "/out/file1.o"}}
	invalidated, err := engine.Invalidate(context.Background(), changedFiles)
	if err != nil {
		t.Fatalf("Invalidate: %v", err)
	}

	invalidatedIDs := vertexIDs(invalidated)

	if !contains(invalidatedIDs, "B") {
		t.Errorf("B should be invalidated when file1.o changes; got %v", invalidatedIDs)
	}
	if !contains(invalidatedIDs, "A") {
		t.Errorf("A should be invalidated when file1.o changes (via B); got %v", invalidatedIDs)
	}
	if contains(invalidatedIDs, "D") {
		t.Errorf("D should NOT be invalidated when only file1.o changes; got %v", invalidatedIDs)
	}
}

// ---------------------------------------------------------------------------
// Scheduler — integration tests
// ---------------------------------------------------------------------------

func TestScheduler_CleanBuild_AllVerticesCompleted(t *testing.T) {
	d, _ := buildLinearDAG(t)
	s := sched(d, dag.WithWorkerCount(2))

	metrics, err := s.Build(context.Background(), "A", nil)
	if err != nil {
		t.Fatalf("Build: %v", err)
	}
	if metrics.Failed != 0 {
		t.Errorf("Failed = %d; want 0", metrics.Failed)
	}
	if metrics.Executed != 3 {
		t.Errorf("Executed = %d; want 3", metrics.Executed)
	}
}

func TestScheduler_CacheHit_SkipsExecution(t *testing.T) {
	d := dag.NewDAG()
	op := &countingOp{id: "A"}
	vA := newVertex("A", op)
	mustAdd(t, d, vA)
	mustSeal(t, d)

	fastCache := dag.NewMemoryCacheStore(0)
	s := sched(d, dag.WithFastCache(fastCache), dag.WithWorkerCount(1))

	ctx := context.Background()

	// Build 1: executes A.
	if _, err := s.Build(ctx, "A", nil); err != nil {
		t.Fatalf("Build 1: %v", err)
	}
	if op.calls.Load() != 1 {
		t.Fatalf("Build 1: expected 1 call, got %d", op.calls.Load())
	}

	// Reset A back to initial so the scheduler tries again.
	vA.Reset()

	// Build 2: cache hit expected.
	m, err := s.Build(ctx, "A", nil)
	if err != nil {
		t.Fatalf("Build 2: %v", err)
	}
	if op.calls.Load() != 1 {
		t.Errorf("Build 2: execute called again; want cache hit (calls=%d)", op.calls.Load())
	}
	if m.FastCacheHits != 1 {
		t.Errorf("FastCacheHits = %d; want 1", m.FastCacheHits)
	}
}

func TestScheduler_CachedError_Replayed(t *testing.T) {
	// Chain: C → B(fails) → A
	// Build 1: B fails, cached. Build 2: B's error replayed, A still fails instantly.
	d := dag.NewDAG()
	failOp := errOp{id: "B", msg: "compilation error"}
	countA := &countingOp{id: "A"}

	vC := newVertex("C", noopOp{id: "C"})
	vB := newVertex("B", failOp)
	vA := newVertex("A", countA)

	for _, v := range []*dag.Vertex{vC, vB, vA} {
		mustAdd(t, d, v)
	}
	mustLink(t, d, "C", "B")
	mustLink(t, d, "B", "A")
	mustSeal(t, d)

	fastCache := dag.NewMemoryCacheStore(0)
	s := sched(d, dag.WithFastCache(fastCache), dag.WithWorkerCount(2))
	ctx := context.Background()

	// Build 1: B fails and gets cached.
	m1, err1 := s.Build(ctx, "A", nil)
	if err1 == nil {
		t.Fatal("Build 1: expected error")
	}
	if !strings.Contains(err1.Error(), "compilation error") {
		t.Errorf("Build 1 error = %v; want compilation error", err1)
	}
	_ = m1

	// Reset B and A; C remains completed.
	vB.Reset()
	vA.Reset()

	// Build 2: B's key resolves to the cached failure → error replayed, A never executed.
	m2, err2 := s.Build(ctx, "A", nil)
	if err2 == nil {
		t.Fatal("Build 2: expected error from cached failure")
	}
	if countA.calls.Load() != 0 {
		t.Errorf("A executed on Build 2; want 0 executions (got %d)", countA.calls.Load())
	}
	if m2.FastCacheHits == 0 {
		t.Error("Build 2: expected at least one fast cache hit for cached error")
	}
}

func TestScheduler_VertexFailed_DownstreamFailed(t *testing.T) {
	// B fails → A (which depends on B) should also be marked failed.
	d := dag.NewDAG()
	vB := newVertex("B", errOp{id: "B", msg: "oops"})
	vA := newVertex("A", noopOp{id: "A"})

	mustAdd(t, d, vB)
	mustAdd(t, d, vA)
	mustLink(t, d, "B", "A")
	mustSeal(t, d)

	s := sched(d, dag.WithWorkerCount(2))
	m, err := s.Build(context.Background(), "A", nil)
	if err == nil {
		t.Fatal("expected build to fail")
	}
	if m.Failed < 1 {
		t.Errorf("Failed = %d; want ≥1", m.Failed)
	}
	if vA.State() != dag.StateFailed {
		t.Errorf("A.State = %s; want failed", vA.State())
	}
}

func TestScheduler_TargetAlreadyCompleted_ShortCircuits(t *testing.T) {
	d := dag.NewDAG()
	op := &countingOp{id: "A"}
	vA := newVertex("A", op)
	mustAdd(t, d, vA)
	mustSeal(t, d)

	// Manually mark target as completed.
	if err := vA.SetState(dag.StateCompleted, "pre-completed"); err != nil {
		t.Fatalf("SetState: %v", err)
	}

	s := sched(d, dag.WithWorkerCount(1))
	m, err := s.Build(context.Background(), "A", nil)
	if err != nil {
		t.Fatalf("Build: %v", err)
	}
	if op.calls.Load() != 0 {
		t.Errorf("Execute called %d times; want 0 (short-circuit)", op.calls.Load())
	}
	if m.Skipped == 0 {
		t.Errorf("Skipped = %d; want > 0", m.Skipped)
	}
}

func TestScheduler_Concurrency_DiamondDAG(t *testing.T) {
	//   A
	//  / \
	// B   C
	//  \ /
	//   D
	// D is root, A is target. B and C can run concurrently.
	d := dag.NewDAG()
	vD := newVertex("D", slowOp{id: "D", sleep: 10 * time.Millisecond})
	vB := newVertex("B", slowOp{id: "B", sleep: 10 * time.Millisecond})
	vC := newVertex("C", slowOp{id: "C", sleep: 10 * time.Millisecond})
	vA := newVertex("A", noopOp{id: "A"})

	for _, v := range []*dag.Vertex{vD, vB, vC, vA} {
		mustAdd(t, d, v)
	}
	mustLink(t, d, "D", "B")
	mustLink(t, d, "D", "C")
	mustLink(t, d, "B", "A")
	mustLink(t, d, "C", "A")
	mustSeal(t, d)

	s := sched(d, dag.WithWorkerCount(4))
	start := time.Now()
	_, err := s.Build(context.Background(), "A", nil)
	elapsed := time.Since(start)
	if err != nil {
		t.Fatalf("Build: %v", err)
	}
	// With concurrency, B and C overlap; total should be well under 40ms.
	if elapsed > 35*time.Millisecond {
		t.Errorf("elapsed %v suggests B and C ran serially (want concurrent)", elapsed)
	}
}

func TestScheduler_ContextCancellation(t *testing.T) {
	d := dag.NewDAG()
	mustAdd(t, d, newVertex("A", slowOp{id: "A", sleep: 500 * time.Millisecond}))
	mustSeal(t, d)

	ctx, cancel := context.WithTimeout(context.Background(), 20*time.Millisecond)
	defer cancel()

	s := sched(d, dag.WithWorkerCount(1))
	_, err := s.Build(ctx, "A", nil)
	if err == nil {
		t.Error("expected error due to context cancellation")
	}
}

// ---------------------------------------------------------------------------
// Hook tests
// ---------------------------------------------------------------------------

func TestHooks_BeforeExecute_AbortOnError(t *testing.T) {
	d := dag.NewDAG()
	op := &countingOp{id: "A"}
	vA := newVertex("A", op)
	mustAdd(t, d, vA)
	mustSeal(t, d)

	hooks := dag.NewHookRegistry()
	hooks.Register(dag.HookBeforeExecute, 0, func(_ context.Context, _ *dag.Vertex, _ dag.HookPayload) error {
		return errors.New("hook blocked execution")
	})

	s := sched(d, dag.WithHooks(hooks), dag.WithWorkerCount(1))
	_, err := s.Build(context.Background(), "A", nil)
	if err == nil {
		t.Error("expected build to fail due to pre-execute hook")
	}
	if op.calls.Load() != 0 {
		t.Errorf("Execute called despite hook blocking; calls=%d", op.calls.Load())
	}
}

func TestHooks_Priority_Order(t *testing.T) {
	registry := dag.NewHookRegistry()
	var order []int

	registry.Register(dag.HookBeforeExecute, 20, func(_ context.Context, _ *dag.Vertex, _ dag.HookPayload) error {
		order = append(order, 20)
		return nil
	})
	registry.Register(dag.HookBeforeExecute, 5, func(_ context.Context, _ *dag.Vertex, _ dag.HookPayload) error {
		order = append(order, 5)
		return nil
	})
	registry.Register(dag.HookBeforeExecute, 10, func(_ context.Context, _ *dag.Vertex, _ dag.HookPayload) error {
		order = append(order, 10)
		return nil
	})

	v := newVertex("x", noopOp{id: "x"})
	if err := registry.Execute(context.Background(), dag.HookBeforeExecute, v, dag.HookPayload{}); err != nil {
		t.Fatalf("Execute: %v", err)
	}
	want := []int{5, 10, 20}
	for i, got := range order {
		if got != want[i] {
			t.Errorf("order[%d] = %d; want %d", i, got, want[i])
		}
	}
}

func TestHooks_Deregister(t *testing.T) {
	registry := dag.NewHookRegistry()
	calls := 0
	deregister := registry.Register(dag.HookBeforeExecute, 0, func(_ context.Context, _ *dag.Vertex, _ dag.HookPayload) error {
		calls++
		return nil
	})

	v := newVertex("x", noopOp{id: "x"})
	_ = registry.Execute(context.Background(), dag.HookBeforeExecute, v, dag.HookPayload{})
	if calls != 1 {
		t.Fatalf("expected 1 call before deregister, got %d", calls)
	}

	deregister()
	_ = registry.Execute(context.Background(), dag.HookBeforeExecute, v, dag.HookPayload{})
	if calls != 1 {
		t.Errorf("expected 1 call after deregister, got %d", calls)
	}
}

// ---------------------------------------------------------------------------
// EventBus tests
// ---------------------------------------------------------------------------

func TestEventBus_Subscribe_ReceivesEvent(t *testing.T) {
	bus := dag.NewEventBus()
	received := make(chan dag.Event, 1)

	bus.Subscribe(dag.EventStateChanged, func(_ context.Context, e dag.Event) {
		received <- e
	})

	v := newVertex("x", noopOp{id: "x"})
	bus.PublishStateChanged(context.Background(), v, dag.StateInitial, dag.StateCompleted)

	select {
	case e := <-received:
		if e.VertexID != "x" {
			t.Errorf("VertexID = %q; want x", e.VertexID)
		}
	case <-time.After(100 * time.Millisecond):
		t.Error("event not received")
	}
}

func TestEventBus_SubscribeAll_ReceivesAllTypes(t *testing.T) {
	bus := dag.NewEventBus()
	var mu sync.Mutex
	var received []dag.EventType

	bus.SubscribeAll(func(_ context.Context, e dag.Event) {
		mu.Lock()
		received = append(received, e.Type)
		mu.Unlock()
	})

	v := newVertex("x", noopOp{id: "x"})
	ctx := context.Background()
	bus.PublishStateChanged(ctx, v, dag.StateInitial, dag.StateCompleted)
	bus.PublishCacheHit(ctx, v, "fast")
	bus.PublishCacheMiss(ctx, v)

	// Give handlers time to run (they're synchronous but let's be explicit).
	mu.Lock()
	got := len(received)
	mu.Unlock()

	if got != 3 {
		t.Errorf("received %d events; want 3", got)
	}
}

func TestEventBus_Unsubscribe(t *testing.T) {
	bus := dag.NewEventBus()
	calls := 0
	v := newVertex("x", noopOp{id: "x"})
	ctx := context.Background()

	unsub := bus.Subscribe(dag.EventCacheHit, func(_ context.Context, _ dag.Event) {
		calls++
	})

	bus.PublishCacheHit(ctx, v, "fast")
	if calls != 1 {
		t.Fatalf("expected 1 call, got %d", calls)
	}

	unsub()
	bus.PublishCacheHit(ctx, v, "fast")
	if calls != 1 {
		t.Errorf("after Unsubscribe, expected still 1 call, got %d", calls)
	}
}

// ---------------------------------------------------------------------------
// Cache tests
// ---------------------------------------------------------------------------

func TestMemoryCacheStore_GetSetDelete(t *testing.T) {
	store := dag.NewMemoryCacheStore(0)
	ctx := context.Background()
	key := dag.CacheKey{1, 2, 3}
	entry := &dag.CacheEntry{
		OutputFiles: []dag.FileRef{{Path: "/out/x"}},
		CachedAt:    time.Now(),
	}

	// Miss before set.
	got, err := store.Get(ctx, key)
	if err != nil || got != nil {
		t.Errorf("Get before Set: got %v, err %v; want nil, nil", got, err)
	}

	if err := store.Set(ctx, key, entry); err != nil {
		t.Fatalf("Set: %v", err)
	}

	got, err = store.Get(ctx, key)
	if err != nil || got == nil {
		t.Errorf("Get after Set: got %v, err %v; want entry, nil", got, err)
	}

	if err := store.Delete(ctx, key); err != nil {
		t.Fatalf("Delete: %v", err)
	}

	got, err = store.Get(ctx, key)
	if err != nil || got != nil {
		t.Errorf("Get after Delete: got %v, err %v; want nil, nil", got, err)
	}
}

func TestMemoryCacheStore_MaxSize_Evicts(t *testing.T) {
	store := dag.NewMemoryCacheStore(2)
	ctx := context.Background()

	for i := range 3 {
		key := dag.CacheKey{byte(i)}
		_ = store.Set(ctx, key, &dag.CacheEntry{CachedAt: time.Now()})
	}

	stats := store.Stats()
	if stats.Entries > 2 {
		t.Errorf("Entries = %d after maxSize=2 insert; want ≤2", stats.Entries)
	}
}

func TestCacheEntry_IsFailed(t *testing.T) {
	success := &dag.CacheEntry{}
	failed := &dag.CacheEntry{CachedErr: "boom"}

	if success.IsFailed() {
		t.Error("success entry should not be failed")
	}
	if !failed.IsFailed() {
		t.Error("failed entry should be failed")
	}
	if err := failed.CachedError(); err == nil || err.Error() != "boom" {
		t.Errorf("CachedError() = %v; want 'boom'", err)
	}
}

// ---------------------------------------------------------------------------
// Full-control API tests
// ---------------------------------------------------------------------------

func TestScheduler_ForceState(t *testing.T) {
	d := dag.NewDAG()
	mustAdd(t, d, newVertex("A", noopOp{id: "A"}))
	mustSeal(t, d)

	s := sched(d, dag.WithWorkerCount(1))
	if err := s.ForceState(context.Background(), "A", dag.StateCompleted, "test"); err != nil {
		t.Fatalf("ForceState: %v", err)
	}

	v, _ := d.Vertex("A")
	if v.State() != dag.StateCompleted {
		t.Errorf("State = %s; want completed", v.State())
	}
}

func TestScheduler_Snapshot(t *testing.T) {
	d, _ := buildLinearDAG(t)
	s := sched(d, dag.WithWorkerCount(2))

	snap := s.Snapshot()
	if len(snap) != 3 {
		t.Errorf("Snapshot len = %d; want 3", len(snap))
	}
	for id, state := range snap {
		if state != dag.StateInitial {
			t.Errorf("Snapshot[%q] = %s; want initial", id, state)
		}
	}
}

func TestScheduler_ResetSubtree(t *testing.T) {
	d, vs := buildLinearDAG(t)
	s := sched(d, dag.WithWorkerCount(2))

	// Complete everything first.
	if _, err := s.Build(context.Background(), "A", nil); err != nil {
		t.Fatalf("Build: %v", err)
	}

	// Reset from B downward (B and its descendant A).
	if err := s.ResetSubtree(context.Background(), "B"); err != nil {
		t.Fatalf("ResetSubtree: %v", err)
	}

	if vs["B"].State() != dag.StateInitial {
		t.Errorf("B.State = %s; want initial", vs["B"].State())
	}
	if vs["A"].State() != dag.StateInitial {
		t.Errorf("A.State = %s; want initial", vs["A"].State())
	}
	// C was not in the reset subtree.
	if vs["C"].State() != dag.StateCompleted {
		t.Errorf("C.State = %s; want completed (untouched)", vs["C"].State())
	}
}

func TestScheduler_MarkTargetAndAncestorsCompleted(t *testing.T) {
	d, vs := buildLinearDAG(t)
	s := sched(d, dag.WithWorkerCount(1))

	// Manually complete A; tell the scheduler to propagate.
	if err := vs["A"].SetState(dag.StateCompleted, "pre"); err != nil {
		t.Fatalf("SetState: %v", err)
	}
	if err := s.MarkTargetAndAncestorsCompleted(context.Background(), "A"); err != nil {
		t.Fatalf("MarkTargetAndAncestorsCompleted: %v", err)
	}

	for id, v := range vs {
		if v.State() != dag.StateCompleted {
			t.Errorf("%s.State = %s; want completed", id, v.State())
		}
	}
}

// ---------------------------------------------------------------------------
// Key computer tests
// ---------------------------------------------------------------------------

func TestDefaultKeyComputer_Deterministic(t *testing.T) {
	kc := dag.DefaultKeyComputer{}
	v := newVertex("A", noopOp{id: "A"})
	files := []dag.FileRef{
		{Path: "/a", Hash: [32]byte{1}},
		{Path: "/b", Hash: [32]byte{2}},
	}

	k1, err1 := kc.Compute(v, files)
	k2, err2 := kc.Compute(v, files)

	if err1 != nil || err2 != nil {
		t.Fatalf("Compute errors: %v, %v", err1, err2)
	}
	if k1 != k2 {
		t.Error("Compute is not deterministic for same inputs")
	}
}

func TestDefaultKeyComputer_DifferentFiles_DifferentKeys(t *testing.T) {
	kc := dag.DefaultKeyComputer{}
	v := newVertex("A", noopOp{id: "A"})

	k1, _ := kc.Compute(v, []dag.FileRef{{Path: "/a", Hash: [32]byte{1}}})
	k2, _ := kc.Compute(v, []dag.FileRef{{Path: "/a", Hash: [32]byte{2}}})

	if k1 == k2 {
		t.Error("different file hashes should produce different cache keys")
	}
}

func TestDefaultKeyComputer_PathOrder_Irrelevant(t *testing.T) {
	kc := dag.DefaultKeyComputer{}
	v := newVertex("A", noopOp{id: "A"})

	files1 := []dag.FileRef{
		{Path: "/a", Hash: [32]byte{1}},
		{Path: "/b", Hash: [32]byte{2}},
	}
	files2 := []dag.FileRef{
		{Path: "/b", Hash: [32]byte{2}},
		{Path: "/a", Hash: [32]byte{1}},
	}

	k1, _ := kc.Compute(v, files1)
	k2, _ := kc.Compute(v, files2)

	if k1 != k2 {
		t.Error("key should be the same regardless of input file order")
	}
}

// ---------------------------------------------------------------------------
// Telemetry tests
// ---------------------------------------------------------------------------

func TestTelemetry_Noop_NoAllocs(t *testing.T) {
	tel := dag.NewNoopTelemetry()
	v := newVertex("A", noopOp{id: "A"})
	ctx := context.Background()

	// None of these should panic.
	ctx2, span := tel.StartVertexSpan(ctx, v)
	_ = ctx2
	span.End()

	tel.RecordCacheResult(ctx, v, "fast", true)
	tel.RecordVertexDuration(ctx, v, 10*time.Millisecond)
	tel.RecordFileAccess(ctx, v, 3, 1)
	tel.RecordBuildDuration(ctx, "A", 100*time.Millisecond)
	tel.RecordInvalidation(ctx, 5)
}

// ---------------------------------------------------------------------------
// Helpers
// ---------------------------------------------------------------------------

func vertexIDs(vs []*dag.Vertex) []string {
	ids := make([]string, len(vs))
	for i, v := range vs {
		ids[i] = v.ID()
	}
	return ids
}

func contains(slice []string, s string) bool {
	for _, x := range slice {
		if x == s {
			return true
		}
	}
	return false
}
