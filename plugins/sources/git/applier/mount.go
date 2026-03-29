package gitapply

import (
	"context"
	"fmt"
	"os"

	"github.com/containerd/containerd/v2/core/mount"
)

// OverlayMountActivator implements [MountActivator] for containerd overlay and
// other kernel-backed mount types.
//
// It creates a temporary host directory, activates all provided mounts onto
// that directory, calls fn with the resulting root path, then unmounts and
// removes the temp dir regardless of whether fn succeeds.
//
// This type is the standard choice for production use with a containerd
// snapshotter; use [PassthroughMountActivator] when the caller has already
// arranged a mount and just wants to pass the path directly.
type OverlayMountActivator struct {
	// TempDir is the base directory for temp mount points.
	// Defaults to os.TempDir() when empty.
	TempDir string
}

var _ MountActivator = (*OverlayMountActivator)(nil)

// Activate creates a temporary directory, mounts all mounts onto it, calls fn,
// then unmounts unconditionally.
func (a *OverlayMountActivator) Activate(
	ctx context.Context,
	mounts []mount.Mount,
	fn func(rootDir string) error,
) (retErr error) {
	dir, err := os.MkdirTemp(a.TempDir, "gitapply-mount-*")
	if err != nil {
		return fmt.Errorf("gitapply: create mount temp dir: %w", err)
	}
	defer func() {
		if err := os.RemoveAll(dir); err != nil && retErr == nil {
			retErr = fmt.Errorf("gitapply: remove mount temp dir: %w", err)
		}
	}()

	if err := mount.All(mounts, dir); err != nil {
		return fmt.Errorf("gitapply: mount %d mounts onto %q: %w", len(mounts), dir, err)
	}
	defer func() {
		if err := mount.UnmountAll(dir, 0); err != nil && retErr == nil {
			retErr = fmt.Errorf("gitapply: unmount %q: %w", dir, err)
		}
	}()

	return fn(dir)
}

// PassthroughMountActivator implements [MountActivator] for callers that have
// already resolved a host directory and do not need containerd mount management.
//
// It ignores the mounts slice entirely and calls fn with the fixed RootDir.
// Useful in tests, or when the destination is a plain directory on the host.
type PassthroughMountActivator struct {
	RootDir string
}

var _ MountActivator = (*PassthroughMountActivator)(nil)

// Activate ignores mounts and calls fn with the pre-configured RootDir.
func (a *PassthroughMountActivator) Activate(
	_ context.Context,
	_ []mount.Mount,
	fn func(rootDir string) error,
) error {
	if a.RootDir == "" {
		return fmt.Errorf("gitapply: PassthroughMountActivator.RootDir must not be empty")
	}
	return fn(a.RootDir)
}
