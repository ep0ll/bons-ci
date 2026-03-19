package differ

import (
	"context"
	"errors"
	"fmt"
	"io/fs"
	"os"
	"path/filepath"
	"strings"
)

// WalkFn carries the callbacks invoked by [walkBoth] for each classified entry.
// Returning a non-nil error from any callback aborts the walk immediately.
//
// Separating the walk from channel sends (or any other delivery mechanism)
// keeps the walker a pure, testable function with no goroutine or channel
// lifecycle management of its own.
type WalkFn struct {
	// OnExclusive is called for each entry found only in the lower directory.
	// Collapsed directory entries subsume their entire subtrees: the walker
	// never calls OnExclusive for a descendant of an already-collapsed entry.
	// Nil means exclusive entries are silently skipped.
	OnExclusive func(ExclusivePath) error

	// OnCommon is called for each entry present in both lower and upper.
	// HashEqual is NOT populated here; that is the job of the downstream
	// [HashPipeline] stage.
	// Nil means common entries are silently skipped.
	OnCommon func(CommonPath) error
}

// walkBoth performs a recursive, sorted two-pointer merge walk of lower and
// upper directory trees rooted at lowerRoot and upperRoot respectively.
//
// # Algorithm — O(L + U) two-pointer merge
//
// os.ReadDir returns entries in lexicographic order (POSIX dcache guarantee).
// The two-pointer scan classifies every entry without sorting:
//
//   - lower[i] < upper[j]: entry only in lower → exclusive (collapsed if dir)
//   - lower[i] > upper[j]: entry only in upper → skipped
//   - lower[i] == upper[j]: entry in both      → common; recurse if both dirs
//
// # Syscall minimisation
//
// When an entire lower directory has no upper counterpart it is emitted as a
// single collapsed ExclusivePath and its subtree is never recursed. This
// reduces removal cost from O(N_files) to O(1) — one os.RemoveAll or one
// io_uring IORING_OP_UNLINKAT covers the whole tree.
//
// relDir is the path relative to both roots, using forward slashes.
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

	lowerDir := filepath.Join(lowerRoot, filepath.FromSlash(relDir))
	upperDir := filepath.Join(upperRoot, filepath.FromSlash(relDir))

	lowerEntries, err := readDirResolved(lowerDir, followSymlinks)
	if err != nil {
		return fmt.Errorf("walker: read lower %q: %w", lowerDir, err)
	}

	upperEntries, err := readDirResolved(upperDir, followSymlinks)
	if err != nil {
		if !errors.Is(err, fs.ErrNotExist) {
			return fmt.Errorf("walker: read upper %q: %w", upperDir, err)
		}
		// Entire upper subtree is absent: every lower entry is exclusive.
		return emitAllExclusive(ctx, lowerDir, relDir, lowerEntries, filter, followSymlinks, fn)
	}

	i, j := 0, 0
	for i < len(lowerEntries) && j < len(upperEntries) {
		if err := ctx.Err(); err != nil {
			return err
		}

		le, ue := lowerEntries[i], upperEntries[j]
		switch strings.Compare(le.Name(), ue.Name()) {
		case -1:
			if err := emitExclusive(ctx, lowerDir, relDir, le, filter, followSymlinks, fn); err != nil {
				return err
			}
			i++
		case 1:
			j++ // upper-only: skip
		case 0:
			relPath := joinRel(relDir, le.Name())
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

	// Drain remaining lower-only entries.
	for ; i < len(lowerEntries); i++ {
		if err := emitExclusive(ctx, lowerDir, relDir, lowerEntries[i], filter, followSymlinks, fn); err != nil {
			return err
		}
	}
	return nil
}

// handleCommonEntry dispatches a single entry whose name appears in both trees.
//
// Both directories → recurse; the directory itself is not emitted.
// Lower-dir / upper-non-dir → BuildKit overlay type mismatch: emit a CommonPath
// for the replacement and a collapsed ExclusivePath for the orphaned subtree.
// All other pairings → emit a single CommonPath.
func handleCommonEntry(
	ctx context.Context,
	lowerRoot, upperRoot, relPath string,
	lInfo, uInfo fs.FileInfo,
	filter Filter,
	followSymlinks bool,
	fn WalkFn,
) error {
	lIsDir, uIsDir := lInfo.IsDir(), uInfo.IsDir()

	switch {
	case lIsDir && uIsDir:
		return walkBoth(ctx, lowerRoot, upperRoot, relPath, filter, followSymlinks, fn)

	case lIsDir && !uIsDir:
		// BuildKit overlay: upper non-dir replaces lower dir entirely.
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

// emitExclusive builds and dispatches an ExclusivePath for a lower-only entry.
//
// # Collapse vs recurse decision
//
// For directories the function must choose between two strategies:
//
//   - Collapse: emit a single ExclusivePath{Collapsed:true} covering the entire
//     subtree.  O(1) syscall optimisation — one os.RemoveAll handles the tree.
//
//   - Recurse: descend into the directory and emit individual matching entries.
//     Required when include patterns might select only a subset of children
//     (e.g. "*.go" should yield pkg/main.go but not pkg/main.txt).
//
// We call filter.Include(relPath, false) — pretending the entry is NOT a
// directory — to check whether the path directly matches a pattern. Passing
// isDir=false bypasses the "couldMatchUnder" allowance in PatternFilter, so
// only explicit/direct matches return true.
//
//   - NoopFilter / no include patterns → always collapse.
//   - "vendor" pattern → direct match → collapse.
//   - "*.go" pattern → does NOT match "pkg" directly → recurse.
func emitExclusive(
	ctx context.Context,
	lowerDirAbs, relDir string,
	e os.DirEntry,
	filter Filter,
	followSymlinks bool,
	fn WalkFn,
) error {
	if ctx.Err() != nil {
		return ctx.Err()
	}
	relPath := joinRel(relDir, e.Name())
	info, err := e.Info()
	if err != nil {
		return fmt.Errorf("walker: stat exclusive %q: %w",
			filepath.Join(lowerDirAbs, e.Name()), err)
	}
	if !filter.Include(relPath, info.IsDir()) {
		return nil
	}

	if info.IsDir() {
		if filter.Include(relPath, false) {
			// Direct match → collapse the entire subtree into one op.
			return callExclusive(fn, ExclusivePath{
				Path:      relPath,
				Kind:      PathKindDir,
				Info:      info,
				Collapsed: true,
			})
		}
		// The dir only passes because descendants might match → recurse.
		// BUG FIX M2: use readDirResolved (not os.ReadDir) so that
		// followSymlinks is honoured when recursing into exclusive subdirs.
		// The original os.ReadDir call skipped symlink resolution, causing
		// symlink→dir entries to never be treated as directories and thus
		// never collapsed — even when followSymlinks=true.
		subAbs := filepath.Join(lowerDirAbs, e.Name())
		subEntries, err := readDirResolved(subAbs, followSymlinks)
		if err != nil {
			return fmt.Errorf("walker: read exclusive dir %q: %w", relPath, err)
		}
		return emitAllExclusive(ctx, subAbs, relPath, subEntries, filter, followSymlinks, fn)
	}

	// Non-directory: emit as a leaf exclusive entry.
	return callExclusive(fn, ExclusivePath{
		Path:      relPath,
		Kind:      PathKindOf(info),
		Info:      info,
		Collapsed: false,
	})
}

// emitAllExclusive calls emitExclusive for every entry in a lower directory
// whose upper counterpart is absent.
func emitAllExclusive(
	ctx context.Context,
	lowerDirAbs, relDir string,
	entries []os.DirEntry,
	filter Filter,
	followSymlinks bool,
	fn WalkFn,
) error {
	for _, e := range entries {
		if ctx.Err() != nil {
			return ctx.Err()
		}
		if err := emitExclusive(ctx, lowerDirAbs, relDir, e, filter, followSymlinks, fn); err != nil {
			return err
		}
	}
	return nil
}

// readDirResolved reads dirPath; when followSymlinks is true it wraps symlink
// entries whose targets are directories so IsDir() reflects the target.
func readDirResolved(dirPath string, followSymlinks bool) ([]os.DirEntry, error) {
	entries, err := os.ReadDir(dirPath)
	if err != nil {
		return nil, err
	}
	if !followSymlinks {
		return entries, nil
	}
	result := make([]os.DirEntry, len(entries))
	for i, e := range entries {
		if e.Type()&fs.ModeSymlink == 0 {
			result[i] = e
			continue
		}
		target := filepath.Join(dirPath, e.Name())
		tInfo, err := os.Stat(target)
		if err != nil {
			result[i] = e // broken symlink: keep Lstat-based entry
			continue
		}
		result[i] = &resolvedSymlinkEntry{DirEntry: e, resolved: tInfo}
	}
	return result, nil
}

// resolvedSymlinkEntry wraps a DirEntry that is a symlink pointing to a
// directory, overriding Info/IsDir/Type to reflect the symlink target.
type resolvedSymlinkEntry struct {
	os.DirEntry
	resolved fs.FileInfo
}

func (r *resolvedSymlinkEntry) Info() (fs.FileInfo, error) { return r.resolved, nil }
func (r *resolvedSymlinkEntry) IsDir() bool                { return r.resolved.IsDir() }
func (r *resolvedSymlinkEntry) Type() fs.FileMode          { return r.resolved.Mode().Type() }

// callExclusive invokes fn.OnExclusive if it is non-nil.
func callExclusive(fn WalkFn, ep ExclusivePath) error {
	if fn.OnExclusive != nil {
		return fn.OnExclusive(ep)
	}
	return nil
}

// callCommon invokes fn.OnCommon if it is non-nil.
func callCommon(fn WalkFn, cp CommonPath) error {
	if fn.OnCommon != nil {
		return fn.OnCommon(cp)
	}
	return nil
}

// joinRel joins a relative directory prefix with an entry name, handling the
// empty-prefix root case without a leading slash.
func joinRel(relDir, name string) string {
	if relDir == "" {
		return name
	}
	return relDir + "/" + name
}

// isContextErr reports whether err is a context cancellation or deadline.
func isContextErr(err error) bool {
	return errors.Is(err, context.Canceled) || errors.Is(err, context.DeadlineExceeded)
}
