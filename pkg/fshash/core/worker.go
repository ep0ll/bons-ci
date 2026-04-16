package core

import (
	"runtime"
	"sync"
)

// ── WorkerPool ────────────────────────────────────────────────────────────────
//
// WorkerPool maintains a fixed set of goroutines that drain a buffered job
// channel. Key properties (SKILL §10):
//
//   - Fixed goroutine count; jobs channel is 8× worker count.
//   - Clean shutdown via channel close + WaitGroup.
//   - 56-byte pad between jobs and wg separates them onto different cache
//     lines, eliminating false sharing between the producer (writing jobs)
//     and the workers (decrementing wg).
//
// Create with NewWorkerPool; stop with Stop when the owning task finishes.
// Do NOT share a WorkerPool across unrelated Sum calls.

// WorkerPool is a fixed-size goroutine pool for parallel file hashing.
type WorkerPool struct {
	jobs chan func()
	_    [56]byte // cache-line pad — sizeof(chan) = 8; keeps wg on next line
	wg   sync.WaitGroup
	n    int
}

// NewWorkerPool creates a WorkerPool with n workers.
// n ≤ 0 uses runtime.NumCPU(); n is capped at 64.
func NewWorkerPool(n int) *WorkerPool {
	if n <= 0 {
		n = runtime.NumCPU()
	}
	if n > 64 {
		n = 64
	}
	wp := &WorkerPool{
		jobs: make(chan func(), n*8),
		n:    n,
	}
	for range n {
		wp.wg.Add(1)
		go func() {
			defer wp.wg.Done()
			for fn := range wp.jobs {
				fn()
			}
		}()
	}
	return wp
}

// Submit enqueues fn for execution. Blocks if the queue is full.
func (wp *WorkerPool) Submit(fn func()) { wp.jobs <- fn }

// Stop drains the queue and waits for all workers to exit.
// Must be called exactly once. Safe to defer.
func (wp *WorkerPool) Stop() { close(wp.jobs); wp.wg.Wait() }

// Workers returns the configured worker count.
func (wp *WorkerPool) Workers() int { return wp.n }
