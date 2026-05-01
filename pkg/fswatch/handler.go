package fanwatch

import (
	"context"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"os"
	"sync"
	"sync/atomic"
)

// ─────────────────────────────────────────────────────────────────────────────
// Handler interface
// ─────────────────────────────────────────────────────────────────────────────

// Handler processes an [EnrichedEvent] that has passed all filters and
// transformers. Handlers are the terminal stage of the pipeline; they perform
// side effects (logging, alerting, metrics, storage) without returning modified
// events.
//
// All implementations must be safe for concurrent use from multiple goroutines.
type Handler interface {
	// Handle processes a single event. A non-nil error is forwarded to the
	// pipeline's error channel but does not affect other in-flight events.
	Handle(ctx context.Context, e *EnrichedEvent) error
}

// HandlerFunc is a function that implements [Handler].
type HandlerFunc func(ctx context.Context, e *EnrichedEvent) error

// Handle implements [Handler].
func (f HandlerFunc) Handle(ctx context.Context, e *EnrichedEvent) error { return f(ctx, e) }

// ─────────────────────────────────────────────────────────────────────────────
// Composite handlers — chain, multi, predicate-gated
// ─────────────────────────────────────────────────────────────────────────────

// ChainHandler invokes handlers in sequence, stopping at the first error.
// Use [MultiHandler] when all handlers must run regardless of errors.
type ChainHandler []Handler

// Handle implements [Handler].
func (c ChainHandler) Handle(ctx context.Context, e *EnrichedEvent) error {
	for _, h := range c {
		if err := h.Handle(ctx, e); err != nil {
			return err
		}
	}
	return nil
}

// MultiHandler fans each event out to all handlers, collecting all errors.
// Unlike [ChainHandler] every handler always runs even when earlier ones fail.
//
// FIX Bug 9: previously used fmt.Errorf which lost the error chain for
// errors.Is/As. Now uses errors.Join so callers can unwrap individual errors.
type MultiHandler []Handler

// Handle implements [Handler].
func (m MultiHandler) Handle(ctx context.Context, e *EnrichedEvent) error {
	var errs []error
	for _, h := range m {
		if err := h.Handle(ctx, e); err != nil {
			errs = append(errs, err)
		}
	}
	return errors.Join(errs...)
}

// PredicateHandler wraps a [Handler] behind a predicate. The handler runs only
// when Predicate returns true. Predicate must not mutate the event.
type PredicateHandler struct {
	Predicate func(*EnrichedEvent) bool
	Handler   Handler
}

// Handle implements [Handler].
func (p PredicateHandler) Handle(ctx context.Context, e *EnrichedEvent) error {
	if p.Predicate(e) {
		return p.Handler.Handle(ctx, e)
	}
	return nil
}

// ─────────────────────────────────────────────────────────────────────────────
// NoopHandler — silent discard
// ─────────────────────────────────────────────────────────────────────────────

// NoopHandler silently discards every event without error.
// Useful as a placeholder in pipelines where side effects live in filters or
// transformers, or in benchmarks that measure pipeline throughput in isolation.
type NoopHandler struct{}

// Handle implements [Handler].
func (NoopHandler) Handle(_ context.Context, _ *EnrichedEvent) error { return nil }

// ─────────────────────────────────────────────────────────────────────────────
// LogHandler — structured logging
// ─────────────────────────────────────────────────────────────────────────────

// LogHandler writes each event to a [slog.Logger] at the given level.
func LogHandler(logger *slog.Logger, level slog.Level) Handler {
	return HandlerFunc(func(ctx context.Context, e *EnrichedEvent) error {
		args := []any{
			slog.String("path", e.Path),
			slog.String("mask", e.Mask.String()),
			slog.Int("pid", int(e.PID)),
			slog.Time("ts", e.Timestamp),
		}
		if e.Process != nil {
			args = append(args,
				slog.String("comm", e.Process.Comm),
				slog.String("exe", e.Process.Exe),
			)
		}
		if e.SourceLayer != nil {
			args = append(args,
				slog.Int("layer_index", e.SourceLayer.Index),
				slog.Bool("layer_is_upper", e.SourceLayer.IsUpper),
			)
		}
		logger.Log(ctx, level, "fanwatch event", args...)
		return nil
	})
}

// ─────────────────────────────────────────────────────────────────────────────
// CountingHandler — atomic event counters
// ─────────────────────────────────────────────────────────────────────────────

// EventCounters is an immutable snapshot of [CountingHandler] state.
type EventCounters struct {
	Total        int64
	AccessEvents int64
	OpenEvents   int64
	ExecEvents   int64
	OtherEvents  int64
}

// CountingHandler tallies events by operation type using atomic counters.
// Safe for concurrent use; call Snapshot() for a consistent point-in-time view.
type CountingHandler struct {
	total  atomic.Int64
	access atomic.Int64
	open   atomic.Int64
	exec   atomic.Int64
	other  atomic.Int64
}

// Handle implements [Handler].
func (c *CountingHandler) Handle(_ context.Context, e *EnrichedEvent) error {
	c.total.Add(1)
	switch {
	case e.Mask.Has(OpAccess):
		c.access.Add(1)
	case e.Mask.Has(OpOpen):
		c.open.Add(1)
	case e.Mask.Has(OpOpenExec):
		c.exec.Add(1)
	default:
		c.other.Add(1)
	}
	return nil
}

// Snapshot returns a point-in-time copy of all counters.
func (c *CountingHandler) Snapshot() EventCounters {
	return EventCounters{
		Total:        c.total.Load(),
		AccessEvents: c.access.Load(),
		OpenEvents:   c.open.Load(),
		ExecEvents:   c.exec.Load(),
		OtherEvents:  c.other.Load(),
	}
}

// Reset zeroes all counters atomically.
func (c *CountingHandler) Reset() {
	c.total.Store(0)
	c.access.Store(0)
	c.open.Store(0)
	c.exec.Store(0)
	c.other.Store(0)
}

// ─────────────────────────────────────────────────────────────────────────────
// CollectingHandler — in-memory accumulation
// ─────────────────────────────────────────────────────────────────────────────

// CollectingHandler accumulates all [EnrichedEvent] values in memory.
// Primarily a test double; also useful for building audit-log snapshots.
type CollectingHandler struct {
	mu     sync.Mutex
	events []*EnrichedEvent
}

// Handle implements [Handler].
func (c *CollectingHandler) Handle(_ context.Context, e *EnrichedEvent) error {
	clone := e.Clone()
	c.mu.Lock()
	defer c.mu.Unlock()
	c.events = append(c.events, clone)
	return nil
}

// Events returns a snapshot of all collected events. Each element is a clone
// so mutations by the caller do not affect the collector's internal state.
func (c *CollectingHandler) Events() []*EnrichedEvent {
	c.mu.Lock()
	defer c.mu.Unlock()
	out := make([]*EnrichedEvent, len(c.events))
	for i, e := range c.events {
		out[i] = e.Clone()
	}
	return out
}

// Len returns the number of events collected so far.
func (c *CollectingHandler) Len() int {
	c.mu.Lock()
	defer c.mu.Unlock()
	return len(c.events)
}

// Reset discards all accumulated events.
func (c *CollectingHandler) Reset() {
	c.mu.Lock()
	c.events = c.events[:0]
	c.mu.Unlock()
}

// ─────────────────────────────────────────────────────────────────────────────
// WriterHandler — plain-text output to any io.Writer
// ─────────────────────────────────────────────────────────────────────────────

// WriterHandler formats each event as a single line and writes it to w.
// Output format: "<time> <mask> pid=<pid> <path>\n"
// Defaults to os.Stdout when w is nil.
func WriterHandler(w io.Writer) Handler {
	if w == nil {
		w = os.Stdout
	}
	return HandlerFunc(func(_ context.Context, e *EnrichedEvent) error {
		_, err := fmt.Fprintf(w, "%s %s pid=%d %s\n",
			e.Timestamp.Format("15:04:05.000"),
			e.Mask,
			e.PID,
			e.Path,
		)
		return err
	})
}

// ─────────────────────────────────────────────────────────────────────────────
// ChannelHandler — forward events to an output channel
// ─────────────────────────────────────────────────────────────────────────────

// ChannelHandler forwards cloned events to a buffered channel.
// When the channel is full the event is dropped without blocking — size the
// channel buffer for your expected event rate.
type ChannelHandler struct {
	out chan<- *EnrichedEvent
}

// NewChannelHandler returns a ChannelHandler and the read-only consumer channel.
func NewChannelHandler(bufSize int) (*ChannelHandler, <-chan *EnrichedEvent) {
	ch := make(chan *EnrichedEvent, bufSize)
	return &ChannelHandler{out: ch}, ch
}

// Handle implements [Handler].
func (c *ChannelHandler) Handle(ctx context.Context, e *EnrichedEvent) error {
	clone := e.Clone()
	select {
	case c.out <- clone:
	case <-ctx.Done():
	default:
		// Channel full — drop event to avoid blocking the pipeline.
	}
	return nil
}
