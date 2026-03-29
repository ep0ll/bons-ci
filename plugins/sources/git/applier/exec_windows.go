//go:build windows

package gitapply

import (
	"context"
	"os/exec"
)

func init() {
	DefaultProcessRunner = runProcess
}

// runProcess is the Windows ProcessRunner.
//
// Windows does not support process groups or signals in the Unix sense.
// We start the process, wait for it, and kill it forcefully on context
// cancellation.  Umask is not a concept on Windows so no special handling
// is needed.
func runProcess(ctx context.Context, cmd *exec.Cmd) error {
	if err := cmd.Start(); err != nil {
		return err
	}

	waitDone := make(chan struct{})
	go func() {
		select {
		case <-ctx.Done():
			// Windows has no SIGTERM; Kill() sends a hard termination.
			_ = cmd.Process.Kill()
		case <-waitDone:
		}
	}()

	err := cmd.Wait()
	close(waitDone)
	return err
}
