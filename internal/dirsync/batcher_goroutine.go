package dirsync

import (
	"context"
	"runtime"
	"sync"
)

// ─────────────────────────────────────────────────────────────────────────────
// GoroutineBatcher
// ─────────────────────────────────────────────────────────────────────────────

// GoroutineBatcher is a [Batcher] that executes operations concurrently using
// a bounded goroutine pool. It is the portable (non-Linux-specific) default,
// and the fallback used by [IOURingBatcher] on kernels older than Linux 5.11.
//
// # Batching strategy
//
// Submitted operations are held in an in-memory queue. When the queue reaches
// AutoFlushAt items, Flush is called automatically to bound memory usage and
// distribute I/O work over time rather than in a single burst.
//
// A final explicit Flush (or Close) must always be called to drain any
// remaining ops after all submissions are complete.
//
// # Concurrency
//
// Up to Parallelism goroutines execute operations concurrently during each
// Flush. On modern kernels VFS metadata operations (unlink, rmdir) are
// frequently parallelised across different directory inodes, so close-to-linear
// throughput scaling is achievable.
//
// # Idempotent Close
//
// Close may be called multiple times safely; subsequent calls return nil
// without performing a second Flush. This matches the [io.Closer] contract
// and makes deferred Close safe in cleanup paths.
type GoroutineBatcher struct {
	view        MergedView
	parallelism int
	autoFlushAt int // 0 means auto-flush is disabled

	mu      sync.Mutex
	pending []BatchOp
	closed  bool
}

// GoroutineBatcherOption is a functional option for [GoroutineBatcher].
type GoroutineBatcherOption func(*GoroutineBatcher)

// WithBatchParallelism sets the maximum number of goroutines that may execute
// operations concurrently during a Flush. Defaults to runtime.NumCPU().
// Values ≤ 0 are ignored.
func WithBatchParallelism(n int) GoroutineBatcherOption {
	return func(b *GoroutineBatcher) {
		if n > 0 {
			b.parallelism = n
		}
	}
}

// WithAutoFlushAt causes the batcher to automatically call Flush whenever the
// pending queue reaches n items. A value of 0 (default) disables auto-flush.
func WithAutoFlushAt(n int) GoroutineBatcherOption {
	return func(b *GoroutineBatcher) {
		if n >= 0 {
			b.autoFlushAt = n
		}
	}
}

// NewGoroutineBatcher constructs a [GoroutineBatcher] targeting view.
func NewGoroutineBatcher(view MergedView, opts ...GoroutineBatcherOption) *GoroutineBatcher {
	b := &GoroutineBatcher{
		view:        view,
		parallelism: runtime.NumCPU(),
		autoFlushAt: 0,
	}
	for _, o := range opts {
		o(b)
	}
	return b
}

// Submit implements [Batcher].
//
// The operation is appended to the pending queue. When AutoFlushAt is
// configured and the queue has reached that threshold, Flush is called
// automatically before returning.
func (b *GoroutineBatcher) Submit(ctx context.Context, op BatchOp) error {
	b.mu.Lock()
	if b.closed {
		b.mu.Unlock()
		return ErrBatcherClosed
	}
	b.pending = append(b.pending, op)
	shouldAutoFlush := b.autoFlushAt > 0 && len(b.pending) >= b.autoFlushAt
	b.mu.Unlock()

	if shouldAutoFlush {
		return b.Flush(ctx)
	}
	return nil
}

// Flush implements [Batcher].
//
// Atomically drains the pending queue and executes all collected ops
// concurrently using a bounded goroutine pool. Individual op errors are
// joined and returned; partial failures do not prevent the remaining ops
// from being attempted.
func (b *GoroutineBatcher) Flush(ctx context.Context) error {
	b.mu.Lock()
	if b.closed {
		b.mu.Unlock()
		return ErrBatcherClosed
	}
	// Swap the pending slice out under the lock so new submissions can proceed
	// concurrently while we execute the current batch.
	work := b.pending
	b.pending = make([]BatchOp, 0, cap(work)) // preserve capacity hint
	b.mu.Unlock()

	return executeBatch(ctx, b.view, work, b.parallelism)
}

// Close implements [Batcher].
//
// Flushes any remaining ops and marks the batcher as closed. Subsequent calls
// to Close return nil without re-flushing (idempotent).
func (b *GoroutineBatcher) Close(ctx context.Context) error {
	b.mu.Lock()
	if b.closed {
		b.mu.Unlock()
		return nil // idempotent: second call is a no-op
	}
	b.mu.Unlock()

	// Flush outside the lock so executeBatch goroutines don't contend with
	// Submit calls that may arrive concurrently before Close fully settles.
	err := b.Flush(ctx)

	b.mu.Lock()
	b.closed = true
	b.mu.Unlock()

	return err
}

// Len returns the number of ops currently waiting in the pending queue.
// Used in tests to assert auto-flush thresholds.
func (b *GoroutineBatcher) Len() int {
	b.mu.Lock()
	defer b.mu.Unlock()
	return len(b.pending)
}
