//go:build linux

package runcexecutor

import (
	"context"
	"io"
	"os"
	"os/exec"
	"syscall"

	"github.com/containerd/console"
	runc "github.com/containerd/go-runc"
	"github.com/moby/buildkit/executor"
	"github.com/moby/buildkit/util/bklog"
	"github.com/pkg/errors"
	"golang.org/x/sync/errgroup"
)

// ─── RunFunc ─────────────────────────────────────────────────────────────────

// RunFunc is the callback that performs the actual `runc run` or `runc exec`
// call.  It receives:
//   - a context derived from NewProcessHandle (safe for blocking runc calls)
//   - a channel that receives the runc monitor PID once the process is alive
//   - the runc.IO to use for stdio
//   - the pidfile path (non-empty only for `runc exec`)
type RunFunc func(ctx context.Context, startedCh chan<- int, io runc.IO, pidfile string) error

// ─── IOProvider ───────────────────────────────────────────────────────────────

// IOProvider sets up the stdio connection between the caller and the container
// process.  Its single method registers any required goroutines in eg and
// returns the runc.IO that should be passed to the RunFunc.
//
// All goroutines registered in eg must respect ctx; they will be collected by
// executeWithIO's deferred eg.Wait.
type IOProvider interface {
	// Attach configures I/O, registers goroutines in eg, and returns the
	// runc.IO to hand to the RunFunc.
	// h is available for TTY implementations that need WaitForReady before
	// sending resize events.
	Attach(ctx context.Context, eg *errgroup.Group, h *ProcessHandle, process executor.ProcessInfo) (runc.IO, error)
}

// SelectIOProvider returns the appropriate IOProvider for the given process.
func SelectIOProvider(process executor.ProcessInfo) IOProvider {
	if process.Meta.Tty {
		return &ttyProvider{}
	}
	return &plainProvider{}
}

// ─── plainProvider ───────────────────────────────────────────────────────────

// plainProvider forwards stdin/stdout/stderr without a pseudoterminal.
type plainProvider struct{}

func (p *plainProvider) Attach(
	_ context.Context,
	_ *errgroup.Group,
	_ *ProcessHandle,
	process executor.ProcessInfo,
) (runc.IO, error) {
	return &forwardIO{
		stdin:  process.Stdin,
		stdout: process.Stdout,
		stderr: process.Stderr,
	}, nil
}

// ─── ttyProvider ─────────────────────────────────────────────────────────────

// ttyProvider allocates a pseudoterminal and wires it between the caller and
// the container process.
//
// Architecture:
//
//	Caller stdin  ──copy──▶ PTM ──[ kernel PTY ]──▶ PTS (container stdin/stdout/stderr)
//	Caller stdout ◀─copy── PTM
//	PTM resize events ◀── process.Resize channel
type ttyProvider struct{}

func (t *ttyProvider) Attach(
	ctx context.Context,
	eg *errgroup.Group,
	h *ProcessHandle,
	process executor.ProcessInfo,
) (runc.IO, error) {
	// Allocate the pseudoterminal master/slave pair.
	ptm, ptsName, err := console.NewPty()
	if err != nil {
		return nil, errors.Wrap(err, "failed to allocate pseudoterminal")
	}

	// Open the slave (pts) end.  O_NOCTTY prevents it from becoming the
	// controlling terminal of the calling process.
	pts, err := os.OpenFile(ptsName, os.O_RDWR|syscall.O_NOCTTY, 0)
	if err != nil {
		_ = ptm.Close()
		return nil, errors.Wrap(err, "failed to open PTY slave")
	}

	// ── stdin: caller → PTM ─────────────────────────────────────────────────
	if process.Stdin != nil {
		eg.Go(func() error {
			defer process.Stdin.Close()
			if _, err := io.Copy(ptm, process.Stdin); err != nil {
				if errors.Is(err, io.ErrClosedPipe) {
					return nil // normal EOF on a pipe-based stdin
				}
				return err
			}
			return nil
		})
	}

	// ── stdout: PTM → caller ─────────────────────────────────────────────────
	if process.Stdout != nil {
		eg.Go(func() error {
			if _, err := io.Copy(process.Stdout, ptm); err != nil {
				// When the PTM is closed (container exited), the kernel returns
				// EIO on reads. That is the normal EOF signal for a PTY.
				if isPTMCloseError(err) {
					return nil
				}
				return err
			}
			return nil
		})
	}

	// ── resize: forward WinSize changes to PTM ───────────────────────────────
	eg.Go(func() error {
		// Block until the process is alive before trying to send resize events.
		if err := h.WaitForReady(ctx); err != nil {
			return err
		}
		return pumpResizeEvents(ctx, ptm, h, process.Resize)
	})

	// ── deferred cleanup ─────────────────────────────────────────────────────
	// pts and ptm must be closed after the runc call returns (or the context
	// is cancelled) to unblock the io.Copy goroutines above.
	// We register the cleanup in the errgroup so it runs before eg.Wait returns.
	eg.Go(func() error {
		// Wait for either completion or cancellation.
		<-ctx.Done()
		_ = pts.Close()
		_ = ptm.Close()
		return nil
	})

	// Build the runc.IO: the container process sees the slave end for all
	// three streams.  nil channels mean "don't connect that stream".
	runcIO := &forwardIO{}
	if process.Stdin != nil {
		runcIO.stdin = pts
	}
	if process.Stdout != nil {
		runcIO.stdout = pts
	}
	if process.Stderr != nil {
		runcIO.stderr = pts
	}
	return runcIO, nil
}

// pumpResizeEvents reads terminal resize events from the resize channel and
// applies them to ptm.  Returns when ctx is cancelled or resize is closed.
func pumpResizeEvents(
	ctx context.Context,
	ptm console.Console,
	h *ProcessHandle,
	resize <-chan executor.WinSize,
) error {
	for {
		select {
		case <-ctx.Done():
			return nil

		case ws, ok := <-resize:
			if !ok {
				return nil
			}
			if err := ptm.Resize(console.WinSize{
				Height: uint16(ws.Rows),
				Width:  uint16(ws.Cols),
			}); err != nil {
				bklog.G(ctx).Errorf("failed to resize PTY: %v", err)
				continue // non-fatal; keep processing events
			}
			// SIGWINCH must be sent to the runc monitor, not to the container
			// process directly — runc handles terminal resize internally.
			if err := h.monitorProcess.Signal(sigwinch); err != nil {
				bklog.G(ctx).Errorf("failed to send SIGWINCH to runc monitor: %v", err)
			}
		}
	}
}

// sigwinch is a typed constant so we don't import moby/sys/signal in process.go.
const sigwinch = syscall.SIGWINCH

// isPTMCloseError reports whether err is the EIO that the kernel returns
// when the PTY master is read after the slave has been closed.
func isPTMCloseError(err error) bool {
	var pathErr *os.PathError
	return errors.As(err, &pathErr) &&
		pathErr.Op == "read" &&
		pathErr.Path == "/dev/ptmx" &&
		errors.Is(pathErr.Err, syscall.EIO)
}

// ─── forwardIO ────────────────────────────────────────────────────────────────

// forwardIO implements runc.IO by forwarding stdio to supplied ReadCloser /
// WriteCloser values.  Unused fields may be nil.
type forwardIO struct {
	stdin          io.ReadCloser
	stdout, stderr io.WriteCloser
}

func (f *forwardIO) Close() error { return nil }

func (f *forwardIO) Set(cmd *exec.Cmd) {
	cmd.Stdin = f.stdin
	cmd.Stdout = f.stdout
	cmd.Stderr = f.stderr
}

// The Stdin/Stdout/Stderr accessors are part of the runc.IO interface but are
// only needed when the caller wants to write/read from the other end.
// For our use case (forwarding to pre-existing streams) these are unused.
func (f *forwardIO) Stdin() io.WriteCloser  { return nil }
func (f *forwardIO) Stdout() io.ReadCloser  { return nil }
func (f *forwardIO) Stderr() io.ReadCloser  { return nil }

// ─── executeWithIO ────────────────────────────────────────────────────────────

// executeWithIO is the central execution primitive shared by Run and Exec paths.
//
// Lifecycle:
//  1. Allocates a ProcessHandle and a derived runcCtx safe for blocking runc calls.
//  2. Constructs an errgroup whose context (egCtx) is cancelled when runcCtx is
//     cancelled or any member goroutine returns a non-nil error.
//  3. Delegates I/O setup to provider.Attach, which may register goroutines in eg.
//  4. Launches WaitForStart (calls onStarted once the process is alive) and
//     ForwardSignals goroutines.
//  5. Calls runner synchronously.  runner blocks until the container process exits.
//  6. On return: calls h.Shutdown() to cancel egCtx, then waits for all goroutines.
//
// onStarted is called exactly once after the runc PID is received; it may be nil.
func executeWithIO(
	ctx context.Context,
	process executor.ProcessInfo,
	killer Killer,
	provider IOProvider,
	onStarted func(),
	runner RunFunc,
) error {
	h, runcCtx := NewProcessHandle(ctx, killer)
	defer h.Release()

	eg, egCtx := errgroup.WithContext(runcCtx)

	// After runner returns, cancel runcCtx so that all goroutines that are
	// blocking on egCtx (signal handler, resize loop, PTY cleanup) can exit.
	defer func() {
		h.Shutdown()
		if waitErr := eg.Wait(); waitErr != nil && !errors.Is(waitErr, context.Canceled) {
			bklog.G(ctx).Errorf("executeWithIO: goroutine error after container exit: %v", waitErr)
		}
	}()

	// Set up I/O — may register copy / resize / cleanup goroutines in eg.
	runcIO, err := provider.Attach(egCtx, eg, h, process)
	if err != nil {
		return errors.Wrap(err, "failed to attach I/O provider")
	}

	// WaitForStart: receive the runc monitor PID and invoke onStarted.
	startedCh := make(chan int, 1)
	eg.Go(func() error {
		return h.WaitForStart(egCtx, startedCh, onStarted)
	})

	// ForwardSignals: relay process signals to the runc monitor process.
	eg.Go(func() error {
		return ForwardSignals(egCtx, h, process.Signal)
	})

	// Execute the actual runc operation (blocks until the process exits).
	return runner(runcCtx, startedCh, runcIO, killer.Pidfile())
}
