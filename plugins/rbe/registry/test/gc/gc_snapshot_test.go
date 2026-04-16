// Package gc_test exhaustively tests the GC collector and snapshot manager.
package gc_test

import (
	"bytes"
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"testing"
	"time"

	digest "github.com/opencontainers/go-digest"
	ocispec "github.com/opencontainers/image-spec/specs-go/v1"

	"github.com/bons/bons-ci/plugins/rbe/registry/internal/gc"
	"github.com/bons/bons-ci/plugins/rbe/registry/internal/index"
	"github.com/bons/bons-ci/plugins/rbe/registry/internal/snapshot"
	"github.com/bons/bons-ci/plugins/rbe/registry/internal/storage/memory"
	"github.com/bons/bons-ci/plugins/rbe/registry/pkg/types"
)

// ────────────────────────────────────────────────────────────────────────────
// Helpers
// ────────────────────────────────────────────────────────────────────────────

func sha(s string) digest.Digest { return digest.Canonical.FromString(s) }

// buildManifest creates a minimal OCI manifest with config + layer and stores
// all blobs into store. Returns the manifest digest and raw bytes.
func buildManifest(t *testing.T, store *memory.Store, tag string) (digest.Digest, []byte) {
	t.Helper()
	ctx := context.Background()

	cfg := []byte(`{"os":"linux"}`)
	cfgDgst := digest.Canonical.FromBytes(cfg)
	putBlob(t, store, cfg)

	layer := []byte("layer-" + tag)
	layerDgst := digest.Canonical.FromBytes(layer)
	putBlob(t, store, layer)

	mf := ocispec.Manifest{
		Config: ocispec.Descriptor{
			MediaType: ocispec.MediaTypeImageConfig,
			Digest:    cfgDgst,
			Size:      int64(len(cfg)),
		},
		Layers: []ocispec.Descriptor{
			{MediaType: ocispec.MediaTypeImageLayerGzip, Digest: layerDgst, Size: int64(len(layer))},
		},
	}
	mf.SchemaVersion = 2
	raw, _ := json.Marshal(mf)
	mfDgst := digest.Canonical.FromBytes(raw)
	putBlob(t, store, raw)
	_ = ctx
	return mfDgst, raw
}

func putBlob(t *testing.T, store *memory.Store, data []byte) digest.Digest {
	t.Helper()
	dgst := digest.Canonical.FromBytes(data)
	_ = store.Put(context.Background(), dgst, bytes.NewReader(data), int64(len(data)))
	return dgst
}

// ────────────────────────────────────────────────────────────────────────────
// GC tests
// ────────────────────────────────────────────────────────────────────────────

func TestGC_SweepsOrphanedBlob(t *testing.T) {
	ctx := context.Background()
	store := memory.New()

	// Push a manifest (reachable)
	_, _ = buildManifest(t, store, "live")

	// Push an orphaned blob (no manifest references it)
	orphan := []byte("i-am-orphaned")
	orphanDgst := putBlob(t, store, orphan)

	lister := gc.NewSimpleManifestLister(store)
	collector := gc.New(store, lister, gc.Config{
		GracePeriod: 0, // no grace — sweep everything unreferenced immediately
	})

	report, err := collector.Collect(ctx)
	if err != nil {
		t.Fatalf("Collect: %v", err)
	}

	// Orphan should have been swept
	if report.SweptBlobs == 0 {
		t.Errorf("expected SweptBlobs > 0, got %d", report.SweptBlobs)
	}

	exists, _ := store.Exists(ctx, orphanDgst)
	if exists {
		t.Error("orphaned blob should have been deleted")
	}
}

func TestGC_PreservesReferencedBlobs(t *testing.T) {
	ctx := context.Background()
	store := memory.New()

	mfDgst, _ := buildManifest(t, store, "preserved")

	// Count blobs before
	var before int
	_ = store.Walk(ctx, func(types.ContentInfo) error { before++; return nil })

	lister := gc.NewSimpleManifestLister(store)
	collector := gc.New(store, lister, gc.Config{GracePeriod: 0})
	report, err := collector.Collect(ctx)
	if err != nil {
		t.Fatalf("Collect: %v", err)
	}

	if report.SweptBlobs != 0 {
		t.Errorf("no blobs should be swept when all are referenced, swept=%d", report.SweptBlobs)
	}

	// Manifest itself must survive
	exists, _ := store.Exists(ctx, mfDgst)
	if !exists {
		t.Error("manifest blob should survive GC")
	}
}

func TestGC_DryRun_DoesNotDelete(t *testing.T) {
	ctx := context.Background()
	store := memory.New()

	_, _ = buildManifest(t, store, "main")
	orphan := putBlob(t, store, []byte("dry-run-orphan"))

	lister := gc.NewSimpleManifestLister(store)
	collector := gc.New(store, lister, gc.Config{GracePeriod: 0, DryRun: true})
	report, err := collector.Collect(ctx)
	if err != nil {
		t.Fatalf("Collect: %v", err)
	}

	// DryRun: blob count should be reported but NOT deleted
	if report.SweptBlobs == 0 {
		t.Error("dry run should still report swept blobs")
	}
	exists, _ := store.Exists(ctx, orphan)
	if !exists {
		t.Error("dry run must NOT delete blobs")
	}
	if !report.DryRun {
		t.Error("report.DryRun should be true")
	}
}

func TestGC_GracePeriod_ProtectsNewBlobs(t *testing.T) {
	ctx := context.Background()
	store := memory.New()

	_, _ = buildManifest(t, store, "main")
	// Fresh orphan — within grace period
	orphan := putBlob(t, store, []byte("fresh-orphan"))

	lister := gc.NewSimpleManifestLister(store)
	collector := gc.New(store, lister, gc.Config{
		GracePeriod: 10 * time.Minute, // new blobs should be protected
	})
	report, err := collector.Collect(ctx)
	if err != nil {
		t.Fatalf("Collect: %v", err)
	}

	// Grace period should protect the orphan
	exists, _ := store.Exists(ctx, orphan)
	if !exists {
		t.Error("orphan within grace period should not be swept")
	}
	_ = report
}

func TestGC_MultipleManifests(t *testing.T) {
	ctx := context.Background()
	store := memory.New()

	// Two live manifests
	_, _ = buildManifest(t, store, "image1")
	_, _ = buildManifest(t, store, "image2")

	// Two orphaned blobs
	o1 := putBlob(t, store, []byte("orphan-1"))
	o2 := putBlob(t, store, []byte("orphan-2"))

	lister := gc.NewSimpleManifestLister(store)
	collector := gc.New(store, lister, gc.Config{GracePeriod: 0})
	report, err := collector.Collect(ctx)
	if err != nil {
		t.Fatalf("Collect: %v", err)
	}

	if report.SweptBlobs < 2 {
		t.Errorf("expected ≥2 swept blobs, got %d", report.SweptBlobs)
	}

	for _, d := range []digest.Digest{o1, o2} {
		if exists, _ := store.Exists(ctx, d); exists {
			t.Errorf("orphan %s should have been swept", d)
		}
	}
}

// ────────────────────────────────────────────────────────────────────────────
// Snapshot tests
// ────────────────────────────────────────────────────────────────────────────

func TestSnapshot_WriteAndRestore(t *testing.T) {
	ctx := context.Background()
	dir := t.TempDir()

	idx := index.NewShardedIndex(1000)

	// Populate index
	src := sha("snapshot-source")
	variant := types.AccelVariant{
		AccelType:      types.AccelNydus,
		ManifestDigest: sha("nydus-manifest"),
		Repository:     "library/node",
		SourceRefs:     []types.SourceRef{{Digest: src, Kind: types.SourceRefManifest}},
		CreatedAt:      time.Now(),
		Annotations:    map[string]string{},
	}
	_ = idx.Index(ctx, variant)

	mgr := snapshot.New(idx, snapshot.Config{Dir: dir, Interval: time.Hour})
	if err := mgr.WriteSnapshot(ctx); err != nil {
		t.Fatalf("WriteSnapshot: %v", err)
	}

	// Verify file exists
	snapPath := filepath.Join(dir, "accelindex.snap.gz")
	if _, err := os.Stat(snapPath); err != nil {
		t.Fatalf("snapshot file not created: %v", err)
	}

	// Create new empty index and restore from snapshot
	idx2 := index.NewShardedIndex(1000)
	mgr2 := snapshot.New(idx2, snapshot.Config{Dir: dir})
	if err := mgr2.Restore(ctx); err != nil {
		t.Fatalf("Restore: %v", err)
	}

	result, err := idx2.Query(ctx, src)
	if err != nil {
		t.Fatalf("Query after restore: %v", err)
	}
	if !result.Found {
		t.Error("expected Found=true after snapshot restore")
	}
	if result.TotalVariants != 1 {
		t.Errorf("expected 1 variant, got %d", result.TotalVariants)
	}
}

func TestSnapshot_RestoreMissingFile_OK(t *testing.T) {
	ctx := context.Background()
	idx := index.NewShardedIndex(100)
	mgr := snapshot.New(idx, snapshot.Config{Dir: t.TempDir()})

	// Restoring when no snapshot file exists should not error
	if err := mgr.Restore(ctx); err != nil {
		t.Errorf("expected nil error for missing snapshot file, got: %v", err)
	}
}

func TestSnapshot_AtomicWrite(t *testing.T) {
	ctx := context.Background()
	dir := t.TempDir()
	idx := index.NewShardedIndex(100)

	src := sha("atomic-test-source")
	_ = idx.Index(ctx, types.AccelVariant{
		AccelType:      types.AccelEstargz,
		ManifestDigest: sha("estargz-mf"),
		Repository:     "test/atomic",
		SourceRefs:     []types.SourceRef{{Digest: src, Kind: types.SourceRefManifest}},
		Annotations:    map[string]string{},
	})

	mgr := snapshot.New(idx, snapshot.Config{Dir: dir})

	// Write 10 snapshots — no tmp files should remain
	for i := 0; i < 10; i++ {
		if err := mgr.WriteSnapshot(ctx); err != nil {
			t.Fatalf("WriteSnapshot iteration %d: %v", i, err)
		}
	}

	// No tmp file should be left behind
	entries, _ := os.ReadDir(dir)
	for _, e := range entries {
		if filepath.Ext(e.Name()) == ".tmp" {
			t.Errorf("tmp file left behind: %s", e.Name())
		}
	}
}

func TestSnapshot_MultipleEntriesRoundTrip(t *testing.T) {
	ctx := context.Background()
	dir := t.TempDir()
	idx := index.NewShardedIndex(10000)

	// Insert 100 sources × 4 accel types
	sources := make([]digest.Digest, 100)
	for i := range sources {
		src := sha(string(rune('a'+i)) + "-source")
		sources[i] = src
		for _, at := range types.KnownAccelTypes {
			_ = idx.Index(ctx, types.AccelVariant{
				AccelType:      at,
				ManifestDigest: sha(string(at) + src.String()),
				Repository:     "batch/test",
				SourceRefs:     []types.SourceRef{{Digest: src, Kind: types.SourceRefManifest}},
				Annotations:    map[string]string{},
			})
		}
	}

	mgr := snapshot.New(idx, snapshot.Config{Dir: dir})
	_ = mgr.WriteSnapshot(ctx)

	idx2 := index.NewShardedIndex(10000)
	mgr2 := snapshot.New(idx2, snapshot.Config{Dir: dir})
	_ = mgr2.Restore(ctx)

	for _, src := range sources[:10] { // spot-check 10
		result, _ := idx2.Query(ctx, src)
		if !result.Found || result.TotalVariants != len(types.KnownAccelTypes) {
			t.Errorf("source %s: expected %d variants after restore, got %d",
				src, len(types.KnownAccelTypes), result.TotalVariants)
		}
	}
}
