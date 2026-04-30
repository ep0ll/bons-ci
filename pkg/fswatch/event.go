package fanwatch

import (
	"fmt"
	"io/fs"
	"time"
)

// ─────────────────────────────────────────────────────────────────────────────
// EventMask — bitmask of fanotify event types
// ─────────────────────────────────────────────────────────────────────────────

// EventMask is a bitmask of one or more filesystem [Op] values.
// It mirrors the fanotify mask field directly; values correspond to the
// FAN_* constants from <linux/fanotify.h>.
type EventMask uint64

// Op is a single fanotify operation bit.
type Op EventMask

const (
	// Read-only operations — these are the events fanwatch observes by default.

	// OpAccess fires when a file is read (pread64, read, sendfile, etc.).
	OpAccess Op = 0x00000001 // FAN_ACCESS

	// OpOpen fires when a file or directory is opened.
	OpOpen Op = 0x00000020 // FAN_OPEN

	// OpOpenExec fires when a file is opened with intent to execute.
	OpOpenExec Op = 0x00001000 // FAN_OPEN_EXEC

	// OpCloseNoWrite fires when a file opened without write intent is closed.
	OpCloseNoWrite Op = 0x00000010 // FAN_CLOSE_NOWRITE

	// Modification operations — filtered out by [MaskReadOnly].

	// OpModify fires when a file is modified.
	OpModify Op = 0x00000002 // FAN_MODIFY

	// OpCloseWrite fires when a file opened with write intent is closed.
	OpCloseWrite Op = 0x00000008 // FAN_CLOSE_WRITE

	// OpCreate fires when a file or directory is created.
	OpCreate Op = 0x00000100 // FAN_CREATE

	// OpDelete fires when a file or directory is deleted.
	OpDelete Op = 0x00000200 // FAN_DELETE

	// OpDeleteSelf fires when the watched object itself is deleted.
	OpDeleteSelf Op = 0x00000400 // FAN_DELETE_SELF

	// OpMovedFrom fires when a file is moved out of the watched directory.
	OpMovedFrom Op = 0x00000040 // FAN_MOVED_FROM

	// OpMovedTo fires when a file is moved into the watched directory.
	OpMovedTo Op = 0x00000080 // FAN_MOVED_TO

	// OpMoveSelf fires when the watched object is moved.
	OpMoveSelf Op = 0x00000800 // FAN_MOVE_SELF

	// OpAttrib fires when file metadata (permissions, timestamps) changes.
	OpAttrib Op = 0x00000004 // FAN_ATTRIB

	// OpOnDir modifies an event so it is emitted for directories too.
	OpOnDir Op = 0x40000000 // FAN_ONDIR

	// OpEventOnChild propagates events from a watched directory to its children.
	OpEventOnChild Op = 0x08000000 // FAN_EVENT_ON_CHILD

	// OpOverflow is a synthetic op indicating the event queue overflowed.
	OpOverflow Op = 0x00004000 // FAN_Q_OVERFLOW
)

// MaskReadOnly is the recommended event mask for a read-only observer.
// It captures file accesses, opens, exec-opens, and non-write closes —
// all operations that do not mutate filesystem state.
var MaskReadOnly = EventMask(OpAccess | OpOpen | OpOpenExec | OpCloseNoWrite)

// MaskAll captures every event type including modifications.
// Use MaskReadOnly unless you need to observe writes.
var MaskAll = EventMask(
	OpAccess | OpOpen | OpOpenExec | OpCloseNoWrite |
		OpModify | OpCloseWrite | OpCreate | OpDelete | OpDeleteSelf |
		OpMovedFrom | OpMovedTo | OpMoveSelf | OpAttrib,
)

// Has reports whether m includes op.
func (m EventMask) Has(op Op) bool { return m&EventMask(op) != 0 }

// IsReadOnly reports true when the mask contains only non-mutating ops.
func (m EventMask) IsReadOnly() bool { return m&EventMask(opModificationBits) == 0 }

// opModificationBits is the union of all modification-class op bits.
var opModificationBits = OpModify | OpCloseWrite | OpCreate | OpDelete |
	OpDeleteSelf | OpMovedFrom | OpMovedTo | OpMoveSelf | OpAttrib

// String returns a human-readable label for the mask.
func (m EventMask) String() string {
	names := opNames(m)
	if len(names) == 0 {
		return "none"
	}
	out := ""
	for i, n := range names {
		if i > 0 {
			out += "|"
		}
		out += n
	}
	return out
}

func opNames(m EventMask) []string {
	table := []struct {
		op   Op
		name string
	}{
		{OpAccess, "ACCESS"},
		{OpOpen, "OPEN"},
		{OpOpenExec, "OPEN_EXEC"},
		{OpCloseNoWrite, "CLOSE_NOWRITE"},
		{OpModify, "MODIFY"},
		{OpCloseWrite, "CLOSE_WRITE"},
		{OpCreate, "CREATE"},
		{OpDelete, "DELETE"},
		{OpDeleteSelf, "DELETE_SELF"},
		{OpMovedFrom, "MOVED_FROM"},
		{OpMovedTo, "MOVED_TO"},
		{OpMoveSelf, "MOVE_SELF"},
		{OpAttrib, "ATTRIB"},
		{OpOverflow, "OVERFLOW"},
	}
	var names []string
	for _, t := range table {
		if m.Has(t.op) {
			names = append(names, t.name)
		}
	}
	return names
}

// ─────────────────────────────────────────────────────────────────────────────
// RawEvent — minimal event as received from the kernel
// ─────────────────────────────────────────────────────────────────────────────

// RawEvent is the unprocessed event delivered by the kernel fanotify subsystem.
// Path is resolved from the per-event file descriptor immediately after reading;
// the fd itself is closed before RawEvent leaves the watcher goroutine.
type RawEvent struct {
	// Mask contains the event type(s) that fired.
	Mask EventMask

	// PID is the process ID of the process that triggered the event.
	PID int32

	// Path is the absolute path to the file or directory that triggered the event.
	// Resolved via /proc/self/fd/<fd> immediately after the kernel delivers the event.
	Path string

	// Timestamp is when the event was read from the fanotify file descriptor.
	Timestamp time.Time

	// WatcherID is set by the Watcher that produced this event.
	// Useful when multiple watchers feed a single pipeline.
	WatcherID string
}

// IsOverflow reports whether this event signals queue overflow.
// When overflow occurs some events were dropped; callers may rescan state.
func (r *RawEvent) IsOverflow() bool { return r.Mask.Has(OpOverflow) }

// String implements fmt.Stringer for logging.
func (r *RawEvent) String() string {
	return fmt.Sprintf("RawEvent{mask=%s pid=%d path=%q ts=%s}",
		r.Mask, r.PID, r.Path, r.Timestamp.Format(time.RFC3339Nano))
}

// ─────────────────────────────────────────────────────────────────────────────
// Event — parsed event with directory/filename split and file metadata
// ─────────────────────────────────────────────────────────────────────────────

// Event extends RawEvent with parsed path components and optional file metadata.
// It is produced by the watcher before entering the pipeline.
type Event struct {
	RawEvent

	// Dir is the directory portion of Path.
	Dir string

	// Name is the filename portion of Path.
	Name string

	// FileInfo is the Lstat result for Path at event time.
	// Nil when the file was already deleted before stat could complete.
	FileInfo fs.FileInfo

	// WatcherID identifies which Watcher produced this event.
	// Useful when multiple watchers feed a single pipeline.
	WatcherID string
}

// ─────────────────────────────────────────────────────────────────────────────
// EnrichedEvent — event plus all metadata added by transformers
// ─────────────────────────────────────────────────────────────────────────────

// EnrichedEvent is an [Event] enriched with overlay filesystem metadata and
// process information. Transformers progressively populate the fields; earlier
// transformers in the pipeline may leave fields nil that later ones populate.
type EnrichedEvent struct {
	Event

	// Overlay contains layer information for the overlay filesystem this event
	// occurred on. Populated by [transform.OverlayEnricher].
	Overlay *OverlayInfo

	// SourceLayer identifies which layer within the overlay stack the accessed
	// path originates from. Populated by [transform.OverlayEnricher].
	SourceLayer *SnapshotLayer

	// Process contains information about the process that triggered the event.
	// Populated by [transform.ProcessEnricher].
	Process *ProcessInfo

	// Attrs is a free-form extension map for custom transformer outputs.
	// Keys should use reverse-domain notation (e.g. "myco.myapp.custom").
	Attrs map[string]any
}

// Clone returns a deep copy of the event. Use before forking to multiple handlers.
func (e *EnrichedEvent) Clone() *EnrichedEvent {
	clone := *e
	if e.Attrs != nil {
		clone.Attrs = make(map[string]any, len(e.Attrs))
		for k, v := range e.Attrs {
			clone.Attrs[k] = v
		}
	}
	return &clone
}

// SetAttr stores a value in the extension map, initialising it on first use.
func (e *EnrichedEvent) SetAttr(key string, value any) {
	if e.Attrs == nil {
		e.Attrs = make(map[string]any)
	}
	e.Attrs[key] = value
}

// Attr retrieves a value from the extension map. Returns nil when absent.
func (e *EnrichedEvent) Attr(key string) any {
	return e.Attrs[key]
}

// IsReadOnly reports true when the event represents a non-mutating operation.
func (e *EnrichedEvent) IsReadOnly() bool { return e.Mask.IsReadOnly() }

// String implements fmt.Stringer.
func (e *EnrichedEvent) String() string {
	pid := int32(0)
	if e.Process != nil {
		pid = e.Process.PID
	}
	return fmt.Sprintf("EnrichedEvent{mask=%s pid=%d path=%q}",
		e.Mask, pid, e.Path)
}

// ─────────────────────────────────────────────────────────────────────────────
// ProcessInfo — caller process metadata
// ─────────────────────────────────────────────────────────────────────────────

// ProcessInfo holds metadata about the process that triggered a fanotify event.
// All fields are populated from /proc/{pid}/ on a best-effort basis;
// fields may be empty if the process exited before the read completed.
type ProcessInfo struct {
	// PID is the kernel process ID.
	PID int32

	// Comm is the short process name (from /proc/{pid}/comm, up to 15 chars).
	Comm string

	// Exe is the absolute path of the executable (from /proc/{pid}/exe).
	Exe string

	// Cmdline is the full command line (from /proc/{pid}/cmdline).
	Cmdline []string

	// ContainerID is the cgroup-derived container ID, if detectable.
	ContainerID string
}

// String implements fmt.Stringer.
func (p *ProcessInfo) String() string {
	return fmt.Sprintf("ProcessInfo{pid=%d comm=%q exe=%q}", p.PID, p.Comm, p.Exe)
}
