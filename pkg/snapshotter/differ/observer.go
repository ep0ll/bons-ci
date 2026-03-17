package diffview

// observer.go – Observer interface and built-in implementations.
//
// An Observer receives one callback per DiffEntry after Apply has handled it
// (deletion attempted, retained, or error).  It is the observability seam:
// swap in a different Observer to get logging, metrics, or audit trails
// without changing any other code.
//
// All implementations are safe for concurrent use.

import (
	"fmt"
	"io"
	"sync"
	"sync/atomic"
)

// ─── Event ────────────────────────────────────────────────────────────────────

// Event is emitted to Observer once per DiffEntry after the Apply engine has
// made its final decision.
type Event struct {
	// Entry is the DiffEntry that was processed.
	Entry DiffEntry

	// DeleteErr is non-nil when a deletion was attempted but the Deleter
	// returned an error.  Entry.Action is ActionDelete in this case.
	DeleteErr error
}

// WasDeleted reports whether the deletion was both requested and successful.
func (e Event) WasDeleted() bool {
	return e.Entry.Action == ActionDelete && e.DeleteErr == nil
}

// WasFailed reports whether a deletion was attempted but failed.
func (e Event) WasFailed() bool {
	return e.Entry.Action == ActionDelete && e.DeleteErr != nil
}

// WasRetained reports whether the path was kept in merged.
func (e Event) WasRetained() bool {
	return e.Entry.Action == ActionRetain
}

// ─── Observer interface ───────────────────────────────────────────────────────

// Observer receives a callback for every path processed by Apply.
//
// OnEvent is called exactly once per DiffEntry, always after the deletion
// attempt (or retention decision) has completed.  Implementations must be
// goroutine-safe; events may arrive from multiple workers concurrently.
type Observer interface {
	OnEvent(event Event)
}

// ─── NoopObserver ─────────────────────────────────────────────────────────────

// NoopObserver silently discards all events.
// It is the default when no Observer is configured.
type NoopObserver struct{}

func (NoopObserver) OnEvent(_ Event) {}

// ─── LogObserver ──────────────────────────────────────────────────────────────

// LogLevel controls the verbosity of LogObserver output.
type LogLevel int

const (
	// LogAll writes one line for every DiffEntry (deletions, retentions, errors).
	LogAll LogLevel = iota
	// LogChanges writes only deletions and errors; retained paths are silent.
	LogChanges
	// LogErrors writes only failed deletions and hash errors.
	LogErrors
)

// LogObserver is a thread-safe Observer that writes structured lines to any
// io.Writer.
//
// Line format:
//
//	[DELETED_EXCL]   rel/path
//	[DELETED_EQUAL]  rel/path
//	[RETAINED_DIFF]  rel/path
//	[RETAINED_ERR]   rel/path  -- <error>
//	[DELETE_FAILED]  rel/path  -- <error>
type LogObserver struct {
	w     io.Writer
	mu    sync.Mutex
	level LogLevel
}

// NewLogObserver creates a LogObserver writing to w at the given verbosity.
func NewLogObserver(w io.Writer, level LogLevel) *LogObserver {
	return &LogObserver{w: w, level: level}
}

func (l *LogObserver) OnEvent(ev Event) {
	tag, extra := l.classify(ev)
	if tag == "" {
		return // below verbosity threshold
	}
	line := fmt.Sprintf("%-16s %s%s\n", tag, ev.Entry.RelPath, extra)
	l.mu.Lock()
	_, _ = l.w.Write([]byte(line))
	l.mu.Unlock()
}

func (l *LogObserver) classify(ev Event) (tag, extra string) {
	switch {
	case ev.WasFailed():
		// Always logged regardless of level.
		return "[DELETE_FAILED]", "  -- " + ev.DeleteErr.Error()

	case ev.Entry.Action == ActionRetain && ev.Entry.RetainReason == RetainReasonHashError:
		// Always logged (error path).
		errStr := ""
		if ev.Entry.Err != nil {
			errStr = "  -- " + ev.Entry.Err.Error()
		}
		return "[RETAINED_ERR] ", errStr

	case ev.WasDeleted() && ev.Entry.DeleteReason == DeleteReasonExclusiveLower:
		if l.level > LogChanges {
			return "", ""
		}
		return "[DELETED_EXCL] ", ""

	case ev.WasDeleted() && ev.Entry.DeleteReason == DeleteReasonCommonEqual:
		if l.level > LogChanges {
			return "", ""
		}
		return "[DELETED_EQUAL]", ""

	case ev.WasRetained() && ev.Entry.RetainReason == RetainReasonCommonDifferent:
		if l.level > LogAll {
			return "", ""
		}
		return "[RETAINED_DIFF]", ""
	}
	return "", ""
}

// ─── CountingObserver ─────────────────────────────────────────────────────────

// CountingObserver atomically tallies every event.
// All counters are safe to read after Apply returns.
type CountingObserver struct {
	DeletedExclusive atomic.Int64
	DeletedEqual     atomic.Int64
	RetainedDiff     atomic.Int64
	RetainedHashErr  atomic.Int64
	DeleteFailed     atomic.Int64
}

func (c *CountingObserver) OnEvent(ev Event) {
	switch {
	case ev.WasFailed():
		c.DeleteFailed.Add(1)
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

// ─── MultiObserver ────────────────────────────────────────────────────────────

// MultiObserver fans out every event to a list of underlying Observers.
// Useful when you want both logging and metrics simultaneously.
// Nil entries are silently skipped at construction time.
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

func (m *MultiObserver) OnEvent(event Event) {
	for _, o := range m.observers {
		o.OnEvent(event)
	}
}
