package diffview

// observer.go – Observer interface and built-in implementations.
//
// An Observer receives one Event per DiffEntry after Apply has decided what
// to do with it. This is the observability seam of diffview: swap in a
// different Observer to get logging, metrics, or audit trails without
// changing any other code.
//
// All implementations are goroutine-safe; events may arrive from multiple
// concurrent workers.

import (
	"fmt"
	"io"
	"sync"
	"sync/atomic"
)

// ─── Event ────────────────────────────────────────────────────────────────────

// Event is emitted to the Observer once per DiffEntry.
//
// It carries both the classification decision (Entry) and the outcome of any
// deletion attempt (SubmitErr). Because deletions go through a dirsync Batcher,
// SubmitErr reflects whether the op was successfully enqueued — not whether
// the underlying syscall (unlinkat, RemoveAll) succeeded. Batcher flush errors
// are reported in Result.Err after Apply returns.
type Event struct {
	// Entry is the classified DiffEntry.
	Entry DiffEntry

	// SubmitErr is non-nil when the deletion op could not be submitted to the
	// Batcher (e.g. batcher already closed). Entry.Action == ActionDelete when set.
	SubmitErr error
}

// WasDeleted reports whether the deletion was requested and the op was submitted successfully.
func (e Event) WasDeleted() bool {
	return e.Entry.Action == ActionDelete && e.SubmitErr == nil
}

// WasFailed reports whether a deletion submission attempt failed.
func (e Event) WasFailed() bool {
	return e.Entry.Action == ActionDelete && e.SubmitErr != nil
}

// WasRetained reports whether the path was classified as retain (no deletion).
func (e Event) WasRetained() bool {
	return e.Entry.Action == ActionRetain
}

// ─── Observer ─────────────────────────────────────────────────────────────────

// Observer receives a notification for every path processed by Apply.
//
// OnEvent is called exactly once per DiffEntry, after the deletion op has been
// submitted to the Batcher (or after the retain decision was made). It must not
// block; slow work should be deferred to a separate goroutine.
type Observer interface {
	OnEvent(event Event)
}

// ─── NoopObserver ─────────────────────────────────────────────────────────────

// NoopObserver silently discards all events. Default when none is configured.
type NoopObserver struct{}

func (NoopObserver) OnEvent(_ Event) {}

// ─── LogObserver ──────────────────────────────────────────────────────────────

// LogLevel controls the verbosity of LogObserver output.
type LogLevel int

const (
	// LogAll writes one line per DiffEntry: deletions, retentions, and errors.
	LogAll LogLevel = iota

	// LogChanges writes deletions and errors only; retained-diff paths are silent.
	LogChanges

	// LogErrors writes only failed submissions and hash errors.
	LogErrors
)

// LogObserver writes structured text lines to any io.Writer.
//
// Line format:
//
//	[DELETED_EXCL]   rel/path
//	[DELETED_EQUAL]  rel/path
//	[RETAINED_DIFF]  rel/path
//	[RETAINED_ERR]   rel/path  -- <error>
//	[SUBMIT_FAILED]  rel/path  -- <error>
//
// Error lines are always emitted regardless of LogLevel.
type LogObserver struct {
	w     io.Writer
	mu    sync.Mutex
	level LogLevel
}

// NewLogObserver creates a LogObserver writing to w at the given verbosity.
func NewLogObserver(w io.Writer, level LogLevel) *LogObserver {
	return &LogObserver{w: w, level: level}
}

// OnEvent implements Observer.
func (l *LogObserver) OnEvent(ev Event) {
	tag, extra := l.classify(ev)
	if tag == "" {
		return
	}
	line := fmt.Sprintf("%-16s %s%s\n", tag, ev.Entry.RelPath, extra)
	l.mu.Lock()
	_, _ = l.w.Write([]byte(line))
	l.mu.Unlock()
}

func (l *LogObserver) classify(ev Event) (tag, extra string) {
	switch {
	case ev.WasFailed():
		return "[SUBMIT_FAILED] ", "  -- " + ev.SubmitErr.Error()

	case ev.Entry.RetainReason == RetainReasonHashError:
		suffix := ""
		if ev.Entry.HashErr != nil {
			suffix = "  -- " + ev.Entry.HashErr.Error()
		}
		return "[RETAINED_ERR]  ", suffix

	case ev.WasDeleted() && ev.Entry.DeleteReason == DeleteReasonExclusiveLower:
		if l.level > LogChanges {
			return "", ""
		}
		return "[DELETED_EXCL]  ", ""

	case ev.WasDeleted() && ev.Entry.DeleteReason == DeleteReasonCommonEqual:
		if l.level > LogChanges {
			return "", ""
		}
		return "[DELETED_EQUAL] ", ""

	case ev.WasRetained() && ev.Entry.RetainReason == RetainReasonCommonDifferent:
		if l.level > LogAll {
			return "", ""
		}
		return "[RETAINED_DIFF] ", ""
	}
	return "", ""
}

// ─── CountingObserver ─────────────────────────────────────────────────────────

// CountingObserver atomically tallies every outcome category.
// All counters are safe to read after Apply returns.
type CountingObserver struct {
	DeletedExclusive atomic.Int64
	DeletedEqual     atomic.Int64
	RetainedDiff     atomic.Int64
	RetainedHashErr  atomic.Int64
	SubmitFailed     atomic.Int64
}

// OnEvent implements Observer.
func (c *CountingObserver) OnEvent(ev Event) {
	switch {
	case ev.WasFailed():
		c.SubmitFailed.Add(1)
	case ev.WasDeleted() && ev.Entry.DeleteReason == DeleteReasonExclusiveLower:
		c.DeletedExclusive.Add(1)
	case ev.WasDeleted() && ev.Entry.DeleteReason == DeleteReasonCommonEqual:
		c.DeletedEqual.Add(1)
	case ev.WasRetained() && ev.Entry.RetainReason == RetainReasonCommonDifferent:
		c.RetainedDiff.Add(1)
	case ev.WasRetained() && ev.Entry.RetainReason == RetainReasonHashError:
		c.RetainedHashErr.Add(1)
	}
}

// Total returns the sum of all counted events.
func (c *CountingObserver) Total() int64 {
	return c.DeletedExclusive.Load() +
		c.DeletedEqual.Load() +
		c.RetainedDiff.Load() +
		c.RetainedHashErr.Load() +
		c.SubmitFailed.Load()
}

// ─── MultiObserver ────────────────────────────────────────────────────────────

// MultiObserver fans out every event to all registered observers in order.
// Nil entries are skipped at construction time.
type MultiObserver struct {
	observers []Observer
}

// NewMultiObserver creates a MultiObserver dispatching to all non-nil observers.
func NewMultiObserver(observers ...Observer) *MultiObserver {
	filtered := make([]Observer, 0, len(observers))
	for _, o := range observers {
		if o != nil {
			filtered = append(filtered, o)
		}
	}
	return &MultiObserver{observers: filtered}
}

// OnEvent implements Observer.
func (m *MultiObserver) OnEvent(event Event) {
	for _, o := range m.observers {
		o.OnEvent(event)
	}
}
