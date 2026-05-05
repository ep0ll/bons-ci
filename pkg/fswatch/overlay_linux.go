//go:build linux

package fswatch

import (
	"fmt"
	"os"
	"path/filepath"

	"github.com/moby/sys/mountinfo"
)

// OverlayInfoFromMount finds and parses the overlay mount whose merged
// directory is mergedDir by querying the kernel via moby/sys/mountinfo.
//
// Uses mountinfo.GetMounts(FSTypeFilter("overlay")) so only overlay-type
// entries are parsed — a single efficient pass of /proc/self/mountinfo.
//
// Returns [ErrMountNotFound] when no overlay mount matches mergedDir.
func OverlayInfoFromMount(mergedDir string) (*OverlayInfo, error) {
	mergedDir = filepath.Clean(mergedDir)

	mounts, err := mountinfo.GetMounts(mountinfo.FSTypeFilter("overlay"))
	if err != nil {
		return nil, fmt.Errorf("overlay: get mounts: %w", err)
	}

	for _, m := range mounts {
		if filepath.Clean(m.Mountpoint) != mergedDir {
			continue
		}
		return overlayInfoFromVFSOptions(m.VFSOptions, mergedDir)
	}
	return nil, fmt.Errorf("overlay: %w: %q", ErrMountNotFound, mergedDir)
}

// overlayInfoFromMountFile reads mountinfo from mountinfoPath (instead of
// /proc/self/mountinfo) and returns the OverlayInfo for mergedDir.
//
// Uses mountinfo.GetMountsFromReader so the full moby parser handles
// octal-escaped paths and optional fields correctly.
//
// This function is unexported; it is exposed for tests via export_test.go.
func overlayInfoFromMountFile(mountinfoPath, mergedDir string) (*OverlayInfo, error) {
	mergedDir = filepath.Clean(mergedDir)

	f, err := os.Open(mountinfoPath)
	if err != nil {
		return nil, fmt.Errorf("overlay: open %s: %w", mountinfoPath, err)
	}
	defer f.Close()

	mounts, err := mountinfo.GetMountsFromReader(f, mountinfo.FSTypeFilter("overlay"))
	if err != nil {
		return nil, fmt.Errorf("overlay: parse %s: %w", mountinfoPath, err)
	}

	for _, m := range mounts {
		if filepath.Clean(m.Mountpoint) != mergedDir {
			continue
		}
		return overlayInfoFromVFSOptions(m.VFSOptions, mergedDir)
	}
	return nil, fmt.Errorf("overlay: %w: %q", ErrMountNotFound, mergedDir)
}
