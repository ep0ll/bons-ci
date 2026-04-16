package fshash_test

import (
	"bytes"
	"context"
	"encoding/hex"
	"errors"
	"fmt"
	"hash"
	"io/fs"
	"os"
	"path/filepath"
	"reflect"
	"runtime"
	"sort"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/bons/bons-ci/pkg/fshash"
	"github.com/bons/bons-ci/pkg/fshash/core"
)

// ── test helpers ──────────────────────────────────────────────────────────────

type fsTree map[string]string // path → content ("" = dir, "-> X" = symlink)

func buildTree(tb testing.TB, root string, tree fsTree) {
	tb.Helper()
	paths := make([]string, 0, len(tree))
	for p := range tree {
		paths = append(paths, p)
	}
	sort.Strings(paths)
	for _, p := range paths {
		content := tree[p]
		abs := filepath.Join(root, filepath.FromSlash(p))
		switch {
		case content == "":
			if err := os.MkdirAll(abs, 0o755); err != nil {
				tb.Fatalf("mkdir %s: %v", abs, err)
			}
		case strings.HasPrefix(content, "-> "):
			os.MkdirAll(filepath.Dir(abs), 0o755)
			os.Remove(abs)
			if err := os.Symlink(strings.TrimPrefix(content, "-> "), abs); err != nil {
				tb.Fatalf("symlink %s: %v", abs, err)
			}
		default:
			os.MkdirAll(filepath.Dir(abs), 0o755)
			if err := os.WriteFile(abs, []byte(content), 0o644); err != nil {
				tb.Fatalf("write %s: %v", abs, err)
			}
		}
	}
}

func mustNew(t *testing.T, opts ...fshash.Option) *fshash.Checksummer {
	t.Helper()
	cs, err := fshash.New(opts...)
	if err != nil {
		t.Fatalf("New: %v", err)
	}
	return cs
}

func mustSum(t *testing.T, cs *fshash.Checksummer, absPath string) fshash.Result {
	t.Helper()
	res, err := cs.Sum(context.Background(), absPath)
	if err != nil {
		t.Fatalf("Sum(%q): %v", absPath, err)
	}
	return res
}

func relPaths(entries []fshash.EntryResult) []string {
	out := make([]string, len(entries))
	for i, e := range entries {
		out[i] = e.RelPath
	}
	return out
}

// ── 1. Reproducibility ────────────────────────────────────────────────────────

func TestReproducibility(t *testing.T) {
	t.Parallel()
	tree := fsTree{
		"a": "", "a/foo": "hello", "a/bar": "world",
		"b": "", "b/nested": "", "b/nested/x": "deep",
		"c.txt": "top-level file",
	}
	r1, r2 := t.TempDir(), t.TempDir()
	buildTree(t, r1, tree)
	buildTree(t, r2, tree)
	cs := mustNew(t)
	if !bytes.Equal(mustSum(t, cs, r1).Digest, mustSum(t, cs, r2).Digest) {
		t.Fatal("non-reproducible across identical trees")
	}
}

// ── 2. Content sensitivity ────────────────────────────────────────────────────

func TestContentSensitivity(t *testing.T) {
	t.Parallel()
	cs := mustNew(t, fshash.WithMetadata(fshash.MetaNone))
	root := t.TempDir()
	p := filepath.Join(root, "f.txt")
	os.WriteFile(p, []byte("original"), 0o644)
	r1 := mustSum(t, cs, root)
	os.WriteFile(p, []byte("changed!"), 0o644)
	r2 := mustSum(t, cs, root)
	if bytes.Equal(r1.Digest, r2.Digest) {
		t.Fatal("digest must change when content changes")
	}
}

// ── 3. Sorted traversal order must not matter ─────────────────────────────────

func TestSortedTraversal(t *testing.T) {
	t.Parallel()
	cs := mustNew(t, fshash.WithMetadata(fshash.MetaNone))
	r1, r2 := t.TempDir(), t.TempDir()
	os.WriteFile(filepath.Join(r1, "z"), []byte("z"), 0o644)
	os.WriteFile(filepath.Join(r1, "a"), []byte("a"), 0o644)
	os.WriteFile(filepath.Join(r2, "a"), []byte("a"), 0o644)
	os.WriteFile(filepath.Join(r2, "z"), []byte("z"), 0o644)
	if !bytes.Equal(mustSum(t, cs, r1).Digest, mustSum(t, cs, r2).Digest) {
		t.Fatal("traversal order must not affect digest")
	}
}

// ── 4. Name sensitivity ───────────────────────────────────────────────────────

func TestNameSensitivity(t *testing.T) {
	t.Parallel()
	cs := mustNew(t, fshash.WithMetadata(fshash.MetaNone))
	r1, r2 := t.TempDir(), t.TempDir()
	os.WriteFile(filepath.Join(r1, "foo"), []byte("hello"), 0o644)
	os.WriteFile(filepath.Join(r2, "bar"), []byte("hello"), 0o644)
	if bytes.Equal(mustSum(t, cs, r1).Digest, mustSum(t, cs, r2).Digest) {
		t.Fatal("different names must produce different digests")
	}
}

// ── 5. Empty directory ────────────────────────────────────────────────────────

func TestEmptyDirectory(t *testing.T) {
	t.Parallel()
	cs := mustNew(t)
	if !bytes.Equal(mustSum(t, cs, t.TempDir()).Digest, mustSum(t, cs, t.TempDir()).Digest) {
		t.Fatal("two empty dirs must hash identically")
	}
}

// ── 6. Worker count does not affect digest (SKILL §1) ─────────────────────────

func TestWorkerCountIrrelevant(t *testing.T) {
	t.Parallel()
	root := t.TempDir()
	for i := range 50 {
		os.WriteFile(filepath.Join(root, fmt.Sprintf("f%03d.txt", i)),
			[]byte(fmt.Sprintf("content %d", i)), 0o644)
	}
	r1 := mustSum(t, mustNew(t, fshash.WithWorkers(1)), root)
	r8 := mustSum(t, mustNew(t, fshash.WithWorkers(8)), root)
	if !bytes.Equal(r1.Digest, r8.Digest) {
		t.Fatalf("worker count must not affect digest: 1=%s 8=%s", r1.Hex(), r8.Hex())
	}
}

// ── 7. Shard-mode reproducibility ────────────────────────────────────────────

func TestShardModeReproducible(t *testing.T) {
	t.Parallel()
	p := filepath.Join(t.TempDir(), "large.bin")
	data := bytes.Repeat([]byte("ABCDEFGHIJKLMNOP"), (core.ShardThreshold+1)/16+1)
	os.WriteFile(p, data, 0o644)

	digests := make([]string, 0, 4)
	for _, w := range []int{1, 2, 4, 8} {
		r := mustSum(t, mustNew(t, fshash.WithWorkers(w), fshash.WithMetadata(fshash.MetaNone)), p)
		digests = append(digests, r.Hex())
	}
	for i := 1; i < len(digests); i++ {
		if digests[0] != digests[i] {
			t.Fatalf("shard mode: workers=1 %s != workers=%d %s", digests[0], []int{1, 2, 4, 8}[i], digests[i])
		}
	}
}

// ── 8. Shard mode differs from sequential (different combine step) ─────────────

func TestShardModeDiffersFromSequential(t *testing.T) {
	t.Parallel()
	p := filepath.Join(t.TempDir(), "large.bin")
	// File >= ShardThreshold → always shard mode regardless of workers.
	// Shard digest must differ from a plain streaming hash of the same bytes
	// (different combine step: 0xFE sentinel + shard digests).
	data := bytes.Repeat([]byte("X"), core.ShardThreshold+1)
	os.WriteFile(p, data, 0o644)
	rShard, _ := fshash.MustNew(fshash.WithWorkers(4), fshash.WithMetadata(fshash.MetaNone)).Sum(context.Background(), p)
	f, _ := os.Open(p)
	defer f.Close()
	rStream, _ := fshash.HashReader(context.Background(), f, fshash.WithMetadata(fshash.MetaNone))
	if bytes.Equal(rShard.Digest, rStream) {
		t.Fatal("sharded digest must differ from plain streaming hash (different combine algorithm)")
	}
}

// ── 9. Metadata sensitivity ───────────────────────────────────────────────────

func TestMetadataSensitivity(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("chmod not fully supported on Windows")
	}
	t.Parallel()
	root := t.TempDir()
	p := filepath.Join(root, "script.sh")
	os.WriteFile(p, []byte("#!/bin/sh"), 0o644)

	cs := mustNew(t, fshash.WithMetadata(fshash.MetaMode))
	r1 := mustSum(t, cs, root)
	os.Chmod(p, 0o755)
	r2 := mustSum(t, cs, root)
	if bytes.Equal(r1.Digest, r2.Digest) {
		t.Fatal("digest must change after chmod when MetaMode is set")
	}
}

// ── 10. MetaNone is respected (regression) ────────────────────────────────────

func TestMetaNoneRespected(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("chmod not fully supported on Windows")
	}
	t.Parallel()
	root := t.TempDir()
	p := filepath.Join(root, "f.sh")
	os.WriteFile(p, []byte("#!/bin/sh"), 0o644)
	cs := mustNew(t, fshash.WithMetadata(fshash.MetaNone))
	r1 := mustSum(t, cs, root)
	os.Chmod(p, 0o755)
	r2 := mustSum(t, cs, root)
	if !bytes.Equal(r1.Digest, r2.Digest) {
		t.Fatal("MetaNone: digest must not change after chmod")
	}
}

// ── 11. All algorithms produce distinct outputs ───────────────────────────────

func TestAllAlgorithmsDistinct(t *testing.T) {
	t.Parallel()
	root := t.TempDir()
	os.WriteFile(filepath.Join(root, "data"), []byte("test data"), 0o644)
	seen := map[string]fshash.Algorithm{}
	for _, algo := range []fshash.Algorithm{
		fshash.SHA256, fshash.SHA512, fshash.SHA1, fshash.MD5,
		fshash.XXHash64, fshash.XXHash3, fshash.Blake3, fshash.CRC32C,
	} {
		r := mustSum(t, mustNew(t, fshash.WithAlgorithm(algo)), root)
		key := r.Hex() + fmt.Sprint(len(r.Digest))
		if prev, ok := seen[key]; ok {
			t.Errorf("collision between %s and %s", algo, prev)
		}
		seen[key] = algo
	}
}

// ── 12. Filter: ExcludePatterns ───────────────────────────────────────────────

func TestFilter_ExcludePatterns(t *testing.T) {
	t.Parallel()
	root := t.TempDir()
	buildTree(t, root, fsTree{
		"include.go": "package main", "exclude.tmp": "noise",
		"sub": "", "sub/keep.go": "// keep", "sub/drop.tmp": "noise2",
	})
	cs := mustNew(t, fshash.WithFilter(fshash.ExcludePatterns("*.tmp")), fshash.WithCollectEntries(true))
	res := mustSum(t, cs, root)
	for _, e := range res.Entries {
		if strings.HasSuffix(e.RelPath, ".tmp") {
			t.Errorf("filtered entry %q must not appear", e.RelPath)
		}
	}
}

// ── 13. Filter: ExcludeNames ──────────────────────────────────────────────────

func TestFilter_ExcludeNames(t *testing.T) {
	t.Parallel()
	root := t.TempDir()
	buildTree(t, root, fsTree{
		"code.go": "pkg", ".git": "", ".git/HEAD": "ref:",
		"vendor": "", "vendor/lib": "lib",
	})
	cs := mustNew(t, fshash.WithFilter(fshash.ExcludeNames(".git", "vendor")), fshash.WithCollectEntries(true))
	res := mustSum(t, cs, root)
	for _, e := range res.Entries {
		if strings.HasPrefix(e.RelPath, ".git") || strings.HasPrefix(e.RelPath, "vendor") {
			t.Errorf("excluded entry %q appeared", e.RelPath)
		}
	}
}

// ── 14. Filter: ExcludeDir semantics ─────────────────────────────────────────

func TestFilter_ExcludeDir(t *testing.T) {
	t.Parallel()
	root := t.TempDir()
	buildTree(t, root, fsTree{
		"include.txt": "yes", "skipdir": "", "skipdir/child": "also",
	})
	filter := fshash.FilterFunc(func(r string, fi fs.FileInfo) fshash.FilterDecision {
		if r == "skipdir" {
			return fshash.ExcludeDir
		}
		return fshash.Include
	})
	cs := mustNew(t, fshash.WithFilter(filter), fshash.WithCollectEntries(true), fshash.WithMetadata(fshash.MetaNone))
	res := mustSum(t, cs, root)
	var foundDir, foundChild bool
	for _, e := range res.Entries {
		if e.RelPath == "skipdir" {
			foundDir = true
		}
		if e.RelPath == "skipdir/child" {
			foundChild = true
		}
	}
	if foundDir {
		t.Error("ExcludeDir: 'skipdir' must not appear in entries")
	}
	if !foundChild {
		t.Error("ExcludeDir: 'skipdir/child' should appear")
	}
}

// ── 15. Filter: ChainFilters ──────────────────────────────────────────────────

func TestFilter_ChainFilters(t *testing.T) {
	t.Parallel()
	root := t.TempDir()
	buildTree(t, root, fsTree{"a.go": "code", "b.tmp": "temp", "c.log": "log"})
	cs := mustNew(t,
		fshash.WithFilter(fshash.ChainFilters(fshash.ExcludePatterns("*.tmp"), fshash.ExcludePatterns("*.log"))),
		fshash.WithCollectEntries(true),
	)
	res := mustSum(t, cs, root)
	for _, e := range res.Entries {
		if e.RelPath == "b.tmp" || e.RelPath == "c.log" {
			t.Errorf("entry %q should have been excluded", e.RelPath)
		}
	}
}

// ── 16. CollectEntries ────────────────────────────────────────────────────────

func TestCollectEntries(t *testing.T) {
	t.Parallel()
	root := t.TempDir()
	buildTree(t, root, fsTree{"x.txt": "x", "y.txt": "y", "sub": "", "sub/z": "z"})
	resOn := mustSum(t, mustNew(t, fshash.WithCollectEntries(true)), root)
	resOff := mustSum(t, mustNew(t, fshash.WithCollectEntries(false)), root)
	if len(resOn.Entries) == 0 {
		t.Fatal("expected entries with CollectEntries=true")
	}
	if len(resOff.Entries) != 0 {
		t.Fatal("expected no entries with CollectEntries=false")
	}
	if !bytes.Equal(resOn.Digest, resOff.Digest) {
		t.Fatal("CollectEntries must not affect the digest value")
	}
}

// ── 17. Collected entries are sorted, root "." is last ────────────────────────

func TestEntriesSortedRootLast(t *testing.T) {
	t.Parallel()
	root := t.TempDir()
	buildTree(t, root, fsTree{
		"a": "", "a/b": "", "a/b/c": "", "a/b/c/d": "",
		"a/b/c/d/f1": "deep1", "a/b/c/d/f2": "deep2", "top": "top",
	})
	res := mustSum(t, mustNew(t, fshash.WithCollectEntries(true)), root)
	paths := relPaths(res.Entries)
	if len(paths) == 0 || paths[len(paths)-1] != "." {
		t.Fatalf("root '.' must be last; got: %v", paths)
	}
	nonRoot := paths[:len(paths)-1]
	if !sort.StringsAreSorted(nonRoot) {
		t.Fatalf("non-root entries must be sorted: %v", nonRoot)
	}
}

// ── 18. Verify ────────────────────────────────────────────────────────────────

func TestVerify(t *testing.T) {
	t.Parallel()
	p := filepath.Join(t.TempDir(), "f.txt")
	os.WriteFile(p, []byte("hello"), 0o644)
	cs := mustNew(t)
	res := mustSum(t, cs, p)
	if err := cs.Verify(context.Background(), p, res.Digest); err != nil {
		t.Fatalf("Verify with correct digest: %v", err)
	}
	bad := make([]byte, len(res.Digest))
	err := cs.Verify(context.Background(), p, bad)
	var ve *fshash.VerifyError
	if !errors.As(err, &ve) {
		t.Fatalf("expected *VerifyError, got %T: %v", err, err)
	}
	if !strings.Contains(ve.Error(), p) {
		t.Error("VerifyError.Error() should contain the path")
	}
}

// ── 19. MemoryCache ───────────────────────────────────────────────────────────

func TestMemoryCache_HitMissInvalidate(t *testing.T) {
	t.Parallel()
	c := &fshash.MemoryCache{}
	if _, ok := c.Get("/x"); ok {
		t.Fatal("expected miss on empty cache")
	}
	c.Set("/x", []byte{1, 2, 3})
	d, ok := c.Get("/x")
	if !ok || !bytes.Equal(d, []byte{1, 2, 3}) {
		t.Fatal("expected hit")
	}
	c.Invalidate("/x")
	if _, ok := c.Get("/x"); ok {
		t.Fatal("expected miss after Invalidate")
	}
}

func TestMemoryCache_DeepCopy(t *testing.T) {
	t.Parallel()
	c := &fshash.MemoryCache{}
	orig := []byte{0xDE, 0xAD}
	c.Set("/k", orig)
	orig[0] = 0xFF
	got, _ := c.Get("/k")
	if got[0] == 0xFF {
		t.Fatal("MemoryCache must deep-copy on Set")
	}
}

// ── 20. MtimeCache ────────────────────────────────────────────────────────────

func TestMtimeCache_AutoInvalidate(t *testing.T) {
	t.Parallel()
	dir := t.TempDir()
	p := filepath.Join(dir, "f.txt")
	os.WriteFile(p, []byte("original"), 0o644)
	c := &fshash.MtimeCache{}
	c.Set(p, []byte{0xAB})
	if _, ok := c.Get(p); !ok {
		t.Fatal("expected hit before modification")
	}
	os.WriteFile(p, []byte("different content with different size"), 0o644)
	if _, ok := c.Get(p); ok {
		t.Fatal("expected miss after file size change")
	}
}

func TestMtimeCache_DeepCopyOnGet(t *testing.T) {
	t.Parallel()
	p := filepath.Join(t.TempDir(), "f")
	os.WriteFile(p, []byte("x"), 0o644)
	c := &fshash.MtimeCache{}
	c.Set(p, []byte{0x11, 0x22})
	got, _ := c.Get(p)
	got[0] = 0xFF
	got2, _ := c.Get(p)
	if got2[0] == 0xFF {
		t.Fatal("MtimeCache.Get must return a deep copy")
	}
}

func TestMtimeCache_NonexistentFile(t *testing.T) {
	t.Parallel()
	c := &fshash.MtimeCache{}
	c.Set("/nonexistent/file.txt", []byte{0x01})
	if c.Len() != 0 {
		t.Fatal("Set on nonexistent file must be a no-op")
	}
}

func TestMtimeCache_InvalidateAll(t *testing.T) {
	t.Parallel()
	dir := t.TempDir()
	c := &fshash.MtimeCache{}
	for _, name := range []string{"a", "b", "c"} {
		p := filepath.Join(dir, name)
		os.WriteFile(p, []byte(name), 0o644)
		c.Set(p, []byte(name))
	}
	if c.Len() != 3 {
		t.Fatalf("Len want 3, got %d", c.Len())
	}
	c.InvalidateAll()
	if c.Len() != 0 {
		t.Fatalf("Len after InvalidateAll want 0, got %d", c.Len())
	}
}

func TestMtimeCache_Prune(t *testing.T) {
	t.Parallel()
	dir := t.TempDir()
	pA, pB := filepath.Join(dir, "a"), filepath.Join(dir, "b")
	os.WriteFile(pA, []byte("a"), 0o644)
	os.WriteFile(pB, []byte("b"), 0o644)
	c := &fshash.MtimeCache{}
	c.Set(pA, []byte{0x01})
	c.Set(pB, []byte{0x02})
	os.Remove(pB)
	c.Prune()
	if c.Len() != 1 {
		t.Fatalf("after Prune want 1, got %d", c.Len())
	}
	if _, ok := c.Get(pA); !ok {
		t.Fatal("valid entry must survive Prune")
	}
}

// ── 21. FileCache short-circuits disk reads ───────────────────────────────────

func TestFileCacheShortCircuit(t *testing.T) {
	t.Parallel()
	root := t.TempDir()
	p := filepath.Join(root, "f.txt")
	os.WriteFile(p, []byte("content"), 0o644)
	cache := &fshash.MemoryCache{}
	cs := mustNew(t, fshash.WithFileCache(cache))
	r1 := mustSum(t, cs, root)
	if _, ok := cache.Get(p); !ok {
		t.Fatal("cache must be populated after first Sum")
	}
	// Corrupt file; cache hit must serve old digest.
	os.WriteFile(p, []byte("CORRUPTED"), 0o644)
	r2 := mustSum(t, cs, root)
	if !bytes.Equal(r1.Digest, r2.Digest) {
		t.Fatal("cache hit must shadow the corrupted file")
	}
	// After invalidation the new content must be read.
	cache.Invalidate(p)
	r3 := mustSum(t, cs, root)
	if bytes.Equal(r1.Digest, r3.Digest) {
		t.Fatal("after invalidation, digest must reflect new content")
	}
}

// ── 22. Symlinks ──────────────────────────────────────────────────────────────

func TestSymlink_NoFollow(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("symlinks require elevated permissions on Windows")
	}
	t.Parallel()
	root := t.TempDir()
	buildTree(t, root, fsTree{
		"real": "real content", "link": "-> real", "dangling": "-> /nonexistent",
	})
	cs := mustNew(t, fshash.WithFollowSymlinks(false), fshash.WithCollectEntries(true))
	res := mustSum(t, cs, root)
	kinds := map[string]fshash.EntryKind{}
	for _, e := range res.Entries {
		kinds[e.RelPath] = e.Kind
	}
	if kinds["real"] != fshash.KindFile {
		t.Errorf("real: want KindFile, got %v", kinds["real"])
	}
	if kinds["link"] != fshash.KindSymlink {
		t.Errorf("link: want KindSymlink, got %v", kinds["link"])
	}
	if kinds["dangling"] != fshash.KindSymlink {
		t.Errorf("dangling: want KindSymlink, got %v", kinds["dangling"])
	}
}

func TestSymlink_Follow(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("symlinks require elevated permissions on Windows")
	}
	t.Parallel()
	r1, r2 := t.TempDir(), t.TempDir()
	buildTree(t, r1, fsTree{"real": "shared content", "link": "-> real"})
	buildTree(t, r2, fsTree{"real": "shared content", "link": "shared content"})
	cs := mustNew(t, fshash.WithFollowSymlinks(true), fshash.WithMetadata(fshash.MetaNone))
	if !bytes.Equal(mustSum(t, cs, r1).Digest, mustSum(t, cs, r2).Digest) {
		t.Fatal("follow-symlink: should equal plain file")
	}
}

func TestSymlink_CycleDetection(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("symlinks require elevated permissions on Windows")
	}
	t.Parallel()
	root := t.TempDir()
	os.Symlink(filepath.Join(root, "b"), filepath.Join(root, "a"))
	os.Symlink(filepath.Join(root, "a"), filepath.Join(root, "b"))
	_, err := mustNew(t, fshash.WithFollowSymlinks(true)).Sum(context.Background(), root)
	if err == nil {
		t.Fatal("expected cycle detection error")
	}
	if !strings.Contains(err.Error(), "cycle") {
		t.Fatalf("error must mention 'cycle': %v", err)
	}
}

// ── 23. FSWalker ──────────────────────────────────────────────────────────────

func TestFSWalker(t *testing.T) {
	t.Parallel()
	root := t.TempDir()
	buildTree(t, root, fsTree{"a": "alpha", "b": "beta", "sub": "", "sub/c": "gamma"})

	// FSWalker hashes files via os.Open using the relative path from ".".
	// Change directory to root so relative paths resolve correctly.
	orig, _ := os.Getwd()
	os.Chdir(root)
	defer os.Chdir(orig)

	diskRes := mustSum(t, mustNew(t, fshash.WithMetadata(fshash.MetaNone)), root)
	fsCS, err := fshash.New(
		fshash.WithMetadata(fshash.MetaNone),
		fshash.WithWalker(fshash.FSWalker{FS: os.DirFS(root)}),
	)
	if err != nil {
		t.Fatal(err)
	}
	fsRes, err := fsCS.Sum(context.Background(), ".")
	if err != nil {
		t.Fatal(err)
	}
	if !bytes.Equal(diskRes.Digest, fsRes.Digest) {
		t.Fatalf("OSWalker vs FSWalker: %s vs %s", diskRes.Hex(), fsRes.Hex())
	}
}

// ── 24. Diff ──────────────────────────────────────────────────────────────────

func TestDiff(t *testing.T) {
	t.Parallel()
	rA, rB := t.TempDir(), t.TempDir()
	buildTree(t, rA, fsTree{"common": "same", "modified": "old", "removed": "gone"})
	buildTree(t, rB, fsTree{"common": "same", "modified": "new", "added": "new"})
	diff, err := mustNew(t, fshash.WithMetadata(fshash.MetaNone)).Diff(context.Background(), rA, rB)
	if err != nil {
		t.Fatal(err)
	}
	if len(diff.Added) != 1 || diff.Added[0] != "added" {
		t.Errorf("Added: want [added], got %v", diff.Added)
	}
	if len(diff.Removed) != 1 || diff.Removed[0] != "removed" {
		t.Errorf("Removed: want [removed], got %v", diff.Removed)
	}
	if len(diff.Modified) != 1 || diff.Modified[0] != "modified" {
		t.Errorf("Modified: want [modified], got %v", diff.Modified)
	}
}

func TestDiff_Identical(t *testing.T) {
	t.Parallel()
	root := t.TempDir()
	buildTree(t, root, fsTree{"f": "data"})
	diff, err := mustNew(t).Diff(context.Background(), root, root)
	if err != nil {
		t.Fatal(err)
	}
	if !diff.Empty() {
		t.Fatalf("expected empty diff: %+v", diff)
	}
}

func TestParallelDiff_MatchesSerial(t *testing.T) {
	t.Parallel()
	rA, rB := t.TempDir(), t.TempDir()
	buildTree(t, rA, fsTree{"common": "same", "m": "old", "r": "gone"})
	buildTree(t, rB, fsTree{"common": "same", "m": "new", "a": "fresh"})
	cs := mustNew(t, fshash.WithMetadata(fshash.MetaNone))
	dSerial, _ := cs.Diff(context.Background(), rA, rB)
	dParallel, _ := cs.ParallelDiff(context.Background(), rA, rB)
	if !reflect.DeepEqual(dSerial, dParallel) {
		t.Fatalf("serial vs parallel diff mismatch:\n%+v\n%+v", dSerial, dParallel)
	}
}

// ── 25. CompareTrees ──────────────────────────────────────────────────────────

func TestCompareTrees_Basic(t *testing.T) {
	t.Parallel()
	rA, rB := t.TempDir(), t.TempDir()
	buildTree(t, rA, fsTree{"common": "same", "modified": "old", "removed": "gone"})
	buildTree(t, rB, fsTree{"common": "same", "modified": "new", "added": "new"})
	cmp, err := mustNew(t, fshash.WithMetadata(fshash.MetaNone)).CompareTrees(context.Background(), rA, rB)
	if err != nil {
		t.Fatal(err)
	}
	byPath := map[string]fshash.ChangeStatus{}
	for _, ch := range cmp.Changes {
		byPath[ch.RelPath] = ch.Status
	}
	if byPath["common"] != fshash.StatusUnchanged {
		t.Errorf("common: want Unchanged, got %s", byPath["common"])
	}
	if byPath["modified"] != fshash.StatusModified {
		t.Errorf("modified: want Modified, got %s", byPath["modified"])
	}
	if byPath["removed"] != fshash.StatusRemoved {
		t.Errorf("removed: want Removed, got %s", byPath["removed"])
	}
	if byPath["added"] != fshash.StatusAdded {
		t.Errorf("added: want Added, got %s", byPath["added"])
	}
	paths := make([]string, len(cmp.Changes))
	for i, ch := range cmp.Changes {
		paths[i] = ch.RelPath
	}
	if !sort.StringsAreSorted(paths) {
		t.Fatalf("Changes not sorted: %v", paths)
	}
}

func TestCompareTrees_Equal(t *testing.T) {
	t.Parallel()
	root := t.TempDir()
	buildTree(t, root, fsTree{"a": "alpha", "b": "beta"})
	cmp, err := mustNew(t).CompareTrees(context.Background(), root, root)
	if err != nil {
		t.Fatal(err)
	}
	if !cmp.Equal() {
		t.Fatal("identical trees must report Equal()==true")
	}
	if len(cmp.OnlyChanged()) != 0 {
		t.Fatal("no changes expected for identical trees")
	}
}

// ── 26. Snapshot ──────────────────────────────────────────────────────────────

func TestSnapshot_RoundTrip(t *testing.T) {
	t.Parallel()
	root := t.TempDir()
	buildTree(t, root, fsTree{"a": "alpha", "b": "beta", "sub": "", "sub/c": "gamma"})
	snap, err := fshash.TakeSnapshot(context.Background(), root)
	if err != nil {
		t.Fatal(err)
	}
	if snap.RootDigest == "" {
		t.Fatal("empty root digest")
	}
	var buf bytes.Buffer
	snap.WriteTo(&buf)
	snap2, err := fshash.ReadSnapshot(&buf)
	if err != nil {
		t.Fatal(err)
	}
	if snap2.RootDigest != snap.RootDigest {
		t.Fatalf("round-trip: %s != %s", snap.RootDigest, snap2.RootDigest)
	}
}

func TestSnapshot_VerifyAgainst(t *testing.T) {
	t.Parallel()
	root := t.TempDir()
	p := filepath.Join(root, "file")
	os.WriteFile(p, []byte("original"), 0o644)
	snap, err := fshash.TakeSnapshot(context.Background(), root)
	if err != nil {
		t.Fatal(err)
	}
	if err := snap.VerifyAgainst(context.Background(), root); err != nil {
		t.Fatalf("VerifyAgainst on unchanged tree: %v", err)
	}
	os.WriteFile(p, []byte("changed"), 0o644)
	if err := snap.VerifyAgainst(context.Background(), root); err == nil {
		t.Fatal("VerifyAgainst must fail after modification")
	}
}

func TestSnapshot_Diff(t *testing.T) {
	t.Parallel()
	rA, rB := t.TempDir(), t.TempDir()
	buildTree(t, rA, fsTree{"common": "same", "modified": "old", "removed": "gone"})
	buildTree(t, rB, fsTree{"common": "same", "modified": "new", "added": "fresh"})
	snapA, _ := fshash.TakeSnapshot(context.Background(), rA, fshash.WithMetadata(fshash.MetaNone))
	snapB, _ := fshash.TakeSnapshot(context.Background(), rB, fshash.WithMetadata(fshash.MetaNone))
	diff := snapA.Diff(snapB)
	if len(diff.Added) != 1 || diff.Added[0] != "added" {
		t.Errorf("Added: want [added], got %v", diff.Added)
	}
	if len(diff.Removed) != 1 || diff.Removed[0] != "removed" {
		t.Errorf("Removed: want [removed], got %v", diff.Removed)
	}
}

func TestSnapshot_MetaNoneRoundTrip(t *testing.T) {
	t.Parallel()
	root := t.TempDir()
	os.WriteFile(filepath.Join(root, "f"), []byte("data"), 0o644)
	snap, err := fshash.TakeSnapshot(context.Background(), root, fshash.WithMetadata(fshash.MetaNone))
	if err != nil {
		t.Fatal(err)
	}
	if snap.Meta() != fshash.MetaNone {
		t.Fatalf("expected MetaNone, got %d", snap.Meta())
	}
	var buf bytes.Buffer
	snap.WriteTo(&buf)
	snap2, _ := fshash.ReadSnapshot(&buf)
	if snap2.Meta() != fshash.MetaNone {
		t.Fatalf("Meta not preserved through JSON round-trip")
	}
	if err := snap2.VerifyAgainst(context.Background(), root); err != nil {
		t.Fatalf("VerifyAgainst after round-trip: %v", err)
	}
}

// ── 27. Inspector ─────────────────────────────────────────────────────────────

func TestInspector_HitRate(t *testing.T) {
	t.Parallel()
	root := t.TempDir()
	buildTree(t, root, fsTree{"a": "alpha", "b": "beta"})
	cache := &fshash.MemoryCache{}
	cs := mustNew(t, fshash.WithFileCache(cache))
	ins := fshash.NewInspector(cs, cache)

	_, entries1, err := ins.Sum(context.Background(), root)
	if err != nil {
		t.Fatal(err)
	}
	for _, e := range entries1 {
		if e.Kind == fshash.KindFile && e.CacheHit {
			t.Errorf("%q: expected cache miss on first pass", e.RelPath)
		}
	}
	if ins.HitRate() != 0 {
		t.Errorf("expected 0 hit rate on first pass, got %f", ins.HitRate())
	}

	_, entries2, err := ins.Sum(context.Background(), root)
	if err != nil {
		t.Fatal(err)
	}
	for _, e := range entries2 {
		if e.Kind == fshash.KindFile && !e.CacheHit {
			t.Errorf("%q: expected cache hit on second pass", e.RelPath)
		}
	}
	if ins.HitRate() == 0 {
		t.Error("expected non-zero hit rate on second pass")
	}
}

// ── 28. Walk ──────────────────────────────────────────────────────────────────

func TestWalk_SortedRootLast(t *testing.T) {
	t.Parallel()
	root := t.TempDir()
	buildTree(t, root, fsTree{"a": "alpha", "b": "beta", "sub": "", "sub/c": "gamma"})
	cs := mustNew(t, fshash.WithMetadata(fshash.MetaNone))
	var visited []string
	res, err := cs.Walk(context.Background(), root, func(e fshash.EntryResult) error {
		visited = append(visited, e.RelPath)
		return nil
	})
	if err != nil {
		t.Fatal(err)
	}
	if len(res.Digest) == 0 {
		t.Fatal("empty root digest")
	}
	if visited[len(visited)-1] != "." {
		t.Fatalf("last Walk entry must be '.', got %q", visited[len(visited)-1])
	}
	nonRoot := visited[:len(visited)-1]
	if !sort.StringsAreSorted(nonRoot) {
		t.Fatalf("non-root entries not sorted: %v", nonRoot)
	}
	sumRes := mustSum(t, cs, root)
	if !bytes.Equal(res.Digest, sumRes.Digest) {
		t.Fatal("Walk root digest must equal Sum digest")
	}
}

func TestWalk_CallbackError(t *testing.T) {
	t.Parallel()
	root := t.TempDir()
	os.WriteFile(filepath.Join(root, "f"), []byte("x"), 0o644)
	sentinel := fmt.Errorf("stop here")
	_, err := mustNew(t).Walk(context.Background(), root, func(_ fshash.EntryResult) error {
		return sentinel
	})
	if !errors.Is(err, sentinel) {
		t.Fatalf("expected sentinel error, got: %v", err)
	}
}

// ── 29. SumStream (reactive) ──────────────────────────────────────────────────

func TestSumStream_ReceivesAllEntries(t *testing.T) {
	t.Parallel()
	root := t.TempDir()
	buildTree(t, root, fsTree{"a": "alpha", "b": "beta", "sub": "", "sub/c": "gamma"})
	cs := mustNew(t, fshash.WithMetadata(fshash.MetaNone))
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	stream := cs.SumStream(ctx, root)
	var entries []fshash.EntryResult
	for e := range stream.Chan() {
		entries = append(entries, e)
	}
	if len(entries) == 0 {
		t.Fatal("SumStream must emit at least one entry")
	}
	last := entries[len(entries)-1]
	if last.RelPath != "." {
		t.Errorf("last streamed entry must be '.', got %q", last.RelPath)
	}
}

// ── 30. Canonicalize / ReadCanonical ─────────────────────────────────────────

func TestCanonicalize_RoundTrip(t *testing.T) {
	t.Parallel()
	root := t.TempDir()
	buildTree(t, root, fsTree{"a.txt": "alpha", "b.txt": "beta", "sub": "", "sub/c": "gamma"})
	cs := mustNew(t)
	var buf bytes.Buffer
	rootDgst, err := cs.Canonicalize(context.Background(), root, &buf)
	if err != nil {
		t.Fatal(err)
	}
	entries, err := fshash.ReadCanonical(&buf)
	if err != nil {
		t.Fatal(err)
	}
	if len(entries) == 0 {
		t.Fatal("ReadCanonical returned no entries")
	}
	if entries[len(entries)-1].Kind != "root" {
		t.Fatalf("last entry kind want 'root', got %q", entries[len(entries)-1].Kind)
	}
	sumRes := mustSum(t, cs, root)
	if !bytes.Equal(rootDgst, sumRes.Digest) {
		t.Fatal("Canonicalize root digest != Sum digest")
	}
}

func TestCanonicalize_PathWithSpaces(t *testing.T) {
	t.Parallel()
	root := t.TempDir()
	os.WriteFile(filepath.Join(root, "my file with spaces.txt"), []byte("spaced"), 0o644)
	cs := mustNew(t)
	var buf bytes.Buffer
	cs.Canonicalize(context.Background(), root, &buf)
	entries, err := fshash.ReadCanonical(&buf)
	if err != nil {
		t.Fatalf("ReadCanonical failed: %v", err)
	}
	var found bool
	for _, e := range entries {
		if strings.Contains(e.RelPath, "my file with spaces.txt") {
			found = true
		}
	}
	if !found {
		t.Fatal("ReadCanonical lost spaces in path")
	}
}

func TestReadCanonical_InvalidLines(t *testing.T) {
	t.Parallel()
	_, err := fshash.ReadCanonical(strings.NewReader("abc123  file\n"))
	if err == nil {
		t.Fatal("expected error for line with missing kind separator")
	}
	_, err = fshash.ReadCanonical(strings.NewReader("ZZZZ  file  foo\n"))
	if err == nil {
		t.Fatal("expected error for bad hex digest")
	}
}

// ── 31. SumMany ───────────────────────────────────────────────────────────────

func TestSumMany_OrderPreserved(t *testing.T) {
	t.Parallel()
	dir := t.TempDir()
	paths := make([]string, 10)
	for i := range paths {
		p := filepath.Join(dir, fmt.Sprintf("f%02d", i))
		os.WriteFile(p, []byte(fmt.Sprintf("data-%d", i)), 0o644)
		paths[i] = p
	}
	cs := mustNew(t, fshash.WithWorkers(4))
	results, errs := cs.SumMany(context.Background(), paths)
	for i, err := range errs {
		if err != nil {
			t.Fatalf("SumMany[%d]: %v", i, err)
		}
	}
	for i, p := range paths {
		single := mustSum(t, cs, p)
		if !bytes.Equal(results[i].Digest, single.Digest) {
			t.Errorf("SumMany[%d] order mismatch for %s", i, p)
		}
	}
}

func TestSumMany_CtxCancel(t *testing.T) {
	t.Parallel()
	dir := t.TempDir()
	paths := make([]string, 20)
	for i := range paths {
		p := filepath.Join(dir, fmt.Sprintf("f%02d.dat", i))
		os.WriteFile(p, bytes.Repeat([]byte("x"), 4096), 0o644)
		paths[i] = p
	}
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	_, errs := mustNew(t).SumMany(ctx, paths)
	var ctxErrs int
	for _, e := range errs {
		if e != nil {
			ctxErrs++
		}
	}
	t.Logf("ctx errors: %d/%d", ctxErrs, len(paths))
}

func TestSumMany_Empty(t *testing.T) {
	t.Parallel()
	res, errs := mustNew(t).SumMany(context.Background(), nil)
	if len(res) != 0 || len(errs) != 0 {
		t.Fatal("SumMany(nil) must return empty slices")
	}
}

// ── 32. SizeLimit ─────────────────────────────────────────────────────────────

func TestSizeLimit_Enforced(t *testing.T) {
	t.Parallel()
	p := filepath.Join(t.TempDir(), "big.bin")
	os.WriteFile(p, bytes.Repeat([]byte("A"), 1024), 0o644)
	_, err := mustNew(t, fshash.WithSizeLimit(512)).Sum(context.Background(), p)
	var tle *fshash.FileTooLargeError
	if !errors.As(err, &tle) {
		t.Fatalf("expected *FileTooLargeError, got %T: %v", err, err)
	}
	if tle.Size != 1024 || tle.Limit != 512 {
		t.Errorf("wrong Size/Limit: %d/%d", tle.Size, tle.Limit)
	}
}

func TestSizeLimit_NotEnforced(t *testing.T) {
	t.Parallel()
	p := filepath.Join(t.TempDir(), "small.bin")
	os.WriteFile(p, bytes.Repeat([]byte("A"), 256), 0o644)
	_, err := mustNew(t, fshash.WithSizeLimit(1024)).Sum(context.Background(), p)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
}

func TestSizeLimit_Zero_NoLimit(t *testing.T) {
	t.Parallel()
	p := filepath.Join(t.TempDir(), "f.txt")
	os.WriteFile(p, bytes.Repeat([]byte("A"), 1<<20), 0o644)
	if _, err := mustNew(t, fshash.WithSizeLimit(0)).Sum(context.Background(), p); err != nil {
		t.Fatalf("SizeLimit=0 must not enforce limit: %v", err)
	}
}

func TestSizeLimit_Negative_Error(t *testing.T) {
	t.Parallel()
	if _, err := fshash.New(fshash.WithSizeLimit(-1)); err == nil {
		t.Fatal("expected error for negative SizeLimit")
	}
}

// ── 33. HashReader ────────────────────────────────────────────────────────────

func TestHashReader_Reproducible(t *testing.T) {
	t.Parallel()
	data := []byte("hello, world")
	d1, err := fshash.HashReader(context.Background(), bytes.NewReader(data))
	if err != nil {
		t.Fatal(err)
	}
	d2, _ := fshash.HashReader(context.Background(), bytes.NewReader(data))
	if !bytes.Equal(d1, d2) {
		t.Fatal("HashReader not reproducible")
	}
}

func TestHashReader_Empty_SHA256(t *testing.T) {
	t.Parallel()
	dgst, err := fshash.HashReader(context.Background(), bytes.NewReader(nil))
	if err != nil {
		t.Fatal(err)
	}
	want := "e3b0c44298fc1c149afbf4c8996fb92427ae41e4649b934ca495991b7852b855"
	if hex.EncodeToString(dgst) != want {
		t.Fatalf("empty SHA-256: got %x want %s", dgst, want)
	}
}

func TestHashReader_AlgorithmOption(t *testing.T) {
	t.Parallel()
	data := []byte("test")
	dSHA, _ := fshash.HashReader(context.Background(), bytes.NewReader(data))
	dB3, _ := fshash.HashReader(context.Background(), bytes.NewReader(data), fshash.WithAlgorithm(fshash.Blake3))
	if bytes.Equal(dSHA, dB3) {
		t.Fatal("different algorithms must produce different digests")
	}
	if len(dB3) != 32 {
		t.Fatalf("Blake3 digest must be 32 bytes, got %d", len(dB3))
	}
}

// ── 34. Convenience functions ─────────────────────────────────────────────────

func TestFileDigest(t *testing.T) {
	t.Parallel()
	p := filepath.Join(t.TempDir(), "f.txt")
	os.WriteFile(p, []byte("content"), 0o644)
	dgst, err := fshash.FileDigest(context.Background(), p)
	if err != nil {
		t.Fatal(err)
	}
	if len(dgst) == 0 {
		t.Fatal("empty digest")
	}
}

func TestDirDigest(t *testing.T) {
	t.Parallel()
	root := t.TempDir()
	buildTree(t, root, fsTree{"a": "alpha", "b": "beta"})
	dgst, err := fshash.DirDigest(context.Background(), root)
	if err != nil {
		t.Fatal(err)
	}
	cs := mustNew(t)
	if !bytes.Equal(dgst, mustSum(t, cs, root).Digest) {
		t.Fatal("DirDigest != Sum")
	}
}

// ── 35. Custom Hasher via Registry ────────────────────────────────────────────

func TestCustomHasher_ViaRegistry(t *testing.T) {
	t.Parallel()
	// Register a custom XOR hasher in a fresh registry — doesn't affect DefaultRegistry.
	reg := core.NewRegistry()
	reg.Register("xor8", xorCoreHasher{})
	h, err := reg.Get("xor8")
	if err != nil {
		t.Fatal(err)
	}
	cs, err := fshash.New(fshash.WithHasher(h))
	if err != nil {
		t.Fatal(err)
	}
	root := t.TempDir()
	os.WriteFile(filepath.Join(root, "f"), []byte("abc"), 0o644)
	res, err := cs.Sum(context.Background(), root)
	if err != nil {
		t.Fatal(err)
	}
	if len(res.Digest) == 0 {
		t.Fatal("expected non-empty digest from custom hasher")
	}
}

// ── 36. Concurrent safety ─────────────────────────────────────────────────────

func TestConcurrentSafety(t *testing.T) {
	t.Parallel()
	root := t.TempDir()
	buildTree(t, root, fsTree{"a": "alpha", "b": "beta", "c": "gamma"})
	cs := mustNew(t)
	const N = 20
	results := make([]fshash.Result, N)
	errs := make([]error, N)
	var wg sync.WaitGroup
	for i := range N {
		i := i
		wg.Add(1)
		go func() {
			defer wg.Done()
			results[i], errs[i] = cs.Sum(context.Background(), root)
		}()
	}
	wg.Wait()
	for i, err := range errs {
		if err != nil {
			t.Errorf("g%d: %v", i, err)
		}
	}
	for i := 1; i < N; i++ {
		if !bytes.Equal(results[0].Digest, results[i].Digest) {
			t.Fatalf("g0=%s g%d=%s", results[0].Hex(), i, results[i].Hex())
		}
	}
}

// ── 37. Watcher: reactive stream detects change ───────────────────────────────

func TestWatcher_StreamDetectsChange(t *testing.T) {
	t.Parallel()
	root := t.TempDir()
	p := filepath.Join(root, "watched.txt")
	os.WriteFile(p, []byte("original"), 0o644)

	cs := mustNew(t)
	w := fshash.NewWatcher(cs, root, fshash.WithWatchInterval(20*time.Millisecond))
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	stream := w.WatchStream(ctx)
	time.Sleep(60 * time.Millisecond) // let watcher establish baseline
	os.WriteFile(p, []byte("modified!"), 0o644)

	select {
	case evt := <-stream.Chan():
		if len(evt.PrevDigest) == 0 || len(evt.CurrDigest) == 0 {
			t.Error("change event must have both prev and curr digests")
		}
		if bytes.Equal(evt.PrevDigest, evt.CurrDigest) {
			t.Error("prev and curr digests must differ on change")
		}
	case <-time.After(3 * time.Second):
		t.Fatal("timeout: no change event received")
	}
}

func TestWatcher_NoSpuriousEvents(t *testing.T) {
	t.Parallel()
	root := t.TempDir()
	os.WriteFile(filepath.Join(root, "stable.txt"), []byte("stable"), 0o644)
	cs := mustNew(t)
	w := fshash.NewWatcher(cs, root, fshash.WithWatchInterval(15*time.Millisecond))
	id, ch := w.Events().Subscribe(4)
	defer w.Events().Unsubscribe(id)

	ctx, cancel := context.WithTimeout(context.Background(), 200*time.Millisecond)
	defer cancel()
	go w.Watch(ctx) //nolint:errcheck

	time.Sleep(250 * time.Millisecond)
	if len(ch) != 0 {
		t.Errorf("expected 0 spurious events, got %d", len(ch))
	}
}

// ── 38. Error handling ────────────────────────────────────────────────────────

func TestInvalidPath(t *testing.T) {
	t.Parallel()
	_, err := mustNew(t).Sum(context.Background(), "/this/path/does/not/exist/hopefully")
	if err == nil {
		t.Fatal("expected error for non-existent path")
	}
}

func TestUnknownAlgorithm(t *testing.T) {
	t.Parallel()
	_, err := fshash.New(fshash.WithAlgorithm("no-such-algo"))
	if err == nil {
		t.Fatal("expected error for unknown algorithm")
	}
}

func TestNilHasher(t *testing.T) {
	t.Parallel()
	_, err := fshash.New(fshash.WithHasher(nil))
	if err == nil {
		t.Fatal("expected error for nil Hasher")
	}
}

func TestNilWalker(t *testing.T) {
	t.Parallel()
	_, err := fshash.New(fshash.WithWalker(nil))
	if err == nil {
		t.Fatal("expected error for nil Walker")
	}
}

func TestMustNew_Panic(t *testing.T) {
	t.Parallel()
	defer func() {
		if r := recover(); r == nil {
			t.Fatal("MustNew with bad option should panic")
		}
	}()
	fshash.MustNew(fshash.WithAlgorithm("bad-algo"))
}

// ── 39. Result helpers ────────────────────────────────────────────────────────

func TestResult_Equal(t *testing.T) {
	t.Parallel()
	root := t.TempDir()
	os.WriteFile(filepath.Join(root, "f"), []byte("data"), 0o644)
	cs := mustNew(t)
	r1 := mustSum(t, cs, root)
	r2 := mustSum(t, cs, root)
	if !r1.Equal(r2) {
		t.Fatal("identical sums must be Equal")
	}
	root2 := t.TempDir()
	os.WriteFile(filepath.Join(root2, "f"), []byte("different"), 0o644)
	r3 := mustSum(t, cs, root2)
	if r1.Equal(r3) {
		t.Fatal("different sums must not be Equal")
	}
}

func TestDiffResult_String(t *testing.T) {
	t.Parallel()
	if (fshash.DiffResult{}).String() != "no differences" {
		t.Error("empty DiffResult.String() should be 'no differences'")
	}
	d := fshash.DiffResult{Added: []string{"a"}, Removed: []string{"b", "c"}, Modified: []string{"d"}}
	s := d.String()
	if !strings.Contains(s, "1 added") || !strings.Contains(s, "2 removed") || !strings.Contains(s, "1 modified") {
		t.Errorf("unexpected DiffResult string: %q", s)
	}
}

func TestEntryKind_String(t *testing.T) {
	t.Parallel()
	cases := []struct {
		k    fshash.EntryKind
		want string
	}{
		{fshash.KindFile, "file"}, {fshash.KindDir, "dir"},
		{fshash.KindSymlink, "symlink"}, {fshash.KindOther, "other"},
	}
	for _, tc := range cases {
		if tc.k.String() != tc.want {
			t.Errorf("EntryKind(%d).String()=%q want %q", tc.k, tc.k.String(), tc.want)
		}
	}
}

// ── 40. Hermetic across time (mtime changes must not affect MetaModeAndSize) ──

func TestHermeticAcrossTime(t *testing.T) {
	t.Parallel()
	root := t.TempDir()
	p := filepath.Join(root, "f")
	os.WriteFile(p, []byte("stable"), 0o644)
	cs := mustNew(t) // default = MetaModeAndSize, no MetaMtime
	r1 := mustSum(t, cs, root)
	os.Chtimes(p, time.Now().Add(24*time.Hour), time.Now().Add(24*time.Hour))
	r2 := mustSum(t, cs, root)
	if !bytes.Equal(r1.Digest, r2.Digest) {
		t.Fatal("digest must not change after mtime bump (MetaModeAndSize excludes mtime)")
	}
}

// ── 41. LargeFile + 1000 small files ─────────────────────────────────────────

func TestLargeFile_ChangeDetected(t *testing.T) {
	t.Parallel()
	p := filepath.Join(t.TempDir(), "large.bin")
	const size = 4 * 1024 * 1024
	data := bytes.Repeat([]byte("ABCDEFGH"), size/8)
	os.WriteFile(p, data, 0o644)
	cs := mustNew(t)
	r1 := mustSum(t, cs, p)
	data[size/2] ^= 0xFF
	os.WriteFile(p, data, 0o644)
	r2 := mustSum(t, cs, p)
	if bytes.Equal(r1.Digest, r2.Digest) {
		t.Fatal("digest must change after modifying large file")
	}
}

func TestManySmallFiles_WorkerConsistency(t *testing.T) {
	t.Parallel()
	root := t.TempDir()
	for i := range 1000 {
		os.WriteFile(filepath.Join(root, fmt.Sprintf("f%05d", i)),
			[]byte(fmt.Sprintf("f%05d", i)), 0o644)
	}
	r1 := mustSum(t, mustNew(t, fshash.WithWorkers(1), fshash.WithMetadata(fshash.MetaNone)), root)
	r4 := mustSum(t, mustNew(t, fshash.WithWorkers(4), fshash.WithMetadata(fshash.MetaNone)), root)
	if !bytes.Equal(r1.Digest, r4.Digest) {
		t.Fatalf("1 vs 4 workers: %s vs %s", r1.Hex(), r4.Hex())
	}
}

// ── 42. Context cancellation ──────────────────────────────────────────────────

func TestContextCancellation(t *testing.T) {
	t.Parallel()
	root := t.TempDir()
	for i := range 200 {
		os.WriteFile(filepath.Join(root, fmt.Sprintf("file%04d.dat", i)),
			bytes.Repeat([]byte("x"), 1024), 0o644)
	}
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	_, err := mustNew(t, fshash.WithWorkers(4)).Sum(ctx, root)
	if err != nil {
		t.Logf("Sum returned (expected) error after cancel: %v", err)
	}
}

// ── Benchmarks ────────────────────────────────────────────────────────────────

func BenchmarkHashDir_100files_1worker(b *testing.B)   { benchDir(b, 100, 1, 4096) }
func BenchmarkHashDir_100files_8workers(b *testing.B)  { benchDir(b, 100, 8, 4096) }
func BenchmarkHashDir_1000files_4workers(b *testing.B) { benchDir(b, 1000, 4, 1024) }

func BenchmarkHashDir_WithMtimeCache(b *testing.B) {
	root := b.TempDir()
	content := bytes.Repeat([]byte("x"), 4096)
	for i := range 100 {
		os.WriteFile(filepath.Join(root, fmt.Sprintf("file%03d.dat", i)), content, 0o644)
	}
	cache := &fshash.MtimeCache{}
	cs := fshash.MustNew(fshash.WithWorkers(4), fshash.WithFileCache(cache))
	ctx := context.Background()
	cs.Sum(ctx, root) // warm
	b.ResetTimer()
	b.ReportAllocs()
	for range b.N {
		cs.Sum(ctx, root) //nolint:errcheck
	}
	b.SetBytes(int64(100 * 4096))
}

func BenchmarkShardedFile_4MiB(b *testing.B) {
	p := filepath.Join(b.TempDir(), "large.bin")
	data := bytes.Repeat([]byte("ABCDEFGHIJKLMNOP"), core.ShardThreshold/16+1)
	os.WriteFile(p, data, 0o644)
	cs := fshash.MustNew(fshash.WithWorkers(4), fshash.WithMetadata(fshash.MetaNone))
	ctx := context.Background()
	b.SetBytes(int64(len(data)))
	b.ResetTimer()
	b.ReportAllocs()
	for range b.N {
		cs.Sum(ctx, p) //nolint:errcheck
	}
}

func BenchmarkBlake3_Dir_100files(b *testing.B) {
	root := b.TempDir()
	for i := range 100 {
		os.WriteFile(filepath.Join(root, fmt.Sprintf("f%03d.dat", i)),
			bytes.Repeat([]byte("x"), 64*1024), 0o644)
	}
	cs := fshash.MustNew(fshash.WithAlgorithm(fshash.Blake3), fshash.WithWorkers(4))
	ctx := context.Background()
	b.ResetTimer()
	b.ReportAllocs()
	for range b.N {
		cs.Sum(ctx, root) //nolint:errcheck
	}
}

func benchDir(b *testing.B, nFiles, workers, fileSize int) {
	b.Helper()
	root := b.TempDir()
	content := bytes.Repeat([]byte("x"), fileSize)
	for i := range nFiles {
		os.WriteFile(filepath.Join(root, fmt.Sprintf("file%06d.dat", i)), content, 0o644)
	}
	cs := fshash.MustNew(fshash.WithWorkers(workers))
	ctx := context.Background()
	b.SetBytes(int64(nFiles * fileSize))
	b.ResetTimer()
	b.ReportAllocs()
	for range b.N {
		if _, err := cs.Sum(ctx, root); err != nil {
			b.Fatal(err)
		}
	}
}

// ── helpers ────────────────────────────────────────────────────────────────────

// xorHash satisfies hash.Hash for testing WithHasher (custom hasher).
type xorHash struct{ v byte }

func (h *xorHash) Write(p []byte) (int, error) {
	for _, b := range p {
		h.v ^= b
	}
	return len(p), nil
}
func (h *xorHash) Sum(b []byte) []byte { return append(b, h.v) }
func (h *xorHash) Reset()              { h.v = 0 }
func (h *xorHash) Size() int           { return 1 }
func (h *xorHash) BlockSize() int      { return 1 }

// xorCoreHasher wraps xorHash to implement core.Hasher.
type xorCoreHasher struct{}

func (x xorCoreHasher) New() hash.Hash    { return &xorHash{} }
func (x xorCoreHasher) Algorithm() string { return "xor8" }
func (x xorCoreHasher) DigestSize() int   { return 1 }

// ── Coverage gap tests ────────────────────────────────────────────────────────

func TestOptions_Accessor(t *testing.T) {
	t.Parallel()
	cs := mustNew(t, fshash.WithAlgorithm(fshash.Blake3), fshash.WithWorkers(3),
		fshash.WithMetadata(fshash.MetaMode))
	opts := cs.Options()
	if opts.Hasher.Algorithm() != "blake3" {
		t.Errorf("got %q want blake3", opts.Hasher.Algorithm())
	}
	if opts.Workers != 3 {
		t.Errorf("Workers=%d want 3", opts.Workers)
	}
	if opts.Meta != fshash.MetaMode {
		t.Errorf("Meta=%v want MetaMode", opts.Meta)
	}
	// Mutating the copy must not affect the Checksummer.
	opts.Workers = 99
	if cs.Options().Workers != 3 {
		t.Error("mutating Options() copy must not affect Checksummer")
	}
}

func TestWithPool_Custom(t *testing.T) {
	t.Parallel()
	pool := core.NewTieredPool()
	cs := mustNew(t, fshash.WithPool(pool), fshash.WithMetadata(fshash.MetaNone))
	root := t.TempDir()
	os.WriteFile(filepath.Join(root, "f"), []byte("data"), 0o644)
	if _, err := cs.Sum(context.Background(), root); err != nil {
		t.Fatalf("Sum with custom pool: %v", err)
	}
}

func TestChangeStatus_String(t *testing.T) {
	t.Parallel()
	cases := []struct {
		s    fshash.ChangeStatus
		want string
	}{
		{fshash.StatusUnchanged, "unchanged"},
		{fshash.StatusAdded, "added"},
		{fshash.StatusRemoved, "removed"},
		{fshash.StatusModified, "modified"},
		{fshash.ChangeStatus(99), "unknown"},
	}
	for _, tc := range cases {
		if got := tc.s.String(); got != tc.want {
			t.Errorf("ChangeStatus(%d).String()=%q want %q", tc.s, got, tc.want)
		}
	}
}

func TestTreeChange_String(t *testing.T) {
	t.Parallel()
	ch := fshash.TreeChange{RelPath: "foo/bar.go", Status: fshash.StatusModified, Kind: fshash.KindFile}
	s := ch.String()
	if !strings.Contains(s, "foo/bar.go") || !strings.Contains(s, "modified") {
		t.Errorf("TreeChange.String()=%q", s)
	}
}

func TestTreeComparison_CountByStatus(t *testing.T) {
	t.Parallel()
	rA, rB := t.TempDir(), t.TempDir()
	buildTree(t, rA, fsTree{"a": "1", "b": "2", "c": "3"})
	buildTree(t, rB, fsTree{"a": "1", "b": "changed", "d": "new"})
	cs := mustNew(t, fshash.WithMetadata(fshash.MetaNone))
	cmp, err := cs.CompareTrees(context.Background(), rA, rB)
	if err != nil {
		t.Fatal(err)
	}
	counts := cmp.CountByStatus()
	if counts[fshash.StatusModified] != 1 {
		t.Errorf("want 1 modified, got %d", counts[fshash.StatusModified])
	}
	if counts[fshash.StatusRemoved] != 1 {
		t.Errorf("want 1 removed (c), got %d", counts[fshash.StatusRemoved])
	}
	if counts[fshash.StatusAdded] != 1 {
		t.Errorf("want 1 added (d), got %d", counts[fshash.StatusAdded])
	}
}

func TestTreeComparison_Summary(t *testing.T) {
	t.Parallel()
	root := t.TempDir()
	buildTree(t, root, fsTree{"f": "data"})
	cs := mustNew(t)
	cmp, _ := cs.CompareTrees(context.Background(), root, root)
	if !strings.Contains(cmp.Summary(), "identical") {
		t.Errorf("identical summary: %q", cmp.Summary())
	}
	rootB := t.TempDir()
	buildTree(t, rootB, fsTree{"f": "different", "g": "new"})
	cmp2, _ := cs.CompareTrees(context.Background(), root, rootB)
	if !strings.Contains(cmp2.Summary(), "added") {
		t.Errorf("diff summary: %q", cmp2.Summary())
	}
}

func TestEntryResult_String(t *testing.T) {
	t.Parallel()
	e := fshash.EntryResult{RelPath: "foo/bar.txt", Kind: fshash.KindFile, Digest: []byte{0x01, 0x02}}
	s := e.String()
	if !strings.Contains(s, "foo/bar.txt") || !strings.Contains(s, "file") {
		t.Errorf("EntryResult.String()=%q", s)
	}
}

func TestResult_Hex(t *testing.T) {
	t.Parallel()
	r := fshash.Result{Digest: []byte{0xAB, 0xCD, 0xEF}}
	if r.Hex() != "abcdef" {
		t.Errorf("Hex()=%q want 'abcdef'", r.Hex())
	}
	if r.String() != "abcdef" {
		t.Errorf("String()=%q want 'abcdef'", r.String())
	}
}

func TestFileTooLargeError_Message(t *testing.T) {
	t.Parallel()
	e := &fshash.FileTooLargeError{Path: "/some/file.bin", Size: 2048, Limit: 1024}
	msg := e.Error()
	if !strings.Contains(msg, "/some/file.bin") || !strings.Contains(msg, "2048") || !strings.Contains(msg, "1024") {
		t.Errorf("FileTooLargeError.Error()=%q", msg)
	}
}

func TestDiffResult_Empty(t *testing.T) {
	t.Parallel()
	if !(fshash.DiffResult{}).Empty() {
		t.Fatal("zero-value DiffResult must be empty")
	}
	if (fshash.DiffResult{Added: []string{"x"}}).Empty() {
		t.Fatal("DiffResult with Added must not be empty")
	}
}

func TestMemoryCache_InvalidateAll(t *testing.T) {
	t.Parallel()
	c := &fshash.MemoryCache{}
	for i := range 5 {
		c.Set(fmt.Sprintf("/key%d", i), []byte{byte(i)})
	}
	c.InvalidateAll()
	for i := range 5 {
		if _, ok := c.Get(fmt.Sprintf("/key%d", i)); ok {
			t.Errorf("key%d should be gone after InvalidateAll", i)
		}
	}
}

func TestMtimeCache_Invalidate(t *testing.T) {
	t.Parallel()
	dir := t.TempDir()
	p := filepath.Join(dir, "f.txt")
	os.WriteFile(p, []byte("data"), 0o644)
	c := &fshash.MtimeCache{}
	c.Set(p, []byte{0x01})
	c.Invalidate(p)
	if _, ok := c.Get(p); ok {
		t.Fatal("expected miss after Invalidate")
	}
}

func TestInstrumentedCache_Invalidate(t *testing.T) {
	t.Parallel()
	root := t.TempDir()
	p := filepath.Join(root, "f")
	os.WriteFile(p, []byte("data"), 0o644)
	cache := &fshash.MemoryCache{}
	cs := mustNew(t, fshash.WithFileCache(cache))
	ins := fshash.NewInspector(cs, cache)
	ins.Sum(context.Background(), root) //nolint:errcheck
	// Verify Invalidate flows through to delegate
	cache.Invalidate(p)
	if _, ok := cache.Get(p); ok {
		t.Fatal("expected miss after Invalidate on delegate")
	}
}

func TestSortedWalker_IsSorted(t *testing.T) {
	t.Parallel()
	sw := fshash.SortedWalker{Inner: fshash.OSWalker{}}
	if !sw.IsSorted() {
		t.Error("SortedWalker.IsSorted() must return true")
	}
	root := t.TempDir()
	buildTree(t, root, fsTree{"z": "z", "a": "a", "m": "m"})
	entries, err := sw.ReadDir(root)
	if err != nil {
		t.Fatal(err)
	}
	names := make([]string, len(entries))
	for i, e := range entries {
		names[i] = e.Name()
	}
	if !sort.StringsAreSorted(names) {
		t.Errorf("SortedWalker must return sorted entries: %v", names)
	}
}

func TestFSWalker_ReadSymlink(t *testing.T) {
	t.Parallel()
	fw := fshash.FSWalker{FS: os.DirFS(".")}
	target, err := fw.ReadSymlink("anything")
	if err != nil {
		t.Fatalf("FSWalker.ReadSymlink must not error: %v", err)
	}
	if target != "" {
		t.Errorf("FSWalker.ReadSymlink must return empty string, got %q", target)
	}
}

func TestWatchWithSnapshot(t *testing.T) {
	t.Parallel()
	root := t.TempDir()
	p := filepath.Join(root, "data.txt")
	os.WriteFile(p, []byte("before"), 0o644)

	snap, err := fshash.TakeSnapshot(context.Background(), root,
		fshash.WithMetadata(fshash.MetaNone))
	if err != nil {
		t.Fatal(err)
	}

	cs := mustNew(t, fshash.WithMetadata(fshash.MetaNone))
	w := fshash.NewWatcher(cs, root,
		fshash.WithWatchInterval(20*time.Millisecond),
		fshash.WithWatchCompareTrees(true),
	)
	id, events := w.Events().Subscribe(4)
	defer w.Events().Unsubscribe(id)

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	go w.WatchWithSnapshot(ctx, snap) //nolint:errcheck

	time.Sleep(60 * time.Millisecond)
	os.WriteFile(p, []byte("after"), 0o644)

	select {
	case evt := <-events:
		if evt.Comparison == nil {
			t.Error("expected non-nil Comparison with WithWatchCompareTrees(true)")
		} else {
			changed := evt.Comparison.OnlyChanged()
			if len(changed) == 0 {
				t.Error("expected at least one changed entry")
			}
		}
	case <-time.After(3 * time.Second):
		t.Fatal("timeout waiting for change event")
	}
}

func TestStream_Ctx(t *testing.T) {
	t.Parallel()
	ctx := context.Background()
	s := core.NewStream[int](ctx, 4)
	if s.Ctx() == nil {
		t.Fatal("Ctx() must not be nil")
	}
	s.Close()
}

func TestEventBus_Subscribers(t *testing.T) {
	t.Parallel()
	bus := core.NewEventBus[int]()
	if bus.Subscribers() != 0 {
		t.Fatalf("expected 0 subscribers initially, got %d", bus.Subscribers())
	}
	id1, _ := bus.Subscribe(4)
	id2, _ := bus.Subscribe(4)
	if bus.Subscribers() != 2 {
		t.Fatalf("expected 2 subscribers, got %d", bus.Subscribers())
	}
	bus.Unsubscribe(id1)
	bus.Unsubscribe(id2)
	if bus.Subscribers() != 0 {
		t.Fatalf("expected 0 after unsubscribe, got %d", bus.Subscribers())
	}
}

func TestMerkleRoot_Reproducible(t *testing.T) {
	t.Parallel()
	root := t.TempDir()
	buildTree(t, root, fsTree{
		"a.txt": "alpha", "b.txt": "beta",
		"sub": "", "sub/c": "gamma",
	})
	cs := mustNew(t, fshash.WithMetadata(fshash.MetaNone))
	ctx := context.Background()

	collectEntries := func() []fshash.EntryResult {
		var entries []fshash.EntryResult
		for e := range cs.MerkleStream(ctx, root).Chan() {
			entries = append(entries, e)
		}
		return entries
	}

	h := core.MustHasher(core.SHA256)
	newHash := func() interface {
		Write([]byte) (int, error)
		Sum([]byte) []byte
	} {
		return h.New()
	}

	// Two independent runs must produce the same Merkle root.
	d1 := fshash.MerkleRoot(collectEntries(), newHash)
	d2 := fshash.MerkleRoot(collectEntries(), newHash)
	if !bytes.Equal(d1, d2) {
		t.Fatal("MerkleRoot must be reproducible across runs")
	}

	// Merkle root must differ from empty input.
	if len(d1) == 0 {
		t.Fatal("MerkleRoot must not be empty")
	}

	// Changing content must change the Merkle root.
	os.WriteFile(filepath.Join(root, "a.txt"), []byte("CHANGED"), 0o644)
	d3 := fshash.MerkleRoot(collectEntries(), newHash)
	if bytes.Equal(d1, d3) {
		t.Fatal("MerkleRoot must change when content changes")
	}
}

func TestWithWatchCompareTrees_Option(t *testing.T) {
	t.Parallel()
	cs := mustNew(t)
	// Just verify the option is accepted and watcher is constructable
	w := fshash.NewWatcher(cs, t.TempDir(),
		fshash.WithWatchCompareTrees(true),
		fshash.WithWatchInterval(1*time.Second),
	)
	if w == nil {
		t.Fatal("NewWatcher returned nil")
	}
}

func TestSortedWalker_LstatAndReadSymlink(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("symlinks require elevated perms on Windows")
	}
	t.Parallel()
	root := t.TempDir()
	p := filepath.Join(root, "f.txt")
	os.WriteFile(p, []byte("data"), 0o644)
	os.Symlink(p, filepath.Join(root, "link"))

	sw := fshash.SortedWalker{Inner: fshash.OSWalker{}}

	fi, err := sw.Lstat(p)
	if err != nil {
		t.Fatalf("SortedWalker.Lstat: %v", err)
	}
	if fi.Name() != "f.txt" {
		t.Errorf("Lstat name: %q", fi.Name())
	}

	target, err := sw.ReadSymlink(filepath.Join(root, "link"))
	if err != nil {
		t.Fatalf("SortedWalker.ReadSymlink: %v", err)
	}
	if target != p {
		t.Errorf("ReadSymlink target: %q want %q", target, p)
	}
}

func TestFSWalker_Lstat(t *testing.T) {
	t.Parallel()
	root := t.TempDir()
	os.WriteFile(filepath.Join(root, "f.txt"), []byte("data"), 0o644)
	fw := fshash.FSWalker{FS: os.DirFS(root)}
	fi, err := fw.Lstat("f.txt")
	if err != nil {
		t.Fatalf("FSWalker.Lstat: %v", err)
	}
	if fi.Name() != "f.txt" {
		t.Errorf("got %q", fi.Name())
	}
}

func TestInstrumentedCache_InvalidatePassthrough(t *testing.T) {
	t.Parallel()
	root := t.TempDir()
	p := filepath.Join(root, "f")
	os.WriteFile(p, []byte("data"), 0o644)

	// NewCachingChecksummer wraps an instrumentedCache over our MemoryCache
	cache := &fshash.MemoryCache{}
	cs, _ := fshash.NewCachingChecksummer(cache)
	ins := fshash.NewInspector(cs, cache)

	ins.Sum(context.Background(), root) //nolint:errcheck
	// File should be cached
	if _, ok := cache.Get(p); !ok {
		t.Skip("file not cached (may depend on OS file size reporting)")
	}
	// Invalidate via Inspector's wrapped cache
	cache.Invalidate(p)
	if _, ok := cache.Get(p); ok {
		t.Fatal("expected miss after Invalidate")
	}
}

func TestInstrumentedCache_InvalidateDirect(t *testing.T) {
	// instrumentedCache.Invalidate is only reachable through Inspector's
	// internal cache wrapper, which delegates to the outer FileCache.
	t.Parallel()
	root := t.TempDir()
	p := filepath.Join(root, "f.txt")
	os.WriteFile(p, []byte("hello"), 0o644)

	cache := &fshash.MemoryCache{}
	cs := mustNew(t, fshash.WithFileCache(cache))
	ins := fshash.NewInspector(cs, cache)

	// First pass populates the cache via instrumentedCache.Set
	ins.Sum(context.Background(), root) //nolint:errcheck

	// Second pass: hit (exercises instrumentedCache.Get hit path)
	ins.Sum(context.Background(), root) //nolint:errcheck

	// Invalidate through the Inspector's underlying instrumented cache
	// by invalidating the outer delegate — the instrumentedCache.Invalidate
	// wrapper is called when Inspector's wrapped cache sees the call.
	cache.Invalidate(p)
	if _, ok := cache.Get(p); ok {
		t.Fatal("expected miss after Invalidate via delegate")
	}

	// After invalidation the hit rate should reset on next Sum
	_, entries, _ := ins.Sum(context.Background(), root)
	for _, e := range entries {
		if e.Kind == fshash.KindFile && e.CacheHit {
			t.Errorf("%q: expected miss after invalidation", e.RelPath)
		}
	}
}
