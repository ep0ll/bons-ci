package dirprune

// observer.go – Observer interface and built-in implementations.
//
// An Observer receives one event per path visited by the walker, after the
// Pruner has decided what to do with it. This is the observability seam:
// swap in a different Observer for logging, metrics, or audit trails.
//
// All implementations are goroutine-safe.

import (
	"fmt"
	"io"
	"sync"
	"sync/atomic"
)

// ─────────────────────────────────────────────────────────────────────────────
// Disposition — what happened to a path
// ─────────────────────────────────────────────────────────────────────────────

// Disposition describes what the Pruner decided for a single path.
type Disposition uint8

const (
	// DispositionKept: the path matched the filter and was left untouched.
	DispositionKept Disposition = iota

	// DispositionDeleted: the path did not match the filter and was submitted
	// to the Batcher for deletion (OpRemove or OpRemoveAll).
	DispositionDeleted

	// DispositionCollapsed: an entire directory subtree did not match the
	// filter and was submitted as a single OpRemoveAll. Children are not
	// visited individually; this entry represents the whole subtree.
	DispositionCollapsed
)

func (d Disposition) String() string {
	switch d {
	case DispositionKept:
		return "kept"
	case DispositionDeleted:
		return "deleted"
	case DispositionCollapsed:
		return "collapsed"
	default:
		return fmt.Sprintf("Disposition(%d)", int(d))
	}
}

// ─────────────────────────────────────────────────────────────────────────────
// Event
// ─────────────────────────────────────────────────────────────────────────────

// Event is emitted to the Observer once per path decision.
type Event struct {
	// RelPath is the forward-slash path relative to the target directory root.
	RelPath string

	// IsDir is true when the entry is a directory.
	IsDir bool

	// Disposition is what the Pruner decided for this path.
	Disposition Disposition

	// SubmitErr is non-nil when the Batcher.Submit call for a deletion failed.
	// When non-nil, Disposition is DispositionDeleted or DispositionCollapsed
	// but the deletion may not have been enqueued.
	SubmitErr error
}

// ─────────────────────────────────────────────────────────────────────────────
// Observer interface
// ─────────────────────────────────────────────────────────────────────────────

// Observer receives one Event per path visited during a Prune call.
// OnEvent must not block; expensive work should be deferred to a channel or
// goroutine. Implementations must be safe for concurrent use.
type Observer interface {
	OnEvent(Event)
}

// ─────────────────────────────────────────────────────────────────────────────
// NoopObserver
// ─────────────────────────────────────────────────────────────────────────────

// NoopObserver silently discards all events. Default when none is configured.
type NoopObserver struct{}

func (NoopObserver) OnEvent(_ Event) {}

// ─────────────────────────────────────────────────────────────────────────────
// LogObserver
// ─────────────────────────────────────────────────────────────────────────────

// LogVerbosity controls the verbosity of [LogObserver].
type LogVerbosity int

const (
	// LogAll writes one line per path: deletions, collapses, and kept paths.
	LogAll LogVerbosity = iota
	// LogDeletions writes only deleted and collapsed paths (silent for kept).
	LogDeletions
	// LogErrors writes only paths whose Batcher.Submit failed.
	LogErrors
)

// LogObserver writes one structured text line per event to any [io.Writer].
//
// Line format:
//
//	[KEPT]       rel/path
//	[DELETED]    rel/path
//	[COLLAPSED]  rel/path/   (trailing slash for dirs)
//	[ERROR]      rel/path  -- <submit error>
type LogObserver struct {
	w     io.Writer
	mu    sync.Mutex
	level LogVerbosity
}

// NewLogObserver creates a LogObserver that writes to w at the given verbosity.
func NewLogObserver(w io.Writer, level LogVerbosity) *LogObserver {
	return &LogObserver{w: w, level: level}
}

// OnEvent implements [Observer].
func (l *LogObserver) OnEvent(ev Event) {
	tag, suffix := l.classify(ev)
	if tag == "" {
		return
	}
	name := ev.RelPath
	if ev.IsDir && ev.Disposition != DispositionKept {
		name += "/"
	}
	line := fmt.Sprintf("%-12s %s%s\n", tag, name, suffix)
	l.mu.Lock()
	_, _ = l.w.Write([]byte(line))
	l.mu.Unlock()
}

func (l *LogObserver) classify(ev Event) (tag, suffix string) {
	if ev.SubmitErr != nil {
		return "[ERROR]     ", "  -- " + ev.SubmitErr.Error()
	}
	switch ev.Disposition {
	case DispositionCollapsed:
		if l.level > LogDeletions {
			return "", ""
		}
		return "[COLLAPSED] ", ""
	case DispositionDeleted:
		if l.level > LogDeletions {
			return "", ""
		}
		return "[DELETED]   ", ""
	case DispositionKept:
		if l.level > LogAll {
			return "", ""
		}
		return "[KEPT]      ", ""
	}
	return "", ""
}

// ─────────────────────────────────────────────────────────────────────────────
// CountingObserver
// ─────────────────────────────────────────────────────────────────────────────

// CountingObserver tallies events by disposition using atomic counters.
// All fields are safe to read after [Pruner.Prune] returns.
type CountingObserver struct {
	Kept      atomic.Int64
	Deleted   atomic.Int64
	Collapsed atomic.Int64
	Errors    atomic.Int64
}

// OnEvent implements [Observer].
func (c *CountingObserver) OnEvent(ev Event) {
	if ev.SubmitErr != nil {
		c.Errors.Add(1)
		return
	}
	switch ev.Disposition {
	case DispositionKept:
		c.Kept.Add(1)
	case DispositionDeleted:
		c.Deleted.Add(1)
	case DispositionCollapsed:
		c.Collapsed.Add(1)
	}
}

// Total returns the sum of all counted events.
func (c *CountingObserver) Total() int64 {
	return c.Kept.Load() + c.Deleted.Load() + c.Collapsed.Load() + c.Errors.Load()
}

// ─────────────────────────────────────────────────────────────────────────────
// MultiObserver
// ─────────────────────────────────────────────────────────────────────────────

// MultiObserver fans out every event to all registered observers in order.
// Nil entries are silently skipped at construction time.
type MultiObserver struct{ observers []Observer }

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

// OnEvent implements [Observer].
func (m *MultiObserver) OnEvent(ev Event) {
	for _, o := range m.observers {
		o.OnEvent(ev)
	}
}
