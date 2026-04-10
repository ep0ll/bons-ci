package fshash

import (
	"bytes"
	"context"
	"encoding/binary"
	"fmt"
	"hash"
	"io"
	"io/fs"
	"os"
	"path/filepath"
	"runtime"
	"slices"
	"sort"
	"sync"
	"sync/atomic"
)

// ── Checksummer ───────────────────────────────────────────────────────────────

// Checksummer computes reproducible digests for files and directory trees.
// Create one with [New] and reuse it across calls; it is safe for concurrent
// use.
type Checksummer struct {
	opts Options
}

// New creates a [Checksummer] configured with the provided options.
func New(opts ...Option) (*Checksummer, error) {
	var o Options
	for _, opt := range opts {
		if err := opt(&o); err != nil {
			return nil, err
		}
	}
	applyDefaults(&o)
	return &Checksummer{opts: o}, nil
}

// MustNew is like [New] but panics on error.
func MustNew(opts ...Option) *Checksummer {
	cs, err := New(opts...)
	if err != nil {
		panic(err)
	}
	return cs
}

// Options returns a copy of the options used by this Checksummer.
func (cs *Checksummer) Options() Options { return cs.opts }

// Sum computes the checksum of the file or directory rooted at absPath.
//
// Digest derivation:
//   - Regular file:  H(meta-header || file-content)
//   - Directory:     H(dir-meta-header || child₁-name NUL child₁-digest …)
//   - Symlink (no follow): H(meta-header || link-target)
//
// Sum is safe for concurrent use.
func (cs *Checksummer) Sum(ctx context.Context, absPath string) (Result, error) {
	fi, err := cs.opts.Walker.Lstat(absPath)
	if err != nil {
		return Result{}, fmt.Errorf("fshash: stat %q: %w", absPath, err)
	}

	wp := newWorkerPool(cs.opts.Workers)
	defer wp.stop()

	var (
		mu      sync.Mutex
		entries []EntryResult
	)

	collect := func(e EntryResult) {
		if cs.opts.CollectEntries {
			mu.Lock()
			entries = append(entries, e)
			mu.Unlock()
		}
	}

	// When FollowSymlinks is false, no cycle can occur via symlinks
	// (they are hashed as leaves).  Pass a nil visited map to save the
	// alloc + clone cost on every recursive directory call.
	var visited map[string]struct{}
	if cs.opts.FollowSymlinks {
		visited = make(map[string]struct{}, 8)
	}

	digest, err := cs.sum(ctx, wp, absPath, ".", fi, visited, collect)
	if err != nil {
		return Result{}, err
	}

	if cs.opts.CollectEntries {
		mu.Lock()
		// Sort with "." last (root dir = bottom-up order).
		slices.SortFunc(entries, func(a, b EntryResult) int {
			switch {
			case a.RelPath == ".":
				return 1
			case b.RelPath == ".":
				return -1
			case a.RelPath < b.RelPath:
				return -1
			case a.RelPath > b.RelPath:
				return 1
			default:
				return 0
			}
		})
		mu.Unlock()
	}

	return Result{Digest: digest, Entries: entries}, nil
}

// Verify computes the digest of absPath and compares it to expected.
// Returns nil on match, *[VerifyError] on mismatch.
func (cs *Checksummer) Verify(ctx context.Context, absPath string, expected []byte) error {
	res, err := cs.Sum(ctx, absPath)
	if err != nil {
		return err
	}
	if !bytes.Equal(res.Digest, expected) {
		return &VerifyError{Path: absPath, Got: res.Digest, Want: expected}
	}
	return nil
}

// VerifyError is returned by [Checksummer.Verify] on a digest mismatch.
type VerifyError struct {
	Path string
	Got  []byte
	Want []byte
}

func (e *VerifyError) Error() string {
	return fmt.Sprintf("fshash: verify %q: got %x, want %x", e.Path, e.Got, e.Want)
}

// ── Internal recursive hasher ─────────────────────────────────────────────────

func (cs *Checksummer) sum(
	ctx context.Context,
	wp *workerPool,
	absPath, relPath string,
	fi fs.FileInfo,
	visited map[string]struct{},
	collect func(EntryResult),
) ([]byte, error) {
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	m := fi.Mode()
	if m&os.ModeSymlink != 0 {
		return cs.hashSymlink(ctx, wp, absPath, relPath, fi, visited, collect)
	}
	if m.IsDir() {
		return cs.hashDir(ctx, wp, absPath, relPath, fi, visited, collect)
	}
	return cs.hashFile(absPath, relPath, fi, collect)
}

// ── hashFile ─────────────────────────────────────────────────────────────────
//
// Optimisations:
//   - SizeLimit check before any I/O
//   - FileCache check before opening the file
//   - Tiered buffer pool: 4KB / 64KB / 1MB based on file size
//   - Zero-alloc digest: stack-allocated digestSink, heap-copy only on store
//   - Large-file shard hashing (>= shardThreshold): parallel ReadAt over N
//     fixed-size shards; results combined deterministically (order by shard
//     index) so the digest is identical regardless of worker count
//   - Explicit resource release (no defer in tight path)

// shardThreshold is the minimum file size for parallel shard hashing.
// Below this threshold sequential reading is faster due to goroutine overhead.
const shardThreshold = 4 * 1024 * 1024 // 4 MiB

// shardSize is the size of each parallel read chunk.
const shardSize = 1 * 1024 * 1024 // 1 MiB

func (cs *Checksummer) hashFile(
	absPath, relPath string,
	fi fs.FileInfo,
	collect func(EntryResult),
) ([]byte, error) {
	// ── size limit ────────────────────────────────────────────────────────────
	fileSize := fi.Size()
	if cs.opts.SizeLimit > 0 && fileSize > cs.opts.SizeLimit {
		return nil, &FileTooLargeError{Path: absPath, Size: fileSize, Limit: cs.opts.SizeLimit}
	}

	// ── cache check ───────────────────────────────────────────────────────────
	if cs.opts.FileCache != nil {
		if cached, ok := cs.opts.FileCache.Get(absPath); ok {
			collect(EntryResult{RelPath: relPath, Kind: KindFile, Digest: cached})
			return cached, nil
		}
	}

	h := cs.opts.Hasher.New()
	writeMetaHeader(h, fi, cs.opts.Meta, "")

	if fi.Mode().IsRegular() && fileSize > 0 {
		var dgst []byte
		var hashErr error

		if fileSize >= shardThreshold && cs.opts.Workers > 1 {
			dgst, hashErr = cs.hashFileSharded(h, absPath, fileSize)
		} else {
			hashErr = cs.hashFileSequential(h, absPath, fileSize)
		}
		if hashErr != nil {
			return nil, hashErr
		}

		if dgst == nil {
			var sink digestSink
			dgst = cloneDigest(sink.sum(h))
		}

		if cs.opts.FileCache != nil {
			cs.opts.FileCache.Set(absPath, dgst)
		}
		collect(EntryResult{RelPath: relPath, Kind: KindFile, Digest: dgst})
		return dgst, nil
	}

	// Zero-size or non-regular file (device, FIFO…) — hash metadata only.
	var sink digestSink
	dgst := cloneDigest(sink.sum(h))
	if cs.opts.FileCache != nil {
		cs.opts.FileCache.Set(absPath, dgst)
	}
	collect(EntryResult{RelPath: relPath, Kind: KindFile, Digest: dgst})
	return dgst, nil
}

// hashFileSequential reads the file once with a pooled buffer.
func (cs *Checksummer) hashFileSequential(h hash.Hash, absPath string, size int64) error {
	f, err := os.Open(absPath) //nolint:gosec
	if err != nil {
		return fmt.Errorf("fshash: open %q: %w", absPath, err)
	}

	buf, _ := getBufForSize(size)
	_, err = io.CopyBuffer(h, f, *buf)
	putBuf(buf)
	f.Close()

	if err != nil {
		return fmt.Errorf("fshash: read %q: %w", absPath, err)
	}
	return nil
}

// hashFileSharded hashes a large file using parallel ReadAt calls.
//
// Algorithm (deterministic regardless of CPU count):
//  1. Divide the file into ceil(size/shardSize) shards.
//  2. Hash each shard independently in parallel using ReadAt.
//  3. Write each shard's digest into h in index order, producing the
//     combined file hash.
//
// The "shard-mode" sentinel distinguishes this from a sequential hash of the
// same content, so callers are not surprised by digest changes when
// shardThreshold is crossed (files exactly at the boundary always use
// sequential mode).
func (cs *Checksummer) hashFileSharded(h hash.Hash, absPath string, size int64) ([]byte, error) {
	f, err := os.Open(absPath) //nolint:gosec
	if err != nil {
		return nil, fmt.Errorf("fshash: open %q: %w", absPath, err)
	}
	defer f.Close()

	nShards := int((size + shardSize - 1) / shardSize)
	shardDigests := make([][]byte, nShards)
	shardErrs := make([]error, nShards)

	// Use min(Workers, nShards, NumCPU) goroutines to avoid over-subscription.
	concurrency := cs.opts.Workers
	if concurrency > nShards {
		concurrency = nShards
	}
	if concurrency > runtime.NumCPU() {
		concurrency = runtime.NumCPU()
	}

	sem := make(chan struct{}, concurrency)
	var wg sync.WaitGroup

	for i := range nShards {
		i := i
		offset := int64(i) * shardSize
		length := shardSize
		if remaining := size - offset; remaining < int64(length) {
			length = int(remaining)
		}

		wg.Add(1)
		sem <- struct{}{}
		go func() {
			defer func() { <-sem; wg.Done() }()

			// Use a pooled large buffer; length ≤ shardSize = largeBufSize.
			pb := largePool.Get().(*[]byte)
			buf := (*pb)[:length]
			_, readErr := f.ReadAt(buf, offset)

			// Hash the shard data while the buffer is still live.
			sh := cs.opts.Hasher.New()
			mustWrite(sh, buf)

			// Return the buffer to the pool before storing the digest so
			// the GC can reclaim it quickly even under high concurrency.
			largePool.Put(pb)

			if readErr != nil && readErr != io.EOF {
				shardErrs[i] = fmt.Errorf("fshash: shard %d read %q: %w", i, absPath, readErr)
				return
			}
			var sink digestSink
			shardDigests[i] = cloneDigest(sink.sum(sh))
		}()
	}
	wg.Wait()

	for _, e := range shardErrs {
		if e != nil {
			return nil, e
		}
	}

	// Write shard-mode sentinel so this digest is distinguishable from a
	// sequential hash of the same bytes.
	//
	// Layout (9 bytes, big-endian):
	//   [0]    0xFE  shard-mode marker
	//   [1:5]  uint32  number of shards
	//   [5:9]  uint32  shard size in bytes
	var sentinel [9]byte
	sentinel[0] = 0xFE
	binary.BigEndian.PutUint32(sentinel[1:5], uint32(nShards))
	binary.BigEndian.PutUint32(sentinel[5:9], uint32(shardSize))
	mustWrite(h, sentinel[:])

	// Combine shard digests in deterministic order.
	for _, sd := range shardDigests {
		mustWrite(h, sd)
	}

	var sink digestSink
	return cloneDigest(sink.sum(h)), nil
}

// ── hashSymlink ───────────────────────────────────────────────────────────────

func (cs *Checksummer) hashSymlink(
	ctx context.Context,
	wp *workerPool,
	absPath, relPath string,
	fi fs.FileInfo,
	visited map[string]struct{},
	collect func(EntryResult),
) ([]byte, error) {
	target, err := cs.opts.Walker.ReadSymlink(absPath)
	if err != nil {
		return nil, fmt.Errorf("fshash: readlink %q: %w", absPath, err)
	}

	if !cs.opts.FollowSymlinks {
		h := cs.opts.Hasher.New()
		writeMetaHeader(h, fi, cs.opts.Meta, target)
		writeString(h, target)
		var sink digestSink
		dgst := cloneDigest(sink.sum(h))
		collect(EntryResult{RelPath: relPath, Kind: KindSymlink, Digest: dgst})
		return dgst, nil
	}

	if target == "" {
		return nil, fmt.Errorf("fshash: cannot follow symlink %q: walker returned empty target", absPath)
	}

	resolved := target
	if !filepath.IsAbs(target) {
		resolved = filepath.Join(filepath.Dir(absPath), target)
	}
	resolved = filepath.Clean(resolved)

	if _, seen := visited[resolved]; seen {
		return nil, fmt.Errorf("fshash: symlink cycle detected at %q -> %q", absPath, resolved)
	}
	visited2 := cloneVisited(visited)
	visited2[resolved] = struct{}{}

	targetFI, err := cs.opts.Walker.Lstat(resolved)
	if err != nil {
		return nil, fmt.Errorf("fshash: stat symlink target %q: %w", resolved, err)
	}
	return cs.sum(ctx, wp, resolved, relPath, targetFI, visited2, collect)
}

// ── hashDir ───────────────────────────────────────────────────────────────────
//
// Optimisations:
//   - Skips defensive sort when Walker.IsSorted() returns true
//   - Uses slices.SortFunc (generic, inlined compare) instead of sort.Slice
//   - Fast path path join: single string concat instead of filepath.Join
//   - Atomic first-error propagation: no errs slice; uses atomic pointer
//   - Avoids cloneVisited when FollowSymlinks=false (visited == nil)
//   - Single NUL-terminated write per child name (writeString + NUL byte)
//   - Zero-alloc digest via stack-based digestSink

func (cs *Checksummer) hashDir(
	ctx context.Context,
	wp *workerPool,
	absPath, relPath string,
	fi fs.FileInfo,
	visited map[string]struct{},
	collect func(EntryResult),
) ([]byte, error) {
	des, err := cs.opts.Walker.ReadDir(absPath)
	if err != nil {
		return nil, fmt.Errorf("fshash: readdir %q: %w", absPath, err)
	}

	// Sort only when the Walker does not guarantee order.
	if !cs.opts.Walker.IsSorted() {
		slices.SortFunc(des, func(a, b fs.DirEntry) int {
			switch {
			case a.Name() < b.Name():
				return -1
			case a.Name() > b.Name():
				return 1
			default:
				return 0
			}
		})
	}

	// ── Phase 1: resolve metadata, apply filter ───────────────────────────────

	type child struct {
		name            string
		absPath         string
		relPath         string
		fi              fs.FileInfo
		kind            EntryKind
		includeInParent bool // false for ExcludeDir nodes
	}

	children := make([]child, 0, len(des))
	sep := string(os.PathSeparator)

	for _, de := range des {
		name := de.Name()
		// Fast path join: absPath is already clean, name has no separators.
		childAbs := absPath + sep + name
		childRel := joinRelPath(relPath, name)

		info, err := de.Info()
		if err != nil {
			return nil, fmt.Errorf("fshash: stat %q: %w", childAbs, err)
		}

		kind := deKind(info)
		decision := cs.opts.Filter.Decide(childRel, info)
		if decision == Exclude || (decision == ExcludeDir && !info.IsDir()) {
			continue
		}

		children = append(children, child{
			name:            name,
			absPath:         childAbs,
			relPath:         childRel,
			fi:              info,
			kind:            kind,
			includeInParent: decision != ExcludeDir,
		})
	}

	// ── Phase 2: compute digests ──────────────────────────────────────────────
	//
	// Files → worker pool (I/O-bound, can overlap).
	// Dirs/symlinks → calling goroutine (must isolate visited state).
	//
	// Error propagation: atomic pointer to the first error avoids allocating a
	// full []error slice (the common path has zero errors).

	results := make([][]byte, len(children))
	var firstErr atomic.Pointer[error]

	var wg sync.WaitGroup
	for i, c := range children {
		if c.kind != KindFile && c.kind != KindOther {
			continue
		}
		i, c := i, c
		wg.Add(1)
		wp.submit(func() {
			defer wg.Done()
			if ctx.Err() != nil {
				e := ctx.Err()
				firstErr.CompareAndSwap(nil, &e)
				return
			}
			dgst, err := cs.hashFile(c.absPath, c.relPath, c.fi, collect)
			results[i] = dgst
			if err != nil {
				firstErr.CompareAndSwap(nil, &err)
			}
		})
	}

	for i, c := range children {
		if c.kind == KindFile || c.kind == KindOther {
			continue
		}
		// Only clone visited when we actually follow symlinks.
		var childVisited map[string]struct{}
		if visited != nil {
			childVisited = cloneVisited(visited)
		}
		dgst, err := cs.sum(ctx, wp, c.absPath, c.relPath, c.fi, childVisited, collect)
		results[i] = dgst
		if err != nil {
			wg.Wait()
			return nil, err
		}
	}

	wg.Wait()

	if ep := firstErr.Load(); ep != nil {
		return nil, *ep
	}

	// ── Phase 3: combine in sorted-name order ─────────────────────────────────
	//
	// Format: H( dir-meta | name₁ NUL digest₁ … nameₙ NUL digestₙ )
	//
	// The NUL byte between name and digest prevents ambiguity between
	// "abc" + digest and "ab" + "c" + digest.

	h := cs.opts.Hasher.New()
	writeMetaHeader(h, fi, cs.opts.Meta, "")
	nul := []byte{0x00}
	for i, c := range children {
		if !c.includeInParent {
			continue
		}
		writeString(h, c.name)
		mustWrite(h, nul)
		mustWrite(h, results[i])
	}

	var sink digestSink
	dgst := cloneDigest(sink.sum(h))
	collect(EntryResult{RelPath: relPath, Kind: KindDir, Digest: dgst})
	return dgst, nil
}

// ── Helpers ───────────────────────────────────────────────────────────────────

func joinRelPath(parent, name string) string {
	if parent == "." || parent == "" {
		return name
	}
	return parent + "/" + name
}

func deKind(fi fs.FileInfo) EntryKind {
	m := fi.Mode()
	switch {
	case m.IsDir():
		return KindDir
	case m&os.ModeSymlink != 0:
		return KindSymlink
	case m.IsRegular():
		return KindFile
	default:
		return KindOther
	}
}

func cloneVisited(m map[string]struct{}) map[string]struct{} {
	out := make(map[string]struct{}, len(m)+1)
	for k := range m {
		out[k] = struct{}{}
	}
	return out
}

// ── Convenience API ───────────────────────────────────────────────────────────

// FileDigest returns the SHA-256 digest of a single file at absPath.
func FileDigest(ctx context.Context, absPath string, opts ...Option) ([]byte, error) {
	cs, err := New(append([]Option{WithWorkers(1)}, opts...)...)
	if err != nil {
		return nil, err
	}
	res, err := cs.Sum(ctx, absPath)
	if err != nil {
		return nil, err
	}
	return res.Digest, nil
}

// DirDigest returns the digest of the directory tree rooted at absPath.
func DirDigest(ctx context.Context, absPath string, opts ...Option) ([]byte, error) {
	cs, err := New(opts...)
	if err != nil {
		return nil, err
	}
	res, err := cs.Sum(ctx, absPath)
	if err != nil {
		return nil, err
	}
	return res.Digest, nil
}

// ── FileCache ─────────────────────────────────────────────────────────────────

// FileCache maps absolute paths to previously computed digests.
// Implementations must be safe for concurrent use.
type FileCache interface {
	Get(absPath string) (digest []byte, ok bool)
	Set(absPath string, digest []byte)
	Invalidate(absPath string)
}

// MemoryCache is a thread-safe in-memory [FileCache].
type MemoryCache struct{ m sync.Map }

func (c *MemoryCache) Get(absPath string) ([]byte, bool) {
	v, ok := c.m.Load(absPath)
	if !ok {
		return nil, false
	}
	return v.([]byte), true
}

func (c *MemoryCache) Set(absPath string, dgst []byte) {
	d := make([]byte, len(dgst))
	copy(d, dgst)
	c.m.Store(absPath, d)
}

func (c *MemoryCache) Invalidate(absPath string) { c.m.Delete(absPath) }

// InvalidateAll clears all entries.
func (c *MemoryCache) InvalidateAll() {
	c.m.Range(func(k, _ any) bool { c.m.Delete(k); return true })
}

// NewCachingChecksummer creates a [Checksummer] backed by cache.
func NewCachingChecksummer(cache FileCache, opts ...Option) (*Checksummer, error) {
	return New(append([]Option{WithFileCache(cache)}, opts...)...)
}

// ── Diff ──────────────────────────────────────────────────────────────────────

// DiffResult describes entry-level differences between two directory trees.
type DiffResult struct {
	Added    []string
	Removed  []string
	Modified []string
}

// Empty returns true when there are no differences.
func (d DiffResult) Empty() bool {
	return len(d.Added)+len(d.Removed)+len(d.Modified) == 0
}

// Diff computes the entry-level difference between two trees.
func (cs *Checksummer) Diff(ctx context.Context, absPathA, absPathB string) (DiffResult, error) {
	sumA, err := cs.withCollect().Sum(ctx, absPathA)
	if err != nil {
		return DiffResult{}, fmt.Errorf("fshash: diff A: %w", err)
	}
	sumB, err := cs.withCollect().Sum(ctx, absPathB)
	if err != nil {
		return DiffResult{}, fmt.Errorf("fshash: diff B: %w", err)
	}
	return buildDiff(sumA.Entries, sumB.Entries), nil
}

func buildDiff(aEntries, bEntries []EntryResult) DiffResult {
	aMap := entryMap(aEntries)
	bMap := entryMap(bEntries)

	var dr DiffResult
	for p, da := range aMap {
		if db, ok := bMap[p]; !ok {
			dr.Removed = append(dr.Removed, p)
		} else if !bytes.Equal(da, db) {
			dr.Modified = append(dr.Modified, p)
		}
	}
	for p := range bMap {
		if _, ok := aMap[p]; !ok {
			dr.Added = append(dr.Added, p)
		}
	}
	sort.Strings(dr.Added)
	sort.Strings(dr.Removed)
	sort.Strings(dr.Modified)
	return dr
}

func (cs *Checksummer) withCollect() *Checksummer {
	opts2 := cs.opts
	opts2.CollectEntries = true
	return &Checksummer{opts: opts2}
}

func entryMap(entries []EntryResult) map[string][]byte {
	m := make(map[string][]byte, len(entries))
	for i := range entries {
		if entries[i].RelPath == "." {
			continue
		}
		m[entries[i].RelPath] = entries[i].Digest
	}
	return m
}
