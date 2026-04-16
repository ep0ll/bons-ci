// Package progress provides ProgressReporter implementations.
// Inject progress.Noop() in tests and progress.NewConsole() in production.
package progress

import (
	"context"
	"fmt"
	"io"
	"os"
	"sync"
	"time"

	"github.com/bons/bons-ci/plugins/exporter/core"
)

// ─── Noop ──────────────────────────────────────────────────────────────────

type noopReporter struct{}

// Noop returns a ProgressReporter that silently discards all events.
// Use in unit tests where progress output is irrelevant.
func Noop() core.ProgressReporter { return &noopReporter{} }

func (n *noopReporter) Start(_ context.Context, _, _ string)          {}
func (n *noopReporter) Update(_ context.Context, _ string, _ int)     {}
func (n *noopReporter) Complete(_ context.Context, _ string, _ error) {}
func (n *noopReporter) Close() error                                  { return nil }

// ─── Console ───────────────────────────────────────────────────────────────

// consoleOp tracks the lifecycle of a single named operation.
type consoleOp struct {
	label   string
	started time.Time
}

// consoleReporter writes human-readable progress lines to an io.Writer.
// It is concurrency-safe.
type consoleReporter struct {
	mu  sync.Mutex
	w   io.Writer
	ops map[string]*consoleOp
}

// ConsoleOption is a functional option for NewConsole.
type ConsoleOption func(*consoleReporter)

// WithWriter sets the output writer (default: os.Stderr).
func WithWriter(w io.Writer) ConsoleOption {
	return func(r *consoleReporter) { r.w = w }
}

// NewConsole returns a ProgressReporter that writes human-readable lines.
func NewConsole(options ...ConsoleOption) core.ProgressReporter {
	r := &consoleReporter{
		w:   os.Stderr,
		ops: make(map[string]*consoleOp),
	}
	for _, o := range options {
		o(r)
	}
	return r
}

func (r *consoleReporter) Start(_ context.Context, id, label string) {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.ops[id] = &consoleOp{label: label, started: time.Now()}
	fmt.Fprintf(r.w, "  ▶ %s\n", label)
}

func (r *consoleReporter) Update(_ context.Context, id string, pct int) {
	r.mu.Lock()
	defer r.mu.Unlock()
	op, ok := r.ops[id]
	if !ok {
		return
	}
	fmt.Fprintf(r.w, "    %s … %d%%\n", op.label, pct)
}

func (r *consoleReporter) Complete(_ context.Context, id string, err error) {
	r.mu.Lock()
	defer r.mu.Unlock()
	op, ok := r.ops[id]
	if !ok {
		return
	}
	elapsed := time.Since(op.started).Truncate(time.Millisecond)
	if err != nil {
		fmt.Fprintf(r.w, "  ✗ %s FAILED (%s): %v\n", op.label, elapsed, err)
	} else {
		fmt.Fprintf(r.w, "  ✓ %s done (%s)\n", op.label, elapsed)
	}
	delete(r.ops, id)
}

func (r *consoleReporter) Close() error { return nil }

// ─── Capturing (for tests) ─────────────────────────────────────────────────

// Event records a single progress event for test assertions.
type Event struct {
	Kind  string // "start" | "update" | "complete"
	ID    string
	Label string // for start events
	Pct   int    // for update events
	Err   error  // for complete events
}

// CapturingReporter records all events; suitable for assertion-heavy tests.
type CapturingReporter struct {
	mu     sync.Mutex
	Events []Event
}

// NewCapturing creates a CapturingReporter.
func NewCapturing() *CapturingReporter { return &CapturingReporter{} }

func (c *CapturingReporter) Start(_ context.Context, id, label string) {
	c.mu.Lock()
	defer c.mu.Unlock()
	c.Events = append(c.Events, Event{Kind: "start", ID: id, Label: label})
}

func (c *CapturingReporter) Update(_ context.Context, id string, pct int) {
	c.mu.Lock()
	defer c.mu.Unlock()
	c.Events = append(c.Events, Event{Kind: "update", ID: id, Pct: pct})
}

func (c *CapturingReporter) Complete(_ context.Context, id string, err error) {
	c.mu.Lock()
	defer c.mu.Unlock()
	c.Events = append(c.Events, Event{Kind: "complete", ID: id, Err: err})
}

func (c *CapturingReporter) Close() error { return nil }

// StartedIDs returns the IDs of all started operations (for assertions).
func (c *CapturingReporter) StartedIDs() []string {
	c.mu.Lock()
	defer c.mu.Unlock()
	var ids []string
	for _, e := range c.Events {
		if e.Kind == "start" {
			ids = append(ids, e.ID)
		}
	}
	return ids
}

// CompletedWithError returns true if any Complete event carried a non-nil error.
func (c *CapturingReporter) CompletedWithError() bool {
	c.mu.Lock()
	defer c.mu.Unlock()
	for _, e := range c.Events {
		if e.Kind == "complete" && e.Err != nil {
			return true
		}
	}
	return false
}
