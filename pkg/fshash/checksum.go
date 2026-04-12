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

// Checksummer computes reproducible, hermetic digests for files and directory
// trees. Create with New and reuse across calls; safe for concurrent use.
type Checksummer struct {
	opts Options
}

// New creates a Checksummer from the provided options.
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

// MustNew is like New but panics on error.
func MustNew(opts ...Option) *Checksummer {
	cs, err := New(opts...)
	if err != nil {
		panic(err)
	}
	return cs
}

// Options returns a copy of the configuration (safe to mutate).
func (cs *Checksummer) Options() Options { return cs.opts }

// Sum computes the checksum of the file or directory at absPath.
// SKILL §1: digest depends only on content, MetaFlag, Algorithm, and the
// compile-time shardThreshold constant — never on worker count.
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

	// SKILL §8: when FollowSymlinks=false, symlinks are leaves — no cycles
	// possible; skip visited-map allocation and cloning entirely.
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
		// Sort with "." (root dir) LAST — natural bottom-up traversal order.
		slices.SortFunc(entries, func(a, b EntryResult) int {
			if a.RelPath == "." {
				return 1
			}
			if b.RelPath == "." {
				return -1
			}
			if a.RelPath < b.RelPath {
				return -1
			}
			if a.RelPath > b.RelPath {
				return 1
			}
			return 0
		})
		mu.Unlock()
	}

	return Result{Digest: digest, Entries: entries}, nil
}

// Verify computes the digest of absPath and compares it to expected.
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

// VerifyError is returned by Checksummer.Verify on a digest mismatch.
type VerifyError struct {
	Path string
	Got  []byte
	Want []byte
}

func (e *VerifyError) Error() string {
	return fmt.Sprintf("fshash: verify %q: got %x, want %x", e.Path, e.Got, e.Want)
}

// ── sharding constants ────────────────────────────────────────────────────────
//
// SKILL §1+§3: shardThreshold is compile-time; mode selection is a function
// of file SIZE alone, guaranteeing identical digests across all worker counts.

const (
	shardThreshold = 4 * 1024 * 1024 // 4 MiB: files >= this use shard mode
	shardSize      = 1 * 1024 * 1024 // 1 MiB per parallel shard
)

// ── internal dispatcher ───────────────────────────────────────────────────────

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

// ── hashFile ──────────────────────────────────────────────────────────────────

func (cs *Checksummer) hashFile(
	absPath, relPath string,
	fi fs.FileInfo,
	collect func(EntryResult),
) ([]byte, error) {
	fileSize := fi.Size()

	if cs.opts.SizeLimit > 0 && fileSize > cs.opts.SizeLimit {
		return nil, &FileTooLargeError{Path: absPath, Size: fileSize, Limit: cs.opts.SizeLimit}
	}

	if cs.opts.FileCache != nil {
		if cached, ok := cs.opts.FileCache.Get(absPath); ok {
			collect(EntryResult{RelPath: relPath, Kind: KindFile, Digest: cached})
			return cached, nil
		}
	}

	h := cs.opts.Hasher.New()
	writeMetaHeader(h, fi, cs.opts.Meta, "")

	var dgst []byte

	if fi.Mode().IsRegular() && fileSize > 0 {
		var hashErr error
		// SKILL §1+§3: shard mode triggered by file SIZE alone — never by
		// Workers — guaranteeing digest idempotency across configurations.
		if fileSize >= shardThreshold {
			dgst, hashErr = cs.hashFileSharded(h, absPath, fileSize)
		} else {
			hashErr = cs.hashFileSequential(h, absPath, fileSize)
		}
		if hashErr != nil {
			return nil, hashErr
		}
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

// hashFileSequential reads the file with a pooled buffer (SKILL §2+§4).
func (cs *Checksummer) hashFileSequential(h hash.Hash, absPath string, size int64) error {
	f, err := openForHash(absPath, size) // SKILL §4: O_NOATIME + fadvise
	if err != nil {
		return fmt.Errorf("fshash: open %q: %w", absPath, err)
	}
	buf, _ := getBufForSize(size) // SKILL §2: smallest fitting tier
	_, err = io.CopyBuffer(h, f, *buf)
	putBuf(buf)
	releasePageCache(f, size) // SKILL §4: drop pages after hashing
	f.Close()
	if err != nil {
		return fmt.Errorf("fshash: read %q: %w", absPath, err)
	}
	return nil
}

// hashFileSharded hashes a large file using parallel pread calls (SKILL §3).
//
// Algorithm (DETERMINISTIC regardless of worker count — SKILL §1):
//  1. Divide into ceil(size/shardSize) chunks.
//  2. Hash each chunk independently in parallel via File.ReadAt (= pread(2),
//     safe to call concurrently on the same fd, no seek-position races).
//  3. Combine shard digests in INDEX ORDER with a sentinel:
//     H(masterHash | 0xFE | nShards_BE32 | shardSize_BE32 | d_0 | d_1 … d_N)
func (cs *Checksummer) hashFileSharded(h hash.Hash, absPath string, size int64) ([]byte, error) {
	f, err := openForHash(absPath, size)
	if err != nil {
		return nil, fmt.Errorf("fshash: open %q: %w", absPath, err)
	}
	defer func() {
		releasePageCache(f, size)
		f.Close()
	}()

	nShards := int((size + shardSize - 1) / shardSize)
	shardDigests := make([][]byte, nShards)
	shardErrs := make([]error, nShards)

	// Bound concurrency: min(Workers, nShards, NumCPU).
	concurrency := cs.opts.Workers
	if concurrency > nShards {
		concurrency = nShards
	}
	if maxCPU := runtime.NumCPU(); concurrency > maxCPU {
		concurrency = maxCPU
	}

	sem := make(chan struct{}, concurrency)
	var wg sync.WaitGroup

	for i := range nShards {
		i := i
		offset := int64(i) * shardSize
		chunkLen := shardSize
		if remaining := size - offset; remaining < int64(chunkLen) {
			chunkLen = int(remaining)
		}

		wg.Add(1)
		sem <- struct{}{}
		go func() {
			defer func() { <-sem; wg.Done() }()

			buf, _ := getBufForSize(int64(chunkLen))
			b := (*buf)[:chunkLen]

			if _, readErr := f.ReadAt(b, offset); readErr != nil && readErr != io.EOF {
				shardErrs[i] = fmt.Errorf("fshash: shard %d read %q: %w", i, absPath, readErr)
				putBuf(buf)
				return
			}

			sh := cs.opts.Hasher.New()
			mustWrite(sh, b)
			putBuf(buf)

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

	// Shard-mode sentinel: 0xFE | nShards_BE32 | shardSize_BE32.
	// Distinguishes sharded digests from sequential digests of the same bytes.
	//
	// BUG FIX: shardSize = 1<<20 = 1048576, so byte(shardSize>>8) = byte(4096)
	// overflows at compile time. Use binary.BigEndian.PutUint32 to encode into
	// a typed uint32 first; runtime truncation to byte is well-defined.
	var sentinel [9]byte
	sentinel[0] = 0xFE
	binary.BigEndian.PutUint32(sentinel[1:5], uint32(nShards))
	binary.BigEndian.PutUint32(sentinel[5:9], uint32(shardSize))
	mustWrite(h, sentinel[:])

	// Combine in DETERMINISTIC INDEX ORDER (SKILL §1).
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
		return nil, fmt.Errorf("fshash: cannot follow symlink %q: walker returned empty target (does the Walker support ReadSymlink?)", absPath)
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

	// SKILL §6: skip O(n log n) sort when Walker guarantees sorted output.
	if !cs.opts.Walker.IsSorted() {
		slices.SortFunc(des, func(a, b fs.DirEntry) int {
			if a.Name() < b.Name() {
				return -1
			}
			if a.Name() > b.Name() {
				return 1
			}
			return 0
		})
	}

	type child struct {
		name            string
		absPath         string
		relPath         string
		fi              fs.FileInfo
		kind            EntryKind
		includeInParent bool // false for ExcludeDir nodes
	}

	children := make([]child, 0, len(des))

	for _, de := range des {
		name := de.Name()

		// SKILL §7: fast-path join; handle "." root for FSWalker correctness.
		var childAbs string
		if absPath == "." {
			childAbs = name
		} else {
			childAbs = absPath + string(os.PathSeparator) + name
		}
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

	results := make([][]byte, len(children))
	var firstErr atomic.Pointer[error]

	// Files → worker pool (I/O-bound, can overlap).
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

	nul := []byte{0x00}

	// Dirs/symlinks → calling goroutine (isolates visited state without locking).
	for i, c := range children {
		if c.kind == KindFile || c.kind == KindOther {
			continue
		}

		// SKILL §8 (ExcludeDir): recurse but suppress the directory node's
		// own collect call; children at deeper relPaths pass through unchanged.
		childCollect := collect
		if !c.includeInParent {
			selfRelPath := c.relPath
			childCollect = func(e EntryResult) {
				if e.RelPath != selfRelPath {
					collect(e)
				}
			}
		}

		var childVisited map[string]struct{}
		if visited != nil {
			childVisited = cloneVisited(visited)
		}
		dgst, err := cs.sum(ctx, wp, c.absPath, c.relPath, c.fi, childVisited, childCollect)
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

	// Combine: H( dir-meta | name_1 NUL d_1 … name_n NUL d_n )
	h := cs.opts.Hasher.New()
	writeMetaHeader(h, fi, cs.opts.Meta, "")
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

// ── helpers ───────────────────────────────────────────────────────────────────

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

// FileDigest returns the digest of a single file.
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

// DirDigest returns the digest of a directory tree.
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

// FileCache maps absolute paths to pre-computed digests.
// Implementations MUST be safe for concurrent use.
type FileCache interface {
	Get(absPath string) (digest []byte, ok bool)
	Set(absPath string, digest []byte)
	Invalidate(absPath string)
}

// MemoryCache is a thread-safe in-memory FileCache backed by sync.Map.
// It never evicts entries; use MtimeCache for automatic invalidation.
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

// NewCachingChecksummer creates a Checksummer backed by cache.
func NewCachingChecksummer(cache FileCache, opts ...Option) (*Checksummer, error) {
	return New(append([]Option{WithFileCache(cache)}, opts...)...)
}

// ── Diff ──────────────────────────────────────────────────────────────────────

// DiffResult describes entry-level differences between two trees.
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
			continue // root represented by root digest, not in diff sets
		}
		m[entries[i].RelPath] = entries[i].Digest
	}
	return m
}
