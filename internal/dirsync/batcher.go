package dirsync

import (
	"context"
	"fmt"
	"sync"
	"sync/atomic"
)

// ─────────────────────────────────────────────────────────────────────────────
// BatchOp
// ─────────────────────────────────────────────────────────────────────────────

// OpKind classifies a [BatchOp] into the filesystem mutation it represents.
type OpKind uint8

const (
	// OpRemove removes a single file or empty directory.
	// Corresponds to os.Remove / unlinkat(AT_FDCWD, path, 0).
	OpRemove OpKind = iota

	// OpRemoveAll removes a path and its entire subtree.
	// Corresponds to os.RemoveAll.
	// Used for collapsed [ExclusivePath] entries where a single call covers
	// the entire exclusive subtree.
	OpRemoveAll
)

// String returns a human-readable label suitable for logging.
func (k OpKind) String() string {
	switch k {
	case OpRemove:
		return "remove"
	case OpRemoveAll:
		return "removeAll"
	default:
		return "unknown"
	}
}

// BatchOp is a single filesystem mutation to be applied to a [MergedView].
type BatchOp struct {
	// Kind determines whether this is a single-entry removal or a recursive one.
	Kind OpKind

	// RelPath is the forward-slash relative path within the MergedView.
	RelPath string

	// Tag is an optional caller-supplied value for tracing, logging, or testing.
	// Not interpreted by the batcher.
	Tag any
}

// ─────────────────────────────────────────────────────────────────────────────
// Batcher interface
// ─────────────────────────────────────────────────────────────────────────────

// Batcher collects [BatchOp] values and executes them against a [MergedView]
// using the most efficient mechanism available on the current platform.
//
// # Implementations
//
//   - [GoroutineBatcher]: executes ops concurrently using a bounded goroutine
//     pool. Portable — works on all platforms.
//   - [IOURingBatcher] (Linux 5.11+): submits batches as a single
//     io_uring_enter(2) call, reducing kernel transitions from O(n) to O(1)
//     per flush. Falls back transparently to GoroutineBatcher on older kernels.
//
// # Lifecycle
//
// Submit → (optionally Flush) → Close.
// Close must be called exactly once. After Close, Submit and Flush must not
// be called; they return [ErrBatcherClosed].
//
// All methods must be safe for concurrent use from multiple goroutines.
type Batcher interface {
	// Submit enqueues op for later execution. May trigger an automatic Flush
	// if the internal queue reaches the configured capacity threshold.
	Submit(ctx context.Context, op BatchOp) error

	// Flush executes all queued ops and blocks until they all complete.
	// Returns the combined errors of all failed ops via errors.Join.
	Flush(ctx context.Context) error

	// Close flushes any remaining ops and releases all resources.
	// Close is idempotent: calling it more than once returns nil.
	// After Close returns the Batcher must not be used.
	Close(ctx context.Context) error
}

// ─────────────────────────────────────────────────────────────────────────────
// NopBatcher — silent discard
// ─────────────────────────────────────────────────────────────────────────────

// NopBatcher is a [Batcher] that silently discards every operation.
//
// Useful as a placeholder in pipelines that use handlers instead of batchers,
// and in benchmarks that measure classification overhead in isolation.
type NopBatcher struct{}

// Submit implements [Batcher].
func (NopBatcher) Submit(_ context.Context, _ BatchOp) error { return nil }

// Flush implements [Batcher].
func (NopBatcher) Flush(_ context.Context) error { return nil }

// Close implements [Batcher].
func (NopBatcher) Close(_ context.Context) error { return nil }

// ─────────────────────────────────────────────────────────────────────────────
// RecordingBatcher — test double
// ─────────────────────────────────────────────────────────────────────────────

// RecordingBatcher records every submitted [BatchOp] without performing any
// filesystem mutation. It is the canonical test double for [Batcher].
//
// # Invariants
//
//   - Ops() returns the permanent log of ALL ops ever submitted, regardless
//     of how many times Flush or Close has been called.
//   - Pending() returns ops submitted but not yet flushed.
//   - Total() returns the cumulative count of ops that have passed through Flush.
type RecordingBatcher struct {
	mu      sync.Mutex
	all     []BatchOp     // permanent log — never cleared
	pending []BatchOp     // ops not yet flushed — cleared by Flush
	total   atomic.Int64  // cumulative count of flushed ops
}

// Submit implements [Batcher].
func (r *RecordingBatcher) Submit(_ context.Context, op BatchOp) error {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.all = append(r.all, op)
	r.pending = append(r.pending, op)
	return nil
}

// Flush implements [Batcher].
// Moves pending ops to the running total. Does NOT clear the permanent log.
func (r *RecordingBatcher) Flush(_ context.Context) error {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.total.Add(int64(len(r.pending)))
	r.pending = r.pending[:0]
	return nil
}

// Close implements [Batcher].
func (r *RecordingBatcher) Close(ctx context.Context) error { return r.Flush(ctx) }

// Ops returns a point-in-time snapshot of ALL ops ever submitted, including
// already-flushed ones. The returned slice is a copy.
func (r *RecordingBatcher) Ops() []BatchOp {
	r.mu.Lock()
	defer r.mu.Unlock()
	out := make([]BatchOp, len(r.all))
	copy(out, r.all)
	return out
}

// Pending returns a snapshot of ops submitted but not yet flushed.
// The returned slice is a copy.
func (r *RecordingBatcher) Pending() []BatchOp {
	r.mu.Lock()
	defer r.mu.Unlock()
	out := make([]BatchOp, len(r.pending))
	copy(out, r.pending)
	return out
}

// Total returns the cumulative count of ops that have passed through Flush.
func (r *RecordingBatcher) Total() int64 { return r.total.Load() }

// ─────────────────────────────────────────────────────────────────────────────
// executeOp — shared execution primitive
// ─────────────────────────────────────────────────────────────────────────────

// executeOp executes a single [BatchOp] against view.
//
// This is a free function — not a method — so that different [Batcher]
// implementations can share the same execution logic without coupling to one
// another.
func executeOp(ctx context.Context, view MergedView, op BatchOp) error {
	if ctx.Err() != nil {
		return ctx.Err()
	}
	switch op.Kind {
	case OpRemove:
		return view.Remove(ctx, op.RelPath)
	case OpRemoveAll:
		return view.RemoveAll(ctx, op.RelPath)
	default:
		return fmt.Errorf("batcher: unknown op kind %d for path %q", op.Kind, op.RelPath)
	}
}

// ─────────────────────────────────────────────────────────────────────────────
// executeBatch — concurrent execution with bounded parallelism
// ─────────────────────────────────────────────────────────────────────────────

// executeBatch runs a slice of ops concurrently up to parallelism goroutines
// and returns the combined error from all failures.
//
// # Cancellation safety
//
// A semaphore acquire uses select with ctx.Done() so that a context cancellation
// unblocks the loop even when all worker slots are occupied. Without this, a
// cancelled context would block the caller until a slot freed up — potentially
// forever if an op was stuck in an I/O call.
//
// Context-cancelled errors from individual ops are swallowed; only real
// filesystem errors are collected and returned.
func executeBatch(ctx context.Context, view MergedView, ops []BatchOp, parallelism int) error {
	if len(ops) == 0 {
		return nil
	}

	sem := make(chan struct{}, parallelism)
	var (
		wg   sync.WaitGroup
		mu   sync.Mutex
		errs []error
	)

	for _, op := range ops {
		if ctx.Err() != nil {
			break
		}
		op := op // capture loop variable

		// Acquire a semaphore slot with cancellation support.
		select {
		case sem <- struct{}{}:
		case <-ctx.Done():
			break
		}

		wg.Add(1)
		go func() {
			defer wg.Done()
			defer func() { <-sem }()

			if err := executeOp(ctx, view, op); err != nil && !isContextErr(err) {
				mu.Lock()
				errs = append(errs, fmt.Errorf("%s %q: %w", op.Kind, op.RelPath, err))
				mu.Unlock()
			}
		}()
	}

	wg.Wait()
	return joinErrors(errs)
}
