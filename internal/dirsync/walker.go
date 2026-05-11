package dirsync

import (
	"context"
	"errors"
	"fmt"
	"io/fs"
	"os"
	"path/filepath"
	"strings"

	securejoin "github.com/cyphar/filepath-securejoin"
)

// ─────────────────────────────────────────────────────────────────────────────
// WalkFn — callback container for the walk algorithm
// ─────────────────────────────────────────────────────────────────────────────

// WalkFn holds the callbacks invoked by [walkBoth] for each classified entry.
//
// Returning a non-nil error from any callback aborts the walk immediately.
// Channel consumers return ctx.Err() to signal cancellation, which lets the
// classifier goroutine exit without leaking.
//
// Keeping the walk separate from delivery (channels, slices, handlers) makes
// [walkBoth] a pure, deterministic, easily testable function.
type WalkFn struct {
	// OnExclusive is called for each entry found only in the lower directory.
	//
	// Collapsed entries (Collapsed == true) subsume their entire subtrees.
	// The walker guarantees that no descendant of a collapsed entry is ever
	// emitted — consumers must not enumerate beneath it.
	//
	// Nil: exclusive entries are silently skipped.
	OnExclusive func(ExclusivePath) error

	// OnCommon is called for each entry present in both lower and upper.
	// HashEqual is NOT set here; that is delegated to [HashPipeline].
	//
	// Nil: common entries are silently skipped.
	OnCommon func(CommonPath) error
}

// ─────────────────────────────────────────────────────────────────────────────
// walkBoth — O(L + U) two-pointer merge walk
// ─────────────────────────────────────────────────────────────────────────────

// walkBoth performs a recursive, sorted two-pointer merge walk of the lower
// and upper directory trees.
//
// # Path safety
//
// Every path join uses [securejoin.SecureJoin] from
// github.com/cyphar/filepath-securejoin, which prevents path-traversal attacks
// where malicious entry names contain ".." components or absolute paths.
//
// # Algorithm
//
// os.ReadDir returns entries in lexicographic order (guaranteed by POSIX
// dcache). The two-pointer scan classifies every entry in a single linear pass:
//
//   - lower[i] < upper[j]: lower-only → exclusive (collapsed if dir)
//   - lower[i] > upper[j]: upper-only → skipped (upper additions)
//   - lower[i] == upper[j]: in both   → common; recurse when both dirs
//
// # Syscall minimisation via collapse
//
// When an entire lower directory has no upper counterpart it is emitted as ONE
// collapsed [ExclusivePath] and its subtree is never recursed. This reduces
// downstream removal cost from O(N_files) syscalls to O(1) — a single
// os.RemoveAll or io_uring unlinkat covers the whole tree.
//
// # Parameters
//
//   - lowerRoot, upperRoot: absolute paths to the two trees.
//   - relDir: current position (forward slashes; empty string means roots).
//   - filter: controls which paths participate.
//   - followSymlinks: treats symlinks-to-dirs as real directories.
//   - fn: callbacks for each classified entry.
func walkBoth(
	ctx context.Context,
	lowerRoot, upperRoot string,
	relDir string,
	filter Filter,
	followSymlinks bool,
	fn WalkFn,
) error {
	if err := ctx.Err(); err != nil {
		return err
	}

	// SecureJoin prevents malicious entry names from escaping the root.
	lowerDir, err := securejoin.SecureJoin(lowerRoot, filepath.FromSlash(relDir))
	if err != nil {
		return fmt.Errorf("walker: secure join lower %q/%q: %w", lowerRoot, relDir, err)
	}
	upperDir, err := securejoin.SecureJoin(upperRoot, filepath.FromSlash(relDir))
	if err != nil {
		return fmt.Errorf("walker: secure join upper %q/%q: %w", upperRoot, relDir, err)
	}

	lowerEntries, err := readDirResolved(lowerDir, followSymlinks)
	if err != nil {
		return fmt.Errorf("walker: read lower %q: %w", lowerDir, err)
	}

	upperEntries, err := readDirResolved(upperDir, followSymlinks)
	if err != nil {
		if errors.Is(err, fs.ErrNotExist) {
			// Entire upper sub-tree absent: every lower entry is exclusive.
			return emitAllExclusive(ctx, lowerRoot, lowerDir, relDir, lowerEntries, filter, followSymlinks, fn)
		}
		return fmt.Errorf("walker: read upper %q: %w", upperDir, err)
	}

	// Two-pointer merge over lexicographically sorted lists.
	i, j := 0, 0
	for i < len(lowerEntries) && j < len(upperEntries) {
		if err := ctx.Err(); err != nil {
			return err
		}
		le, ue := lowerEntries[i], upperEntries[j]
		switch strings.Compare(le.Name(), ue.Name()) {

		case -1: // lower-only entry
			if err := emitExclusive(ctx, lowerRoot, lowerDir, relDir, le, filter, followSymlinks, fn); err != nil {
				return err
			}
			i++

		case 1: // upper-only entry — skip; overlay handles additions
			j++

		case 0: // entry in both trees
			relPath := joinRelPath(relDir, le.Name())
			lInfo, err := le.Info()
			if err != nil {
				return fmt.Errorf("walker: stat lower %q: %w", relPath, err)
			}
			uInfo, err := ue.Info()
			if err != nil {
				return fmt.Errorf("walker: stat upper %q: %w", relPath, err)
			}
			if filter.Include(relPath, lInfo.IsDir()) {
				if err := handleCommonEntry(ctx, lowerRoot, upperRoot, relPath,
					lInfo, uInfo, filter, followSymlinks, fn); err != nil {
					return err
				}
			}
			i++
			j++
		}
	}

	// Drain remaining lower-only entries after upper list is exhausted.
	for ; i < len(lowerEntries); i++ {
		if err := ctx.Err(); err != nil {
			return err
		}
		if err := emitExclusive(ctx, lowerRoot, lowerDir, relDir, lowerEntries[i], filter, followSymlinks, fn); err != nil {
			return err
		}
	}
	return nil
}

// ─────────────────────────────────────────────────────────────────────────────
// handleCommonEntry
// ─────────────────────────────────────────────────────────────────────────────

// handleCommonEntry dispatches a single entry present in both trees.
//
//  1. Both directories          → recurse (do not emit the dir itself).
//  2. Lower dir / upper non-dir → BuildKit type mismatch: emit CommonPath
//     for the replacement AND collapsed ExclusivePath for the orphaned subtree.
//  3. Any other pairing         → emit one CommonPath.
func handleCommonEntry(
	ctx context.Context,
	lowerRoot, upperRoot, relPath string,
	lInfo, uInfo fs.FileInfo,
	filter Filter,
	followSymlinks bool,
	fn WalkFn,
) error {
	switch {
	case lInfo.IsDir() && uInfo.IsDir():
		return walkBoth(ctx, lowerRoot, upperRoot, relPath, filter, followSymlinks, fn)

	case lInfo.IsDir() && !uInfo.IsDir():
		// BuildKit overlay: upper non-directory replaces lower directory tree.
		if err := callCommon(fn, CommonPath{
			Path: relPath, Kind: PathKindDir,
			LowerInfo: lInfo, UpperInfo: uInfo,
		}); err != nil {
			return err
		}
		return callExclusive(fn, ExclusivePath{
			Path: relPath, Kind: PathKindDir, Info: lInfo, Collapsed: true,
		})

	default:
		return callCommon(fn, CommonPath{
			Path: relPath, Kind: PathKindOf(lInfo),
			LowerInfo: lInfo, UpperInfo: uInfo,
		})
	}
}

// ─────────────────────────────────────────────────────────────────────────────
// emitExclusive — collapse vs. recurse decision for lower-only dirs
// ─────────────────────────────────────────────────────────────────────────────

// emitExclusive dispatches an [ExclusivePath] for a single lower-only entry.
//
// # Collapse vs. recurse for directories
//
//   - Collapse (Collapsed=true): emit ONE entry covering the entire subtree.
//     O(1) downstream — one os.RemoveAll.
//     Used when the directory directly matches the filter.
//   - Recurse: descend and emit individual matching entries.
//     Used when include patterns might select only some children.
//
// Decision rule: call filter.Include(relPath, isDir=false). Passing isDir=false
// bypasses PatternFilter's "couldHaveMatchingDescendant" allowance so only
// explicit matches trigger collapse. Indirect (descendant-only) matches recurse.
func emitExclusive(
	ctx context.Context,
	lowerRoot, lowerDirAbs, relDir string,
	entry os.DirEntry,
	filter Filter,
	followSymlinks bool,
	fn WalkFn,
) error {
	if err := ctx.Err(); err != nil {
		return err
	}
	relPath := joinRelPath(relDir, entry.Name())

	info, err := entry.Info()
	if err != nil {
		return fmt.Errorf("walker: stat exclusive %q: %w",
			filepath.Join(lowerDirAbs, entry.Name()), err)
	}
	if !filter.Include(relPath, info.IsDir()) {
		return nil // filtered out
	}

	if info.IsDir() {
		if filter.Include(relPath, false) {
			// Direct pattern match → collapse the entire subtree into one op.
			return callExclusive(fn, ExclusivePath{
				Path: relPath, Kind: PathKindDir, Info: info, Collapsed: true,
			})
		}
		// Only a potential descendant matches → recurse with SecureJoin.
		subAbs, err := securejoin.SecureJoin(lowerRoot, filepath.FromSlash(relPath))
		if err != nil {
			return fmt.Errorf("walker: secure join exclusive %q: %w", relPath, err)
		}
		subEntries, err := readDirResolved(subAbs, followSymlinks)
		if err != nil {
			return fmt.Errorf("walker: read exclusive dir %q: %w", relPath, err)
		}
		return emitAllExclusive(ctx, lowerRoot, subAbs, relPath, subEntries, filter, followSymlinks, fn)
	}

	// Non-directory: emit as a leaf exclusive entry.
	return callExclusive(fn, ExclusivePath{
		Path: relPath, Kind: PathKindOf(info), Info: info, Collapsed: false,
	})
}

// emitAllExclusive calls [emitExclusive] for every entry in a lower directory
// whose upper counterpart is absent.
func emitAllExclusive(
	ctx context.Context,
	lowerRoot, lowerDirAbs, relDir string,
	entries []os.DirEntry,
	filter Filter,
	followSymlinks bool,
	fn WalkFn,
) error {
	for _, entry := range entries {
		if err := ctx.Err(); err != nil {
			return err
		}
		if err := emitExclusive(ctx, lowerRoot, lowerDirAbs, relDir, entry, filter, followSymlinks, fn); err != nil {
			return err
		}
	}
	return nil
}

// ─────────────────────────────────────────────────────────────────────────────
// readDirResolved — symlink-aware directory listing
// ─────────────────────────────────────────────────────────────────────────────

// readDirResolved reads the entries of dirPath, optionally resolving symlinks
// to directories so the walker can recurse into them.
func readDirResolved(dirPath string, followSymlinks bool) ([]os.DirEntry, error) {
	entries, err := os.ReadDir(dirPath)
	if err != nil {
		return nil, err
	}
	if !followSymlinks {
		return entries, nil
	}
	// Resolve symlinks in-place: only symlinks that point to directories need
	// wrapping; others pass through unchanged.
	resolved := make([]os.DirEntry, len(entries))
	for i, e := range entries {
		if e.Type()&fs.ModeSymlink == 0 {
			resolved[i] = e
			continue
		}
		targetInfo, err := os.Stat(filepath.Join(dirPath, e.Name())) // Stat follows link
		if err != nil {
			resolved[i] = e // broken symlink: keep Lstat entry
			continue
		}
		resolved[i] = &resolvedSymlinkEntry{DirEntry: e, targetInfo: targetInfo}
	}
	return resolved, nil
}

// resolvedSymlinkEntry wraps a DirEntry for a symlink-to-directory, overriding
// Info/IsDir/Type to reflect the target so the walker treats it as a directory.
type resolvedSymlinkEntry struct {
	os.DirEntry
	targetInfo fs.FileInfo
}

func (r *resolvedSymlinkEntry) Info() (fs.FileInfo, error) { return r.targetInfo, nil }
func (r *resolvedSymlinkEntry) IsDir() bool                { return r.targetInfo.IsDir() }
func (r *resolvedSymlinkEntry) Type() fs.FileMode          { return r.targetInfo.Mode().Type() }

// ─────────────────────────────────────────────────────────────────────────────
// Internal path and callback helpers
// ─────────────────────────────────────────────────────────────────────────────

func callExclusive(fn WalkFn, ep ExclusivePath) error {
	if fn.OnExclusive != nil {
		return fn.OnExclusive(ep)
	}
	return nil
}

func callCommon(fn WalkFn, cp CommonPath) error {
	if fn.OnCommon != nil {
		return fn.OnCommon(cp)
	}
	return nil
}

// joinRelPath appends name to relDir using "/" separator.
// Returns name alone when relDir is empty (we are at the root).
func joinRelPath(relDir, name string) string {
	if relDir == "" {
		return name
	}
	return relDir + "/" + name
}
