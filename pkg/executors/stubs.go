package executor

// stubs.go – mount-stub lifecycle management.
//
// When BuildKit sets up a container's root filesystem it creates "stub" files
// and directories at standard mount destinations before the actual bind-mounts
// are applied (e.g. /etc/resolv.conf, /etc/hosts, and user-specified mounts).
//
// MountStubsCleaner returns a cleanup function that removes those stubs after
// the container exits, but only if the stubs are still empty (i.e. the real
// mount was unmounted and left the stub behind).
//
// Reproducibility concern (https://github.com/moby/buildkit/issues/3148):
// Removing a file modifies its parent directory's mtime/atime.  The cleanup
// function therefore saves and restores the parent timestamps around each
// removal so that layer diffs remain byte-for-byte reproducible across builds.

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"syscall"
	"time"

	"github.com/containerd/continuity/fs"
	"github.com/moby/buildkit/util/bklog"
)

// ─── Well-known stub paths ────────────────────────────────────────────────────

// defaultStubPaths lists paths that BuildKit always stubs out regardless of the
// user-supplied mount list.
var defaultStubPaths = []string{
	"/etc/resolv.conf",
	"/etc/hosts",
}

// ─── MountStubsCleaner ───────────────────────────────────────────────────────

// MountStubsCleaner returns a cleanup closure that removes mount stubs created
// for the given set of mounts inside the container root at dir.
//
// Parameters:
//   - ctx      used for logging only; the cleanup closure itself is context-free.
//   - dir      absolute path to the container root on the host.
//   - mounts   the list of mounts configured for this container run.
//   - recursive when true, parent directories created solely for a stub are also
//               removed (bottom-up) if they are empty after the stub is deleted.
//
// The returned function is safe to call from any goroutine and is idempotent.
func MountStubsCleaner(ctx context.Context, dir string, mounts []Mount, recursive bool) func() {
	stubs := collectStubCandidates(ctx, dir, mounts, recursive)
	return func() { removeStubs(ctx, dir, stubs) }
}

// ─── Candidate collection ─────────────────────────────────────────────────────

// collectStubCandidates walks the stub paths for dir and returns the absolute
// host paths of entries that were created as stubs (i.e. do not exist in the
// real container filesystem before mounts are applied).
func collectStubCandidates(ctx context.Context, dir string, mounts []Mount, recursive bool) []string {
	// Build the full list of paths to inspect.
	candidates := make([]string, 0, len(defaultStubPaths)+len(mounts))
	candidates = append(candidates, defaultStubPaths...)
	for _, m := range mounts {
		candidates = append(candidates, m.Dest)
	}

	var stubs []string
	for _, p := range candidates {
		stubs = append(stubs, probeStubChain(ctx, dir, p, recursive)...)
	}
	return stubs
}

// probeStubChain resolves p inside dir and walks up the directory chain
// collecting paths that do not exist (were created as stubs).
// When recursive=false only the leaf path is considered.
func probeStubChain(ctx context.Context, dir, p string, recursive bool) []string {
	// Normalise to an absolute path inside the container.
	p = filepath.Join("/", p)
	if p == "/" {
		return nil
	}

	realPath, err := fs.RootPath(dir, p)
	if err != nil {
		bklog.G(ctx).WithError(err).Debugf("stubs: failed to resolve %q inside %q", p, dir)
		return nil
	}

	var chain []string
	for {
		_, statErr := os.Lstat(realPath)
		if !errors.Is(statErr, os.ErrNotExist) && !errors.Is(statErr, syscall.ENOTDIR) {
			break // path exists (or unrelated error) — stop climbing
		}
		chain = append(chain, realPath)

		if !recursive {
			break
		}
		parent := filepath.Dir(realPath)
		if realPath == parent || parent == dir {
			break // reached the container root
		}
		realPath = parent
	}
	return chain
}

// ─── Stub removal ────────────────────────────────────────────────────────────

// removeStubs iterates over stubs and removes each empty placeholder,
// restoring parent directory timestamps for reproducibility.
func removeStubs(ctx context.Context, dir string, stubs []string) {
	for _, p := range stubs {
		removeStub(ctx, dir, p)
	}
}

// removeStub removes a single stub file or empty directory at hostPath,
// then restores the parent directory's atime and mtime.
func removeStub(ctx context.Context, dir, hostPath string) {
	// Re-validate hostPath is still inside dir (defence against TOCTOU).
	safe, err := fs.RootPath(dir, strings.TrimPrefix(hostPath, dir))
	if err != nil || safe != hostPath {
		return
	}

	if !isEmptyStub(hostPath) {
		return // non-empty: real content was written; do not remove
	}

	parent := filepath.Dir(hostPath)
	if !isPathInsideDir(dir, parent) {
		return
	}

	// Save parent timestamps before removal so we can restore them.
	atime, mtime, err := readParentTimes(ctx, dir, parent)
	if err != nil {
		return // warning already logged in readParentTimes
	}

	if err := os.Remove(hostPath); err != nil {
		bklog.G(ctx).WithError(err).Warnf("stubs: failed to remove mount stub %q", hostPath)
		return
	}

	// Restore parent timestamps for reproducible layer diffs.
	if err := os.Chtimes(parent, atime, mtime); err != nil {
		bklog.G(ctx).WithError(err).Warnf(
			"stubs: failed to restore timestamps on %q after removing stub %q",
			parent, hostPath,
		)
	}
}

// ─── Helpers ─────────────────────────────────────────────────────────────────

// isEmptyStub returns true when hostPath is either:
//   - A regular file with size == 0 (empty file stub).
//   - A directory with no entries (empty directory stub).
//
// Returns false for non-existent paths (already cleaned up) and any path
// with real content.
func isEmptyStub(hostPath string) bool {
	st, err := os.Lstat(hostPath)
	if err != nil {
		return false // does not exist or unreadable
	}
	if st.IsDir() {
		entries, err := os.ReadDir(hostPath)
		return err == nil && len(entries) == 0
	}
	return st.Size() == 0
}

// isPathInsideDir reports whether p is a strict descendant of dir.
// Prevents directory traversal outside the container root.
func isPathInsideDir(dir, p string) bool {
	rel, err := filepath.Rel(dir, p)
	if err != nil {
		return false
	}
	return rel != ".." && !strings.HasPrefix(rel, ".."+string(filepath.Separator))
}

// parentTimes holds the saved timestamps of a parent directory.
type parentTimes struct {
	atime time.Time
	mtime time.Time
}

// readParentTimes reads and returns the atime and mtime of the given
// parent directory as time.Time values ready for os.Chtimes.
func readParentTimes(ctx context.Context, dir, parent string) (atime, mtime time.Time, err error) {
	st, err := os.Stat(parent)
	if err != nil {
		bklog.G(ctx).WithError(err).Warnf(
			"stubs: failed to stat parent %q (container root: %q)", parent, dir,
		)
		return time.Time{}, time.Time{}, err
	}
	mt := st.ModTime()
	at, aerr := fs.Atime(st)
	if aerr != nil {
		bklog.G(ctx).WithError(aerr).Warnf(
			"stubs: failed to read atime of %q; falling back to mtime", parent,
		)
		at = mt
	}
	return at, mt, nil
}
