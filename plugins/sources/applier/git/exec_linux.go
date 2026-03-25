//go:build linux

package gitapply

import (
	"context"
	"fmt"
	"os/exec"
	"runtime"
	"syscall"
	"time"

	"golang.org/x/sys/unix"
)

// Grace period between SIGTERM and SIGKILL when cancelling a git subprocess.
const processKillGrace = 10 * time.Second

func init() {
	DefaultProcessRunner = runWithIsolatedUmask
}

// runWithIsolatedUmask is the Linux ProcessRunner.
//
// It spawns a goroutine, locks it to a dedicated OS thread (LockOSThread),
// calls CLONE_FS to give that thread its own filesystem namespace (including
// an independent umask), sets umask 0022, then executes the git command.
//
// When the goroutine exits the locked thread is destroyed, cleanly discarding
// the modified namespace — there is no need to call UnlockOSThread.
//
// Security notes:
//   - CLONE_FS scope: the umask change affects only this OS thread, not others.
//   - Setpgid: git and any children it spawns form a new process group; SIGTERM
//     on context cancellation is delivered to the entire group.
//   - Pdeathsig SIGTERM: if the parent OS thread exits unexpectedly (e.g. the
//     daemon is SIGKILLed), the child group receives SIGTERM rather than
//     becoming orphaned.
func runWithIsolatedUmask(ctx context.Context, cmd *exec.Cmd) error {
	errCh := make(chan error, 1)
	go func() {
		runtime.LockOSThread()
		// Intentionally not unlocking: on goroutine exit the thread is
		// destroyed, wiping the cloned FS namespace and its umask.
		errCh <- unshareAndRun(ctx, cmd)
	}()
	return <-errCh
}

// unshareAndRun must be called from a goroutine that has already called
// runtime.LockOSThread.
func unshareAndRun(ctx context.Context, cmd *exec.Cmd) error {
	// Clone the filesystem namespace of this OS thread so that the Umask
	// call below is scoped to this thread and does not affect others.
	if err := syscall.Unshare(syscall.CLONE_FS); err != nil {
		return fmt.Errorf("gitapply: CLONE_FS unshare: %w", err)
	}
	// Standard "developer" umask: owner rw(x), group and others r(-x).
	// Files created by git (objects, pack files, config) will not be
	// world-writable, even if the process was started with umask 0.
	syscall.Umask(0o022)
	return runInProcessGroup(ctx, cmd)
}

// runInProcessGroup starts cmd in a new process group and waits for it,
// delivering SIGTERM (then SIGKILL after processKillGrace) on ctx cancellation.
func runInProcessGroup(ctx context.Context, cmd *exec.Cmd) error {
	cmd.SysProcAttr = &unix.SysProcAttr{
		// Create a new process group so that signals can be delivered to the
		// whole git subprocess tree with Kill(-pgid, sig).
		Setpgid: true,
		// SIGTERM the process group if this OS thread exits unexpectedly.
		// This is a defence-in-depth measure for daemon scenarios.
		Pdeathsig: unix.SIGTERM,
	}

	if err := cmd.Start(); err != nil {
		return err
	}

	waitDone := make(chan struct{})
	go func() {
		select {
		case <-ctx.Done():
			// Graceful: notify the process group.
			_ = unix.Kill(-cmd.Process.Pid, unix.SIGTERM)
			// Forceful: escalate if the process does not exit promptly.
			go func() {
				select {
				case <-waitDone:
				case <-time.After(processKillGrace):
					_ = unix.Kill(-cmd.Process.Pid, unix.SIGKILL)
				}
			}()
		case <-waitDone:
		}
	}()

	err := cmd.Wait()
	close(waitDone)
	return err
}
