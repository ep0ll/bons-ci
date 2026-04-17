package schedule

import (
	"context"
	"runtime"
	"sync"
	"sync/atomic"

	digest "github.com/opencontainers/go-digest"
)

// Scheduler manages a pool of workers that process tasks from a priority
// queue. It provides vertex-level deduplication to prevent redundant work.
type Scheduler struct {
	queue   *PriorityQueue
	policy  Policy
	workers int

	// active tracks in-flight vertex digests for deduplication.
	active sync.Map // digest.Digest → chan struct{} (completion signal)

	cancel context.CancelFunc
	wg     sync.WaitGroup

	completed atomic.Int64
	failed    atomic.Int64

	// OnTaskComplete is called after each task finishes. Optional.
	OnTaskComplete func(digest digest.Digest, err error)
}

// GetPolicy returns the current scheduling policy.
func (s *Scheduler) GetPolicy() Policy {
	return s.policy
}

// NewScheduler creates a scheduler with the given concurrency and policy.
// If workers <= 0, uses GOMAXPROCS.
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

// Start launches the worker goroutines. Must be called before Submit.
func (s *Scheduler) Start(ctx context.Context) {
	ctx, s.cancel = context.WithCancel(ctx)
	for i := 0; i < s.workers; i++ {
		s.wg.Add(1)
		go s.worker(ctx)
	}
}

// Submit enqueues a task for execution. If a task for the same vertex
// digest is already in-flight, Submit returns a channel that closes when
// the existing task completes, and does not enqueue a duplicate.
func (s *Scheduler) Submit(t *Task) <-chan struct{} {
	// Dedup check: if already active, return existing wait channel.
	if existing, loaded := s.active.LoadOrStore(t.VertexDigest, make(chan struct{})); loaded {
		return existing.(chan struct{})
	}

	// Apply scheduling policy.
	t.Priority = s.policy.Priority(t.Depth, t.EstimatedCost)

	wait := s.active.Load // re-load the channel we just stored
	_ = wait              // suppress unused warning

	ch, _ := s.active.Load(t.VertexDigest)
	s.queue.Push(t)
	return ch.(chan struct{})
}

// Stop signals all workers to finish and waits for completion.
func (s *Scheduler) Stop() {
	if s.cancel != nil {
		s.cancel()
	}
	s.queue.Close()
	s.wg.Wait()
}

// Stats returns execution statistics.
func (s *Scheduler) Stats() (completed, failed int64) {
	return s.completed.Load(), s.failed.Load()
}

// QueueLen returns the current queue depth.
func (s *Scheduler) QueueLen() int {
	return s.queue.Len()
}

func (s *Scheduler) worker(ctx context.Context) {
	defer s.wg.Done()
	for {
		select {
		case <-ctx.Done():
			return
		default:
		}

		task := s.queue.Pop()
		if task == nil {
			return // queue closed
		}

		err := task.Fn()

		// Signal completion.
		if ch, ok := s.active.LoadAndDelete(task.VertexDigest); ok {
			close(ch.(chan struct{}))
		}

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
