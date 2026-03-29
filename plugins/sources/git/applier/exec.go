package gitapply

import (
	"context"
	"os/exec"
)

// ProcessRunner executes a fully-configured *exec.Cmd and returns its exit error.
//
// Implementations are responsible for:
//
//   - Umask isolation   — git must run with umask 0022 regardless of the
//     daemon's own umask.  On Linux this is achieved with CLONE_FS +
//     runtime.LockOSThread; on other Unix systems via process re-exec.
//
//   - Process-group management — git subprocesses are placed in their own
//     process group (Setpgid) so that a SIGTERM from context cancellation
//     reaches every process in the tree.
//
//   - Graceful then forceful shutdown — on context cancellation send SIGTERM
//     to the process group and escalate to SIGKILL after a grace period.
//
//   - Parent-death signal (Linux / FreeBSD) — Pdeathsig SIGTERM ensures
//     the child group dies even if the parent OS thread exits unexpectedly.
//
// Platform-specific implementations are in:
//   - exec_linux.go       (CLONE_FS + runtime.LockOSThread)
//   - exec_unix.go        (process-group without CLONE_FS, for Darwin/BSD)
//   - exec_windows.go     (minimal, no process-group support)
//
// DefaultProcessRunner is initialised by the platform-specific init() and is
// the right choice for most callers.  Inject a custom ProcessRunner via
// [WithProcessRunner] when testing or when the caller has special requirements.
type ProcessRunner func(ctx context.Context, cmd *exec.Cmd) error

// DefaultProcessRunner is the platform-appropriate ProcessRunner for the
// current operating system.  It is set by the platform-specific init() in the
// exec_*.go files before any user code runs.
var DefaultProcessRunner ProcessRunner
