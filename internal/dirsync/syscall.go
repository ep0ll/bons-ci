package differ

import (
	"context"
	"runtime"
	"sync"
)

// GoroutineBatcher is a [Batcher] that executes operations concurrently using
// a bounded goroutine pool. It is the portable (non-Linux-specific) default.
//
// # Batching strategy
//
// Submitted operations are held in an in-memory queue. When the queue reaches
// AutoFlushAt items, Flush is called automatically to bound memory usage.
// A final explicit Flush (or Close) must be called to drain any remainder.
//
// # Concurrency
//
// Up to Parallelism goroutines execute operations concurrently during each
// Flush. This provides near-linear throughput scaling on multi-core systems
// for workloads dominated by filesystem metadata operations (unlink, rmdir),
// which are frequently parallelised by modern kernels' VFS layer.
//
// On Linux, for very large batches consider using [IOURingBatcher] to reduce
// per-op kernel transitions to a single io_uring_enter(2) call.
type GoroutineBatcher struct {
	view        MergedView
	parallelism int
	autoFlushAt int

	mu      sync.Mutex
	pending []BatchOp
	closed  bool
}

// GoroutineBatcherOption is a functional option for [GoroutineBatcher].
type GoroutineBatcherOption func(*GoroutineBatcher)

// WithBatchParallelism sets the maximum number of goroutines that may execute
// operations concurrently during a Flush. Defaults to runtime.NumCPU().
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
// The operation is appended to the pending queue. If AutoFlushAt is configured
// and the queue has reached that threshold, Flush is called automatically.
func (b *GoroutineBatcher) Submit(ctx context.Context, op BatchOp) error {
	b.mu.Lock()
	if b.closed {
		b.mu.Unlock()
		return errBatcherClosed
	}
	b.pending = append(b.pending, op)
	shouldFlush := b.autoFlushAt > 0 && len(b.pending) >= b.autoFlushAt
	b.mu.Unlock()

	if shouldFlush {
		return b.Flush(ctx)
	}
	return nil
}

// Flush implements [Batcher].
// Atomically drains the pending queue and executes all collected ops
// concurrently. Errors from individual ops are joined and returned.
func (b *GoroutineBatcher) Flush(ctx context.Context) error {
	b.mu.Lock()
	if b.closed {
		b.mu.Unlock()
		return errBatcherClosed
	}
	work := b.pending
	b.pending = make([]BatchOp, 0, cap(work)) // preserve capacity hint
	b.mu.Unlock()

	return executeBatch(ctx, b.view, work, b.parallelism)
}

// Close implements [Batcher].
// Flushes any remaining ops and marks the batcher as closed.
//
// BUG FIX M4: The original Close called Flush unconditionally, then set
// b.closed=true. A second call to Close would then call Flush again which
// would immediately return errBatcherClosed (non-nil). This made Close
// non-idempotent. Defensive `defer batcher.Close(ctx)` patterns (common in
// Go) would return a spurious error on the second call.
//
// Fix: check b.closed before calling Flush. If already closed, return nil
// immediately. This matches the io.Closer contract.
func (b *GoroutineBatcher) Close(ctx context.Context) error {
	b.mu.Lock()
	if b.closed {
		b.mu.Unlock()
		return nil
	}
	b.mu.Unlock()

	err := b.Flush(ctx)

	b.mu.Lock()
	b.closed = true
	b.mu.Unlock()
	return err
}

// Len returns the number of ops currently waiting in the pending queue.
func (b *GoroutineBatcher) Len() int {
	b.mu.Lock()
	defer b.mu.Unlock()
	return len(b.pending)
}

// errBatcherClosed is returned by Submit and Flush after Close has been called.
var errBatcherClosed = newStaticError("batcher: already closed")

// newStaticError creates a non-wrapping sentinel error.
func newStaticError(msg string) error { return staticError(msg) }

type staticError string

func (e staticError) Error() string { return string(e) }
