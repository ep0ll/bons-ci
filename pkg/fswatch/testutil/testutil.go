// Package testutil provides test helpers, in-memory fakes, and fixture builders
// for testing code that uses the fanwatch package.
//
// None of this code should be imported in production binaries.
package testutil

import (
	"context"
	"fmt"
	"os"
	"path/filepath"
	"sync"
	"time"

	fswatch "github.com/bons/bons-ci/pkg/fswatch"
)

// ─────────────────────────────────────────────────────────────────────────────
// FakeWatcher — in-memory Watcher for unit tests
// ─────────────────────────────────────────────────────────────────────────────

// FakeWatcher is a [fswatch.Watcher] that emits events injected by the test.
// It never touches the filesystem or the fanotify subsystem.
type FakeWatcher struct {
	mu     sync.Mutex
	out    chan *fswatch.RawEvent
	closed bool
}

// NewFakeWatcher returns a [FakeWatcher] with the given event channel buffer.
func NewFakeWatcher(bufSize int) *FakeWatcher {
	return &FakeWatcher{
		out: make(chan *fswatch.RawEvent, bufSize),
	}
}

// Watch implements [fswatch.Watcher].
func (f *FakeWatcher) Watch(_ context.Context) (<-chan *fswatch.RawEvent, error) {
	return f.out, nil
}

// Close implements [fswatch.Watcher]. Safe to call multiple times.
func (f *FakeWatcher) Close() error {
	f.mu.Lock()
	defer f.mu.Unlock()
	if !f.closed {
		f.closed = true
		close(f.out)
	}
	return nil
}

// Send injects a single [fswatch.RawEvent]. Blocks when channel is full.
func (f *FakeWatcher) Send(e *fswatch.RawEvent) { f.out <- e }

// SendMany injects multiple events in order.
func (f *FakeWatcher) SendMany(events []*fswatch.RawEvent) {
	for _, e := range events {
		f.out <- e
	}
}

// ─────────────────────────────────────────────────────────────────────────────
// RawEventBuilder — fluent builder for RawEvent test fixtures
// ─────────────────────────────────────────────────────────────────────────────

// RawEventBuilder constructs [fswatch.RawEvent] values for tests.
type RawEventBuilder struct {
	event fswatch.RawEvent
}

// NewRawEvent returns a builder pre-populated with sensible defaults.
func NewRawEvent() *RawEventBuilder {
	return &RawEventBuilder{
		event: fswatch.RawEvent{
			Mask:      fswatch.MaskReadOnly,
			PID:       1000,
			Path:      "/merged/test/file.txt",
			Timestamp: time.Now(),
		},
	}
}

// WithMask sets the event mask.
func (b *RawEventBuilder) WithMask(m fswatch.EventMask) *RawEventBuilder {
	b.event.Mask = m
	return b
}

// WithOp sets the event mask to a single operation.
func (b *RawEventBuilder) WithOp(op fswatch.Op) *RawEventBuilder {
	b.event.Mask = fswatch.EventMask(op)
	return b
}

// WithPID sets the triggering process ID.
func (b *RawEventBuilder) WithPID(pid int32) *RawEventBuilder {
	b.event.PID = pid
	return b
}

// WithPath sets the absolute file path.
func (b *RawEventBuilder) WithPath(path string) *RawEventBuilder {
	b.event.Path = path
	return b
}

// WithTimestamp sets the event timestamp.
func (b *RawEventBuilder) WithTimestamp(t time.Time) *RawEventBuilder {
	b.event.Timestamp = t
	return b
}

// Build returns a pointer to the constructed [fswatch.RawEvent].
// Each call returns a fresh copy.
func (b *RawEventBuilder) Build() *fswatch.RawEvent {
	e := b.event
	return &e
}

// ─────────────────────────────────────────────────────────────────────────────
// EnrichedEventBuilder — fluent builder for EnrichedEvent test fixtures
// ─────────────────────────────────────────────────────────────────────────────

// EnrichedEventBuilder constructs [fswatch.EnrichedEvent] values for tests.
type EnrichedEventBuilder struct {
	raw     *RawEventBuilder
	overlay *fswatch.OverlayInfo
	layer   *fswatch.SnapshotLayer
	process *fswatch.ProcessInfo
	attrs   map[string]any
}

// NewEnrichedEvent returns a builder pre-populated with sensible defaults.
func NewEnrichedEvent() *EnrichedEventBuilder {
	return &EnrichedEventBuilder{
		raw:   NewRawEvent(),
		attrs: make(map[string]any),
	}
}

// WithMask sets the event mask.
func (b *EnrichedEventBuilder) WithMask(m fswatch.EventMask) *EnrichedEventBuilder {
	b.raw.WithMask(m)
	return b
}

// WithOp sets the mask to a single operation.
func (b *EnrichedEventBuilder) WithOp(op fswatch.Op) *EnrichedEventBuilder {
	b.raw.WithOp(op)
	return b
}

// WithPID sets the triggering PID.
func (b *EnrichedEventBuilder) WithPID(pid int32) *EnrichedEventBuilder {
	b.raw.WithPID(pid)
	return b
}

// WithPath sets the file path.
func (b *EnrichedEventBuilder) WithPath(path string) *EnrichedEventBuilder {
	b.raw.WithPath(path)
	return b
}

// WithOverlay attaches overlay info.
func (b *EnrichedEventBuilder) WithOverlay(o *fswatch.OverlayInfo) *EnrichedEventBuilder {
	b.overlay = o
	return b
}

// WithSourceLayer attaches a layer.
func (b *EnrichedEventBuilder) WithSourceLayer(l *fswatch.SnapshotLayer) *EnrichedEventBuilder {
	b.layer = l
	return b
}

// WithProcess attaches process info.
func (b *EnrichedEventBuilder) WithProcess(p *fswatch.ProcessInfo) *EnrichedEventBuilder {
	b.process = p
	return b
}

// WithAttr sets a key-value attribute on the event.
func (b *EnrichedEventBuilder) WithAttr(key string, value any) *EnrichedEventBuilder {
	b.attrs[key] = value
	return b
}

// Build returns the constructed [fswatch.EnrichedEvent].
func (b *EnrichedEventBuilder) Build() *fswatch.EnrichedEvent {
	raw := b.raw.Build()
	e := &fswatch.EnrichedEvent{
		Event: fswatch.Event{
			RawEvent: *raw,
			Dir:      filepath.Dir(raw.Path),
			Name:     filepath.Base(raw.Path),
			// WatcherID promoted from embedded RawEvent.
		},
		Overlay:     b.overlay,
		SourceLayer: b.layer,
		Process:     b.process,
	}
	for k, v := range b.attrs {
		e.SetAttr(k, v)
	}
	return e
}

// ─────────────────────────────────────────────────────────────────────────────
// OverlayFixture — temporary overlay directory tree for integration tests
// ─────────────────────────────────────────────────────────────────────────────

// OverlayFixture holds paths of a temporary overlay directory tree.
type OverlayFixture struct {
	// Root is the temp directory containing all overlay subdirectories.
	Root string
	// MergedDir is the simulated merged view path.
	MergedDir string
	// UpperDir is the simulated writable layer path.
	UpperDir string
	// WorkDir is the simulated overlay work directory path.
	WorkDir string
	// LowerDirs are the simulated read-only layer paths, topmost first.
	LowerDirs []string
	// Overlay is the [fswatch.OverlayInfo] built from these paths.
	Overlay *fswatch.OverlayInfo
}

// NewOverlayFixture creates a temporary overlay structure with n lower layers
// under root. Call Cleanup() (or register with t.Cleanup) when done.
func NewOverlayFixture(root string, lowerLayerCount int) (*OverlayFixture, error) {
	if lowerLayerCount < 1 {
		lowerLayerCount = 1
	}

	merged := filepath.Join(root, "merged")
	upper := filepath.Join(root, "upper")
	work := filepath.Join(root, "work")

	for _, dir := range []string{merged, upper, work} {
		if err := os.MkdirAll(dir, 0o755); err != nil {
			return nil, fmt.Errorf("testutil: mkdir %s: %w", dir, err)
		}
	}

	lowerDirs := make([]string, lowerLayerCount)
	for i := range lowerLayerCount {
		d := filepath.Join(root, fmt.Sprintf("lower%d", i))
		if err := os.MkdirAll(d, 0o755); err != nil {
			return nil, fmt.Errorf("testutil: mkdir lower%d: %w", i, err)
		}
		lowerDirs[i] = d
	}

	overlay := fswatch.NewOverlayInfo(merged, upper, work, lowerDirs)

	return &OverlayFixture{
		Root:      root,
		MergedDir: merged,
		UpperDir:  upper,
		WorkDir:   work,
		LowerDirs: lowerDirs,
		Overlay:   overlay,
	}, nil
}

// WriteFile creates a file at relPath (relative to MergedDir) with content.
func (f *OverlayFixture) WriteFile(relPath, content string) error {
	abs := filepath.Join(f.MergedDir, relPath)
	if err := os.MkdirAll(filepath.Dir(abs), 0o755); err != nil {
		return err
	}
	return os.WriteFile(abs, []byte(content), 0o644)
}

// WriteLayerFile creates a file inside the Nth lower layer directory.
func (f *OverlayFixture) WriteLayerFile(layerIndex int, relPath, content string) error {
	if layerIndex < 0 || layerIndex >= len(f.LowerDirs) {
		return fmt.Errorf("testutil: layer index %d out of range [0,%d)", layerIndex, len(f.LowerDirs))
	}
	abs := filepath.Join(f.LowerDirs[layerIndex], relPath)
	if err := os.MkdirAll(filepath.Dir(abs), 0o755); err != nil {
		return err
	}
	return os.WriteFile(abs, []byte(content), 0o644)
}

// WriteUpperFile creates a file in the upper (writable) layer directory.
func (f *OverlayFixture) WriteUpperFile(relPath, content string) error {
	abs := filepath.Join(f.UpperDir, relPath)
	if err := os.MkdirAll(filepath.Dir(abs), 0o755); err != nil {
		return err
	}
	return os.WriteFile(abs, []byte(content), 0o644)
}

// Cleanup removes the entire root directory tree.
func (f *OverlayFixture) Cleanup() { os.RemoveAll(f.Root) }

// MergedPath returns the absolute merged path for a relative path.
func (f *OverlayFixture) MergedPath(relPath string) string {
	return filepath.Join(f.MergedDir, relPath)
}

// ─────────────────────────────────────────────────────────────────────────────
// CapturingMiddleware — records events for test assertions
// ─────────────────────────────────────────────────────────────────────────────

// CapturingMiddleware records every [fswatch.EnrichedEvent] that reaches the
// wrapped handler. Use Events() or WaitForN() to assert pipeline output.
type CapturingMiddleware struct {
	mu     sync.Mutex
	events []*fswatch.EnrichedEvent
}

// Wrap implements [fswatch.Middleware].
func (c *CapturingMiddleware) Wrap(next fswatch.Handler) fswatch.Handler {
	return fswatch.HandlerFunc(func(ctx context.Context, e *fswatch.EnrichedEvent) error {
		clone := e.Clone()
		c.mu.Lock()
		c.events = append(c.events, clone)
		c.mu.Unlock()
		return next.Handle(ctx, e)
	})
}

// Events returns a snapshot copy of all captured events.
func (c *CapturingMiddleware) Events() []*fswatch.EnrichedEvent {
	c.mu.Lock()
	defer c.mu.Unlock()
	out := make([]*fswatch.EnrichedEvent, len(c.events))
	copy(out, c.events)
	return out
}

// Len returns the current event count.
func (c *CapturingMiddleware) Len() int {
	c.mu.Lock()
	defer c.mu.Unlock()
	return len(c.events)
}

// WaitForN blocks until at least n events have been captured or ctx is cancelled.
// Returns true when n events are available.
func (c *CapturingMiddleware) WaitForN(ctx context.Context, n int) bool {
	ticker := time.NewTicker(5 * time.Millisecond)
	defer ticker.Stop()
	for {
		select {
		case <-ctx.Done():
			return false
		case <-ticker.C:
			if c.Len() >= n {
				return true
			}
		}
	}
}

// Reset discards all captured events.
func (c *CapturingMiddleware) Reset() {
	c.mu.Lock()
	c.events = c.events[:0]
	c.mu.Unlock()
}

// ─────────────────────────────────────────────────────────────────────────────
// Specialised handler doubles
// ─────────────────────────────────────────────────────────────────────────────

// ErroringHandler is a [fswatch.Handler] that always returns the given error.
type ErroringHandler struct {
	Err error
}

// Handle implements [fswatch.Handler].
func (e *ErroringHandler) Handle(_ context.Context, _ *fswatch.EnrichedEvent) error {
	return e.Err
}

// PanicHandler is a [fswatch.Handler] that always panics.
// Use to exercise [middleware.RecoveryMiddleware].
type PanicHandler struct {
	Value any
}

// Handle implements [fswatch.Handler].
func (p *PanicHandler) Handle(_ context.Context, _ *fswatch.EnrichedEvent) error {
	panic(p.Value)
}

// ─────────────────────────────────────────────────────────────────────────────
// Event batch helpers
// ─────────────────────────────────────────────────────────────────────────────

// MakeReadOnlyEvents produces n [fswatch.RawEvent] values cycling through
// ACCESS, OPEN, OPEN_EXEC, and CLOSE_NOWRITE masks. All paths are under mergedDir.
func MakeReadOnlyEvents(mergedDir string, n int) []*fswatch.RawEvent {
	ops := []fswatch.Op{
		fswatch.OpAccess,
		fswatch.OpOpen,
		fswatch.OpOpenExec,
		fswatch.OpCloseNoWrite,
	}
	events := make([]*fswatch.RawEvent, n)
	for i := range n {
		events[i] = NewRawEvent().
			WithOp(ops[i%len(ops)]).
			WithPath(fmt.Sprintf("%s/file%04d.dat", mergedDir, i)).
			WithPID(int32(1000 + i%10)).
			Build()
	}
	return events
}

// MakeMixedEvents produces n events interleaving read-only and modification ops.
// Useful for testing that [fswatch.ReadOnlyFilter] correctly drops writes.
func MakeMixedEvents(mergedDir string, n int) []*fswatch.RawEvent {
	ops := []fswatch.Op{
		fswatch.OpAccess,
		fswatch.OpModify,
		fswatch.OpOpen,
		fswatch.OpCreate,
		fswatch.OpOpenExec,
		fswatch.OpDelete,
		fswatch.OpCloseNoWrite,
		fswatch.OpCloseWrite,
	}
	events := make([]*fswatch.RawEvent, n)
	for i := range n {
		events[i] = NewRawEvent().
			WithOp(ops[i%len(ops)]).
			WithPath(fmt.Sprintf("%s/file%04d.dat", mergedDir, i)).
			WithPID(int32(2000 + i%5)).
			Build()
	}
	return events
}

// ProcessInfoFixture builds a [fswatch.ProcessInfo] without requiring /proc.
func ProcessInfoFixture(pid int32, comm, exe string) *fswatch.ProcessInfo {
	return &fswatch.ProcessInfo{
		PID:     pid,
		Comm:    comm,
		Exe:     exe,
		Cmdline: []string{exe},
	}
}

// UpperLayerFixture returns a [fswatch.SnapshotLayer] representing an upperdir.
func UpperLayerFixture(path string) *fswatch.SnapshotLayer {
	return &fswatch.SnapshotLayer{Index: 0, Path: path, IsUpper: true}
}

// LowerLayerFixture returns a [fswatch.SnapshotLayer] representing a lower layer.
func LowerLayerFixture(index int, path string) *fswatch.SnapshotLayer {
	return &fswatch.SnapshotLayer{Index: index, Path: path, IsUpper: false}
}
