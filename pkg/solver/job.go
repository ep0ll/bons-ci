package solver

import (
	"context"
	"fmt"
	"sync"
	"sync/atomic"
	"time"

	digest "github.com/opencontainers/go-digest"
)

// Job represents a single solve session. It provides lifecycle management,
// progress tracking, cancellation propagation, and session-scoped storage.
// This is equivalent to BuildKit's Job in jobs.go.
type Job struct {
	id        string
	createdAt time.Time
	cancel    context.CancelFunc
	ctx       context.Context

	// mu protects values and session state.
	mu     sync.Mutex
	values map[string]any

	// progressCh receives status updates from the solve loop.
	progressCh chan *SolveStatus

	// completed is atomically set to 1 when the job finishes.
	completed int64
}

// NewJob creates a new solve job with a unique ID.
func NewJob(ctx context.Context, id string) *Job {
	ctx, cancel := context.WithCancel(ctx)
	return &Job{
		id:         id,
		createdAt:  time.Now(),
		cancel:     cancel,
		ctx:        ctx,
		values:     make(map[string]any),
		progressCh: make(chan *SolveStatus, 256),
	}
}

// ID returns the job identifier.
func (j *Job) ID() string { return j.id }

// CreatedAt returns when the job was created.
func (j *Job) CreatedAt() time.Time { return j.createdAt }

// Context returns the job's context, which is cancelled when Discard is called.
func (j *Job) Context() context.Context { return j.ctx }

// SetValue stores a session-scoped key-value pair.
func (j *Job) SetValue(key string, val any) {
	j.mu.Lock()
	defer j.mu.Unlock()
	j.values[key] = val
}

// Value retrieves a session-scoped value.
func (j *Job) Value(key string) (any, bool) {
	j.mu.Lock()
	defer j.mu.Unlock()
	val, ok := j.values[key]
	return val, ok
}

// Discard cancels the job and releases resources.
func (j *Job) Discard() {
	atomic.StoreInt64(&j.completed, 1)
	j.cancel()
	close(j.progressCh)
}

// IsCompleted returns whether the job has been discarded.
func (j *Job) IsCompleted() bool {
	return atomic.LoadInt64(&j.completed) == 1
}

// InContext runs fn in the job's context.
func (j *Job) InContext(fn func(context.Context) error) error {
	return fn(j.ctx)
}

// Status streams solve status updates to the caller.
// Blocks until the job completes or the context is cancelled.
func (j *Job) Status(ctx context.Context, ch chan *SolveStatus) error {
	defer close(ch)
	for {
		select {
		case <-ctx.Done():
			return ctx.Err()
		case ss, ok := <-j.progressCh:
			if !ok {
				return nil // job completed
			}
			select {
			case ch <- ss:
			case <-ctx.Done():
				return ctx.Err()
			}
		}
	}
}

// sendStatus sends a status update to the progress channel (non-blocking).
func (j *Job) sendStatus(ss *SolveStatus) {
	select {
	case j.progressCh <- ss:
	default:
		// Drop on full — slow consumer.
	}
}

// ─── Job Registry ────────────────────────────────────────────────────────────

// JobRegistry manages active jobs and prevents duplicate solves.
type JobRegistry struct {
	mu   sync.Mutex
	jobs map[string]*Job
}

// NewJobRegistry creates a new job registry.
func NewJobRegistry() *JobRegistry {
	return &JobRegistry{
		jobs: make(map[string]*Job),
	}
}

// Create creates and registers a new job. Returns an error if a job
// with the same ID already exists.
func (r *JobRegistry) Create(ctx context.Context, id string) (*Job, error) {
	r.mu.Lock()
	defer r.mu.Unlock()
	if _, ok := r.jobs[id]; ok {
		return nil, fmt.Errorf("job %s already exists", id)
	}
	j := NewJob(ctx, id)
	r.jobs[id] = j
	return j, nil
}

// Get returns an active job by ID.
func (r *JobRegistry) Get(id string) (*Job, bool) {
	r.mu.Lock()
	defer r.mu.Unlock()
	j, ok := r.jobs[id]
	return j, ok
}

// Remove removes a job from the registry and discards it.
func (r *JobRegistry) Remove(id string) {
	r.mu.Lock()
	defer r.mu.Unlock()
	if j, ok := r.jobs[id]; ok {
		j.Discard()
		delete(r.jobs, id)
	}
}

// Active returns all active job IDs.
func (r *JobRegistry) Active() []string {
	r.mu.Lock()
	defer r.mu.Unlock()
	ids := make([]string, 0, len(r.jobs))
	for id := range r.jobs {
		ids = append(ids, id)
	}
	return ids
}

// ─── Session integration ─────────────────────────────────────────────────────

// SessionGroup provides session multiplexing for a job. Multiple frontends
// can share sessions through this interface.
type SessionGroup interface {
	SessionIterator() SessionIterator
}

// SessionIterator iterates over sessions in a group.
type SessionIterator interface {
	NextSession() (string, bool)
}

// Builder enables sub-builds within a solve. A vertex Op can call
// Build to recursively solve a sub-DAG.
type Builder interface {
	Build(ctx context.Context, edge Edge) (CachedResult, error)
}

// VertexStatus tracks the execution state of a single vertex.
type VertexStatus struct {
	ID        string
	Vertex    digest.Digest
	Name      string
	Total     int64
	Current   int64
	Timestamp time.Time
	Started   *time.Time
	Completed *time.Time
}

// VertexLog holds log output from a vertex execution.
type VertexLog struct {
	Vertex    digest.Digest
	Stream    int
	Data      []byte
	Timestamp time.Time
}

// VertexWarning holds a warning from a vertex execution.
type VertexWarning struct {
	Vertex digest.Digest
	Level  int
	Short  []byte
	Detail [][]byte
}
