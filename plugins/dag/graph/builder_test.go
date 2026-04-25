package graph_test

import (
	"context"
	"fmt"
	"sync"
	"testing"

	"github.com/bons/bons-ci/plugins/dag/graph"
	"github.com/bons/bons-ci/plugins/dag/ops"
	"github.com/bons/bons-ci/plugins/dag/vertex"
)

// ─── Test helpers ─────────────────────────────────────────────────────────────

// stubVertex is a minimal Vertex implementation for testing.
type stubVertex struct {
	id     string
	kind   vertex.Kind
	inputs []vertex.Vertex
	name   string
	valid  bool
}

func stub(id string, inputs ...vertex.Vertex) *stubVertex {
	return &stubVertex{id: id, kind: vertex.KindSource, inputs: inputs, valid: true}
}

func (s *stubVertex) ID() string              { return s.id }
func (s *stubVertex) Kind() vertex.Kind       { return s.kind }
func (s *stubVertex) Inputs() []vertex.Vertex { return s.inputs }
func (s *stubVertex) Name() string            { return s.name }
func (s *stubVertex) Validate(ctx context.Context) error {
	if !s.valid {
		return fmt.Errorf("stub %q: invalid", s.id)
	}
	return nil
}

// ─── Cycle detection ──────────────────────────────────────────────────────────

func TestNoCycleLinear(t *testing.T) {
	ctx := context.Background()
	// A → B → C  (A depends on B which depends on C)
	c := stub("C")
	b := stub("B", c)
	a := stub("A", b)

	dag, err := graph.BuildFromRoot(ctx, a)
	if err != nil {
		t.Fatalf("expected no error, got: %v", err)
	}
	if dag.Len() != 3 {
		t.Errorf("expected 3 vertices, got %d", dag.Len())
	}
}

func TestNoCycleDiamond(t *testing.T) {
	ctx := context.Background()
	// Diamond: A depends on B and C; both B and C depend on D.
	d := stub("D")
	b := stub("B", d)
	c := stub("C", d)
	a := stub("A", b, c)

	dag, err := graph.BuildFromRoot(ctx, a)
	if err != nil {
		t.Fatalf("diamond DAG should be valid: %v", err)
	}
	// D should appear exactly once despite being referenced twice.
	if dag.Len() != 4 {
		t.Errorf("expected 4 vertices, got %d", dag.Len())
	}
}

func TestCycleDirectSelf(t *testing.T) {
	// Self-loop: A depends on itself.
	a := &stubVertex{id: "A", valid: true}
	a.inputs = []vertex.Vertex{a}

	b := &graph.Builder{}
	_ = b
	// Use manual builder because AddTree would loop infinitely.
	// Instead use graph.NewBuilder and manually add, then Build.
	builder := graph.NewBuilder()
	_ = builder.Add(a) // register without walking inputs
	_, err := builder.Build()
	if err == nil {
		t.Fatal("expected cycle error for self-loop, got nil")
	}
	t.Logf("correct error: %v", err)
}

func TestCycleIndirect(t *testing.T) {
	// A → B → C → A  (cycle of length 3)
	a := &stubVertex{id: "A", valid: true}
	b := &stubVertex{id: "B", valid: true, inputs: []vertex.Vertex{a}}
	c := &stubVertex{id: "C", valid: true, inputs: []vertex.Vertex{b}}
	// Close the cycle: A depends on C.
	a.inputs = []vertex.Vertex{c}

	builder := graph.NewBuilder()
	_ = builder.Add(a)
	_ = builder.Add(b)
	_ = builder.Add(c)
	_, err := builder.Build()
	if err == nil {
		t.Fatal("expected cycle error for A→B→C→A, got nil")
	}
	t.Logf("correct error: %v", err)
}

// ─── Duplicate ID detection ────────────────────────────────────────────────────

func TestDuplicateIDDifferentObject(t *testing.T) {
	v1 := stub("same-id")
	v2 := stub("same-id") // different object, same ID

	builder := graph.NewBuilder()
	if err := builder.Add(v1); err != nil {
		t.Fatalf("first Add should succeed: %v", err)
	}
	if err := builder.Add(v2); err == nil {
		t.Fatal("expected error for duplicate ID with different object")
	}
}

func TestDuplicateIDSameObject(t *testing.T) {
	v := stub("same-id")

	builder := graph.NewBuilder()
	if err := builder.Add(v); err != nil {
		t.Fatalf("first Add: %v", err)
	}
	// Adding the same object again must be a no-op.
	if err := builder.Add(v); err != nil {
		t.Fatalf("second Add of same object should be no-op: %v", err)
	}
}

// ─── Topological ordering ─────────────────────────────────────────────────────

func TestTopologicalOrderLinear(t *testing.T) {
	ctx := context.Background()
	c := stub("C")
	b := stub("B", c)
	a := stub("A", b)

	dag, err := graph.BuildFromRoot(ctx, a)
	if err != nil {
		t.Fatal(err)
	}
	order := dag.TopologicalOrder()
	if len(order) != 3 {
		t.Fatalf("expected 3, got %d", len(order))
	}
	// C must come before B, B before A.
	pos := func(id string) int {
		for i, v := range order {
			if v.ID() == id {
				return i
			}
		}
		return -1
	}
	if pos("C") >= pos("B") {
		t.Error("C must appear before B in topological order")
	}
	if pos("B") >= pos("A") {
		t.Error("B must appear before A in topological order")
	}
}

func TestTopologicalOrderDiamond(t *testing.T) {
	ctx := context.Background()
	d := stub("D")
	b := stub("B", d)
	c := stub("C", d)
	a := stub("A", b, c)

	dag, _ := graph.BuildFromRoot(ctx, a)
	order := dag.TopologicalOrder()

	pos := func(id string) int {
		for i, v := range order {
			if v.ID() == id {
				return i
			}
		}
		return -1
	}

	// D must be before B and C; B and C must be before A.
	if pos("D") >= pos("B") {
		t.Error("D must appear before B")
	}
	if pos("D") >= pos("C") {
		t.Error("D must appear before C")
	}
	if pos("B") >= pos("A") {
		t.Error("B must appear before A")
	}
	if pos("C") >= pos("A") {
		t.Error("C must appear before A")
	}
}

// ─── Determinism ──────────────────────────────────────────────────────────────

func TestTopologicalOrderDeterministic(t *testing.T) {
	ctx := context.Background()
	// Run many times and verify the order is always the same.
	build := func() []string {
		d := stub("D")
		b := stub("B", d)
		c := stub("C", d)
		a := stub("A", b, c)
		dag, err := graph.BuildFromRoot(ctx, a)
		if err != nil {
			t.Fatalf("build: %v", err)
		}
		order := dag.TopologicalOrder()
		ids := make([]string, len(order))
		for i, v := range order {
			ids[i] = v.ID()
		}
		return ids
	}

	first := build()
	for i := 0; i < 20; i++ {
		got := build()
		for j, id := range got {
			if id != first[j] {
				t.Errorf("run %d: position %d: want %q, got %q", i, j, first[j], id)
			}
		}
	}
}

// ─── Roots ────────────────────────────────────────────────────────────────────

func TestRoots(t *testing.T) {
	ctx := context.Background()
	// Two independent roots: A→C and B→C.
	c := stub("C")
	a := stub("A", c)
	b := stub("B", c)

	dag, err := graph.BuildFromRoots(ctx, a, b)
	if err != nil {
		t.Fatal(err)
	}
	roots := dag.Roots()
	if len(roots) != 2 {
		t.Errorf("expected 2 roots, got %d", len(roots))
	}
	rootIDs := map[string]bool{}
	for _, r := range roots {
		rootIDs[r.ID()] = true
	}
	if !rootIDs["A"] || !rootIDs["B"] {
		t.Errorf("expected roots A and B, got %v", rootIDs)
	}
}

// ─── Subgraph ─────────────────────────────────────────────────────────────────

func TestSubgraph(t *testing.T) {
	ctx := context.Background()
	// Full graph: A→B→D, A→C→D, where E is unrelated.
	d := stub("D")
	b := stub("B", d)
	c := stub("C", d)
	a := stub("A", b, c)
	e := stub("E")

	dag, err := graph.BuildFromRoots(ctx, a, e)
	if err != nil {
		t.Fatal(err)
	}
	if dag.Len() != 5 {
		t.Fatalf("expected 5 vertices, got %d", dag.Len())
	}

	// Subgraph from A should contain A, B, C, D but not E.
	sub, err := dag.Subgraph(a)
	if err != nil {
		t.Fatal(err)
	}
	if sub.Len() != 4 {
		t.Errorf("subgraph from A: expected 4 vertices, got %d", sub.Len())
	}
	if sub.Has("E") {
		t.Error("subgraph from A should not contain E")
	}
}

// ─── Validation propagation ────────────────────────────────────────────────────

func TestValidationFailure(t *testing.T) {
	ctx := context.Background()
	bad := &stubVertex{id: "bad", valid: false}
	_, err := graph.BuildFromRoot(ctx, bad)
	if err == nil {
		t.Fatal("expected validation error")
	}
}

// ─── Real ops integration ─────────────────────────────────────────────────────

func TestRealOpsDAG(t *testing.T) {
	ctx := context.Background()

	// Image → FileOp → ExecOp
	img := ops.Image("alpine:3.18")
	fileRef := ops.NewFileOp(
		img.Ref(),
		ops.Mkdir("/app", 0755, ops.WithParents(true)),
		ops.Constraints{},
	).Ref()

	execOp := ops.Exec(fileRef,
		ops.WithArgs("sh", "-c", "echo hello > /app/hello.txt"),
		ops.WithCwd("/app"),
	)

	dag, err := graph.BuildFromRoot(ctx, execOp)
	if err != nil {
		t.Fatalf("real ops DAG: %v", err)
	}
	if dag.Len() != 3 {
		t.Errorf("expected 3 vertices (image, file, exec), got %d", dag.Len())
	}
	roots := dag.Roots()
	if len(roots) != 1 || roots[0].ID() != execOp.ID() {
		t.Error("exec should be the only root")
	}
}

// ─── Concurrency safety ────────────────────────────────────────────────────────

func TestConcurrentBuildFromRoot(t *testing.T) {
	// Building from the same root concurrently must not race.
	ctx := context.Background()
	d := stub("D")
	c := stub("C", d)
	b := stub("B", d)
	a := stub("A", b, c)

	var wg sync.WaitGroup
	for i := 0; i < 20; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			dag, err := graph.BuildFromRoot(ctx, a)
			if err != nil {
				t.Errorf("concurrent build: %v", err)
				return
			}
			if dag.Len() != 4 {
				t.Errorf("expected 4 vertices, got %d", dag.Len())
			}
		}()
	}
	wg.Wait()
}

// ─── Large graph (stress test) ────────────────────────────────────────────────

func TestLargeLinearGraph(t *testing.T) {
	ctx := context.Background()
	const N = 1000

	// Build a linear chain of N vertices.
	prev := stub("v0")
	all := []vertex.Vertex{prev}
	for i := 1; i < N; i++ {
		curr := stub(fmt.Sprintf("v%d", i), prev)
		all = append(all, curr)
		prev = curr
	}

	dag, err := graph.BuildFromRoot(ctx, prev)
	if err != nil {
		t.Fatalf("large linear graph: %v", err)
	}
	if dag.Len() != N {
		t.Errorf("expected %d vertices, got %d", N, dag.Len())
	}
	order := dag.TopologicalOrder()
	// v0 must be first (it has no inputs), v999 must be last (root).
	if order[0].ID() != "v0" {
		t.Errorf("first in order: want v0, got %s", order[0].ID())
	}
	if order[N-1].ID() != fmt.Sprintf("v%d", N-1) {
		t.Errorf("last in order: want v%d, got %s", N-1, order[N-1].ID())
	}
}

func TestLargeFanOutGraph(t *testing.T) {
	ctx := context.Background()
	const N = 500
	// A single root vertex that depends on N leaves.
	leaves := make([]vertex.Vertex, N)
	for i := 0; i < N; i++ {
		leaves[i] = stub(fmt.Sprintf("leaf%d", i))
	}
	root := stub("root", leaves...)

	dag, err := graph.BuildFromRoot(ctx, root)
	if err != nil {
		t.Fatalf("fan-out graph: %v", err)
	}
	if dag.Len() != N+1 {
		t.Errorf("expected %d vertices, got %d", N+1, dag.Len())
	}
}
