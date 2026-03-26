//go:build linux

package runcexecutor

import (
	"context"
	"os"
	"syscall"
	"time"

	"github.com/moby/buildkit/util/bklog"
	"github.com/moby/sys/signal"
	"github.com/pkg/errors"
)

// ─── ProcessHandle ────────────────────────────────────────────────────────────

// ProcessHandle tracks the lifecycle of a single runc-managed process.
//
// Lifecycle:
//
//	NewProcessHandle → runc.Run/Exec → WaitForStart(sets monitorProcess, closes ready)
//	                                 → WaitForReady (used by signal/resize goroutines)
//	                 → process exits → Release (closes ended, releases monitorProcess)
//	                 → Shutdown (cancels the runc context so subsidiary goroutines exit)
//
// Context cancellation model:
//
//	When the request context (ctx passed to Run/Exec) is cancelled, the goroutine
//	spawned by NewProcessHandle sends SIGKILL to the in-container process via the
//	Killer.  If the kill succeeds (or the container exits on its own) within 7 s,
//	the runcCtx is left intact and runc exits cleanly.  If the kill fails after the
//	7 s window, runcCtx is cancelled as a last resort to release any blocked calls.
//
//	Callers must use runcCtx for the go-runc.(Run|Exec) invocations, NOT the
//	original request context.
type ProcessHandle struct {
	// monitorProcess is the OS process entry for the runc binary itself
	// (NOT the in-container init). Used to forward POSIX signals.
	// Set once by WaitForStart; nil until then.
	monitorProcess *os.Process

	// ready is closed exactly once after monitorProcess is set.
	ready chan struct{}

	// ended is closed exactly once when the container process has exited
	// (called by Release). The kill goroutine watches this to stop retrying.
	ended chan struct{}

	// shutdown cancels runcCtx. Called by Shutdown after the runc call returns.
	shutdown context.CancelCauseFunc

	// killer is used to send SIGKILL on context cancellation.
	killer Killer
}

// NewProcessHandle allocates a ProcessHandle and returns a derived runcCtx.
//
//   - runcCtx is a fresh context (NOT derived from ctx) that will only be
//     cancelled if the kill operation itself fails; it is the context to pass
//     to go-runc.(Run|Exec).
//   - When ctx is cancelled, the goroutine started here attempts to SIGKILL
//     the in-container process, then waits 50 ms per attempt for up to 7 s
//     before forcibly cancelling runcCtx.
func NewProcessHandle(ctx context.Context, killer Killer) (*ProcessHandle, context.Context) {
	runcCtx, cancelRunc := context.WithCancelCause(context.Background())
	runcCtx = bklog.WithLogger(runcCtx, bklog.G(ctx)) // preserve log fields

	h := &ProcessHandle{
		ready:    make(chan struct{}),
		ended:    make(chan struct{}),
		shutdown: cancelRunc,
		killer:   killer,
	}

	go h.watchRequestContext(ctx, cancelRunc)

	return h, runcCtx
}

// watchRequestContext monitors the request context and arranges for the
// in-container process to be killed when it is cancelled.
func (h *ProcessHandle) watchRequestContext(ctx context.Context, cancelRunc context.CancelCauseFunc) {
	// Wait until the process handle is ready (PID is known) or the container
	// exits before we do anything — there is nothing to kill otherwise.
	select {
	case <-h.ended:
		return
	case <-h.ready:
		select {
		case <-h.ended:
			return // container already gone
		default:
		}
	}

	for {
		select {
		case <-h.ended:
			return

		case <-ctx.Done():
			// Request was cancelled: kill the in-container process.
			killCtx, killTimeout := context.WithCancelCause(context.Background()) //nolint:govet
			killCtx, _ = context.WithTimeoutCause(killCtx, 7*time.Second, ErrKillTimeout)  //nolint:govet

			killErr := h.killer.Kill(killCtx)
			killTimeout(errors.WithStack(context.Canceled))

			if killErr != nil {
				// Kill itself timed out or failed; check if that was due to our
				// kill deadline. If so, cancel runcCtx as a last resort so that
				// blocked runc calls can return.
				if errors.Is(killCtx.Err(), context.DeadlineExceeded) {
					cancelRunc(errors.WithStack(context.Cause(ctx)))
					return
				}
				// Kill returned a non-timeout error; keep retrying.
			}

			// Give runc a brief moment to react to the kill before retrying.
			select {
			case <-time.After(50 * time.Millisecond):
			case <-h.ended:
				return
			}
		}
	}
}

// WaitForStart receives the runc PID from go-runc's started channel.
//
// It waits at most 10 s for the PID to arrive.  Once received, it records the
// os.Process handle for the runc monitor process, calls onStarted (if non-nil),
// and closes the ready channel so that signal/resize goroutines can proceed.
func (h *ProcessHandle) WaitForStart(
	ctx context.Context,
	startedCh <-chan int,
	onStarted func(),
) error {
	waitCtx, cancel := context.WithTimeoutCause(ctx, 10*time.Second, ErrProcessStartTimeout) //nolint:govet
	defer cancel()

	select {
	case <-waitCtx.Done():
		return errors.Wrap(context.Cause(waitCtx), "runc did not report the started PID in time")

	case pid, ok := <-startedCh:
		if !ok {
			return errors.New("runc started channel was closed without sending a PID")
		}
		if onStarted != nil {
			onStarted()
		}
		proc, err := os.FindProcess(pid)
		if err != nil {
			// Theoretically impossible on Unix; included for robustness.
			return errors.Wrapf(err, "failed to find runc monitor process %d", pid)
		}
		h.monitorProcess = proc
		close(h.ready)
	}
	return nil
}

// WaitForReady blocks until the process PID is known or ctx is cancelled.
// Signal and TTY resize goroutines call this before sending any signals.
func (h *ProcessHandle) WaitForReady(ctx context.Context) error {
	select {
	case <-ctx.Done():
		return context.Cause(ctx)
	case <-h.ready:
		return nil
	}
}

// Release marks the process as ended and releases the OS process handle.
// Must be called exactly once after the runc call returns.
func (h *ProcessHandle) Release() {
	close(h.ended)
	if h.monitorProcess != nil {
		h.monitorProcess.Release()
	}
}

// Shutdown cancels the runcCtx, which causes signal/TTY-resize goroutines that
// are blocking on it to return.  Call after the runc call has returned.
func (h *ProcessHandle) Shutdown() {
	h.shutdown(errors.WithStack(context.Canceled))
}

// ─── signal forwarding ────────────────────────────────────────────────────────

// ForwardSignals forwards signals received on signals to the runc monitor
// process, starting only after h is ready.
//
// SIGKILL is special-cased: it is never forwarded to the runc process directly,
// because runc must stay alive to perform cleanup.  Instead it is routed through
// the Killer to reach the in-container process.
//
// Returns nil when ctx is cancelled or signals is closed.
func ForwardSignals(ctx context.Context, h *ProcessHandle, signals <-chan syscall.Signal) error {
	if signals == nil {
		return nil
	}
	if err := h.WaitForReady(ctx); err != nil {
		return err
	}
	for {
		select {
		case <-ctx.Done():
			return nil

		case sig, ok := <-signals:
			if !ok {
				return nil
			}
			if err := forwardOneSignal(ctx, h, sig); err != nil {
				return err
			}
		}
	}
}

func forwardOneSignal(ctx context.Context, h *ProcessHandle, sig syscall.Signal) error {
	if sig == syscall.SIGKILL {
		// Route SIGKILL through the killer so it reaches the in-container process.
		if err := h.killer.Kill(ctx); err != nil {
			return errors.Wrap(err, "failed to deliver SIGKILL")
		}
		return nil
	}

	// For all other signals, forward to the runc monitor process.
	// runc will forward SIGWINCH etc. to the container's init.
	if sig == signal.SIGWINCH {
		// SIGWINCH is forwarded for terminal resize; use Send not Signal
		// to avoid unnecessary error logging.
		_ = h.monitorProcess.Signal(sig)
		return nil
	}

	if err := h.monitorProcess.Signal(sig); err != nil {
		bklog.G(ctx).Errorf("failed to forward signal %s to runc monitor: %v", sig, err)
		return errors.Wrapf(err, "failed to forward signal %s", sig)
	}
	return nil
}
