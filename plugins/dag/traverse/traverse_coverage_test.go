package traverse_test

import (
	"context"
	"testing"

	"github.com/bons/bons-ci/plugins/dag/graph"
	"github.com/bons/bons-ci/plugins/dag/traverse"
	"github.com/bons/bons-ci/plugins/dag/vertex"
)

// ─── DFS downstream full branch coverage ─────────────────────────────────────

func TestDFSDownstreamDiamondCoverage(t *testing.T) {
	ctx := context.Background()
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

	// D should be visited (start), plus B, C, A.
	if len(visited) != 4 {
		t.Errorf("want 4 visits (D,B,C,A), got %d: %v", len(visited), visited)
	}
	if visited[0] != "D" {
		t.Errorf("first visit: want D, got %s", visited[0])
	}
}

func TestDFSDownstreamSkipFromPreVisit(t *testing.T) {
	ctx := context.Background()
	c := sv("C")
	b := sv("B", c)
	a := sv("A", b)

	dag, _ := graph.BuildFromRoot(ctx, a)

	var visited []string
	eng := traverse.New(traverse.DFS, traverse.Hooks{
		PreVisit: func(_ context.Context, _ traverse.Context, v vertex.Vertex) (traverse.Action, error) {
			visited = append(visited, v.ID())
			if v.ID() == "B" {
				return traverse.Skip, nil // skip B's downstream (A)
			}
			return traverse.Continue, nil
		},
	})
	_ = eng.TraverseDownstream(ctx, c, dag)

	for _, id := range visited {
		if id == "A" {
			t.Error("A must not be visited when B is skipped")
		}
	}
}

func TestDFSDownstreamStopFromPreVisit(t *testing.T) {
	ctx := context.Background()
	c := sv("C")
	b := sv("B", c)
	a := sv("A", b)

	dag, _ := graph.BuildFromRoot(ctx, a)

	var visited []string
	eng := traverse.New(traverse.DFS, traverse.Hooks{
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
			t.Error("A must not be visited after Stop at B")
		}
	}
}

func TestDFSDownstreamPostVisit(t *testing.T) {
	ctx := context.Background()
	b := sv("B")
	a := sv("A", b)

	dag, _ := graph.BuildFromRoot(ctx, a)

	var postVisited []string
	eng := traverse.New(traverse.DFS, traverse.Hooks{
		PostVisit: func(_ context.Context, _ traverse.Context, v vertex.Vertex) (traverse.Action, error) {
			postVisited = append(postVisited, v.ID())
			return traverse.Continue, nil
		},
	})
	if err := eng.TraverseDownstream(ctx, b, dag); err != nil {
		t.Fatal(err)
	}
	// Post-visit order for downstream DFS: B (leaf in downstream direction), then A.
	if len(postVisited) != 2 {
		t.Errorf("want 2 post-visits, got %d: %v", len(postVisited), postVisited)
	}
}

func TestDFSDownstreamOnEdge(t *testing.T) {
	ctx := context.Background()
	c := sv("C")
	b := sv("B", c)
	a := sv("A", b)

	dag, _ := graph.BuildFromRoot(ctx, a)

	var edges []string
	eng := traverse.New(traverse.DFS, traverse.Hooks{
		OnEdge: func(_ context.Context, parent, child vertex.Vertex, _ int) (traverse.Action, error) {
			edges = append(edges, parent.ID()+"→"+child.ID())
			return traverse.Continue, nil
		},
	})
	_ = eng.TraverseDownstream(ctx, c, dag)

	if len(edges) == 0 {
		t.Error("downstream traversal must emit edge events")
	}
}

func TestDFSDownstreamOnEdgeSkip(t *testing.T) {
	ctx := context.Background()
	c := sv("C")
	b := sv("B", c)
	a := sv("A", b)

	dag, _ := graph.BuildFromRoot(ctx, a)

	var visited []string
	eng := traverse.New(traverse.DFS, traverse.Hooks{
		PreVisit: func(_ context.Context, _ traverse.Context, v vertex.Vertex) (traverse.Action, error) {
			visited = append(visited, v.ID())
			return traverse.Continue, nil
		},
		OnEdge: func(_ context.Context, parent, child vertex.Vertex, _ int) (traverse.Action, error) {
			// Skip the C→B edge (in downstream direction).
			if parent.ID() == "C" && child.ID() == "B" {
				return traverse.Skip, nil
			}
			return traverse.Continue, nil
		},
	})
	_ = eng.TraverseDownstream(ctx, c, dag)

	for _, id := range visited {
		if id == "B" || id == "A" {
			t.Errorf("%s must not be visited when C→B edge is skipped", id)
		}
	}
}

func TestDFSDownstreamOnEdgeStop(t *testing.T) {
	ctx := context.Background()
	c := sv("C")
	b := sv("B", c)
	a := sv("A", b)

	dag, _ := graph.BuildFromRoot(ctx, a)

	var visited []string
	eng := traverse.New(traverse.DFS, traverse.Hooks{
		PreVisit: func(_ context.Context, _ traverse.Context, v vertex.Vertex) (traverse.Action, error) {
			visited = append(visited, v.ID())
			return traverse.Continue, nil
		},
		OnEdge: func(_ context.Context, _, child vertex.Vertex, _ int) (traverse.Action, error) {
			if child.ID() == "A" {
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
			t.Error("A must not be visited after Stop on edge to A")
		}
	}
}

func TestDFSDownstreamDeduplication(t *testing.T) {
	ctx := context.Background()
	// Shared: both X and Y depend on D; both X and Y feed into root.
	d := sv("D")
	x := sv("X", d)
	y := sv("Y", d)
	root := sv("ROOT", x, y)

	dag, _ := graph.BuildFromRoot(ctx, root)

	var visited []string
	eng := traverse.New(traverse.DFS, traverse.Hooks{
		PreVisit: func(_ context.Context, _ traverse.Context, v vertex.Vertex) (traverse.Action, error) {
			visited = append(visited, v.ID())
			return traverse.Continue, nil
		},
	})
	_ = eng.TraverseDownstream(ctx, d, dag)

	// root must appear exactly once.
	rootCount := 0
	for _, id := range visited {
		if id == "ROOT" {
			rootCount++
		}
	}
	if rootCount != 1 {
		t.Errorf("ROOT must appear once downstream, got %d: %v", rootCount, visited)
	}
}

// ─── BFS downstream full branch coverage ─────────────────────────────────────

func TestBFSDownstreamPostVisit(t *testing.T) {
	ctx := context.Background()
	b := sv("B")
	a := sv("A", b)

	dag, _ := graph.BuildFromRoot(ctx, a)

	var postVisited []string
	eng := traverse.New(traverse.BFS, traverse.Hooks{
		PostVisit: func(_ context.Context, _ traverse.Context, v vertex.Vertex) (traverse.Action, error) {
			postVisited = append(postVisited, v.ID())
			return traverse.Continue, nil
		},
	})
	_ = eng.TraverseDownstream(ctx, b, dag)

	if len(postVisited) != 2 {
		t.Errorf("BFS downstream post-visit: want 2, got %d: %v", len(postVisited), postVisited)
	}
}

func TestBFSDownstreamEdgeSkip(t *testing.T) {
	ctx := context.Background()
	c := sv("C")
	b := sv("B", c)
	a := sv("A", b)

	dag, _ := graph.BuildFromRoot(ctx, a)

	var visited []string
	eng := traverse.New(traverse.BFS, traverse.Hooks{
		PreVisit: func(_ context.Context, _ traverse.Context, v vertex.Vertex) (traverse.Action, error) {
			visited = append(visited, v.ID())
			return traverse.Continue, nil
		},
		OnEdge: func(_ context.Context, parent, child vertex.Vertex, _ int) (traverse.Action, error) {
			if parent.ID() == "B" {
				return traverse.Skip, nil
			}
			return traverse.Continue, nil
		},
	})
	_ = eng.TraverseDownstream(ctx, c, dag)

	for _, id := range visited {
		if id == "A" {
			t.Error("A must not be visited when B's downstream edges are skipped")
		}
	}
}

func TestBFSDownstreamEdgeStop(t *testing.T) {
	ctx := context.Background()
	c := sv("C")
	b := sv("B", c)
	a := sv("A", b)

	dag, _ := graph.BuildFromRoot(ctx, a)

	var visited []string
	eng := traverse.New(traverse.BFS, traverse.Hooks{
		PreVisit: func(_ context.Context, _ traverse.Context, v vertex.Vertex) (traverse.Action, error) {
			visited = append(visited, v.ID())
			return traverse.Continue, nil
		},
		OnEdge: func(_ context.Context, _, child vertex.Vertex, _ int) (traverse.Action, error) {
			if child.ID() == "A" {
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
			t.Error("A must not be visited after Stop on edge to A")
		}
	}
}

// ─── BFS upstream additional branches ────────────────────────────────────────

func TestBFSUpstreamPostVisit(t *testing.T) {
	c := sv("C")
	b := sv("B", c)
	a := sv("A", b)

	var postVisited []string
	eng := traverse.New(traverse.BFS, traverse.Hooks{
		PostVisit: func(_ context.Context, _ traverse.Context, v vertex.Vertex) (traverse.Action, error) {
			postVisited = append(postVisited, v.ID())
			return traverse.Continue, nil
		},
	})
	_ = eng.TraverseUpstream(context.Background(), a)

	if len(postVisited) != 3 {
		t.Errorf("BFS upstream post-visit: want 3, got %d: %v", len(postVisited), postVisited)
	}
}

func TestBFSUpstreamEdgeStop(t *testing.T) {
	c := sv("C")
	b := sv("B", c)
	a := sv("A", b)

	var visited []string
	eng := traverse.New(traverse.BFS, traverse.Hooks{
		PreVisit: func(_ context.Context, _ traverse.Context, v vertex.Vertex) (traverse.Action, error) {
			visited = append(visited, v.ID())
			return traverse.Continue, nil
		},
		OnEdge: func(_ context.Context, _, child vertex.Vertex, _ int) (traverse.Action, error) {
			if child.ID() == "B" {
				return traverse.Stop, nil
			}
			return traverse.Continue, nil
		},
	})
	if err := eng.TraverseUpstream(context.Background(), a); err != nil {
		t.Fatalf("unexpected: %v", err)
	}
	for _, id := range visited {
		if id == "C" {
			t.Error("C must not be visited after Stop on A→B edge")
		}
	}
}

// ─── Unknown mode ─────────────────────────────────────────────────────────────

func TestUnknownModeUpstream(t *testing.T) {
	eng := traverse.New(traverse.Mode(99), traverse.Hooks{})
	err := eng.TraverseUpstream(context.Background(), sv("X"))
	if err == nil {
		t.Error("unknown mode must return an error")
	}
}

func TestUnknownModeDownstream(t *testing.T) {
	ctx := context.Background()
	x := sv("X")
	dag, _ := graph.BuildFromRoot(ctx, x)
	eng := traverse.New(traverse.Mode(99), traverse.Hooks{})
	err := eng.TraverseDownstream(ctx, x, dag)
	if err == nil {
		t.Error("unknown mode must return an error")
	}
}
