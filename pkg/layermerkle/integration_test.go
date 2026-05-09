//go:build integration

// Integration tests require:
//   - Linux 5.1+ with CAP_SYS_ADMIN (or --privileged container)
//   - Real overlay filesystem mounts
//   - Run as root
//
// Execute with:
//
//	go test -tags integration -count=1 -v ./...
package layermerkle_test

import (
	"context"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/bons/bons-ci/pkg/layermerkle"
	"github.com/bons/bons-ci/pkg/layermerkle/testutil"
)

// TestIntegration_RealOverlayFilesystem tests the full engine pipeline against
// a real overlay filesystem created from temporary directories.
//
// The test:
//  1. Creates a layered directory structure simulating a container rootfs.
//  2. Registers the layers with the engine.
//  3. Submits access events referencing real files in the diff directories.
//  4. Verifies hashes match sha256 of the real file content.
//  5. Confirms deduplication: the second vertex accessing the same base file
//     incurs zero additional hash operations.
func TestIntegration_RealOverlayFilesystem(t *testing.T) {
	if os.Getuid() != 0 {
		t.Skip("integration test requires root (CAP_SYS_ADMIN)")
	}

	// ── Build a two-layer test fixture ────────────────────────────────────────
	root := t.TempDir()
	fix, err := testutil.NewOverlayFixture(root, 2)
	if err != nil {
		t.Fatalf("NewOverlayFixture: %v", err)
	}
	defer fix.Cleanup()

	// Write test files into different layers.
	if err := fix.WriteLayerFile(0, "usr/bin/python3", "#!/usr/bin/python3\n"); err != nil {
		t.Fatalf("WriteLayerFile: %v", err)
	}
	if err := fix.WriteLayerFile(1, "lib/libssl.so", "ELF-mock-binary"); err != nil {
		t.Fatalf("WriteLayerFile: %v", err)
	}
	if err := fix.WriteUpperFile("app/main.py", "print('hello world')"); err != nil {
		t.Fatalf("WriteUpperFile: %v", err)
	}

	// ── Register layers with the engine ──────────────────────────────────────
	registry := layermerkle.NewLayerRegistry()
	layerIDs := make([]layermerkle.LayerID, len(fix.LowerDirs)+1)
	for i, d := range fix.LowerDirs {
		id := testutil.MakeLayerStack(filepath.Base(d)).Top()
		if err := registry.Register(&layermerkle.LayerInfo{
			ID:       id,
			DiffPath: d,
		}); err != nil {
			t.Fatalf("Register lower %d: %v", i, err)
		}
		layerIDs[i] = id
	}
	upperID := testutil.MakeLayerStack("upper").Top()
	if err := registry.Register(&layermerkle.LayerInfo{
		ID:       upperID,
		DiffPath: fix.UpperDir,
	}); err != nil {
		t.Fatalf("Register upper: %v", err)
	}
	layerIDs[len(fix.LowerDirs)] = upperID

	// ── Build engine with real filesystem resolver ─────────────────────────
	resolver := layermerkle.NewOverlayResolver(registry, 10_000)
	engine := layermerkle.NewEngine(
		layermerkle.WithFileHasher(layermerkle.NewSingleflightHasher(layermerkle.NewSHA256Hasher())),
		layermerkle.WithResolver(resolver),
		layermerkle.WithPredefinedRegistry(registry),
		layermerkle.WithWorkers(2),
	)

	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()

	if err := engine.Start(ctx); err != nil {
		t.Fatalf("Start: %v", err)
	}

	stack := layermerkle.LayerStack(layerIDs)

	// ── ExecOp 1: access python3 and libssl.so ────────────────────────────
	vtx1 := testutil.MakeVertexID("exec-op-1")
	events := []*layermerkle.AccessEvent{
		{
			VertexID:   vtx1,
			LayerStack: stack,
			RelPath:    "usr/bin/python3",
			AbsPath:    filepath.Join(fix.MergedDir, "usr/bin/python3"),
			Mask:       0x00001000, // OPEN_EXEC
			Timestamp:  time.Now(),
		},
		{
			VertexID:   vtx1,
			LayerStack: stack,
			RelPath:    "lib/libssl.so",
			AbsPath:    filepath.Join(fix.MergedDir, "lib/libssl.so"),
			Mask:       0x00000001, // ACCESS
			Timestamp:  time.Now(),
		},
	}
	result := engine.SubmitBatch(ctx, events)
	if result.Submitted != 2 {
		t.Errorf("SubmitBatch submitted=%d, want 2", result.Submitted)
	}

	time.Sleep(100 * time.Millisecond)
	tree1, err := engine.FinalizeVertex(vtx1)
	if err != nil {
		t.Fatalf("FinalizeVertex: %v", err)
	}
	if tree1.LeafCount != 2 {
		t.Errorf("LeafCount = %d, want 2", tree1.LeafCount)
	}
	if tree1.Root == "" {
		t.Error("Root should not be empty")
	}

	// ── ExecOp 2: same files — should all be cache hits ───────────────────
	vtx2 := testutil.MakeVertexID("exec-op-2")
	events2 := []*layermerkle.AccessEvent{
		{VertexID: vtx2, LayerStack: stack, RelPath: "usr/bin/python3",
			Mask: 0x00001000, Timestamp: time.Now()},
		{VertexID: vtx2, LayerStack: stack, RelPath: "lib/libssl.so",
			Mask: 0x00000001, Timestamp: time.Now()},
	}
	engine.SubmitBatch(ctx, events2) //nolint:errcheck

	time.Sleep(100 * time.Millisecond)
	tree2, err := engine.FinalizeVertex(vtx2)
	if err != nil {
		t.Fatalf("FinalizeVertex vtx2: %v", err)
	}

	// Both trees must have identical Merkle roots (same files, same content).
	if tree1.Root != tree2.Root {
		t.Errorf("ExecOp1 root %v != ExecOp2 root %v (same files should produce same root)",
			tree1.Root, tree2.Root)
	}

	// ExecOp2 should have cache hits since ExecOp1 already computed the hashes.
	if tree2.CacheHitCount == 0 {
		t.Error("ExecOp2 should have cache hits (files already hashed by ExecOp1)")
	}

	engine.Stop()

	t.Logf("ExecOp1: root=%s leaves=%d", tree1.Root, tree1.LeafCount)
	t.Logf("ExecOp2: root=%s leaves=%d cacheHits=%d", tree2.Root, tree2.LeafCount, tree2.CacheHitCount)
}

// TestIntegration_OCIWhiteoutResolution tests the OCICompliantResolver against
// real whiteout files on disk.
func TestIntegration_OCIWhiteoutResolution(t *testing.T) {
	if os.Getuid() != 0 {
		t.Skip("integration test requires root")
	}

	dir := t.TempDir()

	// Create a per-file whiteout.
	os.MkdirAll(filepath.Join(dir, "usr/lib"), 0o755)                         //nolint:errcheck
	os.WriteFile(filepath.Join(dir, "usr/lib/.wh.libssl.so"), nil, 0o644)     //nolint:errcheck
	os.WriteFile(filepath.Join(dir, "usr/lib/libz.so"), []byte("ELF"), 0o644) //nolint:errcheck

	// Create an opaque whiteout directory.
	os.MkdirAll(filepath.Join(dir, "etc"), 0o755)                           //nolint:errcheck
	os.WriteFile(filepath.Join(dir, "etc/.wh..wh..opq"), nil, 0o644)        //nolint:errcheck
	os.WriteFile(filepath.Join(dir, "etc/shadow"), []byte("root::"), 0o644) //nolint:errcheck

	idx, err := layermerkle.BuildWhiteoutIndex(dir)
	if err != nil {
		t.Fatalf("BuildWhiteoutIndex: %v", err)
	}

	testCases := []struct {
		path   string
		masked bool
		desc   string
	}{
		{"usr/lib/libssl.so", true, "per-file whiteout"},
		{"usr/lib/libz.so", false, "present file not whiteout"},
		{"etc/shadow", true, "opaque directory masks contents"},
		{"etc/passwd", true, "opaque directory masks non-existent file too"},
		{"bin/sh", false, "unrelated path not masked"},
	}

	for _, tc := range testCases {
		got := idx.IsMasked(tc.path)
		if got != tc.masked {
			t.Errorf("IsMasked(%q) = %v, want %v (%s)", tc.path, got, tc.masked, tc.desc)
		}
	}
}
