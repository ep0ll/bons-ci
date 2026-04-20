package reactdag

import (
	"context"
	"fmt"
	"sync"
	"time"
)

// ---------------------------------------------------------------------------
// BuildRequest — a single queued build
// ---------------------------------------------------------------------------

// BuildRequest describes a build to be run by the BuildQueue.
type BuildRequest struct {
	// ID is a caller-assigned identifier for correlation (e.g. CI job ID).
	ID string
	// TargetID is the vertex to build toward.
	TargetID string
	// ChangedFiles is the set of changed files (nil = clean build).
	ChangedFiles []FileRef
	// Priority controls ordering: higher values run first.
	Priority int
	// Deadline, if non-zero, is when the build must complete by.
	Deadline time.Time
	// Labels are arbitrary key-value metadata propagated to BuildRecord.
	Labels map[string]string
}

// BuildResponse is the result of a queued build.
type BuildResponse struct {
	Request  BuildRequest
	Result   BuildResult
	QueuedAt time.Time
	StartedAt time.Time
}

// WaitTime returns how long the request waited in the queue.
func (r BuildResponse) WaitTime() time.Duration {
	return r.StartedAt.Sub(r.QueuedAt)
}

// ---------------------------------------------------------------------------
// BuildQueue — ordered, deduplicating build request queue
// ---------------------------------------------------------------------------

// BuildQueue serialises build requests through an Engine, applying priority
// ordering and deduplication. Multiple callers can submit builds concurrently;
// the queue ensures at most one build runs at a time per target (configurable).
//
// Deduplication: if a request for the same targetID is already pending or
// running, a second identical request is merged into the existing one. Both
// callers receive the same BuildResponse when it completes.
type BuildQueue struct {
	engine     *Engine
	mu         sync.Mutex
	pending    []*queueEntry   // ordered by priority
	inFlight   map[string]*queueEntry // targetID → active entry
	maxParallel int
	workers    int
	workCh     chan *queueEntry
	stopCh     chan struct{}
	wg         sync.WaitGroup
}

type queueEntry struct {
	req      BuildRequest
	queuedAt time.Time
	// subscribers all receive the same result when done.
	mu      sync.Mutex
	subs    []chan BuildResponse
	done    bool
	result  BuildResponse
}

func (e *queueEntry) subscribe() <-chan BuildResponse {
	ch := make(chan BuildResponse, 1)
	e.mu.Lock()
	if e.done {
		ch <- e.result
	} else {
		e.subs = append(e.subs, ch)
	}
	e.mu.Unlock()
	return ch
}

func (e *queueEntry) complete(resp BuildResponse) {
	e.mu.Lock()
	e.done = true
	e.result = resp
	for _, sub := range e.subs {
		sub <- resp
	}
	e.subs = nil
	e.mu.Unlock()
}

// NewBuildQueue constructs a BuildQueue.
// maxParallel is the maximum number of concurrent builds (default 1).
func NewBuildQueue(engine *Engine, maxParallel int) *BuildQueue {
	if maxParallel < 1 {
		maxParallel = 1
	}
	q := &BuildQueue{
		engine:      engine,
		inFlight:    make(map[string]*queueEntry),
		maxParallel: maxParallel,
		workCh:      make(chan *queueEntry, 64),
		stopCh:      make(chan struct{}),
	}
	return q
}

// Start launches background worker goroutines. Call Stop() when done.
func (q *BuildQueue) Start(ctx context.Context) {
	for i := 0; i < q.maxParallel; i++ {
		q.wg.Add(1)
		go q.workerLoop(ctx)
	}
	q.wg.Add(1)
	go q.dispatchLoop(ctx)
}

// Stop signals the queue to shut down and waits for all workers to finish.
func (q *BuildQueue) Stop() {
	close(q.stopCh)
	q.wg.Wait()
}

// Submit enqueues a build request and returns a channel that receives the
// BuildResponse when the build completes. If an identical targetID is already
// pending, the caller is attached to the existing entry (deduplication).
func (q *BuildQueue) Submit(req BuildRequest) <-chan BuildResponse {
	q.mu.Lock()
	defer q.mu.Unlock()

	// Deduplication: return existing pending entry for same target.
	for _, e := range q.pending {
		if e.req.TargetID == req.TargetID {
			// Upgrade priority if new request is higher.
			if req.Priority > e.req.Priority {
				e.req.Priority = req.Priority
				q.sortPendingLocked()
			}
			return e.subscribe()
		}
	}
	// Also deduplicate against in-flight.
	if e, ok := q.inFlight[req.TargetID]; ok {
		return e.subscribe()
	}

	entry := &queueEntry{req: req, queuedAt: time.Now()}
	q.pending = append(q.pending, entry)
	q.sortPendingLocked()
	ch := entry.subscribe()
	return ch
}

// Pending returns the number of requests waiting to be dispatched.
func (q *BuildQueue) Pending() int {
	q.mu.Lock()
	defer q.mu.Unlock()
	return len(q.pending)
}

// InFlight returns the number of builds currently running.
func (q *BuildQueue) InFlight() int {
	q.mu.Lock()
	defer q.mu.Unlock()
	return len(q.inFlight)
}

// ---------------------------------------------------------------------------
// Internal loops
// ---------------------------------------------------------------------------

// dispatchLoop feeds the highest-priority pending entry to the work channel
// whenever a worker is available.
func (q *BuildQueue) dispatchLoop(ctx context.Context) {
	defer q.wg.Done()
	for {
		select {
		case <-ctx.Done():
			return
		case <-q.stopCh:
			return
		default:
		}

		q.mu.Lock()
		if len(q.pending) == 0 || len(q.inFlight) >= q.maxParallel {
			q.mu.Unlock()
			time.Sleep(5 * time.Millisecond)
			continue
		}
		entry := q.pending[0]
		q.pending = q.pending[1:]
		q.inFlight[entry.req.TargetID] = entry
		q.mu.Unlock()

		select {
		case q.workCh <- entry:
		case <-ctx.Done():
			return
		case <-q.stopCh:
			return
		}
	}
}

// workerLoop pulls entries from workCh and runs builds.
func (q *BuildQueue) workerLoop(ctx context.Context) {
	defer q.wg.Done()
	for {
		select {
		case <-ctx.Done():
			return
		case <-q.stopCh:
			return
		case entry, ok := <-q.workCh:
			if !ok {
				return
			}
			q.runBuild(ctx, entry)
		}
	}
}

func (q *BuildQueue) runBuild(ctx context.Context, entry *queueEntry) {
	req := entry.req

	// Apply deadline if set.
	buildCtx := ctx
	var cancel context.CancelFunc
	if !req.Deadline.IsZero() {
		buildCtx, cancel = context.WithDeadline(ctx, req.Deadline)
		defer cancel()
	}

	// Propagate labels to history.
	startedAt := time.Now()
	result := q.engine.Build(buildCtx, req.TargetID, req.ChangedFiles)
	if result.Record.Labels == nil {
		result.Record.Labels = make(map[string]string)
	}
	for k, v := range req.Labels {
		result.Record.Labels[k] = v
	}

	resp := BuildResponse{
		Request:   req,
		Result:    result,
		QueuedAt:  entry.queuedAt,
		StartedAt: startedAt,
	}
	entry.complete(resp)

	q.mu.Lock()
	delete(q.inFlight, req.TargetID)
	q.mu.Unlock()
}

// sortPendingLocked sorts pending entries by priority descending, then FIFO.
// Caller must hold q.mu.
func (q *BuildQueue) sortPendingLocked() {
	n := len(q.pending)
	for i := 1; i < n; i++ {
		for j := i; j > 0 && q.pending[j].req.Priority > q.pending[j-1].req.Priority; j-- {
			q.pending[j], q.pending[j-1] = q.pending[j-1], q.pending[j]
		}
	}
}

// QueueStats is a point-in-time snapshot of the queue state.
type QueueStats struct {
	Pending  int
	InFlight int
}

// Stats returns a snapshot of queue occupancy.
func (q *BuildQueue) Stats() QueueStats {
	q.mu.Lock()
	defer q.mu.Unlock()
	return QueueStats{Pending: len(q.pending), InFlight: len(q.inFlight)}
}

// ---------------------------------------------------------------------------
// SyncBuild — convenience wrapper for synchronous (blocking) queue usage
// ---------------------------------------------------------------------------

// SyncBuild submits a request and blocks until it completes.
func (q *BuildQueue) SyncBuild(ctx context.Context, req BuildRequest) (BuildResponse, error) {
	ch := q.Submit(req)
	select {
	case resp := <-ch:
		return resp, resp.Result.Error
	case <-ctx.Done():
		return BuildResponse{}, fmt.Errorf("build queue: context cancelled: %w", ctx.Err())
	}
}
