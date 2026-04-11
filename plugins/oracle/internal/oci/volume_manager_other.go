//go:build !linux

// volume_manager_other.go: portable stubs for non-Linux platforms.
// These are no-ops used during local development on macOS / Windows.
// All production deployments run on OCI Linux instances where
// volume_manager_linux.go provides the real implementations.
package oci

import (
	"context"
	"time"
)

// waitForDevicePlatform falls through to the portable poll loop on
// non-Linux platforms where inotify is unavailable.
func (v *VolumeManager) waitForDevicePlatform(ctx context.Context, devicePath string, timeout time.Duration) error {
	return v.waitForDevicePoll(ctx, devicePath, timeout)
}

// PreloadCheckpointDir is a no-op on non-Linux platforms (fallocate not available).
func (v *VolumeManager) PreloadCheckpointDir(dir string, estimatedBytes int64) error {
	return nil
}
