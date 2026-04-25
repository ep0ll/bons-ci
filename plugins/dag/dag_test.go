// Package dag_test is an integration test that exercises the full pipeline:
// building a DAG from real ops, validating it, traversing with hooks,
// querying with graph utilities, and serializing to JSON.
//
// The scenario is a realistic two-stage build:
//
//	Stage 1 (builder): compile Go binaries from source
//	Stage 2 (runtime): copy the binary into a minimal runtime image
//
// This mirrors a common Dockerfile multi-stage build pattern.
package dag_test

import (
	"context"
	"encoding/json"
	"fmt"
	"sync"
	"sync/atomic"
	"testing"

	"github.com/bons/bons-ci/plugins/dag/graph"
	"github.com/bons/bons-ci/plugins/dag/marshal"
	"github.com/bons/bons-ci/plugins/dag/ops"
	"github.com/bons/bons-ci/plugins/dag/traverse"
	"github.com/bons/bons-ci/plugins/dag/vertex"
)

// ─── Test fixture: two-stage Go build ────────────────────────────────────────

// buildGoApp builds the following DAG:
//
//	golang:1.22 ─────────────────────────────┐
//	                                          ├─→ compile (exec) ─→ binary (mount)
//	local:. ─→ file(cp src) ─────────────────┘
//	                                               ↓
//	alpine:3.19 ─→ file(mkdir /app) ─→ file(cp binary) ─→ run chmod ─→ [root]
//
// Returns the final exec vertex (root) and all significant intermediate refs.
func buildGoApp(t *testing.T) vertex.Vertex {
	t.Helper()

	// Stage 1 — builder image.
	builderImage := ops.Image("golang:1.22")

	// Copy source code from the local context into the builder.
	localCtx := ops.Local(".", ops.WithLocalInclude("**/*.go", "go.mod", "go.sum"))
	copySrc := ops.NewFileOp(
		builderImage.Ref(),
		ops.Copy(localCtx.Ref(), ".", "/workspace/src",
			ops.WithCreateDestPath(),
		),
		ops.Constraints{},
	)

	// Compile step.
	compile := ops.Exec(copySrc.Ref(),
		ops.WithArgs("go", "build", "-o", "/out/myapp", "./cmd/myapp"),
		ops.WithCwd("/workspace/src"),
		ops.WithEnv("CGO_ENABLED=0", "GOOS=linux"),
		ops.WithMount(ops.Mount{
			Target:   "/out",
			Source:   vertex.Ref{}, // scratch output directory
			Readonly: false,
		}),
	)

	// Stage 2 — runtime image.
	runtimeImage := ops.Image("alpine:3.19")

	// Create app directory in the runtime image.
	mkAppDir := ops.NewFileOp(
		runtimeImage.Ref(),
		ops.Mkdir("/app", 0755, ops.WithParents(true)),
		ops.Constraints{},
	)

	// Copy binary from compile stage.
	binaryRef := compile.MountRef("/out")

	copyBinary := ops.NewFileOp(
		mkAppDir.Ref(),
		ops.Copy(binaryRef, "/myapp", "/app/myapp",
			ops.WithCreateDestPath(),
		),
		ops.Constraints{},
	)

	// Final: run chmod on binary.
	finalRun := ops.Exec(copyBinary.Ref(),
		ops.WithArgs("chmod", "+x", "/app/myapp"),
		ops.WithCwd("/"),
	)

	return finalRun
}

// ─── Full pipeline integration test ──────────────────────────────────────────

func TestIntegrationBuildAndValidate(t *testing.T) {
	ctx := context.Background()
	root := buildGoApp(t)

	dag, err := graph.BuildFromRoot(ctx, root)
	if err != nil {
		t.Fatalf("build: %v", err)
	}

	// Verify structure.
	if dag.Len() == 0 {
		t.Fatal("DAG must not be empty")
	}
	roots := dag.Roots()
	if len(roots) != 1 {
		t.Errorf("want 1 root, got %d", len(roots))
	}
	if roots[0].ID() != root.ID() {
		t.Error("root vertex should be the final exec op")
	}

	// Verify topological order is valid.
	order := dag.TopologicalOrder()
	if len(order) == 0 {
		t.Fatal("empty topological order")
	}
	// Every vertex's inputs must appear earlier in the order.
	pos := make(map[string]int, len(order))
	for i, v := range order {
		pos[v.ID()] = i
	}
	for _, v := range order {
		for _, inp := range v.Inputs() {
			if pos[inp.ID()] >= pos[v.ID()] {
				t.Errorf("vertex %q (%s) appears before its input %q",
					v.ID(), v.Kind(), inp.ID())
			}
		}
	}
}

// ─── Traversal with multiple hook concerns ────────────────────────────────────

func TestIntegrationTraversalHooks(t *testing.T) {
	ctx := context.Background()
	root := buildGoApp(t)

	// Concern 1: count vertices by kind.
	kindCounts := make(map[vertex.Kind]int)
	var mu sync.Mutex
	countHooks := traverse.Hooks{
		PreVisit: func(_ context.Context, _ traverse.Context, v vertex.Vertex) (traverse.Action, error) {
			mu.Lock()
			kindCounts[v.Kind()]++
			mu.Unlock()
			return traverse.Continue, nil
		},
	}

	// Concern 2: collect exec args for debugging.
	var execArgs [][]string
	execHooks := traverse.Hooks{
		PostVisit: func(_ context.Context, _ traverse.Context, v vertex.Vertex) (traverse.Action, error) {
			if e, ok := v.(*ops.ExecOp); ok {
				mu.Lock()
				execArgs = append(execArgs, e.Meta().Args)
				mu.Unlock()
			}
			return traverse.Continue, nil
		},
	}

	// Concern 3: digest walker (demonstrates bottom-up computation).
	dw := traverse.NewDigestWalker()

	// Compose all three hook sets into one traversal.
	combined := countHooks.Compose(execHooks).Compose(dw.Hooks())
	eng := traverse.New(traverse.DFS, combined)
	if err := eng.TraverseUpstream(ctx, root); err != nil {
		t.Fatalf("traversal: %v", err)
	}

	// Verify source count (golang:1.22 + local:. + alpine:3.19 = 3).
	if kindCounts[vertex.KindSource] != 3 {
		t.Errorf("source count: want 3, got %d", kindCounts[vertex.KindSource])
	}

	// Verify exec count (compile + chmod = 2).
	if kindCounts[vertex.KindExec] != 2 {
		t.Errorf("exec count: want 2, got %d", kindCounts[vertex.KindExec])
	}

	// Verify exec args were collected.
	if len(execArgs) != 2 {
		t.Errorf("exec args: want 2, got %d: %v", len(execArgs), execArgs)
	}

	// Verify digest walker computed digests for all vertices.
	n, err := traverse.Count(ctx, root)
	if err != nil {
		t.Fatal(err)
	}
	if len(dw.Digests) != n {
		t.Errorf("digest walker: want %d digests, got %d", n, len(dw.Digests))
	}
}

// ─── Cache-skip integration ────────────────────────────────────────────────────

func TestIntegrationCacheSkip(t *testing.T) {
	ctx := context.Background()

	// Build a graph: A → B → C → D (leaf). Cache hits on B.
	// D and C must NOT be traversed (they are B's subtree).
	// A must be traversed (it's not a cache hit).
	d := sv("D")
	c := sv("C", d)
	b := sv("B", c)
	a := sv("A", b)

	var traversed []string
	var mu sync.Mutex

	visitHooks := traverse.Hooks{
		PreVisit: func(_ context.Context, _ traverse.Context, v vertex.Vertex) (traverse.Action, error) {
			mu.Lock()
			traversed = append(traversed, v.ID())
			mu.Unlock()
			return traverse.Continue, nil
		},
	}

	cacheHooks := traverse.CachingHooks(
		func(_ context.Context, v vertex.Vertex) (traverse.CacheResult, error) {
			if v.ID() == "B" {
				return traverse.CacheResult{Hit: true, Key: "b-cache"}, nil
			}
			return traverse.CacheResult{Hit: false}, nil
		},
		nil,
	)

	eng := traverse.New(traverse.DFS, visitHooks.Compose(cacheHooks))
	if err := eng.TraverseUpstream(ctx, a); err != nil {
		t.Fatal(err)
	}

	traversedSet := make(map[string]bool)
	for _, id := range traversed {
		traversedSet[id] = true
	}

	if !traversedSet["A"] {
		t.Error("A must be traversed")
	}
	if !traversedSet["B"] {
		t.Error("B must be traversed (cache check happens at PreVisit)")
	}
	if traversedSet["C"] {
		t.Error("C must NOT be traversed (B is a cache hit)")
	}
	if traversedSet["D"] {
		t.Error("D must NOT be traversed (B is a cache hit)")
	}
}

// ─── Serialization round-trip ─────────────────────────────────────────────────

func TestIntegrationMarshalRoundTrip(t *testing.T) {
	ctx := context.Background()
	root := buildGoApp(t)

	dag, err := graph.BuildFromRoot(ctx, root)
	if err != nil {
		t.Fatalf("build: %v", err)
	}

	m := marshal.New()
	data, err := m.MarshalToJSON(ctx, dag, root)
	if err != nil {
		t.Fatalf("marshal: %v", err)
	}
	if len(data) == 0 {
		t.Fatal("empty serialization")
	}

	// Unmarshal and verify.
	def, err := marshal.Unmarshal(data)
	if err != nil {
		t.Fatalf("unmarshal: %v", err)
	}
	if def.Version != marshal.Version {
		t.Errorf("version: want %q, got %q", marshal.Version, def.Version)
	}
	if len(def.Ops) != dag.Len() {
		t.Errorf("op count: want %d, got %d", dag.Len(), len(def.Ops))
	}

	// Every vertex in the DAG must appear in the serialized form.
	serializedIDs := make(map[string]bool)
	for _, op := range def.Ops {
		serializedIDs[op.ID] = true
	}
	for _, v := range dag.TopologicalOrder() {
		if !serializedIDs[v.ID()] {
			t.Errorf("vertex %q not found in serialized definition", v.ID())
		}
	}
}

func TestIntegrationMarshalDeterminism(t *testing.T) {
	ctx := context.Background()

	buildDigest := func() string {
		root := buildGoApp(t)
		dag, _ := graph.BuildFromRoot(ctx, root)
		m := marshal.New()
		def, err := m.Marshal(ctx, dag, root)
		if err != nil {
			t.Fatalf("marshal: %v", err)
		}
		return def.Digest
	}

	first := buildDigest()
	for i := 0; i < 15; i++ {
		got := buildDigest()
		if got != first {
			t.Errorf("run %d: non-deterministic digest: %q vs %q", i+1, first, got)
		}
	}
}

// ─── Subgraph extraction ──────────────────────────────────────────────────────

func TestIntegrationSubgraph(t *testing.T) {
	ctx := context.Background()

	// Build a full graph with two independent pipelines.
	pipelineA := buildGoApp(t)
	pipelineB := ops.Exec(
		ops.Image("python:3.12").Ref(),
		ops.WithArgs("python", "-m", "pytest"),
		ops.WithCwd("/workspace"),
	)

	dag, err := graph.BuildFromRoots(ctx, pipelineA, pipelineB)
	if err != nil {
		t.Fatalf("build: %v", err)
	}

	// Extract only the Go pipeline.
	subA, err := dag.Subgraph(pipelineA)
	if err != nil {
		t.Fatalf("subgraph A: %v", err)
	}
	if subA.Has(pipelineB.ID()) {
		t.Error("Python pipeline should not be in Go subgraph")
	}
	// Python's source image should not be in the Go subgraph.
	for _, v := range subA.TopologicalOrder() {
		if src, ok := v.(*ops.SourceOp); ok {
			if src.Identifier() == "docker-image://docker.io/library/python:3.12" {
				t.Error("python:3.12 should not appear in Go subgraph")
			}
		}
	}
}

// ─── Graph query integration ──────────────────────────────────────────────────

func TestIntegrationAncestry(t *testing.T) {
	ctx := context.Background()
	root := buildGoApp(t)
	dag, _ := graph.BuildFromRoot(ctx, root)

	// The final exec (root) should have all other vertices as ancestors.
	for _, v := range dag.TopologicalOrder() {
		if v.ID() == root.ID() {
			continue
		}
		if !dag.IsAncestor(v, root) {
			t.Errorf("vertex %q (%s) should be an ancestor of root", v.ID(), v.Kind())
		}
	}
}

func TestIntegrationLeaves(t *testing.T) {
	ctx := context.Background()
	root := buildGoApp(t)
	dag, _ := graph.BuildFromRoot(ctx, root)

	leaves := dag.Leaves()
	// Sources are always leaves (no inputs).
	for _, l := range leaves {
		if l.Kind() != vertex.KindSource {
			t.Errorf("leaf %q has kind %s, but only sources should be leaves", l.ID(), l.Kind())
		}
	}
}

// ─── Concurrent traversal safety ─────────────────────────────────────────────

func TestIntegrationConcurrentTraversal(t *testing.T) {
	ctx := context.Background()
	root := buildGoApp(t)

	var wg sync.WaitGroup
	var errors []string
	var mu sync.Mutex
	var totalCount int64

	for i := 0; i < 30; i++ {
		wg.Add(1)
		go func(n int) {
			defer wg.Done()
			count, err := traverse.Count(ctx, root)
			if err != nil {
				mu.Lock()
				errors = append(errors, fmt.Sprintf("goroutine %d: %v", n, err))
				mu.Unlock()
				return
			}
			atomic.AddInt64(&totalCount, int64(count))
		}(i)
	}
	wg.Wait()

	if len(errors) > 0 {
		for _, e := range errors {
			t.Error(e)
		}
	}
}

// ─── BFS vs DFS produces same set ────────────────────────────────────────────

func TestIntegrationBFSvsDFSSameSet(t *testing.T) {
	ctx := context.Background()
	root := buildGoApp(t)

	collect := func(mode traverse.Mode) map[string]bool {
		ids := make(map[string]bool)
		eng := traverse.New(mode, traverse.Hooks{
			PreVisit: func(_ context.Context, _ traverse.Context, v vertex.Vertex) (traverse.Action, error) {
				ids[v.ID()] = true
				return traverse.Continue, nil
			},
		})
		_ = eng.TraverseUpstream(ctx, root)
		return ids
	}

	dfsIDs := collect(traverse.DFS)
	bfsIDs := collect(traverse.BFS)

	for id := range dfsIDs {
		if !bfsIDs[id] {
			t.Errorf("DFS visited %q but BFS did not", id)
		}
	}
	for id := range bfsIDs {
		if !dfsIDs[id] {
			t.Errorf("BFS visited %q but DFS did not", id)
		}
	}
}

// ─── AllPaths integration ─────────────────────────────────────────────────────

func TestIntegrationAllPathsToLeaf(t *testing.T) {
	ctx := context.Background()
	root := buildGoApp(t)

	leaves, err := traverse.FindByKind(ctx, root, vertex.KindSource)
	if err != nil || len(leaves) == 0 {
		t.Fatalf("no sources found: %v", err)
	}

	for _, leaf := range leaves {
		paths, err := traverse.AllPaths(ctx, root, leaf)
		if err != nil {
			t.Fatalf("AllPaths to %q: %v", leaf.ID(), err)
		}
		if len(paths) == 0 {
			t.Errorf("source %q should be reachable from root", leaf.ID())
		}
		for _, p := range paths {
			if p[0].ID() != root.ID() {
				t.Error("path must start at root")
			}
			if p[len(p)-1].ID() != leaf.ID() {
				t.Error("path must end at target leaf")
			}
		}
	}
}

// ─── JSON completeness ────────────────────────────────────────────────────────

func TestIntegrationJSONFieldPresence(t *testing.T) {
	ctx := context.Background()
	root := buildGoApp(t)
	dag, _ := graph.BuildFromRoot(ctx, root)

	m := marshal.New()
	data, _ := m.MarshalToJSON(ctx, dag, root)

	var raw map[string]any
	if err := json.Unmarshal(data, &raw); err != nil {
		t.Fatalf("parse json: %v", err)
	}

	required := []string{"version", "ops", "digest", "created_at"}
	for _, field := range required {
		if _, ok := raw[field]; !ok {
			t.Errorf("JSON missing required field %q", field)
		}
	}

	opsList, ok := raw["ops"].([]any)
	if !ok || len(opsList) == 0 {
		t.Fatal("ops field must be a non-empty array")
	}

	for i, opRaw := range opsList {
		op, ok := opRaw.(map[string]any)
		if !ok {
			t.Fatalf("op[%d] is not a map", i)
		}
		for _, reqField := range []string{"id", "kind", "op"} {
			if _, ok := op[reqField]; !ok {
				t.Errorf("op[%d] missing field %q", i, reqField)
			}
		}
	}
}

// ─── Helper ───────────────────────────────────────────────────────────────────

// sv is shared with engine_test.go in the same test package — define here for
// the dag_test package which is a separate compilation unit.
type svInteg struct {
	id     string
	kind   vertex.Kind
	inputs []vertex.Vertex
}

func sv(id string, inputs ...vertex.Vertex) *svInteg {
	return &svInteg{id: id, kind: vertex.KindSource, inputs: inputs}
}

func (s *svInteg) ID() string                       { return s.id }
func (s *svInteg) Kind() vertex.Kind                { return s.kind }
func (s *svInteg) Inputs() []vertex.Vertex          { return s.inputs }
func (s *svInteg) Validate(_ context.Context) error { return nil }
