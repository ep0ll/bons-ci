package marshal_test

import (
	"context"
	"encoding/json"
	"testing"

	"github.com/bons/bons-ci/plugins/dag/graph"
	"github.com/bons/bons-ci/plugins/dag/marshal"
	"github.com/bons/bons-ci/plugins/dag/ops"
	"github.com/bons/bons-ci/plugins/dag/vertex"
)

// ─── Helpers ─────────────────────────────────────────────────────────────────

func buildAndMarshal(t *testing.T, root vertex.Vertex) (*marshal.Definition, []byte) {
	t.Helper()
	ctx := context.Background()

	dag, err := graph.BuildFromRoot(ctx, root)
	if err != nil {
		t.Fatalf("build: %v", err)
	}

	m := marshal.New()
	def, err := m.Marshal(ctx, dag, root)
	if err != nil {
		t.Fatalf("marshal: %v", err)
	}

	data, err := json.Marshal(def)
	if err != nil {
		t.Fatalf("json: %v", err)
	}
	return def, data
}

// ─── Basic serialization ─────────────────────────────────────────────────────

func TestMarshalSingleImage(t *testing.T) {
	img := ops.Image("alpine:3.18")
	def, data := buildAndMarshal(t, img)

	if def.Version != marshal.Version {
		t.Errorf("version: want %q, got %q", marshal.Version, def.Version)
	}
	if len(def.Ops) != 1 {
		t.Errorf("expected 1 op, got %d", len(def.Ops))
	}
	op := def.Ops[0]
	if op.Kind != vertex.KindSource {
		t.Errorf("kind: want %q, got %q", vertex.KindSource, op.Kind)
	}
	if op.Op.Source == nil {
		t.Fatal("expected Source payload")
	}
	if op.Op.Source.Identifier != "docker-image://docker.io/library/alpine:3.18" {
		t.Errorf("identifier: got %q", op.Op.Source.Identifier)
	}
	if len(data) == 0 {
		t.Error("serialized bytes must not be empty")
	}
}

func TestMarshalLinearChain(t *testing.T) {
	// Image → FileOp → ExecOp
	ctx := context.Background()
	img := ops.Image("alpine:3.18")
	fileOp := ops.NewFileOp(
		img.Ref(),
		ops.Mkdir("/app", 0755),
		ops.Constraints{},
	)
	execOp := ops.Exec(fileOp.Ref(),
		ops.WithArgs("sh", "-c", "echo hello"),
		ops.WithCwd("/app"),
	)

	dag, err := graph.BuildFromRoot(ctx, execOp)
	if err != nil {
		t.Fatal(err)
	}

	m := marshal.New()
	def, err := m.Marshal(ctx, dag, execOp)
	if err != nil {
		t.Fatal(err)
	}

	if len(def.Ops) != 3 {
		t.Errorf("expected 3 ops, got %d", len(def.Ops))
	}

	// Topological order: image first, then file, then exec.
	kinds := make([]vertex.Kind, len(def.Ops))
	for i, op := range def.Ops {
		kinds[i] = op.Kind
	}
	if kinds[0] != vertex.KindSource {
		t.Errorf("op[0]: want source, got %s", kinds[0])
	}
	if kinds[1] != vertex.KindFile {
		t.Errorf("op[1]: want file, got %s", kinds[1])
	}
	if kinds[2] != vertex.KindExec {
		t.Errorf("op[2]: want exec, got %s", kinds[2])
	}
}

func TestMarshalInputRefs(t *testing.T) {
	ctx := context.Background()
	img := ops.Image("alpine:3.18")
	execOp := ops.Exec(img.Ref(),
		ops.WithArgs("ls"),
		ops.WithCwd("/"),
	)

	dag, _ := graph.BuildFromRoot(ctx, execOp)
	m := marshal.New()
	def, err := m.Marshal(ctx, dag, execOp)
	if err != nil {
		t.Fatal(err)
	}

	// exec op at index 1; its inputs should reference op at index 0 (image).
	execSOP := def.Ops[1]
	if len(execSOP.Inputs) == 0 {
		t.Fatal("exec op must have inputs")
	}
	if execSOP.Inputs[0].OpIndex != 0 {
		t.Errorf("exec input must reference op[0] (image), got op[%d]", execSOP.Inputs[0].OpIndex)
	}
}

// ─── Determinism ─────────────────────────────────────────────────────────────

func TestMarshalDeterministic(t *testing.T) {
	// The same graph must always produce the same content digest.
	// (CreatedAt is a timestamp and is intentionally excluded from the content hash.)
	buildDigest := func() string {
		img := ops.Image("alpine:3.18")
		fileOp := ops.NewFileOp(
			img.Ref(),
			ops.Mkdir("/app", 0755, ops.WithParents(true)).
				Mkfile("/app/hello.txt", 0644, []byte("hello world")),
			ops.Constraints{},
		)
		execOp := ops.Exec(fileOp.Ref(),
			ops.WithArgs("cat", "/app/hello.txt"),
			ops.WithCwd("/app"),
		)
		def, _ := buildAndMarshal(t, execOp)
		return def.Digest
	}

	first := buildDigest()
	for i := 0; i < 20; i++ {
		got := buildDigest()
		if got != first {
			t.Errorf("run %d: content digest is not deterministic: %q vs %q", i+1, first, got)
		}
	}
}

func TestMarshalDigestDeterministic(t *testing.T) {
	// The content digest in the Definition must be stable.
	build := func() string {
		img := ops.Image("ubuntu:22.04")
		e := ops.Exec(img.Ref(), ops.WithArgs("apt-get", "update"), ops.WithCwd("/"))
		_, data := buildAndMarshal(t, e)
		var def marshal.Definition
		_ = json.Unmarshal(data, &def)
		return def.Digest
	}

	first := build()
	for i := 0; i < 10; i++ {
		if got := build(); got != first {
			t.Errorf("digest changed on run %d: %q vs %q", i+1, first, got)
		}
	}
}

// ─── Merge / Diff ─────────────────────────────────────────────────────────────

func TestMarshalMerge(t *testing.T) {
	ctx := context.Background()
	base := ops.Image("alpine:3.18")
	extra := ops.Image("busybox:latest")
	mergeRef := ops.Merge([]vertex.Ref{base.Ref(), extra.Ref()}, ops.Constraints{})

	mergeOp := mergeRef.Vertex // *ops.MergeOp
	dag, err := graph.BuildFromRoot(ctx, mergeOp)
	if err != nil {
		t.Fatal(err)
	}

	m := marshal.New()
	def, err := m.Marshal(ctx, dag, mergeOp)
	if err != nil {
		t.Fatal(err)
	}

	// Should have: alpine, busybox, merge (3 ops).
	if len(def.Ops) != 3 {
		t.Errorf("expected 3 ops, got %d", len(def.Ops))
	}

	var mergeFound bool
	for _, op := range def.Ops {
		if op.Kind == vertex.KindMerge {
			mergeFound = true
			if op.Op.Merge == nil {
				t.Error("merge op must have Merge payload")
			}
			if len(op.Op.Merge.Inputs) != 2 {
				t.Errorf("merge must have 2 inputs, got %d", len(op.Op.Merge.Inputs))
			}
		}
	}
	if !mergeFound {
		t.Error("no merge op found in definition")
	}
}

func TestMarshalDiff(t *testing.T) {
	ctx := context.Background()
	lower := ops.Image("alpine:3.18")
	upper := ops.Image("alpine:edge")
	diffRef := ops.Diff(lower.Ref(), upper.Ref(), ops.Constraints{})

	dag, err := graph.BuildFromRoot(ctx, diffRef.Vertex)
	if err != nil {
		t.Fatal(err)
	}

	m := marshal.New()
	def, err := m.Marshal(ctx, dag, diffRef.Vertex)
	if err != nil {
		t.Fatal(err)
	}

	var diffFound bool
	for _, op := range def.Ops {
		if op.Kind == vertex.KindDiff {
			diffFound = true
			if op.Op.Diff == nil {
				t.Error("diff op must have Diff payload")
			}
			if op.Op.Diff.Lower == nil || op.Op.Diff.Upper == nil {
				t.Error("diff op must reference both lower and upper")
			}
		}
	}
	if !diffFound {
		t.Error("no diff op found")
	}
}

// ─── Unmarshal round-trip ─────────────────────────────────────────────────────

func TestMarshalRoundTrip(t *testing.T) {
	img := ops.Image("alpine:3.18")
	_, data := buildAndMarshal(t, img)

	def, err := marshal.Unmarshal(data)
	if err != nil {
		t.Fatalf("unmarshal: %v", err)
	}
	if def.Version != marshal.Version {
		t.Errorf("round-trip version: want %q, got %q", marshal.Version, def.Version)
	}
	if len(def.Ops) != 1 {
		t.Errorf("round-trip: expected 1 op, got %d", len(def.Ops))
	}
}

func TestMarshalComplexRoundTrip(t *testing.T) {
	img := ops.Image("alpine:3.18")
	fileOp := ops.NewFileOp(
		img.Ref(),
		ops.Mkdir("/etc/myapp", 0755).
			Mkfile("/etc/myapp/config.toml", 0644, []byte(`[server]\nport = 8080`)),
		ops.Constraints{},
	)
	execOp := ops.Exec(fileOp.Ref(),
		ops.WithArgs("myapp", "--config", "/etc/myapp/config.toml"),
		ops.WithCwd("/"),
		ops.WithEnv("GOMAXPROCS=1"),
	)

	_, data := buildAndMarshal(t, execOp)

	def, err := marshal.Unmarshal(data)
	if err != nil {
		t.Fatalf("round-trip unmarshal: %v", err)
	}
	if len(def.Ops) != 3 {
		t.Errorf("round-trip: expected 3 ops, got %d", len(def.Ops))
	}
}

// ─── Diamond subgraph marshaling ──────────────────────────────────────────────

func TestMarshalDiamondDeduplication(t *testing.T) {
	// Both A and B depend on the same image C.
	// C should appear exactly once in the serialized form.
	ctx := context.Background()
	c := ops.Image("shared:latest")
	a := ops.Exec(c.Ref(), ops.WithArgs("cmd-a"), ops.WithCwd("/"))
	b := ops.Exec(c.Ref(), ops.WithArgs("cmd-b"), ops.WithCwd("/"))
	merge := ops.Merge([]vertex.Ref{a.RootRef(), b.RootRef()}, ops.Constraints{})

	dag, err := graph.BuildFromRoot(ctx, merge.Vertex)
	if err != nil {
		t.Fatal(err)
	}

	m := marshal.New()
	def, err := m.Marshal(ctx, dag, merge.Vertex)
	if err != nil {
		t.Fatal(err)
	}

	// Count source ops.
	sourceCount := 0
	for _, op := range def.Ops {
		if op.Kind == vertex.KindSource {
			sourceCount++
		}
	}
	if sourceCount != 1 {
		t.Errorf("shared image should appear once, got %d source ops", sourceCount)
	}

	// Total: 1 source + 2 exec + 1 merge = 4
	if len(def.Ops) != 4 {
		t.Errorf("expected 4 ops total, got %d", len(def.Ops))
	}
}

// ─── Subgraph marshaling ─────────────────────────────────────────────────────

func TestMarshalSubgraphFromRoot(t *testing.T) {
	ctx := context.Background()

	// Full graph: two independent pipelines.
	imgA := ops.Image("alpine:3.18")
	execA := ops.Exec(imgA.Ref(), ops.WithArgs("echo", "a"), ops.WithCwd("/"))

	imgB := ops.Image("ubuntu:22.04")
	execB := ops.Exec(imgB.Ref(), ops.WithArgs("echo", "b"), ops.WithCwd("/"))

	dag, err := graph.BuildFromRoots(ctx, execA, execB)
	if err != nil {
		t.Fatal(err)
	}

	// Marshal only the A pipeline.
	m := marshal.New()
	defA, err := m.Marshal(ctx, dag, execA)
	if err != nil {
		t.Fatal(err)
	}

	// Should contain only alpine + execA (2 ops), not ubuntu or execB.
	if len(defA.Ops) != 2 {
		t.Errorf("subgraph A: expected 2 ops, got %d", len(defA.Ops))
	}
	for _, op := range defA.Ops {
		if op.Op.Source != nil && op.Op.Source.Identifier == "docker-image://docker.io/library/ubuntu:22.04" {
			t.Error("ubuntu should not appear in subgraph A")
		}
	}
}

// ─── File action serialization ────────────────────────────────────────────────

func TestMarshalFileActions(t *testing.T) {
	ctx := context.Background()
	img := ops.Image("alpine:3.18")

	chain := ops.Mkdir("/app", 0755, ops.WithParents(true)).
		Mkfile("/app/main.go", 0644, []byte("package main")).
		Rm("/tmp/junk", ops.WithAllowNotFound()).
		Symlink("/app/main.go", "/usr/local/bin/main")

	fileOp := ops.NewFileOp(img.Ref(), chain, ops.Constraints{})
	dag, err := graph.BuildFromRoot(ctx, fileOp)
	if err != nil {
		t.Fatal(err)
	}

	m := marshal.New()
	def, err := m.Marshal(ctx, dag, fileOp)
	if err != nil {
		t.Fatal(err)
	}

	var fileSOP *marshal.SerializedOp
	for i := range def.Ops {
		if def.Ops[i].Kind == vertex.KindFile {
			fileSOP = &def.Ops[i]
			break
		}
	}
	if fileSOP == nil {
		t.Fatal("no file op in definition")
	}
	if fileSOP.Op.File == nil {
		t.Fatal("file op has no File payload")
	}
	if len(fileSOP.Op.File.Actions) != 4 {
		t.Errorf("expected 4 actions, got %d", len(fileSOP.Op.File.Actions))
	}

	wantKinds := []string{"mkdir", "mkfile", "rm", "symlink"}
	for i, a := range fileSOP.Op.File.Actions {
		if a.Kind != wantKinds[i] {
			t.Errorf("action[%d]: want %q, got %q", i, wantKinds[i], a.Kind)
		}
	}
}

// ─── Exec payload detail ─────────────────────────────────────────────────────

func TestMarshalExecPayload(t *testing.T) {
	ctx := context.Background()
	img := ops.Image("alpine:3.18")
	e := ops.Exec(img.Ref(),
		ops.WithArgs("sh", "-c", "echo $FOO"),
		ops.WithCwd("/workspace"),
		ops.WithEnv("FOO=bar", "BAZ=qux"),
		ops.WithUser("nobody"),
		ops.WithNetwork(ops.NetModeNone),
	)

	dag, _ := graph.BuildFromRoot(ctx, e)
	m := marshal.New()
	def, err := m.Marshal(ctx, dag, e)
	if err != nil {
		t.Fatal(err)
	}

	var execSOP *marshal.SerializedOp
	for i := range def.Ops {
		if def.Ops[i].Kind == vertex.KindExec {
			execSOP = &def.Ops[i]
		}
	}
	if execSOP == nil || execSOP.Op.Exec == nil {
		t.Fatal("exec op not found")
	}

	ep := execSOP.Op.Exec
	if len(ep.Args) != 3 || ep.Args[0] != "sh" {
		t.Errorf("args: got %v", ep.Args)
	}
	if ep.Cwd != "/workspace" {
		t.Errorf("cwd: want /workspace, got %q", ep.Cwd)
	}
	if ep.User != "nobody" {
		t.Errorf("user: want nobody, got %q", ep.User)
	}
	if ep.Network != int(ops.NetModeNone) {
		t.Errorf("network: want %d, got %d", ops.NetModeNone, ep.Network)
	}
	if len(ep.Env) != 2 {
		t.Errorf("env: expected 2 vars, got %v", ep.Env)
	}
}

// ─── Git / HTTP / Local sources ───────────────────────────────────────────────

func TestMarshalGitSource(t *testing.T) {
	src := ops.Git("https://github.com/example/repo.git",
		ops.WithGitRef("v1.2.3"),
		ops.WithGitSubDir("src"),
	)
	def, _ := buildAndMarshal(t, src)
	if def.Ops[0].Op.Source == nil {
		t.Fatal("no source payload")
	}
	id := def.Ops[0].Op.Source.Identifier
	if id == "" {
		t.Error("git identifier must not be empty")
	}
}

func TestMarshalHTTPSource(t *testing.T) {
	src := ops.HTTP("https://example.com/archive.tar.gz",
		ops.WithHTTPChecksum("sha256:abc123"),
	)
	def, _ := buildAndMarshal(t, src)
	if def.Ops[0].Op.Source.Identifier != "https://example.com/archive.tar.gz" {
		t.Errorf("http identifier: got %q", def.Ops[0].Op.Source.Identifier)
	}
	if def.Ops[0].Op.Source.Attrs["http.checksum"] != "sha256:abc123" {
		t.Errorf("http checksum attr missing")
	}
}

func TestMarshalLocalSource(t *testing.T) {
	src := ops.Local("my-context",
		ops.WithLocalInclude("**/*.go"),
		ops.WithLocalExclude("vendor/**"),
	)
	def, _ := buildAndMarshal(t, src)
	if def.Ops[0].Op.Source.Identifier != "local://my-context" {
		t.Errorf("local identifier: got %q", def.Ops[0].Op.Source.Identifier)
	}
}
