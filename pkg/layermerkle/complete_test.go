package layermerkle_test

import (
	"bytes"
	"context"
	"errors"
	"strings"
	"testing"
	"time"

	"github.com/bons/bons-ci/pkg/layermerkle"
	"github.com/bons/bons-ci/pkg/layermerkle/testutil"
)

// ─────────────────────────────────────────────────────────────────────────────
// ForestDiff tests
// ─────────────────────────────────────────────────────────────────────────────

func buildForest(trees ...*layermerkle.MerkleTree) *layermerkle.MerkleForest {
	f := layermerkle.NewMerkleForest()
	for _, t := range trees {
		f.Add(t)
	}
	return f
}

func makeTree(vtx, content string) *layermerkle.MerkleTree {
	b := layermerkle.NewMerkleTreeBuilder(
		testutil.MakeVertexID(vtx),
		testutil.MakeLayerStack("l"),
	)
	b.Add(&layermerkle.HashResult{
		VertexID: testutil.MakeVertexID(vtx),
		RelPath:  "bin/app",
		Hash:     testutil.MakeFileHash(content),
	})
	return b.Finalize()
}

func TestDiff_IdenticalForests(t *testing.T) {
	tree := makeTree("v1", "content-v1")
	a := buildForest(tree)
	b := buildForest(tree)

	d := layermerkle.Diff(a, b)
	if !d.IsIdentical() {
		t.Errorf("identical forests: IsIdentical()=false, got %s", d.Summary())
	}
}

func TestDiff_AddedVertex(t *testing.T) {
	t1 := makeTree("v1", "c1")
	t2 := makeTree("v2", "c2")

	a := buildForest(t1)
	b := buildForest(t1, t2)

	d := layermerkle.Diff(a, b)
	if len(d.Added) != 1 {
		t.Errorf("Added=%d, want 1", len(d.Added))
	}
	if d.Added[0].VertexID != t2.VertexID {
		t.Errorf("Added[0] wrong vertex")
	}
	if len(d.Unchanged) != 1 {
		t.Errorf("Unchanged=%d, want 1", len(d.Unchanged))
	}
}

func TestDiff_RemovedVertex(t *testing.T) {
	t1 := makeTree("v1", "c1")
	t2 := makeTree("v2", "c2")

	a := buildForest(t1, t2)
	b := buildForest(t1)

	d := layermerkle.Diff(a, b)
	if len(d.Removed) != 1 {
		t.Errorf("Removed=%d, want 1", len(d.Removed))
	}
}

func TestDiff_ChangedVertex(t *testing.T) {
	vtx := "v1"
	before := makeTree(vtx, "content-v1")
	after := makeTree(vtx, "content-v2")

	a := buildForest(before)
	b := buildForest(after)

	d := layermerkle.Diff(a, b)
	if len(d.Changed) != 1 {
		t.Errorf("Changed=%d, want 1", len(d.Changed))
	}
	if d.Changed[0].Before.Root == d.Changed[0].After.Root {
		t.Error("Changed entry should have different Before/After roots")
	}
}

func TestDiff_ComputeFileDiff(t *testing.T) {
	vtx := "v1"
	b1 := layermerkle.NewMerkleTreeBuilder(testutil.MakeVertexID(vtx), testutil.MakeLayerStack("l"))
	b1.Add(&layermerkle.HashResult{RelPath: "bin/a", Hash: testutil.MakeFileHash("a-v1")})
	b1.Add(&layermerkle.HashResult{RelPath: "bin/b", Hash: testutil.MakeFileHash("b")})
	before := b1.Finalize()

	b2 := layermerkle.NewMerkleTreeBuilder(testutil.MakeVertexID(vtx), testutil.MakeLayerStack("l"))
	b2.Add(&layermerkle.HashResult{RelPath: "bin/a", Hash: testutil.MakeFileHash("a-v2")}) // changed
	b2.Add(&layermerkle.HashResult{RelPath: "bin/c", Hash: testutil.MakeFileHash("c")})    // new
	// bin/b removed
	after := b2.Finalize()

	a := buildForest(before)
	bF := buildForest(after)
	d := layermerkle.Diff(a, bF)

	if len(d.Changed) != 1 {
		t.Fatalf("Changed=%d, want 1", len(d.Changed))
	}
	fd := d.Changed[0].ComputeFileDiff()

	if len(fd.Added) != 1 || fd.Added[0].RelPath != "bin/c" {
		t.Errorf("FileDiff.Added wrong: %+v", fd.Added)
	}
	if len(fd.Removed) != 1 || fd.Removed[0].RelPath != "bin/b" {
		t.Errorf("FileDiff.Removed wrong: %+v", fd.Removed)
	}
	if len(fd.Modified) != 1 || fd.Modified[0].RelPath != "bin/a" {
		t.Errorf("FileDiff.Modified wrong: %+v", fd.Modified)
	}
}

func TestDiff_Summary_Format(t *testing.T) {
	a := buildForest(makeTree("v1", "c"))
	b := buildForest(makeTree("v2", "c"))
	d := layermerkle.Diff(a, b)
	s := d.Summary()
	if !strings.Contains(s, "added=1") || !strings.Contains(s, "removed=1") {
		t.Errorf("Summary() = %q missing expected fields", s)
	}
}

// ─────────────────────────────────────────────────────────────────────────────
// Serialization tests
// ─────────────────────────────────────────────────────────────────────────────

func TestMarshalForest_RoundTrip(t *testing.T) {
	original := buildForest(
		makeTree("vertex-a", "content-a"),
		makeTree("vertex-b", "content-b"),
	)

	data, err := layermerkle.MarshalForest(original)
	if err != nil {
		t.Fatalf("MarshalForest: %v", err)
	}
	if len(data) == 0 {
		t.Fatal("MarshalForest produced empty output")
	}

	restored, err := layermerkle.UnmarshalForest(data)
	if err != nil {
		t.Fatalf("UnmarshalForest: %v", err)
	}
	if restored.Len() != original.Len() {
		t.Errorf("forest Len: got %d, want %d", restored.Len(), original.Len())
	}
	for _, orig := range original.All() {
		got, ok := restored.Get(orig.VertexID)
		if !ok {
			t.Errorf("vertex %s missing after restore", orig.VertexID)
			continue
		}
		if got.Root != orig.Root {
			t.Errorf("root mismatch for %s: got %v, want %v", orig.VertexID, got.Root, orig.Root)
		}
	}
}

func TestMarshalForest_InvalidVersionFails(t *testing.T) {
	_, err := layermerkle.UnmarshalForest([]byte(`{"version":99,"trees":[]}`))
	if err == nil {
		t.Error("expected error for unsupported version")
	}
}

func TestMarshalTree_RoundTrip(t *testing.T) {
	tree := makeTree("round-trip-vertex", "file-content")
	data, err := layermerkle.MarshalTree(tree)
	if err != nil {
		t.Fatalf("MarshalTree: %v", err)
	}
	got, err := layermerkle.UnmarshalTree(data)
	if err != nil {
		t.Fatalf("UnmarshalTree: %v", err)
	}
	if got.Root != tree.Root {
		t.Errorf("root mismatch: got %v, want %v", got.Root, tree.Root)
	}
	if got.LeafCount != tree.LeafCount {
		t.Errorf("LeafCount: got %d, want %d", got.LeafCount, tree.LeafCount)
	}
}

func TestWriteForest_ReadForest_RoundTrip(t *testing.T) {
	original := buildForest(makeTree("io-vertex", "io-content"))
	var buf bytes.Buffer
	if err := layermerkle.WriteForest(&buf, original); err != nil {
		t.Fatalf("WriteForest: %v", err)
	}
	restored, err := layermerkle.ReadForest(&buf)
	if err != nil {
		t.Fatalf("ReadForest: %v", err)
	}
	if restored.Len() != original.Len() {
		t.Errorf("Len: got %d, want %d", restored.Len(), original.Len())
	}
}

func TestMarshalForest_IsStable(t *testing.T) {
	forest := buildForest(makeTree("stable-v", "stable-c"))
	d1, err := layermerkle.MarshalForest(forest)
	if err != nil {
		t.Fatal(err)
	}
	d2, err := layermerkle.MarshalForest(forest)
	if err != nil {
		t.Fatal(err)
	}
	if !bytes.Equal(d1, d2) {
		t.Error("MarshalForest is not stable (same input produced different output)")
	}
}

// ─────────────────────────────────────────────────────────────────────────────
// PrettyPrintTree tests
// ─────────────────────────────────────────────────────────────────────────────

func TestPrettyPrintTree_ContainsRootAndLeaves(t *testing.T) {
	b := layermerkle.NewMerkleTreeBuilder(testutil.MakeVertexID("pp-vertex"), testutil.MakeLayerStack("l"))
	b.Add(&layermerkle.HashResult{RelPath: "usr/bin/sh", Hash: testutil.MakeFileHash("sh")})
	tree := b.Finalize()

	var buf bytes.Buffer
	if err := layermerkle.PrettyPrintTree(&buf, tree); err != nil {
		t.Fatalf("PrettyPrintTree: %v", err)
	}
	out := buf.String()
	if !strings.Contains(out, "MerkleTree{") {
		t.Error("output missing MerkleTree header")
	}
	if !strings.Contains(out, "sh") {
		t.Error("output missing leaf filename")
	}
}

// ─────────────────────────────────────────────────────────────────────────────
// WhiteoutDetector tests
// ─────────────────────────────────────────────────────────────────────────────

func TestClassifyWhiteout_FileWhiteout(t *testing.T) {
	if layermerkle.ClassifyWhiteout(".wh.passwd") != layermerkle.WhiteoutFile {
		t.Error("expected WhiteoutFile for .wh.passwd")
	}
}

func TestClassifyWhiteout_OpaqueWhiteout(t *testing.T) {
	if layermerkle.ClassifyWhiteout(".wh..wh..opq") != layermerkle.WhiteoutOpaque {
		t.Error("expected WhiteoutOpaque for .wh..wh..opq")
	}
}

func TestClassifyWhiteout_RegularFile(t *testing.T) {
	if layermerkle.ClassifyWhiteout("passwd") != layermerkle.WhiteoutNone {
		t.Error("expected WhiteoutNone for regular file")
	}
}

func TestClassifyWhiteout_MetaWhiteout(t *testing.T) {
	if layermerkle.ClassifyWhiteout(".wh..wh.baseimg") != layermerkle.WhiteoutMetadata {
		t.Error("expected WhiteoutMetadata for .wh..wh.baseimg")
	}
}

func TestOriginalName_ExtractsFileName(t *testing.T) {
	orig, ok := layermerkle.OriginalName(".wh.libssl.so")
	if !ok {
		t.Error("OriginalName returned ok=false for .wh.libssl.so")
	}
	if orig != "libssl.so" {
		t.Errorf("OriginalName = %q, want libssl.so", orig)
	}
}

func TestOriginalName_NonWhiteout(t *testing.T) {
	_, ok := layermerkle.OriginalName("libssl.so")
	if ok {
		t.Error("OriginalName should return ok=false for non-whiteout")
	}
}

func TestIsWhiteoutPath_WithWhiteoutSegment(t *testing.T) {
	if !layermerkle.IsWhiteoutPath("usr/lib/.wh.libssl.so") {
		t.Error("expected IsWhiteoutPath=true for path with whiteout segment")
	}
}

func TestIsWhiteoutPath_WithOpaqueSegment(t *testing.T) {
	if !layermerkle.IsWhiteoutPath("usr/.wh..wh..opq") {
		t.Error("expected IsWhiteoutPath=true for opaque whiteout path")
	}
}

func TestIsWhiteoutPath_RegularPath(t *testing.T) {
	if layermerkle.IsWhiteoutPath("usr/lib/libssl.so") {
		t.Error("expected IsWhiteoutPath=false for regular path")
	}
}

func TestOpaqueWhiteoutPaths_AllAncestors(t *testing.T) {
	paths := layermerkle.OpaqueWhiteoutPaths("usr/lib/libssl.so")
	// Should include usr/.wh..wh..opq and usr/lib/.wh..wh..opq
	found := map[string]bool{}
	for _, p := range paths {
		found[p] = true
	}
	if !found["usr/.wh..wh..opq"] {
		t.Errorf("missing usr/.wh..wh..opq in %v", paths)
	}
	if !found["usr/lib/.wh..wh..opq"] {
		t.Errorf("missing usr/lib/.wh..wh..opq in %v", paths)
	}
}

// ─────────────────────────────────────────────────────────────────────────────
// RingStatCache tests
// ─────────────────────────────────────────────────────────────────────────────

func TestRingStatCache_SetAndGet(t *testing.T) {
	c := layermerkle.NewRingStatCache(256)
	id := testutil.MakeLayerStack("ring-layer").Top()

	c.Set(id, "usr/bin/sh", layermerkle.StatPresent)
	result, ok := c.Get(id, "usr/bin/sh")
	if !ok {
		t.Fatal("Get after Set returned ok=false")
	}
	if result != layermerkle.StatPresent {
		t.Errorf("result = %v, want StatPresent", result)
	}
}

func TestRingStatCache_Miss(t *testing.T) {
	c := layermerkle.NewRingStatCache(256)
	id := testutil.MakeLayerStack("ring-miss").Top()
	_, ok := c.Get(id, "not/there")
	if ok {
		t.Error("expected miss for unset key")
	}
}

func TestRingStatCache_InvalidateLayer(t *testing.T) {
	c := layermerkle.NewRingStatCache(256)
	id := testutil.MakeLayerStack("invalidate-layer").Top()
	c.Set(id, "lib/x", layermerkle.StatPresent)
	c.InvalidateLayer(id)
	_, ok := c.Get(id, "lib/x")
	if ok {
		t.Error("entry should be gone after InvalidateLayer")
	}
}

func TestRingStatCache_EvictsWhenFull(t *testing.T) {
	cap := 4
	c := layermerkle.NewRingStatCache(cap)
	id := testutil.MakeLayerStack("evict-ring").Top()
	// Fill past capacity.
	for i := range cap + 2 {
		c.Set(id, string(rune('a'+i)), layermerkle.StatPresent)
	}
	// Total len should not exceed capacity.
	if c.Len() > cap {
		t.Errorf("Len() = %d, exceeds cap %d", c.Len(), cap)
	}
	h, _, e := c.Stats()
	if e == 0 {
		t.Error("expected evictions, got 0")
	}
	_ = h
}

func TestRingStatCache_Stats_HitMiss(t *testing.T) {
	c := layermerkle.NewRingStatCache(256)
	id := testutil.MakeLayerStack("stats").Top()
	c.Set(id, "a", layermerkle.StatPresent)
	c.Get(id, "a")    // hit
	c.Get(id, "miss") // miss

	h, m, _ := c.Stats()
	if h != 1 {
		t.Errorf("hits = %d, want 1", h)
	}
	if m != 1 {
		t.Errorf("misses = %d, want 1", m)
	}
}

// ─────────────────────────────────────────────────────────────────────────────
// Adapter tests
// ─────────────────────────────────────────────────────────────────────────────

func TestMergedPathResolver_RelPath(t *testing.T) {
	r := layermerkle.NewMergedPathResolver("/merged")

	tests := []struct {
		abs     string
		wantRel string
		wantErr bool
	}{
		{"/merged/usr/bin/sh", "usr/bin/sh", false},
		{"/merged", ".", false},
		{"/proc/1/maps", "", true},
		{"/mergedX/file", "", true},
	}
	for _, tt := range tests {
		rel, err := r.RelPath(tt.abs)
		if tt.wantErr {
			if err == nil {
				t.Errorf("RelPath(%q): expected error", tt.abs)
			}
			continue
		}
		if err != nil {
			t.Errorf("RelPath(%q): unexpected error: %v", tt.abs, err)
			continue
		}
		if rel != tt.wantRel {
			t.Errorf("RelPath(%q) = %q, want %q", tt.abs, rel, tt.wantRel)
		}
	}
}

func TestDirectEventSource_GettersReturnValues(t *testing.T) {
	ts := time.Now()
	attrs := map[string]any{"key": "value"}
	src := layermerkle.NewDirectEventSource(0x01, 999, "/path", ts, attrs)

	if src.GetMask() != 0x01 {
		t.Error("GetMask wrong")
	}
	if src.GetPID() != 999 {
		t.Error("GetPID wrong")
	}
	if src.GetPath() != "/path" {
		t.Error("GetPath wrong")
	}
	if src.GetTimestamp() != ts {
		t.Error("GetTimestamp wrong")
	}
	if src.GetAttr("key") != "value" {
		t.Error("GetAttr wrong")
	}
	if src.GetAttr("missing") != nil {
		t.Error("GetAttr for missing key should be nil")
	}
}

func TestPipelineAdapter_ConvertAndSubmit_MissingAttrs_ReturnsNil(t *testing.T) {
	engine := layermerkle.NewEngine(
		layermerkle.WithFileHasher(testutil.NewFakeHasher()),
		layermerkle.WithResolver(testutil.NewFakeResolver()),
	)
	ctx := context.Background()
	_ = engine.Start(ctx)
	defer engine.Stop()

	adapter := layermerkle.NewPipelineAdapter(engine)
	src := layermerkle.NewDirectEventSource(0x01, 100, "/path", time.Now(), nil)
	err := adapter.ConvertAndSubmit(ctx, src)
	if err != nil {
		t.Errorf("ConvertAndSubmit with missing attrs should return nil, got %v", err)
	}
}

func TestPipelineAdapter_ConvertAndSubmit_WriteEvent_Skipped(t *testing.T) {
	errCount := 0
	engine := layermerkle.NewEngine(
		layermerkle.WithFileHasher(testutil.NewFakeHasher()),
		layermerkle.WithResolver(testutil.NewFakeResolver()),
	)
	ctx := context.Background()
	_ = engine.Start(ctx)
	defer engine.Stop()

	adapter := layermerkle.NewPipelineAdapter(engine,
		layermerkle.WithAdapterErrorCallback(func(error) { errCount++ }),
	)
	vtx := testutil.MakeVertexID("write-test")
	stack := testutil.MakeLayerStack("l")
	attrs := layermerkle.AccessEventAttrs(vtx, stack, "bin/app")
	src := layermerkle.NewDirectEventSource(0x02, 100, "/merged/bin/app", time.Now(), attrs)
	err := adapter.ConvertAndSubmit(ctx, src)
	if err != nil {
		t.Errorf("write events should not error: %v", err)
	}
	if errCount != 0 {
		t.Errorf("errCount = %d, want 0 for write event skip", errCount)
	}
}

// ─────────────────────────────────────────────────────────────────────────────
// OCICompliantResolver interface compliance
// ─────────────────────────────────────────────────────────────────────────────

func TestOCICompliantResolver_ImplementsLayerFileResolver(t *testing.T) {
	var _ layermerkle.LayerFileResolver = layermerkle.NewOCICompliantResolver(
		layermerkle.NewLayerRegistry(), 1024,
	)
}

func TestOverlayResolverWithRingCache_ImplementsLayerFileResolver(t *testing.T) {
	var _ layermerkle.LayerFileResolver = layermerkle.NewOverlayResolverWithRingCache(
		layermerkle.NewLayerRegistry(), 1024,
	)
}

// ─────────────────────────────────────────────────────────────────────────────
// Regression: ErrWhiteout wraps properly through errors.Is
// ─────────────────────────────────────────────────────────────────────────────

func TestErrWhiteout_WrapsWithErrorsIs(t *testing.T) {
	wrapped := errors.Join(layermerkle.ErrWhiteout, errors.New("extra"))
	if !errors.Is(wrapped, layermerkle.ErrWhiteout) {
		t.Error("errors.Is should find ErrWhiteout in joined error")
	}
}
