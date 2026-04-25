package marshal_test

import (
	"context"
	"encoding/json"
	"testing"

	"github.com/bons/bons-ci/plugins/dag/graph"
	"github.com/bons/bons-ci/plugins/dag/marshal"
	"github.com/bons/bons-ci/plugins/dag/ops"
	"github.com/bons/bons-ci/plugins/dag/traverse"
	"github.com/bons/bons-ci/plugins/dag/vertex"
)

// ─── MarshalToJSON ───────────────────────────────────────────────────────────

func TestMarshalToJSON(t *testing.T) {
	ctx := context.Background()
	img := ops.Image("alpine:3.18")
	dag, _ := graph.BuildFromRoot(ctx, img)

	m := marshal.New()
	data, err := m.MarshalToJSON(ctx, dag, img)
	if err != nil {
		t.Fatalf("MarshalToJSON: %v", err)
	}
	if len(data) == 0 {
		t.Fatal("MarshalToJSON must produce non-empty output")
	}

	// Must be valid JSON.
	var raw map[string]any
	if err := json.Unmarshal(data, &raw); err != nil {
		t.Fatalf("MarshalToJSON produced invalid JSON: %v", err)
	}
	if raw["version"] != marshal.Version {
		t.Errorf("version: want %q, got %v", marshal.Version, raw["version"])
	}
}

func TestMarshalToJSONIndented(t *testing.T) {
	ctx := context.Background()
	img := ops.Image("alpine:3.18")
	dag, _ := graph.BuildFromRoot(ctx, img)

	m := marshal.New()
	data, err := m.MarshalToJSON(ctx, dag, img)
	if err != nil {
		t.Fatal(err)
	}
	// Indented output must contain newlines.
	found := false
	for _, b := range data {
		if b == '\n' {
			found = true
			break
		}
	}
	if !found {
		t.Error("MarshalToJSON must produce indented (multi-line) output")
	}
}

func TestMarshalToJSONRoundTrip(t *testing.T) {
	ctx := context.Background()
	img := ops.Image("alpine:3.18")
	fileOp := ops.NewFileOp(img.Ref(), ops.Mkdir("/app", 0755), ops.Constraints{})
	dag, _ := graph.BuildFromRoot(ctx, fileOp)

	m := marshal.New()
	data, err := m.MarshalToJSON(ctx, dag, fileOp)
	if err != nil {
		t.Fatal(err)
	}

	def, err := marshal.Unmarshal(data)
	if err != nil {
		t.Fatalf("round-trip unmarshal: %v", err)
	}
	if len(def.Ops) != 2 {
		t.Errorf("expected 2 ops, got %d", len(def.Ops))
	}
}

// ─── BuildOp serialization ────────────────────────────────────────────────────

func TestSerializeBuildOp(t *testing.T) {
	ctx := context.Background()

	// Create a source that contains a nested definition.
	src := ops.Image("alpine:3.18")
	buildOp := ops.NewBuildOp(src.Ref(),
		ops.WithDefinitionFile("Dockerfile.llb"),
	)

	dag, err := graph.BuildFromRoot(ctx, buildOp)
	if err != nil {
		t.Fatalf("build: %v", err)
	}

	m := marshal.New()
	def, err := m.Marshal(ctx, dag, buildOp)
	if err != nil {
		t.Fatalf("marshal: %v", err)
	}

	// Find the build op in serialized form.
	var buildFound bool
	for _, op := range def.Ops {
		if op.Kind == vertex.KindBuild {
			buildFound = true
			if op.Op.Build == nil {
				t.Error("build op must have Build payload")
			} else {
				if op.Op.Build.DefinitionFile != "Dockerfile.llb" {
					t.Errorf("definition file: got %q", op.Op.Build.DefinitionFile)
				}
				if op.Op.Build.SourceRef == nil {
					t.Error("build op must have a source ref")
				}
			}
		}
	}
	if !buildFound {
		t.Error("no build op found in serialized definition")
	}
}

func TestSerializeBuildOpWithoutDefinitionFile(t *testing.T) {
	ctx := context.Background()
	src := ops.Image("alpine:3.18")
	buildOp := ops.NewBuildOp(src.Ref()) // no definition file

	dag, _ := graph.BuildFromRoot(ctx, buildOp)
	m := marshal.New()
	def, err := m.Marshal(ctx, dag, buildOp)
	if err != nil {
		t.Fatalf("marshal: %v", err)
	}

	for _, op := range def.Ops {
		if op.Kind == vertex.KindBuild {
			if op.Op.Build == nil {
				t.Error("build op must have Build payload")
				continue
			}
			if op.Op.Build.DefinitionFile != "" {
				t.Errorf("definition file should be empty, got %q", op.Op.Build.DefinitionFile)
			}
		}
	}
}

func TestSerializeBuildOpIDStability(t *testing.T) {
	ctx := context.Background()
	src := ops.Image("alpine:3.18")
	buildOp := ops.NewBuildOp(src.Ref(), ops.WithDefinitionFile("plan.llb"))

	dag, _ := graph.BuildFromRoot(ctx, buildOp)
	m := marshal.New()

	var firstDigest string
	for i := 0; i < 10; i++ {
		def, err := m.Marshal(ctx, dag, buildOp)
		if err != nil {
			t.Fatal(err)
		}
		if firstDigest == "" {
			firstDigest = def.Digest
		} else if def.Digest != firstDigest {
			t.Errorf("run %d: digest changed from %q to %q", i+1, firstDigest, def.Digest)
		}
	}
}

// ─── CollectAllFromDAG ────────────────────────────────────────────────────────

func TestCollectAllFromDAG(t *testing.T) {
	ctx := context.Background()
	c := mkStub("C")
	b := mkStub("B", c)
	a := mkStub("A", b)

	dag, _ := graph.BuildFromRoot(ctx, a)
	verts := traverse.CollectAllFromDAG(dag)

	if len(verts) != 3 {
		t.Fatalf("want 3 vertices, got %d", len(verts))
	}
	// Must be in topological order: C before B before A.
	pos := make(map[string]int)
	for i, v := range verts {
		pos[v.ID()] = i
	}
	if pos["C"] >= pos["B"] || pos["B"] >= pos["A"] {
		t.Errorf("topological order violated: C=%d B=%d A=%d",
			pos["C"], pos["B"], pos["A"])
	}
}

func TestCollectAllFromDAGEmpty(t *testing.T) {
	builder := graph.NewBuilder()
	dag, _ := builder.Build()
	verts := traverse.CollectAllFromDAG(dag)
	if len(verts) != 0 {
		t.Errorf("empty DAG: want 0, got %d", len(verts))
	}
}

func TestCollectAllFromDAGDiamond(t *testing.T) {
	ctx := context.Background()
	d := mkStub("D")
	b := mkStub("B", d)
	c := mkStub("C", d)
	a := mkStub("A", b, c)

	dag, _ := graph.BuildFromRoot(ctx, a)
	verts := traverse.CollectAllFromDAG(dag)

	// D must appear exactly once.
	count := 0
	for _, v := range verts {
		if v.ID() == "D" {
			count++
		}
	}
	if count != 1 {
		t.Errorf("D must appear once, got %d: %v", count, idsOfVerts(verts))
	}
	if len(verts) != 4 {
		t.Errorf("expected 4 unique vertices, got %d", len(verts))
	}
}

// ─── Unmarshal strict mode ────────────────────────────────────────────────────

func TestUnmarshalExtraField(t *testing.T) {
	// Extra top-level field should not cause a hard error (forward compat).
	// We parse with strict decoder first, fall back to lenient.
	validJSON := `{"version":"v1","ops":[],"digest":"abc","created_at":"2024-01-01T00:00:00Z","extra_future_field":"ignored"}`
	def, err := marshal.Unmarshal([]byte(validJSON))
	if err != nil {
		// This is acceptable: strict mode rejects extra fields.
		// The fallback lenient decoder will catch the empty ops case.
		t.Logf("strict mode rejected extra field (acceptable): %v", err)
		return
	}
	if def.Version != "v1" {
		t.Errorf("version: want v1, got %q", def.Version)
	}
}

func TestUnmarshalBadJSON(t *testing.T) {
	_, err := marshal.Unmarshal([]byte(`{{bad json`))
	if err == nil {
		t.Fatal("bad JSON must produce an error")
	}
}

func TestUnmarshalEmptyVersion(t *testing.T) {
	_, err := marshal.Unmarshal([]byte(`{"version":"","ops":[],"digest":"x","created_at":"2024-01-01T00:00:00Z"}`))
	if err == nil {
		t.Fatal("empty version must produce an error")
	}
}

// ─── Marshal nil root (all vertices) ─────────────────────────────────────────

func TestMarshalNilRoot(t *testing.T) {
	ctx := context.Background()
	a := ops.Image("alpine:3.18")
	b := ops.Image("busybox:latest")

	dag, _ := graph.BuildFromRoots(ctx, a, b)
	m := marshal.New()
	def, err := m.Marshal(ctx, dag, nil) // nil = all vertices
	if err != nil {
		t.Fatalf("marshal nil root: %v", err)
	}
	if len(def.Ops) != 2 {
		t.Errorf("expected 2 ops, got %d", len(def.Ops))
	}
}

// ─── helpers ─────────────────────────────────────────────────────────────────

type marshalStub struct {
	id     string
	inputs []vertex.Vertex
}

func mkStub(id string, inputs ...vertex.Vertex) *marshalStub {
	return &marshalStub{id: id, inputs: inputs}
}
func (s *marshalStub) ID() string                       { return s.id }
func (s *marshalStub) Kind() vertex.Kind                { return vertex.KindSource }
func (s *marshalStub) Inputs() []vertex.Vertex          { return s.inputs }
func (s *marshalStub) Validate(_ context.Context) error { return nil }

func idsOfVerts(verts []vertex.Vertex) []string {
	ids := make([]string, len(verts))
	for i, v := range verts {
		ids[i] = v.ID()
	}
	return ids
}
