// Package schedule provides priority-based, dependency-distance-aware
// scheduling for solver vertex execution.
package schedule

import (
	"container/heap"
	"sync"

	digest "github.com/opencontainers/go-digest"
)

// Task represents a unit of work to be scheduled.
type Task struct {
	// VertexDigest uniquely identifies the vertex being executed.
	VertexDigest digest.Digest

	// Name is a human-readable label for progress/logging.
	Name string

	// Priority determines execution order: LOWER values run FIRST.
	// For CriticalPath scheduling: Priority = maxDepth - vertexDepth.
	Priority int

	// Depth is the vertex's dependency distance from the deepest root.
	Depth int

	// EstimatedCost is an optional cost hint from VertexOptions.EstimatedCost.
	EstimatedCost float64

	// Fn is the work function. It must respect context cancellation.
	Fn func() error

	// index is managed by container/heap. Do not touch.
	index int
}

// PriorityQueue is a thread-safe min-heap of Tasks ordered by priority.
// Lower priority values are dequeued first (critical-path-first).
type PriorityQueue struct {
	mu     sync.Mutex
	cond   *sync.Cond
	h      taskHeap
	closed bool
}

// NewPriorityQueue creates a ready-to-use priority queue.
func NewPriorityQueue() *PriorityQueue {
	pq := &PriorityQueue{
		h: make(taskHeap, 0, 64),
	}
	pq.cond = sync.NewCond(&pq.mu)
	heap.Init(&pq.h)
	return pq
}

// Push adds a task to the queue and wakes one blocked Pop caller.
func (pq *PriorityQueue) Push(t *Task) {
	pq.mu.Lock()
	defer pq.mu.Unlock()
	if pq.closed {
		return
	}
	heap.Push(&pq.h, t)
	pq.cond.Signal()
}

// Pop removes and returns the highest-priority (lowest value) task.
// Blocks if the queue is empty. Returns nil if the queue is closed.
func (pq *PriorityQueue) Pop() *Task {
	pq.mu.Lock()
	defer pq.mu.Unlock()
	for pq.h.Len() == 0 && !pq.closed {
		pq.cond.Wait()
	}
	if pq.closed && pq.h.Len() == 0 {
		return nil
	}
	return heap.Pop(&pq.h).(*Task)
}

// TryPop returns the highest-priority task without blocking, or nil if empty.
func (pq *PriorityQueue) TryPop() *Task {
	pq.mu.Lock()
	defer pq.mu.Unlock()
	if pq.h.Len() == 0 {
		return nil
	}
	return heap.Pop(&pq.h).(*Task)
}

// Len returns the number of tasks currently in the queue.
func (pq *PriorityQueue) Len() int {
	pq.mu.Lock()
	n := pq.h.Len()
	pq.mu.Unlock()
	return n
}

// Close marks the queue as closed and wakes all blocked Pop callers.
// After Close, Push is a no-op and Pop returns nil once the queue is drained.
func (pq *PriorityQueue) Close() {
	pq.mu.Lock()
	defer pq.mu.Unlock()
	if !pq.closed {
		pq.closed = true
		pq.cond.Broadcast()
	}
}

// ─── heap.Interface implementation ────────────────────────────────────────────

type taskHeap []*Task

func (h taskHeap) Len() int { return len(h) }

// Less defines the ordering. Priority is the primary sort key (lower = first).
// EstimatedCost breaks ties (higher cost = more urgent = first).
// VertexDigest provides a final deterministic tiebreaker.
func (h taskHeap) Less(i, j int) bool {
	if h[i].Priority != h[j].Priority {
		return h[i].Priority < h[j].Priority
	}
	if h[i].EstimatedCost != h[j].EstimatedCost {
		return h[i].EstimatedCost > h[j].EstimatedCost // higher cost runs first
	}
	return h[i].VertexDigest < h[j].VertexDigest
}

// Swap must also update the index field used by Fix/Remove.
func (h taskHeap) Swap(i, j int) {
	h[i], h[j] = h[j], h[i]
	h[i].index = i
	h[j].index = j
}

func (h *taskHeap) Push(x any) {
	t := x.(*Task)
	t.index = len(*h)
	*h = append(*h, t)
}

func (h *taskHeap) Pop() any {
	old := *h
	n := len(old)
	t := old[n-1]
	old[n-1] = nil // prevent memory leak
	t.index = -1   // mark as removed
	*h = old[:n-1]
	return t
}
