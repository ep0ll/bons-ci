package differ

import "io/fs"

// PathKind classifies the type of a filesystem entry as observed by the differ.
// Values are derived from the lower-side Lstat result (or Stat when following
// symlinks).
type PathKind uint8

const (
	PathKindUnknown PathKind = iota
	PathKindFile             // regular file
	PathKindDir              // directory
	PathKindSymlink          // symbolic link (only when FollowSymlinks is false)
	PathKindOther            // device file, named pipe, socket, etc.
)

// String returns a human-readable label.
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

// PathKindOf derives a PathKind from an fs.FileInfo.
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

// ExclusivePath is a filesystem entry that exists only in the lower directory.
//
// # Collapsed semantics
//
// When Collapsed is true this single entry represents an entire directory
// subtree that is exclusive to lower. Consumers may treat it as a single
// atomic unit — for example a single os.RemoveAll call — without enumerating
// individual descendants. The differ guarantees that no child entries of a
// collapsed directory are ever emitted on the exclusive channel, minimising
// the number of syscalls required for downstream operations (os.RemoveAll,
// io_uring batch unlinks, etc.).
//
// Collapsed is always true when Kind == PathKindDir and the entire subtree
// is exclusive. It is never true for files, symlinks, or other entry kinds.
type ExclusivePath struct {
	// Path is the relative path from the lower root, using forward slashes.
	Path string

	// Kind is the entry type in lower.
	Kind PathKind

	// Info is the Lstat (or Stat when following symlinks) result from lower.
	Info fs.FileInfo

	// Collapsed is true when this directory entry subsumes its entire subtree.
	// Consumers MUST NOT further enumerate the subtree; doing so wastes syscalls
	// and re-does work already captured by this entry.
	Collapsed bool
}

// CommonPath is a filesystem entry present in both lower and upper.
//
// # Hash equality
//
// HashEqual is populated lazily by the differ's hash-enrichment stage after
// the entry leaves the walker. Consumers receiving CommonPath values from the
// common channel are guaranteed to receive them with HashEqual already set for
// regular files and symlinks.
//
//   - nil   → comparison not yet performed (can occur for dirs / special files)
//   - true  → content is byte-for-byte identical (or symlink targets match)
//   - false → content differs
//
// # Extensibility
//
// CommonPath is intentionally a plain value type so that consumers can attach
// behaviour via separate handler types, event channels, or visitor patterns
// without modifying this package. The hash comparison result provides the
// information needed to decide between deletion, update, copy, or any other
// action.
type CommonPath struct {
	// Path is the relative path from the respective root, using forward slashes.
	Path string

	// Kind is derived from the lower-side entry.
	Kind PathKind

	// LowerInfo is the Lstat (or Stat) result from the lower directory.
	LowerInfo fs.FileInfo

	// UpperInfo is the Lstat (or Stat) result from the upper directory.
	UpperInfo fs.FileInfo

	// HashEqual is set after the two-phase comparison completes.
	// Nil means the comparison has not been performed (e.g. for directories).
	HashEqual *bool
}

// IsContentEqual safely reads the HashEqual pointer.
// Returns (equal=false, checked=false) when the comparison has not run.
func (c *CommonPath) IsContentEqual() (equal, checked bool) {
	if c.HashEqual == nil {
		return false, false
	}
	return *c.HashEqual, true
}

// TypeMismatch reports whether lower and upper have different entry types at
// this path. This occurs when, for example, lower has a directory and upper
// has a regular file at the same path, following BuildKit overlay semantics
// where the upper non-directory implicitly removes the lower directory tree.
//
// Returns false when either LowerInfo or UpperInfo is nil (e.g. in unit tests
// that construct CommonPath values without real fs.FileInfo).
func (c *CommonPath) TypeMismatch() bool {
	if c.LowerInfo == nil || c.UpperInfo == nil {
		return false
	}
	return c.LowerInfo.Mode().Type() != c.UpperInfo.Mode().Type()
}

// boolPtr is an internal helper that boxes a bool into a *bool.
func boolPtr(v bool) *bool { return &v }
