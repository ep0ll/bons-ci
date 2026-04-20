package overlay_test

import (
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/bons/bons-ci/plugins/checksummer/pkg/overlay"
)

// ─────────────────────────── test mountinfo content ──────────────────────────

const sampleMountInfo = `22 1 8:1 / / rw,relatime shared:1 - ext4 /dev/sda1 rw
23 22 0:20 / /proc rw,nosuid shared:12 - proc proc rw
24 22 0:21 / /sys rw,nosuid shared:13 - sysfs sysfs rw
100 22 0:50 / /overlay1 rw,relatime shared:50 - overlay overlay rw,lowerdir=/lower1:/lower2,upperdir=/upper1,workdir=/work1
101 22 0:51 / /overlay2 rw,relatime shared:51 - overlay overlay rw,lowerdir=/lower1:/lower3,upperdir=/upper2,workdir=/work2
`

func writeMountInfo(t *testing.T, content string) string {
	t.Helper()
	f, err := os.CreateTemp("", "mountinfo-*.txt")
	if err != nil {
		t.Fatalf("CreateTemp: %v", err)
	}
	t.Cleanup(func() { os.Remove(f.Name()) })
	if _, err := f.WriteString(content); err != nil {
		t.Fatalf("write: %v", err)
	}
	f.Close()
	return f.Name()
}

// ─────────────────────────── parser ──────────────────────────────────────────

func TestParseMountInfo(t *testing.T) {
	path := writeMountInfo(t, sampleMountInfo)
	mi := overlay.NewMountInfo(path, 0)

	if err := mi.Refresh(); err != nil {
		t.Fatalf("Refresh: %v", err)
	}
	entries := mi.Entries()
	if len(entries) != 5 {
		t.Errorf("expected 5 entries, got %d", len(entries))
	}
}

func TestOverlayMounts(t *testing.T) {
	path := writeMountInfo(t, sampleMountInfo)
	mi := overlay.NewMountInfo(path, 0)

	mounts, err := mi.OverlayMounts()
	if err != nil {
		t.Fatalf("OverlayMounts: %v", err)
	}
	if len(mounts) != 2 {
		t.Errorf("expected 2 overlay mounts, got %d", len(mounts))
	}
}

func TestOverlayLowerDirs(t *testing.T) {
	path := writeMountInfo(t, sampleMountInfo)
	mi := overlay.NewMountInfo(path, 0)

	mounts, _ := mi.OverlayMounts()
	for _, m := range mounts {
		if !m.IsOverlay() {
			t.Errorf("expected overlay, got %s", m.FSType)
		}
		if len(m.LowerDirs) < 1 {
			t.Errorf("mount %s: expected at least 1 lowerdir", m.MountPoint)
		}
		if m.UpperDir == "" {
			t.Errorf("mount %s: expected upperdir", m.MountPoint)
		}
		if m.WorkDir == "" {
			t.Errorf("mount %s: expected workdir", m.MountPoint)
		}
	}
}

func TestOverlay1LowerDirs(t *testing.T) {
	path := writeMountInfo(t, sampleMountInfo)
	mi := overlay.NewMountInfo(path, 0)

	e, ok, err := mi.ForMountPoint("/overlay1")
	if err != nil || !ok {
		t.Fatalf("ForMountPoint: %v, ok=%v", err, ok)
	}
	if len(e.LowerDirs) != 2 {
		t.Errorf("expected 2 lowerdirs, got %v", e.LowerDirs)
	}
	if e.LowerDirs[0] != "/lower1" || e.LowerDirs[1] != "/lower2" {
		t.Errorf("unexpected lowerdirs: %v", e.LowerDirs)
	}
	if e.UpperDir != "/upper1" {
		t.Errorf("unexpected upperdir: %s", e.UpperDir)
	}
}

func TestSharedLowerDir(t *testing.T) {
	// Both overlay1 and overlay2 share /lower1.
	path := writeMountInfo(t, sampleMountInfo)
	mi := overlay.NewMountInfo(path, 0)

	e1, _, _ := mi.ForMountPoint("/overlay1")
	e2, _, _ := mi.ForMountPoint("/overlay2")

	shared := false
	for _, l1 := range e1.LowerDirs {
		for _, l2 := range e2.LowerDirs {
			if l1 == l2 {
				shared = true
			}
		}
	}
	if !shared {
		t.Error("expected overlay1 and overlay2 to share at least one lowerdir")
	}
}

func TestForMountPointMiss(t *testing.T) {
	path := writeMountInfo(t, sampleMountInfo)
	mi := overlay.NewMountInfo(path, 0)

	_, ok, err := mi.ForMountPoint("/nonexistent")
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if ok {
		t.Error("expected not found for nonexistent mount point")
	}
}

func TestContainingMount(t *testing.T) {
	path := writeMountInfo(t, sampleMountInfo)
	mi := overlay.NewMountInfo(path, 0)

	m, err := mi.ContainingMount("/overlay1/some/file.txt")
	if err != nil {
		t.Fatalf("ContainingMount: %v", err)
	}
	if m.MountPoint != "/overlay1" {
		t.Errorf("expected /overlay1, got %s", m.MountPoint)
	}
}

func TestContainingMountRoot(t *testing.T) {
	path := writeMountInfo(t, sampleMountInfo)
	mi := overlay.NewMountInfo(path, 0)

	m, err := mi.ContainingMount("/proc/self/status")
	if err != nil {
		t.Fatalf("ContainingMount /proc: %v", err)
	}
	if m.MountPoint != "/proc" {
		t.Errorf("expected /proc, got %s", m.MountPoint)
	}
}

func TestAllLayers(t *testing.T) {
	path := writeMountInfo(t, sampleMountInfo)
	mi := overlay.NewMountInfo(path, 0)

	e, ok, _ := mi.ForMountPoint("/overlay1")
	if !ok {
		t.Fatal("overlay1 not found")
	}
	layers := e.AllLayers()
	// upper + 2 lowers = 3
	if len(layers) != 3 {
		t.Errorf("expected 3 layers, got %d: %v", len(layers), layers)
	}
	// upper must be first
	if layers[0] != "/upper1" {
		t.Errorf("first layer should be upperdir, got %s", layers[0])
	}
}

func TestCaching(t *testing.T) {
	path := writeMountInfo(t, sampleMountInfo)
	mi := overlay.NewMountInfo(path, 5*time.Second)

	// Force first load.
	_, _ = mi.OverlayMounts()

	// Overwrite the file – cached result should still be used within TTL.
	if err := os.WriteFile(path, []byte(""), 0644); err != nil {
		t.Fatalf("write: %v", err)
	}
	mounts, _ := mi.OverlayMounts()
	if len(mounts) != 2 {
		t.Errorf("expected cached 2 mounts, got %d", len(mounts))
	}
}

// ─────────────────────────── BackingResolver ─────────────────────────────────

func TestBackingResolverNoOverlay(t *testing.T) {
	path := writeMountInfo(t, sampleMountInfo)
	mi := overlay.NewMountInfo(path, 0)
	r := overlay.NewBackingResolver(mi)

	// A path that is not in any overlay mount.
	backing, inOverlay, err := r.BackingPath("/tmp/regular/file.txt")
	if err != nil {
		t.Fatalf("BackingPath: %v", err)
	}
	if inOverlay {
		t.Error("regular path should not be flagged as overlay")
	}
	if backing != "/tmp/regular/file.txt" {
		t.Errorf("expected original path, got %s", backing)
	}
}

func TestBackingResolverFindsInLayer(t *testing.T) {
	// Create real temporary directories and a file in a lower dir.
	tmpDir := t.TempDir()
	upper := filepath.Join(tmpDir, "upper")
	lower1 := filepath.Join(tmpDir, "lower1")
	lower2 := filepath.Join(tmpDir, "lower2")
	work := filepath.Join(tmpDir, "work")
	merged := filepath.Join(tmpDir, "merged")

	for _, d := range []string{upper, lower1, lower2, work, merged} {
		if err := os.MkdirAll(d, 0755); err != nil {
			t.Fatalf("MkdirAll %s: %v", d, err)
		}
	}

	// Place a file in lower2.
	testFile := filepath.Join(lower2, "data.bin")
	if err := os.WriteFile(testFile, []byte("test content"), 0644); err != nil {
		t.Fatalf("WriteFile: %v", err)
	}

	// Build a synthetic mountinfo pointing to our temp dirs.
	content := "1 0 8:1 / / rw - ext4 /dev/sda1 rw\n" +
		"100 1 0:99 / " + merged + " rw - overlay overlay " +
		"rw,lowerdir=" + lower1 + ":" + lower2 + "," +
		"upperdir=" + upper + ",workdir=" + work + "\n"

	miPath := writeMountInfo(t, content)
	mi := overlay.NewMountInfo(miPath, 0)
	r := overlay.NewBackingResolver(mi)

	backing, inOverlay, err := r.BackingPath(filepath.Join(merged, "data.bin"))
	if err != nil {
		t.Fatalf("BackingPath: %v", err)
	}
	if !inOverlay {
		t.Error("expected inOverlay=true")
	}
	if backing != testFile {
		t.Errorf("expected %s, got %s", testFile, backing)
	}
}

func TestLayersForMount(t *testing.T) {
	path := writeMountInfo(t, sampleMountInfo)
	mi := overlay.NewMountInfo(path, 0)
	r := overlay.NewBackingResolver(mi)

	upper, lowers, err := r.LayersForMount("/overlay1")
	if err != nil {
		t.Fatalf("LayersForMount: %v", err)
	}
	if upper != "/upper1" {
		t.Errorf("expected /upper1, got %s", upper)
	}
	if len(lowers) != 2 {
		t.Errorf("expected 2 lowers, got %v", lowers)
	}
}
