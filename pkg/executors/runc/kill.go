//go:build linux

package runcexecutor

import (
	"context"
	"os"
	"path/filepath"
	"strconv"
	"syscall"
	"time"

	runc "github.com/containerd/go-runc"
	"github.com/moby/buildkit/util/bklog"
	"github.com/pkg/errors"
)

// ─── Killer interface ─────────────────────────────────────────────────────────

// Killer is the mechanism for sending SIGKILL to a container process.
//
// Two implementations exist because the kill path differs depending on how
// the process was started:
//
//   - ContainerKiller: for processes started with `runc run`.  The container
//     init PID is managed by runc itself, so we delegate to `runc kill`.
//
//   - ExecKiller: for processes started with `runc exec`.  runc writes the
//     process PID into a pidfile; we read it and signal the process directly.
type Killer interface {
	// Kill sends SIGKILL to the target process.
	// Implementations must respect ctx for timeout/cancellation.
	Kill(ctx context.Context) error

	// Pidfile returns the path used for the PID file, if any.
	// ContainerKiller returns ""; ExecKiller returns the allocated temp path.
	Pidfile() string
}

// ─── ContainerKiller ─────────────────────────────────────────────────────────

// ContainerKiller kills a container's init process via `runc kill`.
// This is the correct path for processes started with `runc run`.
type ContainerKiller struct {
	runC *runc.Runc
	id   string
}

// NewContainerKiller returns a Killer for a container started with `runc run`.
func NewContainerKiller(runC *runc.Runc, id string) Killer {
	return &ContainerKiller{runC: runC, id: id}
}

func (k *ContainerKiller) Kill(ctx context.Context) error {
	bklog.G(ctx).Debugf("ContainerKiller: sending SIGKILL to container %s", k.id)
	ctx, cancel := context.WithTimeoutCause(ctx, 10*time.Second, ErrKillTimeout) //nolint:govet
	defer cancel()
	if err := k.runC.Kill(ctx, k.id, int(syscall.SIGKILL), nil); err != nil {
		bklog.G(ctx).Errorf("ContainerKiller: runc kill failed for %s: %v", k.id, err)
		return err
	}
	return nil
}

func (k *ContainerKiller) Pidfile() string { return "" }

// ─── ExecKiller ──────────────────────────────────────────────────────────────

// ExecKiller kills a process started with `runc exec` by reading the PID
// runc wrote into a temporary pidfile and sending SIGKILL directly.
//
// The pidfile directory is allocated in a system temp directory and must be
// cleaned up via Close() when the process has exited.
type ExecKiller struct {
	runC    *runc.Runc
	id      string
	pidfile string   // absolute path; allocated on construction
	tempDir string   // the parent dir; removed by Close
}

// NewExecKiller allocates a temporary pidfile directory and returns a Killer
// suitable for processes started with `runc exec`.
//
// Callers must call Close() after the process exits to remove the temp dir.
func NewExecKiller(runC *runc.Runc, id string) (*ExecKiller, error) {
	dir, err := os.MkdirTemp("", "buildkit-runc-exec-")
	if err != nil {
		return nil, errors.Wrap(err, "failed to create pidfile directory")
	}
	return &ExecKiller{
		runC:    runC,
		id:      id,
		pidfile: filepath.Join(dir, "pidfile"),
		tempDir: dir,
	}, nil
}

// Close removes the temporary pidfile directory.
// Safe to call multiple times.
func (k *ExecKiller) Close() error {
	return os.RemoveAll(k.tempDir)
}

func (k *ExecKiller) Pidfile() string { return k.pidfile }

func (k *ExecKiller) Kill(ctx context.Context) error {
	bklog.G(ctx).Debugf("ExecKiller: sending SIGKILL to exec process in container %s", k.id)

	// Apply a failsafe timeout; the caller should already have a tighter one.
	killCtx, cancel := context.WithTimeoutCause(ctx, 10*time.Second, ErrKillTimeout) //nolint:govet
	defer cancel()

	pid, err := k.awaitPidfile(killCtx)
	if err != nil {
		bklog.G(ctx).Errorf("ExecKiller: failed to read pidfile for %s: %v", k.id, err)
		return err
	}

	proc, err := os.FindProcess(pid)
	if err != nil {
		// Only possible on non-Unix hosts; included for completeness.
		return errors.Wrapf(err, "ExecKiller: failed to find process %d", pid)
	}
	defer proc.Release()

	if err := proc.Signal(syscall.SIGKILL); err != nil {
		bklog.G(ctx).Errorf("ExecKiller: SIGKILL to pid %d failed: %v", pid, err)
		return err
	}
	return nil
}

// awaitPidfile polls until runc exec writes the pidfile or ctx is cancelled.
//
// runc writes the pidfile a few milliseconds after we receive the runc PID via
// the started channel, so we may need to retry briefly in the edge case where
// the caller requests a kill immediately after process creation.
func (k *ExecKiller) awaitPidfile(ctx context.Context) (int, error) {
	const pollInterval = 10 * time.Millisecond
	for {
		data, err := os.ReadFile(k.pidfile)
		if err == nil {
			pid, parseErr := strconv.Atoi(string(data))
			if parseErr != nil {
				return 0, errors.Wrap(parseErr, "invalid pidfile content")
			}
			return pid, nil
		}
		if !os.IsNotExist(err) {
			return 0, errors.Wrap(err, "failed to read pidfile")
		}
		// Pidfile not yet written; wait and retry.
		select {
		case <-ctx.Done():
			return 0, errors.New("context cancelled while waiting for runc to write pidfile")
		case <-time.After(pollInterval):
		}
	}
}
