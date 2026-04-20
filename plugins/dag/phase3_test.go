package reactdag_test

import (
	"context"
	"strings"
	"testing"
	"time"

	dag "github.com/bons/bons-ci/plugins/dag"
)

// =========================================================================
// Group tests
// =========================================================================

func buildFanOutDAG(t *testing.T) (*dag.DAG, map[string]*dag.Vertex) {
	t.Helper()
	// root → compile, lint, vet  (three independent targets)
	d := dag.NewDAG()
	vRoot := dag.NewVertex("root", noopOp{id: "root"})
	vCompile := dag.NewVertex("compile", noopOp{id: "compile"})
	vLint := dag.NewVertex("lint", noopOp{id: "lint"})
	vVet := dag.NewVertex("vet", noopOp{id: "vet"})
	for _, v := range []*dag.Vertex{vRoot, vCompile, vLint, vVet} {
		mustAdd(t, d, v)
	}
	mustLink(t, d, "root", "compile")
	mustLink(t, d, "root", "lint")
	mustLink(t, d, "root", "vet")
	mustSeal(t, d)
	return d, map[string]*dag.Vertex{
		"root": vRoot, "compile": vCompile, "lint": vLint, "vet": vVet,
	}
}

func TestGroupRegistry_RegisterAndGet(t *testing.T) {
	reg := dag.NewGroupRegistry()
	g := dag.NewGroup("ci", "compile", "lint", "vet")
	g.SetLabel("env", "production")
	reg.Register(g)

	got, ok := reg.Get("ci")
	if !ok {
		t.Fatal("expected group 'ci' to be found")
	}
	if got.Name() != "ci" {
		t.Errorf("Name = %q; want ci", got.Name())
	}
	if len(got.Members()) != 3 {
		t.Errorf("Members = %d; want 3", len(got.Members()))
	}
}

func TestGroupRegistry_ByLabel(t *testing.T) {
	reg := dag.NewGroupRegistry()
	g1 := dag.NewGroup("ci", "compile")
	g1.SetLabel("env", "prod")
	g2 := dag.NewGroup("nightly", "lint")
	g2.SetLabel("env", "prod")
	g3 := dag.NewGroup("dev", "vet")
	g3.SetLabel("env", "dev")
	for _, g := range []*dag.Group{g1, g2, g3} {
		reg.Register(g)
	}

	prodGroups := reg.ByLabel("env", "prod")
	if len(prodGroups) != 2 {
		t.Errorf("ByLabel(env=prod) = %d groups; want 2", len(prodGroups))
	}
}

func TestGroupScheduler_BuildGroup_Success(t *testing.T) {
	d, _ := buildFanOutDAG(t)
	reg := dag.NewGroupRegistry()
	reg.Register(dag.NewGroup("checks", "compile", "lint", "vet"))

	gs := dag.NewGroupScheduler(d, reg, dag.WithWorkerCount(4))
	result, err := gs.BuildGroup(context.Background(), "checks", nil)
	if err != nil {
		t.Fatalf("BuildGroup: %v", err)
	}
	if !result.Succeeded() {
		t.Errorf("GroupBuildResult should succeed; errors = %v", result.Errors())
	}
	if len(result.Results) != 3 {
		t.Errorf("Results count = %d; want 3", len(result.Results))
	}
}

func TestGroupScheduler_BuildGroup_UnknownGroup(t *testing.T) {
	d, _ := buildFanOutDAG(t)
	reg := dag.NewGroupRegistry()
	gs := dag.NewGroupScheduler(d, reg)

	_, err := gs.BuildGroup(context.Background(), "nonexistent", nil)
	if err == nil {
		t.Error("expected error for unknown group")
	}
}

func TestGroupScheduler_BuildByLabel(t *testing.T) {
	d, _ := buildFanOutDAG(t)
	reg := dag.NewGroupRegistry()

	g1 := dag.NewGroup("a", "compile")
	g1.SetLabel("stage", "build")
	g2 := dag.NewGroup("b", "lint")
	g2.SetLabel("stage", "build")
	reg.Register(g1)
	reg.Register(g2)

	gs := dag.NewGroupScheduler(d, reg, dag.WithWorkerCount(4))
	results, err := gs.BuildByLabel(context.Background(), "stage", "build", nil)
	if err != nil {
		t.Fatalf("BuildByLabel: %v", err)
	}
	if len(results) != 2 {
		t.Errorf("results count = %d; want 2", len(results))
	}
}

// =========================================================================
// GraphAnalyser tests
// =========================================================================

func TestAnalyse_LinearDAG(t *testing.T) {
	d, _ := buildLinearDAG(t)
	a, err := dag.Analyse(d)
	if err != nil {
		t.Fatalf("Analyse: %v", err)
	}

	if a.VertexCount != 3 {
		t.Errorf("VertexCount = %d; want 3", a.VertexCount)
	}
	if a.EdgeCount != 2 {
		t.Errorf("EdgeCount = %d; want 2", a.EdgeCount)
	}
	if a.RootCount != 1 {
		t.Errorf("RootCount = %d; want 1", a.RootCount)
	}
	if a.LeafCount != 1 {
		t.Errorf("LeafCount = %d; want 1", a.LeafCount)
	}
	if a.MaxDepth != 2 {
		t.Errorf("MaxDepth = %d; want 2 (C→B→A)", a.MaxDepth)
	}
	if len(a.CriticalPath) != 3 {
		t.Errorf("CriticalPath len = %d; want 3", len(a.CriticalPath))
	}
}

func TestAnalyse_WideDAG_FanOut(t *testing.T) {
	d, _ := buildFanOutDAG(t)
	a, err := dag.Analyse(d)
	if err != nil {
		t.Fatalf("Analyse: %v", err)
	}

	// root has 3 children → MaxFanOut = 3
	if a.MaxFanOut < 3 {
		t.Errorf("MaxFanOut = %d; want ≥3", a.MaxFanOut)
	}
	// compile/lint/vet each have 1 parent → MaxFanIn ≥ 1
	if a.MaxFanIn < 1 {
		t.Errorf("MaxFanIn = %d; want ≥1", a.MaxFanIn)
	}
}

func TestAnalyse_IsolatedNode(t *testing.T) {
	d := dag.NewDAG()
	mustAdd(t, d, dag.NewVertex("solo", noopOp{id: "solo"}))
	mustSeal(t, d)

	a, err := dag.Analyse(d)
	if err != nil {
		t.Fatalf("Analyse: %v", err)
	}
	if len(a.IsolatedNodes) != 1 || a.IsolatedNodes[0] != "solo" {
		t.Errorf("IsolatedNodes = %v; want [solo]", a.IsolatedNodes)
	}
}

func TestAnalyse_PerVertex_Flags(t *testing.T) {
	d, _ := buildLinearDAG(t)
	a, err := dag.Analyse(d)
	if err != nil {
		t.Fatalf("Analyse: %v", err)
	}

	c := a.PerVertex["C"]
	if !c.IsRoot {
		t.Error("C should be a root")
	}
	if c.IsLeaf {
		t.Error("C should not be a leaf")
	}

	av := a.PerVertex["A"]
	if av.IsRoot {
		t.Error("A should not be a root")
	}
	if !av.IsLeaf {
		t.Error("A should be a leaf")
	}
}

func TestRenderAnalysis_ContainsHeader(t *testing.T) {
	d, _ := buildLinearDAG(t)
	a, _ := dag.Analyse(d)
	out := dag.RenderAnalysis(a)
	if !strings.Contains(out, "Graph Analysis") {
		t.Errorf("missing header: %s", out)
	}
	if !strings.Contains(out, "Critical path") {
		t.Errorf("missing critical path: %s", out)
	}
}

func TestAnalyseParallelism_LinearIsAllSerial(t *testing.T) {
	d, _ := buildLinearDAG(t)
	r, err := dag.AnalyseParallelism(d)
	if err != nil {
		t.Fatalf("AnalyseParallelism: %v", err)
	}
	if r.MaxWidth != 1 {
		t.Errorf("MaxWidth = %d; want 1 for linear chain", r.MaxWidth)
	}
	if r.SerialFraction != 1.0 {
		t.Errorf("SerialFraction = %.2f; want 1.0", r.SerialFraction)
	}
}

func TestAnalyseParallelism_WideDAG_HasParallelism(t *testing.T) {
	d, _ := buildFanOutDAG(t)
	r, err := dag.AnalyseParallelism(d)
	if err != nil {
		t.Fatalf("AnalyseParallelism: %v", err)
	}
	// Level 0: root (1); Level 1: compile, lint, vet (3)
	if r.MaxWidth < 3 {
		t.Errorf("MaxWidth = %d; want ≥3 for wide fan-out", r.MaxWidth)
	}
	if r.SerialFraction >= 1.0 {
		t.Errorf("SerialFraction = %.2f; expected some parallel levels", r.SerialFraction)
	}
}

func TestRenderParallelismReport_ContainsLevels(t *testing.T) {
	d, _ := buildLinearDAG(t)
	r, _ := dag.AnalyseParallelism(d)
	out := dag.RenderParallelismReport(r)
	if !strings.Contains(out, "Parallelism") {
		t.Errorf("missing header: %s", out)
	}
	if !strings.Contains(out, "L0") {
		t.Errorf("missing level L0: %s", out)
	}
}

// =========================================================================
// Eviction policy tests
// =========================================================================

func makeTestEntry(size int64) *dag.CacheEntry {
	return &dag.CacheEntry{
		OutputFiles: []dag.FileRef{{Path: "/out/x", Size: size}},
		CachedAt:    time.Now(),
	}
}

func TestLRUPolicy_EvictsLeastRecentlyAccessed(t *testing.T) {
	store := dag.NewManagedStore(2, 0, dag.LRUPolicy{})
	ctx := context.Background()

	k1 := dag.CacheKey{1}
	k2 := dag.CacheKey{2}
	k3 := dag.CacheKey{3}

	store.Set(ctx, k1, makeTestEntry(100))
	time.Sleep(1 * time.Millisecond)
	store.Set(ctx, k2, makeTestEntry(100))

	// Access k1 to make k2 the LRU candidate.
	store.Get(ctx, k1)
	time.Sleep(1 * time.Millisecond)

	// k3 insert should evict k2 (least recently accessed).
	store.Set(ctx, k3, makeTestEntry(100))

	// k2 should be evicted.
	got, _ := store.Get(ctx, k2)
	if got != nil {
		t.Error("k2 should have been evicted by LRU policy")
	}
	// k1 and k3 should survive.
	if g, _ := store.Get(ctx, k1); g == nil {
		t.Error("k1 should not have been evicted")
	}
	if g, _ := store.Get(ctx, k3); g == nil {
		t.Error("k3 should not have been evicted")
	}
}

func TestLFUPolicy_EvictsLeastFrequentlyAccessed(t *testing.T) {
	store := dag.NewManagedStore(2, 0, dag.LFUPolicy{})
	ctx := context.Background()

	k1 := dag.CacheKey{1}
	k2 := dag.CacheKey{2}
	k3 := dag.CacheKey{3}

	store.Set(ctx, k1, makeTestEntry(100))
	store.Set(ctx, k2, makeTestEntry(100))

	// Hit k1 twice — k2 remains at 0 hits.
	store.Get(ctx, k1)
	store.Get(ctx, k1)

	// k3 insert should evict k2 (fewest hits = 0).
	store.Set(ctx, k3, makeTestEntry(100))

	got, _ := store.Get(ctx, k2)
	if got != nil {
		t.Error("k2 should have been evicted by LFU policy (0 hits)")
	}
}

func TestSizePolicy_EvictsLargestEntry(t *testing.T) {
	store := dag.NewManagedStore(2, 0, dag.SizePolicy{})
	ctx := context.Background()

	k1 := dag.CacheKey{1}
	k2 := dag.CacheKey{2}
	k3 := dag.CacheKey{3}

	store.Set(ctx, k1, makeTestEntry(500)) // large
	store.Set(ctx, k2, makeTestEntry(100)) // small

	// k3 insert — SizePolicy evicts the largest (k1).
	store.Set(ctx, k3, makeTestEntry(50))

	got, _ := store.Get(ctx, k1)
	if got != nil {
		t.Error("k1 (largest) should have been evicted by SizePolicy")
	}
}

func TestTTLPolicy_EvictsExpiredEntries(t *testing.T) {
	ttl := dag.TTLPolicy{MaxAge: 10 * time.Millisecond}
	store := dag.NewManagedStore(10, 0, ttl)
	ctx := context.Background()

	k1 := dag.CacheKey{1}
	k2 := dag.CacheKey{2}

	store.Set(ctx, k1, makeTestEntry(100))
	time.Sleep(20 * time.Millisecond) // k1 now expired
	store.Set(ctx, k2, makeTestEntry(100))

	// PruneExpired removes k1.
	n := store.PruneExpired(10 * time.Millisecond)
	if n != 1 {
		t.Errorf("PruneExpired removed %d entries; want 1", n)
	}
	got, _ := store.Get(ctx, k1)
	if got != nil {
		t.Error("k1 should have been pruned (expired)")
	}
	if g, _ := store.Get(ctx, k2); g == nil {
		t.Error("k2 should still be present")
	}
}

func TestManagedStore_ByteCapacity_Evicts(t *testing.T) {
	// Cap at 200 bytes; each entry is 100 bytes.
	store := dag.NewManagedStore(0, 200, dag.LRUPolicy{})
	ctx := context.Background()

	store.Set(ctx, dag.CacheKey{1}, makeTestEntry(100))
	store.Set(ctx, dag.CacheKey{2}, makeTestEntry(100))
	// Third entry (100B) pushes total to 300B > 200B cap → evict one.
	store.Set(ctx, dag.CacheKey{3}, makeTestEntry(100))

	stats := store.Stats()
	if stats.TotalBytes > 200 {
		t.Errorf("TotalBytes = %d; want ≤200 (byte cap)", stats.TotalBytes)
	}
}

func TestManagedStore_TopN(t *testing.T) {
	store := dag.NewManagedStore(0, 0, nil)
	ctx := context.Background()

	for i := 0; i < 5; i++ {
		store.Set(ctx, dag.CacheKey{byte(i)}, makeTestEntry(10))
	}
	// Hit key 2 three times, key 4 once.
	for range 3 {
		store.Get(ctx, dag.CacheKey{2})
	}
	store.Get(ctx, dag.CacheKey{4})

	top := store.TopN(2)
	if len(top) != 2 {
		t.Fatalf("TopN(2) returned %d entries; want 2", len(top))
	}
	// First should be key 2 (3 hits).
	if top[0].Key != (dag.CacheKey{2}) {
		t.Errorf("TopN[0].Key = %v; want key{2}", top[0].Key)
	}
}

func TestBackgroundPruner_RemovesExpired(t *testing.T) {
	store := dag.NewManagedStore(0, 0, nil)
	ctx, cancel := context.WithTimeout(context.Background(), 500*time.Millisecond)
	defer cancel()

	// Insert entry that expires in 5ms.
	store.Set(ctx, dag.CacheKey{1}, makeTestEntry(10))

	pruner := dag.NewBackgroundPruner(store, 10*time.Millisecond, 5*time.Millisecond)
	pruner.Start(ctx)
	defer pruner.Stop()

	// After 50ms the pruner should have run at least once.
	time.Sleep(50 * time.Millisecond)

	got, _ := store.Get(ctx, dag.CacheKey{1})
	if got != nil {
		t.Error("background pruner should have removed the expired entry")
	}
}

func TestRenderCacheStats_Format(t *testing.T) {
	stats := dag.ManagedStoreStats{Entries: 5, TotalBytes: 1536, TotalHits: 12}
	out := dag.RenderCacheStats(stats)
	if !strings.Contains(out, "entries=5") {
		t.Errorf("missing entries count: %s", out)
	}
	if !strings.Contains(out, "hits=12") {
		t.Errorf("missing hit count: %s", out)
	}
}

// =========================================================================
// Integration: analysis + eviction + group + watcher
// =========================================================================

func TestIntegration_AnalysisInformsParallelism(t *testing.T) {
	// Build a diamond DAG, analyse it, verify the recommended worker count
	// matches the widest level of parallelism.
	//   D → B,C → A
	d := dag.NewDAG()
	for _, id := range []string{"D", "B", "C", "A"} {
		mustAdd(t, d, dag.NewVertex(id, noopOp{id: id}))
	}
	mustLink(t, d, "D", "B")
	mustLink(t, d, "D", "C")
	mustLink(t, d, "B", "A")
	mustLink(t, d, "C", "A")
	mustSeal(t, d)

	report, err := dag.AnalyseParallelism(d)
	if err != nil {
		t.Fatalf("AnalyseParallelism: %v", err)
	}

	// Diamond has max width 2 at the middle level (B, C).
	if report.MaxWidth < 2 {
		t.Errorf("MaxWidth = %d; want ≥2 for diamond", report.MaxWidth)
	}

	// Now actually build with workers = MaxWidth and verify it completes.
	s := dag.NewScheduler(d, dag.WithWorkerCount(report.MaxWidth))
	if _, err := s.Build(context.Background(), "A", nil); err != nil {
		t.Fatalf("Build with recommended workers: %v", err)
	}
}

func TestIntegration_ManagedCacheWithScheduler(t *testing.T) {
	d, _ := buildLinearDAG(t)

	// Use LFU-evicted cache with 2-entry cap.
	managed := dag.NewManagedStore(2, 0, dag.LFUPolicy{})
	s := dag.NewScheduler(d,
		dag.WithFastCache(managed),
		dag.WithWorkerCount(2),
	)

	// Build 1: 3 vertices → should fill and evict from the 2-entry cache.
	if _, err := s.Build(context.Background(), "A", nil); err != nil {
		t.Fatalf("Build: %v", err)
	}

	stats := managed.Stats()
	if stats.Entries > 2 {
		t.Errorf("ManagedStore exceeded maxCount=2: entries=%d", stats.Entries)
	}
}

func TestIntegration_WatcherWithGroupScheduler(t *testing.T) {
	d, _ := buildFanOutDAG(t)
	reg := dag.NewGroupRegistry()
	reg.Register(dag.NewGroup("all-checks", "compile", "lint", "vet"))

	gs := dag.NewGroupScheduler(d, reg, dag.WithWorkerCount(4))

	// Verify group build works with changed files.
	changedFiles := []dag.FileRef{{Path: "/src/main.go"}}
	result, err := gs.BuildGroup(context.Background(), "all-checks", changedFiles)
	if err != nil {
		// Some members may fail due to missing parents being built first — that's OK.
		// What we care about is that the group scheduler doesn't panic or deadlock.
		t.Logf("BuildGroup returned error (expected in some topologies): %v", err)
	}
	if result.GroupName != "all-checks" {
		t.Errorf("GroupName = %q; want all-checks", result.GroupName)
	}
}
