package schedule

import (
	"context"
	"runtime"
	"sync"
	"sync/atomic"

	digest "github.com/opencontainers/go-digest"
)

// execFuture represents the in-flight execution of a single vertex. Multiple
// callers that arrive while the vertex is executing all receive the same
// future channel, which is closed when the work completes. This provides
// vertex-level deduplication equivalent to BuildKit's shared activeOp +
// flightcontrol.Group.
type execFuture struct {
	done chan struct{} // closed when the task completes
	err  error        // set before done is closed
}

// Scheduler manages a pool of workers that drain a priority queue. It provides
// vertex-level deduplication: if a vertex is already in-flight, subsequent
// Submit calls return the existing future rather than enqueuing a duplicate.
//
// This is safe for concurrent use. Stop() must be called to release goroutines.
type Scheduler struct {
	queue   *PriorityQueue
	policy  Policy
	workers int

	// active maps digest.Digest → *execFuture for currently-in-flight vertices.
	// Using sync.Map because the hot path is mostly reads (Load) with rare
	// writes (LoadOrStore / Delete).
	active sync.Map

	// wg tracks live worker goroutines.
	wg sync.WaitGroup

	// cancel stops the worker loop goroutines.
	cancel context.CancelFunc

	// completed / failed are exposed via Stats().
	completed atomic.Int64
	failed    atomic.Int64

	// OnTaskComplete is called after each task finishes (optional hook).
	OnTaskComplete func(dgst digest.Digest, err error)
}

// NewScheduler creates a scheduler with the given concurrency and policy.
// workers ≤ 0 → GOMAXPROCS. nil policy → CriticalPathPolicy.
func NewScheduler(workers int, policy Policy) *Scheduler {
	if workers <= 0 {
		workers = runtime.GOMAXPROCS(0)
	}
	if policy == nil {
		policy = &CriticalPathPolicy{}
	}
	return &Scheduler{
		queue:   NewPriorityQueue(),
		policy:  policy,
		workers: workers,
	}
}

// GetPolicy returns the scheduling policy in use.
func (s *Scheduler) GetPolicy() Policy { return s.policy }

// Start launches the worker goroutines. Must be called exactly once before
// Submit. The scheduler runs until Stop() is called or ctx is cancelled.
func (s *Scheduler) Start(ctx context.Context) {
	ctx, s.cancel = context.WithCancel(ctx)
	for i := 0; i < s.workers; i++ {
		s.wg.Add(1)
		go s.worker(ctx)
	}
}

// Submit enqueues a task for execution and returns a channel that is closed
// when the task completes. If an identical vertex (same VertexDigest) is
// already in-flight, Submit returns that task's existing future channel
// WITHOUT enqueuing a duplicate — this is the critical deduplication path.
//
// PREVIOUS BUG: the original code wrote:
//
//	wait := s.active.Load   // assigns the *method*, not its result
//	_ = wait
//	ch, _ := s.active.Load(t.VertexDigest)
//
// That made the dedup channel unreachable and always enqueued duplicates.
// Fixed by storing the future struct atomically.
func (s *Scheduler) Submit(t *Task) <-chan struct{} {
	future := &execFuture{done: make(chan struct{})}

	// LoadOrStore returns (existing, true) if already present, or stores
	// our new future and returns (future, false).
	actual, loaded := s.active.LoadOrStore(t.VertexDigest, future)
	if loaded {
		// Vertex already in-flight; return its existing completion channel.
		return actual.(*execFuture).done
	}

	// Apply scheduling priority from the configured policy.
	t.Priority = s.policy.Priority(t.Depth, t.EstimatedCost)

	// Wrap the task function to signal the future on completion.
	origFn := t.Fn
	t.Fn = func() error {
		err := origFn()
		// Always mark the future, even on error.
		future.err = err
		close(future.done)
		// Remove from the active map so subsequent submits re-run the vertex.
		s.active.Delete(t.VertexDigest)
		return err
	}

	s.queue.Push(t)
	return future.done
}

// Wait blocks until the future for dgst has completed. Returns the task error.
// If dgst is not in-flight, returns nil immediately.
func (s *Scheduler) Wait(dgst digest.Digest) error {
	v, ok := s.active.Load(dgst)
	if !ok {
		return nil
	}
	f := v.(*execFuture)
	<-f.done
	return f.err
}

// Stop signals all workers to stop, drains and closes the queue, then waits
// for every goroutine to exit cleanly.
//
// PREVIOUS BUG: calling cancel() before queue.Close() left workers blocked on
// the queue.Pop() cond-var because the worker's ctx.Done() select arm only
// fires at the top of the loop, after Pop() returns. Correct order:
// close the queue first (unblocks all Pop() calls), then cancel context.
func (s *Scheduler) Stop() {
	// 1. Close the queue so blocked Pop() calls unblock and return nil.
	s.queue.Close()
	// 2. Cancel the context to stop the worker loop.
	if s.cancel != nil {
		s.cancel()
	}
	// 3. Wait for all workers to exit.
	s.wg.Wait()
}

// Stats returns lifetime execution counters.
func (s *Scheduler) Stats() (completed, failed int64) {
	return s.completed.Load(), s.failed.Load()
}

// QueueLen returns the current number of pending tasks.
func (s *Scheduler) QueueLen() int { return s.queue.Len() }

// worker is the goroutine that processes tasks from the priority queue.
func (s *Scheduler) worker(ctx context.Context) {
	defer s.wg.Done()
	for {
		// Check context cancellation before blocking on Pop.
		select {
		case <-ctx.Done():
			return
		default:
		}

		task := s.queue.Pop() // blocks until a task is available or queue closes
		if task == nil {
			return // queue closed
		}

		err := task.Fn()

		if err != nil {
			s.failed.Add(1)
		} else {
			s.completed.Add(1)
		}

		if s.OnTaskComplete != nil {
			s.OnTaskComplete(task.VertexDigest, err)
		}
	}
}
