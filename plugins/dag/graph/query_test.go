package graph_test

import (
	"context"
	"testing"

	"github.com/bons/bons-ci/plugins/dag/graph"
	"github.com/bons/bons-ci/plugins/dag/vertex"
)

// ─── IsAncestor / IsDescendant ────────────────────────────────────────────────

func TestIsAncestorLinear(t *testing.T) {
	ctx := context.Background()
	c := stub("C")
	b := stub("B", c)
	a := stub("A", b)
	dag, _ := graph.BuildFromRoot(ctx, a)

	if !dag.IsAncestor(c, a) {
		t.Error("C should be an ancestor of A")
	}
	if !dag.IsAncestor(b, a) {
		t.Error("B should be an ancestor of A")
	}
	if !dag.IsAncestor(c, b) {
		t.Error("C should be an ancestor of B")
	}
	if dag.IsAncestor(a, c) {
		t.Error("A should NOT be an ancestor of C")
	}
}

func TestIsAncestorSelf(t *testing.T) {
	ctx := context.Background()
	a := stub("A")
	dag, _ := graph.BuildFromRoot(ctx, a)
	if dag.IsAncestor(a, a) {
		t.Error("vertex should NOT be an ancestor of itself")
	}
}

func TestIsAncestorUnrelated(t *testing.T) {
	ctx := context.Background()
	x := stub("X")
	y := stub("Y")
	dag, _ := graph.BuildFromRoots(ctx, x, y)
	if dag.IsAncestor(x, y) {
		t.Error("unrelated vertices should not be ancestors")
	}
}

func TestIsDescendantSymmetry(t *testing.T) {
	ctx := context.Background()
	c := stub("C")
	b := stub("B", c)
	a := stub("A", b)
	dag, _ := graph.BuildFromRoot(ctx, a)

	// IsDescendant is the inverse of IsAncestor.
	if !dag.IsDescendant(a, c) {
		t.Error("A should be a descendant of C")
	}
	if dag.IsDescendant(c, a) {
		t.Error("C should NOT be a descendant of A")
	}
}

// ─── UpstreamCone / DownstreamCone ────────────────────────────────────────────

func TestUpstreamCone(t *testing.T) {
	ctx := context.Background()
	d := stub("D")
	c := stub("C", d)
	b := stub("B", d)
	a := stub("A", b, c)
	dag, _ := graph.BuildFromRoot(ctx, a)

	cone := dag.UpstreamCone(a)
	ids := toIDSet(cone)

	// Cone of A should include B, C, D but not A itself.
	if ids["A"] {
		t.Error("upstream cone must not include the vertex itself")
	}
	if !ids["B"] || !ids["C"] || !ids["D"] {
		t.Errorf("upstream cone of A: want {B,C,D}, got %v", ids)
	}
}

func TestUpstreamConeLeaf(t *testing.T) {
	ctx := context.Background()
	a := stub("A")
	dag, _ := graph.BuildFromRoot(ctx, a)

	cone := dag.UpstreamCone(a)
	if len(cone) != 0 {
		t.Errorf("leaf upstream cone should be empty, got %v", cone)
	}
}

func TestDownstreamCone(t *testing.T) {
	ctx := context.Background()
	d := stub("D")
	b := stub("B", d)
	c := stub("C", d)
	a := stub("A", b, c)
	dag, _ := graph.BuildFromRoot(ctx, a)

	cone := dag.DownstreamCone(d)
	ids := toIDSet(cone)

	// Downstream cone of D should include B, C, A but not D itself.
	if ids["D"] {
		t.Error("downstream cone must not include the vertex itself")
	}
	if !ids["B"] || !ids["C"] || !ids["A"] {
		t.Errorf("downstream cone of D: want {B,C,A}, got %v", ids)
	}
}

func TestDownstreamConeRoot(t *testing.T) {
	ctx := context.Background()
	a := stub("A")
	dag, _ := graph.BuildFromRoot(ctx, a)

	cone := dag.DownstreamCone(a)
	if len(cone) != 0 {
		t.Errorf("root downstream cone should be empty, got %v", cone)
	}
}

// ─── Leaves ───────────────────────────────────────────────────────────────────

func TestLeaves(t *testing.T) {
	ctx := context.Background()
	c := stub("C")
	b := stub("B", c)
	a := stub("A", b)
	dag, _ := graph.BuildFromRoot(ctx, a)

	leaves := dag.Leaves()
	if len(leaves) != 1 || leaves[0].ID() != "C" {
		t.Errorf("leaves: want [C], got %v", leaves)
	}
}

func TestLeavesDiamond(t *testing.T) {
	ctx := context.Background()
	shared := stub("shared")
	b := stub("B", shared)
	c := stub("C", shared)
	a := stub("A", b, c)
	dag, _ := graph.BuildFromRoot(ctx, a)

	leaves := dag.Leaves()
	if len(leaves) != 1 {
		t.Errorf("diamond with one shared leaf: want 1 leaf, got %d", len(leaves))
	}
}

func TestLeavesMultiple(t *testing.T) {
	ctx := context.Background()
	x := stub("X")
	y := stub("Y")
	z := stub("Z", x, y)
	dag, _ := graph.BuildFromRoot(ctx, z)

	leaves := dag.Leaves()
	if len(leaves) != 2 {
		t.Errorf("two leaves: want 2, got %d", len(leaves))
	}
}

// ─── Filter ───────────────────────────────────────────────────────────────────

func TestFilterByKind(t *testing.T) {
	ctx := context.Background()
	// Mix of kinds: we simulate this by using stubVertex with different kinds.
	exec1 := &stubVertex{id: "exec1", kind: "exec", valid: true}
	exec2 := &stubVertex{id: "exec2", kind: "exec", valid: true, inputs: []vertex.Vertex{exec1}}
	src := &stubVertex{id: "src", kind: vertex.KindSource, valid: true}
	root := &stubVertex{id: "root", kind: "exec", valid: true, inputs: []vertex.Vertex{exec2, src}}

	dag, err := graph.BuildFromRoot(ctx, root)
	if err != nil {
		t.Fatal(err)
	}

	filtered, err := dag.Filter(func(v vertex.Vertex) bool {
		return v.Kind() == "exec"
	})
	if err != nil {
		t.Fatal(err)
	}
	if filtered.Len() != 3 {
		t.Errorf("filter exec: want 3, got %d", filtered.Len())
	}
	if filtered.Has("src") {
		t.Error("source should be filtered out")
	}
}

func TestFilterPreservesEdges(t *testing.T) {
	ctx := context.Background()
	a := stub("A")
	b := stub("B", a)
	c := stub("C", b, a) // C depends on both B and A.

	dag, _ := graph.BuildFromRoot(ctx, c)

	// Remove A from the graph. Edges to A from B and C should be removed.
	filtered, err := dag.Filter(func(v vertex.Vertex) bool {
		return v.ID() != "A"
	})
	if err != nil {
		t.Fatal(err)
	}
	if filtered.Has("A") {
		t.Error("A should be filtered out")
	}
	if !filtered.Has("B") || !filtered.Has("C") {
		t.Error("B and C should remain")
	}
	// B now has no inputs (A was removed).
	bInputs := filtered.Upstream(b)
	if len(bInputs) != 0 {
		t.Errorf("B should have no inputs after filtering A, got %v", bInputs)
	}
}

// ─── CommonAncestors ─────────────────────────────────────────────────────────

func TestCommonAncestors(t *testing.T) {
	ctx := context.Background()
	shared1 := stub("shared1")
	shared2 := stub("shared2", shared1)
	a := stub("A", shared2)
	b := stub("B", shared2)

	dag, _ := graph.BuildFromRoots(ctx, a, b)

	common := dag.CommonAncestors(a, b)
	ids := toIDSet(common)

	if !ids["shared2"] {
		t.Error("shared2 must be a common ancestor")
	}
	if !ids["shared1"] {
		t.Error("shared1 must be a common ancestor (transitive)")
	}
	if ids["A"] || ids["B"] {
		t.Error("A and B themselves should not be common ancestors")
	}
}

func TestCommonAncestorsNone(t *testing.T) {
	ctx := context.Background()
	x := stub("X")
	y := stub("Y")
	dag, _ := graph.BuildFromRoots(ctx, x, y)

	common := dag.CommonAncestors(x, y)
	if len(common) != 0 {
		t.Errorf("disjoint vertices: want no common ancestors, got %v", common)
	}
}

// ─── AllVertices ──────────────────────────────────────────────────────────────

func TestAllVertices(t *testing.T) {
	ctx := context.Background()
	b := stub("B")
	a := stub("A", b)
	dag, _ := graph.BuildFromRoot(ctx, a)

	all := dag.AllVertices()
	if len(all) != 2 {
		t.Errorf("want 2, got %d", len(all))
	}
	if _, ok := all["A"]; !ok {
		t.Error("A missing from AllVertices")
	}
}

func TestAllVerticesMutation(t *testing.T) {
	ctx := context.Background()
	a := stub("A")
	dag, _ := graph.BuildFromRoot(ctx, a)

	all := dag.AllVertices()
	// Mutating the returned map should not affect the DAG.
	delete(all, "A")
	if !dag.Has("A") {
		t.Error("mutating AllVertices result must not affect the DAG")
	}
}

// ─── VertexByID ───────────────────────────────────────────────────────────────

func TestVertexByID(t *testing.T) {
	ctx := context.Background()
	a := stub("A")
	dag, _ := graph.BuildFromRoot(ctx, a)

	got := dag.VertexByID("A")
	if got == nil || got.ID() != "A" {
		t.Error("VertexByID should return the vertex")
	}
	if dag.VertexByID("missing") != nil {
		t.Error("missing ID should return nil")
	}
}

// ─── helpers ─────────────────────────────────────────────────────────────────

func toIDSet(vs []vertex.Vertex) map[string]bool {
	m := make(map[string]bool, len(vs))
	for _, v := range vs {
		m[v.ID()] = true
	}
	return m
}
