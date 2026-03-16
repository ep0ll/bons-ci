// Package dirsync compares two directory trees (lower vs upper) and streams
// typed results over channels.
//
// Two output streams are produced:
//
//  1. ExclusivePath – paths that exist only in lower, pruned at the highest
//     directory boundary so a single os.RemoveAll per entry is sufficient.
//     The pruning DSA reduces deletion from O(subtree-size) syscalls to O(1)
//     per exclusive sub-tree.
//
//  2. CommonPath – paths present in both trees, enriched with a two-stage
//     equality check:
//     a) Fast path: size + mtime + inode (no I/O beyond the stat(2) already
//        done during directory listing).
//     b) Slow path: incremental SHA-256 content hashing (64 KiB chunks via a
//        buffer pool) performed only when the fast path fails.
//
// The walk algorithm is O(n) in directory entries (merge-sort scan over
// pre-sorted os.ReadDir output) with exactly one directory read per visited
// directory.  Content hashing is performed by a worker pool for parallelism.
package dirsync

import (
	"context"
	"io/fs"
	"runtime"
)

// ─── Public types ─────────────────────────────────────────────────────────────

// ExclusivePath is a path that exists only in the lower directory.
//
// DSA – pruned prefix tree:
// The emitted set forms a minimal cover of the exclusive sub-forest.
// When Pruned == true the entire sub-tree rooted at RelPath is exclusive;
// no descendants are emitted separately.  Callers therefore only need:
//
//	os.RemoveAll(ep.AbsPath)   // O(1) syscall, regardless of subtree depth
//
// This reduces deletion cost from O(n) individual stat+unlink calls to O(k)
// calls where k is the number of pruned roots, which is always ≤ n.
type ExclusivePath struct {
	RelPath string // path relative to lower root
	AbsPath string // absolute path in lower
	IsDir   bool
	// Pruned == true: entire subtree is exclusive to lower.
	// A single os.RemoveAll(AbsPath) is sufficient – no need to enumerate children.
	Pruned bool
}

// CommonPath is a path present in both lower and upper directories.
//
// The equality determination is tiered:
//  1. MetaEqual  – fast path passed; content assumed identical.
//  2. HashChecked – fast path failed; SHA-256 was computed.
//     HashEqual reports content equality.
//  3. Err        – hashing failed (e.g. permission denied, file removed mid-walk).
type CommonPath struct {
	RelPath   string
	LowerAbs  string
	UpperAbs  string
	LowerInfo fs.FileInfo
	UpperInfo fs.FileInfo

	// MetaEqual is true when size + mtime + (optionally) inode agree.
	// When true no content hash was performed.
	MetaEqual bool

	// HashChecked is true when content hashing was performed
	// (MetaEqual was false or entry is a symlink comparison).
	HashChecked bool
	// HashEqual is valid only when HashChecked == true.
	HashEqual bool
	// LowerHash / UpperHash are hex-encoded SHA-256 digests (for regular files)
	// or readlink(2) targets (for symlinks).  Populated when HashChecked == true.
	LowerHash string
	UpperHash string

	// Err is non-nil when hashing or readlink failed for this entry.
	Err error
}

// Options configures the Diff operation.
type Options struct {
	// FollowSymlinks: follow symlinks when stating entries.
	// Default (false): symlinks are treated as opaque leaf nodes and are
	// compared by their link target string, not by the target's content.
	FollowSymlinks bool

	// HashWorkers: goroutines dedicated to content hashing.
	// 0 → runtime.GOMAXPROCS(0).
	HashWorkers int

	// Channel buffer depths.  Larger buffers reduce producer stalls at the
	// cost of memory.  Defaults: 512 each.
	ExclusiveBuf int
	CommonBuf    int
}

func (o *Options) applyDefaults() {
	if o.HashWorkers <= 0 {
		o.HashWorkers = runtime.GOMAXPROCS(0)
	}
	if o.HashWorkers < 1 {
		o.HashWorkers = 1
	}
	if o.ExclusiveBuf <= 0 {
		o.ExclusiveBuf = 512
	}
	if o.CommonBuf <= 0 {
		o.CommonBuf = 512
	}
}

// Result holds the streaming output channels.
//
// ⚠ IMPORTANT: always drain Exclusive and Common fully before reading Err.
// Failing to drain blocks the background goroutine and leaks resources.
//
// Typical usage:
//
//	res := dirsync.Diff(ctx, lower, upper, opts)
//	var wg sync.WaitGroup
//	wg.Add(2)
//	go func() { defer wg.Done(); for ep := range res.Exclusive { /* … */ } }()
//	go func() { defer wg.Done(); for cp := range res.Common    { /* … */ } }()
//	wg.Wait()
//	if err := <-res.Err; err != nil { log.Fatal(err) }
type Result struct {
	// Exclusive streams paths present only in lower, with subtree pruning.
	Exclusive <-chan ExclusivePath
	// Common streams paths present in both, enriched with equality metadata.
	Common <-chan CommonPath
	// Err carries the first fatal walk error, or is closed empty on success.
	// Read only after draining Exclusive and Common.
	Err <-chan error
}

// ─── Entry point ──────────────────────────────────────────────────────────────

// Diff starts the comparison and returns immediately.
// Cancel ctx to abort; the background goroutine will exit promptly.
func Diff(ctx context.Context, lowerRoot, upperRoot string, opts Options) Result {
	opts.applyDefaults()

	excCh := make(chan ExclusivePath, opts.ExclusiveBuf)
	comCh := make(chan CommonPath, opts.CommonBuf)
	errCh := make(chan error, 1)

	go func() {
		// Hash workers write enriched CommonPath entries to comCh.
		pool := newHashPool(ctx, opts.HashWorkers, comCh)

		w := &walker{
			ctx:            ctx,
			lowerRoot:      lowerRoot,
			upperRoot:      upperRoot,
			followSymlinks: opts.FollowSymlinks,
			excCh:          excCh,
			comCh:          comCh,
			pool:           pool,
		}

		walkErr := w.compareDir("")

		// All exclusive paths are emitted synchronously by compareDir; close now.
		close(excCh)

		// Hash workers may still be running; wait for them to flush to comCh.
		pool.drain()
		close(comCh)

		// Report walk error only when the context was not already cancelled
		// (context cancellation is not an error from the caller's perspective).
		if walkErr != nil && ctx.Err() == nil {
			errCh <- walkErr
		}
		close(errCh)
	}()

	return Result{Exclusive: excCh, Common: comCh, Err: errCh}
}
