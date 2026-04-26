package llb_test

import (
	"context"
	"sync"
	"sync/atomic"
	"testing"

	"github.com/bons/bons-ci/client/llb/builder"
	"github.com/bons/bons-ci/client/llb/core"
	"github.com/bons/bons-ci/client/llb/graph"
	"github.com/bons/bons-ci/client/llb/ops/conditional"
	"github.com/bons/bons-ci/client/llb/ops/diff"
	"github.com/bons/bons-ci/client/llb/ops/dyn"
	execop "github.com/bons/bons-ci/client/llb/ops/exec"
	"github.com/bons/bons-ci/client/llb/ops/export"
	fileop "github.com/bons/bons-ci/client/llb/ops/file"
	"github.com/bons/bons-ci/client/llb/ops/gate"
	"github.com/bons/bons-ci/client/llb/ops/matrix"
	"github.com/bons/bons-ci/client/llb/ops/merge"
	selectorop "github.com/bons/bons-ci/client/llb/ops/selector"
	"github.com/bons/bons-ci/client/llb/ops/solve"
	"github.com/bons/bons-ci/client/llb/ops/source/git"
	"github.com/bons/bons-ci/client/llb/ops/source/http"
	"github.com/bons/bons-ci/client/llb/ops/source/image"
	"github.com/bons/bons-ci/client/llb/ops/source/local"
	"github.com/bons/bons-ci/client/llb/reactive"
	"github.com/bons/bons-ci/client/llb/state"
	ocispecs "github.com/opencontainers/image-spec/specs-go/v1"
)

// ═══════════════════════════════════════════════════════════════════
// Source ops
// ═══════════════════════════════════════════════════════════════════

func TestImageVertex_Construction(t *testing.T) {
	t.Parallel()
	v, err := image.New(image.WithRef("alpine:3.20"))
	if err != nil {
		t.Fatalf("image.New: %v", err)
	}
	if v.Type() != core.VertexTypeSource {
		t.Errorf("Type = %v", v.Type())
	}
	if got := v.NormalisedRef(); got != "docker.io/library/alpine:3.20" {
		t.Errorf("NormalisedRef = %q", got)
	}
	if len(v.Inputs()) != 0 {
		t.Errorf("image vertex must have 0 inputs")
	}
	if len(v.Outputs()) != 1 {
		t.Errorf("image vertex must have 1 output")
	}
}

func TestImageVertex_RequiresRef(t *testing.T) {
	t.Parallel()
	_, err := image.New()
	if err == nil {
		t.Fatal("expected error for empty Ref")
	}
}

func TestImageVertex_Marshal_Determinism(t *testing.T) {
	t.Parallel()
	ctx := context.Background()
	v, _ := image.New(image.WithRef("busybox:latest"))
	c := core.DefaultConstraints()
	var prevDigest string
	for i := 0; i < 50; i++ {
		mv, err := v.Marshal(ctx, c)
		if err != nil {
			t.Fatalf("iter %d: Marshal: %v", i, err)
		}
		if prevDigest != "" && string(mv.Digest) != prevDigest {
			t.Fatalf("iter %d: non-deterministic digest", i)
		}
		prevDigest = string(mv.Digest)
	}
}

func TestImageVertex_WithOption_ChangesDigest(t *testing.T) {
	t.Parallel()
	ctx := context.Background()
	c := core.DefaultConstraints()
	v1, _ := image.New(image.WithRef("alpine:3.20"))
	v2, err := v1.WithOption(image.WithResolveMode(image.ResolveModePreferLocal))
	if err != nil {
		t.Fatalf("WithOption: %v", err)
	}
	mv1, _ := v1.Marshal(ctx, c)
	mv2, _ := v2.Marshal(ctx, c)
	if mv1.Digest == mv2.Digest {
		t.Error("WithOption produced the same digest – mutation not reflected")
	}
}

func TestGitVertex_Construction(t *testing.T) {
	t.Parallel()
	v, err := git.New(
		git.WithRemote("https://github.com/moby/buildkit.git"),
		git.WithRef(git.TagRef("v0.15.0")),
	)
	if err != nil {
		t.Fatalf("git.New: %v", err)
	}
	if v.Ref().String() != "v0.15.0" {
		t.Errorf("Ref = %q", v.Ref())
	}
}

func TestGitVertex_RefTypes(t *testing.T) {
	t.Parallel()
	cases := []struct {
		ref   git.Ref
		value string
	}{
		{git.BranchRef("main"), "main"},
		{git.TagRef("v1.0.0"), "v1.0.0"},
		{git.CommitRef("abc123"), "abc123"},
	}
	for _, tc := range cases {
		if tc.ref.String() != tc.value {
			t.Errorf("Ref.String() = %q, want %q", tc.ref.String(), tc.value)
		}
	}
}

func TestHTTPVertex(t *testing.T) {
	t.Parallel()
	ctx := context.Background()
	v, err := http.New(http.WithURL("https://example.com/archive.tar.gz"))
	if err != nil {
		t.Fatalf("http.New: %v", err)
	}
	mv, err := v.Marshal(ctx, core.DefaultConstraints())
	if err != nil {
		t.Fatalf("Marshal: %v", err)
	}
	if mv.Digest == "" {
		t.Error("empty digest")
	}
}

func TestLocalVertex(t *testing.T) {
	t.Parallel()
	ctx := context.Background()
	v, err := local.New(local.WithName("context"))
	if err != nil {
		t.Fatalf("local.New: %v", err)
	}
	mv, err := v.Marshal(ctx, core.DefaultConstraints())
	if err != nil {
		t.Fatalf("Marshal: %v", err)
	}
	if mv.Digest == "" {
		t.Error("empty digest")
	}
}

// ═══════════════════════════════════════════════════════════════════
// File op
// ═══════════════════════════════════════════════════════════════════

func TestFileOp_Mkdir(t *testing.T) {
	t.Parallel()
	ctx := context.Background()
	base, _ := image.New(image.WithRef("alpine:3.20"))
	fv, err := fileop.New(
		fileop.OnState(base.Output()),
		fileop.Do(fileop.Mkdir("/out", 0755, fileop.WithMkdirParents(true))),
	)
	if err != nil {
		t.Fatalf("file.New: %v", err)
	}
	mv, err := fv.Marshal(ctx, core.DefaultConstraints())
	if err != nil {
		t.Fatalf("Marshal: %v", err)
	}
	if mv.Digest == "" {
		t.Error("empty digest")
	}
}

func TestFileOp_MkdirMkfile(t *testing.T) {
	t.Parallel()
	ctx := context.Background()
	fv, err := fileop.New(fileop.Do(
		fileop.Mkdir("/out", 0755),
		fileop.Mkfile("/out/hello.txt", 0644, []byte("hello from bons-ci\n")),
	))
	if err != nil {
		t.Fatalf("file.New: %v", err)
	}
	mv, err := fv.Marshal(ctx, core.DefaultConstraints())
	if err != nil {
		t.Fatalf("Marshal: %v", err)
	}
	if mv.Digest == "" {
		t.Error("empty digest")
	}
}

func TestFileOp_RequiresActions(t *testing.T) {
	t.Parallel()
	_, err := fileop.New()
	if err == nil {
		t.Fatal("expected error when no actions provided")
	}
}

// ═══════════════════════════════════════════════════════════════════
// Merge / Diff
// ═══════════════════════════════════════════════════════════════════

func TestMergeVertex_TwoImages(t *testing.T) {
	t.Parallel()
	ctx := context.Background()
	a, _ := image.New(image.WithRef("alpine:3.20"))
	b, _ := image.New(image.WithRef("busybox:latest"))
	mv, err := merge.New(merge.WithInputs(a.Output(), b.Output()))
	if err != nil {
		t.Fatalf("merge.New: %v", err)
	}
	result, err := mv.Marshal(ctx, core.DefaultConstraints())
	if err != nil {
		t.Fatalf("Marshal: %v", err)
	}
	if result.Digest == "" {
		t.Error("empty digest")
	}
}

func TestMergeVertex_RequiresTwo(t *testing.T) {
	t.Parallel()
	a, _ := image.New(image.WithRef("alpine:3.20"))
	_, err := merge.New(merge.WithInputs(a.Output()))
	if err == nil {
		t.Fatal("expected error for single input merge")
	}
}

func TestDiffVertex(t *testing.T) {
	t.Parallel()
	ctx := context.Background()
	a, _ := image.New(image.WithRef("alpine:3.20"))
	b, _ := image.New(image.WithRef("busybox:latest"))
	dv := diff.New(diff.WithLower(a.Output()), diff.WithUpper(b.Output()))
	result, err := dv.Marshal(ctx, core.DefaultConstraints())
	if err != nil {
		t.Fatalf("Marshal: %v", err)
	}
	if result.Digest == "" {
		t.Error("empty digest")
	}
}

// ═══════════════════════════════════════════════════════════════════
// Conditional vertex
// ═══════════════════════════════════════════════════════════════════

func TestConditional_PlatformOS(t *testing.T) {
	t.Parallel()
	ctx := context.Background()

	alpine, _ := image.New(image.WithRef("alpine:3.20"))
	windows, _ := image.New(image.WithRef("mcr.microsoft.com/windows/nanoserver:ltsc2022"))

	cv, err := conditional.New(
		conditional.WithPredicate(conditional.PlatformOS("windows")),
		conditional.WithThen(windows.Output()),
		conditional.WithElse(alpine.Output()),
	)
	if err != nil {
		t.Fatalf("conditional.New: %v", err)
	}

	// Linux → should pick alpine.
	cLinux := core.DefaultConstraints()
	cLinux.Platform = &ocispecs.Platform{OS: "linux", Architecture: "amd64"}
	mvLinux, err := cv.Marshal(ctx, cLinux)
	if err != nil {
		t.Fatalf("Marshal linux: %v", err)
	}

	// Windows → should pick windows image.
	cWin := core.DefaultConstraints()
	cWin.Platform = &ocispecs.Platform{OS: "windows", Architecture: "amd64"}
	mvWin, err := cv.Marshal(ctx, cWin)
	if err != nil {
		t.Fatalf("Marshal windows: %v", err)
	}

	if mvLinux.Digest == mvWin.Digest {
		t.Error("linux and windows constraints should produce different digests")
	}
}

func TestConditional_BuildArg(t *testing.T) {
	t.Parallel()
	ctx := context.Background()

	devImage, _ := image.New(image.WithRef("alpine:dev"))
	prodImage, _ := image.New(image.WithRef("alpine:prod"))

	cv, err := conditional.New(
		conditional.WithPredicate(conditional.BuildArgEquals("ENV", "dev")),
		conditional.WithThen(devImage.Output()),
		conditional.WithElse(prodImage.Output()),
	)
	if err != nil {
		t.Fatalf("conditional.New: %v", err)
	}

	cDev := core.DefaultConstraints()
	cDev.BuildArgs["ENV"] = "dev"
	mvDev, _ := cv.Marshal(ctx, cDev)

	cProd := core.DefaultConstraints()
	cProd.BuildArgs["ENV"] = "prod"
	mvProd, _ := cv.Marshal(ctx, cProd)

	if mvDev.Digest == mvProd.Digest {
		t.Error("dev and prod should produce different digests")
	}
}

func TestConditional_LogicalCombinators(t *testing.T) {
	t.Parallel()
	ctx := context.Background()

	a, _ := image.New(image.WithRef("alpine:3.20"))
	b, _ := image.New(image.WithRef("busybox:latest"))

	and := conditional.And(
		conditional.PlatformOS("linux"),
		conditional.PlatformArch("amd64"),
	)
	cv, _ := conditional.New(
		conditional.WithPredicate(and),
		conditional.WithThen(a.Output()),
		conditional.WithElse(b.Output()),
	)

	cMatch := core.DefaultConstraints()
	cMatch.Platform = &ocispecs.Platform{OS: "linux", Architecture: "amd64"}
	mvMatch, _ := cv.Marshal(ctx, cMatch)

	cMiss := core.DefaultConstraints()
	cMiss.Platform = &ocispecs.Platform{OS: "linux", Architecture: "arm64"}
	mvMiss, _ := cv.Marshal(ctx, cMiss)

	if mvMatch.Digest == mvMiss.Digest {
		t.Error("AND predicate: amd64 and arm64 should differ")
	}
}

// ═══════════════════════════════════════════════════════════════════
// Matrix expansion
// ═══════════════════════════════════════════════════════════════════

func TestMatrix_CartesianExpand(t *testing.T) {
	t.Parallel()
	ctx := context.Background()

	// Use an image vertex as template; each expansion injects a build arg.
	template, _ := image.New(image.WithRef("alpine:3.20"))
	c := core.DefaultConstraints()

	results, err := matrix.Expand(ctx, template, c,
		matrix.NewAxis("GO_VERSION", "1.21", "1.22", "1.23"),
		matrix.NewAxis("GOOS", "linux", "darwin"),
	)
	if err != nil {
		t.Fatalf("matrix.Expand: %v", err)
	}
	// 3 GO_VERSION × 2 GOOS = 6 configurations
	if len(results) != 6 {
		t.Errorf("expected 6 results, got %d", len(results))
	}

	// All digests should be the same because the template vertex (image) does
	// not inspect build args itself. That's the caller's responsibility via
	// conditional.Vertex or similar.
	// But the Params should all be distinct.
	seen := map[string]bool{}
	for _, r := range results {
		key := r.Params["GO_VERSION"] + "/" + r.Params["GOOS"]
		if seen[key] {
			t.Errorf("duplicate config: %s", key)
		}
		seen[key] = true
	}
}

func TestMatrix_ExplicitExpand(t *testing.T) {
	t.Parallel()
	ctx := context.Background()

	template, _ := image.New(image.WithRef("alpine:3.20"))
	c := core.DefaultConstraints()

	configs := []matrix.ParamMap{
		{"platform": "linux/amd64"},
		{"platform": "linux/arm64"},
	}
	results, err := matrix.ExplicitExpand(ctx, template, c, configs)
	if err != nil {
		t.Fatalf("ExplicitExpand: %v", err)
	}
	if len(results) != 2 {
		t.Errorf("expected 2, got %d", len(results))
	}
}

// ═══════════════════════════════════════════════════════════════════
// Gate vertex
// ═══════════════════════════════════════════════════════════════════

func TestGate_PassThrough(t *testing.T) {
	t.Parallel()
	ctx := context.Background()
	c := core.DefaultConstraints()

	alpine, _ := image.New(image.WithRef("alpine:3.20"))
	policy := gate.NewPolicyFunc("allow-all", func(_ context.Context, _ core.Vertex, _ *core.Constraints) error {
		return nil // always pass
	})
	gv, err := gate.New(gate.WithSubject(alpine.Output()), gate.WithPolicy(policy))
	if err != nil {
		t.Fatalf("gate.New: %v", err)
	}
	mv, err := gv.Marshal(ctx, c)
	if err != nil {
		t.Fatalf("Marshal: %v", err)
	}
	// Gate should be transparent: digest matches alpine.
	alpineMV, _ := alpine.Marshal(ctx, c)
	if mv.Digest != alpineMV.Digest {
		t.Error("gate with passing policy should return subject's digest unchanged")
	}
}

func TestGate_RejectWithError(t *testing.T) {
	t.Parallel()
	ctx := context.Background()
	c := core.DefaultConstraints()

	alpine, _ := image.New(image.WithRef("alpine:3.20"))
	policy := gate.NewPolicyFunc("deny-all", func(_ context.Context, _ core.Vertex, _ *core.Constraints) error {
		return core.ErrPolicyRejected
	})
	gv, err := gate.New(gate.WithSubject(alpine.Output()), gate.WithPolicy(policy), gate.WithFailOnReject(true))
	if err != nil {
		t.Fatalf("gate.New: %v", err)
	}
	_, err = gv.Marshal(ctx, c)
	if err == nil {
		t.Fatal("expected error from rejecting gate")
	}
}

func TestGate_RejectWithFallback(t *testing.T) {
	t.Parallel()
	ctx := context.Background()
	c := core.DefaultConstraints()

	alpine, _ := image.New(image.WithRef("alpine:3.20"))
	busybox, _ := image.New(image.WithRef("busybox:latest"))

	policy := gate.NewPolicyFunc("deny-all", func(_ context.Context, _ core.Vertex, _ *core.Constraints) error {
		return core.ErrPolicyRejected
	})
	gv, err := gate.New(
		gate.WithSubject(alpine.Output()),
		gate.WithPolicy(policy),
		gate.WithFailOnReject(false),
		gate.WithFallback(busybox.Output()),
	)
	if err != nil {
		t.Fatalf("gate.New: %v", err)
	}
	mv, err := gv.Marshal(ctx, c)
	if err != nil {
		t.Fatalf("Marshal: %v", err)
	}
	busyboxMV, _ := busybox.Marshal(ctx, c)
	if mv.Digest != busyboxMV.Digest {
		t.Error("rejected gate with fallback should return fallback digest")
	}
}

func TestGate_EvaluateNow(t *testing.T) {
	t.Parallel()
	ctx := context.Background()
	called := false
	alpine, _ := image.New(image.WithRef("alpine:3.20"))
	policy := gate.NewPolicyFunc("spy", func(_ context.Context, _ core.Vertex, _ *core.Constraints) error {
		called = true
		return nil
	})
	gv, _ := gate.New(gate.WithSubject(alpine.Output()), gate.WithPolicy(policy))
	err := gv.EvaluateNow(ctx, core.DefaultConstraints())
	if err != nil {
		t.Fatalf("EvaluateNow: %v", err)
	}
	if !called {
		t.Error("EvaluateNow should have called the policy")
	}
}

// ═══════════════════════════════════════════════════════════════════
// Selector vertex
// ═══════════════════════════════════════════════════════════════════

func TestSelector_PlatformDispatch(t *testing.T) {
	t.Parallel()
	ctx := context.Background()

	linuxAmd64, _ := image.New(image.WithRef("alpine:amd64"))
	linuxArm64, _ := image.New(image.WithRef("alpine:arm64"))

	sel, err := selectorop.New(
		selectorop.WithCandidate(linuxAmd64.Output(), core.Labels{"os": "linux", "arch": "amd64"}),
		selectorop.WithCandidate(linuxArm64.Output(), core.Labels{"os": "linux", "arch": "arm64"}),
	)
	if err != nil {
		t.Fatalf("selector.New: %v", err)
	}

	cAmd := core.DefaultConstraints()
	cAmd.Platform = &ocispecs.Platform{OS: "linux", Architecture: "amd64"}
	mvAmd, err := sel.Marshal(ctx, cAmd)
	if err != nil {
		t.Fatalf("Marshal amd64: %v", err)
	}

	cArm := core.DefaultConstraints()
	cArm.Platform = &ocispecs.Platform{OS: "linux", Architecture: "arm64"}
	mvArm, err := sel.Marshal(ctx, cArm)
	if err != nil {
		t.Fatalf("Marshal arm64: %v", err)
	}

	if mvAmd.Digest == mvArm.Digest {
		t.Error("amd64 and arm64 selections should differ")
	}
}

func TestSelector_SelectNow(t *testing.T) {
	t.Parallel()
	linuxAmd64, _ := image.New(image.WithRef("linux-amd64"))
	darwin, _ := image.New(image.WithRef("darwin-amd64"))

	sel, _ := selectorop.New(
		selectorop.WithCandidate(linuxAmd64.Output(), core.Labels{"os": "linux"}),
		selectorop.WithCandidate(darwin.Output(), core.Labels{"os": "darwin"}),
	)

	c := core.DefaultConstraints()
	c.Platform = &ocispecs.Platform{OS: "darwin", Architecture: "amd64"}

	winner, err := sel.SelectNow(c)
	if err != nil {
		t.Fatalf("SelectNow: %v", err)
	}
	if winner.Labels["os"] != "darwin" {
		t.Errorf("expected darwin, got %v", winner.Labels)
	}
}

// ═══════════════════════════════════════════════════════════════════
// DAG operations: merge, prune, traversal, mutations
// ═══════════════════════════════════════════════════════════════════

func TestDAG_New(t *testing.T) {
	t.Parallel()
	ctx := context.Background()
	c := core.DefaultConstraints()

	v, _ := image.New(image.WithRef("alpine:3.20"))
	d, err := graph.New(ctx, v, c)
	if err != nil {
		t.Fatalf("graph.New: %v", err)
	}
	if d.Size() == 0 {
		t.Error("DAG should have at least one vertex")
	}
	if len(d.Roots()) != 1 {
		t.Errorf("expected 1 root, got %d", len(d.Roots()))
	}
}

func TestDAG_MergeDAGs(t *testing.T) {
	t.Parallel()
	ctx := context.Background()
	c := core.DefaultConstraints()

	va, _ := image.New(image.WithRef("alpine:3.20"))
	vb, _ := image.New(image.WithRef("busybox:latest"))

	da, _ := graph.New(ctx, va, c)
	db, _ := graph.New(ctx, vb, c)

	merged, err := graph.MergeDAGs(ctx, c, da, db)
	if err != nil {
		t.Fatalf("MergeDAGs: %v", err)
	}
	if merged.Size() < 2 {
		t.Errorf("merged DAG should have ≥2 vertices, got %d", merged.Size())
	}
	if len(merged.Roots()) < 2 {
		t.Errorf("merged DAG should have ≥2 roots, got %d", len(merged.Roots()))
	}
}

func TestDAG_Replace_PropagatesDigest(t *testing.T) {
	t.Parallel()
	ctx := context.Background()
	c := core.DefaultConstraints()

	alpine, _ := image.New(image.WithRef("alpine:3.20"))
	d, _ := graph.New(ctx, alpine, c)
	oldRoot := d.Roots()[0]

	busybox, _ := image.New(image.WithRef("busybox:latest"))
	mut := graph.NewMutator(d)
	nd, err := mut.Replace(ctx, oldRoot, busybox, c)
	if err != nil {
		t.Fatalf("Replace: %v", err)
	}

	newRoot := nd.Roots()[0]
	if newRoot == oldRoot {
		t.Error("Replace should produce a new root digest")
	}
}

func TestDAG_Prune(t *testing.T) {
	t.Parallel()
	ctx := context.Background()
	c := core.DefaultConstraints()

	va, _ := image.New(image.WithRef("alpine:3.20"))
	vb, _ := image.New(image.WithRef("busybox:latest"))

	da, _ := graph.New(ctx, va, c)
	db, _ := graph.New(ctx, vb, c)
	merged, _ := graph.MergeDAGs(ctx, c, da, db)

	// Prune to only the alpine root.
	alpineRoot := da.Roots()[0]
	mut := graph.NewMutator(merged)
	pruned, removed, err := mut.Prune(ctx, c, alpineRoot)
	if err != nil {
		t.Fatalf("Prune: %v", err)
	}
	if removed == 0 {
		t.Error("expected at least one vertex removed")
	}
	if pruned.Size() >= merged.Size() {
		t.Error("pruned DAG should be smaller than merged")
	}
}

func TestDAG_DiffDAGs(t *testing.T) {
	t.Parallel()
	ctx := context.Background()
	c := core.DefaultConstraints()

	va, _ := image.New(image.WithRef("alpine:3.20"))
	vb, _ := image.New(image.WithRef("busybox:latest"))

	da, _ := graph.New(ctx, va, c)
	db, _ := graph.New(ctx, vb, c)
	merged, _ := graph.MergeDAGs(ctx, c, da, db)

	diff := graph.DiffDAGs(ctx, da, merged)
	if len(diff.Added) == 0 {
		t.Error("expected at least one added vertex in diff")
	}
	if len(diff.Removed) != 0 {
		t.Error("should have no removed vertices")
	}
}

func TestDAG_Labels(t *testing.T) {
	t.Parallel()
	ctx := context.Background()
	c := core.DefaultConstraints()

	v, _ := image.New(image.WithRef("alpine:3.20"))
	d, _ := graph.New(ctx, v, c)
	rootID := d.Roots()[0]

	labels := core.Labels{"tier": "base", "os": "linux"}
	d2, err := d.SetLabels(rootID, labels)
	if err != nil {
		t.Fatalf("SetLabels: %v", err)
	}
	got := d2.Labels(rootID)
	if got["tier"] != "base" {
		t.Errorf("Labels = %v", got)
	}

	// Selector should find it.
	sel := graph.NewSelector(d2)
	ids := sel.ByLabel(core.Labels{"tier": "base"})
	if len(ids) != 1 {
		t.Errorf("ByLabel returned %d results, want 1", len(ids))
	}
}

func TestDAG_Traversal_TopologicalOrder(t *testing.T) {
	t.Parallel()
	ctx := context.Background()
	c := core.DefaultConstraints()

	v, _ := image.New(image.WithRef("alpine:3.20"))
	d, _ := graph.New(ctx, v, c)

	tr := graph.NewTraversal(d)
	order := tr.TopologicalOrder(d.Roots())
	if len(order) == 0 {
		t.Error("expected non-empty topological order")
	}
}

// ═══════════════════════════════════════════════════════════════════
// State API
// ═══════════════════════════════════════════════════════════════════

func TestState_ImageMarshal(t *testing.T) {
	t.Parallel()
	ctx := context.Background()
	b := builder.New()

	alpine := b.Image("alpine:3.20")
	if alpine.IsScratch() {
		t.Fatal("Image() returned scratch")
	}

	def, err := b.Serialize(ctx, alpine)
	if err != nil {
		t.Fatalf("Serialize: %v", err)
	}
	if len(def.Def) == 0 {
		t.Error("definition should contain at least one op")
	}
}

func TestState_Merge(t *testing.T) {
	t.Parallel()
	ctx := context.Background()
	b := builder.New()

	a := b.Image("alpine:3.20")
	bb := b.Image("busybox:latest")
	merged := a.Merge(bb)

	if merged.IsScratch() {
		t.Fatal("Merge returned scratch")
	}
	def, err := b.Serialize(ctx, merged)
	if err != nil {
		t.Fatalf("Serialize: %v", err)
	}
	if len(def.Def) == 0 {
		t.Error("merged definition is empty")
	}
}

func TestState_ScratchMerge(t *testing.T) {
	t.Parallel()
	// scratch + scratch → scratch
	merged := state.Scratch().Merge(state.Scratch())
	if !merged.IsScratch() {
		t.Error("scratch.Merge(scratch) should return scratch")
	}
	// non-scratch + scratch → non-scratch (returned as-is)
	b := builder.New()
	alpine := b.Image("alpine:3.20")
	result := alpine.Merge(state.Scratch())
	if result.IsScratch() {
		t.Error("non-scratch.Merge(scratch) should not be scratch")
	}
}

func TestState_Diff(t *testing.T) {
	t.Parallel()
	ctx := context.Background()
	b := builder.New()

	base := b.Image("alpine:3.20")
	upper := b.Image("busybox:latest")
	diffed := base.Diff(upper)

	def, err := b.Serialize(ctx, diffed)
	if err != nil {
		t.Fatalf("Serialize: %v", err)
	}
	if len(def.Def) == 0 {
		t.Error("diff definition is empty")
	}
}

func TestState_ScratchSerialize(t *testing.T) {
	t.Parallel()
	ctx := context.Background()
	b := builder.New()
	def, err := b.Serialize(ctx, b.Scratch())
	if err != nil {
		t.Fatalf("Serialize scratch: %v", err)
	}
	if len(def.Def) != 0 {
		t.Errorf("scratch should produce empty definition, got %d ops", len(def.Def))
	}
}

func TestBuilder_BuildDAG(t *testing.T) {
	t.Parallel()
	ctx := context.Background()
	b := builder.New()

	alpine := b.Image("alpine:3.20")
	d, err := b.BuildDAG(ctx, alpine)
	if err != nil {
		t.Fatalf("BuildDAG: %v", err)
	}
	if d.Size() == 0 {
		t.Error("DAG should not be empty")
	}
}

func TestBuilder_WithBuildArg(t *testing.T) {
	t.Parallel()
	b := builder.New()
	b2 := b.WithBuildArg("ENV", "dev")
	val, ok := b2.Constraints().BuildArg("ENV")
	if !ok || val != "dev" {
		t.Errorf("BuildArg ENV = %q %v, want dev true", val, ok)
	}
}

// ═══════════════════════════════════════════════════════════════════
// Exec op
// ═══════════════════════════════════════════════════════════════════

func TestExecOp_Basic(t *testing.T) {
	t.Parallel()
	ctx := context.Background()
	c := core.DefaultConstraints()

	alpine, _ := image.New(image.WithRef("alpine:3.20"))
	ev, err := execop.New(
		execop.WithRootMount(alpine.Output(), true),
		execop.WithCommand("echo", "hello"),
		execop.WithWorkingDir("/"),
	)
	if err != nil {
		t.Fatalf("exec.New: %v", err)
	}
	mv, err := ev.Marshal(ctx, c)
	if err != nil {
		t.Fatalf("Marshal: %v", err)
	}
	if mv.Digest == "" {
		t.Error("empty digest")
	}
}

func TestExecOp_CacheMount(t *testing.T) {
	t.Parallel()
	ctx := context.Background()
	c := core.DefaultConstraints()

	alpine, _ := image.New(image.WithRef("alpine:3.20"))
	ev, err := execop.New(
		execop.WithRootMount(alpine.Output(), true),
		execop.WithCommand("go", "build", "./..."),
		execop.WithWorkingDir("/src"),
		execop.WithMount(execop.Mount{
			Target:       "/root/go/pkg",
			Type:         execop.MountTypeCache,
			CacheID:      "go-pkg-cache",
			CacheSharing: execop.CacheSharingShared,
		}),
	)
	if err != nil {
		t.Fatalf("exec.New: %v", err)
	}
	mv, err := ev.Marshal(ctx, c)
	if err != nil {
		t.Fatalf("Marshal: %v", err)
	}
	if mv.Digest == "" {
		t.Error("empty digest")
	}
}

// ═══════════════════════════════════════════════════════════════════
// Reactive event bus
// ═══════════════════════════════════════════════════════════════════

func TestEventBus_PublishSubscribe(t *testing.T) {
	t.Parallel()
	bus := reactive.NewEventBus[string]()
	defer bus.Close()

	var mu sync.Mutex
	var received []string

	sub := bus.Subscribe(func(s string) {
		mu.Lock()
		received = append(received, s)
		mu.Unlock()
	})
	defer sub.Cancel()

	bus.Publish("hello")
	bus.Publish("world")

	// Give goroutine time to drain (100 iterations is fast enough in tests).
	for i := 0; i < 200; i++ {
		mu.Lock()
		n := len(received)
		mu.Unlock()
		if n == 2 {
			break
		}
	}
	mu.Lock()
	defer mu.Unlock()
	if len(received) != 2 {
		t.Errorf("received %d events, want 2", len(received))
	}
}

func TestEventBus_Cancel(t *testing.T) {
	t.Parallel()
	bus := reactive.NewEventBus[int]()
	defer bus.Close()

	var count atomic.Int64
	sub := bus.Subscribe(func(_ int) { count.Add(1) })

	bus.Publish(1)
	sub.Cancel()
	bus.Publish(2) // should not reach the subscriber

	for i := 0; i < 200; i++ {
		if count.Load() >= 1 {
			break
		}
	}
	if count.Load() > 1 {
		t.Errorf("received %d events after Cancel, want ≤1", count.Load())
	}
}

func TestObservable_SetNotifies(t *testing.T) {
	t.Parallel()
	obs := reactive.NewObservable("initial")
	defer obs.Close()

	changes := make(chan reactive.ChangeEvent[string], 4)
	sub := obs.Subscribe(func(e reactive.ChangeEvent[string]) { changes <- e })
	defer sub.Cancel()

	obs.Set("updated")

	select {
	case e := <-changes:
		if e.Old != "initial" || e.New != "updated" {
			t.Errorf("ChangeEvent = {%q, %q}", e.Old, e.New)
		}
	default:
		t.Error("no change event received")
	}
}

func TestObservable_NoEventOnSameValue(t *testing.T) {
	t.Parallel()
	obs := reactive.NewObservable(42)
	defer obs.Close()
	var count atomic.Int64
	sub := obs.Subscribe(func(_ reactive.ChangeEvent[int]) { count.Add(1) })
	defer sub.Cancel()
	obs.Set(42) // no-op
	if count.Load() != 0 {
		t.Errorf("got %d events for no-op Set", count.Load())
	}
}

func TestBuilder_EmitsEvents(t *testing.T) {
	t.Parallel()
	b := builder.New()
	var count atomic.Int64
	sub := b.Subscribe(func(_ reactive.GraphEvent) { count.Add(1) })
	defer sub.Cancel()

	_ = b.Image("alpine:3.20")
	_ = b.Local("ctx")

	for i := 0; i < 100; i++ {
		if count.Load() >= 2 {
			break
		}
	}
	if count.Load() < 2 {
		t.Errorf("expected ≥2 events, got %d", count.Load())
	}
}

// ═══════════════════════════════════════════════════════════════════
// SolveOp
// ═══════════════════════════════════════════════════════════════════

func TestSolveOp_WrapSubGraph(t *testing.T) {
	t.Parallel()
	ctx := context.Background()
	c := core.DefaultConstraints()

	alpine, _ := image.New(image.WithRef("alpine:3.20"))
	sv, err := solve.New(solve.WithInput(alpine.Output()), solve.WithEntryPoint("build"))
	if err != nil {
		t.Fatalf("solve.New: %v", err)
	}
	if sv.Type() != core.VertexTypeSolve {
		t.Errorf("Type = %v, want solve", sv.Type())
	}
	if len(sv.Inputs()) != 1 {
		t.Errorf("expected 1 input, got %d", len(sv.Inputs()))
	}
	if len(sv.Outputs()) != 1 {
		t.Errorf("expected 1 output, got %d", len(sv.Outputs()))
	}
	mv, err := sv.Marshal(ctx, c)
	if err != nil {
		t.Fatalf("Marshal: %v", err)
	}
	if mv.Digest == "" {
		t.Error("empty digest")
	}
}

func TestSolveOp_RequiresInput(t *testing.T) {
	t.Parallel()
	_, err := solve.New()
	if err == nil {
		t.Fatal("expected error for missing Input")
	}
}

func TestSolveOp_DeterministicDigest(t *testing.T) {
	t.Parallel()
	ctx := context.Background()
	c := core.DefaultConstraints()

	alpine, _ := image.New(image.WithRef("alpine:3.20"))
	sv, _ := solve.New(solve.WithInput(alpine.Output()))

	var prevDigest string
	for i := 0; i < 20; i++ {
		mv, err := sv.Marshal(ctx, c)
		if err != nil {
			t.Fatalf("iter %d: Marshal: %v", i, err)
		}
		if prevDigest != "" && string(mv.Digest) != prevDigest {
			t.Fatalf("iter %d: non-deterministic digest", i)
		}
		prevDigest = string(mv.Digest)
	}
}

func TestSolveOp_StateFluent(t *testing.T) {
	t.Parallel()
	ctx := context.Background()
	b := builder.New()

	alpine := b.Image("alpine:3.20")
	solved := alpine.Solve()

	if solved.IsScratch() {
		t.Fatal("Solve() returned scratch")
	}
	def, err := b.Serialize(ctx, solved)
	if err != nil {
		t.Fatalf("Serialize: %v", err)
	}
	if len(def.Def) == 0 {
		t.Error("solve definition is empty")
	}
}

// ═══════════════════════════════════════════════════════════════════
// ExportOp
// ═══════════════════════════════════════════════════════════════════

func TestExportOp_OCIImage(t *testing.T) {
	t.Parallel()
	ctx := context.Background()
	c := core.DefaultConstraints()

	alpine, _ := image.New(image.WithRef("alpine:3.20"))
	ev, err := export.New(
		export.WithInput(alpine.Output()),
		export.WithFormat(export.FormatOCIImage),
		export.WithImageRef("registry.example.com/myapp:latest"),
		export.WithCompression(export.CompressionZstd),
	)
	if err != nil {
		t.Fatalf("export.New: %v", err)
	}
	if ev.Type() != core.VertexTypeExport {
		t.Errorf("Type = %v, want export", ev.Type())
	}
	mv, err := ev.Marshal(ctx, c)
	if err != nil {
		t.Fatalf("Marshal: %v", err)
	}
	if mv.Digest == "" {
		t.Error("empty digest")
	}
}

func TestExportOp_RegistryPush(t *testing.T) {
	t.Parallel()
	ctx := context.Background()
	c := core.DefaultConstraints()

	alpine, _ := image.New(image.WithRef("alpine:3.20"))
	ev, err := export.New(
		export.WithInput(alpine.Output()),
		export.WithFormat(export.FormatRegistryPush),
		export.WithImageRef("ghcr.io/bons/ci:v1.0"),
		export.WithPush(true),
		export.WithAnnotation("org.opencontainers.image.source", "https://github.com/bons/bons-ci"),
	)
	if err != nil {
		t.Fatalf("export.New: %v", err)
	}
	mv, err := ev.Marshal(ctx, c)
	if err != nil {
		t.Fatalf("Marshal: %v", err)
	}
	if mv.Digest == "" {
		t.Error("empty digest")
	}
}

func TestExportOp_RequiresInput(t *testing.T) {
	t.Parallel()
	_, err := export.New(export.WithFormat(export.FormatOCIImage))
	if err == nil {
		t.Fatal("expected error for missing Input")
	}
}

func TestExportOp_InvalidFormat(t *testing.T) {
	t.Parallel()
	alpine, _ := image.New(image.WithRef("alpine:3.20"))
	_, err := export.New(
		export.WithInput(alpine.Output()),
		export.WithFormat("bogus"),
	)
	if err == nil {
		t.Fatal("expected error for invalid format")
	}
}

func TestExportOp_DifferentFormats_DifferentDigests(t *testing.T) {
	t.Parallel()
	ctx := context.Background()
	c := core.DefaultConstraints()

	alpine, _ := image.New(image.WithRef("alpine:3.20"))
	ociExport, _ := export.New(
		export.WithInput(alpine.Output()),
		export.WithFormat(export.FormatOCIImage),
		export.WithImageRef("a:latest"),
	)
	regExport, _ := export.New(
		export.WithInput(alpine.Output()),
		export.WithFormat(export.FormatRegistryPush),
		export.WithImageRef("b:latest"),
	)

	mv1, _ := ociExport.Marshal(ctx, c)
	mv2, _ := regExport.Marshal(ctx, c)
	if mv1.Digest == mv2.Digest {
		t.Error("different formats should produce different digests")
	}
}

func TestExportOp_StateFluent(t *testing.T) {
	t.Parallel()
	ctx := context.Background()
	b := builder.New()

	alpine := b.Image("alpine:3.20")
	exported := alpine.Export(
		export.WithFormat(export.FormatOCIImage),
		export.WithImageRef("myapp:latest"),
	)
	if exported.IsScratch() {
		t.Fatal("Export() returned scratch")
	}
	def, err := b.Serialize(ctx, exported)
	if err != nil {
		t.Fatalf("Serialize: %v", err)
	}
	if len(def.Def) == 0 {
		t.Error("export definition is empty")
	}
}

// ═══════════════════════════════════════════════════════════════════
// DynOp
// ═══════════════════════════════════════════════════════════════════

func TestDynOp_Construction(t *testing.T) {
	t.Parallel()
	ctx := context.Background()
	c := core.DefaultConstraints()

	alpine, _ := image.New(image.WithRef("alpine:3.20"))
	dv, err := dyn.New(
		dyn.WithRootMount(alpine.Output()),
		dyn.WithCommand("sh", "-c", "cat /policies/check.rego > /output/policy.rego"),
		dyn.WithPolicyPath("/output/policy.rego"),
		dyn.WithPolicyFormat(dyn.PolicyFormatRego),
		dyn.WithWorkingDir("/"),
	)
	if err != nil {
		t.Fatalf("dyn.New: %v", err)
	}
	if dv.Type() != core.VertexTypeDyn {
		t.Errorf("Type = %v, want dyn", dv.Type())
	}
	// Should have 2 outputs: filesystem and policy.
	if len(dv.Outputs()) != 2 {
		t.Errorf("expected 2 outputs, got %d", len(dv.Outputs()))
	}
	mv, err := dv.Marshal(ctx, c)
	if err != nil {
		t.Fatalf("Marshal: %v", err)
	}
	if mv.Digest == "" {
		t.Error("empty digest")
	}
}

func TestDynOp_RequiresPolicyPath(t *testing.T) {
	t.Parallel()
	alpine, _ := image.New(image.WithRef("alpine:3.20"))
	_, err := dyn.New(
		dyn.WithRootMount(alpine.Output()),
		dyn.WithCommand("echo", "hello"),
	)
	if err == nil {
		t.Fatal("expected error for missing PolicyPath")
	}
}

func TestDynOp_PolicyOutput(t *testing.T) {
	t.Parallel()
	alpine, _ := image.New(image.WithRef("alpine:3.20"))
	dv, _ := dyn.New(
		dyn.WithRootMount(alpine.Output()),
		dyn.WithCommand("echo", "policy"),
		dyn.WithPolicyPath("/output/policy.rego"),
		dyn.WithWorkingDir("/"),
	)
	fsOut := dv.Output()
	policyOut := dv.PolicyOutput()

	if fsOut == nil {
		t.Error("Output() should not be nil")
	}
	if policyOut == nil {
		t.Error("PolicyOutput() should not be nil")
	}
}

// ═══════════════════════════════════════════════════════════════════
// Conditional Switch
// ═══════════════════════════════════════════════════════════════════

func TestConditional_Switch(t *testing.T) {
	t.Parallel()
	ctx := context.Background()

	linuxImg, _ := image.New(image.WithRef("alpine:linux"))
	windowsImg, _ := image.New(image.WithRef("mcr.microsoft.com/nanoserver:ltsc2022"))
	darwinImg, _ := image.New(image.WithRef("macos-base:latest"))
	fallbackImg, _ := image.New(image.WithRef("scratch-fallback:latest"))

	sw, err := conditional.NewSwitch(
		conditional.SwitchCase(conditional.PlatformOS("linux"), linuxImg.Output(), "linux"),
		conditional.SwitchCase(conditional.PlatformOS("windows"), windowsImg.Output(), "windows"),
		conditional.SwitchCase(conditional.PlatformOS("darwin"), darwinImg.Output(), "darwin"),
	).WithDefault(fallbackImg.Output()).Build()
	if err != nil {
		t.Fatalf("NewSwitch: %v", err)
	}

	// Linux → linux image
	cLinux := core.DefaultConstraints()
	cLinux.Platform = &ocispecs.Platform{OS: "linux", Architecture: "amd64"}
	mvLinux, err := sw.Marshal(ctx, cLinux)
	if err != nil {
		t.Fatalf("Marshal linux: %v", err)
	}

	// Windows → windows image
	cWin := core.DefaultConstraints()
	cWin.Platform = &ocispecs.Platform{OS: "windows", Architecture: "amd64"}
	mvWin, err := sw.Marshal(ctx, cWin)
	if err != nil {
		t.Fatalf("Marshal windows: %v", err)
	}

	// Darwin → darwin image
	cDarwin := core.DefaultConstraints()
	cDarwin.Platform = &ocispecs.Platform{OS: "darwin", Architecture: "arm64"}
	mvDarwin, err := sw.Marshal(ctx, cDarwin)
	if err != nil {
		t.Fatalf("Marshal darwin: %v", err)
	}

	// All three should be different.
	if mvLinux.Digest == mvWin.Digest {
		t.Error("linux and windows should differ")
	}
	if mvLinux.Digest == mvDarwin.Digest {
		t.Error("linux and darwin should differ")
	}
}

func TestConditional_Switch_Fallback(t *testing.T) {
	t.Parallel()
	ctx := context.Background()

	linuxImg, _ := image.New(image.WithRef("alpine:linux"))
	fallbackImg, _ := image.New(image.WithRef("fallback:latest"))

	sw, _ := conditional.NewSwitch(
		conditional.SwitchCase(conditional.PlatformOS("linux"), linuxImg.Output(), "linux"),
	).WithDefault(fallbackImg.Output()).Build()

	// FreeBSD → should fall back.
	cFreeBSD := core.DefaultConstraints()
	cFreeBSD.Platform = &ocispecs.Platform{OS: "freebsd", Architecture: "amd64"}
	mvFallback, err := sw.Marshal(ctx, cFreeBSD)
	if err != nil {
		t.Fatalf("Marshal freebsd: %v", err)
	}

	// Verify it matches the fallback image.
	fallbackMV, _ := fallbackImg.Marshal(ctx, cFreeBSD)
	if mvFallback.Digest != fallbackMV.Digest {
		t.Error("switch with no matching case should return fallback digest")
	}
}

func TestConditional_Switch_RequiresCases(t *testing.T) {
	t.Parallel()
	_, err := conditional.NewSwitch().Build()
	if err == nil {
		t.Fatal("expected error for empty switch")
	}
}

// ═══════════════════════════════════════════════════════════════════
// Matrix Vertex
// ═══════════════════════════════════════════════════════════════════

func TestMatrixVertex_Marshal(t *testing.T) {
	t.Parallel()
	ctx := context.Background()
	c := core.DefaultConstraints()

	template, _ := image.New(image.WithRef("alpine:3.20"))
	mv, err := matrix.NewVertex(
		matrix.VertexWithTemplate(template),
		matrix.VertexWithAxes(
			matrix.NewAxis("GO_VERSION", "1.21", "1.22"),
			matrix.NewAxis("GOOS", "linux", "darwin"),
		),
	)
	if err != nil {
		t.Fatalf("matrix.NewVertex: %v", err)
	}
	if mv.Type() != core.VertexTypeMatrix {
		t.Errorf("Type = %v, want matrix", mv.Type())
	}

	// Should produce 2×2 = 4 configs.
	configs := mv.ExpandedConfigs()
	if len(configs) != 4 {
		t.Errorf("expected 4 configs, got %d", len(configs))
	}

	result, err := mv.Marshal(ctx, c)
	if err != nil {
		t.Fatalf("Marshal: %v", err)
	}
	if result.Digest == "" {
		t.Error("empty digest")
	}
}

func TestMatrixVertex_RequiresTemplate(t *testing.T) {
	t.Parallel()
	_, err := matrix.NewVertex(
		matrix.VertexWithAxes(matrix.NewAxis("K", "V")),
	)
	if err == nil {
		t.Fatal("expected error for missing template")
	}
}

func TestMatrixVertex_RequiresAxes(t *testing.T) {
	t.Parallel()
	template, _ := image.New(image.WithRef("alpine:3.20"))
	_, err := matrix.NewVertex(matrix.VertexWithTemplate(template))
	if err == nil {
		t.Fatal("expected error for missing axes")
	}
}

func TestMatrixVertex_SingleExpansion(t *testing.T) {
	t.Parallel()
	ctx := context.Background()
	c := core.DefaultConstraints()

	template, _ := image.New(image.WithRef("alpine:3.20"))
	mv, _ := matrix.NewVertex(
		matrix.VertexWithTemplate(template),
		matrix.VertexWithAxes(matrix.NewAxis("K", "V")),
	)
	result, err := mv.Marshal(ctx, c)
	if err != nil {
		t.Fatalf("Marshal: %v", err)
	}
	if result.Digest == "" {
		t.Error("empty digest")
	}
}
