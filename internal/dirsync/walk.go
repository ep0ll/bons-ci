package dirsync

// walk.go – the recursive merge-sort directory comparison.
//
// walker is a value type created once per Diff call.  It owns no goroutines
// (the hash pool is owned by dirsync.go and injected here).  The walk itself
// is single-threaded; only hash workers run concurrently.
//
// Dependency injection points:
//   filter   PathFilter       – decides which entries to emit/traverse
//   tracker  *requiredTracker – records which required paths were seen
//   pool     *hashPool        – offloads SHA-256 computation
//
// The walker depends on interfaces, not concrete implementations.

import (
	"context"
	"fmt"
	"io/fs"
	"os"
	"path/filepath"
	"syscall"
)

// walker holds all per-Diff state for the recursive comparison.
type walker struct {
	ctx            context.Context
	lowerRoot      string
	upperRoot      string
	followSymlinks bool

	// Injected dependencies — all are interface/pointer types so the walk
	// goroutine never needs to know concrete implementations.
	filter  PathFilter       // decides Allow / Skip / Prune per entry
	tracker *requiredTracker // marks required paths as seen; nil = disabled

	// Output channels.
	// excCh is written synchronously (compareDir itself emits exclusive paths).
	// comCh is written both synchronously (meta-equal fast path) and
	// asynchronously (by hash pool workers).
	excCh chan<- ExclusivePath
	comCh chan<- CommonPath

	// pool offloads incremental SHA-256 hashing to N worker goroutines.
	pool *hashPool
}

// ─── Filter helper ────────────────────────────────────────────────────────────

// decide consults the filter and returns the FilterDecision for relPath.
// When filter is NopFilter or nil, returns FilterAllow with no overhead.
func (w *walker) decide(relPath string, isDir bool) FilterDecision {
	if w.filter == nil {
		return FilterAllow
	}
	return w.filter.Decide(relPath, isDir)
}

// ─── Core recursive algorithm ─────────────────────────────────────────────────

// compareDir performs the merge-sort scan for relDir within lowerRoot/upperRoot.
//
// Algorithm:
//  1. Read both directories with os.ReadDir (single getdents64 each, sorted).
//  2. Advance two pointers li (lower) and ui (upper) simultaneously:
//     • lower[li].Name < upper[ui].Name  → exclusive to lower; filter then emit.
//     • lower[li].Name > upper[ui].Name  → exclusive to upper; advance ui only.
//     • lower[li].Name == upper[ui].Name → common; filter then check/recurse.
//  3. Remaining lower entries (tail) are all exclusive.
//
// Complexity: O(n) comparisons where n = max(|lower|, |upper|) at each level.
// Total: O(N) across the whole tree where N = total directory entries visited.
func (w *walker) compareDir(relDir string) error {
	if w.ctx.Err() != nil {
		return w.ctx.Err()
	}

	// For non-root directories that exist in both trees: mark them seen so
	// RequiredPaths pointing at directories are satisfied even though dirs are
	// not emitted as CommonPath entries.
	if relDir != "" {
		w.tracker.markSeen(relDir)
	}

	lowerDir := filepath.Join(w.lowerRoot, relDir)
	upperDir := filepath.Join(w.upperRoot, relDir)

	lowerEntries, err := readDirEntries(lowerDir, w.followSymlinks)
	if err != nil {
		return fmt.Errorf("read lower dir %q: %w", lowerDir, err)
	}

	upperEntries, err := readDirEntries(upperDir, w.followSymlinks)
	if err != nil {
		if os.IsNotExist(err) {
			// Upper directory does not exist → the entire lower sub-tree is
			// exclusive.  Emit each top-level entry as pruned rather than
			// recursing.  This is the key pruning step: O(|lowerEntries|)
			// instead of O(entire sub-tree size).
			return w.emitAllExclusive(relDir, lowerEntries)
		}
		return fmt.Errorf("read upper dir %q: %w", upperDir, err)
	}

	li, ui := 0, 0
	for li < len(lowerEntries) && ui < len(upperEntries) {
		if w.ctx.Err() != nil {
			return w.ctx.Err()
		}

		le := lowerEntries[li]
		ue := upperEntries[ui]
		lName := le.info.Name()
		uName := ue.info.Name()

		switch {
		case lName < uName:
			// Entry exists only in lower: filter then emit exclusive.
			relPath := joinRel(relDir, lName)
			d := w.decide(relPath, le.info.IsDir())
			switch {
			case d == FilterAllow:
				// Emit as a pruned exclusive root (dir) or leaf (file).
				if err := w.emitExclusive(relPath, le); err != nil {
					return err
				}
			case le.info.IsDir() && d == FilterSkip:
				// The directory itself doesn't match an IncludePattern, but its
				// children might (FilterSkip → traverse, not FilterPrune → stop).
				// Walk lower-only to find individual matching descendants.
				if err := w.emitLowerOnlyDir(relPath); err != nil {
					return err
				}
			// d == FilterPrune  → excluded entirely, or (file && FilterSkip) → drop.
			}
			li++

		case lName > uName:
			// Entry exists only in upper — not relevant to caller.
			ui++

		default:
			// Entry exists in both directories.
			if err := w.handleCommon(relDir, le, ue); err != nil {
				return err
			}
			li++
			ui++
		}
	}

	// Tail: all remaining lower entries have no upper counterpart → exclusive.
	for ; li < len(lowerEntries); li++ {
		if w.ctx.Err() != nil {
			return w.ctx.Err()
		}
		le := lowerEntries[li]
		relPath := joinRel(relDir, le.info.Name())
		d := w.decide(relPath, le.info.IsDir())
		switch {
		case d == FilterAllow:
			if err := w.emitExclusive(relPath, le); err != nil {
				return err
			}
		case le.info.IsDir() && d == FilterSkip:
			if err := w.emitLowerOnlyDir(relPath); err != nil {
				return err
			}
		}
	}

	return nil
}

// ─── Common entry handler ─────────────────────────────────────────────────────

// handleCommon dispatches entries that exist in both trees.
//
// Dispatch table:
//
//	dir  ∩ dir        → filter check (Prune stops recursion) → recurse
//	file ∩ file       → filter check → metadata check → [hash check]
//	link ∩ link (!)   → filter check → compare readlink(2) targets
//	type mismatch     → filter check → lower entry emitted as exclusive
//
// (!) only when followSymlinks==false; otherwise links are already resolved.
//
// Filter semantics for directories:
//   FilterPrune → skip subtree entirely (excludes dir and all descendants).
//   FilterSkip  → still recurse (children may match IncludePatterns).
//   FilterAllow → recurse normally.
func (w *walker) handleCommon(relDir string, le, ue dirEntry) error {
	name := le.info.Name()
	relPath := joinRel(relDir, name)
	lowerAbs := filepath.Join(w.lowerRoot, relPath)
	upperAbs := filepath.Join(w.upperRoot, relPath)

	lIsDir := le.info.IsDir()
	uIsDir := ue.info.IsDir()

	// Symlink detection (only meaningful when followSymlinks==false).
	lIsLink := isSymlink(le.info)
	uIsLink := isSymlink(ue.info)

	switch {
	case lIsDir && uIsDir:
		// Both directories: mark as seen, apply prune check, then recurse.
		// FilterSkip still recurses (children may match IncludePatterns).
		// FilterPrune stops all descent — key syscall-saving optimisation for
		// ExcludePatterns on directories.
		w.tracker.markSeen(relPath)
		if w.decide(relPath, true) == FilterPrune {
			return nil
		}
		return w.compareDir(relPath)

	case lIsLink && uIsLink && !w.followSymlinks:
		// Both symlinks (no-follow mode): filter then compare targets.
		if w.decide(relPath, false) != FilterAllow {
			return nil
		}
		return w.handleSymlinks(relPath, lowerAbs, upperAbs, le, ue)

	case !lIsDir && !uIsDir && !lIsLink && !uIsLink:
		// Both non-directory, non-symlink entries.
		lType := le.info.Mode() & fs.ModeType
		uType := ue.info.Mode() & fs.ModeType

		if lType != uType {
			// Different special types (e.g. char device vs regular file):
			// lower is superseded → exclusive.
			if w.decide(relPath, le.info.IsDir()) != FilterAllow {
				return nil
			}
			return w.emitExclusive(relPath, le)
		}

		// Guard: never open special files (devices, sockets, FIFOs) for hashing.
		if w.decide(relPath, false) != FilterAllow {
			return nil
		}
		if le.info.Mode().IsRegular() {
			return w.handleFiles(relPath, lowerAbs, upperAbs, le, ue)
		}
		return w.handleSpecialFile(relPath, lowerAbs, upperAbs, le, ue)

	default:
		// Type mismatch (e.g. lower=dir / upper=file, or link vs non-link).
		// The lower entry is superseded by the upper; treat lower as exclusive.
		if w.decide(relPath, le.info.IsDir()) != FilterAllow {
			return nil
		}
		return w.emitExclusive(relPath, le)
	}
}

// ─── File / symlink / special-file handlers ───────────────────────────────────

// handleFiles applies the two-tier equality check for a pair of regular files.
//
//   - Tier 1 (fast, 0 I/O):   sameMetadata → emit immediately with MetaEqual=true.
//   - Tier 2 (slow, file I/O): enqueue to hash pool → worker emits with HashChecked=true.
//
// tracker.markSeen is called before any emission so RequiredPaths is satisfied
// even for files routed to the async hash pool.
func (w *walker) handleFiles(relPath, lowerAbs, upperAbs string, le, ue dirEntry) error {
	// Mark seen before any emission path (fast or slow) to ensure RequiredPaths
	// is satisfied regardless of which goroutine ultimately writes to comCh.
	w.tracker.markSeen(relPath)

	cp := CommonPath{
		RelPath:   relPath,
		LowerAbs:  lowerAbs,
		UpperAbs:  upperAbs,
		LowerInfo: le.info,
		UpperInfo: ue.info,
	}

	if sameMetadata(le.info, ue.info) {
		// Fast path: metadata agrees → assume equal; no content read needed.
		cp.MetaEqual = true
		select {
		case w.comCh <- cp:
		case <-w.ctx.Done():
			return w.ctx.Err()
		}
		return nil
	}

	// Slow path: metadata differs → offload incremental hash to worker pool.
	// The worker will send the enriched CommonPath to w.comCh when done.
	w.pool.submit(hashJob{
		relPath:   relPath,
		lowerAbs:  lowerAbs,
		upperAbs:  upperAbs,
		lowerInfo: le.info,
		upperInfo: ue.info,
	})
	return nil
}

// handleSymlinks compares two symbolic links by their readlink(2) targets.
// This avoids opening or hashing file content — O(1) syscalls per pair.
//
// The LowerHash / UpperHash fields of CommonPath carry the link target strings
// (not SHA-256 digests) so callers can inspect where each link points.
func (w *walker) handleSymlinks(relPath, lowerAbs, upperAbs string, le, ue dirEntry) error {
	lTarget, err := os.Readlink(lowerAbs)
	if err != nil {
		return fmt.Errorf("readlink lower %q: %w", lowerAbs, err)
	}
	uTarget, err := os.Readlink(upperAbs)
	if err != nil {
		return fmt.Errorf("readlink upper %q: %w", upperAbs, err)
	}

	w.tracker.markSeen(relPath)

	equal := lTarget == uTarget
	cp := CommonPath{
		RelPath:     relPath,
		LowerAbs:    lowerAbs,
		UpperAbs:    upperAbs,
		LowerInfo:   le.info,
		UpperInfo:   ue.info,
		MetaEqual:   equal,   // same target → treat as meta-equal
		HashChecked: true,    // we did "check" (via readlink)
		HashEqual:   equal,
		LowerHash:   lTarget, // target string stored in hash field for inspection
		UpperHash:   uTarget,
	}

	select {
	case w.comCh <- cp:
		return nil
	case <-w.ctx.Done():
		return w.ctx.Err()
	}
}

// handleSpecialFile compares device files, sockets, and FIFOs by their mode
// bits and (for character/block devices) raw device numbers.
// Content hashing is not performed — special files either have no meaningful
// content (sockets, FIFOs) or are identified purely by device number.
func (w *walker) handleSpecialFile(relPath, lowerAbs, upperAbs string, le, ue dirEntry) error {
	modeEqual := le.info.Mode() == ue.info.Mode()

	// For character and block devices, also compare the raw device number.
	devEqual := true
	lStat, lok := le.info.Sys().(*syscall.Stat_t)
	uStat, uok := ue.info.Sys().(*syscall.Stat_t)
	if lok && uok {
		devEqual = lStat.Rdev == uStat.Rdev
	}

	w.tracker.markSeen(relPath)

	equal := modeEqual && devEqual
	cp := CommonPath{
		RelPath:     relPath,
		LowerAbs:    lowerAbs,
		UpperAbs:    upperAbs,
		LowerInfo:   le.info,
		UpperInfo:   ue.info,
		MetaEqual:   equal,
		HashChecked: true, // "checked" via mode+rdev comparison
		HashEqual:   equal,
	}
	select {
	case w.comCh <- cp:
		return nil
	case <-w.ctx.Done():
		return w.ctx.Err()
	}
}

// ─── Exclusive emission ───────────────────────────────────────────────────────

// emitExclusive emits a single lower entry as an ExclusivePath.
//
// relPath is the caller-computed relative path (avoids recomputing it here).
//
// DSA pruning rule:
//   - If the entry is a directory → Pruned=true, do NOT recurse.
//     The caller only needs os.RemoveAll(AbsPath): one syscall removes the
//     whole tree.  Without pruning we would recurse and emit O(subtree) paths.
//   - If the entry is a file/symlink → emit as-is (leaf node, no recursion).
//
// tracker.markSeen is called before the channel send so RequiredPaths is
// satisfied before any consumer goroutine processes the entry.
func (w *walker) emitExclusive(relPath string, e dirEntry) error {
	ep := ExclusivePath{
		RelPath: relPath,
		AbsPath: filepath.Join(w.lowerRoot, relPath),
		IsDir:   e.info.IsDir(),
		Pruned:  e.info.IsDir(), // prune entire subtree for directories
	}

	w.tracker.markSeen(relPath)

	select {
	case w.excCh <- ep:
		return nil
	case <-w.ctx.Done():
		return w.ctx.Err()
	}
}

// ─── Lower-only dir walk ──────────────────────────────────────────────────────

// emitLowerOnlyDir walks a directory that exists only in lower and applies the
// filter to each descendant individually.
//
// This is called when an exclusive directory returns FilterSkip — meaning the
// directory itself doesn't match an IncludePattern, but its children might.
// FilterPrune would have stopped recursion at the parent; FilterSkip means
// "don't emit the directory, but still look inside."
//
// Dispatch per child:
//
//	FilterAllow            → emitExclusive (leaf file or pruned sub-dir root)
//	isDir && FilterSkip    → recurse (emitLowerOnlyDir)
//	FilterPrune            → skip entirely (excluded sub-tree)
//	file && FilterSkip     → skip (filtered out by IncludePattern)
func (w *walker) emitLowerOnlyDir(relDir string) error {
	if w.ctx.Err() != nil {
		return w.ctx.Err()
	}

	entries, err := readDirEntries(
		filepath.Join(w.lowerRoot, relDir),
		w.followSymlinks,
	)
	if err != nil {
		if os.IsNotExist(err) {
			return nil // vanished between listing and recursion; safe to skip
		}
		return fmt.Errorf("read lower dir %q: %w",
			filepath.Join(w.lowerRoot, relDir), err)
	}

	for _, e := range entries {
		if w.ctx.Err() != nil {
			return w.ctx.Err()
		}
		relPath := joinRel(relDir, e.info.Name())
		d := w.decide(relPath, e.info.IsDir())
		switch {
		case d == FilterAllow:
			// Emit individual file or pruned sub-directory root.
			if err := w.emitExclusive(relPath, e); err != nil {
				return err
			}
		case e.info.IsDir() && d == FilterSkip:
			// Sub-directory still doesn't match; recurse to find matching children.
			if err := w.emitLowerOnlyDir(relPath); err != nil {
				return err
			}
		// d == FilterPrune → excluded entirely.
		// file && d == FilterSkip → doesn't match include pattern, drop.
		}
	}
	return nil
}

// emitAllExclusive filters then emits every entry in a directory as exclusive.
// Called when the entire lower sub-tree has no upper counterpart.
//
// Per-entry filter dispatch:
//   - FilterAllow              → emitExclusive (Pruned=true for dirs, O(1) deletion).
//   - isDir && FilterSkip      → emitLowerOnlyDir (recurse to find matching children).
//   - FilterPrune or FilterSkip (file) → skip silently.
func (w *walker) emitAllExclusive(relDir string, entries []dirEntry) error {
	for _, e := range entries {
		if w.ctx.Err() != nil {
			return w.ctx.Err()
		}
		relPath := joinRel(relDir, e.info.Name())
		d := w.decide(relPath, e.info.IsDir())
		switch {
		case d == FilterAllow:
			if err := w.emitExclusive(relPath, e); err != nil {
				return err
			}
		case e.info.IsDir() && d == FilterSkip:
			if err := w.emitLowerOnlyDir(relPath); err != nil {
				return err
			}
		}
	}
	return nil
}

// ─── Helpers ──────────────────────────────────────────────────────────────────

// joinRel joins a relative directory prefix with an entry name.
// When relDir is empty (root level), returns name directly to avoid a leading
// slash or dot in the relative path.
func joinRel(relDir, name string) string {
	if relDir == "" {
		return name
	}
	return filepath.Join(relDir, name)
}
