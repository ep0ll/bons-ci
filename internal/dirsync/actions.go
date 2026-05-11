package dirsync

import (
	"context"
	"fmt"
	"io"
	"log/slog"
	"os"
	"sync"
	"sync/atomic"
)

// ─────────────────────────────────────────────────────────────────────────────
// Logging handlers — structured output via slog
// ─────────────────────────────────────────────────────────────────────────────

// LogExclusiveHandler logs each exclusive path as a structured [slog] record.
// It is a pure observer — it performs no mutations.
type LogExclusiveHandler struct {
	// Logger is the slog instance to write to. Required; will panic if nil.
	Logger *slog.Logger
	// Level controls which log level is used. Defaults to slog.LevelInfo.
	Level slog.Level
}

// HandleExclusive implements [ExclusiveHandler].
func (h *LogExclusiveHandler) HandleExclusive(ctx context.Context, ep ExclusivePath) error {
	// Using the provided ctx preserves trace IDs, cancellation, and deadline
	// information that callers attach — never use context.Background() here.
	h.Logger.Log(ctx, h.Level, "exclusive path",
		slog.String("path", ep.Path),
		slog.String("kind", ep.Kind.String()),
		slog.Bool("collapsed", ep.Collapsed),
	)
	return nil
}

// LogCommonHandler logs each common path including the hash equality result.
type LogCommonHandler struct {
	Logger *slog.Logger
	Level  slog.Level
}

// HandleCommon implements [CommonHandler].
func (h *LogCommonHandler) HandleCommon(ctx context.Context, cp CommonPath) error {
	eq, checked := cp.IsContentEqual()
	h.Logger.Log(ctx, h.Level, "common path",
		slog.String("path", cp.Path),
		slog.String("kind", cp.Kind.String()),
		slog.Bool("hash_checked", checked),
		slog.Bool("hash_equal", eq),
		slog.Bool("type_mismatch", cp.TypeMismatch()),
	)
	return nil
}

// ─────────────────────────────────────────────────────────────────────────────
// Counting handlers — atomic, concurrency-safe tallies
// ─────────────────────────────────────────────────────────────────────────────

// CountingExclusiveHandler tallies exclusive paths by kind using atomic
// counters. Safe for concurrent use from multiple handler goroutines.
type CountingExclusiveHandler struct {
	files     atomic.Int64
	dirs      atomic.Int64
	symlinks  atomic.Int64
	other     atomic.Int64
	collapsed atomic.Int64
}

// HandleExclusive implements [ExclusiveHandler].
func (c *CountingExclusiveHandler) HandleExclusive(_ context.Context, ep ExclusivePath) error {
	switch ep.Kind {
	case PathKindFile:
		c.files.Add(1)
	case PathKindDir:
		c.dirs.Add(1)
	case PathKindSymlink:
		c.symlinks.Add(1)
	default:
		c.other.Add(1)
	}
	if ep.Collapsed {
		c.collapsed.Add(1)
	}
	return nil
}

// Snapshot returns a point-in-time copy of all counters.
// The snapshot is self-consistent within the bounds of one atomic read per field.
func (c *CountingExclusiveHandler) Snapshot() ExclusiveCounts {
	return ExclusiveCounts{
		Files:     c.files.Load(),
		Dirs:      c.dirs.Load(),
		Symlinks:  c.symlinks.Load(),
		Other:     c.other.Load(),
		Collapsed: c.collapsed.Load(),
	}
}

// ExclusiveCounts is an immutable snapshot of [CountingExclusiveHandler] counters.
type ExclusiveCounts struct {
	Files, Dirs, Symlinks, Other int64
	// Collapsed is the subset of Dirs entries that were collapsed.
	Collapsed int64
}

// Total returns the sum of all classified entries (files + dirs + symlinks + other).
func (e ExclusiveCounts) Total() int64 { return e.Files + e.Dirs + e.Symlinks + e.Other }

// CountingCommonHandler tallies common paths by their comparison outcome.
type CountingCommonHandler struct {
	total     atomic.Int64
	equal     atomic.Int64
	changed   atomic.Int64
	unchecked atomic.Int64 // directories, special files; comparison not performed
	mismatch  atomic.Int64 // type-mismatch entries
}

// HandleCommon implements [CommonHandler].
func (c *CountingCommonHandler) HandleCommon(_ context.Context, cp CommonPath) error {
	c.total.Add(1)
	if cp.TypeMismatch() {
		c.mismatch.Add(1)
		return nil
	}
	eq, checked := cp.IsContentEqual()
	switch {
	case !checked:
		c.unchecked.Add(1)
	case eq:
		c.equal.Add(1)
	default:
		c.changed.Add(1)
	}
	return nil
}

// Snapshot returns a point-in-time copy of all counters.
func (c *CountingCommonHandler) Snapshot() CommonCounts {
	return CommonCounts{
		Total:     c.total.Load(),
		Equal:     c.equal.Load(),
		Changed:   c.changed.Load(),
		Unchecked: c.unchecked.Load(),
		Mismatch:  c.mismatch.Load(),
	}
}

// CommonCounts is an immutable snapshot of [CountingCommonHandler] counters.
type CommonCounts struct {
	Total, Equal, Changed, Unchecked, Mismatch int64
}

// ─────────────────────────────────────────────────────────────────────────────
// Collecting handlers — in-memory accumulation, for tests and audit
// ─────────────────────────────────────────────────────────────────────────────

// CollectingExclusiveHandler accumulates all [ExclusivePath] values in memory.
// Intended for unit tests and audit use cases where in-memory inspection is
// more convenient than channel draining.
//
// Thread-safe: multiple goroutines may call HandleExclusive concurrently.
type CollectingExclusiveHandler struct {
	mu    sync.Mutex
	paths []ExclusivePath
}

// HandleExclusive implements [ExclusiveHandler].
func (c *CollectingExclusiveHandler) HandleExclusive(_ context.Context, ep ExclusivePath) error {
	c.mu.Lock()
	defer c.mu.Unlock()
	c.paths = append(c.paths, ep)
	return nil
}

// Paths returns a point-in-time snapshot of all accumulated paths.
// The returned slice is a copy; modifications do not affect the handler.
func (c *CollectingExclusiveHandler) Paths() []ExclusivePath {
	c.mu.Lock()
	defer c.mu.Unlock()
	out := make([]ExclusivePath, len(c.paths))
	copy(out, c.paths)
	return out
}

// Reset clears all accumulated paths.
func (c *CollectingExclusiveHandler) Reset() {
	c.mu.Lock()
	c.paths = c.paths[:0]
	c.mu.Unlock()
}

// CollectingCommonHandler accumulates all [CommonPath] values in memory.
type CollectingCommonHandler struct {
	mu    sync.Mutex
	paths []CommonPath
}

// HandleCommon implements [CommonHandler].
func (c *CollectingCommonHandler) HandleCommon(_ context.Context, cp CommonPath) error {
	c.mu.Lock()
	defer c.mu.Unlock()
	c.paths = append(c.paths, cp)
	return nil
}

// Paths returns a point-in-time snapshot of all accumulated paths.
func (c *CollectingCommonHandler) Paths() []CommonPath {
	c.mu.Lock()
	defer c.mu.Unlock()
	out := make([]CommonPath, len(c.paths))
	copy(out, c.paths)
	return out
}

// Reset clears all accumulated paths.
func (c *CollectingCommonHandler) Reset() {
	c.mu.Lock()
	c.paths = c.paths[:0]
	c.mu.Unlock()
}

// ─────────────────────────────────────────────────────────────────────────────
// AccumulatingExclusiveHandler — feeds a PruningSet for two-phase workflows
// ─────────────────────────────────────────────────────────────────────────────

// AccumulatingExclusiveHandler appends each exclusive path into a [PruningSet].
//
// Designed for two-phase workflows where all paths must be collected first
// and then batch-processed:
//
//  1. Collect phase: run the pipeline with this handler; PruningSet accumulates.
//  2. Process phase: call PruningSet.Drain to execute the batched operations.
//
// This avoids the overhead of individual Remove calls during classification,
// which is beneficial when removal requires exclusive access or cross-system
// coordination.
type AccumulatingExclusiveHandler struct {
	// Set is the target pruning set. Must be non-nil.
	Set *PruningSet
}

// HandleExclusive implements [ExclusiveHandler].
func (a *AccumulatingExclusiveHandler) HandleExclusive(_ context.Context, ep ExclusivePath) error {
	a.Set.Add(ep)
	return nil
}

// ─────────────────────────────────────────────────────────────────────────────
// DryRunExclusiveHandler — preview without mutation
// ─────────────────────────────────────────────────────────────────────────────

// DryRunExclusiveHandler records every exclusive path that would have been
// deleted, printing a human-readable description to Writer without performing
// any filesystem mutations. Use it to preview operations before committing.
type DryRunExclusiveHandler struct {
	// Writer receives the dry-run output. Defaults to os.Stdout when nil.
	Writer io.Writer
}

// HandleExclusive implements [ExclusiveHandler].
func (h *DryRunExclusiveHandler) HandleExclusive(_ context.Context, ep ExclusivePath) error {
	w := h.Writer
	if w == nil {
		w = os.Stdout
	}

	// Collapsed directories would use RemoveAll (recursive); individual files
	// and non-collapsed entries use the lighter Remove.
	verb := "remove"
	if ep.Collapsed {
		verb = "removeAll"
	}
	_, err := fmt.Fprintf(w, "dry-run: %s %q (%s)\n", verb, ep.Path, ep.Kind)
	return err
}
