// Package schedule provides priority-based scheduling for solver vertex
// execution. It uses dependency-distance aware ordering to prioritize
// deeper (more expensive) branches first.
package schedule

import (
	"container/heap"
	"sync"

	digest "github.com/opencontainers/go-digest"
)

// Task represents a unit of work to be scheduled.
type Task struct {
	// VertexDigest uniquely identifies the vertex.
	VertexDigest digest.Digest

	// Name is a human-readable label for progress reporting.
	Name string

	// Priority determines execution order. Lower values execute first.
	// For critical-path scheduling, this is (maxDepth - vertexDepth).
	Priority int

	// Depth is the vertex's distance from the deepest root.
	Depth int

	// EstimatedCost is an optional hint from the vertex options.
	EstimatedCost float64

	// Fn is the work function to execute.
	Fn func() error

	// index is the heap index, managed by container/heap.
	index int
}

// PriorityQueue implements a min-heap of Tasks, ordered by priority.
// Lower priority values are dequeued first (critical-path-first).
// It is safe for concurrent use.
type PriorityQueue struct {
	mu   sync.Mutex
	cond *sync.Cond
	h    taskHeap

	closed bool
}

// NewPriorityQueue creates a new priority queue.
func NewPriorityQueue() *PriorityQueue {
	pq := &PriorityQueue{
		h: make(taskHeap, 0, 64),
	}
	pq.cond = sync.NewCond(&pq.mu)
	heap.Init(&pq.h)
	return pq
}

// Push adds a task to the queue.
func (pq *PriorityQueue) Push(t *Task) {
	pq.mu.Lock()
	defer pq.mu.Unlock()
	if pq.closed {
		return
	}
	heap.Push(&pq.h, t)
	pq.cond.Signal()
}

// Pop removes and returns the highest-priority task. Blocks if the queue
// is empty. Returns nil if the queue is closed.
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

// TryPop returns the highest-priority task without blocking.
// Returns nil if the queue is empty.
func (pq *PriorityQueue) TryPop() *Task {
	pq.mu.Lock()
	defer pq.mu.Unlock()
	if pq.h.Len() == 0 {
		return nil
	}
	return heap.Pop(&pq.h).(*Task)
}

// Len returns the number of tasks in the queue.
func (pq *PriorityQueue) Len() int {
	pq.mu.Lock()
	defer pq.mu.Unlock()
	return pq.h.Len()
}

// Close closes the queue and wakes any blocked Pop callers.
func (pq *PriorityQueue) Close() {
	pq.mu.Lock()
	defer pq.mu.Unlock()
	pq.closed = true
	pq.cond.Broadcast()
}

// taskHeap implements heap.Interface for Task pointers.
type taskHeap []*Task

func (h taskHeap) Len() int { return len(h) }

func (h taskHeap) Less(i, j int) bool {
	// Primary: lower priority value first (critical path).
	if h[i].Priority != h[j].Priority {
		return h[i].Priority < h[j].Priority
	}
	// Secondary: higher estimated cost first.
	if h[i].EstimatedCost != h[j].EstimatedCost {
		return h[i].EstimatedCost > h[j].EstimatedCost
	}
	// Tertiary: deterministic by digest.
	return h[i].VertexDigest < h[j].VertexDigest
}

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
	old[n-1] = nil // avoid memory leak
	t.index = -1
	*h = old[:n-1]
	return t
}
