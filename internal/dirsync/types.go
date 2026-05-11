package dirsync

import "io/fs"

// ─────────────────────────────────────────────────────────────────────────────
// PathKind
// ─────────────────────────────────────────────────────────────────────────────

// PathKind classifies the type of a filesystem entry.
// Values are derived from the lower-side Lstat result (or Stat when
// [WithFollowSymlinks] is enabled).
type PathKind uint8

const (
	PathKindUnknown PathKind = iota
	PathKindFile             // regular file
	PathKindDir              // directory
	PathKindSymlink          // symbolic link (only when FollowSymlinks is false)
	PathKindOther            // device file, named pipe, socket, etc.
)

// String returns a human-readable label for logging and debugging.
func (k PathKind) String() string {
	switch k {
	case PathKindFile:
		return "file"
	case PathKindDir:
		return "dir"
	case PathKindSymlink:
		return "symlink"
	case PathKindOther:
		return "other"
	default:
		return "unknown"
	}
}

// PathKindOf derives a [PathKind] from an [fs.FileInfo].
// When following symlinks the info is a Stat result, so ModeSymlink is never
// set and symlinks appear as the kind of their target.
func PathKindOf(info fs.FileInfo) PathKind {
	m := info.Mode()
	switch {
	case m.IsDir():
		return PathKindDir
	case m.IsRegular():
		return PathKindFile
	case m&fs.ModeSymlink != 0:
		return PathKindSymlink
	default:
		return PathKindOther
	}
}

// ─────────────────────────────────────────────────────────────────────────────
// ExclusivePath
// ─────────────────────────────────────────────────────────────────────────────

// ExclusivePath is a filesystem entry found only in the lower directory.
//
// # Collapsed semantics
//
// When Collapsed is true this entry represents an entire directory subtree
// that is exclusive to lower. Consumers treat it as one atomic unit — a
// single os.RemoveAll or io_uring unlinkat call covers everything beneath it.
//
// The walker guarantees that no child of a collapsed directory is ever emitted
// on the exclusive channel, so consumers need never enumerate descendants.
//
// Collapsed is true only when Kind == PathKindDir and the entire directory
// subtree is exclusive. It is never true for files, symlinks, or other kinds.
type ExclusivePath struct {
	// Path is the relative path from the lower root, using forward slashes.
	Path string

	// Kind is the entry type as observed in the lower directory.
	Kind PathKind

	// Info is the Lstat (or Stat when following symlinks) result from lower.
	Info fs.FileInfo

	// Collapsed is true when this directory entry subsumes its entire subtree.
	// Consumers MUST NOT further enumerate the subtree.
	Collapsed bool
}

// ─────────────────────────────────────────────────────────────────────────────
// CommonPath
// ─────────────────────────────────────────────────────────────────────────────

// CommonPath is a filesystem entry present in both lower and upper directories.
//
// # Hash equality lifecycle
//
// HashEqual starts nil when a CommonPath leaves the walker. It is stamped by
// [HashPipeline] after the content comparison completes:
//
//   - nil   — comparison not performed (directories, special files)
//   - true  — content byte-for-byte identical (or symlink targets match)
//   - false — content differs; the upper version is authoritative
//
// # BuildKit type mismatch
//
// When lower has a directory and upper has a regular file at the same path,
// the upper non-directory implicitly replaces the lower directory tree.
// [TypeMismatch] returns true for these entries. The walker also emits a
// collapsed [ExclusivePath] covering the orphaned lower subtree.
type CommonPath struct {
	// Path is the relative path from the respective root, using forward slashes.
	Path string

	// Kind is derived from the lower-side entry.
	Kind PathKind

	// LowerInfo is the Lstat (or Stat) result from the lower directory.
	LowerInfo fs.FileInfo

	// UpperInfo is the Lstat (or Stat) result from the upper directory.
	UpperInfo fs.FileInfo

	// HashEqual is nil until the hash pipeline stamps it.
	HashEqual *bool
}

// IsContentEqual safely reads the HashEqual pointer.
// Returns (false, false) when comparison has not been performed.
func (c *CommonPath) IsContentEqual() (equal, checked bool) {
	if c.HashEqual == nil {
		return false, false
	}
	return *c.HashEqual, true
}

// TypeMismatch reports whether lower and upper have different entry types.
// Returns false when either LowerInfo or UpperInfo is nil.
func (c *CommonPath) TypeMismatch() bool {
	if c.LowerInfo == nil || c.UpperInfo == nil {
		return false
	}
	return c.LowerInfo.Mode().Type() != c.UpperInfo.Mode().Type()
}

// hashEqualPtr boxes v into a *bool without escaping to the heap.
// Used only by the hash pipeline to stamp HashEqual.
func hashEqualPtr(v bool) *bool { return &v }
