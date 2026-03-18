package differ

import (
	"context"
	"fmt"
	"io"
	"log/slog"
	"os"
	"sync/atomic"
)

// ─────────────────────────────────────────────────────────────────────────────
// Observers (read-only, no mutations)
// ─────────────────────────────────────────────────────────────────────────────

// LogExclusiveHandler logs each exclusive path using structured [slog].
type LogExclusiveHandler struct {
	Logger *slog.Logger
	Level  slog.Level
}

func (l *LogExclusiveHandler) HandleExclusive(_ context.Context, ep ExclusivePath) error {
	l.Logger.Log(context.Background(), l.Level, "exclusive path",
		slog.String("path", ep.Path),
		slog.String("kind", ep.Kind.String()),
		slog.Bool("collapsed", ep.Collapsed))
	return nil
}

// LogCommonHandler logs each common path including the hash equality result.
type LogCommonHandler struct {
	Logger *slog.Logger
	Level  slog.Level
}

func (l *LogCommonHandler) HandleCommon(_ context.Context, cp CommonPath) error {
	eq, checked := cp.IsContentEqual()
	l.Logger.Log(context.Background(), l.Level, "common path",
		slog.String("path", cp.Path),
		slog.String("kind", cp.Kind.String()),
		slog.Bool("hash_checked", checked),
		slog.Bool("hash_equal", eq),
		slog.Bool("type_mismatch", cp.TypeMismatch()))
	return nil
}

// ─────────────────────────────────────────────────────────────────────────────
// Counters (atomic, concurrency-safe)
// ─────────────────────────────────────────────────────────────────────────────

// CountingExclusiveHandler tallies exclusive paths by kind using atomic counters.
type CountingExclusiveHandler struct {
	files     atomic.Int64
	dirs      atomic.Int64
	symlinks  atomic.Int64
	other     atomic.Int64
	collapsed atomic.Int64
}

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
func (c *CountingExclusiveHandler) Snapshot() ExclusiveCounters {
	return ExclusiveCounters{
		Files:     c.files.Load(),
		Dirs:      c.dirs.Load(),
		Symlinks:  c.symlinks.Load(),
		Other:     c.other.Load(),
		Collapsed: c.collapsed.Load(),
	}
}

// ExclusiveCounters is an immutable snapshot of [CountingExclusiveHandler] counters.
type ExclusiveCounters struct {
	Files, Dirs, Symlinks, Other, Collapsed int64
}

// Total returns the sum of all classified entries.
func (e ExclusiveCounters) Total() int64 { return e.Files + e.Dirs + e.Symlinks + e.Other }

// CountingCommonHandler tallies common paths by outcome.
type CountingCommonHandler struct {
	total     atomic.Int64
	equal     atomic.Int64
	changed   atomic.Int64
	unchecked atomic.Int64
	mismatch  atomic.Int64
}

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
func (c *CountingCommonHandler) Snapshot() CommonCounters {
	return CommonCounters{
		Total:     c.total.Load(),
		Equal:     c.equal.Load(),
		Changed:   c.changed.Load(),
		Unchecked: c.unchecked.Load(),
		Mismatch:  c.mismatch.Load(),
	}
}

// CommonCounters is an immutable snapshot of [CountingCommonHandler] counters.
type CommonCounters struct {
	Total, Equal, Changed, Unchecked, Mismatch int64
}

// ─────────────────────────────────────────────────────────────────────────────
// Accumulating handler — feeds a PruningSet
// ─────────────────────────────────────────────────────────────────────────────

// AccumulatingExclusiveHandler appends each exclusive path into a [PruningSet].
// Designed for two-phase workflows: collect all paths, then batch-process.
type AccumulatingExclusiveHandler struct {
	Set *PruningSet
}

func (a *AccumulatingExclusiveHandler) HandleExclusive(_ context.Context, ep ExclusivePath) error {
	a.Set.Add(ep)
	return nil
}

// ─────────────────────────────────────────────────────────────────────────────
// Collecting handlers — in-memory accumulation (test doubles / audit)
// ─────────────────────────────────────────────────────────────────────────────

// CollectingExclusiveHandler accumulates all ExclusivePath values in memory.
type CollectingExclusiveHandler struct {
	mu    noCopy
	paths []ExclusivePath
}

func (c *CollectingExclusiveHandler) HandleExclusive(_ context.Context, ep ExclusivePath) error {
	c.mu.Lock()
	defer c.mu.Unlock()
	c.paths = append(c.paths, ep)
	return nil
}

// Paths returns a snapshot of accumulated paths.
func (c *CollectingExclusiveHandler) Paths() []ExclusivePath {
	c.mu.Lock()
	defer c.mu.Unlock()
	out := make([]ExclusivePath, len(c.paths))
	copy(out, c.paths)
	return out
}

// Reset clears the accumulated paths.
func (c *CollectingExclusiveHandler) Reset() {
	c.mu.Lock()
	c.paths = c.paths[:0]
	c.mu.Unlock()
}

// CollectingCommonHandler accumulates all CommonPath values in memory.
type CollectingCommonHandler struct {
	mu    noCopy
	paths []CommonPath
}

func (c *CollectingCommonHandler) HandleCommon(_ context.Context, cp CommonPath) error {
	c.mu.Lock()
	defer c.mu.Unlock()
	c.paths = append(c.paths, cp)
	return nil
}

// Paths returns a snapshot of accumulated paths.
func (c *CollectingCommonHandler) Paths() []CommonPath {
	c.mu.Lock()
	defer c.mu.Unlock()
	out := make([]CommonPath, len(c.paths))
	copy(out, c.paths)
	return out
}

// Reset clears the accumulated paths.
func (c *CollectingCommonHandler) Reset() {
	c.mu.Lock()
	c.paths = c.paths[:0]
	c.mu.Unlock()
}

// ─────────────────────────────────────────────────────────────────────────────
// DryRun handler — prints what would happen
// ─────────────────────────────────────────────────────────────────────────────

// DryRunExclusiveHandler records every exclusive path that would have been
// deleted, without performing any filesystem mutation.
type DryRunExclusiveHandler struct {
	// Writer is where dry-run output is written. Defaults to os.Stdout.
	Writer io.Writer
}

func (r *DryRunExclusiveHandler) HandleExclusive(_ context.Context, ep ExclusivePath) error {
	w := r.Writer
	if w == nil {
		w = os.Stdout
	}
	verb := "remove"
	if ep.Collapsed {
		verb = "removeAll"
	}
	_, err := fmt.Fprintf(w, "dry-run: %s %q (%s)\n", verb, ep.Path, ep.Kind)
	return err
}
