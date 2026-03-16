package dirsync

import (
	"context"
	"fmt"
	"io/fs"
	"os"
	"path/filepath"
	"syscall"
)

// walker holds the shared context for the recursive directory comparison.
// It is created once per Diff call and never shared across goroutines
// (the walk itself is single-threaded; only hash workers run concurrently).
type walker struct {
	ctx            context.Context
	lowerRoot      string
	upperRoot      string
	followSymlinks bool

	// excCh receives exclusive-lower paths (written synchronously by compareDir).
	excCh chan<- ExclusivePath

	// comCh receives meta-equal common paths (fast path, written synchronously)
	// AND hash-checked paths (written asynchronously by pool workers).
	comCh chan<- CommonPath

	// pool offloads content hashing to worker goroutines.
	pool *hashPool
}

// ─── Core recursive algorithm ─────────────────────────────────────────────────

// compareDir performs the merge-sort scan for relDir within lowerRoot/upperRoot.
//
// Algorithm:
//  1. Read both directories with os.ReadDir (single getdents64 each, sorted).
//  2. Advance two pointers li (lower) and ui (upper) simultaneously:
//     • lower[li].Name < upper[ui].Name  → exclusive to lower; emit (pruned if dir).
//     • lower[li].Name > upper[ui].Name  → exclusive to upper; advance ui only.
//     • lower[li].Name == upper[ui].Name → common; check meta/hash; recurse if dir.
//  3. Remaining lower entries (tail) are all exclusive.
//
// Complexity: O(n) comparisons where n = max(|lower|, |upper|) at each level.
// Total: O(N) across the whole tree where N = total directory entries visited.
func (w *walker) compareDir(relDir string) error {
	if w.ctx.Err() != nil {
		return w.ctx.Err()
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
			// Entry exists only in lower.
			if err := w.emitExclusive(relDir, le); err != nil {
				return err
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
		if err := w.emitExclusive(relDir, lowerEntries[li]); err != nil {
			return err
		}
	}

	return nil
}

// ─── Common entry handler ─────────────────────────────────────────────────────

// handleCommon dispatches entries that exist in both trees.
//
// Dispatch table:
//
//	dir  ∩ dir        → recurse into sub-directory
//	file ∩ file       → metadata check → [hash check]
//	link ∩ link (!)   → compare readlink(2) targets
//	type mismatch     → lower entry is exclusive (upper replaced it)
//
// (!) only when followSymlinks==false; otherwise links are already resolved.
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
		// Both directories → recurse.
		return w.compareDir(relPath)

	case lIsLink && uIsLink && !w.followSymlinks:
		// Both symlinks (no-follow mode) → compare link targets.
		return w.handleSymlinks(relPath, lowerAbs, upperAbs, le, ue)

	case !lIsDir && !uIsDir && !lIsLink && !uIsLink:
		// Both non-directory, non-symlink entries.
		// Guard against special files (devices, sockets, pipes) that would
		// hang or error if opened for hashing.
		lType := le.info.Mode() & fs.ModeType
		uType := ue.info.Mode() & fs.ModeType
		if lType != uType {
			// Different special types → lower is exclusive (type changed).
			return w.emitExclusive(relDir, le)
		}
		if le.info.Mode().IsRegular() {
			// Regular files: metadata fast-path then incremental hash.
			return w.handleFiles(relPath, lowerAbs, upperAbs, le, ue)
		}
		// Special files (char/block devices, sockets, FIFOs): compare by
		// mode bits and raw device number; no content hash is meaningful.
		return w.handleSpecialFile(relPath, lowerAbs, upperAbs, le, ue)

	default:
		// Type mismatch (e.g. lower=dir / upper=file, or link vs non-link).
		// The lower entry is superseded by the upper; treat lower as exclusive.
		return w.emitExclusive(relDir, le)
	}
}

// handleFiles applies the two-tier equality check for a pair of regular files.
//
//   - Tier 1 (fast, 0 I/O):   sameMetadata → emit immediately with MetaEqual=true.
//   - Tier 2 (slow, file I/O): enqueue to hash pool → worker emits with HashChecked=true.
func (w *walker) handleFiles(relPath, lowerAbs, upperAbs string, le, ue dirEntry) error {
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

	equal := lTarget == uTarget
	cp := CommonPath{
		RelPath:     relPath,
		LowerAbs:    lowerAbs,
		UpperAbs:    upperAbs,
		LowerInfo:   le.info,
		UpperInfo:   ue.info,
		MetaEqual:   equal,  // same target → treat as meta-equal
		HashChecked: true,   // we did "check" (via readlink)
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



// emitExclusive emits a single lower entry as an ExclusivePath.
//
// DSA pruning rule:
//   - If the entry is a directory → Pruned=true, do NOT recurse.
//     The caller only needs os.RemoveAll(AbsPath): one syscall removes the
//     whole tree.  Without pruning we would recurse and emit O(subtree) paths.
//   - If the entry is a file/symlink → emit as-is (leaf node, no recursion).
func (w *walker) emitExclusive(relDir string, e dirEntry) error {
	relPath := joinRel(relDir, e.info.Name())
	ep := ExclusivePath{
		RelPath: relPath,
		AbsPath: filepath.Join(w.lowerRoot, relPath),
		IsDir:   e.info.IsDir(),
		Pruned:  e.info.IsDir(), // prune entire subtree for directories
	}

	select {
	case w.excCh <- ep:
		return nil
	case <-w.ctx.Done():
		return w.ctx.Err()
	}
}

// emitAllExclusive emits every entry in a directory as exclusive (pruned).
// Called when the entire lower sub-tree has no upper counterpart.
func (w *walker) emitAllExclusive(relDir string, entries []dirEntry) error {
	for _, e := range entries {
		if w.ctx.Err() != nil {
			return w.ctx.Err()
		}
		if err := w.emitExclusive(relDir, e); err != nil {
			return err
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
