package layermerkle_test

import (
	"bytes"
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/bons/bons-ci/pkg/layermerkle"
	"github.com/bons/bons-ci/pkg/layermerkle/testutil"
)

// ─────────────────────────────────────────────────────────────────────────────
// OCIProvenance tests
// ─────────────────────────────────────────────────────────────────────────────

func buildSampleForest() *layermerkle.MerkleForest {
	b := layermerkle.NewMerkleTreeBuilder(
		testutil.MakeVertexID("prov-vertex"),
		testutil.MakeLayerStack("base-layer"),
	)
	b.Add(&layermerkle.HashResult{
		VertexID:     testutil.MakeVertexID("prov-vertex"),
		RelPath:      "usr/bin/python3",
		Hash:         testutil.MakeFileHash("python-content"),
		OwnerLayerID: testutil.MakeLayerStack("base-layer").Top(),
		FromCache:    false,
	})
	b.Add(&layermerkle.HashResult{
		VertexID:     testutil.MakeVertexID("prov-vertex"),
		RelPath:      "lib/libssl.so",
		Hash:         testutil.MakeFileHash("ssl-content"),
		OwnerLayerID: testutil.MakeLayerStack("base-layer").Top(),
		FromCache:    true,
	})
	f := layermerkle.NewMerkleForest()
	f.Add(b.Finalize())
	return f
}

func TestExportProvenance_FieldsPopulated(t *testing.T) {
	forest := buildSampleForest()
	p := layermerkle.ExportProvenance(forest,
		layermerkle.WithProvenanceBuildID("build-001"),
		layermerkle.WithProvenanceFiles(true),
	)

	if p.MediaType != layermerkle.OCIProvenanceMediaType {
		t.Errorf("MediaType = %q, want %q", p.MediaType, layermerkle.OCIProvenanceMediaType)
	}
	if p.SchemaVersion != 2 {
		t.Errorf("SchemaVersion = %d, want 2", p.SchemaVersion)
	}
	if p.BuildID != "build-001" {
		t.Errorf("BuildID = %q, want build-001", p.BuildID)
	}
	if len(p.Vertices) != 1 {
		t.Fatalf("Vertices len = %d, want 1", len(p.Vertices))
	}
	if len(p.Vertices[0].Files) != 2 {
		t.Errorf("Files len = %d, want 2", len(p.Vertices[0].Files))
	}
	if p.TotalFiles != 2 {
		t.Errorf("TotalFiles = %d, want 2", p.TotalFiles)
	}
	if p.TotalCacheHits != 1 {
		t.Errorf("TotalCacheHits = %d, want 1", p.TotalCacheHits)
	}
}

func TestExportProvenance_WithoutFiles(t *testing.T) {
	forest := buildSampleForest()
	p := layermerkle.ExportProvenance(forest, layermerkle.WithProvenanceFiles(false))
	if len(p.Vertices[0].Files) != 0 {
		t.Error("Files should be empty when WithProvenanceFiles(false)")
	}
}

func TestWriteReadProvenance_RoundTrip(t *testing.T) {
	forest := buildSampleForest()
	original := layermerkle.ExportProvenance(forest, layermerkle.WithProvenanceBuildID("rt-01"))

	var buf bytes.Buffer
	if err := layermerkle.WriteProvenance(&buf, original); err != nil {
		t.Fatalf("WriteProvenance: %v", err)
	}

	restored, err := layermerkle.ReadProvenance(&buf)
	if err != nil {
		t.Fatalf("ReadProvenance: %v", err)
	}
	if restored.BuildID != original.BuildID {
		t.Errorf("BuildID: got %q, want %q", restored.BuildID, original.BuildID)
	}
	if len(restored.Vertices) != len(original.Vertices) {
		t.Errorf("Vertices count: got %d, want %d", len(restored.Vertices), len(original.Vertices))
	}
	if restored.Vertices[0].MerkleRoot != original.Vertices[0].MerkleRoot {
		t.Error("MerkleRoot mismatch after round-trip")
	}
}

func TestReadProvenance_WrongMediaType_Errors(t *testing.T) {
	data := `{"mediaType":"application/vnd.other","schemaVersion":2,"vertices":[]}`
	_, err := layermerkle.ReadProvenance(strings.NewReader(data))
	if err == nil {
		t.Error("expected error for wrong media type")
	}
}

func TestDiffProvenance_DetectsChanges(t *testing.T) {
	forest1 := buildSampleForest()
	p1 := layermerkle.ExportProvenance(forest1)

	// Build a second forest with different content for the same vertex.
	b := layermerkle.NewMerkleTreeBuilder(
		testutil.MakeVertexID("prov-vertex"),
		testutil.MakeLayerStack("base-layer"),
	)
	b.Add(&layermerkle.HashResult{
		RelPath: "usr/bin/python3",
		Hash:    testutil.MakeFileHash("python-content-v2"), // changed
	})
	f2 := layermerkle.NewMerkleForest()
	f2.Add(b.Finalize())
	p2 := layermerkle.ExportProvenance(f2)

	result := layermerkle.DiffProvenance(p1, p2)
	if len(result.ChangedVertices) != 1 {
		t.Errorf("ChangedVertices = %d, want 1", len(result.ChangedVertices))
	}
}

func TestDiffProvenance_AddedVertex(t *testing.T) {
	p1 := layermerkle.ExportProvenance(layermerkle.NewMerkleForest())
	forest2 := buildSampleForest()
	p2 := layermerkle.ExportProvenance(forest2)

	result := layermerkle.DiffProvenance(p1, p2)
	if len(result.AddedVertices) != 1 {
		t.Errorf("AddedVertices = %d, want 1", len(result.AddedVertices))
	}
}

// ─────────────────────────────────────────────────────────────────────────────
// CacheWarmer tests
// ─────────────────────────────────────────────────────────────────────────────

func TestCacheWarmer_WarmFromForest_PopulatesCache(t *testing.T) {
	registry := layermerkle.NewLayerRegistry()
	layerID := testutil.MakeLayerStack("warm-layer").Top()
	_ = registry.Register(&layermerkle.LayerInfo{
		ID: layerID, DiffPath: "/diffs/warm",
	})

	cache := layermerkle.NewShardedLRUCache(1024)
	warmer := layermerkle.NewCacheWarmer(cache, registry)

	b := layermerkle.NewMerkleTreeBuilder(testutil.MakeVertexID("warm-v"), testutil.MakeLayerStack("warm-layer"))
	b.Add(&layermerkle.HashResult{
		RelPath:      "lib/libssl.so",
		Hash:         testutil.MakeFileHash("ssl"),
		OwnerLayerID: layerID,
	})
	forest := layermerkle.NewMerkleForest()
	forest.Add(b.Finalize())

	result, err := warmer.WarmFromForest(context.Background(), forest)
	if err != nil {
		t.Fatalf("WarmFromForest: %v", err)
	}
	if result.Warmed != 1 {
		t.Errorf("Warmed = %d, want 1", result.Warmed)
	}
	if _, ok := cache.Get(layerID, "lib/libssl.so"); !ok {
		t.Error("cache should be populated after warming")
	}
}

func TestCacheWarmer_Warm_SkipsUnknownLayer(t *testing.T) {
	registry := layermerkle.NewLayerRegistry()
	cache := layermerkle.NewShardedLRUCache(1024)
	warmer := layermerkle.NewCacheWarmer(cache, registry)

	p := &layermerkle.OCIProvenance{
		MediaType: layermerkle.OCIProvenanceMediaType,
		Vertices: []layermerkle.OCIProvenanceVertex{
			{
				VertexDigest: string(testutil.MakeVertexID("skip-v")),
				Files: []layermerkle.OCIProvenanceFile{
					{RelPath: "bin/sh", ContentDigest: "sha256:abc", OwnerLayer: "sha256:unknown"},
				},
			},
		},
	}

	result, err := warmer.Warm(context.Background(), p)
	if err != nil {
		t.Fatalf("Warm: %v", err)
	}
	if result.Skipped != 1 {
		t.Errorf("Skipped = %d, want 1", result.Skipped)
	}
	if result.Warmed != 0 {
		t.Errorf("Warmed = %d, want 0", result.Warmed)
	}
}

func TestCacheWarmer_WarmFromForest_SkipsEmptyHash(t *testing.T) {
	registry := layermerkle.NewLayerRegistry()
	cache := layermerkle.NewShardedLRUCache(1024)
	warmer := layermerkle.NewCacheWarmer(cache, registry)

	b := layermerkle.NewMerkleTreeBuilder(testutil.MakeVertexID("skip-hash"), testutil.MakeLayerStack("l"))
	b.Add(&layermerkle.HashResult{RelPath: "a", Hash: ""}) // empty hash — should skip
	forest := layermerkle.NewMerkleForest()
	forest.Add(b.Finalize())

	result, err := warmer.WarmFromForest(context.Background(), forest)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if result.Warmed != 0 {
		t.Errorf("Warmed = %d, want 0 for empty hash", result.Warmed)
	}
}

func TestNewEngineWithWarmStart_FromForest(t *testing.T) {
	registry := layermerkle.NewLayerRegistry()
	layerID := testutil.MakeLayerStack("ws-layer").Top()
	_ = registry.Register(&layermerkle.LayerInfo{
		ID: layerID, DiffPath: "/diffs/ws",
	})

	// Build a prior run's forest with a real OwnerLayerID.
	b := layermerkle.NewMerkleTreeBuilder(testutil.MakeVertexID("ws-v"), testutil.MakeLayerStack("ws-layer"))
	b.Add(&layermerkle.HashResult{
		RelPath:      "bin/sh",
		Hash:         testutil.MakeFileHash("sh-content"),
		OwnerLayerID: layerID,
	})
	priorForest := layermerkle.NewMerkleForest()
	priorForest.Add(b.Finalize())

	// Provide the same registry to the engine so the warmer can look up layers.
	engine, warmResult, err := layermerkle.NewEngineWithWarmStart(
		context.Background(),
		layermerkle.WarmStartConfig{
			Forest: priorForest,
			EngineOptions: []layermerkle.EngineOption{
				layermerkle.WithFileHasher(testutil.NewFakeHasher()),
				layermerkle.WithResolver(testutil.NewFakeResolver()),
				layermerkle.WithPredefinedRegistry(registry),
			},
		},
	)
	if err != nil {
		t.Fatalf("NewEngineWithWarmStart: %v", err)
	}
	if engine == nil {
		t.Fatal("engine should not be nil")
	}
	if warmResult.Warmed != 1 {
		t.Errorf("WarmResult.Warmed = %d, want 1", warmResult.Warmed)
	}
}

// ─────────────────────────────────────────────────────────────────────────────
// PersistentHashCache tests
// ─────────────────────────────────────────────────────────────────────────────

func TestPersistentHashCache_SetAndGet(t *testing.T) {
	walPath := filepath.Join(t.TempDir(), "cache.wal")
	c, err := layermerkle.NewPersistentHashCache(walPath, 1024, 0)
	if err != nil {
		t.Fatalf("NewPersistentHashCache: %v", err)
	}
	defer c.Close()

	id := testutil.MakeLayerStack("persist-layer").Top()
	hash := testutil.MakeFileHash("persist-content")
	c.Set(id, "lib/libssl.so", hash)

	got, ok := c.Get(id, "lib/libssl.so")
	if !ok {
		t.Fatal("Get after Set returned false")
	}
	if got != hash {
		t.Errorf("Get() = %v, want %v", got, hash)
	}
}

func TestPersistentHashCache_Delete_RemovesEntry(t *testing.T) {
	walPath := filepath.Join(t.TempDir(), "del.wal")
	c, err := layermerkle.NewPersistentHashCache(walPath, 1024, 0)
	if err != nil {
		t.Fatalf("NewPersistentHashCache: %v", err)
	}
	defer c.Close()

	id := testutil.MakeLayerStack("del-layer").Top()
	c.Set(id, "bin/sh", testutil.MakeFileHash("sh"))
	c.Delete(id, "bin/sh")

	if _, ok := c.Get(id, "bin/sh"); ok {
		t.Error("entry should be absent after Delete")
	}
}

func TestPersistentHashCache_WALReplay_RestoresEntries(t *testing.T) {
	dir := t.TempDir()
	walPath := filepath.Join(dir, "replay.wal")

	id := testutil.MakeLayerStack("replay-layer").Top()
	hash := testutil.MakeFileHash("replay-content")

	// First instance: write an entry.
	c1, err := layermerkle.NewPersistentHashCache(walPath, 1024, 0)
	if err != nil {
		t.Fatalf("open first: %v", err)
	}
	c1.Set(id, "lib/replay.so", hash)
	if err := c1.Close(); err != nil {
		t.Fatalf("close first: %v", err)
	}

	// Second instance: should replay the WAL and find the entry.
	c2, err := layermerkle.NewPersistentHashCache(walPath, 1024, 0)
	if err != nil {
		t.Fatalf("open second: %v", err)
	}
	defer c2.Close()

	got, ok := c2.Get(id, "lib/replay.so")
	if !ok {
		t.Fatal("WAL replay: entry not found after reopen")
	}
	if got != hash {
		t.Errorf("WAL replay: hash = %v, want %v", got, hash)
	}
}

func TestPersistentHashCache_WALPath_Returns_Path(t *testing.T) {
	walPath := filepath.Join(t.TempDir(), "path.wal")
	c, err := layermerkle.NewPersistentHashCache(walPath, 1024, 0)
	if err != nil {
		t.Fatalf("NewPersistentHashCache: %v", err)
	}
	defer c.Close()

	if c.WALPath() != walPath {
		t.Errorf("WALPath() = %q, want %q", c.WALPath(), walPath)
	}
}

func TestPersistentHashCache_InvalidPath_Errors(t *testing.T) {
	_, err := layermerkle.NewPersistentHashCache("/proc/invalid-dir/no.wal", 1024, 0)
	if err == nil {
		t.Error("expected error for invalid WAL path")
	}
}

func TestPersistentHashCache_ImplementsHashCache_Interface(t *testing.T) {
	walPath := filepath.Join(t.TempDir(), "iface.wal")
	c, err := layermerkle.NewPersistentHashCache(walPath, 1024, 0)
	if err != nil {
		t.Fatalf("NewPersistentHashCache: %v", err)
	}
	defer c.Close()
	var _ layermerkle.HashCache = c
}

// ─────────────────────────────────────────────────────────────────────────────
// HTML Report tests
// ─────────────────────────────────────────────────────────────────────────────

func buildSampleDiff() *layermerkle.ForestDiff {
	vtx := testutil.MakeVertexID("html-v1")

	b1 := layermerkle.NewMerkleTreeBuilder(vtx, testutil.MakeLayerStack("l"))
	b1.Add(&layermerkle.HashResult{RelPath: "bin/a", Hash: testutil.MakeFileHash("v1")})
	before := layermerkle.NewMerkleForest()
	before.Add(b1.Finalize())

	b2 := layermerkle.NewMerkleTreeBuilder(vtx, testutil.MakeLayerStack("l"))
	b2.Add(&layermerkle.HashResult{RelPath: "bin/a", Hash: testutil.MakeFileHash("v2")})
	b2.Add(&layermerkle.HashResult{RelPath: "bin/b", Hash: testutil.MakeFileHash("new")})
	after := layermerkle.NewMerkleForest()
	after.Add(b2.Finalize())

	return layermerkle.Diff(before, after)
}

func TestWriteHTMLReport_ContainsRequiredElements(t *testing.T) {
	d := buildSampleDiff()
	var buf bytes.Buffer
	err := layermerkle.WriteHTMLReport(&buf, d, layermerkle.HTMLReportConfig{
		Title:             "Test Report",
		BuildID:           "build-abc",
		ShowUnchanged:     true,
		MaxFilesPerVertex: 50,
	})
	if err != nil {
		t.Fatalf("WriteHTMLReport: %v", err)
	}
	html := buf.String()
	checks := []string{
		"<!DOCTYPE html>",
		"Test Report",
		"build-abc",
		"Summary",
		"Changed Vertices",
		"layermerkle",
	}
	for _, want := range checks {
		if !strings.Contains(html, want) {
			t.Errorf("HTML report missing %q", want)
		}
	}
}

func TestWriteHTMLReport_EscapesHTMLInDigests(t *testing.T) {
	// ForestDiff with no changed vertices — just ensure no injection.
	d := layermerkle.Diff(layermerkle.NewMerkleForest(), layermerkle.NewMerkleForest())
	var buf bytes.Buffer
	err := layermerkle.WriteHTMLReport(&buf, d, layermerkle.HTMLReportConfig{
		Title: "<script>alert(1)</script>",
	})
	if err != nil {
		t.Fatalf("WriteHTMLReport: %v", err)
	}
	html := buf.String()
	if strings.Contains(html, "<script>alert") {
		t.Error("HTML report should escape script tags in title")
	}
}

func TestWriteMarkdownReport_ContainsTableAndBadges(t *testing.T) {
	d := buildSampleDiff()
	var buf bytes.Buffer
	if err := layermerkle.WriteMarkdownReport(&buf, d, "md-build-1"); err != nil {
		t.Fatalf("WriteMarkdownReport: %v", err)
	}
	md := buf.String()
	checks := []string{
		"## layermerkle Build Comparison",
		"md-build-1",
		"| Category | Count |",
		"Changed",
		"lminspect",
	}
	for _, want := range checks {
		if !strings.Contains(md, want) {
			t.Errorf("Markdown report missing %q", md)
		}
	}
}

func TestWriteMarkdownReport_IdenticalForests(t *testing.T) {
	d := layermerkle.Diff(layermerkle.NewMerkleForest(), layermerkle.NewMerkleForest())
	var buf bytes.Buffer
	_ = layermerkle.WriteMarkdownReport(&buf, d, "")
	if !strings.Contains(buf.String(), "identical") {
		t.Error("identical forests should say so in markdown report")
	}
}

// ─────────────────────────────────────────────────────────────────────────────
// WhiteoutIndex tests (filesystem-based)
// ─────────────────────────────────────────────────────────────────────────────

func TestWhiteoutIndex_BuildAndQuery(t *testing.T) {
	dir := t.TempDir()

	// Create a per-file whiteout.
	os.MkdirAll(filepath.Join(dir, "usr/lib"), 0o755)          //nolint:errcheck
	os.WriteFile(filepath.Join(dir, "usr/lib/.wh.libssl.so"),  //nolint:errcheck
		nil, 0o644)

	// Create an opaque whiteout directory.
	os.MkdirAll(filepath.Join(dir, "etc"), 0o755)             //nolint:errcheck
	os.WriteFile(filepath.Join(dir, "etc/.wh..wh..opq"),      //nolint:errcheck
		nil, 0o644)

	idx, err := layermerkle.BuildWhiteoutIndex(dir)
	if err != nil {
		t.Fatalf("BuildWhiteoutIndex: %v", err)
	}

	// Per-file whiteout: usr/lib/libssl.so should be masked.
	if !idx.IsMasked("usr/lib/libssl.so") {
		t.Error("usr/lib/libssl.so should be masked by .wh.libssl.so")
	}

	// Opaque whiteout: anything under etc/ should be masked.
	if !idx.IsMasked("etc/passwd") {
		t.Error("etc/passwd should be masked by etc/.wh..wh..opq")
	}
	if !idx.IsMasked("etc/hosts") {
		t.Error("etc/hosts should be masked by opaque whiteout")
	}

	// Regular file — not masked.
	if idx.IsMasked("bin/sh") {
		t.Error("bin/sh should not be masked")
	}

	if idx.FileWhiteoutCount() != 1 {
		t.Errorf("FileWhiteoutCount = %d, want 1", idx.FileWhiteoutCount())
	}
	if idx.OpaqueDirectoryCount() != 1 {
		t.Errorf("OpaqueDirectoryCount = %d, want 1", idx.OpaqueDirectoryCount())
	}
}

func TestWhiteoutIndex_EmptyDir_ReturnsEmptyIndex(t *testing.T) {
	idx, err := layermerkle.BuildWhiteoutIndex(t.TempDir())
	if err != nil {
		t.Fatalf("BuildWhiteoutIndex on empty dir: %v", err)
	}
	if idx.FileWhiteoutCount() != 0 || idx.OpaqueDirectoryCount() != 0 {
		t.Error("empty directory should produce empty index")
	}
	if idx.IsMasked("any/path") {
		t.Error("empty index should not mask any path")
	}
}

func TestWhiteoutIndex_NonexistentDir_ReturnsEmptyIndex(t *testing.T) {
	idx, err := layermerkle.BuildWhiteoutIndex("/nonexistent/path/12345")
	if err != nil {
		t.Fatalf("BuildWhiteoutIndex on nonexistent dir should not error: %v", err)
	}
	if idx == nil {
		t.Fatal("index should not be nil")
	}
	_ = time.Now() // suppress import
}
