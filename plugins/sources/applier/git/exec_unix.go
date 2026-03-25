//go:build unix && !linux

package gitapply

import (
	"context"
	"os/exec"
	"time"

	"golang.org/x/sys/unix"
)

// processKillGrace is the time between SIGTERM and SIGKILL on cancellation.
const processKillGrace = 10 * time.Second

func init() {
	DefaultProcessRunner = runWithProcessGroup
}

// runWithProcessGroup is the non-Linux Unix ProcessRunner (Darwin, FreeBSD, …).
//
// Darwin and FreeBSD lack CLONE_FS so we cannot isolate a umask change to a
// single OS thread.  Instead we rely on the caller (or the OS default) having
// a sane umask.  We still place git in its own process group for clean
// cancellation.
//
// Security notes:
//   - Setpgid: git and its children form a new process group.
//   - On FreeBSD, Pdeathsig SIGTERM is set (Darwin does not support Pdeathsig).
//   - SIGTERM → 10 s grace → SIGKILL on context cancellation.
func runWithProcessGroup(ctx context.Context, cmd *exec.Cmd) error {
	cmd.SysProcAttr = buildSysProcAttr()

	if err := cmd.Start(); err != nil {
		return err
	}

	waitDone := make(chan struct{})
	go func() {
		select {
		case <-ctx.Done():
			_ = unix.Kill(-cmd.Process.Pid, unix.SIGTERM)
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
