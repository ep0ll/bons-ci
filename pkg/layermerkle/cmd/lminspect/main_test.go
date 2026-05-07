package main

import (
	"bytes"
	"strings"
	"testing"

	"github.com/bons/bons-ci/pkg/layermerkle"
	"github.com/bons/bons-ci/pkg/layermerkle/testutil"
)

func buildTestForest() *layermerkle.MerkleForest {
	b1 := layermerkle.NewMerkleTreeBuilder(
		testutil.MakeVertexID("vertex-alpha"),
		testutil.MakeLayerStack("layer-base"),
	)
	b1.Add(&layermerkle.HashResult{
		RelPath: "usr/bin/python3",
		Hash:    testutil.MakeFileHash("python3-content"),
	})
	b1.Add(&layermerkle.HashResult{
		RelPath:   "lib/libssl.so",
		Hash:      testutil.MakeFileHash("libssl-content"),
		FromCache: true,
	})

	f := layermerkle.NewMerkleForest()
	f.Add(b1.Finalize())
	return f
}

func TestRun_Inspect_HumanOutput(t *testing.T) {
	forest := buildTestForest()
	data, err := layermerkle.MarshalForest(forest)
	if err != nil {
		t.Fatalf("MarshalForest: %v", err)
	}

	// Write forest to a temp file and read it back.
	var out, errOut bytes.Buffer
	cfg := config{args: []string{}, jsonOutput: false}

	// Feed via stdin simulation — pass forest directly through runInspect.
	restoredForest, err := layermerkle.UnmarshalForest(data)
	if err != nil {
		t.Fatalf("UnmarshalForest: %v", err)
	}
	trees := restoredForest.All()
	if err := emitHuman(&out, trees); err != nil {
		t.Fatalf("emitHuman: %v", err)
	}
	_ = errOut
	_ = cfg

	text := out.String()
	if !strings.Contains(text, "MerkleTree{") {
		t.Errorf("human output missing MerkleTree header, got:\n%s", text)
	}
}

func TestRun_Inspect_JSONOutput(t *testing.T) {
	forest := buildTestForest()
	var out bytes.Buffer
	trees := forest.All()
	if err := emitJSON(&out, trees); err != nil {
		t.Fatalf("emitJSON: %v", err)
	}
	text := out.String()
	if !strings.Contains(text, `"version"`) {
		t.Errorf("JSON output missing version field, got:\n%s", text)
	}
	if !strings.Contains(text, `"trees"`) {
		t.Errorf("JSON output missing trees field")
	}
}

func TestRun_Inspect_Stats(t *testing.T) {
	forest := buildTestForest()
	var out bytes.Buffer
	trees := forest.All()
	if err := emitStats(&out, trees); err != nil {
		t.Fatalf("emitStats: %v", err)
	}
	text := out.String()
	if !strings.Contains(text, "hit_rate") {
		t.Errorf("stats output missing hit_rate column")
	}
	if !strings.Contains(text, "leaves") {
		t.Errorf("stats output missing leaves column")
	}
}

func TestRun_Diff_HumanOutput(t *testing.T) {
	b1 := layermerkle.NewMerkleTreeBuilder(testutil.MakeVertexID("v1"), testutil.MakeLayerStack("l"))
	b1.Add(&layermerkle.HashResult{RelPath: "bin/a", Hash: testutil.MakeFileHash("v1")})
	before := layermerkle.NewMerkleForest()
	before.Add(b1.Finalize())

	b2 := layermerkle.NewMerkleTreeBuilder(testutil.MakeVertexID("v1"), testutil.MakeLayerStack("l"))
	b2.Add(&layermerkle.HashResult{RelPath: "bin/a", Hash: testutil.MakeFileHash("v2")}) // changed
	after := layermerkle.NewMerkleForest()
	after.Add(b2.Finalize())

	d := layermerkle.Diff(before, after)
	var out bytes.Buffer
	if err := emitDiffHuman(&out, d); err != nil {
		t.Fatalf("emitDiffHuman: %v", err)
	}
	text := out.String()
	if !strings.Contains(text, "Changed") {
		t.Errorf("diff output should mention Changed vertices, got:\n%s", text)
	}
}

func TestRun_Diff_JSONOutput(t *testing.T) {
	f1 := layermerkle.NewMerkleForest()
	f2 := layermerkle.NewMerkleForest()
	// Add a vertex only to f2.
	b := layermerkle.NewMerkleTreeBuilder(testutil.MakeVertexID("new-v"), testutil.MakeLayerStack("l"))
	f2.Add(b.Finalize())

	d := layermerkle.Diff(f1, f2)
	var out bytes.Buffer
	if err := emitDiffJSON(&out, d); err != nil {
		t.Fatalf("emitDiffJSON: %v", err)
	}
	text := out.String()
	if !strings.Contains(text, `"added"`) {
		t.Errorf("diff JSON missing added field, got:\n%s", text)
	}
}

func TestFilterByVertex_PrefixMatch(t *testing.T) {
	forest := buildTestForest()
	trees := forest.All()
	// Use a clearly non-matching prefix.
	filtered := filterByVertex(trees, "sha256:zzznomatch")
	if len(filtered) != 0 {
		t.Errorf("filterByVertex: expected 0 matches, got %d", len(filtered))
	}
	// Use the full vertex ID of the first tree.
	if len(trees) > 0 {
		fullID := string(trees[0].VertexID)
		filtered2 := filterByVertex(trees, fullID[:10]) // prefix match
		if len(filtered2) == 0 {
			t.Error("filterByVertex: should match on prefix")
		}
	}
}

func TestShortDigestStr_Truncates(t *testing.T) {
	long := "sha256:abcdef1234567890abcdef1234567890abcdef1234"
	short := shortDigestStr(long)
	if len(short) > 19 {
		t.Errorf("shortDigestStr(%q) = %q (len=%d), want ≤19", long, short, len(short))
	}
}

func TestShortDigestStr_ShortPassthrough(t *testing.T) {
	s := "sha256:abc"
	if got := shortDigestStr(s); got != s {
		t.Errorf("shortDigestStr(%q) = %q, want unchanged", s, got)
	}
}
