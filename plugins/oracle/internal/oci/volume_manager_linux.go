//go:build linux

// volume_manager_linux.go: Linux-specific fast paths for volume operations.
// This file is compiled ONLY on linux; other platforms get volume_manager_other.go.
package oci

import (
	"context"
	"fmt"
	"os"
	"time"

	"go.uber.org/zap"
	"golang.org/x/sys/unix"
)

// waitForDevicePlatform uses Linux inotify on /dev so we react in < 1ms
// when the iSCSI block device appears, instead of sleeping in a loop.
func (v *VolumeManager) waitForDevicePlatform(ctx context.Context, devicePath string, timeout time.Duration) error {
	// Initialise inotify. IN_CLOEXEC prevents leaking the fd into child
	// processes; IN_NONBLOCK lets us use Select without blocking forever.
	fd, err := unix.InotifyInit1(unix.IN_CLOEXEC | unix.IN_NONBLOCK)
	if err != nil {
		// inotify unavailable (e.g. inside certain containers) — fall back.
		v.log.Debug("inotify unavailable — using poll fallback", zap.Error(err))
		return v.waitForDevicePoll(ctx, devicePath, timeout)
	}
	defer unix.Close(fd)

	// Watch /dev for file creation and attribute changes.
	if _, err := unix.InotifyAddWatch(fd, "/dev", unix.IN_CREATE|unix.IN_ATTRIB); err != nil {
		unix.Close(fd)
		return v.waitForDevicePoll(ctx, devicePath, timeout)
	}

	deadline := time.Now().Add(timeout)
	rfds := &unix.FdSet{}

	for {
		select {
		case <-ctx.Done():
			return ctx.Err()
		default:
		}

		if _, err := os.Stat(devicePath); err == nil {
			return nil
		}
		if time.Now().After(deadline) {
			return fmt.Errorf("device %s did not appear within %s", devicePath, timeout)
		}

		// Block for up to 100 ms waiting for an inotify event, then recheck.
		rfds.Zero()
		rfds.Set(fd)
		tv := unix.NsecToTimeval(int64(100 * time.Millisecond))
		unix.Select(fd+1, rfds, nil, nil, &tv) //nolint:errcheck
	}
}

// PreloadCheckpointDir pre-allocates contiguous disk space using fallocate(2).
// This prevents filesystem fragmentation so the large pages files are written
// in a single sequential extent — critical for fast sequential reads during
// restore on the successor.
func (v *VolumeManager) PreloadCheckpointDir(dir string, estimatedBytes int64) error {
	if estimatedBytes <= 0 {
		return nil
	}
	if err := os.MkdirAll(dir, 0o700); err != nil {
		return nil // non-fatal
	}
	placeholder := dir + "/.preallocated"
	f, err := os.Create(placeholder)
	if err != nil {
		return nil // non-fatal — best effort only
	}
	defer f.Close()

	// FALLOC_FL_KEEP_SIZE (0) allocates blocks but does not update st_size,
	// so the file appears empty until written.  We use mode 0 (no flags) to
	// actually extend the file so the blocks are reserved on disk.
	if err := unix.Fallocate(int(f.Fd()), 0, 0, estimatedBytes); err != nil {
		// Not all filesystems support fallocate (e.g. NFS, tmpfs).
		v.log.Debug("fallocate unsupported — skipping pre-allocation",
			zap.String("dir", dir),
			zap.Error(err),
		)
	}
	return nil
}
