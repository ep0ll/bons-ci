package differ

import (
	"context"
	"errors"
	"fmt"
	"sync"
	"sync/atomic"
)

// ─────────────────────────────────────────────────────────────────────────────
// Operation types
// ─────────────────────────────────────────────────────────────────────────────

// OpKind classifies a [BatchOp].
type OpKind uint8

const (
	// OpRemove removes a single file or empty directory from the merged view.
	// Corresponds to os.Remove / unlinkat(AT_FDCWD, path, 0).
	OpRemove OpKind = iota

	// OpRemoveAll removes a path and its entire subtree from the merged view.
	// Corresponds to os.RemoveAll / unlinkat(AT_FDCWD, path, AT_REMOVEDIR)
	// followed by recursive descent for non-empty directories.
	// This is the operation used for collapsed [ExclusivePath] entries.
	OpRemoveAll
)

// String returns a human-readable label.
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
// Operations are collected into a [Batcher] and executed efficiently in bulk.
type BatchOp struct {
	Kind    OpKind
	RelPath string // forward-slash relative path within the MergedView
	// Tag is an optional caller-supplied identifier for tracing or logging.
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
//     pool. Works on all platforms.
//   - [IOURingBatcher] (Linux 5.11+): submits batches as a single
//     io_uring_enter(2) call, reducing kernel transitions from O(n) to O(1)
//     per flush. Falls back to [GoroutineBatcher] on older kernels.
//
// # Lifecycle
//
// Submit → (optionally Flush at any point) → Close.
// Close must be called exactly once. After Close, Submit and Flush must not
// be called.
//
// All methods must be safe for concurrent use.
type Batcher interface {
	// Submit enqueues op for later execution. May auto-flush if the internal
	// queue reaches its capacity threshold.
	Submit(ctx context.Context, op BatchOp) error

	// Flush executes all queued ops and blocks until they complete.
	// Returns the combined errors of all failed ops via errors.Join.
	// Callers should invoke Flush at the end of every pipeline to ensure
	// no ops are silently discarded.
	Flush(ctx context.Context) error

	// Close flushes any remaining ops and releases resources (goroutines,
	// io_uring ring FDs, mmapped memory).
	// After Close returns the Batcher must not be used.
	Close(ctx context.Context) error
}

// ─────────────────────────────────────────────────────────────────────────────
// NopBatcher
// ─────────────────────────────────────────────────────────────────────────────

// NopBatcher is a [Batcher] that silently discards every operation.
// Useful as a placeholder and in benchmarks that measure classification
// overhead in isolation.
type NopBatcher struct{}

func (NopBatcher) Submit(_ context.Context, _ BatchOp) error { return nil }
func (NopBatcher) Flush(_ context.Context) error             { return nil }
func (NopBatcher) Close(_ context.Context) error             { return nil }

// ─────────────────────────────────────────────────────────────────────────────
// RecordingBatcher — test double
// ─────────────────────────────────────────────────────────────────────────────

// RecordingBatcher records every submitted [BatchOp] without performing any
// filesystem mutation. It is the canonical test double for [Batcher].
//
// # Key invariant
//
// Ops() returns ALL ops ever submitted, regardless of how many times Flush or
// Close has been called. This mirrors the real batcher contract where ops are
// executed (and thus "seen") at flush time but their identity is not lost.
// Tests that call pl.Run() — which internally triggers a final Flush — can
// still inspect the full op log via Ops() afterwards.
//
// Total() returns only the count of ops that have passed through a Flush,
// which is useful for verifying that the pipeline actually drained cleanly.
type RecordingBatcher struct {
	mu      sync.Mutex
	all     []BatchOp // permanent log — never cleared
	pending []BatchOp // unflushed ops — cleared by Flush
	total   atomic.Int64
}

// Submit implements [Batcher].
func (r *RecordingBatcher) Submit(_ context.Context, op BatchOp) error {
	r.mu.Lock()
	r.all = append(r.all, op)
	r.pending = append(r.pending, op)
	r.mu.Unlock()
	return nil
}

// Flush implements [Batcher] — moves pending ops to the total counter.
// Does NOT clear the permanent log returned by Ops().
func (r *RecordingBatcher) Flush(_ context.Context) error {
	r.mu.Lock()
	r.total.Add(int64(len(r.pending)))
	r.pending = r.pending[:0]
	r.mu.Unlock()
	return nil
}

// Close implements [Batcher].
func (r *RecordingBatcher) Close(ctx context.Context) error { return r.Flush(ctx) }

// Ops returns a snapshot of ALL ops ever submitted to this batcher, including
// those already flushed. The slice is a copy; it is safe to modify.
func (r *RecordingBatcher) Ops() []BatchOp {
	r.mu.Lock()
	defer r.mu.Unlock()
	out := make([]BatchOp, len(r.all))
	copy(out, r.all)
	return out
}

// Pending returns a snapshot of ops that have been submitted but not yet flushed.
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
// executeOp — shared execution primitive used by all Batcher implementations
// ─────────────────────────────────────────────────────────────────────────────

// executeOp executes a single [BatchOp] against view. It is intentionally a
// free function so that different Batcher implementations (goroutine pool,
// io_uring) can share the same execution logic for the fallback path.
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
		return fmt.Errorf("batch: unknown op kind %d for path %q", op.Kind, op.RelPath)
	}
}

// executeBatch runs a slice of ops concurrently up to parallelism goroutines
// and returns the combined error. This is used by both [GoroutineBatcher.Flush]
// and the io_uring fallback path.
func executeBatch(ctx context.Context, view MergedView, ops []BatchOp, parallelism int) error {
	if len(ops) == 0 {
		return nil
	}

	sem := make(chan struct{}, parallelism)
	var wg sync.WaitGroup
	var mu sync.Mutex
	var errs []error

	for _, op := range ops {
		if ctx.Err() != nil {
			break
		}
		op := op
		sem <- struct{}{}
		wg.Add(1)
		go func() {
			defer wg.Done()
			defer func() { <-sem }()
			if err := executeOp(ctx, view, op); err != nil && !errors.Is(err, context.Canceled) {
				mu.Lock()
				errs = append(errs, fmt.Errorf("%s %q: %w", op.Kind, op.RelPath, err))
				mu.Unlock()
			}
		}()
	}
	wg.Wait()
	return joinErrors(errs)
}
