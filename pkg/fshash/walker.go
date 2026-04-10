package fshash

import (
	"io/fs"
	"os"
	"slices"
)

// Walker abstracts filesystem traversal.
//
// Implementations MUST be safe for concurrent use.
type Walker interface {
	// ReadDir returns the direct children of the directory at absPath.
	// The caller always sorts the result before use, so implementations MAY
	// return entries in any order.  Returning them pre-sorted (as OSWalker
	// does) allows the caller to skip the sort entirely via IsSorted().
	ReadDir(absPath string) ([]fs.DirEntry, error)

	// Lstat returns file information for absPath without following symlinks.
	Lstat(absPath string) (fs.FileInfo, error)

	// ReadSymlink returns the target of the symbolic link at absPath.
	ReadSymlink(absPath string) (string, error)

	// IsSorted reports whether ReadDir always returns entries in ascending
	// lexicographic order by name.  Returning true allows callers to skip
	// the defensive sort, saving O(n log n) work per directory.
	IsSorted() bool
}

// OSWalker is the default [Walker] that reads the local filesystem.
//
// os.ReadDir (getdents64 on Linux) already returns entries in dirent order,
// which the kernel sorts for most filesystems (ext4, xfs, btrfs, APFS,
// NTFS).  We declare IsSorted()=true and skip the redundant sort.
type OSWalker struct{}

func (OSWalker) ReadDir(absPath string) ([]fs.DirEntry, error) {
	return os.ReadDir(absPath)
}

func (OSWalker) Lstat(absPath string) (fs.FileInfo, error) { return os.Lstat(absPath) }
func (OSWalker) ReadSymlink(absPath string) (string, error) { return os.Readlink(absPath) }

// IsSorted returns true — os.ReadDir guarantees lexicographic order.
func (OSWalker) IsSorted() bool { return true }

// FSWalker wraps an [fs.FS] to satisfy [Walker].
type FSWalker struct {
	FS fs.FS
}

func (w FSWalker) ReadDir(absPath string) ([]fs.DirEntry, error) {
	return fs.ReadDir(w.FS, absPath)
}

func (w FSWalker) Lstat(absPath string) (fs.FileInfo, error) { return fs.Stat(w.FS, absPath) }
func (FSWalker) ReadSymlink(_ string) (string, error)         { return "", nil }

// IsSorted returns true — fs.ReadDir guarantees lexicographic order.
func (FSWalker) IsSorted() bool { return true }

// SortedWalker wraps any Walker and ensures its ReadDir output is sorted.
// Use this to adapt third-party Walker implementations that do not guarantee
// sort order, without touching their code.
type SortedWalker struct {
	Inner Walker
}

func (w SortedWalker) ReadDir(absPath string) ([]fs.DirEntry, error) {
	entries, err := w.Inner.ReadDir(absPath)
	if err != nil {
		return nil, err
	}
	slices.SortFunc(entries, func(a, b fs.DirEntry) int {
		if a.Name() < b.Name() {
			return -1
		}
		if a.Name() > b.Name() {
			return 1
		}
		return 0
	})
	return entries, nil
}

func (w SortedWalker) Lstat(absPath string) (fs.FileInfo, error) { return w.Inner.Lstat(absPath) }
func (w SortedWalker) ReadSymlink(absPath string) (string, error) {
	return w.Inner.ReadSymlink(absPath)
}
func (SortedWalker) IsSorted() bool { return true }
