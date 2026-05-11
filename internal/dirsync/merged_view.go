package dirsync

import (
	"context"
	"errors"
	"fmt"
	"io/fs"
	"os"
	"path/filepath"
	"sync"
)

// ─────────────────────────────────────────────────────────────────────────────
// MergedView interface
// ─────────────────────────────────────────────────────────────────────────────

// MergedView abstracts read and write operations on the merged overlay directory.
//
// In the lower/upper/merged overlay model the merged view is the only target
// for mutations. Lower and upper directories are read-only inputs used solely
// for classification; every delete or prune operation is applied to the merged
// view through this interface.
//
// Separating the merged view from the classifier makes it trivial to swap
// alternative backends (in-memory, remote FS, test double) without altering
// any classification or batching logic.
//
// # Idempotency contract
//
// All mutating operations (Remove, RemoveAll) must treat absent paths as a
// no-op. Callers rely on this to safely retry failed operations and to call
// cleanup functions unconditionally in deferred statements.
//
// All implementations must be safe for concurrent use from multiple goroutines.
type MergedView interface {
	// Remove removes the single entry at relPath from the merged view.
	// relPath must use forward slashes and be relative to Root().
	// An absent path is treated as a no-op (idempotent).
	Remove(ctx context.Context, relPath string) error

	// RemoveAll removes relPath and all its descendants from the merged view.
	// Semantics match os.RemoveAll: absent paths are a no-op.
	RemoveAll(ctx context.Context, relPath string) error

	// Stat returns FileInfo for relPath in the merged view.
	Stat(ctx context.Context, relPath string) (fs.FileInfo, error)

	// AbsPath resolves a relative path to its absolute path within the merged
	// view. Callers that need the underlying filesystem path for direct syscall
	// use (e.g. io_uring unlinkat) should use this rather than constructing
	// paths manually.
	AbsPath(relPath string) string

	// Root returns the absolute root path of the merged directory.
	Root() string
}

// ─────────────────────────────────────────────────────────────────────────────
// FSMergedView — on-disk implementation
// ─────────────────────────────────────────────────────────────────────────────

// FSMergedView implements [MergedView] against an on-disk directory.
//
// All operations use standard POSIX syscalls via the os package. For
// high-throughput batch deletions consider wrapping with an [IOURingBatcher]
// on Linux 5.11+, which submits many unlinkat calls with a single
// io_uring_enter(2) kernel crossing.
type FSMergedView struct {
	root string // absolute, cleaned path; immutable after construction
}

// NewFSMergedView constructs an [FSMergedView] rooted at dir.
//
// dir must point to an existing directory. An error is returned when dir
// does not exist, cannot be stat-checked, or is not a directory.
// The path is resolved to an absolute path via filepath.Abs so callers need
// not worry about the process working directory changing after construction.
func NewFSMergedView(dir string) (*FSMergedView, error) {
	abs, err := filepath.Abs(dir)
	if err != nil {
		return nil, fmt.Errorf("merged view: resolve path %q: %w", dir, err)
	}

	info, err := os.Stat(abs)
	if err != nil {
		return nil, fmt.Errorf("merged view: stat root %q: %w", abs, err)
	}
	if !info.IsDir() {
		return nil, fmt.Errorf("merged view: root %q is not a directory", abs)
	}

	return &FSMergedView{root: abs}, nil
}

// Root implements [MergedView].
func (v *FSMergedView) Root() string { return v.root }

// AbsPath implements [MergedView].
func (v *FSMergedView) AbsPath(relPath string) string {
	return filepath.Join(v.root, filepath.FromSlash(relPath))
}

// Remove implements [MergedView].
// Silently swallows fs.ErrNotExist to honour the idempotency contract.
func (v *FSMergedView) Remove(_ context.Context, relPath string) error {
	abs := v.AbsPath(relPath)
	if err := os.Remove(abs); err != nil && !errors.Is(err, fs.ErrNotExist) {
		return wrapOp("remove", relPath, err)
	}
	return nil
}

// RemoveAll implements [MergedView].
// os.RemoveAll is already idempotent for absent paths.
func (v *FSMergedView) RemoveAll(_ context.Context, relPath string) error {
	abs := v.AbsPath(relPath)
	if err := os.RemoveAll(abs); err != nil {
		return wrapOp("removeAll", relPath, err)
	}
	return nil
}

// Stat implements [MergedView].
// Uses Lstat so symlinks are reported as symlinks, not as their targets.
func (v *FSMergedView) Stat(_ context.Context, relPath string) (fs.FileInfo, error) {
	abs := v.AbsPath(relPath)
	info, err := os.Lstat(abs)
	if err != nil {
		return nil, wrapOp("stat", relPath, err)
	}
	return info, nil
}

// ─────────────────────────────────────────────────────────────────────────────
// MemMergedView — in-memory test double
// ─────────────────────────────────────────────────────────────────────────────

// MemMergedView is an in-memory [MergedView] implementation intended for unit
// tests and dry-run pipelines. It records every Remove and RemoveAll call
// without touching the filesystem.
//
// Callers inspect Removed and RemovedAll after the pipeline completes to
// assert on what would have been deleted.
//
// Thread-safe: all fields are protected by an internal mutex.
type MemMergedView struct {
	mu         sync.Mutex
	root       string
	Removed    []string // paths passed to Remove, in call order
	RemovedAll []string // paths passed to RemoveAll, in call order
}

// NewMemMergedView creates an in-memory view with the given logical root.
// The root path is stored as-is and returned by Root(); it is not stat-checked.
func NewMemMergedView(root string) *MemMergedView {
	return &MemMergedView{root: root}
}

// Root implements [MergedView].
func (m *MemMergedView) Root() string { return m.root }

// AbsPath implements [MergedView].
func (m *MemMergedView) AbsPath(relPath string) string {
	return filepath.Join(m.root, filepath.FromSlash(relPath))
}

// Remove implements [MergedView] — records the call without touching the disk.
func (m *MemMergedView) Remove(_ context.Context, relPath string) error {
	m.mu.Lock()
	defer m.mu.Unlock()
	m.Removed = append(m.Removed, relPath)
	return nil
}

// RemoveAll implements [MergedView] — records the call without touching the disk.
func (m *MemMergedView) RemoveAll(_ context.Context, relPath string) error {
	m.mu.Lock()
	defer m.mu.Unlock()
	m.RemovedAll = append(m.RemovedAll, relPath)
	return nil
}

// Stat implements [MergedView] — always returns fs.ErrNotExist.
// This is correct for a view that performs no real filesystem operations.
func (m *MemMergedView) Stat(_ context.Context, _ string) (fs.FileInfo, error) {
	return nil, fs.ErrNotExist
}

// AllOps returns the union of Removed and RemovedAll paths in call order
// (all Remove calls first, then all RemoveAll calls).
//
// The returned slice is a copy; modifications do not affect the view.
func (m *MemMergedView) AllOps() []string {
	m.mu.Lock()
	defer m.mu.Unlock()
	out := make([]string, 0, len(m.Removed)+len(m.RemovedAll))
	out = append(out, m.Removed...)
	out = append(out, m.RemovedAll...)
	return out
}

// Reset clears all recorded operations, returning the view to its initial state.
// Useful when reusing a MemMergedView across multiple test sub-cases.
func (m *MemMergedView) Reset() {
	m.mu.Lock()
	defer m.mu.Unlock()
	m.Removed = m.Removed[:0]
	m.RemovedAll = m.RemovedAll[:0]
}
