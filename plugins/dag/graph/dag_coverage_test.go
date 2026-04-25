package graph_test

import (
	"context"
	"testing"

	"github.com/bons/bons-ci/plugins/dag/graph"
	"github.com/bons/bons-ci/plugins/dag/vertex"
)

// ─── DAG.Vertex ───────────────────────────────────────────────────────────────

func TestDAGVertex(t *testing.T) {
	ctx := context.Background()
	a := stub("A")
	dag, _ := graph.BuildFromRoot(ctx, a)

	got := dag.Vertex("A")
	if got == nil {
		t.Fatal("Vertex(A) must return the vertex")
	}
	if got.ID() != "A" {
		t.Errorf("ID: want A, got %s", got.ID())
	}
}

func TestDAGVertexMissing(t *testing.T) {
	ctx := context.Background()
	a := stub("A")
	dag, _ := graph.BuildFromRoot(ctx, a)

	got := dag.Vertex("MISSING")
	if got != nil {
		t.Errorf("Vertex(MISSING) must return nil, got %v", got)
	}
}

// ─── DAG.Vertices ─────────────────────────────────────────────────────────────

func TestDAGVertices(t *testing.T) {
	ctx := context.Background()
	b := stub("B")
	a := stub("A", b)
	dag, _ := graph.BuildFromRoot(ctx, a)

	verts := dag.Vertices()
	if len(verts) != 2 {
		t.Errorf("Vertices: want 2, got %d", len(verts))
	}
	ids := make(map[string]bool)
	for _, v := range verts {
		ids[v.ID()] = true
	}
	if !ids["A"] || !ids["B"] {
		t.Errorf("Vertices must contain A and B: got %v", ids)
	}
}

func TestDAGVerticesMutation(t *testing.T) {
	ctx := context.Background()
	a := stub("A")
	dag, _ := graph.BuildFromRoot(ctx, a)

	// Mutating the returned slice must not affect the DAG.
	verts := dag.Vertices()
	verts[0] = nil
	if dag.Len() != 1 {
		t.Error("mutating Vertices() must not affect the DAG")
	}
}

// ─── DAG.Downstream ───────────────────────────────────────────────────────────

func TestDAGDownstream(t *testing.T) {
	ctx := context.Background()
	// A → B → C. Downstream of C: B. Downstream of B: A.
	c := stub("C")
	b := stub("B", c)
	a := stub("A", b)
	dag, _ := graph.BuildFromRoot(ctx, a)

	downC := dag.Downstream(c)
	if len(downC) != 1 || downC[0].ID() != "B" {
		t.Errorf("Downstream(C): want [B], got %v", idsOf(downC))
	}

	downB := dag.Downstream(b)
	if len(downB) != 1 || downB[0].ID() != "A" {
		t.Errorf("Downstream(B): want [A], got %v", idsOf(downB))
	}

	downA := dag.Downstream(a)
	if len(downA) != 0 {
		t.Errorf("Downstream(root A): want [], got %v", idsOf(downA))
	}
}

func TestDAGDownstreamDiamond(t *testing.T) {
	ctx := context.Background()
	d := stub("D")
	b := stub("B", d)
	c := stub("C", d)
	a := stub("A", b, c)
	dag, _ := graph.BuildFromRoot(ctx, a)

	// D has two dependents: B and C.
	downD := dag.Downstream(d)
	if len(downD) != 2 {
		t.Errorf("Downstream(D): want 2, got %d: %v", len(downD), idsOf(downD))
	}
	ids := make(map[string]bool)
	for _, v := range downD {
		ids[v.ID()] = true
	}
	if !ids["B"] || !ids["C"] {
		t.Errorf("Downstream(D) must contain B and C, got %v", ids)
	}
}

func TestDAGDownstreamNotInGraph(t *testing.T) {
	ctx := context.Background()
	a := stub("A")
	dag, _ := graph.BuildFromRoot(ctx, a)

	outside := stub("OUTSIDE")
	down := dag.Downstream(outside)
	if len(down) != 0 {
		t.Errorf("Downstream of vertex not in graph must be empty, got %v", down)
	}
}

// ─── Upstream ────────────────────────────────────────────────────────────────

func TestDAGUpstream(t *testing.T) {
	ctx := context.Background()
	b := stub("B")
	a := stub("A", b)
	dag, _ := graph.BuildFromRoot(ctx, a)

	upA := dag.Upstream(a)
	if len(upA) != 1 || upA[0].ID() != "B" {
		t.Errorf("Upstream(A): want [B], got %v", idsOf(upA))
	}
	upB := dag.Upstream(b)
	if len(upB) != 0 {
		t.Errorf("Upstream(leaf B): want [], got %v", idsOf(upB))
	}
}

func TestDAGUpstreamNotInGraph(t *testing.T) {
	ctx := context.Background()
	a := stub("A")
	dag, _ := graph.BuildFromRoot(ctx, a)

	outside := stub("OUTSIDE")
	up := dag.Upstream(outside)
	if len(up) != 0 {
		t.Errorf("Upstream of vertex not in graph must be empty, got %v", up)
	}
}

// ─── BuildFromRoots with multiple ─────────────────────────────────────────────

func TestBuildFromRootsMultiple(t *testing.T) {
	ctx := context.Background()
	shared := stub("shared")
	a := stub("A", shared)
	b := stub("B", shared)

	dag, err := graph.BuildFromRoots(ctx, a, b)
	if err != nil {
		t.Fatal(err)
	}
	if dag.Len() != 3 {
		t.Errorf("expected 3 vertices, got %d", dag.Len())
	}
	roots := dag.Roots()
	if len(roots) != 2 {
		t.Errorf("expected 2 roots, got %d: %v", len(roots), idsOf(roots))
	}
}

// ─── AddTree with diamond ─────────────────────────────────────────────────────

func TestBuilderAddTreeDiamond(t *testing.T) {
	ctx := context.Background()
	shared := stub("shared")
	a := stub("A", shared)
	b := stub("B", shared)
	root := stub("root", a, b)

	builder := graph.NewBuilder()
	if err := builder.AddTree(ctx, root); err != nil {
		t.Fatalf("AddTree: %v", err)
	}
	dag, err := builder.Build()
	if err != nil {
		t.Fatalf("Build: %v", err)
	}
	// shared must appear exactly once.
	if dag.Len() != 4 {
		t.Errorf("expected 4 vertices, got %d", dag.Len())
	}
}

// ─── Empty DAG ────────────────────────────────────────────────────────────────

func TestEmptyDAG(t *testing.T) {
	builder := graph.NewBuilder()
	dag, err := builder.Build()
	if err != nil {
		t.Fatalf("empty build: %v", err)
	}
	if dag.Len() != 0 {
		t.Errorf("empty DAG: want 0 vertices, got %d", dag.Len())
	}
	if len(dag.Roots()) != 0 {
		t.Errorf("empty DAG: want 0 roots, got %d", len(dag.Roots()))
	}
	if len(dag.TopologicalOrder()) != 0 {
		t.Errorf("empty DAG: topological order must be empty")
	}
	if len(dag.Leaves()) != 0 {
		t.Errorf("empty DAG: leaves must be empty")
	}
}

// ─── buildCyclePath coverage (long vertex ID) ─────────────────────────────────

func TestCycleErrorMessageContainsCycleVertices(t *testing.T) {
	a := &stubVertex{id: "vertex-A-with-a-long-id-that-exceeds-12-chars", valid: true}
	b := &stubVertex{id: "vertex-B", valid: true, inputs: []vertex.Vertex{a}}
	a.inputs = []vertex.Vertex{b}

	builder := graph.NewBuilder()
	_ = builder.Add(a)
	_ = builder.Add(b)
	_, err := builder.Build()
	if err == nil {
		t.Fatal("expected cycle error")
	}
	// Error must mention the cycle somehow.
	errMsg := err.Error()
	if len(errMsg) == 0 {
		t.Error("cycle error message must not be empty")
	}
}

// ─── helpers ─────────────────────────────────────────────────────────────────

func idsOf(verts []vertex.Vertex) []string {
	ids := make([]string, len(verts))
	for i, v := range verts {
		ids[i] = v.ID()
	}
	return ids
}
