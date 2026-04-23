package reactdag

import (
	"errors"
	"fmt"
	"sync"
	"time"
)

// Vertex is a node in the reactive DAG. All exported methods are safe for
// concurrent use. Graph structure fields (parents, children) are set during
// DAG construction and treated as read-only once Build starts.
type Vertex struct {
	mu sync.RWMutex

	// --- Immutable identity (set at construction) ---
	id     string
	op     Operation
	labels map[string]string

	// --- Graph structure (written during construction, read during execution) ---
	parents  []*Vertex
	children []*Vertex
	// fileDeps narrows which of each parent's output files this vertex reads.
	fileDeps []FileDependency

	// --- Mutable execution state (always under mu) ---
	state       State
	cacheKey    CacheKey
	inputFiles  []FileRef // resolved inputs consumed before execution
	outputFiles []FileRef // files produced by this vertex
	err         error     // live or cached error
	metrics     VertexMetrics

	// generation is incremented on every Reset() so the Scheduler's gate map
	// can distinguish "fresh" from "stale" gates.
	generation uint64
}

// NewVertex constructs a Vertex. id must be non-empty; op must be non-nil.
func NewVertex(id string, op Operation) *Vertex {
	return &Vertex{
		id:     id,
		op:     op,
		labels: make(map[string]string),
		state:  StateInitial,
	}
}

// ---------------------------------------------------------------------------
// Identity
// ---------------------------------------------------------------------------

// ID returns the vertex's unique identifier.
func (v *Vertex) ID() string { return v.id }

// Op returns the underlying operation.
func (v *Vertex) Op() Operation { return v.op }

// OpID returns the operation's stable content-addressable ID.
func (v *Vertex) OpID() string {
	if v.op == nil {
		return ""
	}
	return v.op.ID()
}

// Label returns the value of a label key, and whether it was set.
func (v *Vertex) Label(key string) (string, bool) {
	v.mu.RLock()
	defer v.mu.RUnlock()
	val, ok := v.labels[key]
	return val, ok
}

// SetLabel attaches an arbitrary key-value label to the vertex.
func (v *Vertex) SetLabel(key, value string) {
	v.mu.Lock()
	defer v.mu.Unlock()
	v.labels[key] = value
}

// Labels returns a shallow copy of all labels.
func (v *Vertex) Labels() map[string]string {
	v.mu.RLock()
	defer v.mu.RUnlock()
	cp := make(map[string]string, len(v.labels))
	for k, val := range v.labels {
		cp[k] = val
	}
	return cp
}

// ---------------------------------------------------------------------------
// State machine
// ---------------------------------------------------------------------------

// State returns the current execution state (snapshot).
func (v *Vertex) State() State {
	v.mu.RLock()
	defer v.mu.RUnlock()
	return v.state
}

// Err returns the error associated with this vertex (populated on StateFailed).
func (v *Vertex) Err() error {
	v.mu.RLock()
	defer v.mu.RUnlock()
	return v.err
}

// SetState transitions to the target state, recording the human-readable cause.
// Returns an error if the transition violates the state machine rules.
func (v *Vertex) SetState(to State, cause string) error {
	v.mu.Lock()
	defer v.mu.Unlock()
	return v.transitionLocked(to, cause)
}

// SetFailed transitions to StateFailed and stores the error.
func (v *Vertex) SetFailed(err error, cause string) error {
	v.mu.Lock()
	defer v.mu.Unlock()
	v.err = err
	return v.transitionLocked(StateFailed, cause)
}

// transitionLocked is the only function allowed to mutate v.state.
// Caller must hold v.mu for writing.
func (v *Vertex) transitionLocked(to State, cause string) error {
	if !isTransitionAllowed(v.state, to) {
		return fmt.Errorf("vertex %q: illegal transition %s → %s (%s)",
			v.id, v.state, to, cause)
	}
	v.metrics.Transitions = append(v.metrics.Transitions, StateTransition{
		From:      v.state,
		To:        to,
		Timestamp: time.Now(),
		Cause:     cause,
	})
	v.state = to
	return nil
}

// isTransitionAllowed encodes the state machine adjacency rules.
//
//	initial → fast_cache | slow_cache | completed | failed
//	fast_cache | slow_cache → completed | failed
//	completed | failed → (terminal; only Reset() can restart)
func isTransitionAllowed(from, to State) bool {
	switch from {
	case StateInitial:
		return to != StateInitial
	case StateFastCache, StateSlowCache:
		return to == StateCompleted || to == StateFailed
	default:
		return false // completed and failed are terminal
	}
}

// Reset forces the vertex back to StateInitial and clears all execution data.
// OutputFiles are intentionally preserved: the InvalidationEngine needs the
// last-known outputs to propagate file-change invalidation to downstream
// consumers. The generation counter is incremented so the Scheduler's gate
// map treats the next execution as a fresh epoch.
func (v *Vertex) Reset() {
	v.mu.Lock()
	defer v.mu.Unlock()
	v.state = StateInitial
	v.cacheKey = CacheKey{}
	v.inputFiles = nil
	// v.outputFiles intentionally NOT cleared — see doc above.
	v.err = nil
	v.metrics = VertexMetrics{}
	v.generation++
}

// Generation returns the current reset-epoch counter.
// Each call to Reset() increments this value.
func (v *Vertex) Generation() uint64 {
	v.mu.RLock()
	defer v.mu.RUnlock()
	return v.generation
}

// ---------------------------------------------------------------------------
// File dependencies (fine-grained invalidation)
// ---------------------------------------------------------------------------

// AddFileDependency registers that this vertex consumes only the listed paths
// from the named parent's output. If paths is nil, all parent outputs are assumed.
func (v *Vertex) AddFileDependency(parentID string, paths []string) {
	v.mu.Lock()
	defer v.mu.Unlock()
	v.fileDeps = append(v.fileDeps, FileDependency{ParentID: parentID, Paths: paths})
}

// FileDependencies returns a copy of the fine-grained dependency list.
func (v *Vertex) FileDependencies() []FileDependency {
	v.mu.RLock()
	defer v.mu.RUnlock()
	cp := make([]FileDependency, len(v.fileDeps))
	copy(cp, v.fileDeps)
	return cp
}

// FileDependencyForParent returns the declared paths consumed from a specific parent,
// and whether an explicit declaration exists. nil paths means "all outputs".
func (v *Vertex) FileDependencyForParent(parentID string) ([]string, bool) {
	v.mu.RLock()
	defer v.mu.RUnlock()
	for _, d := range v.fileDeps {
		if d.ParentID == parentID {
			return d.Paths, true
		}
	}
	return nil, false
}

// ---------------------------------------------------------------------------
// File I/O
// ---------------------------------------------------------------------------

// SetInputFiles records the resolved inputs consumed before execution.
func (v *Vertex) SetInputFiles(refs []FileRef) {
	v.mu.Lock()
	defer v.mu.Unlock()
	v.inputFiles = refs
}

// InputFiles returns the resolved input file list.
func (v *Vertex) InputFiles() []FileRef {
	v.mu.RLock()
	defer v.mu.RUnlock()
	return v.inputFiles
}

// SetOutputFiles records the files produced by this vertex.
func (v *Vertex) SetOutputFiles(refs []FileRef) {
	v.mu.Lock()
	defer v.mu.Unlock()
	v.outputFiles = refs
}

// OutputFiles returns the files produced by this vertex.
func (v *Vertex) OutputFiles() []FileRef {
	v.mu.RLock()
	defer v.mu.RUnlock()
	return v.outputFiles
}

// OutputFileByPath looks up a produced file by its path. Returns (FileRef{}, false) on miss.
func (v *Vertex) OutputFileByPath(path string) (FileRef, bool) {
	v.mu.RLock()
	defer v.mu.RUnlock()
	for _, f := range v.outputFiles {
		if f.Path == path {
			return f, true
		}
	}
	return FileRef{}, false
}

// ---------------------------------------------------------------------------
// Cache key
// ---------------------------------------------------------------------------

// CacheKey returns the computed content-addressable key.
func (v *Vertex) CacheKey() CacheKey {
	v.mu.RLock()
	defer v.mu.RUnlock()
	return v.cacheKey
}

// SetCacheKey stores the pre-computed cache key.
func (v *Vertex) SetCacheKey(k CacheKey) {
	v.mu.Lock()
	defer v.mu.Unlock()
	v.cacheKey = k
}

// ---------------------------------------------------------------------------
// Metrics
// ---------------------------------------------------------------------------

// Metrics returns a snapshot of the vertex's performance counters.
func (v *Vertex) Metrics() VertexMetrics {
	v.mu.RLock()
	defer v.mu.RUnlock()
	return v.metrics
}

// RecordQueued stamps the time the vertex entered the work queue.
func (v *Vertex) RecordQueued() {
	v.mu.Lock()
	defer v.mu.Unlock()
	v.metrics.QueuedAt = time.Now()
}

// RecordStart stamps execution start.
func (v *Vertex) RecordStart() {
	v.mu.Lock()
	defer v.mu.Unlock()
	v.metrics.StartedAt = time.Now()
}

// RecordFinish stamps execution end.
func (v *Vertex) RecordFinish() {
	v.mu.Lock()
	defer v.mu.Unlock()
	v.metrics.FinishedAt = time.Now()
}

// RecordFileAccess adds I/O counters observed by the FileTracker.
func (v *Vertex) RecordFileAccess(filesRead, filesWritten int, bytesRead, bytesWritten int64) {
	v.mu.Lock()
	defer v.mu.Unlock()
	v.metrics.FilesRead += filesRead
	v.metrics.FilesWritten += filesWritten
	v.metrics.BytesRead += bytesRead
	v.metrics.BytesWritten += bytesWritten
}

// ---------------------------------------------------------------------------
// Graph accessors (read-only during execution)
// ---------------------------------------------------------------------------

// Parents returns the direct parent vertices.
func (v *Vertex) Parents() []*Vertex {
	v.mu.RLock()
	defer v.mu.RUnlock()
	return v.parents
}

// Children returns the direct child vertices.
func (v *Vertex) Children() []*Vertex {
	v.mu.RLock()
	defer v.mu.RUnlock()
	return v.children
}

// ---------------------------------------------------------------------------
// Validation
// ---------------------------------------------------------------------------

// Validate checks that the vertex is well-formed for use in a DAG.
func (v *Vertex) Validate() error {
	if v.id == "" {
		return errors.New("vertex: id must not be empty")
	}
	if v.op == nil {
		return errors.New("vertex: operation must not be nil")
	}
	return nil
}
