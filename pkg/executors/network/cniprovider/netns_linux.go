//go:build linux

package cniprovider

import (
	"fmt"
	"os"
	"path/filepath"
	"runtime"
	"syscall"

	"github.com/containerd/containerd/v2/pkg/oci"
	"github.com/moby/buildkit/util/bklog"
	specs "github.com/opencontainers/runtime-spec/specs-go"
	"github.com/pkg/errors"
	"golang.org/x/sys/unix"
)

// nsDir returns the directory where this provider stores its namespace files.
func (c *cniProvider) nsDir() string {
	return filepath.Join(c.root, "net", "cni")
}

// cleanOldNamespaces removes any network namespace files left over from a
// previous (crashed) daemon instance.  It runs asynchronously to avoid
// blocking provider construction.
//
// Stale namespaces are identified by the presence of files under nsDir().
// We attempt a best-effort CNI Remove + unmount + delete for each, logging
// failures at WARNING level because they are non-fatal.
func cleanOldNamespaces(c *cniProvider) {
	nsDir := c.nsDir()
	entries, err := os.ReadDir(nsDir)
	if err != nil {
		bklog.L.Debugf("cniprovider: could not read stale-ns directory %q: %v", nsDir, err)
		return
	}

	go func() {
		for _, d := range entries {
			id := d.Name()
			ns := &cniNS{
				id:       id,
				nativeID: filepath.Join(nsDir, id),
				handle:   c.handle,
			}
			if err := ns.release(); err != nil {
				bklog.L.Warnf("cniprovider: failed to release stale namespace %q: %v", id, err)
			}
		}
	}()
}

// createNetNS allocates a new Linux network namespace and bind-mounts it at a
// stable path so it persists after the creating goroutine exits.
//
// The implementation:
//  1. Creates an empty file at nsPath (the mount target).
//  2. Spawns a new OS thread, locks it, and calls unshare(CLONE_NEWNET) to
//     enter a new network namespace on that thread.
//  3. Bind-mounts /proc/self/task/<tid>/ns/net onto nsPath to give the
//     namespace a path-based lifetime independent of the thread.
//  4. Leaves the thread locked so the Go runtime destroys it on return,
//     preventing the unshared namespace from leaking back into the goroutine
//     scheduler's thread pool.
//
// The bind-mount approach is the same technique used by ip-netns(8) and
// containerd's netns package.  It is necessary because a network namespace
// only persists as long as either a process is running inside it or it has
// at least one bind-mount reference.
func createNetNS(c *cniProvider, id string) (_ string, err error) {
	nsPath := filepath.Join(c.nsDir(), id)
	if err := os.MkdirAll(filepath.Dir(nsPath), 0o700); err != nil {
		return "", errors.Wrapf(err, "cniprovider: mkdir %s", filepath.Dir(nsPath))
	}

	// Create the bind-mount target file.
	f, err := os.Create(nsPath)
	if err != nil {
		return "", errors.Wrapf(err, "cniprovider: create netns file %s", nsPath)
	}
	if err := f.Close(); err != nil {
		return "", errors.Wrapf(err, "cniprovider: close netns file %s", nsPath)
	}

	// Clean up the file if namespace creation fails.
	defer func() {
		if err != nil {
			if delErr := deleteNetNS(nsPath); delErr != nil {
				bklog.L.WithError(delErr).Warnf("cniprovider: cleanup: failed to remove %s", nsPath)
			}
		}
	}()

	errCh := make(chan error, 1)

	go func() {
		// LockOSThread pins this goroutine to the current OS thread.
		// The unshare(2) call below modifies the calling thread's namespace.
		// By locking, we prevent the Go scheduler from migrating this goroutine
		// to a different thread, which would unshare the wrong thread.
		//
		// We intentionally leave the thread locked after this goroutine returns
		// so that the Go runtime terminates the thread; reusing an
		// unshare()d thread in the scheduler pool would corrupt networking for
		// other goroutines.
		runtime.LockOSThread()

		if err := unshareAndBindMount(nsPath); err != nil {
			errCh <- err
		}
		close(errCh)
	}()

	if err := <-errCh; err != nil {
		return "", err
	}
	return nsPath, nil
}

// unshareAndBindMount must be called on a locked OS thread.
// It creates a new network namespace for the calling thread and bind-mounts
// it at target so it survives after the thread exits.
func unshareAndBindMount(target string) error {
	if err := syscall.Unshare(syscall.CLONE_NEWNET); err != nil {
		return errors.Wrap(err, "unshare CLONE_NEWNET")
	}
	// /proc/self/task/<tid>/ns/net is the canonical path to the calling
	// thread's current network namespace.
	procPath := fmt.Sprintf("/proc/self/task/%d/ns/net", syscall.Gettid())
	if err := syscall.Mount(procPath, target, "", syscall.MS_BIND, ""); err != nil {
		return errors.Wrapf(err, "bind-mount %s → %s", procPath, target)
	}
	return nil
}

// setNetNS updates the OCI spec to join the network namespace at nsPath.
func setNetNS(s *specs.Spec, nsPath string) error {
	return oci.WithLinuxNamespace(specs.LinuxNamespace{
		Type: specs.NetworkNamespace,
		Path: nsPath,
	})(nil, nil, nil, s)
}

// unmountNetNS detaches the bind-mount from nsPath so the kernel can reclaim
// the network namespace once all references are dropped.
//
// EINVAL means the path is not a mount point (never mounted, or already
// unmounted).  ENOENT means the file was already deleted.  Both are benign.
func unmountNetNS(nsPath string) error {
	if err := unix.Unmount(nsPath, unix.MNT_DETACH); err != nil {
		if !errors.Is(err, syscall.EINVAL) && !errors.Is(err, syscall.ENOENT) {
			return errors.Wrapf(err, "cniprovider: unmount netns %s", nsPath)
		}
	}
	return nil
}

// deleteNetNS removes the netns bind-mount file.  ENOENT is treated as
// success (idempotent).
func deleteNetNS(nsPath string) error {
	if err := os.Remove(nsPath); err != nil && !errors.Is(err, os.ErrNotExist) {
		return errors.Wrapf(err, "cniprovider: remove netns file %s", nsPath)
	}
	return nil
}
