package snapshot_test

import (
	"strings"
	"context"
	"fmt"
	"testing"
	"time"

	ctdmount "github.com/containerd/containerd/mount"
	"github.com/containerd/containerd/snapshots"

	fswatch "github.com/bons/bons-ci/pkg/fswatch"
	"github.com/bons/bons-ci/pkg/fswatch/snapshot"
	"github.com/bons/bons-ci/pkg/fswatch/testutil"
)

// ─────────────────────────────────────────────────────────────────────────────
// Fake Snapshotter — implements snapshots.Snapshotter without a real daemon
// ─────────────────────────────────────────────────────────────────────────────

// fakeSnapshotter is an in-memory snapshots.Snapshotter that returns
// pre-configured mounts and info records. Enables testing without containerd.
type fakeSnapshotter struct {
	// mounts maps snapshot key → []mount.Mount to return from Mounts().
	mounts map[string][]ctdmount.Mount
	// infos maps snapshot key → snapshots.Info to return from Stat().
	infos map[string]snapshots.Info
}

func (f *fakeSnapshotter) Stat(_ context.Context, key string) (snapshots.Info, error) {
	if info, ok := f.infos[key]; ok {
		return info, nil
	}
	return snapshots.Info{}, fmt.Errorf("snapshot not found: %q", key)
}

func (f *fakeSnapshotter) Update(_ context.Context, info snapshots.Info, _ ...string) (snapshots.Info, error) {
	return info, nil
}

func (f *fakeSnapshotter) Usage(_ context.Context, key string) (snapshots.Usage, error) {
	return snapshots.Usage{}, nil
}

func (f *fakeSnapshotter) Mounts(_ context.Context, key string) ([]ctdmount.Mount, error) {
	if mounts, ok := f.mounts[key]; ok {
		return mounts, nil
	}
	return nil, fmt.Errorf("snapshot not found: %q", key)
}

func (f *fakeSnapshotter) Prepare(_ context.Context, key, parent string, _ ...snapshots.Opt) ([]ctdmount.Mount, error) {
	return f.mounts[key], nil
}

func (f *fakeSnapshotter) View(_ context.Context, key, parent string, _ ...snapshots.Opt) ([]ctdmount.Mount, error) {
	return f.mounts[key], nil
}

func (f *fakeSnapshotter) Commit(_ context.Context, name, key string, _ ...snapshots.Opt) error {
	return nil
}

func (f *fakeSnapshotter) Remove(_ context.Context, key string) error {
	delete(f.mounts, key)
	delete(f.infos, key)
	return nil
}

func (f *fakeSnapshotter) Walk(_ context.Context, fn snapshots.WalkFunc, _ ...string) error {
	for _, info := range f.infos {
		if err := fn(context.Background(), info); err != nil {
			return err
		}
	}
	return nil
}

func (f *fakeSnapshotter) Close() error { return nil }

// buildFakeSnapshotter returns a pre-configured fake with a 3-layer chain:
//
//	snapshot "container-1" (active, writable)
//	  lowerdir = /snapshots/sha256-layer2/fs : /snapshots/sha256-layer1/fs
//	  upperdir = /snapshots/sha256-active/fs
//	  workdir  = /snapshots/sha256-active/work
func buildFakeSnapshotter() *fakeSnapshotter {
	activeKey := "sha256:abc123"
	layer2Key := "sha256:layer2"
	layer1Key := "sha256:layer1"

	activeMounts := []ctdmount.Mount{
		{
			Type:   "overlay",
			Source: "overlay",
			Options: []string{
				"workdir=/snapshots/sha256-active/work",
				"upperdir=/snapshots/sha256-active/fs",
				"lowerdir=/snapshots/sha256-layer2/fs:/snapshots/sha256-layer1/fs",
			},
		},
	}

	// Single-layer (read-only) view — containerd produces a bind mount.
	layer2Mounts := []ctdmount.Mount{
		{
			Type:    "bind",
			Source:  "/snapshots/sha256-layer2/fs",
			Options: []string{"ro", "rbind"},
		},
	}

	now := time.Now()
	return &fakeSnapshotter{
		mounts: map[string][]ctdmount.Mount{
			activeKey: activeMounts,
			layer2Key: layer2Mounts,
		},
		infos: map[string]snapshots.Info{
			activeKey: {
				Kind:    snapshots.KindActive,
				Name:    activeKey,
				Parent:  layer2Key,
				Created: now,
				Updated: now,
				Labels:  map[string]string{"containerd.io/gc.root": "true"},
			},
			layer2Key: {
				Kind:    snapshots.KindCommitted,
				Name:    layer2Key,
				Parent:  layer1Key,
				Created: now,
				Updated: now,
			},
			layer1Key: {
				Kind:    snapshots.KindCommitted,
				Name:    layer1Key,
				Parent:  "",
				Created: now,
				Updated: now,
			},
		},
	}
}

// ─────────────────────────────────────────────────────────────────────────────
// OverlayInfoFromSnapshotter tests
// ─────────────────────────────────────────────────────────────────────────────

func TestOverlayInfoFromSnapshotter_ActiveSnapshot(t *testing.T) {
	sn := buildFakeSnapshotter()
	ctx := context.Background()

	info, err := snapshot.OverlayInfoFromSnapshotter(ctx, sn, "sha256:abc123", "/merged")
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	if info.MergedDir != "/merged" {
		t.Errorf("MergedDir = %q, want /merged", info.MergedDir)
	}
	if info.UpperDir != "/snapshots/sha256-active/fs" {
		t.Errorf("UpperDir = %q, want /snapshots/sha256-active/fs", info.UpperDir)
	}
	if info.WorkDir != "/snapshots/sha256-active/work" {
		t.Errorf("WorkDir = %q, want /snapshots/sha256-active/work", info.WorkDir)
	}
	if len(info.LowerDirs) != 2 {
		t.Fatalf("LowerDirs len = %d, want 2", len(info.LowerDirs))
	}
	if info.LowerDirs[0] != "/snapshots/sha256-layer2/fs" {
		t.Errorf("LowerDirs[0] = %q", info.LowerDirs[0])
	}
	if info.LowerDirs[1] != "/snapshots/sha256-layer1/fs" {
		t.Errorf("LowerDirs[1] = %q", info.LowerDirs[1])
	}
}

func TestOverlayInfoFromSnapshotter_BindMount_SingleLayer(t *testing.T) {
	sn := buildFakeSnapshotter()
	ctx := context.Background()

	info, err := snapshot.OverlayInfoFromSnapshotter(ctx, sn, "sha256:layer2", "/merged-ro")
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	// Bind mount: no upperdir/workdir, one lowerdir = bind source.
	if info.UpperDir != "" {
		t.Errorf("UpperDir = %q, want empty for bind mount", info.UpperDir)
	}
	if len(info.LowerDirs) != 1 {
		t.Fatalf("LowerDirs len = %d, want 1", len(info.LowerDirs))
	}
	if info.LowerDirs[0] != "/snapshots/sha256-layer2/fs" {
		t.Errorf("LowerDirs[0] = %q", info.LowerDirs[0])
	}
}

func TestOverlayInfoFromSnapshotter_NotFoundKey(t *testing.T) {
	sn := buildFakeSnapshotter()
	_, err := snapshot.OverlayInfoFromSnapshotter(context.Background(), sn, "sha256:nonexistent", "/merged")
	if err == nil {
		t.Fatal("expected error for non-existent snapshot key")
	}
}

// ─────────────────────────────────────────────────────────────────────────────
// MountsToOverlayInfo tests
// ─────────────────────────────────────────────────────────────────────────────

func TestMountsToOverlayInfo_OverlayType(t *testing.T) {
	mounts := []ctdmount.Mount{
		{
			Type:   "overlay",
			Source: "overlay",
			Options: []string{
				"workdir=/work",
				"upperdir=/upper",
				"lowerdir=/lower2:/lower1",
			},
		},
	}

	info, err := snapshot.MountsToOverlayInfo(mounts, "/merged")
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	if info.UpperDir != "/upper" {
		t.Errorf("UpperDir = %q, want /upper", info.UpperDir)
	}
	if info.WorkDir != "/work" {
		t.Errorf("WorkDir = %q, want /work", info.WorkDir)
	}
	if len(info.LowerDirs) != 2 {
		t.Fatalf("LowerDirs len = %d, want 2", len(info.LowerDirs))
	}
}

func TestMountsToOverlayInfo_BindType(t *testing.T) {
	mounts := []ctdmount.Mount{
		{Type: "bind", Source: "/lower", Options: []string{"ro", "rbind"}},
	}

	info, err := snapshot.MountsToOverlayInfo(mounts, "/merged")
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	if len(info.LowerDirs) != 1 || info.LowerDirs[0] != "/lower" {
		t.Errorf("LowerDirs = %v, want [/lower]", info.LowerDirs)
	}
}

func TestMountsToOverlayInfo_EmptyMounts_Error(t *testing.T) {
	_, err := snapshot.MountsToOverlayInfo(nil, "/merged")
	if err == nil {
		t.Fatal("expected error for empty mounts")
	}
}

func TestMountsToOverlayInfo_MissingLowerdir_Error(t *testing.T) {
	mounts := []ctdmount.Mount{
		{Type: "overlay", Source: "overlay", Options: []string{"upperdir=/u"}},
	}
	_, err := snapshot.MountsToOverlayInfo(mounts, "/merged")
	if err == nil {
		t.Fatal("expected error when lowerdir is missing")
	}
}

// ─────────────────────────────────────────────────────────────────────────────
// SnapshotAncestors tests
// ─────────────────────────────────────────────────────────────────────────────

func TestSnapshotAncestors_FullChain(t *testing.T) {
	sn := buildFakeSnapshotter()
	ctx := context.Background()

	chain, err := snapshot.SnapshotAncestors(ctx, sn, "sha256:abc123")
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	// Expect: abc123 → layer2 → layer1 (3 entries total, leaf first).
	if len(chain) != 3 {
		t.Fatalf("chain len = %d, want 3; chain: %v",
			len(chain), func() []string {
				var names []string
				for _, c := range chain {
					names = append(names, c.Name)
				}
				return names
			}())
	}
	if chain[0].Name != "sha256:abc123" {
		t.Errorf("chain[0].Name = %q, want sha256:abc123", chain[0].Name)
	}
	if chain[1].Name != "sha256:layer2" {
		t.Errorf("chain[1].Name = %q, want sha256:layer2", chain[1].Name)
	}
	if chain[2].Name != "sha256:layer1" {
		t.Errorf("chain[2].Name = %q, want sha256:layer1", chain[2].Name)
	}
}

func TestSnapshotAncestors_BaseLayer(t *testing.T) {
	sn := buildFakeSnapshotter()
	chain, err := snapshot.SnapshotAncestors(context.Background(), sn, "sha256:layer1")
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if len(chain) != 1 {
		t.Errorf("chain len = %d, want 1 (base layer has no parents)", len(chain))
	}
}

func TestSnapshotAncestors_NotFound(t *testing.T) {
	sn := buildFakeSnapshotter()
	_, err := snapshot.SnapshotAncestors(context.Background(), sn, "sha256:gone")
	if err == nil {
		t.Fatal("expected error for nonexistent key")
	}
}

// ─────────────────────────────────────────────────────────────────────────────
// SnapshotInfoTransformer tests
// ─────────────────────────────────────────────────────────────────────────────

func TestSnapshotInfoTransformer_AttachesLabels(t *testing.T) {
	sn := buildFakeSnapshotter()

	keyFn := func(e *fswatch.EnrichedEvent) string {
		// Return the active snapshot key for any event.
		return "sha256:abc123"
	}

	tr := snapshot.SnapshotInfoTransformer(sn, keyFn)
	e := testutil.NewEnrichedEvent().Build()

	if err := tr.Transform(context.Background(), e); err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	if e.Attr("containerd.snapshot.name") != "sha256:abc123" {
		t.Errorf("name attr = %v", e.Attr("containerd.snapshot.name"))
	}
	kindAttr, _ := e.Attr("containerd.snapshot.kind").(string)
	if !strings.EqualFold(kindAttr, "active") {
		t.Errorf("kind attr = %v", e.Attr("containerd.snapshot.kind"))
	}
	if e.Attr("containerd.snapshot.parent") != "sha256:layer2" {
		t.Errorf("parent attr = %v", e.Attr("containerd.snapshot.parent"))
	}
	labels, ok := e.Attr("containerd.snapshot.labels").(map[string]string)
	if !ok {
		t.Fatal("containerd.snapshot.labels attr is not map[string]string")
	}
	if labels["containerd.io/gc.root"] != "true" {
		t.Errorf("gc.root label = %v", labels["containerd.io/gc.root"])
	}
}

func TestSnapshotInfoTransformer_SkipsEmptyKey(t *testing.T) {
	sn := buildFakeSnapshotter()
	tr := snapshot.SnapshotInfoTransformer(sn, func(_ *fswatch.EnrichedEvent) string { return "" })
	e := testutil.NewEnrichedEvent().Build()

	if err := tr.Transform(context.Background(), e); err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if e.Attr("containerd.snapshot.name") != nil {
		t.Error("should not set attrs when key is empty")
	}
}

// ─────────────────────────────────────────────────────────────────────────────
// MountEnricher tests
// ─────────────────────────────────────────────────────────────────────────────

func TestMountEnricher_SetsOverlayForPathInside(t *testing.T) {
	mounts := []ctdmount.Mount{
		{
			Type:   "overlay",
			Source: "overlay",
			Options: []string{
				"workdir=/work",
				"upperdir=/upper",
				"lowerdir=/lower",
			},
		},
	}

	enricher, err := snapshot.NewMountEnricher(mounts, "/merged")
	if err != nil {
		t.Fatalf("NewMountEnricher: %v", err)
	}

	e := testutil.NewEnrichedEvent().WithPath("/merged/usr/bin/python3").Build()
	if err := enricher.Transform(context.Background(), e); err != nil {
		t.Fatalf("Transform: %v", err)
	}

	if e.Overlay == nil {
		t.Fatal("Overlay should be set for path inside mergedDir")
	}
	if e.Overlay.UpperDir != "/upper" {
		t.Errorf("UpperDir = %q, want /upper", e.Overlay.UpperDir)
	}
}

func TestMountEnricher_SkipsPathOutsideMergedDir(t *testing.T) {
	mounts := []ctdmount.Mount{
		{
			Type:    "overlay",
			Source:  "overlay",
			Options: []string{"workdir=/work", "upperdir=/upper", "lowerdir=/lower"},
		},
	}

	enricher, err := snapshot.NewMountEnricher(mounts, "/merged")
	if err != nil {
		t.Fatalf("NewMountEnricher: %v", err)
	}

	e := testutil.NewEnrichedEvent().WithPath("/proc/1/maps").Build()
	if err := enricher.Transform(context.Background(), e); err != nil {
		t.Fatalf("Transform: %v", err)
	}

	if e.Overlay != nil {
		t.Error("Overlay should be nil for path outside mergedDir")
	}
}

// ─────────────────────────────────────────────────────────────────────────────
// SnapshotterEnricher tests
// ─────────────────────────────────────────────────────────────────────────────

func TestSnapshotterEnricher_ResolvesViaSnapshotter(t *testing.T) {
	sn := buildFakeSnapshotter()

	enricher := snapshot.NewSnapshotterEnricher(sn, "/merged", func(e *fswatch.EnrichedEvent) string {
		return "sha256:abc123"
	})

	e := testutil.NewEnrichedEvent().WithPath("/merged/usr/lib/libssl.so").Build()
	if err := enricher.Transform(context.Background(), e); err != nil {
		t.Fatalf("Transform: %v", err)
	}

	if e.Overlay == nil {
		t.Fatal("Overlay should be set after SnapshotterEnricher")
	}
	if e.Overlay.UpperDir != "/snapshots/sha256-active/fs" {
		t.Errorf("UpperDir = %q", e.Overlay.UpperDir)
	}
}

// ─────────────────────────────────────────────────────────────────────────────
// Snapshot-aware filter tests
// ─────────────────────────────────────────────────────────────────────────────

func TestSnapshotKeyFilter_PassesMatchingLayer(t *testing.T) {
	overlay := fswatch.NewOverlayInfo(
		"/merged",
		"/snapshots/sha256-active/fs",
		"/snapshots/sha256-active/work",
		[]string{"/snapshots/sha256-layer2/fs", "/snapshots/sha256-layer1/fs"},
	)

	f := snapshot.SnapshotKeyFilter("sha256-active")
	ctx := context.Background()

	e := testutil.NewEnrichedEvent().Build()
	e.Overlay = overlay

	if !f.Allow(ctx, e) {
		t.Error("SnapshotKeyFilter: should pass event matching sha256-active")
	}

	f2 := snapshot.SnapshotKeyFilter("sha256-other")
	if f2.Allow(ctx, e) {
		t.Error("SnapshotKeyFilter: should reject event not matching sha256-other")
	}
}

func TestSnapshotKeyFilter_RejectsNilOverlay(t *testing.T) {
	f := snapshot.SnapshotKeyFilter("sha256-active")
	e := testutil.NewEnrichedEvent().Build()
	if f.Allow(context.Background(), e) {
		t.Error("SnapshotKeyFilter: should reject events with nil Overlay")
	}
}

func TestMergedDirFilter_CorrectlyFilters(t *testing.T) {
	f := snapshot.MergedDirFilter("/merged/container1")
	ctx := context.Background()

	pass := testutil.NewEnrichedEvent().WithPath("/merged/container1/usr/bin/bash").Build()
	fail := testutil.NewEnrichedEvent().WithPath("/merged/container2/usr/bin/bash").Build()

	if !f.Allow(ctx, pass) {
		t.Error("MergedDirFilter: should pass path inside mergedDir")
	}
	if f.Allow(ctx, fail) {
		t.Error("MergedDirFilter: should reject path in different container")
	}
}

// ─────────────────────────────────────────────────────────────────────────────
// IsMountReadOnly tests
// ─────────────────────────────────────────────────────────────────────────────

func TestIsMountReadOnly(t *testing.T) {
	rw := ctdmount.Mount{Type: "overlay", Source: "overlay", Options: []string{"lowerdir=/l", "upperdir=/u", "workdir=/w"}}
	ro := ctdmount.Mount{Type: "bind", Source: "/l", Options: []string{"ro", "rbind"}}

	if snapshot.IsMountReadOnly(rw) {
		t.Error("overlay rw mount should not be read-only")
	}
	if !snapshot.IsMountReadOnly(ro) {
		t.Error("bind ro mount should be read-only")
	}
}

// ─────────────────────────────────────────────────────────────────────────────
// Pipeline integration: snapshot enricher → fanwatch pipeline
// ─────────────────────────────────────────────────────────────────────────────

func TestPipeline_WithSnapshotEnricher_EndToEnd(t *testing.T) {
	sn := buildFakeSnapshotter()

	// Build the overlay info from the snapshotter and create a MountEnricher.
	mounts, err := sn.Mounts(context.Background(), "sha256:abc123")
	if err != nil {
		t.Fatalf("Mounts: %v", err)
	}
	enricher, err := snapshot.NewMountEnricher(mounts, "/merged")
	if err != nil {
		t.Fatalf("NewMountEnricher: %v", err)
	}

	collector := &fswatch.CollectingHandler{}

	pipeline := fswatch.NewPipeline(
		fswatch.WithFilter(fswatch.ReadOnlyFilter()),
		fswatch.WithTransformer(enricher),
		fswatch.WithHandler(collector),
		fswatch.WithWorkers(1),
	)

	w := testutil.NewFakeWatcher(8)
	w.Send(testutil.NewRawEvent().
		WithOp(fswatch.OpOpen).
		WithPath("/merged/usr/bin/python3").
		Build())
	w.Send(testutil.NewRawEvent().
		WithOp(fswatch.OpOpen).
		WithPath("/proc/1/maps"). // outside merged dir
		Build())
	w.Close()

	ctx := context.Background()
	ch, _ := w.Watch(ctx)
	result := pipeline.RunSync(ctx, ch, nil)

	if result.Received != 2 {
		t.Errorf("Received = %d, want 2", result.Received)
	}
	if result.Handled != 2 {
		t.Errorf("Handled = %d, want 2", result.Handled)
	}

	events := collector.Events()
	// First event (inside merged) should have Overlay set.
	if events[0].Overlay == nil {
		t.Error("event[0] Overlay should be set (path inside merged dir)")
	}
	// Second event (/proc) should have nil Overlay.
	if events[1].Overlay != nil {
		t.Error("event[1] Overlay should be nil (path outside merged dir)")
	}
}

// TestSnapshotAncestors_CycleDetected verifies that a cycle in the parent chain
// is detected and returns an error rather than looping forever.
func TestSnapshotAncestors_CycleDetected(t *testing.T) {
	// Build a snapshotter where A → B → A (cycle).
	sn := &fakeSnapshotter{
		mounts: map[string][]ctdmount.Mount{},
		infos: map[string]snapshots.Info{
			"snap-a": {Kind: snapshots.KindCommitted, Name: "snap-a", Parent: "snap-b"},
			"snap-b": {Kind: snapshots.KindCommitted, Name: "snap-b", Parent: "snap-a"},
		},
	}

	_, err := snapshot.SnapshotAncestors(context.Background(), sn, "snap-a")
	if err == nil {
		t.Fatal("expected error for cyclic parent chain")
	}
	if !strings.Contains(err.Error(), "cycle") {
		t.Errorf("expected 'cycle' in error message, got: %v", err)
	}
}


