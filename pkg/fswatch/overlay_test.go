package fswatch_test

import (
	"os"
	"path/filepath"
	"testing"

	fswatch "github.com/bons/bons-ci/pkg/fswatch"
)

// ─────────────────────────────────────────────────────────────────────────────
// mountinfo parsing tests
// ─────────────────────────────────────────────────────────────────────────────

// mountinfoDockerLine is a realistic Docker overlay mount line from
// /proc/self/mountinfo. Fields from left to right:
//
//	mountID parentID major:minor root mountPoint mountOptions [optFields] - fsType source superOptions
var mountinfoDockerLine = `69 64 0:46 / /var/lib/docker/overlay2/abc123/merged rw,relatime shared:34 - overlay overlay rw,lowerdir=/var/lib/docker/overlay2/lower2/diff:/var/lib/docker/overlay2/lower1/diff,upperdir=/var/lib/docker/overlay2/abc123/diff,workdir=/var/lib/docker/overlay2/abc123/work`

var mountinfoMultiLowerLine = `70 64 0:47 / /merged rw,relatime - overlay overlay rw,lowerdir=/l3:/l2:/l1,upperdir=/upper,workdir=/work`

var mountinfoNonOverlayLine = `71 64 8:1 / / rw,relatime - ext4 /dev/sda1 rw`

func writeMountinfo(t *testing.T, content string) string {
	t.Helper()
	dir := t.TempDir()
	path := filepath.Join(dir, "mountinfo")
	if err := os.WriteFile(path, []byte(content), 0o644); err != nil {
		t.Fatalf("write mountinfo fixture: %v", err)
	}
	return path
}

func TestOverlayInfoFromMount_ParsesDockerLine(t *testing.T) {
	// Write a realistic mountinfo fixture.
	path := writeMountinfo(t, mountinfoDockerLine+"\n")

	// Use the unexported test hook by calling the public function with a
	// test-controlled mountinfo via the internal helper exposed for tests.
	// Since overlayInfoFromMountFile is unexported we test via a thin wrapper.
	info, err := overlayInfoFromMountFileExported(path, "/var/lib/docker/overlay2/abc123/merged")
	if err != nil {
		t.Fatalf("OverlayInfoFromMount: unexpected error: %v", err)
	}

	if info.MergedDir != "/var/lib/docker/overlay2/abc123/merged" {
		t.Errorf("MergedDir = %q", info.MergedDir)
	}
	if info.UpperDir != "/var/lib/docker/overlay2/abc123/diff" {
		t.Errorf("UpperDir = %q", info.UpperDir)
	}
	if info.WorkDir != "/var/lib/docker/overlay2/abc123/work" {
		t.Errorf("WorkDir = %q", info.WorkDir)
	}
	if len(info.LowerDirs) != 2 {
		t.Fatalf("LowerDirs len = %d, want 2", len(info.LowerDirs))
	}
	if info.LowerDirs[0] != "/var/lib/docker/overlay2/lower2/diff" {
		t.Errorf("LowerDirs[0] = %q", info.LowerDirs[0])
	}
}

func TestOverlayInfoFromMount_MultiLower(t *testing.T) {
	path := writeMountinfo(t, mountinfoMultiLowerLine+"\n")
	info, err := overlayInfoFromMountFileExported(path, "/merged")
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if len(info.LowerDirs) != 3 {
		t.Fatalf("LowerDirs len = %d, want 3", len(info.LowerDirs))
	}
}

func TestOverlayInfoFromMount_MountNotFound(t *testing.T) {
	path := writeMountinfo(t, mountinfoNonOverlayLine+"\n")
	_, err := overlayInfoFromMountFileExported(path, "/merged")
	if err == nil {
		t.Fatal("expected ErrMountNotFound, got nil")
	}
}

func TestOverlayInfoFromMount_MixedLines(t *testing.T) {
	content := mountinfoNonOverlayLine + "\n" + mountinfoDockerLine + "\n"
	path := writeMountinfo(t, content)
	info, err := overlayInfoFromMountFileExported(path, "/var/lib/docker/overlay2/abc123/merged")
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if info.MergedDir != "/var/lib/docker/overlay2/abc123/merged" {
		t.Errorf("MergedDir = %q", info.MergedDir)
	}
}

func TestOverlayInfoFromMount_EmptyFile(t *testing.T) {
	path := writeMountinfo(t, "")
	_, err := overlayInfoFromMountFileExported(path, "/merged")
	if err == nil {
		t.Fatal("expected error for empty mountinfo, got nil")
	}
}

// overlayInfoFromMountFileExported exposes the private overlayInfoFromMountFile
// for testing. We use the public NewOverlayInfo as a stand-in here and test
// parsing indirectly via a test-only exported helper in the overlay_test.go
// companion file, which uses the internal/testable path.
//
// Since Go does not allow accessing unexported identifiers across package
// boundaries, we use an export_test.go file pattern.
func overlayInfoFromMountFileExported(mountFile, mergedDir string) (*fswatch.OverlayInfo, error) {
	return fswatch.OverlayInfoFromMountFile(mountFile, mergedDir)
}
