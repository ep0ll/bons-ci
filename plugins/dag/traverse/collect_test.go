package traverse_test

import (
	"context"
	"fmt"
	"testing"

	"github.com/bons/bons-ci/plugins/dag/ops"
	"github.com/bons/bons-ci/plugins/dag/traverse"
	"github.com/bons/bons-ci/plugins/dag/vertex"
)

// ─── CollectAll ───────────────────────────────────────────────────────────────

func TestCollectAllOrder(t *testing.T) {
	// A → B → C: CollectAll returns post-order [C, B, A].
	c := sv("C")
	b := sv("B", c)
	a := sv("A", b)

	got, err := traverse.CollectAll(context.Background(), a)
	if err != nil {
		t.Fatal(err)
	}
	if len(got) != 3 {
		t.Fatalf("want 3, got %d", len(got))
	}
	// C must come before B (post-order = leaves first).
	pos := func(id string) int {
		for i, v := range got {
			if v.ID() == id {
				return i
			}
		}
		return -1
	}
	if pos("C") >= pos("B") || pos("B") >= pos("A") {
		t.Errorf("post-order violated: C=%d B=%d A=%d", pos("C"), pos("B"), pos("A"))
	}
}

func TestCollectAllDiamondDedup(t *testing.T) {
	d := sv("D")
	b := sv("B", d)
	c := sv("C", d)
	a := sv("A", b, c)

	got, err := traverse.CollectAll(context.Background(), a)
	if err != nil {
		t.Fatal(err)
	}
	// D must appear exactly once.
	n := 0
	for _, v := range got {
		if v.ID() == "D" {
			n++
		}
	}
	if n != 1 {
		t.Errorf("D appeared %d times, want 1", n)
	}
	if len(got) != 4 {
		t.Errorf("want 4, got %d", len(got))
	}
}

func TestCollectAllNil(t *testing.T) {
	got, err := traverse.CollectAll(context.Background(), nil)
	if err != nil {
		t.Fatal(err)
	}
	if len(got) != 0 {
		t.Errorf("nil start: want 0, got %d", len(got))
	}
}

// ─── FindByKind ───────────────────────────────────────────────────────────────

func TestFindByKind(t *testing.T) {
	img := ops.Image("alpine:3.18")
	fileOp := ops.NewFileOp(img.Ref(), ops.Mkdir("/app", 0755), ops.Constraints{})
	execOp := ops.Exec(fileOp.Ref(), ops.WithArgs("ls"), ops.WithCwd("/"))

	got, err := traverse.FindByKind(context.Background(), execOp, vertex.KindSource)
	if err != nil {
		t.Fatal(err)
	}
	if len(got) != 1 {
		t.Fatalf("want 1 source, got %d", len(got))
	}
	if got[0].Kind() != vertex.KindSource {
		t.Errorf("kind: want source, got %s", got[0].Kind())
	}
}

func TestFindByKindMultiple(t *testing.T) {
	// Two source ops feeding into a merge.
	img1 := ops.Image("alpine:3.18")
	img2 := ops.Image("ubuntu:22.04")
	merge := ops.NewMergeOp([]vertex.Ref{img1.Ref(), img2.Ref()}, ops.Constraints{})

	got, err := traverse.FindByKind(context.Background(), merge, vertex.KindSource)
	if err != nil {
		t.Fatal(err)
	}
	if len(got) != 2 {
		t.Errorf("want 2 sources, got %d", len(got))
	}
}

func TestFindByKindNotFound(t *testing.T) {
	img := ops.Image("alpine:3.18")
	// Image is a source; no file ops.
	got, err := traverse.FindByKind(context.Background(), img, vertex.KindFile)
	if err != nil {
		t.Fatal(err)
	}
	if len(got) != 0 {
		t.Errorf("want 0 file ops, got %d", len(got))
	}
}

// ─── Collect ─────────────────────────────────────────────────────────────────

func TestCollectPredicate(t *testing.T) {
	img := ops.Image("alpine:3.18")
	exec1 := ops.Exec(img.Ref(), ops.WithArgs("step1"), ops.WithCwd("/"))
	exec2 := ops.Exec(exec1.RootRef(), ops.WithArgs("step2"), ops.WithCwd("/"))

	got, err := traverse.Collect(context.Background(), exec2, func(v vertex.Vertex) bool {
		return v.Kind() == vertex.KindExec
	})
	if err != nil {
		t.Fatal(err)
	}
	if len(got) != 2 {
		t.Errorf("want 2 exec ops, got %d", len(got))
	}
}

// ─── Find ────────────────────────────────────────────────────────────────────

func TestFindReturnsFirstMatch(t *testing.T) {
	img := ops.Image("alpine:3.18")
	got, err := traverse.Find(context.Background(), img, func(v vertex.Vertex) bool {
		return v.Kind() == vertex.KindSource
	})
	if err != nil {
		t.Fatal(err)
	}
	if got == nil {
		t.Fatal("expected a match, got nil")
	}
	if got.Kind() != vertex.KindSource {
		t.Errorf("kind: want source, got %s", got.Kind())
	}
}

func TestFindNoMatch(t *testing.T) {
	img := ops.Image("alpine:3.18")
	got, err := traverse.Find(context.Background(), img, func(v vertex.Vertex) bool {
		return v.Kind() == vertex.KindExec
	})
	if err != nil {
		t.Fatal(err)
	}
	if got != nil {
		t.Errorf("expected nil, got %v", got)
	}
}

// ─── Count ────────────────────────────────────────────────────────────────────

func TestCount(t *testing.T) {
	d := sv("D")
	b := sv("B", d)
	c := sv("C", d)
	a := sv("A", b, c)

	n, err := traverse.Count(context.Background(), a)
	if err != nil {
		t.Fatal(err)
	}
	if n != 4 {
		t.Errorf("want 4, got %d", n)
	}
}

func TestCountSingleVertex(t *testing.T) {
	v := sv("solo")
	n, err := traverse.Count(context.Background(), v)
	if err != nil {
		t.Fatal(err)
	}
	if n != 1 {
		t.Errorf("single vertex count: want 1, got %d", n)
	}
}

// ─── MaxDepth ─────────────────────────────────────────────────────────────────

func TestMaxDepth(t *testing.T) {
	// A → B → C → D (depth = 3)
	d := sv("D")
	c := sv("C", d)
	b := sv("B", c)
	a := sv("A", b)

	depth, err := traverse.MaxDepth(context.Background(), a)
	if err != nil {
		t.Fatal(err)
	}
	if depth != 3 {
		t.Errorf("max depth: want 3, got %d", depth)
	}
}

func TestMaxDepthLeaf(t *testing.T) {
	v := sv("solo")
	depth, err := traverse.MaxDepth(context.Background(), v)
	if err != nil {
		t.Fatal(err)
	}
	if depth != 0 {
		t.Errorf("leaf depth: want 0, got %d", depth)
	}
}

func TestMaxDepthDiamond(t *testing.T) {
	// A → B → D and A → C → D: max depth = 2 (not 3 because D is shared)
	d := sv("D")
	b := sv("B", d)
	c := sv("C", d)
	a := sv("A", b, c)

	depth, err := traverse.MaxDepth(context.Background(), a)
	if err != nil {
		t.Fatal(err)
	}
	if depth != 2 {
		t.Errorf("diamond max depth: want 2, got %d", depth)
	}
}

// ─── AllPaths ─────────────────────────────────────────────────────────────────

func TestAllPathsLinear(t *testing.T) {
	// A → B → C: one path from A to C.
	c := sv("C")
	b := sv("B", c)
	a := sv("A", b)

	paths, err := traverse.AllPaths(context.Background(), a, c)
	if err != nil {
		t.Fatal(err)
	}
	if len(paths) != 1 {
		t.Fatalf("want 1 path, got %d", len(paths))
	}
	if len(paths[0]) != 3 {
		t.Errorf("path length: want 3, got %d", len(paths[0]))
	}
}

func TestAllPathsDiamond(t *testing.T) {
	// A → B → D and A → C → D: two paths from A to D.
	d := sv("D")
	b := sv("B", d)
	c := sv("C", d)
	a := sv("A", b, c)

	paths, err := traverse.AllPaths(context.Background(), a, d)
	if err != nil {
		t.Fatal(err)
	}
	if len(paths) != 2 {
		t.Errorf("want 2 paths in diamond, got %d", len(paths))
	}
}

func TestAllPathsNoTarget(t *testing.T) {
	a := sv("A")
	notReachable := sv("Z")

	paths, err := traverse.AllPaths(context.Background(), a, notReachable)
	if err != nil {
		t.Fatal(err)
	}
	if len(paths) != 0 {
		t.Errorf("unreachable target: want 0 paths, got %d", len(paths))
	}
}

// ─── WalkFunc ─────────────────────────────────────────────────────────────────

func TestWalkFunc(t *testing.T) {
	c := sv("C")
	b := sv("B", c)
	a := sv("A", b)

	var visited []string
	err := traverse.WalkFunc(context.Background(), a, func(_ context.Context, v vertex.Vertex) error {
		visited = append(visited, v.ID())
		return nil
	})
	if err != nil {
		t.Fatal(err)
	}
	if len(visited) != 3 {
		t.Errorf("want 3 visits, got %d", len(visited))
	}
}

func TestWalkFuncErrorPropagation(t *testing.T) {
	c := sv("C")
	b := sv("B", c)
	a := sv("A", b)

	wantErr := fmt.Errorf("stop here")
	err := traverse.WalkFunc(context.Background(), a, func(_ context.Context, v vertex.Vertex) error {
		if v.ID() == "B" {
			return wantErr
		}
		return nil
	})
	if err == nil {
		t.Fatal("expected error from WalkFunc, got nil")
	}
}

// ─── DigestWalker ─────────────────────────────────────────────────────────────

func TestDigestWalker(t *testing.T) {
	img := ops.Image("alpine:3.18")
	execOp := ops.Exec(img.Ref(), ops.WithArgs("ls"), ops.WithCwd("/"))

	dw := traverse.NewDigestWalker()
	eng := traverse.New(traverse.DFS, dw.Hooks())
	if err := eng.TraverseUpstream(context.Background(), execOp); err != nil {
		t.Fatal(err)
	}

	// Both vertices should have digests.
	if _, ok := dw.Digests[img.ID()]; !ok {
		t.Error("image vertex digest missing")
	}
	if _, ok := dw.Digests[execOp.ID()]; !ok {
		t.Error("exec vertex digest missing")
	}
	// Digests must be non-empty strings.
	for id, d := range dw.Digests {
		if d == "" {
			t.Errorf("empty digest for %s", id)
		}
	}
}

// ─── TracingHooks ─────────────────────────────────────────────────────────────

func TestTracingHooks(t *testing.T) {
	c := sv("C")
	b := sv("B", c)
	a := sv("A", b)

	events := make(chan traverse.SpanEvent, 100)
	hooks := traverse.TracingHooks(events)
	eng := traverse.New(traverse.DFS, hooks)
	if err := eng.TraverseUpstream(context.Background(), a); err != nil {
		t.Fatal(err)
	}
	close(events)

	var pre, post, edge int
	for ev := range events {
		switch ev.Type {
		case "pre":
			pre++
		case "post":
			post++
		case "edge":
			edge++
		}
	}
	// 3 vertices: 3 pre, 3 post, 2 edges (A→B, B→C).
	if pre != 3 {
		t.Errorf("pre events: want 3, got %d", pre)
	}
	if post != 3 {
		t.Errorf("post events: want 3, got %d", post)
	}
	if edge != 2 {
		t.Errorf("edge events: want 2, got %d", edge)
	}
}

// ─── CachingHooks ─────────────────────────────────────────────────────────────

func TestCachingHooksSkipsSubtree(t *testing.T) {
	// Graph: A → B → C. Cache hits on B.
	// Since B is a hit, C should not be visited.
	c := sv("C")
	b := sv("B", c)
	a := sv("A", b)

	var visited []string
	hitFn := func(_ context.Context, v vertex.Vertex) (traverse.CacheResult, error) {
		if v.ID() == "B" {
			return traverse.CacheResult{Hit: true, Key: "b-key"}, nil
		}
		return traverse.CacheResult{Hit: false}, nil
	}

	hits := make(map[string]traverse.CacheResult)
	cacheHooks := traverse.CachingHooks(hitFn, hits)
	visitHooks := traverse.Hooks{
		PreVisit: func(_ context.Context, _ traverse.Context, v vertex.Vertex) (traverse.Action, error) {
			visited = append(visited, v.ID())
			return traverse.Continue, nil
		},
	}

	eng := traverse.New(traverse.DFS, visitHooks.Compose(cacheHooks))
	if err := eng.TraverseUpstream(context.Background(), a); err != nil {
		t.Fatal(err)
	}

	for _, id := range visited {
		if id == "C" {
			t.Error("C must not be visited when B is a cache hit")
		}
	}
	if _, ok := hits["B"]; !ok {
		t.Error("B must appear in hits map")
	}
	if !hits["B"].Hit {
		t.Error("B must be recorded as a hit")
	}
}

func TestCachingHooksMiss(t *testing.T) {
	img := ops.Image("alpine:3.18")
	exec := ops.Exec(img.Ref(), ops.WithArgs("ls"), ops.WithCwd("/"))

	hits := make(map[string]traverse.CacheResult)
	missAll := func(_ context.Context, _ vertex.Vertex) (traverse.CacheResult, error) {
		return traverse.CacheResult{Hit: false}, nil
	}

	eng := traverse.New(traverse.DFS, traverse.CachingHooks(missAll, hits))
	if err := eng.TraverseUpstream(context.Background(), exec); err != nil {
		t.Fatal(err)
	}
	if len(hits) != 2 {
		t.Errorf("all misses: want 2 entries in hits, got %d", len(hits))
	}
}

// ─── Large graph stress test ──────────────────────────────────────────────────

func TestCollectAllLargeGraph(t *testing.T) {
	const N = 800
	prev := sv("v0")
	for i := 1; i < N; i++ {
		curr := sv(fmt.Sprintf("v%d", i), prev)
		prev = curr
	}
	got, err := traverse.CollectAll(context.Background(), prev)
	if err != nil {
		t.Fatal(err)
	}
	if len(got) != N {
		t.Errorf("large graph: want %d, got %d", N, len(got))
	}
	// Post-order: v0 must be first.
	if got[0].ID() != "v0" {
		t.Errorf("first: want v0, got %s", got[0].ID())
	}
}
