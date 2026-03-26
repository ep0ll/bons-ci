//go:build linux

package runcexecutor

import (
	"syscall"

	runc "github.com/containerd/go-runc"
)

// applyHostOSRuncDefaults applies OS-specific settings to the runc.Runc runtime
// struct after it has been constructed.
//
// On Linux, PdeathSignal is set to SIGKILL so that if the BuildKit daemon dies
// unexpectedly, orphaned runc processes are killed by the kernel via the parent-
// death signal mechanism.
//
// Note: PdeathSignal only affects the runc monitor process, not the in-container
// init.  The in-container process may still leak if the runc monitor itself is
// killed before it can propagate the signal — hence the comment "this can still
// leak the process" preserved from the original implementation.
func applyHostOSRuncDefaults(rt *runc.Runc) {
	rt.PdeathSignal = syscall.SIGKILL
}
