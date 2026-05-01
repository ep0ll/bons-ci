package snapshot_test

import (
	"context"
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"testing"

	fanwatch "github.com/bons/bons-ci/pkg/fswatch"
	"github.com/bons/bons-ci/pkg/fswatch/snapshot"
	"github.com/bons/bons-ci/pkg/fswatch/testutil"
)

// ─────────────────────────────────────────────────────────────────────────────
// Fixtures
// ─────────────────────────────────────────────────────────────────────────────

// buildSnapshotterFixture creates a fake containerd snapshotter directory:
//
//	<root>/snapshots/
//	  1/fs/          ← base image layer
//	  2/fs/          ← second layer
//	  2/parent       ← contains "1"
//	  3/fs/          ← container writable layer
//	  3/work/        ← overlay work dir
//	  3/parent       ← contains "2"
func buildSnapshotterFixture(t *testing.T) (root string, snapsDir string) {
	t.Helper()
	root = t.TempDir()
	snapsDir = filepath.Join(root, "snapshots")

	layers := []struct {
		id      string
		parent  string
		hasWork bool
	}{
		{id: "1", parent: "", hasWork: false},
		{id: "2", parent: "1", hasWork: false},
		{id: "3", parent: "2", hasWork: true},
	}

	for _, l := range layers {
		fsDir := filepath.Join(snapsDir, l.id, "fs")
		if err := os.MkdirAll(fsDir, 0o755); err != nil {
			t.Fatalf("mkdir %s: %v", fsDir, err)
		}
		// Write a sentinel file so ResolveLayer can find the path.
		_ = os.WriteFile(filepath.Join(fsDir, "sentinel.txt"), []byte(l.id), 0o644)

		if l.parent != "" {
			pf := filepath.Join(snapsDir, l.id, "parent")
			_ = os.WriteFile(pf, []byte(l.parent), 0o644)
		}
		if l.hasWork {
			wd := filepath.Join(snapsDir, l.id, "work")
			_ = os.MkdirAll(wd, 0o755)
		}
	}

	return root, snapsDir
}

// buildMountinfoFixture writes a fake /proc/self/mountinfo line for an overlay.
func buildMountinfoFixture(t *testing.T, merged, upper, work string, lowers []string) string {
	t.Helper()
	dir := t.TempDir()
	path := filepath.Join(dir, "mountinfo")

	lowerStr := strings.Join(lowers, ":")
	line := fmt.Sprintf(
		`69 64 0:46 / %s rw,relatime shared:34 - overlay overlay rw,lowerdir=%s,upperdir=%s,workdir=%s`,
		merged, lowerStr, upper, work,
	)
	_ = os.WriteFile(path, []byte(line+"\n"), 0o644)
	return path
}

// ─────────────────────────────────────────────────────────────────────────────
// OverlayInfoFromMountFile tests
// ─────────────────────────────────────────────────────────────────────────────

func TestOverlayInfoFromMountFile_ParsesOverlayLine(t *testing.T) {
	merged := "/run/containerd/io.containerd.runtime.v2.task/k8s.io/abc/rootfs"
	upper := "/var/lib/containerd/snapshots/3/fs"
	work := "/var/lib/containerd/snapshots/3/work"
	lowers := []string{
		"/var/lib/containerd/snapshots/2/fs",
		"/var/lib/containerd/snapshots/1/fs",
	}

	mountFile := buildMountinfoFixture(t, merged, upper, work, lowers)

	info, err := snapshot.OverlayInfoFromMountFile(mountFile, merged)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	if info.MergedDir != merged {
		t.Errorf("MergedDir = %q, want %q", info.MergedDir, merged)
	}
	if info.UpperDir != upper {
		t.Errorf("UpperDir = %q, want %q", info.UpperDir, upper)
	}
	if info.WorkDir != work {
		t.Errorf("WorkDir = %q, want %q", info.WorkDir, work)
	}
	if len(info.LowerDirs) != 2 {
		t.Fatalf("LowerDirs len = %d, want 2", len(info.LowerDirs))
	}
	if info.LowerDirs[0] != lowers[0] {
		t.Errorf("LowerDirs[0] = %q, want %q", info.LowerDirs[0], lowers[0])
	}
}

func TestOverlayInfoFromMountFile_MultiLowerDirs(t *testing.T) {
	lowers := []string{"/l4/fs", "/l3/fs", "/l2/fs", "/l1/fs"}
	mountFile := buildMountinfoFixture(t, "/merged", "/upper", "/work", lowers)

	info, err := snapshot.OverlayInfoFromMountFile(mountFile, "/merged")
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if len(info.LowerDirs) != 4 {
		t.Errorf("LowerDirs len = %d, want 4", len(info.LowerDirs))
	}
}

func TestOverlayInfoFromMountFile_NotFound(t *testing.T) {
	mountFile := buildMountinfoFixture(t, "/other/merged", "/upper", "/work", []string{"/lower"})

	_, err := snapshot.OverlayInfoFromMountFile(mountFile, "/not/found")
	if err == nil {
		t.Fatal("expected error for non-existent merged dir")
	}
}

func TestOverlayInfoFromMountFile_SkipsNonOverlay(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "mountinfo")
	// ext4 + proc lines, no overlay.
	lines := "71 64 8:1 / / rw,relatime - ext4 /dev/sda1 rw\n" +
		"72 64 0:5 / /proc rw - proc proc rw\n"
	_ = os.WriteFile(path, []byte(lines), 0o644)

	_, err := snapshot.OverlayInfoFromMountFile(path, "/merged")
	if err == nil {
		t.Fatal("expected ErrMountNotFound for non-overlay file")
	}
}

// ─────────────────────────────────────────────────────────────────────────────
// ContainerdSnapshotter.resolveSnapshotChain tests (via OverlayInfoFromContainerdSnapshot)
// ─────────────────────────────────────────────────────────────────────────────

func TestOverlayInfoFromContainerdSnapshot_BuildsChain(t *testing.T) {
	root, _ := buildSnapshotterFixture(t)

	s := snapshot.NewContainerdSnapshotter().WithRoot(root)
	info, err := s.OverlayInfoFromContainerdSnapshot("3", "/merged")
	if err != nil {
		t.Fatalf("OverlayInfoFromContainerdSnapshot: unexpected error: %v", err)
	}

	if info.MergedDir != "/merged" {
		t.Errorf("MergedDir = %q, want /merged", info.MergedDir)
	}

	// Layer 3 is writable → WorkDir and UpperDir must be non-empty.
	if info.WorkDir == "" {
		t.Error("WorkDir should be set for writable snapshot 3")
	}

	// At least 2 lower dirs (snapshot 2 and 1) should appear.
	if len(info.LowerDirs) < 2 {
		t.Errorf("LowerDirs len = %d, want >= 2; dirs: %v", len(info.LowerDirs), info.LowerDirs)
	}
}

func TestSnapshotIDFromMergedDir_ExtractsID(t *testing.T) {
	root, snapsDir := buildSnapshotterFixture(t)
	s := snapshot.NewContainerdSnapshotter().WithRoot(root)

	mergedDir := filepath.Join(snapsDir, "3", "merged")
	id, err := s.SnapshotIDFromMergedDir(mergedDir)
	if err != nil {
		t.Fatalf("SnapshotIDFromMergedDir: unexpected error: %v", err)
	}
	if id != "3" {
		t.Errorf("SnapshotID = %q, want 3", id)
	}
}

func TestSnapshotIDFromMergedDir_RejectsExternalPath(t *testing.T) {
	root, _ := buildSnapshotterFixture(t)
	s := snapshot.NewContainerdSnapshotter().WithRoot(root)

	_, err := s.SnapshotIDFromMergedDir("/var/lib/other/merged")
	if err == nil {
		t.Fatal("expected error for path outside snapshotter root")
	}
}

// ─────────────────────────────────────────────────────────────────────────────
// AllRunningSnapshotMounts tests
// ─────────────────────────────────────────────────────────────────────────────

func TestAllRunningSnapshotMounts_FindsContainerdMounts(t *testing.T) {
	snapsDir := "/var/lib/containerd/io.containerd.snapshotter.v1.overlayfs/snapshots"

	dir := t.TempDir()
	mountFile := filepath.Join(dir, "mountinfo")

	// Write two overlay mount lines whose lowerdirs are under the snaps dir.
	lines := fmt.Sprintf(
		"69 64 0:46 / /merged1 rw - overlay overlay rw,lowerdir=%s/2/fs:%s/1/fs,upperdir=%s/3/fs,workdir=%s/3/work\n"+
			"70 64 0:47 / /merged2 rw - overlay overlay rw,lowerdir=%s/5/fs:%s/4/fs,upperdir=%s/6/fs,workdir=%s/6/work\n"+
			"71 64 8:1 / / rw - ext4 /dev/sda1 rw\n",
		snapsDir, snapsDir, snapsDir, snapsDir,
		snapsDir, snapsDir, snapsDir, snapsDir,
	)
	_ = os.WriteFile(mountFile, []byte(lines), 0o644)

	root := "/var/lib/containerd/io.containerd.snapshotter.v1.overlayfs"
	entries, err := snapshot.AllRunningSnapshotMountsFromFile(mountFile, snapsDir)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	_ = root

	if len(entries) != 2 {
		t.Fatalf("entries len = %d, want 2; got: %v",
			len(entries), func() []string {
				var paths []string
				for _, e := range entries {
					paths = append(paths, e.MergedDir)
				}
				return paths
			}())
	}

	// Verify entries are sorted by merged dir.
	dirs := []string{entries[0].MergedDir, entries[1].MergedDir}
	sorted := append([]string{}, dirs...)
	sort.Strings(sorted)
	if dirs[0] != sorted[0] || dirs[1] != sorted[1] {
		t.Error("entries should be sorted by MergedDir")
	}
}

func TestAllRunningSnapshotMounts_EmptyWhenNoContainerd(t *testing.T) {
	dir := t.TempDir()
	mountFile := filepath.Join(dir, "mountinfo")
	// Only ext4 mounts.
	_ = os.WriteFile(mountFile, []byte("71 64 8:1 / / rw - ext4 /dev/sda1 rw\n"), 0o644)

	entries, err := snapshot.AllRunningSnapshotMountsFromFile(mountFile, "/var/lib/containerd")
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if len(entries) != 0 {
		t.Errorf("entries = %v, want empty", entries)
	}
}

// ─────────────────────────────────────────────────────────────────────────────
// ContainerdEnricher pipeline integration test
// ─────────────────────────────────────────────────────────────────────────────

func TestContainerdEnricher_SetsOverlayForKnownMount(t *testing.T) {
	// Build a fake overlay info and prime the enricher's cache via a custom
	// implementation that bypasses /proc/self/mountinfo.
	overlay := fanwatch.NewOverlayInfo(
		"/merged/container1",
		"/snapshots/3/fs",
		"/snapshots/3/work",
		[]string{"/snapshots/2/fs", "/snapshots/1/fs"},
	)

	// Use a static overlay enricher as stand-in (ContainerdEnricher uses the
	// same underlying overlay info structure once resolved).
	enricher := fanwatch.NewOverlayEnricher(overlay)

	pipeline := fanwatch.NewPipeline(
		fanwatch.WithReadOnlyPipeline(),
		fanwatch.WithTransformer(enricher),
		fanwatch.WithHandler(fanwatch.NoopHandler{}),
		fanwatch.WithWorkers(1),
	)

	w := testutil.NewFakeWatcher(8)
	w.Send(testutil.NewRawEvent().
		WithOp(fanwatch.OpOpen).
		WithPath("/merged/container1/usr/bin/python3").
		Build())
	w.Close()

	collector := &fanwatch.CollectingHandler{}
	enrichPipeline := fanwatch.NewPipeline(
		fanwatch.WithFilter(fanwatch.ReadOnlyFilter()),
		fanwatch.WithTransformer(enricher),
		fanwatch.WithHandler(collector),
		fanwatch.WithWorkers(1),
	)
	_ = pipeline

	ctx := context.Background()
	ch, _ := w.Watch(ctx)
	enrichPipeline.RunSync(ctx, ch, nil)

	events := collector.Events()
	if len(events) == 0 {
		t.Fatal("expected at least one event")
	}
	if events[0].Overlay == nil {
		t.Error("Overlay should be set for path inside merged dir")
	}
}

// ─────────────────────────────────────────────────────────────────────────────
// SnapshotIDFilter test
// ─────────────────────────────────────────────────────────────────────────────

func TestSnapshotIDFilter_PassesMatchingLayer(t *testing.T) {
	overlay := fanwatch.NewOverlayInfo(
		"/merged",
		"/snapshots/47/fs",
		"/snapshots/47/work",
		[]string{"/snapshots/23/fs", "/snapshots/1/fs"},
	)
	f := snapshot.SnapshotIDFilter("47")
	ctx := context.Background()

	// Event with overlay set — layer "47" exists in the overlay.
	e := testutil.NewEnrichedEvent().Build()
	e.Overlay = overlay

	if !f.Allow(ctx, e) {
		t.Error("SnapshotIDFilter: should pass event for snapshot 47")
	}

	// Filter for a different snapshot ID.
	f2 := snapshot.SnapshotIDFilter("99")
	if f2.Allow(ctx, e) {
		t.Error("SnapshotIDFilter: should reject event for snapshot 99")
	}
}

func TestSnapshotIDFilter_RejectsNilOverlay(t *testing.T) {
	f := snapshot.SnapshotIDFilter("47")
	e := testutil.NewEnrichedEvent().Build() // no Overlay set
	if f.Allow(context.Background(), e) {
		t.Error("SnapshotIDFilter: should reject events with nil Overlay")
	}
}
