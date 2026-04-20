package reactdag

import (
	"context"
	"sync"
	"time"
)

// ---------------------------------------------------------------------------
// TestClock — deterministic time source for unit tests
// ---------------------------------------------------------------------------

// TestClock is a manually-advanced clock for use in tests.
// It replaces time.Now() calls in components that accept a ClockFn.
type TestClock struct {
	mu      sync.Mutex
	current time.Time
}

// NewTestClock creates a TestClock starting at t.
func NewTestClock(t time.Time) *TestClock { return &TestClock{current: t} }

// Now returns the current (simulated) time.
func (c *TestClock) Now() time.Time {
	c.mu.Lock()
	defer c.mu.Unlock()
	return c.current
}

// Advance moves the clock forward by d.
func (c *TestClock) Advance(d time.Duration) {
	c.mu.Lock()
	defer c.mu.Unlock()
	c.current = c.current.Add(d)
}

// Set sets the clock to an absolute time.
func (c *TestClock) Set(t time.Time) {
	c.mu.Lock()
	defer c.mu.Unlock()
	c.current = t
}

// ---------------------------------------------------------------------------
// RecordingExecutor — records every Execute call for test assertions
// ---------------------------------------------------------------------------

// ExecutionRecord is one recorded invocation of an Executor.
type ExecutionRecord struct {
	VertexID   string
	OpID       string
	InputFiles []FileRef
	CalledAt   time.Time
	Err        error
}

// RecordingExecutor wraps an inner Executor and records all invocations.
// Safe for concurrent use.
type RecordingExecutor struct {
	inner   Executor
	mu      sync.Mutex
	records []ExecutionRecord
}

// NewRecordingExecutor constructs a RecordingExecutor.
// If inner is nil, it wraps the default executor (which calls v.Op().Execute()
// and stores the output files). Pass a custom inner only when you want to
// replace the default execution behaviour entirely.
func NewRecordingExecutor(inner Executor) *RecordingExecutor {
	if inner == nil {
		inner = newDefaultExecutor(nil)
	}
	return &RecordingExecutor{inner: inner}
}

// Execute records the call and delegates to the inner executor.
func (r *RecordingExecutor) Execute(ctx context.Context, v *Vertex) error {
	err := r.inner.Execute(ctx, v)
	r.mu.Lock()
	r.records = append(r.records, ExecutionRecord{
		VertexID:   v.ID(),
		OpID:       v.OpID(),
		InputFiles: v.InputFiles(),
		CalledAt:   time.Now(),
		Err:        err,
	})
	r.mu.Unlock()
	return err
}

// Records returns a copy of all execution records in call order.
func (r *RecordingExecutor) Records() []ExecutionRecord {
	r.mu.Lock()
	defer r.mu.Unlock()
	cp := make([]ExecutionRecord, len(r.records))
	copy(cp, r.records)
	return cp
}

// CallCount returns the total number of Execute calls.
func (r *RecordingExecutor) CallCount() int {
	r.mu.Lock()
	defer r.mu.Unlock()
	return len(r.records)
}

// CallsFor returns records for a specific vertex ID.
func (r *RecordingExecutor) CallsFor(vertexID string) []ExecutionRecord {
	r.mu.Lock()
	defer r.mu.Unlock()
	var out []ExecutionRecord
	for _, rec := range r.records {
		if rec.VertexID == vertexID {
			out = append(out, rec)
		}
	}
	return out
}

// WasCalled reports whether a vertex was executed at least once.
func (r *RecordingExecutor) WasCalled(vertexID string) bool {
	return len(r.CallsFor(vertexID)) > 0
}

// Reset clears all recorded calls.
func (r *RecordingExecutor) Reset() {
	r.mu.Lock()
	r.records = r.records[:0]
	r.mu.Unlock()
}

// ---------------------------------------------------------------------------
// BuildHarness — table-driven test helper for Scheduler scenarios
// ---------------------------------------------------------------------------

// BuildHarness holds a pre-wired DAG + Scheduler for use in table-driven tests.
// Construct with NewBuildHarness, then call Run() for each scenario.
type BuildHarness struct {
	DAG        *DAG
	Scheduler  *Scheduler
	Recorder   *RecordingExecutor
	FastCache  *MemoryCacheStore
	EventBus   *EventBus
	Events     []Event
	eventsMu   sync.Mutex
}

// NewBuildHarness constructs a harness around the given DAG.
// The DAG must be sealed before calling NewBuildHarness.
func NewBuildHarness(d *DAG, opts ...Option) *BuildHarness {
	fast := NewMemoryCacheStore(0)
	bus := NewEventBus()
	rec := NewRecordingExecutor(nil)

	defaults := []Option{
		WithFastCache(fast),
		WithEventBus(bus),
		WithExecutor(rec),
		WithWorkerCount(4),
	}
	s := NewScheduler(d, append(defaults, opts...)...)

	h := &BuildHarness{
		DAG:       d,
		Scheduler: s,
		Recorder:  rec,
		FastCache: fast,
		EventBus:  bus,
	}
	// Capture all events.
	bus.SubscribeAll(func(_ context.Context, e Event) {
		h.eventsMu.Lock()
		h.Events = append(h.Events, e)
		h.eventsMu.Unlock()
	})
	return h
}

// Run executes a build and returns the metrics. It does NOT reset the DAG;
// call Reset() between scenarios if you want a fresh state.
func (h *BuildHarness) Run(ctx context.Context, targetID string, changedFiles []FileRef) (*BuildMetrics, error) {
	return h.Scheduler.Build(ctx, targetID, changedFiles)
}

// Reset resets all vertices to StateInitial and clears recorded calls and events.
func (h *BuildHarness) Reset() {
	for _, v := range h.DAG.All() {
		v.Reset()
	}
	h.Recorder.Reset()
	h.eventsMu.Lock()
	h.Events = h.Events[:0]
	h.eventsMu.Unlock()
}

// EventsOfType returns all captured events of the given type.
func (h *BuildHarness) EventsOfType(t EventType) []Event {
	h.eventsMu.Lock()
	defer h.eventsMu.Unlock()
	var out []Event
	for _, e := range h.Events {
		if e.Type == t {
			out = append(out, e)
		}
	}
	return out
}

// AssertExecuted panics-with-t if vertexID was not executed.
// Returns the execution records for the vertex.
func (h *BuildHarness) ExecutedVertices() []string {
	recs := h.Recorder.Records()
	seen := make(map[string]bool)
	var out []string
	for _, r := range recs {
		if !seen[r.VertexID] {
			seen[r.VertexID] = true
			out = append(out, r.VertexID)
		}
	}
	return out
}

// ---------------------------------------------------------------------------
// ScenarioOp — configurable operation for table-driven scenarios
// ---------------------------------------------------------------------------

// ScenarioOp is an Operation whose behaviour is configurable at construction
// time, making it ideal for table-driven test scenarios.
type ScenarioOp struct {
	id           string
	outputs      []FileRef
	err          error
	delay        time.Duration
	deriveHashes bool // mix input hashes into output hashes for realistic cache variation
}

// NewScenarioOp creates a configurable operation.
func NewScenarioOp(id string, opts ...ScenarioOpOption) *ScenarioOp {
	op := &ScenarioOp{id: id}
	for _, o := range opts {
		o(op)
	}
	return op
}

// ScenarioOpOption configures a ScenarioOp.
type ScenarioOpOption func(*ScenarioOp)

// WithOutputs sets the files the operation produces.
func WithOutputs(files ...FileRef) ScenarioOpOption {
	return func(op *ScenarioOp) { op.outputs = files }
}

// WithError makes the operation return err on Execute.
func WithError(err error) ScenarioOpOption {
	return func(op *ScenarioOp) { op.err = err }
}

// WithDelay adds a simulated execution delay.
func WithDelay(d time.Duration) ScenarioOpOption {
	return func(op *ScenarioOp) { op.delay = d }
}

// WithDeriveOutputHashes makes Execute mix input file hashes into output file
// hashes, so that changing an input produces a different output hash. This
// enables realistic incremental-build tests where cache keys vary with content.
func WithDeriveOutputHashes() ScenarioOpOption {
	return func(op *ScenarioOp) { op.deriveHashes = true }
}

// ID satisfies Operation.
func (o *ScenarioOp) ID() string { return "scenario:" + o.id }

// Execute satisfies Operation. If WithDeriveOutputHashes() is set, output
// file hashes are derived from the input file hashes so that changing an input
// propagates a different hash to downstream vertices (realistic cache behaviour).
func (o *ScenarioOp) Execute(ctx context.Context, inputs []FileRef) ([]FileRef, error) {
	if o.delay > 0 {
		select {
		case <-time.After(o.delay):
		case <-ctx.Done():
			return nil, ctx.Err()
		}
	}
	if o.err != nil {
		return nil, o.err
	}
	if !o.deriveHashes || len(o.outputs) == 0 || len(inputs) == 0 {
		return o.outputs, nil
	}
	// Mix all input hashes into output file hashes via XOR so that different
	// input content produces different output content.
	var combined [32]byte
	for _, inp := range inputs {
		for i, b := range inp.Hash {
			combined[i] ^= b
		}
	}
	derived := make([]FileRef, len(o.outputs))
	copy(derived, o.outputs)
	for i := range derived {
		for j, b := range combined {
			derived[i].Hash[j] ^= b
		}
	}
	return derived, nil
}
