package traverse_test

import (
	"context"
	"fmt"
	"testing"

	"github.com/bons/bons-ci/plugins/dag/graph"
	"github.com/bons/bons-ci/plugins/dag/traverse"
	"github.com/bons/bons-ci/plugins/dag/vertex"
)

// ─── PostVisit composition ────────────────────────────────────────────────────

func TestComposePostVisitBothCalled(t *testing.T) {
	// Both post-visit hooks must be called.
	c := sv("C")
	b := sv("B", c)
	a := sv("A", b)

	var logA, logB []string
	hA := traverse.Hooks{
		PostVisit: func(_ context.Context, _ traverse.Context, v vertex.Vertex) (traverse.Action, error) {
			logA = append(logA, "postA:"+v.ID())
			return traverse.Continue, nil
		},
	}
	hB := traverse.Hooks{
		PostVisit: func(_ context.Context, _ traverse.Context, v vertex.Vertex) (traverse.Action, error) {
			logB = append(logB, "postB:"+v.ID())
			return traverse.Continue, nil
		},
	}

	eng := traverse.New(traverse.DFS, hA.Compose(hB))
	if err := eng.TraverseUpstream(context.Background(), a); err != nil {
		t.Fatal(err)
	}
	if len(logA) != 3 {
		t.Errorf("postA: want 3 calls, got %d: %v", len(logA), logA)
	}
	if len(logB) != 3 {
		t.Errorf("postB: want 3 calls, got %d: %v", len(logB), logB)
	}
}

func TestComposePostVisitStopPropagates(t *testing.T) {
	// hA returns Continue, hB returns Stop on "B" → traversal stops.
	c := sv("C")
	b := sv("B", c)
	a := sv("A", b)

	var postVisited []string
	hA := traverse.Hooks{
		PostVisit: func(_ context.Context, _ traverse.Context, v vertex.Vertex) (traverse.Action, error) {
			postVisited = append(postVisited, v.ID())
			return traverse.Continue, nil
		},
	}
	hB := traverse.Hooks{
		PostVisit: func(_ context.Context, _ traverse.Context, v vertex.Vertex) (traverse.Action, error) {
			if v.ID() == "C" {
				return traverse.Stop, nil
			}
			return traverse.Continue, nil
		},
	}

	eng := traverse.New(traverse.DFS, hA.Compose(hB))
	if err := eng.TraverseUpstream(context.Background(), a); err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	// Only C's post-visit should fire (stops immediately after C).
	for _, id := range postVisited {
		if id == "B" || id == "A" {
			t.Errorf("vertex %s should not be post-visited after Stop at C", id)
		}
	}
}

func TestComposePostVisitSkipIsMostRestrictive(t *testing.T) {
	// hA=Skip, hB=Continue → composed result should be Skip.
	// For PostVisit, Skip just means: stop propagation at this vertex.
	// In practice it means the composed result is Skip.
	a := sv("A")

	var hBCalled bool
	hA := traverse.Hooks{
		PostVisit: func(_ context.Context, _ traverse.Context, _ vertex.Vertex) (traverse.Action, error) {
			return traverse.Skip, nil
		},
	}
	hB := traverse.Hooks{
		PostVisit: func(_ context.Context, _ traverse.Context, _ vertex.Vertex) (traverse.Action, error) {
			hBCalled = true
			return traverse.Continue, nil
		},
	}

	eng := traverse.New(traverse.DFS, hA.Compose(hB))
	_ = eng.TraverseUpstream(context.Background(), a)

	// hB must still be called (composition is sequential, not short-circuit on Skip for PostVisit).
	if !hBCalled {
		t.Error("hB PostVisit must be called even when hA returns Skip")
	}
}

// ─── Edge composition ─────────────────────────────────────────────────────────

func TestComposeEdgeBothCalled(t *testing.T) {
	c := sv("C")
	b := sv("B", c)
	a := sv("A", b)

	var edgesA, edgesB []string
	hA := traverse.Hooks{
		OnEdge: func(_ context.Context, parent, child vertex.Vertex, _ int) (traverse.Action, error) {
			edgesA = append(edgesA, fmt.Sprintf("%s→%s", parent.ID(), child.ID()))
			return traverse.Continue, nil
		},
	}
	hB := traverse.Hooks{
		OnEdge: func(_ context.Context, parent, child vertex.Vertex, _ int) (traverse.Action, error) {
			edgesB = append(edgesB, fmt.Sprintf("%s→%s", parent.ID(), child.ID()))
			return traverse.Continue, nil
		},
	}

	eng := traverse.New(traverse.DFS, hA.Compose(hB))
	if err := eng.TraverseUpstream(context.Background(), a); err != nil {
		t.Fatal(err)
	}
	// A→B and B→C: 2 edges each.
	if len(edgesA) != 2 {
		t.Errorf("edgesA: want 2, got %d: %v", len(edgesA), edgesA)
	}
	if len(edgesB) != 2 {
		t.Errorf("edgesB: want 2, got %d: %v", len(edgesB), edgesB)
	}
}

func TestComposeEdgeStopFromSecondHook(t *testing.T) {
	c := sv("C")
	b := sv("B", c)
	a := sv("A", b)

	var visited []string
	hVisit := traverse.Hooks{
		PreVisit: func(_ context.Context, _ traverse.Context, v vertex.Vertex) (traverse.Action, error) {
			visited = append(visited, v.ID())
			return traverse.Continue, nil
		},
	}
	// hB stops all traversal on the A→B edge.
	hStop := traverse.Hooks{
		OnEdge: func(_ context.Context, parent, child vertex.Vertex, _ int) (traverse.Action, error) {
			if parent.ID() == "A" && child.ID() == "B" {
				return traverse.Stop, nil
			}
			return traverse.Continue, nil
		},
	}

	eng := traverse.New(traverse.DFS, hVisit.Compose(hStop))
	if err := eng.TraverseUpstream(context.Background(), a); err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	for _, id := range visited {
		if id == "B" || id == "C" {
			t.Errorf("vertex %s must not be visited after Stop on A→B edge", id)
		}
	}
}

func TestComposeEdgeSkipFromFirstHook(t *testing.T) {
	// hA returns Skip on A→B edge → B should not be visited.
	c := sv("C")
	b := sv("B", c)
	a := sv("A", b)

	var visited []string
	hVisit := traverse.Hooks{
		PreVisit: func(_ context.Context, _ traverse.Context, v vertex.Vertex) (traverse.Action, error) {
			visited = append(visited, v.ID())
			return traverse.Continue, nil
		},
	}
	hSkip := traverse.Hooks{
		OnEdge: func(_ context.Context, parent, child vertex.Vertex, _ int) (traverse.Action, error) {
			if child.ID() == "B" {
				return traverse.Skip, nil
			}
			return traverse.Continue, nil
		},
	}

	eng := traverse.New(traverse.DFS, hVisit.Compose(hSkip))
	_ = eng.TraverseUpstream(context.Background(), a)

	for _, id := range visited {
		if id == "B" || id == "C" {
			t.Errorf("vertex %s should be skipped", id)
		}
	}
}

// ─── BFS downstream ───────────────────────────────────────────────────────────

func TestBFSDownstreamLinear(t *testing.T) {
	ctx := context.Background()
	c := sv("C")
	b := sv("B", c)
	a := sv("A", b)

	dag, err := graph.BuildFromRoot(ctx, a)
	if err != nil {
		t.Fatal(err)
	}

	var visited []string
	eng := traverse.New(traverse.BFS, traverse.Hooks{
		PreVisit: func(_ context.Context, _ traverse.Context, v vertex.Vertex) (traverse.Action, error) {
			visited = append(visited, v.ID())
			return traverse.Continue, nil
		},
	})

	if err := eng.TraverseDownstream(ctx, c, dag); err != nil {
		t.Fatal(err)
	}
	if len(visited) != 3 {
		t.Fatalf("BFS downstream: want 3, got %d: %v", len(visited), visited)
	}
	// C starts at depth 0; A is last.
	if visited[0] != "C" {
		t.Errorf("BFS downstream first: want C, got %s", visited[0])
	}
}

func TestBFSDownstreamSkip(t *testing.T) {
	ctx := context.Background()
	c := sv("C")
	b := sv("B", c)
	a := sv("A", b)

	dag, _ := graph.BuildFromRoot(ctx, a)

	var visited []string
	eng := traverse.New(traverse.BFS, traverse.Hooks{
		PreVisit: func(_ context.Context, _ traverse.Context, v vertex.Vertex) (traverse.Action, error) {
			visited = append(visited, v.ID())
			if v.ID() == "B" {
				return traverse.Skip, nil
			}
			return traverse.Continue, nil
		},
	})

	_ = eng.TraverseDownstream(ctx, c, dag)
	for _, id := range visited {
		if id == "A" {
			t.Error("A should not be visited when B is skipped in BFS downstream")
		}
	}
}

func TestBFSDownstreamStop(t *testing.T) {
	ctx := context.Background()
	c := sv("C")
	b := sv("B", c)
	a := sv("A", b)

	dag, _ := graph.BuildFromRoot(ctx, a)

	var visited []string
	eng := traverse.New(traverse.BFS, traverse.Hooks{
		PreVisit: func(_ context.Context, _ traverse.Context, v vertex.Vertex) (traverse.Action, error) {
			visited = append(visited, v.ID())
			if v.ID() == "B" {
				return traverse.Stop, nil
			}
			return traverse.Continue, nil
		},
	})
	if err := eng.TraverseDownstream(ctx, c, dag); err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	for _, id := range visited {
		if id == "A" {
			t.Error("A should not be visited after Stop at B")
		}
	}
}

func TestDFSDownstreamDiamond(t *testing.T) {
	ctx := context.Background()
	// D is a shared ancestor of both B and C, both feeding into A.
	d := sv("D")
	b := sv("B", d)
	c := sv("C", d)
	a := sv("A", b, c)

	dag, _ := graph.BuildFromRoot(ctx, a)

	var visited []string
	eng := traverse.New(traverse.DFS, traverse.Hooks{
		PreVisit: func(_ context.Context, _ traverse.Context, v vertex.Vertex) (traverse.Action, error) {
			visited = append(visited, v.ID())
			return traverse.Continue, nil
		},
	})
	if err := eng.TraverseDownstream(ctx, d, dag); err != nil {
		t.Fatal(err)
	}

	// D→B→A, D→C→A. A should appear exactly once.
	aCount := 0
	for _, id := range visited {
		if id == "A" {
			aCount++
		}
	}
	if aCount != 1 {
		t.Errorf("A should appear once in downstream diamond, got %d", aCount)
	}
}

func TestDownstreamRequiresDAG(t *testing.T) {
	a := sv("A")
	eng := traverse.New(traverse.DFS, traverse.Hooks{})
	err := eng.TraverseDownstream(context.Background(), a, nil)
	if err == nil {
		t.Error("downstream traversal without DAG should return error")
	}
}

// ─── WithHooks builder ────────────────────────────────────────────────────────

func TestWithHooksAdditive(t *testing.T) {
	// WithHooks composes on top of existing hooks.
	a := sv("A")

	var base, extra []string
	baseHooks := traverse.Hooks{
		PreVisit: func(_ context.Context, _ traverse.Context, v vertex.Vertex) (traverse.Action, error) {
			base = append(base, v.ID())
			return traverse.Continue, nil
		},
	}
	extraHooks := traverse.Hooks{
		PreVisit: func(_ context.Context, _ traverse.Context, v vertex.Vertex) (traverse.Action, error) {
			extra = append(extra, v.ID())
			return traverse.Continue, nil
		},
	}

	eng := traverse.New(traverse.DFS, baseHooks).WithHooks(extraHooks)
	if err := eng.TraverseUpstream(context.Background(), a); err != nil {
		t.Fatal(err)
	}
	if len(base) != 1 || len(extra) != 1 {
		t.Errorf("both hooks must be called: base=%v extra=%v", base, extra)
	}
}
