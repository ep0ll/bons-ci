package fshash

import (
	"bytes"
	"context"
	"fmt"
	"hash"
	"io"
	"io/fs"
	"os"
	"path/filepath"
	"slices"
	"sync"
	"sync/atomic"

	"github.com/bons/bons-ci/pkg/fshash/core"
)

// ── Checksummer ───────────────────────────────────────────────────────────────

// Checksummer computes reproducible, hermetic digests for files and directory
// trees. Create with New; safe for concurrent use across goroutines.
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

// Options returns a copy of the current configuration. Safe to mutate.
func (cs *Checksummer) Options() Options { return cs.opts }

// withCollect returns a shallow copy with CollectEntries forced on.
func (cs *Checksummer) withCollect() *Checksummer {
	o := cs.opts
	o.CollectEntries = true
	return &Checksummer{opts: o}
}

// ── Sum ───────────────────────────────────────────────────────────────────────

// Sum computes the checksum of absPath (file or directory).
func (cs *Checksummer) Sum(ctx context.Context, absPath string) (Result, error) {
	fi, err := cs.opts.Walker.Lstat(absPath)
	if err != nil {
		return Result{}, fmt.Errorf("fshash: stat %q: %w", absPath, err)
	}

	wp := core.NewWorkerPool(cs.opts.Workers)
	defer wp.Stop()

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

	var visited map[string]struct{}
	if cs.opts.FollowSymlinks {
		visited = make(map[string]struct{}, 8)
	}

	digest, err := cs.dispatch(ctx, wp, absPath, ".", fi, visited, collect)
	if err != nil {
		return Result{}, err
	}

	if cs.opts.CollectEntries {
		mu.Lock()
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

// ── dispatch ──────────────────────────────────────────────────────────────────

func (cs *Checksummer) dispatch(
	ctx context.Context,
	wp *core.WorkerPool,
	absPath, relPath string,
	fi fs.FileInfo,
	visited map[string]struct{},
	collect func(EntryResult),
) ([]byte, error) {
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	m := fi.Mode()
	switch {
	case m&os.ModeSymlink != 0:
		return cs.hashSymlink(ctx, wp, absPath, relPath, fi, visited, collect)
	case m.IsDir():
		return cs.hashDir(ctx, wp, absPath, relPath, fi, visited, collect)
	default:
		return cs.hashFile(absPath, relPath, fi, collect)
	}
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
	core.WriteMetaHeader(h, fi, cs.opts.Meta, "")

	var dgst []byte

	if fi.Mode().IsRegular() && fileSize > 0 {
		// SKILL §1: mode selection based on SIZE alone — never on worker count.
		if fileSize >= core.ShardThreshold {
			var err error
			dgst, err = cs.hashFileSharded(h, absPath, fileSize)
			if err != nil {
				return nil, err
			}
		} else {
			if err := cs.hashFileSequential(h, absPath, fileSize); err != nil {
				return nil, err
			}
		}
	}

	if dgst == nil {
		var sink core.DigestSink
		dgst = core.CloneDigest(sink.Sum(h))
	}

	if cs.opts.FileCache != nil {
		cs.opts.FileCache.Set(absPath, dgst)
	}
	collect(EntryResult{RelPath: relPath, Kind: KindFile, Digest: dgst})
	return dgst, nil
}

// hashFileSequential reads the file into a pooled buffer and feeds it to h.
func (cs *Checksummer) hashFileSequential(h hash.Hash, absPath string, size int64) error {
	f, err := openForHash(absPath, size)
	if err != nil {
		return fmt.Errorf("fshash: open %q: %w", absPath, err)
	}
	buf := cs.opts.Pool.Get(size)
	_, err = io.CopyBuffer(h, f, *buf)
	cs.opts.Pool.Put(buf)
	releasePageCache(f, size)
	f.Close()
	if err != nil {
		return fmt.Errorf("fshash: read %q: %w", absPath, err)
	}
	return nil
}

// hashFileSharded hashes a large file in parallel using pread(2) (SKILL §3).
func (cs *Checksummer) hashFileSharded(h hash.Hash, absPath string, size int64) ([]byte, error) {
	f, err := openForHash(absPath, size)
	if err != nil {
		return nil, fmt.Errorf("fshash: open %q: %w", absPath, err)
	}
	defer func() { releasePageCache(f, size); f.Close() }()

	shardDigests, err := core.HashSharded(
		f, size,
		cs.opts.Hasher.New,
		cs.opts.Pool,
		cs.opts.Workers,
	)
	if err != nil {
		return nil, fmt.Errorf("fshash: shard %q: %w", absPath, err)
	}
	return core.CombineShards(h, shardDigests), nil
}

// ── hashSymlink ───────────────────────────────────────────────────────────────

func (cs *Checksummer) hashSymlink(
	ctx context.Context,
	wp *core.WorkerPool,
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
		core.WriteMetaHeader(h, fi, cs.opts.Meta, target)
		core.WriteString(h, target)
		var sink core.DigestSink
		dgst := core.CloneDigest(sink.Sum(h))
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
	return cs.dispatch(ctx, wp, resolved, relPath, targetFI, visited2, collect)
}

// ── hashDir ───────────────────────────────────────────────────────────────────

func (cs *Checksummer) hashDir(
	ctx context.Context,
	wp *core.WorkerPool,
	absPath, relPath string,
	fi fs.FileInfo,
	visited map[string]struct{},
	collect func(EntryResult),
) ([]byte, error) {
	des, err := cs.opts.Walker.ReadDir(absPath)
	if err != nil {
		return nil, fmt.Errorf("fshash: readdir %q: %w", absPath, err)
	}

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
		includeInParent bool
	}

	children := make([]child, 0, len(des))
	for _, de := range des {
		name := de.Name()
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

		decision := cs.opts.Filter.Decide(childRel, info)
		if decision == Exclude || (decision == ExcludeDir && !info.IsDir()) {
			continue
		}

		children = append(children, child{
			name:            name,
			absPath:         childAbs,
			relPath:         childRel,
			fi:              info,
			kind:            deKind(info),
			includeInParent: decision != ExcludeDir,
		})
	}

	results := make([][]byte, len(children))
	var firstErr atomic.Pointer[error]

	// Files → worker pool (I/O-bound; can be parallelised safely).
	var wg sync.WaitGroup
	for i, c := range children {
		if c.kind != KindFile && c.kind != KindOther {
			continue
		}
		i, c := i, c
		wg.Add(1)
		wp.Submit(func() {
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

	// Dirs/symlinks → calling goroutine (isolates visited-map state without locking).
	for i, c := range children {
		if c.kind == KindFile || c.kind == KindOther {
			continue
		}

		// SKILL §8 ExcludeDir: recurse but suppress the dir node's own collect call.
		childCollect := collect
		if !c.includeInParent {
			selfRel := c.relPath
			childCollect = func(e EntryResult) {
				if e.RelPath != selfRel {
					collect(e)
				}
			}
		}

		var childVisited map[string]struct{}
		if visited != nil {
			childVisited = cloneVisited(visited)
		}
		dgst, err := cs.dispatch(ctx, wp, c.absPath, c.relPath, c.fi, childVisited, childCollect)
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
	core.WriteMetaHeader(h, fi, cs.opts.Meta, "")
	for i, c := range children {
		if !c.includeInParent {
			continue
		}
		core.WriteString(h, c.name)
		core.MustWrite(h, nul)
		core.MustWrite(h, results[i])
	}

	var sink core.DigestSink
	dgst := core.CloneDigest(sink.Sum(h))
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

func digestsEqual(a, b []byte) bool {
	if len(a) != len(b) {
		return false
	}
	for i := range a {
		if a[i] != b[i] {
			return false
		}
	}
	return true
}
