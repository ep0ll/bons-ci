package fshash

import (
	"io/fs"
	"os"
	"slices"
)

// Walker abstracts filesystem traversal.
// All implementations MUST be safe for concurrent use.
type Walker interface {
	ReadDir(absPath string) ([]fs.DirEntry, error)
	Lstat(absPath string) (fs.FileInfo, error)
	ReadSymlink(absPath string) (string, error)
	// IsSorted reports whether ReadDir always returns entries in ascending
	// lexicographic order. When true, hashDir skips the O(n log n) sort.
	IsSorted() bool
}

// OSWalker is the default Walker for the local filesystem.
// os.ReadDir guarantees lexicographic order — IsSorted returns true.
type OSWalker struct{}

func (OSWalker) ReadDir(p string) ([]fs.DirEntry, error) { return os.ReadDir(p) }
func (OSWalker) Lstat(p string) (fs.FileInfo, error)     { return os.Lstat(p) }
func (OSWalker) ReadSymlink(p string) (string, error)    { return os.Readlink(p) }
func (OSWalker) IsSorted() bool                          { return true }

// FSWalker wraps an fs.FS. fs.ReadDir guarantees lexicographic order.
// ReadSymlink always returns "" (fs.FS has no Readlink).
type FSWalker struct{ FS fs.FS }

func (w FSWalker) ReadDir(p string) ([]fs.DirEntry, error) { return fs.ReadDir(w.FS, p) }
func (w FSWalker) Lstat(p string) (fs.FileInfo, error)     { return fs.Stat(w.FS, p) }
func (FSWalker) ReadSymlink(_ string) (string, error)      { return "", nil }
func (FSWalker) IsSorted() bool                            { return true }

// SortedWalker adapts any Walker whose ReadDir output may not be sorted.
type SortedWalker struct{ Inner Walker }

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

func (w SortedWalker) Lstat(p string) (fs.FileInfo, error) { return w.Inner.Lstat(p) }
func (w SortedWalker) ReadSymlink(p string) (string, error) {
	return w.Inner.ReadSymlink(p)
}
func (SortedWalker) IsSorted() bool { return true }
