// Package runcexecutor provides a BuildKit Executor backed by runc.
//
// Architecture overview:
//
//	Executor.Run  → ContainerRegistry.Register → OCI spec → executeWithIO(ContainerKiller, IOProvider)
//	Executor.Exec → ContainerRegistry.WaitRunning → load spec → executeWithIO(ExecKiller, IOProvider)
//
// Every process lifecycle flows through executeWithIO, which selects a Killer and an
// IOProvider (plain or TTY), creates a ProcessHandle, then calls the supplied RunFunc
// inside a shared errgroup.  OOMDetector annotates the exit error post-hoc by reading
// cgroup v2 memory.events.
package runcexecutor

import (
	"errors"
	"fmt"
)

// ─── sentinel errors ────────────────────────────────────────────────────────

var (
	// ErrContainerNotFound is returned by Exec when the container ID is unknown.
	ErrContainerNotFound = errors.New("container not found")

	// ErrContainerStopped is returned by Exec when the container exited before
	// the process could be started.
	ErrContainerStopped = errors.New("container stopped before exec could start")

	// ErrRuncBinaryNotFound is returned by New when no runc binary is on PATH.
	ErrRuncBinaryNotFound = errors.New("runc binary not found in PATH")

	// ErrProcessStartTimeout is returned when go-runc never sends the started PID.
	ErrProcessStartTimeout = errors.New("timeout waiting for runc process to start")

	// ErrKillTimeout is returned when the kill operation exceeds its deadline.
	ErrKillTimeout = errors.New("kill operation timed out")
)

// ─── ExitError ───────────────────────────────────────────────────────────────

// ExitError carries a container exit code and optional context.
// It is the only error type returned from Run / Exec on a process-exit path;
// callers can distinguish OOM kills, context cancellations, and plain non-zero
// exits through the exported fields and helper functions.
type ExitError struct {
	// ExitCode is the integer exit code reported by runc (0 on success).
	ExitCode int

	// OOMKilled is true when the cgroup memory controller sent oom_kill.
	OOMKilled bool

	// Cause is the underlying error if the exit was triggered by a context
	// cancellation or another internal failure (may be nil).
	Cause error
}

func (e *ExitError) Error() string {
	var msg string
	if e.OOMKilled {
		msg = fmt.Sprintf("OOM killed (exit %d)", e.ExitCode)
	} else {
		msg = fmt.Sprintf("exit code %d", e.ExitCode)
	}
	if e.Cause != nil {
		msg += ": " + e.Cause.Error()
	}
	return msg
}

// Unwrap returns the underlying cause so that errors.Is / errors.As
// can traverse the chain.
func (e *ExitError) Unwrap() error { return e.Cause }

// ─── helpers ─────────────────────────────────────────────────────────────────

// IsOOMKilled reports whether err (or any error in its chain) represents an
// OOM kill from the kernel's memory controller.
func IsOOMKilled(err error) bool {
	var e *ExitError
	return errors.As(err, &e) && e.OOMKilled
}

// ExitCode extracts the numeric exit code from err.
// Returns -1 if err is not an ExitError.
func ExitCode(err error) int {
	var e *ExitError
	if errors.As(err, &e) {
		return e.ExitCode
	}
	return -1
}
