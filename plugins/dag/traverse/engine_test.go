package traverse_test

import (
	"context"
	"fmt"
	"sync/atomic"
	"testing"

	"github.com/bons/bons-ci/plugins/dag/graph"
	"github.com/bons/bons-ci/plugins/dag/traverse"
	"github.com/bons/bons-ci/plugins/dag/vertex"
)

// ─── Test helpers ─────────────────────────────────────────────────────────────

type stubV struct {
	id     string
	kind   vertex.Kind
	inputs []vertex.Vertex
}

func sv(id string, inputs ...vertex.Vertex) *stubV {
	return &stubV{id: id, kind: vertex.KindSource, inputs: inputs}
}

func (s *stubV) ID() string                       { return s.id }
func (s *stubV) Kind() vertex.Kind                { return s.kind }
func (s *stubV) Inputs() []vertex.Vertex          { return s.inputs }
func (s *stubV) Validate(_ context.Context) error { return nil }
func (s *stubV) Name() string                     { return s.id }

// recorder collects the order in which Pre/Post-Visit and Edge hooks are called.
type recorder struct {
	preVisit  []string
	postVisit []string
	edges     []string
}

func (r *recorder) hooks() traverse.Hooks {
	return traverse.Hooks{
		PreVisit: func(_ context.Context, tc traverse.Context, v vertex.Vertex) (traverse.Action, error) {
			r.preVisit = append(r.preVisit, v.ID())
			return traverse.Continue, nil
		},
		PostVisit: func(_ context.Context, tc traverse.Context, v vertex.Vertex) (traverse.Action, error) {
			r.postVisit = append(r.postVisit, v.ID())
			return traverse.Continue, nil
		},
		OnEdge: func(_ context.Context, parent, child vertex.Vertex, idx int) (traverse.Action, error) {
			r.edges = append(r.edges, fmt.Sprintf("%s→%s", parent.ID(), child.ID()))
			return traverse.Continue, nil
		},
	}
}

// ─── DFS upstream ─────────────────────────────────────────────────────────────

func TestDFSLinearPreVisitOrder(t *testing.T) {
	// A → B → C
	c := sv("C")
	b := sv("B", c)
	a := sv("A", b)

	rec := &recorder{}
	eng := traverse.New(traverse.DFS, rec.hooks())
	if err := eng.TraverseUpstream(context.Background(), a); err != nil {
		t.Fatal(err)
	}

	// DFS PreVisit order for A→B→C: A, B, C (follows the path depth-first before siblings)
	wantPre := []string{"A", "B", "C"}
	if fmt.Sprint(rec.preVisit) != fmt.Sprint(wantPre) {
		t.Errorf("DFS pre-visit order: want %v, got %v", wantPre, rec.preVisit)
	}

	// PostVisit order is the reverse (leaves exit first).
	wantPost := []string{"C", "B", "A"}
	if fmt.Sprint(rec.postVisit) != fmt.Sprint(wantPost) {
		t.Errorf("DFS post-visit order: want %v, got %v", wantPost, rec.postVisit)
	}
}

func TestDFSPreVisitBeforePostVisit(t *testing.T) {
	// For any vertex, PreVisit must be called before PostVisit.
	c := sv("C")
	b := sv("B", c)
	a := sv("A", b)

	var events []string
	eng := traverse.New(traverse.DFS, traverse.Hooks{
		PreVisit: func(_ context.Context, _ traverse.Context, v vertex.Vertex) (traverse.Action, error) {
			events = append(events, "pre:"+v.ID())
			return traverse.Continue, nil
		},
		PostVisit: func(_ context.Context, _ traverse.Context, v vertex.Vertex) (traverse.Action, error) {
			events = append(events, "post:"+v.ID())
			return traverse.Continue, nil
		},
	})
	if err := eng.TraverseUpstream(context.Background(), a); err != nil {
		t.Fatal(err)
	}

	// Verify each vertex: pre comes before post.
	seen := map[string]int{} // id → last pre index
	for i, ev := range events {
		if len(ev) > 4 && ev[:4] == "pre:" {
			seen[ev[4:]] = i
		} else if len(ev) > 5 && ev[:5] == "post:" {
			id := ev[5:]
			if preIdx, ok := seen[id]; !ok || preIdx >= i {
				t.Errorf("post:%s at index %d without prior pre (or pre comes after)", id, i)
			}
		}
	}
}

func TestDFSDiamondVisitOnce(t *testing.T) {
	// D is shared: A→B→D, A→C→D. D must be visited exactly once.
	d := sv("D")
	b := sv("B", d)
	c := sv("C", d)
	a := sv("A", b, c)

	rec := &recorder{}
	eng := traverse.New(traverse.DFS, rec.hooks())
	if err := eng.TraverseUpstream(context.Background(), a); err != nil {
		t.Fatal(err)
	}

	countD := 0
	for _, id := range rec.preVisit {
		if id == "D" {
			countD++
		}
	}
	if countD != 1 {
		t.Errorf("D should be visited exactly once in a diamond, visited %d times", countD)
	}
}

func TestDFSEdgeHook(t *testing.T) {
	// A → B → C; edges should be recorded as A→B and B→C.
	c := sv("C")
	b := sv("B", c)
	a := sv("A", b)

	rec := &recorder{}
	eng := traverse.New(traverse.DFS, rec.hooks())
	if err := eng.TraverseUpstream(context.Background(), a); err != nil {
		t.Fatal(err)
	}

	wantEdges := []string{"A→B", "B→C"}
	if fmt.Sprint(rec.edges) != fmt.Sprint(wantEdges) {
		t.Errorf("edges: want %v, got %v", wantEdges, rec.edges)
	}
}

// ─── BFS upstream ─────────────────────────────────────────────────────────────

func TestBFSLinearOrder(t *testing.T) {
	// A → B → C. BFS visits by level: A, B, C.
	c := sv("C")
	b := sv("B", c)
	a := sv("A", b)

	rec := &recorder{}
	eng := traverse.New(traverse.BFS, rec.hooks())
	if err := eng.TraverseUpstream(context.Background(), a); err != nil {
		t.Fatal(err)
	}

	// BFS should visit A first (depth 0), B second (depth 1), C third (depth 2).
	if len(rec.preVisit) != 3 {
		t.Fatalf("expected 3 visits, got %d", len(rec.preVisit))
	}
	if rec.preVisit[0] != "A" {
		t.Errorf("BFS first visit: want A, got %s", rec.preVisit[0])
	}
}

func TestBFSDiamondVisitOnce(t *testing.T) {
	d := sv("D")
	b := sv("B", d)
	c := sv("C", d)
	a := sv("A", b, c)

	rec := &recorder{}
	eng := traverse.New(traverse.BFS, rec.hooks())
	if err := eng.TraverseUpstream(context.Background(), a); err != nil {
		t.Fatal(err)
	}

	countD := 0
	for _, id := range rec.preVisit {
		if id == "D" {
			countD++
		}
	}
	if countD != 1 {
		t.Errorf("BFS: D should be visited once, got %d", countD)
	}
}

// ─── Skip action ──────────────────────────────────────────────────────────────

func TestSkipPreventsInputTraversal(t *testing.T) {
	// A → B → C. Skip B. C should not be visited.
	c := sv("C")
	b := sv("B", c)
	a := sv("A", b)

	var visited []string
	eng := traverse.New(traverse.DFS, traverse.Hooks{
		PreVisit: func(_ context.Context, _ traverse.Context, v vertex.Vertex) (traverse.Action, error) {
			visited = append(visited, v.ID())
			if v.ID() == "B" {
				return traverse.Skip, nil
			}
			return traverse.Continue, nil
		},
	})
	if err := eng.TraverseUpstream(context.Background(), a); err != nil {
		t.Fatal(err)
	}

	for _, id := range visited {
		if id == "C" {
			t.Error("C should not be visited when B is skipped")
		}
	}
	// A and B must be visited.
	found := map[string]bool{}
	for _, id := range visited {
		found[id] = true
	}
	if !found["A"] || !found["B"] {
		t.Errorf("A and B must be visited; got: %v", visited)
	}
}

func TestSkipEdgePreventsChildTraversal(t *testing.T) {
	// A → B → C. Skip the A→B edge. B and C should not be visited.
	c := sv("C")
	b := sv("B", c)
	a := sv("A", b)

	var visited []string
	eng := traverse.New(traverse.DFS, traverse.Hooks{
		PreVisit: func(_ context.Context, _ traverse.Context, v vertex.Vertex) (traverse.Action, error) {
			visited = append(visited, v.ID())
			return traverse.Continue, nil
		},
		OnEdge: func(_ context.Context, parent, child vertex.Vertex, _ int) (traverse.Action, error) {
			if parent.ID() == "A" && child.ID() == "B" {
				return traverse.Skip, nil
			}
			return traverse.Continue, nil
		},
	})
	if err := eng.TraverseUpstream(context.Background(), a); err != nil {
		t.Fatal(err)
	}
	for _, id := range visited {
		if id == "B" || id == "C" {
			t.Errorf("vertex %s should not be visited when A→B edge is skipped", id)
		}
	}
}

// ─── Stop action ──────────────────────────────────────────────────────────────

func TestStopHaltsTraversal(t *testing.T) {
	// A → B → C → D. Stop when we see C. D must not be visited.
	d := sv("D")
	c := sv("C", d)
	b := sv("B", c)
	a := sv("A", b)

	var visited []string
	eng := traverse.New(traverse.DFS, traverse.Hooks{
		PreVisit: func(_ context.Context, _ traverse.Context, v vertex.Vertex) (traverse.Action, error) {
			visited = append(visited, v.ID())
			if v.ID() == "C" {
				return traverse.Stop, nil
			}
			return traverse.Continue, nil
		},
	})
	// Stop returns nil error (Stop is a control signal, not an error).
	if err := eng.TraverseUpstream(context.Background(), a); err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	for _, id := range visited {
		if id == "D" {
			t.Error("D must not be visited after Stop at C")
		}
	}
}

// ─── Depth tracking ───────────────────────────────────────────────────────────

func TestDepthTracking(t *testing.T) {
	// A(0) → B(1) → C(2)
	c := sv("C")
	b := sv("B", c)
	a := sv("A", b)

	depths := map[string]int{}
	eng := traverse.New(traverse.DFS, traverse.Hooks{
		PreVisit: func(_ context.Context, tc traverse.Context, v vertex.Vertex) (traverse.Action, error) {
			depths[v.ID()] = tc.Depth
			return traverse.Continue, nil
		},
	})
	if err := eng.TraverseUpstream(context.Background(), a); err != nil {
		t.Fatal(err)
	}
	if depths["A"] != 0 {
		t.Errorf("A depth: want 0, got %d", depths["A"])
	}
	if depths["B"] != 1 {
		t.Errorf("B depth: want 1, got %d", depths["B"])
	}
	if depths["C"] != 2 {
		t.Errorf("C depth: want 2, got %d", depths["C"])
	}
}

// ─── Nil hooks are safe ────────────────────────────────────────────────────────

func TestNilHooksAreSafe(t *testing.T) {
	c := sv("C")
	b := sv("B", c)
	a := sv("A", b)

	eng := traverse.New(traverse.DFS, traverse.Hooks{})
	if err := eng.TraverseUpstream(context.Background(), a); err != nil {
		t.Fatalf("nil hooks: %v", err)
	}
}

func TestNilStartVertex(t *testing.T) {
	eng := traverse.New(traverse.DFS, traverse.Hooks{})
	if err := eng.TraverseUpstream(context.Background(), nil); err != nil {
		t.Fatalf("nil start: %v", err)
	}
}

// ─── Hook composition ─────────────────────────────────────────────────────────

func TestHookComposition(t *testing.T) {
	c := sv("C")
	b := sv("B", c)
	a := sv("A", b)

	var countA, countB int32
	hooksA := traverse.Hooks{
		PreVisit: func(_ context.Context, _ traverse.Context, v vertex.Vertex) (traverse.Action, error) {
			atomic.AddInt32(&countA, 1)
			return traverse.Continue, nil
		},
	}
	hooksB := traverse.Hooks{
		PreVisit: func(_ context.Context, _ traverse.Context, v vertex.Vertex) (traverse.Action, error) {
			atomic.AddInt32(&countB, 1)
			return traverse.Continue, nil
		},
	}

	combined := hooksA.Compose(hooksB)
	eng := traverse.New(traverse.DFS, combined)
	if err := eng.TraverseUpstream(context.Background(), a); err != nil {
		t.Fatal(err)
	}

	// Both hooks should have been called once per vertex (3 vertices).
	if countA != 3 {
		t.Errorf("hooksA called %d times, want 3", countA)
	}
	if countB != 3 {
		t.Errorf("hooksB called %d times, want 3", countB)
	}
}

func TestCompositionStopIsMostRestrictive(t *testing.T) {
	// hooksA returns Continue, hooksB returns Stop.
	// Composed result should be Stop → traversal halts after start vertex.
	c := sv("C")
	b := sv("B", c)
	a := sv("A", b)

	var visited []string
	hooksA := traverse.Hooks{
		PreVisit: func(_ context.Context, _ traverse.Context, v vertex.Vertex) (traverse.Action, error) {
			visited = append(visited, v.ID())
			return traverse.Continue, nil
		},
	}
	hooksB := traverse.Hooks{
		PreVisit: func(_ context.Context, _ traverse.Context, v vertex.Vertex) (traverse.Action, error) {
			if v.ID() == "A" {
				return traverse.Stop, nil
			}
			return traverse.Continue, nil
		},
	}

	eng := traverse.New(traverse.DFS, hooksA.Compose(hooksB))
	_ = eng.TraverseUpstream(context.Background(), a)

	if len(visited) > 1 {
		t.Errorf("Stop from hooksB should halt after A; visited: %v", visited)
	}
}

// ─── Downstream traversal ─────────────────────────────────────────────────────

func TestDownstreamTraversal(t *testing.T) {
	ctx := context.Background()
	// A → B → C (A depends on B which depends on C).
	// Downstream from C: C, B, A.
	c := sv("C")
	b := sv("B", c)
	a := sv("A", b)

	dag, err := graph.BuildFromRoot(ctx, a)
	if err != nil {
		t.Fatal(err)
	}

	var visited []string
	eng := traverse.New(traverse.DFS, traverse.Hooks{
		PreVisit: func(_ context.Context, _ traverse.Context, v vertex.Vertex) (traverse.Action, error) {
			visited = append(visited, v.ID())
			return traverse.Continue, nil
		},
	})

	if err := eng.TraverseDownstream(ctx, c, dag); err != nil {
		t.Fatal(err)
	}

	// C itself should be first, then B, then A.
	if len(visited) != 3 {
		t.Fatalf("expected 3 visits, got %d: %v", len(visited), visited)
	}
	if visited[0] != "C" {
		t.Errorf("first downstream visit: want C, got %s", visited[0])
	}
}

// ─── Large graph traversal ────────────────────────────────────────────────────

func TestLargeGraphTraversal(t *testing.T) {
	// 500-node linear chain. All nodes must be visited exactly once.
	const N = 500
	prev := sv("v0")
	for i := 1; i < N; i++ {
		curr := sv(fmt.Sprintf("v%d", i), prev)
		prev = curr
	}

	var count int32
	eng := traverse.New(traverse.DFS, traverse.Hooks{
		PreVisit: func(_ context.Context, _ traverse.Context, v vertex.Vertex) (traverse.Action, error) {
			atomic.AddInt32(&count, 1)
			return traverse.Continue, nil
		},
	})
	if err := eng.TraverseUpstream(context.Background(), prev); err != nil {
		t.Fatal(err)
	}
	if count != N {
		t.Errorf("expected %d visits, got %d", N, count)
	}
}

// ─── Context cancellation ─────────────────────────────────────────────────────

func TestContextCancellation(t *testing.T) {
	// Hook returns an error when context is cancelled.
	c := sv("C")
	b := sv("B", c)
	a := sv("A", b)

	ctx, cancel := context.WithCancel(context.Background())

	eng := traverse.New(traverse.DFS, traverse.Hooks{
		PreVisit: func(ctx context.Context, _ traverse.Context, v vertex.Vertex) (traverse.Action, error) {
			if v.ID() == "B" {
				cancel() // cancel after visiting B
			}
			if err := ctx.Err(); err != nil {
				return traverse.Stop, nil
			}
			return traverse.Continue, nil
		},
	})

	// Should not panic or deadlock.
	_ = eng.TraverseUpstream(ctx, a)
}
