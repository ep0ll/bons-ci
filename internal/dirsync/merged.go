package differ

import (
	"context"
	"errors"
	"fmt"
	"io/fs"
	"os"
	"path/filepath"
)

// MergedView abstracts read and write operations on the merged overlay directory.
//
// In the lower/upper/merged overlay model the merged view is the *only* target
// for mutations performed by this package. Lower and upper directories are
// read-only inputs used solely for classification; every delete, overwrite, or
// prune operation is applied to the merged view through this interface.
//
// Separating the merged view from the classifier makes it trivial to swap in
// alternative backends (in-memory, remote FS, test double) without altering
// any classification or batching logic.
//
// All implementations must be safe for concurrent use from multiple goroutines.
type MergedView interface {
	// Remove removes the entry at relPath from the merged view.
	// relPath must be a forward-slash-delimited path relative to Root().
	// An absent path must be treated as a no-op (idempotent).
	Remove(ctx context.Context, relPath string) error

	// RemoveAll removes relPath and all descendants from the merged view.
	// Semantics match os.RemoveAll: absent paths are a no-op.
	RemoveAll(ctx context.Context, relPath string) error

	// Stat returns FileInfo for relPath in the merged view.
	Stat(ctx context.Context, relPath string) (fs.FileInfo, error)

	// AbsPath resolves a relative path to its absolute counterpart within the
	// merged view. Callers requiring the underlying filesystem path (e.g. for
	// direct syscall use) should prefer this over constructing paths manually.
	AbsPath(relPath string) string

	// Root returns the absolute root path of the merged directory.
	Root() string
}

// FSMergedView implements [MergedView] against an on-disk directory.
// All operations are performed via standard POSIX syscalls. For high-throughput
// batch deletions consider wrapping with a [Batcher] that submits ops to an
// io_uring ring instead of making per-path syscalls.
type FSMergedView struct {
	root string // absolute, cleaned path
}

// NewFSMergedView constructs an [FSMergedView] rooted at dir.
// dir must be an absolute path to an existing directory. An error is returned
// if the path is relative or does not exist.
func NewFSMergedView(dir string) (MergedView, error) {
	abs, err := filepath.Abs(dir)
	if err != nil {
		return nil, fmt.Errorf("merged view: resolve %q: %w", dir, err)
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
// Uses os.Remove; ErrNotExist is silently swallowed (idempotent).
func (v *FSMergedView) Remove(_ context.Context, relPath string) error {
	abs := v.AbsPath(relPath)
	if err := os.Remove(abs); err != nil && !errors.Is(err, fs.ErrNotExist) {
		return fmt.Errorf("merged view remove %q: %w", relPath, err)
	}
	return nil
}

// RemoveAll implements [MergedView].
// Uses os.RemoveAll which is always idempotent for absent paths.
func (v *FSMergedView) RemoveAll(_ context.Context, relPath string) error {
	abs := v.AbsPath(relPath)
	if err := os.RemoveAll(abs); err != nil {
		return fmt.Errorf("merged view removeAll %q: %w", relPath, err)
	}
	return nil
}

// Stat implements [MergedView].
func (v *FSMergedView) Stat(_ context.Context, relPath string) (fs.FileInfo, error) {
	abs := v.AbsPath(relPath)
	info, err := os.Lstat(abs)
	if err != nil {
		return nil, fmt.Errorf("merged view stat %q: %w", relPath, err)
	}
	return info, nil
}

// ─────────────────────────────────────────────────────────────────────────────
// MemMergedView — in-memory test double
// ─────────────────────────────────────────────────────────────────────────────

// MemMergedView is an in-memory [MergedView] implementation intended for unit
// tests. It records every Remove and RemoveAll call without touching the
// filesystem. Callers may inspect Removed and RemovedAll after the pipeline
// completes.
type MemMergedView struct {
	mu         noCopy
	root       string
	Removed    []string // paths passed to Remove
	RemovedAll []string // paths passed to RemoveAll
}

// NewMemMergedView creates an in-memory view with the given logical root.
func NewMemMergedView(root string) *MemMergedView {
	return &MemMergedView{root: root}
}

// Root implements [MergedView].
func (m *MemMergedView) Root() string { return m.root }

// AbsPath implements [MergedView].
func (m *MemMergedView) AbsPath(relPath string) string {
	return filepath.Join(m.root, filepath.FromSlash(relPath))
}

// Remove implements [MergedView] (records without touching disk).
func (m *MemMergedView) Remove(_ context.Context, relPath string) error {
	m.mu.Lock()
	defer m.mu.Unlock()
	m.Removed = append(m.Removed, relPath)
	return nil
}

// RemoveAll implements [MergedView] (records without touching disk).
func (m *MemMergedView) RemoveAll(_ context.Context, relPath string) error {
	m.mu.Lock()
	defer m.mu.Unlock()
	m.RemovedAll = append(m.RemovedAll, relPath)
	return nil
}

// Stat implements [MergedView] — always returns ErrNotExist in the test double.
func (m *MemMergedView) Stat(_ context.Context, _ string) (fs.FileInfo, error) {
	return nil, fs.ErrNotExist
}

// AllOps returns the union of Removed and RemovedAll as a sorted-safe slice.
func (m *MemMergedView) AllOps() []string {
	m.mu.Lock()
	defer m.mu.Unlock()
	out := make([]string, 0, len(m.Removed)+len(m.RemovedAll))
	out = append(out, m.Removed...)
	out = append(out, m.RemovedAll...)
	return out
}
