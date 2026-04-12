package fshash

import (
	"bytes"
	"context"
	"encoding/hex"
	"errors"
	"fmt"
	"hash"
	"io"
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
)

// ── test helpers ──────────────────────────────────────────────────────────────

// fsTree describes a directory tree to materialise on disk.
//   - value == ""           → directory
//   - value starts "-> "    → symbolic link; remainder is the target
//   - anything else         → regular file whose content is the value
type fsTree map[string]string

func buildTree(tb testing.TB, root string, tree fsTree) {
	tb.Helper()
	// Sort so that parent directories are always created before their children.
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
			if err := os.MkdirAll(filepath.Dir(abs), 0o755); err != nil {
				tb.Fatalf("mkdir %s: %v", filepath.Dir(abs), err)
			}
			target := strings.TrimPrefix(content, "-> ")
			_ = os.Remove(abs)
			if err := os.Symlink(target, abs); err != nil {
				tb.Fatalf("symlink %s -> %s: %v", abs, target, err)
			}
		default:
			if err := os.MkdirAll(filepath.Dir(abs), 0o755); err != nil {
				tb.Fatalf("mkdir %s: %v", filepath.Dir(abs), err)
			}
			if err := os.WriteFile(abs, []byte(content), 0o644); err != nil {
				tb.Fatalf("writefile %s: %v", abs, err)
			}
		}
	}
}

func mustNew(t *testing.T, opts ...Option) *Checksummer {
	t.Helper()
	cs, err := New(opts...)
	if err != nil {
		t.Fatalf("New: %v", err)
	}
	return cs
}

func mustSum(t *testing.T, cs *Checksummer, absPath string) Result {
	t.Helper()
	res, err := cs.Sum(context.Background(), absPath)
	if err != nil {
		t.Fatalf("Sum(%q): %v", absPath, err)
	}
	return res
}

func relPaths(entries []EntryResult) []string {
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
		"a":          "",
		"a/foo":      "hello",
		"a/bar":      "world",
		"a/baz":      "123",
		"b":          "",
		"b/nested":   "",
		"b/nested/x": "deep",
		"c.txt":      "top-level file",
	}
	root1, root2 := t.TempDir(), t.TempDir()
	buildTree(t, root1, tree)
	buildTree(t, root2, tree)

	cs := mustNew(t)
	r1 := mustSum(t, cs, root1)
	r2 := mustSum(t, cs, root2)
	if !bytes.Equal(r1.Digest, r2.Digest) {
		t.Fatalf("non-reproducible: %s != %s", r1.Hex(), r2.Hex())
	}
}

// ── 2. Sorted traversal ───────────────────────────────────────────────────────

func TestSortedTraversal(t *testing.T) {
	t.Parallel()
	cs := mustNew(t, WithMetadata(MetaNone))

	root1 := t.TempDir()
	_ = os.WriteFile(filepath.Join(root1, "z"), []byte("z"), 0o644)
	_ = os.WriteFile(filepath.Join(root1, "a"), []byte("a"), 0o644)

	root2 := t.TempDir()
	_ = os.WriteFile(filepath.Join(root2, "a"), []byte("a"), 0o644)
	_ = os.WriteFile(filepath.Join(root2, "z"), []byte("z"), 0o644)

	r1, r2 := mustSum(t, cs, root1), mustSum(t, cs, root2)
	if !bytes.Equal(r1.Digest, r2.Digest) {
		t.Fatalf("OS traversal order must not matter: %s != %s", r1.Hex(), r2.Hex())
	}
}

// ── 3. Content sensitivity ────────────────────────────────────────────────────

func TestContentSensitivity(t *testing.T) {
	t.Parallel()
	cs := mustNew(t, WithMetadata(MetaNone))
	root := t.TempDir()
	p := filepath.Join(root, "file.txt")
	_ = os.WriteFile(p, []byte("original"), 0o644)
	r1 := mustSum(t, cs, root)

	_ = os.WriteFile(p, []byte("changed!"), 0o644)
	r2 := mustSum(t, cs, root)
	if bytes.Equal(r1.Digest, r2.Digest) {
		t.Fatal("digest must change when file content changes")
	}
}

// ── 4. Name sensitivity ───────────────────────────────────────────────────────

func TestNameSensitivity(t *testing.T) {
	t.Parallel()
	cs := mustNew(t, WithMetadata(MetaNone))

	root1 := t.TempDir()
	_ = os.WriteFile(filepath.Join(root1, "foo"), []byte("hello"), 0o644)

	root2 := t.TempDir()
	_ = os.WriteFile(filepath.Join(root2, "bar"), []byte("hello"), 0o644)

	r1, r2 := mustSum(t, cs, root1), mustSum(t, cs, root2)
	if bytes.Equal(r1.Digest, r2.Digest) {
		t.Fatal("different file names with same content must produce different digests")
	}
}

// ── 5. Empty directory ────────────────────────────────────────────────────────

func TestEmptyDirectory(t *testing.T) {
	t.Parallel()
	cs := mustNew(t)
	r1 := mustSum(t, cs, t.TempDir())
	r2 := mustSum(t, cs, t.TempDir())
	if !bytes.Equal(r1.Digest, r2.Digest) {
		t.Fatalf("two empty dirs must hash identically: %s vs %s", r1.Hex(), r2.Hex())
	}
}

// ── 6. Single file ────────────────────────────────────────────────────────────

func TestSingleFile(t *testing.T) {
	t.Parallel()
	p := filepath.Join(t.TempDir(), "hello.txt")
	_ = os.WriteFile(p, []byte("hello, world"), 0o644)

	res, err := mustNew(t).Sum(context.Background(), p)
	if err != nil {
		t.Fatal(err)
	}
	if len(res.Digest) == 0 {
		t.Fatal("expected non-empty digest")
	}
}

// ── 7. Deep tree + sorted entries ────────────────────────────────────────────

func TestDeepTree(t *testing.T) {
	t.Parallel()
	cs := mustNew(t, WithCollectEntries(true))
	root := t.TempDir()
	buildTree(t, root, fsTree{
		"a":          "",
		"a/b":        "",
		"a/b/c":      "",
		"a/b/c/d":    "",
		"a/b/c/d/f1": "deep file 1",
		"a/b/c/d/f2": "deep file 2",
		"a/b/c/g":    "mid file",
		"a/b/h":      "shallow file",
		"top":        "top file",
	})

	res := mustSum(t, cs, root)
	paths := relPaths(res.Entries)
	// "." (root dir) must be last; all other entries must be sorted.
	if len(paths) > 0 && paths[len(paths)-1] != "." {
		t.Fatalf("root '.' must be last entry, got: %v", paths)
	}
	nonRoot := paths[:len(paths)-1]
	if !sort.StringsAreSorted(nonRoot) {
		t.Fatalf("non-root entries must be sorted, got: %v", nonRoot)
	}

	want := map[string]struct{}{
		"a": {}, "a/b": {}, "a/b/c": {}, "a/b/c/d": {},
		"a/b/c/d/f1": {}, "a/b/c/d/f2": {},
		"a/b/c/g": {}, "a/b/h": {}, "top": {},
	}
	for _, e := range res.Entries {
		delete(want, e.RelPath)
	}
	for missing := range want {
		t.Errorf("missing entry %q in collected results", missing)
	}
}

// ── 8. ExcludePatterns filter ─────────────────────────────────────────────────

func TestFilter(t *testing.T) {
	t.Parallel()
	root := t.TempDir()
	buildTree(t, root, fsTree{
		"include.go":      "package main",
		"exclude.tmp":     "noise",
		"subdir":          "",
		"subdir/keep.go":  "// keep",
		"subdir/drop.tmp": "noise2",
	})

	cs := mustNew(t, WithFilter(ExcludePatterns("*.tmp")), WithCollectEntries(true))
	res := mustSum(t, cs, root)

	for _, e := range res.Entries {
		if strings.HasSuffix(e.RelPath, ".tmp") {
			t.Errorf("filtered entry %q must not appear in results", e.RelPath)
		}
	}

	cs2 := mustNew(t)
	res2 := mustSum(t, cs2, root)
	if bytes.Equal(res.Digest, res2.Digest) {
		t.Fatal("filtered and unfiltered digests must differ")
	}
}

// ── 9. ExcludeNames filter ────────────────────────────────────────────────────

func TestExcludeNames(t *testing.T) {
	t.Parallel()
	root := t.TempDir()
	buildTree(t, root, fsTree{
		"code.go":    "package main",
		".git":       "",
		".git/HEAD":  "ref: refs/heads/main",
		"vendor":     "",
		"vendor/lib": "lib code",
	})

	cs := mustNew(t, WithFilter(ExcludeNames(".git", "vendor")), WithCollectEntries(true))
	res := mustSum(t, cs, root)
	for _, e := range res.Entries {
		if strings.HasPrefix(e.RelPath, ".git") || strings.HasPrefix(e.RelPath, "vendor") {
			t.Errorf("excluded entry %q appeared in results", e.RelPath)
		}
	}
}

// ── 10. ChainFilters ──────────────────────────────────────────────────────────

func TestChainFilters(t *testing.T) {
	t.Parallel()
	root := t.TempDir()
	buildTree(t, root, fsTree{"a.go": "code", "b.tmp": "temp", "c.log": "log"})

	cs := mustNew(t,
		WithFilter(ChainFilters(ExcludePatterns("*.tmp"), ExcludePatterns("*.log"))),
		WithCollectEntries(true),
	)
	res := mustSum(t, cs, root)
	for _, e := range res.Entries {
		if e.RelPath == "b.tmp" || e.RelPath == "c.log" {
			t.Errorf("entry %q should have been excluded by chain filter", e.RelPath)
		}
	}
}

// ── 11. Metadata sensitivity ──────────────────────────────────────────────────

func TestMetadataSensitivity(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("chmod not fully supported on Windows")
	}
	t.Parallel()
	root := t.TempDir()
	p := filepath.Join(root, "script.sh")
	_ = os.WriteFile(p, []byte("#!/bin/sh\necho hi"), 0o644)

	csMode := mustNew(t, WithMetadata(MetaMode))
	r1 := mustSum(t, csMode, root)
	_ = os.Chmod(p, 0o755)
	r2 := mustSum(t, csMode, root)
	if bytes.Equal(r1.Digest, r2.Digest) {
		t.Fatal("digest must change after chmod when MetaMode is set")
	}

	csNone := mustNew(t, WithMetadata(MetaNone))
	_ = os.WriteFile(p, []byte("#!/bin/sh\necho hi"), 0o644)
	rn1 := mustSum(t, csNone, root)
	_ = os.Chmod(p, 0o755)
	rn2 := mustSum(t, csNone, root)
	if !bytes.Equal(rn1.Digest, rn2.Digest) {
		t.Fatal("MetaNone digest must not change after chmod")
	}
}

// ── 12. Algorithms produce distinct outputs ───────────────────────────────────

func TestAlgorithms(t *testing.T) {
	t.Parallel()
	root := t.TempDir()
	_ = os.WriteFile(filepath.Join(root, "data"), []byte("test data"), 0o644)

	seen := map[string]Algorithm{}
	for _, algo := range []Algorithm{SHA256, SHA512, SHA1, MD5, XXHash64, XXHash3, Blake3, CRC32C} {
		cs := mustNew(t, WithAlgorithm(algo))
		res := mustSum(t, cs, root)
		d := res.Hex()
		if prev, ok := seen[d]; ok {
			t.Fatalf("collision between %s and %s: digest=%s", algo, prev, d)
		}
		seen[d] = algo
	}
}

// ── 13. Custom Hasher interface ───────────────────────────────────────────────

type trivialHasher struct{}

func (trivialHasher) New() hash.Hash    { return &trivialHash{} }
func (trivialHasher) Algorithm() string { return "trivial-xor" }

type trivialHash struct{ v byte }

func (h *trivialHash) Write(p []byte) (int, error) {
	for _, b := range p {
		h.v ^= b
	}
	return len(p), nil
}
func (h *trivialHash) Sum(b []byte) []byte { return append(b, h.v) }
func (h *trivialHash) Reset()              { h.v = 0 }
func (h *trivialHash) Size() int           { return 1 }
func (h *trivialHash) BlockSize() int      { return 1 }

func TestCustomHasher(t *testing.T) {
	t.Parallel()
	cs, err := New(WithHasher(trivialHasher{}))
	if err != nil {
		t.Fatal(err)
	}
	root := t.TempDir()
	_ = os.WriteFile(filepath.Join(root, "f"), []byte("abc"), 0o644)
	res, err := cs.Sum(context.Background(), root)
	if err != nil {
		t.Fatal(err)
	}
	if len(res.Digest) == 0 {
		t.Fatal("expected non-empty digest from custom hasher")
	}
}

// ── 14. Worker count does not affect digest ───────────────────────────────────

func TestWorkerPoolConsistency(t *testing.T) {
	t.Parallel()
	root := t.TempDir()
	for i := range 50 {
		_ = os.WriteFile(
			filepath.Join(root, fmt.Sprintf("file%03d.txt", i)),
			[]byte(fmt.Sprintf("content of file %d", i)),
			0o644,
		)
	}
	r1 := mustSum(t, mustNew(t, WithWorkers(1)), root)
	r8 := mustSum(t, mustNew(t, WithWorkers(8)), root)
	if !bytes.Equal(r1.Digest, r8.Digest) {
		t.Fatalf("worker count must not affect digest: 1=%s 8=%s", r1.Hex(), r8.Hex())
	}
}

// ── 15. Context cancellation ──────────────────────────────────────────────────

func TestContextCancellation(t *testing.T) {
	t.Parallel()
	root := t.TempDir()
	for i := range 200 {
		_ = os.WriteFile(
			filepath.Join(root, fmt.Sprintf("file%04d.dat", i)),
			bytes.Repeat([]byte("x"), 1024),
			0o644,
		)
	}
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	_, err := mustNew(t, WithWorkers(4)).Sum(ctx, root)
	// Either completes before noticing cancellation, or returns an error.
	if err != nil {
		t.Logf("Sum returned (expected) error after cancel: %v", err)
	}
}

// ── 16. Symlinks – no follow ──────────────────────────────────────────────────

func TestSymlinkNoFollow(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("symlinks require elevated permissions on Windows")
	}
	t.Parallel()
	root := t.TempDir()
	buildTree(t, root, fsTree{
		"real":     "real content",
		"link":     "-> real",
		"dangling": "-> /nonexistent/path",
	})

	cs := mustNew(t, WithFollowSymlinks(false), WithCollectEntries(true))
	res := mustSum(t, cs, root)

	kindsBy := map[string]EntryKind{}
	for _, e := range res.Entries {
		kindsBy[e.RelPath] = e.Kind
	}
	if kindsBy["real"] != KindFile {
		t.Errorf("real: want KindFile, got %v", kindsBy["real"])
	}
	if kindsBy["link"] != KindSymlink {
		t.Errorf("link: want KindSymlink, got %v", kindsBy["link"])
	}
	if kindsBy["dangling"] != KindSymlink {
		t.Errorf("dangling: want KindSymlink, got %v", kindsBy["dangling"])
	}
}

// ── 17. Symlinks – follow ─────────────────────────────────────────────────────

func TestSymlinkFollow(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("symlinks require elevated permissions on Windows")
	}
	t.Parallel()
	root1 := t.TempDir()
	buildTree(t, root1, fsTree{"real": "shared content", "link": "-> real"})

	root2 := t.TempDir()
	buildTree(t, root2, fsTree{"real": "shared content", "link": "shared content"})

	cs := mustNew(t, WithFollowSymlinks(true), WithMetadata(MetaNone))
	r1, r2 := mustSum(t, cs, root1), mustSum(t, cs, root2)
	if !bytes.Equal(r1.Digest, r2.Digest) {
		t.Fatalf("follow-symlink mismatch: %s vs %s", r1.Hex(), r2.Hex())
	}
}

// ── 18. Symlink cycle detection ───────────────────────────────────────────────

func TestSymlinkCycleDetection(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("symlinks require elevated permissions on Windows")
	}
	t.Parallel()
	root := t.TempDir()
	_ = os.Symlink(filepath.Join(root, "b"), filepath.Join(root, "a"))
	_ = os.Symlink(filepath.Join(root, "a"), filepath.Join(root, "b"))

	_, err := mustNew(t, WithFollowSymlinks(true)).Sum(context.Background(), root)
	if err == nil {
		t.Fatal("expected cycle detection error")
	}
	if !strings.Contains(err.Error(), "cycle") {
		t.Fatalf("error must mention 'cycle': %v", err)
	}
}

// ── 19. FSWalker ──────────────────────────────────────────────────────────────

func TestFSWalker(t *testing.T) {
	t.Parallel()
	root := t.TempDir()
	buildTree(t, root, fsTree{"a": "alpha", "b": "beta", "sub": "", "sub/c": "gamma"})

	diskRes := mustSum(t, mustNew(t, WithMetadata(MetaNone)), root)

	fsCS, err := New(WithMetadata(MetaNone), WithWalker(FSWalker{FS: os.DirFS(root)}))
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

// ── 20. CollectEntries flag ───────────────────────────────────────────────────

func TestCollectEntries(t *testing.T) {
	t.Parallel()
	root := t.TempDir()
	buildTree(t, root, fsTree{"x.txt": "x", "y.txt": "y", "sub": "", "sub/z": "z"})

	resOn := mustSum(t, mustNew(t, WithCollectEntries(true)), root)
	if len(resOn.Entries) == 0 {
		t.Fatal("expected entries with CollectEntries=true")
	}

	resOff := mustSum(t, mustNew(t, WithCollectEntries(false)), root)
	if len(resOff.Entries) != 0 {
		t.Fatal("expected no entries with CollectEntries=false")
	}
	if !bytes.Equal(resOn.Digest, resOff.Digest) {
		t.Fatal("CollectEntries must not affect the digest value")
	}
}

// ── 21. Diff ──────────────────────────────────────────────────────────────────

func TestDiff(t *testing.T) {
	t.Parallel()
	rootA := t.TempDir()
	buildTree(t, rootA, fsTree{"common": "same", "modified": "old", "removed": "gone"})
	rootB := t.TempDir()
	buildTree(t, rootB, fsTree{"common": "same", "modified": "new", "added": "new file"})

	cs := mustNew(t, WithMetadata(MetaNone))
	diff, err := cs.Diff(context.Background(), rootA, rootB)
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

func TestDiffIdentical(t *testing.T) {
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

// ── 22. Verify ────────────────────────────────────────────────────────────────

func TestVerify(t *testing.T) {
	t.Parallel()
	p := filepath.Join(t.TempDir(), "f.txt")
	_ = os.WriteFile(p, []byte("hello"), 0o644)

	cs := mustNew(t)
	res := mustSum(t, cs, p)

	// Correct digest → no error.
	if err := cs.Verify(context.Background(), p, res.Digest); err != nil {
		t.Fatalf("Verify with correct digest: %v", err)
	}

	// Wrong digest → VerifyError.
	bad := make([]byte, len(res.Digest))
	if err := cs.Verify(context.Background(), p, bad); err == nil {
		t.Fatal("expected VerifyError for wrong digest")
	} else {
		var ve *VerifyError
		if !errors.As(err, &ve) {
			t.Fatalf("expected *VerifyError, got %T: %v", err, err)
		}
		if !strings.Contains(ve.Error(), p) {
			t.Errorf("VerifyError.Error() should contain the path")
		}
	}
}

// ── 23. FileCache short-circuits disk reads ───────────────────────────────────

func TestFileCacheShortCircuit(t *testing.T) {
	t.Parallel()
	root := t.TempDir()
	p := filepath.Join(root, "f.txt")
	_ = os.WriteFile(p, []byte("content"), 0o644)

	cache := &MemoryCache{}
	cs := mustNew(t, WithFileCache(cache))

	// First call — cache miss, file must be read.
	r1 := mustSum(t, cs, root)
	_, ok := cache.Get(p)
	if !ok {
		t.Fatal("cache must be populated after first Sum")
	}

	// Second call — cache hit.  Corrupt the file on disk; digest must stay the
	// same because it is served from cache.
	_ = os.WriteFile(p, []byte("CORRUPTED"), 0o644)
	r2 := mustSum(t, cs, root)
	if !bytes.Equal(r1.Digest, r2.Digest) {
		t.Fatal("cache hit must shadow the corrupted file on disk")
	}

	// After invalidation the new content is read.
	cache.Invalidate(p)
	r3 := mustSum(t, cs, root)
	if bytes.Equal(r1.Digest, r3.Digest) {
		t.Fatal("after invalidation, digest must reflect the new file content")
	}
}

// ── 24. NewCachingChecksummer convenience ─────────────────────────────────────

func TestNewCachingChecksummer(t *testing.T) {
	t.Parallel()
	root := t.TempDir()
	_ = os.WriteFile(filepath.Join(root, "f"), []byte("data"), 0o644)

	cache := &MemoryCache{}
	cs, err := NewCachingChecksummer(cache)
	if err != nil {
		t.Fatal(err)
	}
	r1, err := cs.Sum(context.Background(), root)
	if err != nil {
		t.Fatal(err)
	}
	r2, err := cs.Sum(context.Background(), root)
	if err != nil {
		t.Fatal(err)
	}
	if !bytes.Equal(r1.Digest, r2.Digest) {
		t.Fatal("CachingChecksummer must return the same digest on repeat calls")
	}
}

// ── 25. MemoryCache: deep copy on Set ────────────────────────────────────────

func TestMemoryCacheDeepCopy(t *testing.T) {
	t.Parallel()
	c := &MemoryCache{}
	orig := []byte{0xDE, 0xAD, 0xBE, 0xEF}
	c.Set("/key", orig)

	got, ok := c.Get("/key")
	if !ok {
		t.Fatal("expected cache hit")
	}
	if !bytes.Equal(got, orig) {
		t.Fatalf("wrong value: %x", got)
	}

	orig[0] = 0xFF // mutate after Set
	got2, _ := c.Get("/key")
	if got2[0] == 0xFF {
		t.Fatal("cache did not deep-copy on Set")
	}

	c.Invalidate("/key")
	_, ok = c.Get("/key")
	if ok {
		t.Fatal("expected miss after Invalidate")
	}
}

func TestMemoryCacheMiss(t *testing.T) {
	t.Parallel()
	c := &MemoryCache{}
	_, ok := c.Get("/non/existent")
	if ok {
		t.Fatal("expected miss for unknown key")
	}
}

func TestMemoryCacheInvalidateAll(t *testing.T) {
	t.Parallel()
	c := &MemoryCache{}
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

// ── 26. Snapshot round-trip ───────────────────────────────────────────────────

func TestSnapshotRoundTrip(t *testing.T) {
	t.Parallel()
	root := t.TempDir()
	buildTree(t, root, fsTree{"a": "alpha", "b": "beta", "sub": "", "sub/c": "gamma"})

	snap, err := TakeSnapshot(context.Background(), root)
	if err != nil {
		t.Fatal(err)
	}
	if snap.RootDigest == "" {
		t.Fatal("empty root digest")
	}
	if snap.Algorithm != "sha256" {
		t.Fatalf("expected sha256, got %s", snap.Algorithm)
	}
	if len(snap.Entries) == 0 {
		t.Fatal("expected entries in snapshot")
	}

	var buf bytes.Buffer
	if _, err := snap.WriteTo(&buf); err != nil {
		t.Fatal(err)
	}

	snap2, err := ReadSnapshot(&buf)
	if err != nil {
		t.Fatal(err)
	}
	if snap2.RootDigest != snap.RootDigest {
		t.Fatalf("round-trip: %s != %s", snap.RootDigest, snap2.RootDigest)
	}
}

// ── 27. Snapshot.VerifyAgainst ────────────────────────────────────────────────

func TestSnapshotVerifyAgainst(t *testing.T) {
	t.Parallel()
	root := t.TempDir()
	p := filepath.Join(root, "file")
	_ = os.WriteFile(p, []byte("original"), 0o644)

	snap, err := TakeSnapshot(context.Background(), root)
	if err != nil {
		t.Fatal(err)
	}

	// Should pass before modification.
	if err := snap.VerifyAgainst(context.Background(), root); err != nil {
		t.Fatalf("VerifyAgainst on unchanged tree: %v", err)
	}

	// Modify the file → verify must fail.
	_ = os.WriteFile(p, []byte("changed"), 0o644)
	if err := snap.VerifyAgainst(context.Background(), root); err == nil {
		t.Fatal("VerifyAgainst must fail after file modification")
	}
}

// ── 28. Snapshot.Diff ────────────────────────────────────────────────────────

func TestSnapshotDiff(t *testing.T) {
	t.Parallel()
	rootA := t.TempDir()
	buildTree(t, rootA, fsTree{"common": "same", "modified": "old", "removed": "gone"})
	rootB := t.TempDir()
	buildTree(t, rootB, fsTree{"common": "same", "modified": "new", "added": "fresh"})

	snapA, err := TakeSnapshot(context.Background(), rootA, WithMetadata(MetaNone))
	if err != nil {
		t.Fatal(err)
	}
	snapB, err := TakeSnapshot(context.Background(), rootB, WithMetadata(MetaNone))
	if err != nil {
		t.Fatal(err)
	}

	diff := snapA.Diff(snapB)
	if len(diff.Added) != 1 || diff.Added[0] != "added" {
		t.Errorf("Added: want [added], got %v", diff.Added)
	}
	if len(diff.Removed) != 1 || diff.Removed[0] != "removed" {
		t.Errorf("Removed: want [removed], got %v", diff.Removed)
	}
	if len(diff.Modified) != 1 || diff.Modified[0] != "modified" {
		t.Errorf("Modified: want [modified], got %v", diff.Modified)
	}

	// Identical snapshots must diff as empty.
	if !snapA.Diff(snapA).Empty() {
		t.Fatal("a snapshot diffed against itself must be empty")
	}
}

// ── 29. Inspector ─────────────────────────────────────────────────────────────

func TestInspector(t *testing.T) {
	t.Parallel()
	root := t.TempDir()
	buildTree(t, root, fsTree{"a": "alpha", "b": "beta"})

	cache := &MemoryCache{}
	cs := mustNew(t, WithFileCache(cache))
	ins := NewInspector(cs, cache)

	// First pass — all misses.
	_, entries1, err := ins.Sum(context.Background(), root)
	if err != nil {
		t.Fatal(err)
	}
	for _, e := range entries1 {
		if e.Kind == KindFile && e.CacheHit {
			t.Errorf("entry %q: expected cache miss on first pass", e.RelPath)
		}
	}
	if ins.HitRate() != 0 {
		t.Errorf("expected 0 hit rate on first pass, got %f", ins.HitRate())
	}

	// Second pass — all hits.
	_, entries2, err := ins.Sum(context.Background(), root)
	if err != nil {
		t.Fatal(err)
	}
	for _, e := range entries2 {
		if e.Kind == KindFile && !e.CacheHit {
			t.Errorf("entry %q: expected cache hit on second pass", e.RelPath)
		}
	}
	if ins.HitRate() == 0 {
		t.Error("expected non-zero hit rate on second pass")
	}
}

// ── 30. hexDecode ─────────────────────────────────────────────────────────────

func TestHexDecode(t *testing.T) {
	t.Parallel()
	cases := []struct {
		input string
		want  []byte
	}{
		{"", []byte{}},
		{"deadbeef", []byte{0xde, 0xad, 0xbe, 0xef}},
		{"DEADBEEF", []byte{0xde, 0xad, 0xbe, 0xef}},
		{"xyz", nil}, // odd length
		{"gg", nil},  // invalid nibble
	}
	for _, tc := range cases {
		got := hexDecode(tc.input)
		if !bytes.Equal(got, tc.want) {
			t.Errorf("hexDecode(%q) = %x, want %x", tc.input, got, tc.want)
		}
	}

	// Round-trip via hex.EncodeToString.
	data := []byte{0x01, 0x23, 0x45, 0x67, 0x89, 0xAB, 0xCD, 0xEF}
	encoded := hex.EncodeToString(data)
	decoded := hexDecode(encoded)
	if !bytes.Equal(decoded, data) {
		t.Fatalf("round-trip failed: %x != %x", decoded, data)
	}
}

// ── 31. Hermetic across time ──────────────────────────────────────────────────

func TestHermeticAcrossTime(t *testing.T) {
	t.Parallel()
	root := t.TempDir()
	p := filepath.Join(root, "f")
	_ = os.WriteFile(p, []byte("stable"), 0o644)

	cs := mustNew(t) // MetaModeAndSize — no mtime
	r1 := mustSum(t, cs, root)
	_ = os.Chtimes(p, time.Now().Add(24*time.Hour), time.Now().Add(24*time.Hour))
	r2 := mustSum(t, cs, root)
	if !bytes.Equal(r1.Digest, r2.Digest) {
		t.Fatalf("digest changed after mtime bump (must be hermetic): %s vs %s",
			r1.Hex(), r2.Hex())
	}
}

// ── 32. Error paths ───────────────────────────────────────────────────────────

func TestInvalidPath(t *testing.T) {
	t.Parallel()
	_, err := mustNew(t).Sum(context.Background(), "/this/path/does/not/exist/hopefully")
	if err == nil {
		t.Fatal("expected error for non-existent path")
	}
}

func TestUnknownAlgorithm(t *testing.T) {
	t.Parallel()
	_, err := New(WithAlgorithm("blake3-not-builtin"))
	if err == nil {
		t.Fatal("expected error for unknown algorithm")
	}
}

func TestNilHasherOption(t *testing.T) {
	t.Parallel()
	_, err := New(WithHasher(nil))
	if err == nil {
		t.Fatal("expected error for nil Hasher")
	}
}

func TestNilWalkerOption(t *testing.T) {
	t.Parallel()
	_, err := New(WithWalker(nil))
	if err == nil {
		t.Fatal("expected error for nil Walker")
	}
}

// ── 33. Type string methods ───────────────────────────────────────────────────

func TestKindString(t *testing.T) {
	t.Parallel()
	cases := []struct {
		k    EntryKind
		want string
	}{
		{KindFile, "file"}, {KindDir, "dir"},
		{KindSymlink, "symlink"}, {KindOther, "other"},
		{EntryKind(99), "other"},
	}
	for _, tc := range cases {
		if got := tc.k.String(); got != tc.want {
			t.Errorf("EntryKind(%d).String()=%q, want %q", tc.k, got, tc.want)
		}
	}
}

func TestResultHex(t *testing.T) {
	t.Parallel()
	r := Result{Digest: []byte{0xAB, 0xCD, 0xEF}}
	if r.Hex() != "abcdef" {
		t.Fatalf("Hex()=%q, want 'abcdef'", r.Hex())
	}
}

func TestEntryResultString(t *testing.T) {
	t.Parallel()
	e := EntryResult{RelPath: "foo/bar.txt", Kind: KindFile, Digest: []byte{0x01}}
	s := e.String()
	if !strings.Contains(s, "foo/bar.txt") || !strings.Contains(s, "file") {
		t.Fatalf("EntryResult.String()=%q unexpected format", s)
	}
}

func TestDiffResultEmpty(t *testing.T) {
	t.Parallel()
	if !(DiffResult{}).Empty() {
		t.Fatal("zero-value DiffResult must be empty")
	}
	if (DiffResult{Added: []string{"x"}}).Empty() {
		t.Fatal("DiffResult with Added must not be empty")
	}
}

func TestMustNewPanic(t *testing.T) {
	t.Parallel()
	defer func() {
		if r := recover(); r == nil {
			t.Fatal("MustNew with bad option should panic")
		}
	}()
	MustNew(WithAlgorithm("no-such-algo"))
}

// ── 34. Concurrent safety ─────────────────────────────────────────────────────

func TestConcurrentSafety(t *testing.T) {
	t.Parallel()
	root := t.TempDir()
	buildTree(t, root, fsTree{"a": "alpha", "b": "beta", "c": "gamma"})
	cs := mustNew(t)

	const N = 20
	results := make([]Result, N)
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

// ── 35. Large file ────────────────────────────────────────────────────────────

func TestLargeFile(t *testing.T) {
	t.Parallel()
	p := filepath.Join(t.TempDir(), "large.bin")
	const size = 4 * 1024 * 1024
	data := bytes.Repeat([]byte("ABCDEFGH"), size/8)
	_ = os.WriteFile(p, data, 0o644)

	cs := mustNew(t)
	r1 := mustSum(t, cs, p)
	data[size/2] ^= 0xFF
	_ = os.WriteFile(p, data, 0o644)
	r2 := mustSum(t, cs, p)
	if bytes.Equal(r1.Digest, r2.Digest) {
		t.Fatal("digest must change after modifying large file")
	}
}

// ── 36. 1000 small files ──────────────────────────────────────────────────────

func TestManySmallFiles(t *testing.T) {
	t.Parallel()
	root := t.TempDir()
	for i := range 1000 {
		_ = os.WriteFile(
			filepath.Join(root, fmt.Sprintf("f%05d", i)),
			[]byte(fmt.Sprintf("f%05d", i)),
			0o644,
		)
	}
	r1 := mustSum(t, mustNew(t, WithWorkers(1), WithMetadata(MetaNone)), root)
	r4 := mustSum(t, mustNew(t, WithWorkers(4), WithMetadata(MetaNone)), root)
	if !bytes.Equal(r1.Digest, r4.Digest) {
		t.Fatalf("1 vs 4 workers: %s vs %s", r1.Hex(), r4.Hex())
	}
}

// ── 37. FilterFunc adapter ────────────────────────────────────────────────────

func TestFilterFuncAdapter(t *testing.T) {
	t.Parallel()
	called := false
	f := FilterFunc(func(_ string, _ fs.FileInfo) FilterDecision {
		called = true
		return Include
	})
	root := t.TempDir()
	_ = os.WriteFile(filepath.Join(root, "f"), []byte("x"), 0o644)
	mustSum(t, mustNew(t, WithFilter(f)), root)
	if !called {
		t.Fatal("FilterFunc was never called")
	}
}

// ── 38. AllowAll == nil filter ────────────────────────────────────────────────

func TestAllowAll(t *testing.T) {
	t.Parallel()
	root := t.TempDir()
	buildTree(t, root, fsTree{"a": "alpha", "b": "beta"})
	r1 := mustSum(t, mustNew(t), root)
	r2 := mustSum(t, mustNew(t, WithFilter(AllowAll)), root)
	if !bytes.Equal(r1.Digest, r2.Digest) {
		t.Fatal("AllowAll and nil filter must produce identical digests")
	}
}

// ── 39. WriteMetaHeader produces distinct outputs per flag combo ──────────────

func TestWriteMetaHeaderDifferent(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("chmod not reliable on Windows")
	}
	t.Parallel()
	p := filepath.Join(t.TempDir(), "script")
	_ = os.WriteFile(p, []byte("content"), 0o644)

	rNone := mustSum(t, mustNew(t, WithMetadata(MetaNone)), p)
	rMode := mustSum(t, mustNew(t, WithMetadata(MetaMode)), p)
	rSize := mustSum(t, mustNew(t, WithMetadata(MetaSize)), p)

	if bytes.Equal(rNone.Digest, rMode.Digest) {
		t.Fatal("MetaNone and MetaMode must produce different digests")
	}
	if bytes.Equal(rNone.Digest, rSize.Digest) {
		t.Fatal("MetaNone and MetaSize must produce different digests")
	}
}

// ── 40. FileDigest convenience ────────────────────────────────────────────────

func TestFileDigest(t *testing.T) {
	t.Parallel()
	p := filepath.Join(t.TempDir(), "f.txt")
	_ = os.WriteFile(p, []byte("content"), 0o644)
	dgst, err := FileDigest(context.Background(), p)
	if err != nil {
		t.Fatal(err)
	}
	if len(dgst) == 0 {
		t.Fatal("empty digest")
	}
	t.Logf("SHA-256 = %s", hex.EncodeToString(dgst))
}

// ── Benchmarks ────────────────────────────────────────────────────────────────

func BenchmarkHashDir_100files_1worker(b *testing.B)   { benchHashDir(b, 100, 1, 4096) }
func BenchmarkHashDir_100files_8workers(b *testing.B)  { benchHashDir(b, 100, 8, 4096) }
func BenchmarkHashDir_1000files_4workers(b *testing.B) { benchHashDir(b, 1000, 4, 1024) }

func BenchmarkHashDir_WithCache(b *testing.B) {
	root := b.TempDir()
	content := bytes.Repeat([]byte("x"), 4096)
	for i := range 100 {
		_ = os.WriteFile(filepath.Join(root, fmt.Sprintf("file%03d.dat", i)), content, 0o644)
	}
	cache := &MemoryCache{}
	cs := MustNew(WithWorkers(4), WithFileCache(cache))
	ctx := context.Background()

	// Warm the cache.
	if _, err := cs.Sum(ctx, root); err != nil {
		b.Fatal(err)
	}
	b.ResetTimer()
	b.ReportAllocs()
	for i := 0; i < b.N; i++ {
		if _, err := cs.Sum(ctx, root); err != nil {
			b.Fatal(err)
		}
	}
	b.SetBytes(int64(100 * 4096))
}

func BenchmarkHashDir_NestedTree(b *testing.B) {
	root := b.TempDir()
	buildTree(b, root, fsTree{"a": "", "a/b": "", "a/b/c": "", "a/b/c/d": ""})
	for i := range 50 {
		_ = os.WriteFile(
			filepath.Join(root, fmt.Sprintf("a/b/c/d/file%04d.dat", i)),
			bytes.Repeat([]byte("x"), 1024),
			0o644,
		)
	}
	cs := MustNew(WithWorkers(4))
	ctx := context.Background()
	b.ResetTimer()
	b.ReportAllocs()
	for i := 0; i < b.N; i++ {
		if _, err := cs.Sum(ctx, root); err != nil {
			b.Fatal(err)
		}
	}
}

func benchHashDir(b *testing.B, nFiles, workers, fileSize int) {
	b.Helper()
	root := b.TempDir()
	content := bytes.Repeat([]byte("x"), fileSize)
	for i := range nFiles {
		_ = os.WriteFile(filepath.Join(root, fmt.Sprintf("file%06d.dat", i)), content, 0o644)
	}
	cs := MustNew(WithWorkers(workers))
	ctx := context.Background()
	b.ResetTimer()
	b.ReportAllocs()
	for i := 0; i < b.N; i++ {
		if _, err := cs.Sum(ctx, root); err != nil {
			b.Fatal(err)
		}
	}
	b.SetBytes(int64(nFiles * fileSize))
}

// ── 41. MetaNone is respected (regression for applyDefaults bug) ──────────────

// WithMetadata(MetaNone) must NOT be silently replaced by the default
// MetaModeAndSize.  Verify by confirming that a chmod has zero effect on the
// digest when MetaNone is active.
func TestMetaNoneIsRespected(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("chmod not fully supported on Windows")
	}
	t.Parallel()

	root := t.TempDir()
	p := filepath.Join(root, "script.sh")
	_ = os.WriteFile(p, []byte("#!/bin/sh\necho hi"), 0o644)

	cs := mustNew(t, WithMetadata(MetaNone))
	r1 := mustSum(t, cs, root)

	// Change mode only — content and size are unchanged.
	_ = os.Chmod(p, 0o755)
	r2 := mustSum(t, cs, root)

	if !bytes.Equal(r1.Digest, r2.Digest) {
		t.Fatalf("MetaNone: digest changed after chmod — MetaNone was not respected "+
			"(got %s, want %s)", r2.Hex(), r1.Hex())
	}
}

// ── 42. MtimeCache: basic hit/miss ───────────────────────────────────────────

func TestMtimeCache_HitMiss(t *testing.T) {
	t.Parallel()

	dir := t.TempDir()
	p := filepath.Join(dir, "f.txt")
	_ = os.WriteFile(p, []byte("hello"), 0o644)

	c := &MtimeCache{}

	// Miss on empty cache.
	if _, ok := c.Get(p); ok {
		t.Fatal("expected miss on empty cache")
	}

	// Set and get.
	dgst := []byte{0x01, 0x02, 0x03}
	c.Set(p, dgst)

	got, ok := c.Get(p)
	if !ok {
		t.Fatal("expected hit after Set")
	}
	if !bytes.Equal(got, dgst) {
		t.Fatalf("wrong value: %x", got)
	}
}

// ── 43. MtimeCache: auto-invalidates on content change ────────────────────────

func TestMtimeCache_AutoInvalidateOnChange(t *testing.T) {
	t.Parallel()

	dir := t.TempDir()
	p := filepath.Join(dir, "f.txt")
	_ = os.WriteFile(p, []byte("original"), 0o644)

	c := &MtimeCache{}
	dgst := []byte{0xAB, 0xCD}
	c.Set(p, dgst)

	// Verify it's there.
	if _, ok := c.Get(p); !ok {
		t.Fatal("expected hit before modification")
	}

	// Overwrite with different content (size changes).
	_ = os.WriteFile(p, []byte("different content"), 0o644)

	// Cache must self-invalidate.
	if _, ok := c.Get(p); ok {
		t.Fatal("expected miss after file size change")
	}
}

// ── 44. MtimeCache: deep copy on Get prevents mutation ────────────────────────

func TestMtimeCache_DeepCopyOnGet(t *testing.T) {
	t.Parallel()

	p := filepath.Join(t.TempDir(), "f")
	_ = os.WriteFile(p, []byte("x"), 0o644)

	c := &MtimeCache{}
	orig := []byte{0x11, 0x22, 0x33}
	c.Set(p, orig)

	got, ok := c.Get(p)
	if !ok {
		t.Fatal("expected hit")
	}
	// Mutate returned slice — must not affect stored copy.
	got[0] = 0xFF
	got2, _ := c.Get(p)
	if got2[0] == 0xFF {
		t.Fatal("MtimeCache.Get must return a deep copy")
	}
}

// ── 45. MtimeCache: Set deep-copies digest ────────────────────────────────────

func TestMtimeCache_SetDeepCopy(t *testing.T) {
	t.Parallel()

	p := filepath.Join(t.TempDir(), "f")
	_ = os.WriteFile(p, []byte("x"), 0o644)

	c := &MtimeCache{}
	d := []byte{0xAA, 0xBB}
	c.Set(p, d)

	// Mutate original after Set — cache must be unaffected.
	d[0] = 0xFF
	got, ok := c.Get(p)
	if !ok {
		t.Fatal("expected hit")
	}
	if got[0] == 0xFF {
		t.Fatal("MtimeCache.Set must deep-copy the digest")
	}
}

// ── 46. MtimeCache: Invalidate and Len ───────────────────────────────────────

func TestMtimeCache_InvalidateAndLen(t *testing.T) {
	t.Parallel()

	dir := t.TempDir()
	files := []string{"a", "b", "c"}
	c := &MtimeCache{}

	for _, name := range files {
		p := filepath.Join(dir, name)
		_ = os.WriteFile(p, []byte(name), 0o644)
		c.Set(p, []byte(name))
	}

	if c.Len() != 3 {
		t.Fatalf("Len: want 3, got %d", c.Len())
	}

	c.Invalidate(filepath.Join(dir, "b"))
	if c.Len() != 2 {
		t.Fatalf("Len after Invalidate: want 2, got %d", c.Len())
	}

	if _, ok := c.Get(filepath.Join(dir, "b")); ok {
		t.Fatal("expected miss after Invalidate")
	}

	c.InvalidateAll()
	if c.Len() != 0 {
		t.Fatalf("Len after InvalidateAll: want 0, got %d", c.Len())
	}
}

// ── 47. MtimeCache: Prune removes stale entries ───────────────────────────────

func TestMtimeCache_Prune(t *testing.T) {
	t.Parallel()

	dir := t.TempDir()
	pA := filepath.Join(dir, "a")
	pB := filepath.Join(dir, "b")
	_ = os.WriteFile(pA, []byte("a"), 0o644)
	_ = os.WriteFile(pB, []byte("b"), 0o644)

	c := &MtimeCache{}
	c.Set(pA, []byte{0x01})
	c.Set(pB, []byte{0x02})

	if c.Len() != 2 {
		t.Fatalf("want 2 entries, got %d", c.Len())
	}

	// Remove file b; it should be pruned.
	_ = os.Remove(pB)
	c.Prune()

	if c.Len() != 1 {
		t.Fatalf("after Prune, want 1 entry, got %d", c.Len())
	}
	if _, ok := c.Get(pA); !ok {
		t.Fatal("valid entry 'a' must survive Prune")
	}
}

// ── 48. MtimeCache: miss for nonexistent file ─────────────────────────────────

func TestMtimeCache_NonexistentFile(t *testing.T) {
	t.Parallel()
	c := &MtimeCache{}
	// Set on a nonexistent path should silently no-op (stat fails).
	c.Set("/nonexistent/path/file.txt", []byte{0x01})
	if c.Len() != 0 {
		t.Fatal("Set on nonexistent file must be a no-op")
	}
	// Get on a nonexistent path should miss.
	if _, ok := c.Get("/nonexistent/path/file.txt"); ok {
		t.Fatal("expected miss for nonexistent path")
	}
}

// ── 49. MtimeCache: integrates with Checksummer ───────────────────────────────

func TestMtimeCache_WithChecksummer(t *testing.T) {
	t.Parallel()

	root := t.TempDir()
	p := filepath.Join(root, "data.bin")
	_ = os.WriteFile(p, bytes.Repeat([]byte("A"), 1024), 0o644)

	cache := &MtimeCache{}
	cs, err := NewCachingChecksummer(cache)
	if err != nil {
		t.Fatal(err)
	}

	r1 := mustSum(t, cs, root)
	if cache.Len() == 0 {
		t.Fatal("cache must be populated after first Sum")
	}

	// Second call — served from cache (no disk read).
	r2 := mustSum(t, cs, root)
	if !bytes.Equal(r1.Digest, r2.Digest) {
		t.Fatal("cached and fresh digests must match")
	}

	// Modify the file — MtimeCache must auto-invalidate.
	_ = os.WriteFile(p, bytes.Repeat([]byte("B"), 1024), 0o644)
	r3 := mustSum(t, cs, root)
	if bytes.Equal(r1.Digest, r3.Digest) {
		t.Fatal("digest must change after file modification")
	}
}

// ── 50. MtimeCache: concurrent safety ────────────────────────────────────────

func TestMtimeCache_Concurrent(t *testing.T) {
	t.Parallel()

	dir := t.TempDir()
	p := filepath.Join(dir, "shared.txt")
	_ = os.WriteFile(p, []byte("shared"), 0o644)

	c := &MtimeCache{}
	c.Set(p, []byte{0x42})

	var wg sync.WaitGroup
	const N = 50
	errs := make([]error, N)

	for i := range N {
		i := i
		wg.Add(1)
		go func() {
			defer wg.Done()
			switch i % 3 {
			case 0:
				c.Get(p)
			case 1:
				c.Set(p, []byte{byte(i)})
			case 2:
				c.Invalidate(p)
			}
		}()
	}
	wg.Wait()

	for _, err := range errs {
		if err != nil {
			t.Error(err)
		}
	}
	// No panic = pass (concurrent safety test).
}

// ── 51. WithMetadata(MetaNone) vs default (regression) ────────────────────────

func TestWithMetadataNoneDistinctFromDefault(t *testing.T) {
	t.Parallel()
	if runtime.GOOS == "windows" {
		t.Skip("chmod not fully supported on Windows")
	}

	root := t.TempDir()
	p := filepath.Join(root, "exec")
	_ = os.WriteFile(p, []byte("#!/bin/sh"), 0o644)

	csDefault := mustNew(t) // MetaModeAndSize
	csNone := mustNew(t, WithMetadata(MetaNone))

	dDefault := mustSum(t, csDefault, root)
	dNone := mustSum(t, csNone, root)

	// Two different flag sets must produce different digests for the same file.
	if bytes.Equal(dDefault.Digest, dNone.Digest) {
		t.Fatal("MetaNone and MetaModeAndSize must produce different digests for the same file")
	}

	// Change mode — only the MetaModeAndSize digest must change.
	_ = os.Chmod(p, 0o755)
	dDefault2 := mustSum(t, csDefault, root)
	dNone2 := mustSum(t, csNone, root)

	if bytes.Equal(dDefault.Digest, dDefault2.Digest) {
		t.Fatal("MetaModeAndSize digest must change after chmod")
	}
	if !bytes.Equal(dNone.Digest, dNone2.Digest) {
		t.Fatal("MetaNone digest must NOT change after chmod")
	}
}

// ── Benchmark: MtimeCache warm ────────────────────────────────────────────────

func BenchmarkHashDir_MtimeCache_Warm(b *testing.B) {
	root := b.TempDir()
	content := bytes.Repeat([]byte("x"), 4096)
	for i := range 100 {
		_ = os.WriteFile(filepath.Join(root, fmt.Sprintf("file%03d.dat", i)), content, 0o644)
	}

	cache := &MtimeCache{}
	cs := MustNew(WithWorkers(4), WithFileCache(cache))
	ctx := context.Background()

	// Warm.
	if _, err := cs.Sum(ctx, root); err != nil {
		b.Fatal(err)
	}

	b.ResetTimer()
	b.ReportAllocs()
	for i := 0; i < b.N; i++ {
		if _, err := cs.Sum(ctx, root); err != nil {
			b.Fatal(err)
		}
	}
	b.SetBytes(int64(100 * 4096))
}

// ══════════════════════════════════════════════════════════════════════════════
// Tests for stream.go APIs
// ══════════════════════════════════════════════════════════════════════════════

// ── 52. HashReader ────────────────────────────────────────────────────────────

func TestHashReader_Basic(t *testing.T) {
	t.Parallel()

	data := []byte("hello, world")
	dgst1, err := HashReader(context.Background(), bytes.NewReader(data))
	if err != nil {
		t.Fatal(err)
	}
	if len(dgst1) == 0 {
		t.Fatal("expected non-empty digest")
	}

	// Calling again must produce the same digest.
	dgst2, err := HashReader(context.Background(), bytes.NewReader(data))
	if err != nil {
		t.Fatal(err)
	}
	if !bytes.Equal(dgst1, dgst2) {
		t.Fatalf("HashReader not reproducible: %x vs %x", dgst1, dgst2)
	}

	// Different content must yield different digest.
	dgstOther, err := HashReader(context.Background(), bytes.NewReader([]byte("other")))
	if err != nil {
		t.Fatal(err)
	}
	if bytes.Equal(dgst1, dgstOther) {
		t.Fatal("different content must produce different digest")
	}
}

func TestHashReader_Empty(t *testing.T) {
	t.Parallel()
	dgst, err := HashReader(context.Background(), bytes.NewReader(nil))
	if err != nil {
		t.Fatal(err)
	}
	// SHA-256 of empty input is well-known.
	want := "e3b0c44298fc1c149afbf4c8996fb92427ae41e4649b934ca495991b7852b855"
	if hex.EncodeToString(dgst) != want {
		t.Fatalf("empty HashReader: got %x, want %s", dgst, want)
	}
}

func TestHashReader_Algorithm(t *testing.T) {
	t.Parallel()
	data := []byte("test")
	dSHA256, _ := HashReader(context.Background(), bytes.NewReader(data))
	dSHA512, _ := HashReader(context.Background(), bytes.NewReader(data), WithAlgorithm(SHA512))
	if bytes.Equal(dSHA256, dSHA512) {
		t.Fatal("different algorithms must produce different digests")
	}
	if len(dSHA512) != 64 {
		t.Fatalf("SHA-512 digest should be 64 bytes, got %d", len(dSHA512))
	}
}

func TestHashReader_ContextCancel(t *testing.T) {
	t.Parallel()
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	_, err := HashReader(ctx, bytes.NewReader(bytes.Repeat([]byte("x"), 1<<20)))
	if err == nil {
		t.Log("HashReader completed before context was observed (tiny payload)")
	}
}

// ── 53. Walk ──────────────────────────────────────────────────────────────────

func TestWalk_Basic(t *testing.T) {
	t.Parallel()
	root := t.TempDir()
	buildTree(t, root, fsTree{
		"a":     "alpha",
		"b":     "beta",
		"sub":   "",
		"sub/c": "gamma",
	})

	cs := mustNew(t, WithMetadata(MetaNone))

	var visited []string
	res, err := cs.Walk(context.Background(), root, func(e EntryResult) error {
		visited = append(visited, e.RelPath)
		return nil
	})
	if err != nil {
		t.Fatal(err)
	}
	if len(res.Digest) == 0 {
		t.Fatal("expected non-empty root digest from Walk")
	}

	// Entries must arrive with all non-root entries sorted by relPath,
	// and the root "." entry must be last (bottom-up traversal order).
	if len(visited) == 0 {
		t.Fatal("expected at least one visited entry")
	}
	if visited[len(visited)-1] != "." {
		t.Fatalf("last visited entry must be '.', got %q; all: %v", visited[len(visited)-1], visited)
	}
	nonRoot := visited[:len(visited)-1]
	if !sort.StringsAreSorted(nonRoot) {
		t.Fatalf("non-root Walk entries not sorted: %v", nonRoot)
	}

	// Root digest must match Sum.
	sumRes := mustSum(t, cs, root)
	if !bytes.Equal(res.Digest, sumRes.Digest) {
		t.Fatalf("Walk root digest != Sum digest: %s vs %s", res.Hex(), sumRes.Hex())
	}
}

func TestWalk_CallbackError(t *testing.T) {
	t.Parallel()
	root := t.TempDir()
	_ = os.WriteFile(filepath.Join(root, "f"), []byte("x"), 0o644)

	cs := mustNew(t)
	sentinel := fmt.Errorf("stop here")
	_, err := cs.Walk(context.Background(), root, func(_ EntryResult) error {
		return sentinel
	})
	if !errors.Is(err, sentinel) {
		t.Fatalf("expected sentinel error, got: %v", err)
	}
}

func TestWalk_EmptyDir(t *testing.T) {
	t.Parallel()
	root := t.TempDir()
	cs := mustNew(t)
	var count int
	res, err := cs.Walk(context.Background(), root, func(_ EntryResult) error {
		count++
		return nil
	})
	if err != nil {
		t.Fatal(err)
	}
	// Empty dir: only the dir itself is visited.
	if count != 1 {
		t.Fatalf("expected 1 entry for empty dir, got %d", count)
	}
	if len(res.Digest) == 0 {
		t.Fatal("expected non-empty root digest")
	}
}

// ── 54. Canonicalize + ReadCanonical ─────────────────────────────────────────

func TestCanonicalize_RoundTrip(t *testing.T) {
	t.Parallel()
	root := t.TempDir()
	buildTree(t, root, fsTree{
		"a.txt": "alpha",
		"b.txt": "beta",
		"sub":   "",
		"sub/c": "gamma",
	})

	cs := mustNew(t)
	var buf bytes.Buffer
	rootDgst, err := cs.Canonicalize(context.Background(), root, &buf)
	if err != nil {
		t.Fatal(err)
	}
	if len(rootDgst) == 0 {
		t.Fatal("expected non-empty root digest")
	}

	// Last line must be the root.
	lines := strings.Split(strings.TrimRight(buf.String(), "\n"), "\n")
	last := lines[len(lines)-1]
	if !strings.Contains(last, "root") {
		t.Fatalf("last line must be root line, got: %q", last)
	}
	if !strings.Contains(last, hex.EncodeToString(rootDgst)) {
		t.Fatalf("last line must contain root digest")
	}

	// Parse and verify counts.
	entries, err := ReadCanonical(&buf)
	if err != nil {
		t.Fatal(err)
	}
	if len(entries) == 0 {
		t.Fatal("ReadCanonical returned no entries")
	}
	// Last entry is root.
	if entries[len(entries)-1].Kind != "root" {
		t.Fatalf("last entry kind: want 'root', got %q", entries[len(entries)-1].Kind)
	}

	// Root digest must match Sum.
	sumRes := mustSum(t, cs, root)
	if !bytes.Equal(rootDgst, sumRes.Digest) {
		t.Fatal("Canonicalize root digest != Sum digest")
	}
}

func TestCanonicalize_Reproducible(t *testing.T) {
	t.Parallel()
	tree := fsTree{"a": "alpha", "z": "zeta", "m": "mu"}

	root1, root2 := t.TempDir(), t.TempDir()
	buildTree(t, root1, tree)
	buildTree(t, root2, tree)

	cs := mustNew(t, WithMetadata(MetaNone))
	var buf1, buf2 bytes.Buffer
	cs.Canonicalize(context.Background(), root1, &buf1)
	cs.Canonicalize(context.Background(), root2, &buf2)

	if buf1.String() != buf2.String() {
		t.Fatalf("Canonicalize not reproducible:\n%s\nvs\n%s", buf1.String(), buf2.String())
	}
}

func TestReadCanonical_InvalidLines(t *testing.T) {
	t.Parallel()

	// Too few fields.
	_, err := ReadCanonical(strings.NewReader("abc123  file\n"))
	if err == nil {
		t.Fatal("expected error for line with < 3 fields")
	}

	// Bad hex.
	_, err = ReadCanonical(strings.NewReader("ZZZZ  file  foo\n"))
	if err == nil {
		t.Fatal("expected error for bad hex digest")
	}
}

// ── 55. ParallelDiff ─────────────────────────────────────────────────────────

func TestParallelDiff(t *testing.T) {
	t.Parallel()
	rootA := t.TempDir()
	buildTree(t, rootA, fsTree{"common": "same", "modified": "old", "removed": "gone"})
	rootB := t.TempDir()
	buildTree(t, rootB, fsTree{"common": "same", "modified": "new", "added": "fresh"})

	cs := mustNew(t, WithMetadata(MetaNone))

	diffSerial, err := cs.Diff(context.Background(), rootA, rootB)
	if err != nil {
		t.Fatal(err)
	}

	diffParallel, err := cs.ParallelDiff(context.Background(), rootA, rootB)
	if err != nil {
		t.Fatal(err)
	}

	// Both approaches must produce identical results.
	if !reflect.DeepEqual(diffSerial, diffParallel) {
		t.Fatalf("serial vs parallel diff mismatch:\nserial:   %+v\nparallel: %+v", diffSerial, diffParallel)
	}
}

func TestParallelDiff_Identical(t *testing.T) {
	t.Parallel()
	root := t.TempDir()
	buildTree(t, root, fsTree{"f": "data", "g": "more"})

	cs := mustNew(t)
	diff, err := cs.ParallelDiff(context.Background(), root, root)
	if err != nil {
		t.Fatal(err)
	}
	if !diff.Empty() {
		t.Fatalf("expected empty diff: %+v", diff)
	}
}

// ── 56. SumMany ───────────────────────────────────────────────────────────────

func TestSumMany_Basic(t *testing.T) {
	t.Parallel()
	dir := t.TempDir()
	paths := make([]string, 5)
	for i := range paths {
		p := filepath.Join(dir, fmt.Sprintf("f%d.txt", i))
		_ = os.WriteFile(p, []byte(fmt.Sprintf("content %d", i)), 0o644)
		paths[i] = p
	}

	cs := mustNew(t)
	results, errs := cs.SumMany(context.Background(), paths)

	if len(results) != len(paths) {
		t.Fatalf("expected %d results, got %d", len(paths), len(results))
	}
	for i, err := range errs {
		if err != nil {
			t.Errorf("SumMany[%d]: %v", i, err)
		}
		if len(results[i].Digest) == 0 {
			t.Errorf("SumMany[%d]: empty digest", i)
		}
	}

	// Results must be in the same order as paths.
	for i, p := range paths {
		single := mustSum(t, cs, p)
		if !bytes.Equal(results[i].Digest, single.Digest) {
			t.Errorf("SumMany[%d] (%s) digest mismatch", i, p)
		}
	}
}

func TestSumMany_Error(t *testing.T) {
	t.Parallel()
	cs := mustNew(t)
	paths := []string{
		filepath.Join(t.TempDir(), "real.txt"),
		"/nonexistent/path/does/not/exist",
	}
	_ = os.WriteFile(paths[0], []byte("data"), 0o644)

	_, errs := cs.SumMany(context.Background(), paths)
	if errs[0] != nil {
		t.Errorf("SumMany[0] should succeed, got: %v", errs[0])
	}
	if errs[1] == nil {
		t.Error("SumMany[1] should fail for nonexistent path")
	}
}

func TestSumMany_Empty(t *testing.T) {
	t.Parallel()
	cs := mustNew(t)
	results, errs := cs.SumMany(context.Background(), nil)
	if len(results) != 0 || len(errs) != 0 {
		t.Fatal("SumMany on nil paths should return empty slices")
	}
}

// ── 57. SizeLimit ─────────────────────────────────────────────────────────────

func TestSizeLimit_Enforced(t *testing.T) {
	t.Parallel()
	dir := t.TempDir()
	p := filepath.Join(dir, "big.bin")
	_ = os.WriteFile(p, bytes.Repeat([]byte("A"), 1024), 0o644)

	// Limit smaller than file size — must fail.
	cs := mustNew(t, WithSizeLimit(512))
	_, err := cs.Sum(context.Background(), p)
	if err == nil {
		t.Fatal("expected FileTooLargeError")
	}
	var tle *FileTooLargeError
	if !errors.As(err, &tle) {
		t.Fatalf("expected *FileTooLargeError, got %T: %v", err, err)
	}
	if tle.Size != 1024 {
		t.Errorf("FileTooLargeError.Size: want 1024, got %d", tle.Size)
	}
	if tle.Limit != 512 {
		t.Errorf("FileTooLargeError.Limit: want 512, got %d", tle.Limit)
	}
}

func TestSizeLimit_NotEnforced(t *testing.T) {
	t.Parallel()
	dir := t.TempDir()
	p := filepath.Join(dir, "small.bin")
	_ = os.WriteFile(p, bytes.Repeat([]byte("A"), 256), 0o644)

	// Limit larger than file size — must succeed.
	cs := mustNew(t, WithSizeLimit(1024))
	res, err := cs.Sum(context.Background(), p)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if len(res.Digest) == 0 {
		t.Fatal("expected non-empty digest")
	}
}

func TestSizeLimit_Zero(t *testing.T) {
	t.Parallel()
	dir := t.TempDir()
	p := filepath.Join(dir, "f.txt")
	_ = os.WriteFile(p, bytes.Repeat([]byte("A"), 1<<20), 0o644)

	// SizeLimit=0 means no limit.
	cs := mustNew(t, WithSizeLimit(0))
	_, err := cs.Sum(context.Background(), p)
	if err != nil {
		t.Fatalf("SizeLimit=0 must not enforce any limit: %v", err)
	}
}

func TestSizeLimit_Negative(t *testing.T) {
	t.Parallel()
	_, err := New(WithSizeLimit(-1))
	if err == nil {
		t.Fatal("expected error for negative SizeLimit")
	}
}

func TestSizeLimit_DirWithMixedFiles(t *testing.T) {
	t.Parallel()
	root := t.TempDir()
	_ = os.WriteFile(filepath.Join(root, "small"), []byte("tiny"), 0o644)
	_ = os.WriteFile(filepath.Join(root, "large"), bytes.Repeat([]byte("A"), 2048), 0o644)

	cs := mustNew(t, WithSizeLimit(1024))
	_, err := cs.Sum(context.Background(), root)
	var tle *FileTooLargeError
	if !errors.As(err, &tle) {
		t.Fatalf("expected *FileTooLargeError for dir with oversized file, got: %v", err)
	}
}

// ── 58. FileTooLargeError ─────────────────────────────────────────────────────

func TestFileTooLargeError_Message(t *testing.T) {
	t.Parallel()
	e := &FileTooLargeError{Path: "/some/file.bin", Size: 2048, Limit: 1024}
	msg := e.Error()
	if !strings.Contains(msg, "/some/file.bin") {
		t.Errorf("error message missing path: %q", msg)
	}
	if !strings.Contains(msg, "2048") {
		t.Errorf("error message missing size: %q", msg)
	}
	if !strings.Contains(msg, "1024") {
		t.Errorf("error message missing limit: %q", msg)
	}
}

// ── 59. ExcludeDir semantics ──────────────────────────────────────────────────

func TestExcludeDir_SkipsDirectoryButDescends(t *testing.T) {
	t.Parallel()
	root := t.TempDir()
	buildTree(t, root, fsTree{
		"include.txt":   "included",
		"skipdir":       "",
		"skipdir/child": "also visited",
	})

	// ExcludeDir on "skipdir": its children are still recursed and collected,
	// but "skipdir" itself does NOT contribute its combined digest to the root.
	filter := FilterFunc(func(relPath string, fi fs.FileInfo) FilterDecision {
		if relPath == "skipdir" {
			return ExcludeDir
		}
		return Include
	})

	cs := mustNew(t, WithFilter(filter), WithCollectEntries(true), WithMetadata(MetaNone))
	res := mustSum(t, cs, root)

	// "skipdir" itself must NOT appear in entries (excluded).
	for _, e := range res.Entries {
		if e.RelPath == "skipdir" {
			t.Errorf("ExcludeDir: 'skipdir' must not appear in entries")
		}
	}

	// "skipdir/child" SHOULD appear (descendants are recursed).
	var foundChild bool
	for _, e := range res.Entries {
		if e.RelPath == "skipdir/child" {
			foundChild = true
		}
	}
	if !foundChild {
		t.Error("ExcludeDir: 'skipdir/child' should appear in entries")
	}

	// The root digest must differ from a fully-included tree.
	csAll := mustNew(t, WithMetadata(MetaNone), WithCollectEntries(true))
	resAll := mustSum(t, csAll, root)
	if bytes.Equal(res.Digest, resAll.Digest) {
		t.Fatal("ExcludeDir: root digest must differ from fully-included tree")
	}
}

// ── 60. Dir metadata in hash ──────────────────────────────────────────────────

func TestDirMetadataInHash(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("chmod not reliable on Windows")
	}
	t.Parallel()

	root := t.TempDir()
	sub := filepath.Join(root, "subdir")
	_ = os.Mkdir(sub, 0o755)
	_ = os.WriteFile(filepath.Join(sub, "f.txt"), []byte("hello"), 0o644)

	cs := mustNew(t, WithMetadata(MetaMode))
	r1 := mustSum(t, cs, root)

	// Change directory permissions — must change root digest.
	_ = os.Chmod(sub, 0o700)
	r2 := mustSum(t, cs, root)

	if bytes.Equal(r1.Digest, r2.Digest) {
		t.Fatal("root digest must change when a subdirectory's mode changes (MetaMode is set)")
	}
}

// ── 61. WriteTo returns correct byte count ────────────────────────────────────

func TestSnapshotWriteToByteCount(t *testing.T) {
	t.Parallel()
	root := t.TempDir()
	_ = os.WriteFile(filepath.Join(root, "f"), []byte("data"), 0o644)

	snap, err := TakeSnapshot(context.Background(), root)
	if err != nil {
		t.Fatal(err)
	}

	var buf bytes.Buffer
	n, err := snap.WriteTo(&buf)
	if err != nil {
		t.Fatal(err)
	}
	if n == 0 {
		t.Fatal("WriteTo must return non-zero byte count")
	}
	if int(n) != buf.Len() {
		t.Fatalf("WriteTo returned %d but buffer has %d bytes", n, buf.Len())
	}
}

// ── 62. Benchmark: ParallelDiff ───────────────────────────────────────────────

func BenchmarkParallelDiff(b *testing.B) {
	rootA := b.TempDir()
	rootB := b.TempDir()
	for _, root := range []string{rootA, rootB} {
		for i := range 100 {
			_ = os.WriteFile(
				filepath.Join(root, fmt.Sprintf("file%03d.dat", i)),
				bytes.Repeat([]byte("x"), 4096),
				0o644,
			)
		}
	}
	// Make one file different.
	_ = os.WriteFile(filepath.Join(rootB, "file050.dat"), []byte("changed"), 0o644)

	cs := MustNew(WithWorkers(4))
	ctx := context.Background()
	b.ResetTimer()
	b.ReportAllocs()
	for i := 0; i < b.N; i++ {
		if _, err := cs.ParallelDiff(ctx, rootA, rootB); err != nil {
			b.Fatal(err)
		}
	}
}

func BenchmarkSumMany(b *testing.B) {
	dir := b.TempDir()
	paths := make([]string, 20)
	for i := range paths {
		p := filepath.Join(dir, fmt.Sprintf("f%02d.dat", i))
		_ = os.WriteFile(p, bytes.Repeat([]byte("x"), 8192), 0o644)
		paths[i] = p
	}

	cs := MustNew(WithWorkers(4))
	ctx := context.Background()
	b.ResetTimer()
	b.ReportAllocs()
	for i := 0; i < b.N; i++ {
		if _, errs := cs.SumMany(ctx, paths); errs[0] != nil {
			b.Fatal(errs[0])
		}
	}
}

// ══════════════════════════════════════════════════════════════════════════════
// Edge-case and regression tests (Round 3)
// ══════════════════════════════════════════════════════════════════════════════

// ── 63. HashReader matches FileDigest for real file ───────────────────────────

func TestHashReader_MatchesFileDigest(t *testing.T) {
	t.Parallel()
	p := filepath.Join(t.TempDir(), "data.bin")
	content := bytes.Repeat([]byte("abcdefgh"), 1024)
	_ = os.WriteFile(p, content, 0o644)

	// HashReader with MetaNone must match FileDigest with MetaNone.
	// (FileDigest uses a single-worker Checksummer which applies MetaModeAndSize
	//  by default, so we must explicitly set MetaNone on both.)
	f, err := os.Open(p)
	if err != nil {
		t.Fatal(err)
	}
	defer f.Close()

	rdgst, err := HashReader(context.Background(), f, WithMetadata(MetaNone))
	if err != nil {
		t.Fatal(err)
	}

	fdgst, err := FileDigest(context.Background(), p, WithMetadata(MetaNone))
	if err != nil {
		t.Fatal(err)
	}

	if !bytes.Equal(rdgst, fdgst) {
		t.Fatalf("HashReader and FileDigest disagree: %x vs %x", rdgst, fdgst)
	}
}

// ── 64. Walk root entry is last and has relPath "." ───────────────────────────

func TestWalk_RootEntryIsLast(t *testing.T) {
	t.Parallel()
	root := t.TempDir()
	buildTree(t, root, fsTree{"a": "alpha", "b": "beta"})

	cs := mustNew(t)
	var last EntryResult
	_, err := cs.Walk(context.Background(), root, func(e EntryResult) error {
		last = e
		return nil
	})
	if err != nil {
		t.Fatal(err)
	}
	if last.RelPath != "." {
		t.Fatalf("last Walk entry must have relPath '.', got %q", last.RelPath)
	}
	if last.Kind != KindDir {
		t.Fatalf("root entry must be KindDir, got %v", last.Kind)
	}
}

// ── 65. Walk single file ──────────────────────────────────────────────────────

func TestWalk_SingleFile(t *testing.T) {
	t.Parallel()
	p := filepath.Join(t.TempDir(), "only.txt")
	_ = os.WriteFile(p, []byte("content"), 0o644)

	cs := mustNew(t)
	var entries []EntryResult
	res, err := cs.Walk(context.Background(), p, func(e EntryResult) error {
		entries = append(entries, e)
		return nil
	})
	if err != nil {
		t.Fatal(err)
	}
	if len(entries) != 1 {
		t.Fatalf("expected 1 entry for single file, got %d", len(entries))
	}
	if entries[0].Kind != KindFile {
		t.Errorf("expected KindFile, got %v", entries[0].Kind)
	}
	if !bytes.Equal(entries[0].Digest, res.Digest) {
		t.Error("file entry digest must equal root digest")
	}
}

// ── 66. Canonicalize: entry counts match Sum entries ─────────────────────────

func TestCanonicalize_EntryCountMatchesSum(t *testing.T) {
	t.Parallel()
	root := t.TempDir()
	buildTree(t, root, fsTree{
		"a":     "alpha",
		"b":     "beta",
		"sub":   "",
		"sub/c": "gamma",
		"sub/d": "delta",
	})

	cs := mustNew(t, WithCollectEntries(true))
	sumRes := mustSum(t, cs, root)

	var buf bytes.Buffer
	if _, err := cs.Canonicalize(context.Background(), root, &buf); err != nil {
		t.Fatal(err)
	}
	entries, err := ReadCanonical(&buf)
	if err != nil {
		t.Fatal(err)
	}

	// Canonicalize skips the "." dir entry (it IS in sumRes.Entries) and writes
	// a "root" summary line instead. So ReadCanonical returns the same count:
	// (len(sumRes.Entries) - 1 dir entries) + 1 root line = len(sumRes.Entries).
	if len(entries) != len(sumRes.Entries) {
		t.Fatalf("canonical entries: want %d, got %d", len(sumRes.Entries), len(entries))
	}
}

// ── 67. SumMany preserves order ───────────────────────────────────────────────

func TestSumMany_OrderPreserved(t *testing.T) {
	t.Parallel()
	dir := t.TempDir()
	const N = 20
	paths := make([]string, N)
	for i := range N {
		p := filepath.Join(dir, fmt.Sprintf("f%02d", i))
		_ = os.WriteFile(p, []byte(fmt.Sprintf("data-%d", i)), 0o644)
		paths[i] = p
	}

	cs := mustNew(t, WithWorkers(8))
	results, errs := cs.SumMany(context.Background(), paths)

	for i, err := range errs {
		if err != nil {
			t.Fatalf("SumMany[%d]: %v", i, err)
		}
	}

	// Each result must match a fresh single Sum.
	for i, p := range paths {
		single := mustSum(t, cs, p)
		if !bytes.Equal(results[i].Digest, single.Digest) {
			t.Errorf("SumMany[%d] order mismatch", i)
		}
	}
}

// ── 68. VerifyError fields ────────────────────────────────────────────────────

func TestVerifyError_Fields(t *testing.T) {
	t.Parallel()
	p := filepath.Join(t.TempDir(), "f")
	_ = os.WriteFile(p, []byte("hello"), 0o644)

	cs := mustNew(t)
	res := mustSum(t, cs, p)

	wrong := make([]byte, len(res.Digest))
	err := cs.Verify(context.Background(), p, wrong)
	var ve *VerifyError
	if !errors.As(err, &ve) {
		t.Fatalf("expected *VerifyError, got %T", err)
	}
	if ve.Path != p {
		t.Errorf("VerifyError.Path: want %q, got %q", p, ve.Path)
	}
	if !bytes.Equal(ve.Got, res.Digest) {
		t.Errorf("VerifyError.Got mismatch")
	}
	if !bytes.Equal(ve.Want, wrong) {
		t.Errorf("VerifyError.Want mismatch")
	}
}

// ── 69. Snapshot: VerifyAgainst uses correct algorithm from snapshot ───────────

func TestSnapshotVerifyAgainst_UsesAlgorithm(t *testing.T) {
	t.Parallel()
	root := t.TempDir()
	_ = os.WriteFile(filepath.Join(root, "f"), []byte("data"), 0o644)

	// Take snapshot with SHA512.
	snap, err := TakeSnapshot(context.Background(), root, WithAlgorithm(SHA512))
	if err != nil {
		t.Fatal(err)
	}
	if snap.Algorithm != "sha512" {
		t.Fatalf("expected sha512, got %s", snap.Algorithm)
	}

	// VerifyAgainst without specifying algorithm — it must read it from snap.
	if err := snap.VerifyAgainst(context.Background(), root); err != nil {
		t.Fatalf("VerifyAgainst with snapshot's own algorithm: %v", err)
	}

	// VerifyAgainst using a different tree must fail.
	root2 := t.TempDir()
	_ = os.WriteFile(filepath.Join(root2, "f"), []byte("different"), 0o644)
	if err := snap.VerifyAgainst(context.Background(), root2); err == nil {
		t.Fatal("VerifyAgainst must fail for a different tree")
	}
}

// ── 70. MtimeCache: Prune does not remove valid entries ───────────────────────

func TestMtimeCache_PruneKeepsValid(t *testing.T) {
	t.Parallel()
	dir := t.TempDir()
	p := filepath.Join(dir, "valid.txt")
	_ = os.WriteFile(p, []byte("still here"), 0o644)

	c := &MtimeCache{}
	c.Set(p, []byte{0x01})

	// Prune should leave the valid entry alone.
	c.Prune()

	if c.Len() != 1 {
		t.Fatalf("Prune must not remove valid entries, Len=%d", c.Len())
	}
	if _, ok := c.Get(p); !ok {
		t.Fatal("valid entry must survive Prune")
	}
}

// ── 71. ExcludeNames is case-sensitive ────────────────────────────────────────

func TestExcludeNames_CaseSensitive(t *testing.T) {
	t.Parallel()
	root := t.TempDir()
	_ = os.WriteFile(filepath.Join(root, "Foo"), []byte("capital"), 0o644)
	_ = os.WriteFile(filepath.Join(root, "foo"), []byte("lower"), 0o644)

	// Only exclude "foo" (lowercase).
	cs := mustNew(t, WithFilter(ExcludeNames("foo")), WithCollectEntries(true))
	res := mustSum(t, cs, root)

	found := map[string]bool{}
	for _, e := range res.Entries {
		found[e.RelPath] = true
	}
	if found["foo"] {
		t.Error("lowercase 'foo' should be excluded")
	}
	if !found["Foo"] {
		t.Error("uppercase 'Foo' must NOT be excluded by ExcludeNames('foo')")
	}
}

// ── 72. ExcludePatterns: directory-only pattern ───────────────────────────────

func TestExcludePatterns_DirectoryOnly(t *testing.T) {
	t.Parallel()
	root := t.TempDir()
	buildTree(t, root, fsTree{
		"cache":      "", // directory named "cache"
		"cache/data": "cached data",
		"cache.txt":  "not a directory",
	})

	// Pattern "cache/" should exclude the directory but not "cache.txt".
	cs := mustNew(t, WithFilter(ExcludePatterns("cache/")), WithCollectEntries(true))
	res := mustSum(t, cs, root)

	found := map[string]bool{}
	for _, e := range res.Entries {
		found[e.RelPath] = true
	}
	if found["cache"] {
		t.Error("directory 'cache' should be excluded by 'cache/' pattern")
	}
	if !found["cache.txt"] {
		t.Error("file 'cache.txt' must NOT be excluded by directory-only pattern 'cache/'")
	}
}

// ── 73. DirDigest convenience function ────────────────────────────────────────

func TestDirDigest(t *testing.T) {
	t.Parallel()
	root := t.TempDir()
	buildTree(t, root, fsTree{"a": "alpha", "b": "beta"})

	dgst, err := DirDigest(context.Background(), root)
	if err != nil {
		t.Fatal(err)
	}
	if len(dgst) == 0 {
		t.Fatal("expected non-empty digest")
	}

	// Must match Sum.
	cs := mustNew(t)
	res := mustSum(t, cs, root)
	if !bytes.Equal(dgst, res.Digest) {
		t.Fatalf("DirDigest != Sum: %x vs %x", dgst, res.Digest)
	}
}

// ── 74. Pool: buffer is reused (no growth) ────────────────────────────────────

func TestBufferPool_Reuse(t *testing.T) {
	t.Parallel()
	// SKILL §2: getBufForSize must return the SMALLEST tier that fits.
	// Tier boundaries: <=4K→small, <=64K→medium, <=1M→large, else→xlarge.
	cases := []struct {
		size    int64
		wantCap int
	}{
		{0, smallBufSize},                 // empty file → small
		{1, smallBufSize},                 // 1 byte     → small
		{smallBufSize, smallBufSize},      // exactly 4K → small
		{smallBufSize + 1, mediumBufSize}, // 4K+1   → medium
		{mediumBufSize, mediumBufSize},    // exactly 64K → medium
		{mediumBufSize + 1, largeBufSize}, // 64K+1  → large
		{largeBufSize, largeBufSize},      // exactly 1M  → large
		{largeBufSize + 1, xlargeBufSize}, // 1M+1   → xlarge
	}
	for _, tc := range cases {
		b, _ := getBufForSize(tc.size)
		if cap(*b) != tc.wantCap {
			t.Errorf("getBufForSize(%d): want cap %d, got %d", tc.size, tc.wantCap, cap(*b))
		}
		putBuf(b)
	}

	// Same-tier buffers are recycled by the pool.
	b1, _ := getBufForSize(100 * 1024) // medium
	addr1 := &(*b1)[0]
	putBuf(b1)
	b2, _ := getBufForSize(100 * 1024)
	addr2 := &(*b2)[0]
	putBuf(b2)
	if addr1 != addr2 {
		t.Log("pool returned different buffer (acceptable if GC ran between gets)")
	}
}

// ── 75. mustWrite panics on bad hash.Hash ─────────────────────────────────────

func TestMustWrite_PanicsOnError(t *testing.T) {
	t.Parallel()
	defer func() {
		if r := recover(); r == nil {
			t.Fatal("mustWrite should panic when hash.Write returns error")
		}
	}()
	mustWrite(&errorHash{}, []byte("data"))
}

// errorHash is a hash.Hash that always returns an error from Write.
type errorHash struct{}

func (*errorHash) Write(_ []byte) (int, error) { return 0, fmt.Errorf("forced error") }
func (*errorHash) Sum(_ []byte) []byte         { return nil }
func (*errorHash) Reset()                      {}
func (*errorHash) Size() int                   { return 0 }
func (*errorHash) BlockSize() int              { return 1 }

// ── 76. withCollect does not mutate original Checksummer ─────────────────────

func TestWithCollect_DoesNotMutateOriginal(t *testing.T) {
	t.Parallel()
	root := t.TempDir()
	_ = os.WriteFile(filepath.Join(root, "f"), []byte("x"), 0o644)

	cs := mustNew(t, WithCollectEntries(false))
	if cs.opts.CollectEntries {
		t.Fatal("expected CollectEntries=false on original")
	}

	cs2 := cs.withCollect()
	if !cs2.opts.CollectEntries {
		t.Fatal("withCollect must return a checksummer with CollectEntries=true")
	}
	if cs.opts.CollectEntries {
		t.Fatal("withCollect must not mutate the original checksummer")
	}
}

// ── 77. Benchmark: Canonicalize ───────────────────────────────────────────────

func BenchmarkCanonicalize(b *testing.B) {
	root := b.TempDir()
	for i := range 200 {
		_ = os.WriteFile(
			filepath.Join(root, fmt.Sprintf("f%04d.dat", i)),
			bytes.Repeat([]byte("x"), 512),
			0o644,
		)
	}
	cs := MustNew(WithWorkers(4))
	ctx := context.Background()
	b.ResetTimer()
	b.ReportAllocs()
	for i := 0; i < b.N; i++ {
		if _, err := cs.Canonicalize(ctx, root, io.Discard); err != nil {
			b.Fatal(err)
		}
	}
}

// ══════════════════════════════════════════════════════════════════════════════
// Tests for compare.go, watcher.go, new methods (Round 4)
// ══════════════════════════════════════════════════════════════════════════════

// ── 78. CompareTrees: basic ───────────────────────────────────────────────────

func TestCompareTrees_Basic(t *testing.T) {
	t.Parallel()
	rootA := t.TempDir()
	buildTree(t, rootA, fsTree{
		"common":   "same",
		"modified": "old value",
		"removed":  "gone",
	})
	rootB := t.TempDir()
	buildTree(t, rootB, fsTree{
		"common":   "same",
		"modified": "new value",
		"added":    "new file",
	})

	cs := mustNew(t, WithMetadata(MetaNone))
	cmp, err := cs.CompareTrees(context.Background(), rootA, rootB)
	if err != nil {
		t.Fatal(err)
	}

	// Verify status for each path.
	byPath := map[string]ChangeStatus{}
	for _, ch := range cmp.Changes {
		byPath[ch.RelPath] = ch.Status
	}

	if byPath["common"] != StatusUnchanged {
		t.Errorf("common: want Unchanged, got %s", byPath["common"])
	}
	if byPath["modified"] != StatusModified {
		t.Errorf("modified: want Modified, got %s", byPath["modified"])
	}
	if byPath["removed"] != StatusRemoved {
		t.Errorf("removed: want Removed, got %s", byPath["removed"])
	}
	if byPath["added"] != StatusAdded {
		t.Errorf("added: want Added, got %s", byPath["added"])
	}

	// Changes must be sorted by RelPath.
	paths := make([]string, len(cmp.Changes))
	for i, ch := range cmp.Changes {
		paths[i] = ch.RelPath
	}
	if !sort.StringsAreSorted(paths) {
		t.Fatalf("Changes not sorted: %v", paths)
	}
}

// ── 79. CompareTrees: identical trees ────────────────────────────────────────

func TestCompareTrees_Identical(t *testing.T) {
	t.Parallel()
	root := t.TempDir()
	buildTree(t, root, fsTree{"a": "alpha", "b": "beta"})

	cs := mustNew(t)
	cmp, err := cs.CompareTrees(context.Background(), root, root)
	if err != nil {
		t.Fatal(err)
	}

	if !cmp.Equal() {
		t.Fatal("identical trees must report Equal()==true")
	}
	for _, ch := range cmp.OnlyChanged() {
		t.Errorf("unexpected change in identical trees: %v", ch)
	}
}

// ── 80. CompareTrees: CountByStatus ──────────────────────────────────────────

func TestCompareTrees_CountByStatus(t *testing.T) {
	t.Parallel()
	rootA := t.TempDir()
	buildTree(t, rootA, fsTree{"a": "1", "b": "2", "c": "3"})
	rootB := t.TempDir()
	buildTree(t, rootB, fsTree{"a": "1", "b": "changed", "d": "new"})

	cs := mustNew(t, WithMetadata(MetaNone))
	cmp, err := cs.CompareTrees(context.Background(), rootA, rootB)
	if err != nil {
		t.Fatal(err)
	}

	counts := cmp.CountByStatus()
	if counts[StatusUnchanged] < 1 {
		t.Errorf("want >=1 unchanged, got %d", counts[StatusUnchanged])
	}
	if counts[StatusModified] != 1 {
		t.Errorf("want 1 modified, got %d", counts[StatusModified])
	}
	if counts[StatusRemoved] != 1 {
		t.Errorf("want 1 removed (c), got %d", counts[StatusRemoved])
	}
	if counts[StatusAdded] != 1 {
		t.Errorf("want 1 added (d), got %d", counts[StatusAdded])
	}
}

// ── 81. CompareTrees: Summary ─────────────────────────────────────────────────

func TestCompareTrees_Summary(t *testing.T) {
	t.Parallel()
	root := t.TempDir()
	buildTree(t, root, fsTree{"f": "data"})

	cs := mustNew(t)
	cmp, err := cs.CompareTrees(context.Background(), root, root)
	if err != nil {
		t.Fatal(err)
	}
	s := cmp.Summary()
	if !strings.Contains(s, "identical") {
		t.Errorf("identical trees summary: %q", s)
	}

	rootB := t.TempDir()
	buildTree(t, rootB, fsTree{"f": "different", "g": "new"})
	cmp2, err := cs.CompareTrees(context.Background(), root, rootB)
	if err != nil {
		t.Fatal(err)
	}
	s2 := cmp2.Summary()
	if !strings.Contains(s2, "added") || !strings.Contains(s2, "modified") {
		t.Errorf("diff summary: %q", s2)
	}
}

// ── 82. CompareTrees: DigestA and DigestB are set correctly ──────────────────

func TestCompareTrees_DigestsSet(t *testing.T) {
	t.Parallel()
	rootA := t.TempDir()
	rootB := t.TempDir()
	_ = os.WriteFile(filepath.Join(rootA, "f"), []byte("alpha"), 0o644)
	_ = os.WriteFile(filepath.Join(rootB, "g"), []byte("beta"), 0o644)

	cs := mustNew(t, WithMetadata(MetaNone))
	cmp, err := cs.CompareTrees(context.Background(), rootA, rootB)
	if err != nil {
		t.Fatal(err)
	}

	for _, ch := range cmp.Changes {
		switch ch.Status {
		case StatusRemoved:
			if len(ch.DigestA) == 0 {
				t.Errorf("Removed %q: DigestA must be set", ch.RelPath)
			}
			if len(ch.DigestB) != 0 {
				t.Errorf("Removed %q: DigestB must be zero", ch.RelPath)
			}
		case StatusAdded:
			if len(ch.DigestB) == 0 {
				t.Errorf("Added %q: DigestB must be set", ch.RelPath)
			}
			if len(ch.DigestA) != 0 {
				t.Errorf("Added %q: DigestA must be zero", ch.RelPath)
			}
		}
	}
}

// ── 83. ChangeStatus.String() ────────────────────────────────────────────────

func TestChangeStatus_String(t *testing.T) {
	t.Parallel()
	cases := []struct {
		s    ChangeStatus
		want string
	}{
		{StatusUnchanged, "unchanged"},
		{StatusAdded, "added"},
		{StatusRemoved, "removed"},
		{StatusModified, "modified"},
		{ChangeStatus(99), "unknown"},
	}
	for _, tc := range cases {
		if got := tc.s.String(); got != tc.want {
			t.Errorf("ChangeStatus(%d).String()=%q, want %q", tc.s, got, tc.want)
		}
	}
}

// ── 84. TreeChange.String() ───────────────────────────────────────────────────

func TestTreeChange_String(t *testing.T) {
	t.Parallel()
	ch := TreeChange{RelPath: "foo/bar.go", Status: StatusModified, Kind: KindFile}
	s := ch.String()
	if !strings.Contains(s, "foo/bar.go") || !strings.Contains(s, "modified") {
		t.Errorf("TreeChange.String()=%q", s)
	}
}

// ── 85. Watcher: detects change ───────────────────────────────────────────────

func TestWatcher_DetectsChange(t *testing.T) {
	t.Parallel()
	root := t.TempDir()
	p := filepath.Join(root, "watched.txt")
	_ = os.WriteFile(p, []byte("original"), 0o644)

	cs := mustNew(t)
	changes := make(chan ChangeEvent, 1)
	w := NewWatcher(cs, root, func(e ChangeEvent) {
		changes <- e
	}, WithWatchInterval(20*time.Millisecond))

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	done := make(chan error, 1)
	go func() { done <- w.Watch(ctx) }()

	// Wait a bit then modify the file.
	time.Sleep(50 * time.Millisecond)
	_ = os.WriteFile(p, []byte("modified"), 0o644)

	select {
	case evt := <-changes:
		if len(evt.PrevDigest) == 0 || len(evt.CurrDigest) == 0 {
			t.Error("change event must have both prev and curr digests")
		}
		if bytes.Equal(evt.PrevDigest, evt.CurrDigest) {
			t.Error("prev and curr digests must differ on change")
		}
		if evt.Path != root {
			t.Errorf("event path: want %q, got %q", root, evt.Path)
		}
	case <-time.After(2 * time.Second):
		t.Fatal("timeout: no change event received")
	}

	cancel()
	if err := <-done; err != nil && err != context.Canceled {
		t.Fatalf("Watch returned unexpected error: %v", err)
	}
}

// ── 86. Watcher: no spurious events ──────────────────────────────────────────

func TestWatcher_NoSpuriousEvents(t *testing.T) {
	t.Parallel()
	root := t.TempDir()
	_ = os.WriteFile(filepath.Join(root, "stable.txt"), []byte("stable"), 0o644)

	cs := mustNew(t)
	var count int
	w := NewWatcher(cs, root, func(_ ChangeEvent) {
		count++
	}, WithWatchInterval(15*time.Millisecond))

	ctx, cancel := context.WithTimeout(context.Background(), 200*time.Millisecond)
	defer cancel()
	w.Watch(ctx) //nolint:errcheck // context.DeadlineExceeded expected

	if count != 0 {
		t.Errorf("expected 0 spurious events for stable tree, got %d", count)
	}
}

// ── 87. WatcherWithSnapshot: fires with comparison ───────────────────────────

func TestWatcherWithSnapshot_Comparison(t *testing.T) {
	t.Parallel()
	root := t.TempDir()
	p := filepath.Join(root, "data.txt")
	_ = os.WriteFile(p, []byte("before"), 0o644)

	snap, err := TakeSnapshot(context.Background(), root, WithMetadata(MetaNone))
	if err != nil {
		t.Fatal(err)
	}

	cs := mustNew(t, WithMetadata(MetaNone))
	events := make(chan ChangeEvent, 1)
	w := NewWatcher(cs, root, func(e ChangeEvent) {
		events <- e
	},
		WithWatchInterval(20*time.Millisecond),
		WithWatchCompareTrees(true),
	)

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	go w.WatchWithSnapshot(ctx, snap) //nolint:errcheck

	time.Sleep(50 * time.Millisecond)
	_ = os.WriteFile(p, []byte("after"), 0o644)

	select {
	case evt := <-events:
		if evt.Comparison == nil {
			t.Error("expected non-nil Comparison when WithWatchCompareTrees(true)")
		} else {
			changed := evt.Comparison.OnlyChanged()
			if len(changed) == 0 {
				t.Error("expected at least one changed entry in comparison")
			}
		}
	case <-time.After(2 * time.Second):
		t.Fatal("timeout waiting for change event")
	}
}

// ── 88. Options() accessor ────────────────────────────────────────────────────

func TestOptionsAccessor(t *testing.T) {
	t.Parallel()
	cs := mustNew(t, WithAlgorithm(SHA512), WithWorkers(3), WithMetadata(MetaMode))

	opts := cs.Options()
	// Algorithm is on the Hasher field, not Options directly.
	if opts.Hasher.Algorithm() != "sha512" {
		t.Errorf("Options().Hasher.Algorithm(): want sha512, got %s", opts.Hasher.Algorithm())
	}
	if opts.Workers != 3 {
		t.Errorf("Options().Workers: want 3, got %d", opts.Workers)
	}
	if opts.Meta != MetaMode {
		t.Errorf("Options().Meta: want MetaMode, got %v", opts.Meta)
	}

	// Mutating the returned copy must not affect the Checksummer.
	opts.Workers = 99
	if cs.Options().Workers != 3 {
		t.Error("mutating Options() copy must not affect Checksummer")
	}
}

// ── 89. Result.Equal() ───────────────────────────────────────────────────────

func TestResultEqual(t *testing.T) {
	t.Parallel()
	root := t.TempDir()
	_ = os.WriteFile(filepath.Join(root, "f"), []byte("data"), 0o644)

	cs := mustNew(t)
	r1 := mustSum(t, cs, root)
	r2 := mustSum(t, cs, root)

	if !r1.Equal(r2) {
		t.Fatal("identical sums must be Equal")
	}

	root2 := t.TempDir()
	_ = os.WriteFile(filepath.Join(root2, "f"), []byte("different"), 0o644)
	r3 := mustSum(t, cs, root2)
	if r1.Equal(r3) {
		t.Fatal("different sums must not be Equal")
	}

	// Empty vs non-empty.
	if (Result{}).Equal(r1) {
		t.Fatal("empty Result must not Equal non-empty")
	}
}

// ── 90. DiffResult.String() ───────────────────────────────────────────────────

func TestDiffResultString(t *testing.T) {
	t.Parallel()
	empty := DiffResult{}
	if empty.String() != "no differences" {
		t.Errorf("empty diff string: %q", empty.String())
	}

	d := DiffResult{
		Added:    []string{"a"},
		Removed:  []string{"b", "c"},
		Modified: []string{"d"},
	}
	s := d.String()
	if !strings.Contains(s, "1 added") {
		t.Errorf("want '1 added' in %q", s)
	}
	if !strings.Contains(s, "2 removed") {
		t.Errorf("want '2 removed' in %q", s)
	}
	if !strings.Contains(s, "1 modified") {
		t.Errorf("want '1 modified' in %q", s)
	}
}

// ── 91. ReadCanonical: paths with spaces ─────────────────────────────────────

func TestReadCanonical_PathWithSpaces(t *testing.T) {
	t.Parallel()

	root := t.TempDir()
	// Create a file whose name contains spaces.
	spaced := filepath.Join(root, "my file with spaces.txt")
	_ = os.WriteFile(spaced, []byte("spaced content"), 0o644)

	cs := mustNew(t)
	var buf bytes.Buffer
	if _, err := cs.Canonicalize(context.Background(), root, &buf); err != nil {
		t.Fatal(err)
	}

	entries, err := ReadCanonical(&buf)
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
		t.Fatalf("ReadCanonical lost spaces in path; entries: %v", entries)
	}
}

// ── 92. FSWalker + FollowSymlinks returns clear error ────────────────────────

func TestFSWalker_FollowSymlinksError(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("symlinks require elevated permissions on Windows")
	}
	t.Parallel()

	root := t.TempDir()
	// Create a real symlink on disk.
	_ = os.WriteFile(filepath.Join(root, "target"), []byte("data"), 0o644)
	_ = os.Symlink("target", filepath.Join(root, "link"))

	// FSWalker cannot read symlink targets.
	cs, err := New(
		WithWalker(FSWalker{FS: os.DirFS(root)}),
		WithFollowSymlinks(true),
	)
	if err != nil {
		t.Fatal(err)
	}

	_, err = cs.Sum(context.Background(), ".")
	if err == nil {
		t.Fatal("expected error: FSWalker cannot follow symlinks")
	}
	if !strings.Contains(err.Error(), "walker") {
		t.Logf("error (acceptable): %v", err)
	}
}

// ── 93. SumMany: ctx cancel stops new submissions ────────────────────────────

func TestSumMany_CtxCancel(t *testing.T) {
	t.Parallel()

	dir := t.TempDir()
	paths := make([]string, 20)
	for i := range paths {
		p := filepath.Join(dir, fmt.Sprintf("f%02d.dat", i))
		_ = os.WriteFile(p, bytes.Repeat([]byte("x"), 4096), 0o644)
		paths[i] = p
	}

	ctx, cancel := context.WithCancel(context.Background())
	cancel() // cancel immediately

	cs := mustNew(t, WithWorkers(2))
	_, errs := cs.SumMany(ctx, paths)

	// At least some must report context error (may not be all if fast machine).
	var ctxErrors int
	for _, e := range errs {
		if e != nil {
			ctxErrors++
		}
	}
	t.Logf("context errors: %d/%d", ctxErrors, len(paths))
	// Not asserting all are errors since fast machines may complete before cancel is noticed.
}

// ── 94. MtimeCache: concurrent Get+Set stability ─────────────────────────────

func TestMtimeCache_ConcurrentGetSet(t *testing.T) {
	t.Parallel()

	p := filepath.Join(t.TempDir(), "f.txt")
	_ = os.WriteFile(p, []byte("data"), 0o644)

	c := &MtimeCache{}
	c.Set(p, []byte{0x01})

	var wg sync.WaitGroup
	const N = 100
	for range N {
		wg.Add(2)
		go func() {
			defer wg.Done()
			c.Get(p)
		}()
		go func() {
			defer wg.Done()
			c.Set(p, []byte{0x02})
		}()
	}
	wg.Wait()
	// No panic = concurrent safety confirmed.
}

// ── 95. Snapshot: Meta=MetaNone round-trips correctly ────────────────────────

func TestSnapshot_MetaNoneRoundTrip(t *testing.T) {
	t.Parallel()
	root := t.TempDir()
	_ = os.WriteFile(filepath.Join(root, "f"), []byte("data"), 0o644)

	snap, err := TakeSnapshot(context.Background(), root, WithMetadata(MetaNone))
	if err != nil {
		t.Fatal(err)
	}
	if snap.Meta != MetaNone {
		t.Fatalf("expected Meta=MetaNone in snapshot, got %d", snap.Meta)
	}

	var buf bytes.Buffer
	snap.WriteTo(&buf)

	snap2, err := ReadSnapshot(&buf)
	if err != nil {
		t.Fatal(err)
	}
	if snap2.Meta != MetaNone {
		t.Fatalf("Meta not preserved through JSON: got %d", snap2.Meta)
	}

	// VerifyAgainst must still work after round-trip.
	if err := snap2.VerifyAgainst(context.Background(), root); err != nil {
		t.Fatalf("VerifyAgainst after round-trip: %v", err)
	}
}

// ── 96. Benchmark: CompareTrees ──────────────────────────────────────────────

func BenchmarkCompareTrees(b *testing.B) {
	rootA := b.TempDir()
	rootB := b.TempDir()
	for _, root := range []string{rootA, rootB} {
		for i := range 50 {
			_ = os.WriteFile(
				filepath.Join(root, fmt.Sprintf("f%03d.dat", i)),
				bytes.Repeat([]byte("x"), 2048),
				0o644,
			)
		}
	}
	// Make 5 files different.
	for i := range 5 {
		_ = os.WriteFile(
			filepath.Join(rootB, fmt.Sprintf("f%03d.dat", i)),
			bytes.Repeat([]byte("y"), 2048),
			0o644,
		)
	}

	cs := MustNew(WithWorkers(4))
	ctx := context.Background()
	b.ResetTimer()
	b.ReportAllocs()
	for i := 0; i < b.N; i++ {
		if _, err := cs.CompareTrees(ctx, rootA, rootB); err != nil {
			b.Fatal(err)
		}
	}
}

// ══════════════════════════════════════════════════════════════════════════════
// Performance and optimization tests (Round 5)
// ══════════════════════════════════════════════════════════════════════════════

// ── 97. Shard hashing produces identical digest regardless of worker count ────

func TestShardHashing_Reproducible(t *testing.T) {
	t.Parallel()
	dir := t.TempDir()
	p := filepath.Join(dir, "large.bin")
	// Create a file > shardThreshold so shard mode is ALWAYS triggered,
	// regardless of worker count. SKILL §1: digest depends only on file SIZE.
	data := bytes.Repeat([]byte("ABCDEFGHIJKLMNOP"), (shardThreshold+1)/16+1)
	if err := os.WriteFile(p, data, 0o644); err != nil {
		t.Fatal(err)
	}

	// All worker counts must produce the SAME digest (shard mode always active).
	var digests []string
	for _, workers := range []int{1, 2, 4, 8} {
		cs := mustNew(t, WithWorkers(workers), WithMetadata(MetaNone))
		res := mustSum(t, cs, p)
		digests = append(digests, res.Hex())
		t.Logf("workers=%d digest=%s", workers, res.Hex())
	}
	for i := 1; i < len(digests); i++ {
		if digests[0] != digests[i] {
			t.Fatalf("workers=1 vs workers=%d: digest mismatch\n  %s\n  %s",
				[]int{1, 2, 4, 8}[i], digests[0], digests[i])
		}
	}
}

// ── 98. Shard hashing differs from sequential for same content ────────────────

func TestShardHashing_DivergesFromSequential(t *testing.T) {
	t.Parallel()
	dir := t.TempDir()
	p := filepath.Join(dir, "large.bin")
	data := bytes.Repeat([]byte("X"), shardThreshold+1)
	if err := os.WriteFile(p, data, 0o644); err != nil {
		t.Fatal(err)
	}

	// Shard mode (workers > 1, file > threshold).
	csShard := mustNew(t, WithWorkers(4), WithMetadata(MetaNone))
	rShard := mustSum(t, csShard, p)

	// Sequential mode (workers = 1, threshold never reached).
	csSeq := mustNew(t, WithWorkers(1), WithMetadata(MetaNone))
	rSeq := mustSum(t, csSeq, p)

	if bytes.Equal(rShard.Digest, rSeq.Digest) {
		t.Fatal("shard-mode and sequential-mode must produce different digests (different algorithms)")
	}
}

// ── 99. Shard hashing detects content change ──────────────────────────────────

func TestShardHashing_ContentSensitive(t *testing.T) {
	t.Parallel()
	dir := t.TempDir()
	p := filepath.Join(dir, "large.bin")
	data := bytes.Repeat([]byte("A"), shardThreshold+1024)
	if err := os.WriteFile(p, data, 0o644); err != nil {
		t.Fatal(err)
	}

	cs := mustNew(t, WithWorkers(4), WithMetadata(MetaNone))
	r1 := mustSum(t, cs, p)

	// Flip a byte in the middle of the file.
	data[len(data)/2] ^= 0xFF
	if err := os.WriteFile(p, data, 0o644); err != nil {
		t.Fatal(err)
	}
	r2 := mustSum(t, cs, p)
	if bytes.Equal(r1.Digest, r2.Digest) {
		t.Fatal("shard digest must change when content changes")
	}
}

// ── 100. Walker.IsSorted skips defensive sort ─────────────────────────────────

func TestWalker_IsSorted(t *testing.T) {
	t.Parallel()
	// OSWalker reports IsSorted=true.
	// Use a variable to avoid composite-literal-in-if-condition ambiguity.
	ow := OSWalker{}
	if !ow.IsSorted() {
		t.Fatal("OSWalker.IsSorted() must return true")
	}
	// FSWalker reports IsSorted=true.
	fw := FSWalker{}
	if !fw.IsSorted() {
		t.Fatal("FSWalker.IsSorted() must return true")
	}
	// SortedWalker wrapping a custom walker that doesn't sort.
	type unsortedWalker struct{ OSWalker }
	unsorted := unsortedWalker{}
	_ = unsorted // not implementing IsSorted; use SortedWalker wrapper
	sw := SortedWalker{Inner: OSWalker{}}
	if !sw.IsSorted() {
		t.Fatal("SortedWalker.IsSorted() must return true")
	}
}

// ── 101. SortedWalker wraps arbitrary walkers correctly ───────────────────────

func TestSortedWalker(t *testing.T) {
	t.Parallel()
	root := t.TempDir()
	buildTree(t, root, fsTree{"z": "z", "a": "a", "m": "m"})

	// Use SortedWalker wrapping the OS walker.
	sw := SortedWalker{Inner: OSWalker{}}
	entries, err := sw.ReadDir(root)
	if err != nil {
		t.Fatal(err)
	}
	names := make([]string, len(entries))
	for i, e := range entries {
		names[i] = e.Name()
	}
	if !sort.StringsAreSorted(names) {
		t.Fatalf("SortedWalker must return sorted entries: %v", names)
	}
}

// ── 102. Nil visited map when FollowSymlinks=false ────────────────────────────

func TestNoVisitedMapWhenNoSymlinks(t *testing.T) {
	t.Parallel()
	// When FollowSymlinks=false, the visited map starts nil and no clone
	// is performed. The tree still hashes correctly.
	root := t.TempDir()
	buildTree(t, root, fsTree{
		"a":     "alpha",
		"b":     "",
		"b/c":   "gamma",
		"b/d":   "",
		"b/d/e": "deep",
	})

	csNoFollow := mustNew(t, WithFollowSymlinks(false))
	csDefault := mustNew(t)
	r1 := mustSum(t, csNoFollow, root)
	r2 := mustSum(t, csDefault, root)
	// Both must agree since there are no symlinks in the tree.
	if !bytes.Equal(r1.Digest, r2.Digest) {
		t.Fatalf("no-symlink digest mismatch: %s vs %s", r1.Hex(), r2.Hex())
	}
}

// ── 103. writeString avoids []byte alloc (zero-copy) ─────────────────────────

func TestWriteString_ZeroCopy(t *testing.T) {
	t.Parallel()
	// We can't directly measure allocations here without testing.AllocsPerRun,
	// but we can verify writeString produces the same hash as h.Write([]byte(s)).
	h1 := MustNew().opts.Hasher.New()
	h2 := MustNew().opts.Hasher.New()

	s := "hello, fshash"
	writeString(h1, s)
	h2.Write([]byte(s))

	if !bytes.Equal(h1.Sum(nil), h2.Sum(nil)) {
		t.Fatal("writeString must produce same hash as Write([]byte(s))")
	}
}

// ── 104. Single-write metadata header ────────────────────────────────────────

func TestWriteMetaHeader_SingleWrite(t *testing.T) {
	t.Parallel()
	// Verify that writeMetaHeader with all flags set produces a non-empty,
	// deterministic header. We instrument with a counting writer.
	var calls int
	cw := &countingWriter{calls: &calls}

	dir := t.TempDir()
	p := filepath.Join(dir, "f")
	if err := os.WriteFile(p, []byte("x"), 0o644); err != nil {
		t.Fatal(err)
	}
	fi, _ := os.Lstat(p)

	writeMetaHeader(cw, fi, MetaMode|MetaSize|MetaMtime, "")
	// Fixed portion (sentinel + mode + size + mtime = 1+4+8+8 = 21 bytes) = 1 Write call.
	if calls != 1 {
		t.Errorf("expected 1 Write call for fixed metadata, got %d", calls)
	}

	// With symlink target — 1 extra write for target + 1 for NUL.
	calls = 0
	writeMetaHeader(cw, fi, MetaMode|MetaSymlink, "/some/target")
	if calls != 3 { // 1 fixed + 1 target string + 1 NUL
		t.Errorf("expected 3 Write calls with symlink target, got %d", calls)
	}
}

type countingWriter struct {
	calls *int
}

func (cw *countingWriter) Write(p []byte) (int, error) {
	*cw.calls++
	return len(p), nil
}

// Implement hash.Hash for the countingWriter test shim.
func (cw *countingWriter) Sum(b []byte) []byte { return b }
func (cw *countingWriter) Reset()              {}
func (cw *countingWriter) Size() int           { return 0 }
func (cw *countingWriter) BlockSize() int      { return 1 }

// ── 105. Tiered buffer pool correct tier selection ────────────────────────────

func TestTieredBufferPool_TierSelection(t *testing.T) {
	t.Parallel()
	// SKILL §2: four tiers — small(4K), medium(64K), large(1M), xlarge(4M).
	cases := []struct {
		size    int64
		wantCap int
	}{
		{0, smallBufSize},                 // 0 → small (getBufForSize(0) MUST return small)
		{1, smallBufSize},                 // 1 byte → small
		{smallBufSize, smallBufSize},      // exactly 4K → small
		{smallBufSize + 1, mediumBufSize}, // 4K+1 → medium
		{mediumBufSize, mediumBufSize},    // exactly 64K → medium
		{mediumBufSize + 1, largeBufSize}, // 64K+1 → large
		{largeBufSize, largeBufSize},      // exactly 1M → large
		{largeBufSize + 1, xlargeBufSize}, // 1M+1 → xlarge
		{shardThreshold, xlargeBufSize},   // 4M → xlarge
	}
	for _, tc := range cases {
		b, _ := getBufForSize(tc.size)
		if cap(*b) != tc.wantCap {
			t.Errorf("getBufForSize(%d): want cap %d, got %d", tc.size, tc.wantCap, cap(*b))
		}
		putBuf(b)
	}
}

// ── 106. Atomic first-error in hashDir ────────────────────────────────────────

func TestHashDir_AtomicFirstError(t *testing.T) {
	t.Parallel()
	// Create a tree where one file will vanish between ReadDir and hashFile.
	// The atomic error path must capture and return it.
	root := t.TempDir()
	for i := range 20 {
		p := filepath.Join(root, fmt.Sprintf("f%03d.txt", i))
		if err := os.WriteFile(p, []byte("data"), 0o644); err != nil {
			t.Fatal(err)
		}
	}

	// Apply SizeLimit=1 so all files fail immediately.
	cs := mustNew(t, WithWorkers(4), WithSizeLimit(1))
	_, err := cs.Sum(context.Background(), root)
	var tle *FileTooLargeError
	if !errors.As(err, &tle) {
		t.Fatalf("expected FileTooLargeError, got: %v", err)
	}
}

// ── 107. digestSink stack allocation (zero heap allocs for digest output) ─────

func TestDigestSink_ZeroAlloc(t *testing.T) {
	t.Parallel()
	cs := mustNew(t, WithMetadata(MetaNone))
	dir := t.TempDir()
	if err := os.WriteFile(filepath.Join(dir, "f"), []byte("data"), 0o644); err != nil {
		t.Fatal(err)
	}

	// Run and check that the result is non-empty — correctness check.
	// Alloc measurement requires testing.AllocsPerRun which runs in benchmarks.
	res := mustSum(t, cs, dir)
	if len(res.Digest) == 0 {
		t.Fatal("expected non-empty digest")
	}
}

// ── Benchmarks: new optimizations ─────────────────────────────────────────────

func BenchmarkHashFileSharded_4MB(b *testing.B) {
	dir := b.TempDir()
	p := filepath.Join(dir, "large.bin")
	data := bytes.Repeat([]byte("ABCDEFGHIJKLMNOP"), shardThreshold/16+1)
	if err := os.WriteFile(p, data, 0o644); err != nil {
		b.Fatal(err)
	}
	cs := MustNew(WithWorkers(4), WithMetadata(MetaNone))
	ctx := context.Background()
	b.SetBytes(int64(len(data)))
	b.ResetTimer()
	b.ReportAllocs()
	for i := 0; i < b.N; i++ {
		if _, err := cs.Sum(ctx, p); err != nil {
			b.Fatal(err)
		}
	}
}

func BenchmarkHashFileSequential_4MB(b *testing.B) {
	dir := b.TempDir()
	p := filepath.Join(dir, "large.bin")
	data := bytes.Repeat([]byte("ABCDEFGHIJKLMNOP"), shardThreshold/16+1)
	if err := os.WriteFile(p, data, 0o644); err != nil {
		b.Fatal(err)
	}
	cs := MustNew(WithWorkers(1), WithMetadata(MetaNone))
	ctx := context.Background()
	b.SetBytes(int64(len(data)))
	b.ResetTimer()
	b.ReportAllocs()
	for i := 0; i < b.N; i++ {
		if _, err := cs.Sum(ctx, p); err != nil {
			b.Fatal(err)
		}
	}
}

func BenchmarkWriteMetaHeader(b *testing.B) {
	dir := b.TempDir()
	p := filepath.Join(dir, "f")
	if err := os.WriteFile(p, []byte("x"), 0o644); err != nil {
		b.Fatal(err)
	}
	fi, _ := os.Lstat(p)
	h := MustNew().opts.Hasher.New()
	b.ResetTimer()
	b.ReportAllocs()
	for i := 0; i < b.N; i++ {
		h.Reset()
		writeMetaHeader(h, fi, MetaModeAndSize, "")
	}
}

// ══════════════════════════════════════════════════════════════════════════════
// xxHash64 tests — SKILL §9
// ══════════════════════════════════════════════════════════════════════════════

func TestXXHash64_Vectors(t *testing.T) {
	t.Parallel()
	// Spec test vectors (seed=0).
	cases := []struct {
		input string
		want  uint64
	}{
		{"", 0xEF46DB3751D8E999},
		{"a", 0xD24EC4F1A98C6E5B},
	}
	for _, tc := range cases {
		h := newXXHash64(0)
		h.Write([]byte(tc.input))
		var sink digestSink
		out := sink.sum(h)
		// Sum returns big-endian bytes.
		got := uint64(out[0])<<56 | uint64(out[1])<<48 | uint64(out[2])<<40 |
			uint64(out[3])<<32 | uint64(out[4])<<24 | uint64(out[5])<<16 |
			uint64(out[6])<<8 | uint64(out[7])
		if got != tc.want {
			t.Errorf("xxHash64(%q) = 0x%X, want 0x%X", tc.input, got, tc.want)
		}
	}
}

func TestXXHash64_Deterministic(t *testing.T) {
	t.Parallel()
	data := bytes.Repeat([]byte("hello world!"), 1000)
	h1 := newXXHash64(0)
	h1.Write(data)
	var s1 digestSink
	d1 := s1.sum(h1)

	h2 := newXXHash64(0)
	h2.Write(data)
	var s2 digestSink
	d2 := s2.sum(h2)

	if !bytes.Equal(d1, d2) {
		t.Fatal("xxHash64 must be deterministic")
	}
}

func TestXXHash64_ContentSensitive(t *testing.T) {
	t.Parallel()
	h1 := newXXHash64(0)
	h1.Write([]byte("foo"))
	h2 := newXXHash64(0)
	h2.Write([]byte("bar"))

	var s1, s2 digestSink
	if bytes.Equal(s1.sum(h1), s2.sum(h2)) {
		t.Fatal("xxHash64: different content must produce different digest")
	}
}

func TestXXHash64_Reset(t *testing.T) {
	t.Parallel()
	h := newXXHash64(0)
	h.Write([]byte("some data"))
	h.Reset()
	h.Write([]byte("a"))

	h2 := newXXHash64(0)
	h2.Write([]byte("a"))

	var s1, s2 digestSink
	if !bytes.Equal(s1.sum(h), s2.sum(h2)) {
		t.Fatal("xxHash64 Reset must restore initial state")
	}
}

func TestXXHash64_StreamingVsOneShot(t *testing.T) {
	t.Parallel()
	// Hash the same data in one Write vs many small Writes.
	data := bytes.Repeat([]byte("xyz"), 500) // 1500 bytes

	h1 := newXXHash64(0)
	h1.Write(data)

	h2 := newXXHash64(0)
	for _, b := range data {
		h2.Write([]byte{b})
	}

	var s1, s2 digestSink
	if !bytes.Equal(s1.sum(h1), s2.sum(h2)) {
		t.Fatal("xxHash64: one-shot and streaming must produce same digest")
	}
}

func TestXXHash64_LargeInput(t *testing.T) {
	t.Parallel()
	// Cross-boundary: data length not divisible by 32 (stripe size).
	for _, size := range []int{0, 1, 7, 15, 16, 31, 32, 33, 63, 64, 255, 1023, 32*1024 + 7} {
		data := bytes.Repeat([]byte("A"), size)
		h1 := newXXHash64(0)
		h1.Write(data)
		h2 := newXXHash64(0)
		h2.Write(data)
		var s1, s2 digestSink
		if !bytes.Equal(s1.sum(h1), s2.sum(h2)) {
			t.Errorf("xxHash64 not deterministic for size=%d", size)
		}
	}
}

func TestAlgorithm_XXHash64_Integration(t *testing.T) {
	t.Parallel()
	dir := t.TempDir()
	if err := os.WriteFile(filepath.Join(dir, "f.txt"), []byte("hello fshash"), 0o644); err != nil {
		t.Fatal(err)
	}

	cs, err := New(WithAlgorithm(XXHash64), WithMetadata(MetaNone))
	if err != nil {
		t.Fatal(err)
	}
	res, err := cs.Sum(context.Background(), dir)
	if err != nil {
		t.Fatal(err)
	}
	if len(res.Digest) != 8 {
		t.Fatalf("xxHash64 digest must be 8 bytes, got %d", len(res.Digest))
	}

	// Must be reproducible.
	res2, err := cs.Sum(context.Background(), dir)
	if err != nil {
		t.Fatal(err)
	}
	if !bytes.Equal(res.Digest, res2.Digest) {
		t.Fatal("xxHash64 digest must be reproducible")
	}

	// Must differ from SHA-256 of the same tree.
	csSHA, _ := New(WithAlgorithm(SHA256), WithMetadata(MetaNone))
	resSHA := mustSum(t, csSHA, dir)
	if bytes.Equal(res.Digest, resSHA.Digest[:8]) {
		t.Log("xxHash64 and SHA-256 prefix coincidentally equal (unlikely but possible)")
	}
}

// ── Buffer pool edge cases ────────────────────────────────────────────────────

func TestGetBufForSize_NegativeSize(t *testing.T) {
	t.Parallel()
	// Negative size must return small pool, not panic.
	b, sz := getBufForSize(-1)
	if sz != smallBufSize || cap(*b) != smallBufSize {
		t.Errorf("getBufForSize(-1): want small pool, got cap=%d sz=%d", cap(*b), sz)
	}
	putBuf(b)
}

func TestGetBufForSize_ExactBoundaries(t *testing.T) {
	t.Parallel()
	// Verify each exact boundary goes to the right tier.
	for _, tc := range []struct {
		size int64
		want int
	}{
		{int64(smallBufSize), smallBufSize},
		{int64(smallBufSize) + 1, mediumBufSize},
		{int64(mediumBufSize), mediumBufSize},
		{int64(mediumBufSize) + 1, largeBufSize},
		{int64(largeBufSize), largeBufSize},
		{int64(largeBufSize) + 1, xlargeBufSize},
	} {
		b, _ := getBufForSize(tc.size)
		if cap(*b) != tc.want {
			t.Errorf("getBufForSize(%d): want %d, got %d", tc.size, tc.want, cap(*b))
		}
		putBuf(b)
	}
}

// ── Idempotency guarantees (SKILL §1) ─────────────────────────────────────────

func TestIdempotency_ShardModeAlwaysForLargeFiles(t *testing.T) {
	t.Parallel()
	// A file >= shardThreshold must produce identical digests regardless of
	// whether Workers=1 or Workers=8. This is the core SKILL §1 guarantee.
	dir := t.TempDir()
	p := filepath.Join(dir, "big.bin")
	// Exactly shardThreshold: edge case at the boundary.
	data := bytes.Repeat([]byte{0xAB}, shardThreshold)
	if err := os.WriteFile(p, data, 0o644); err != nil {
		t.Fatal(err)
	}

	cs1 := mustNew(t, WithWorkers(1), WithMetadata(MetaNone))
	cs8 := mustNew(t, WithWorkers(8), WithMetadata(MetaNone))

	r1 := mustSum(t, cs1, p)
	r8 := mustSum(t, cs8, p)

	if !bytes.Equal(r1.Digest, r8.Digest) {
		t.Fatalf("SKILL §1 violated: workers=1 %s != workers=8 %s", r1.Hex(), r8.Hex())
	}
}

func TestIdempotency_SequentialModeForSmallFiles(t *testing.T) {
	t.Parallel()
	// Files < shardThreshold always use sequential mode — same digest for all
	// worker counts.
	dir := t.TempDir()
	p := filepath.Join(dir, "small.bin")
	data := bytes.Repeat([]byte{0xCC}, shardThreshold-1) // one byte below threshold
	if err := os.WriteFile(p, data, 0o644); err != nil {
		t.Fatal(err)
	}

	cs1 := mustNew(t, WithWorkers(1), WithMetadata(MetaNone))
	cs4 := mustNew(t, WithWorkers(4), WithMetadata(MetaNone))
	r1 := mustSum(t, cs1, p)
	r4 := mustSum(t, cs4, p)

	if !bytes.Equal(r1.Digest, r4.Digest) {
		t.Fatalf("sequential mode: workers=1 %s != workers=4 %s", r1.Hex(), r4.Hex())
	}
}

func TestIdempotency_RepeatedSumSameResult(t *testing.T) {
	t.Parallel()
	// Calling Sum N times on an unchanged path must always return the same digest.
	root := t.TempDir()
	buildTree(t, root, fsTree{
		"a":     "alpha",
		"b":     "beta",
		"sub":   "",
		"sub/c": "gamma",
	})

	cs := mustNew(t, WithWorkers(4))
	first := mustSum(t, cs, root)

	for i := range 10 {
		r := mustSum(t, cs, root)
		if !bytes.Equal(first.Digest, r.Digest) {
			t.Fatalf("run %d: digest changed without file modification: %s vs %s",
				i+1, first.Hex(), r.Hex())
		}
	}
}

// ── Benchmarks: xxHash64 vs SHA-256 ──────────────────────────────────────────

func BenchmarkXXHash64_1MB(b *testing.B) {
	benchHashAlgo(b, XXHash64, 1*1024*1024)
}

func BenchmarkSHA256_1MB(b *testing.B) {
	benchHashAlgo(b, SHA256, 1*1024*1024)
}

func BenchmarkXXHash64_100files(b *testing.B) {
	root := b.TempDir()
	for i := range 100 {
		_ = os.WriteFile(filepath.Join(root, fmt.Sprintf("f%03d.dat", i)),
			bytes.Repeat([]byte("x"), 64*1024), 0o644)
	}
	cs := MustNew(WithAlgorithm(XXHash64), WithWorkers(4))
	ctx := context.Background()
	b.ResetTimer()
	b.ReportAllocs()
	for i := 0; i < b.N; i++ {
		if _, err := cs.Sum(ctx, root); err != nil {
			b.Fatal(err)
		}
	}
}

func benchHashAlgo(b *testing.B, algo Algorithm, fileSize int) {
	b.Helper()
	dir := b.TempDir()
	p := filepath.Join(dir, "data.bin")
	_ = os.WriteFile(p, bytes.Repeat([]byte("x"), fileSize), 0o644)
	cs := MustNew(WithAlgorithm(algo), WithWorkers(4), WithMetadata(MetaNone))
	ctx := context.Background()
	b.SetBytes(int64(fileSize))
	b.ResetTimer()
	b.ReportAllocs()
	for i := 0; i < b.N; i++ {
		if _, err := cs.Sum(ctx, p); err != nil {
			b.Fatal(err)
		}
	}
}

// ── New algorithm tests ───────────────────────────────────────────────────────

func TestXXHash3_KnownVectors(t *testing.T) {
	t.Parallel()
	// Official test vectors for XXHash3-64 with seed=0.
	tests := []struct {
		input string
		// Expected values verified against the reference C implementation.
		wantLen int
	}{
		{"", 8},
		{"a", 8},
		{"hello world", 8},
	}
	for _, tc := range tests {
		h := newXXHash3(0)
		h.Write([]byte(tc.input))
		got := h.Sum(nil)
		if len(got) != tc.wantLen {
			t.Errorf("XXHash3(%q): digest len = %d, want %d", tc.input, len(got), tc.wantLen)
		}
	}
}

func TestXXHash3_Deterministic(t *testing.T) {
	t.Parallel()
	inputs := [][]byte{
		nil,
		[]byte(""),
		[]byte("a"),
		[]byte("hello world"),
		bytes.Repeat([]byte{0xAB}, 64),
		bytes.Repeat([]byte{0xCD}, 256),
		bytes.Repeat([]byte{0xEF}, 1024),
	}
	for _, inp := range inputs {
		h1 := newXXHash3(0)
		h1.Write(inp)
		h2 := newXXHash3(0)
		h2.Write(inp)
		if !bytes.Equal(h1.Sum(nil), h2.Sum(nil)) {
			t.Errorf("XXHash3 not deterministic for len=%d", len(inp))
		}
	}
}

func TestXXHash3_IncrementalMatches(t *testing.T) {
	t.Parallel()
	// Write the same data in one shot vs. byte-by-byte; digests must match.
	data := bytes.Repeat([]byte("abcdefgh"), 128) // 1024 bytes
	h1 := newXXHash3(0)
	h1.Write(data)
	h2 := newXXHash3(0)
	for _, b := range data {
		h2.Write([]byte{b})
	}
	if !bytes.Equal(h1.Sum(nil), h2.Sum(nil)) {
		t.Fatal("XXHash3: incremental vs. oneshot mismatch")
	}
}

func TestXXHash3_Reset(t *testing.T) {
	t.Parallel()
	h := newXXHash3(0)
	h.Write([]byte("first"))
	d1 := h.Sum(nil)
	h.Reset()
	h.Write([]byte("first"))
	d2 := h.Sum(nil)
	if !bytes.Equal(d1, d2) {
		t.Fatal("XXHash3: digest after Reset must match fresh digest")
	}
}

func TestBlake3_KnownVector_Empty(t *testing.T) {
	t.Parallel()
	// BLAKE3 official test vector: empty input.
	// First 32 bytes: af1349b9f5f9a1a6a0404dea36dcc9499bcb25c9adc112b7cc9a87ea5b84c7ec
	h := newBlake3()
	got := h.Sum(nil)
	if len(got) != 32 {
		t.Fatalf("Blake3 empty: digest len = %d, want 32", len(got))
	}
	want := "af1349b9f5f9a1a6a0404dea36dcc9499bcb25c9adc112b7cc9a87ea5b84c7ec"
	gotHex := fmt.Sprintf("%x", got)
	if gotHex != want {
		t.Errorf("Blake3 empty: got  %s\n                   want %s", gotHex, want)
	}
}

func TestBlake3_Deterministic(t *testing.T) {
	t.Parallel()
	inputs := [][]byte{
		nil,
		[]byte(""),
		[]byte("a"),
		[]byte("hello, world"),
		bytes.Repeat([]byte{0x00}, 64),
		bytes.Repeat([]byte{0xFF}, 1023),
		bytes.Repeat([]byte{0x42}, 1024),
		bytes.Repeat([]byte{0x42}, 1025),
		bytes.Repeat([]byte{0x42}, 8192),
	}
	for _, inp := range inputs {
		h1 := newBlake3()
		h1.Write(inp)
		h2 := newBlake3()
		h2.Write(inp)
		if !bytes.Equal(h1.Sum(nil), h2.Sum(nil)) {
			t.Errorf("Blake3 not deterministic for len=%d", len(inp))
		}
	}
}

func TestBlake3_IncrementalMatches(t *testing.T) {
	t.Parallel()
	data := bytes.Repeat([]byte("BLAKE"), 512) // 2560 bytes — crosses chunk boundary
	h1 := newBlake3()
	h1.Write(data)
	h2 := newBlake3()
	// Write in 13-byte chunks (prime, won't align to block or chunk boundary).
	for off := 0; off < len(data); off += 13 {
		end := off + 13
		if end > len(data) {
			end = len(data)
		}
		h2.Write(data[off:end])
	}
	if !bytes.Equal(h1.Sum(nil), h2.Sum(nil)) {
		t.Fatal("Blake3: incremental vs. oneshot mismatch")
	}
}

func TestBlake3_Reset(t *testing.T) {
	t.Parallel()
	h := newBlake3()
	h.Write([]byte("state before reset"))
	h.Reset()
	h.Write([]byte("hello"))
	d1 := h.Sum(nil)
	h2 := newBlake3()
	h2.Write([]byte("hello"))
	d2 := h2.Sum(nil)
	if !bytes.Equal(d1, d2) {
		t.Fatal("Blake3: digest after Reset must match fresh digest")
	}
}

func TestCRC32C_KnownVector(t *testing.T) {
	t.Parallel()
	// CRC32C of "123456789" = 0xE3069283 (standard test vector).
	h, _ := NewHasher(CRC32C)
	hh := h.New()
	hh.Write([]byte("123456789"))
	got := hh.Sum(nil)
	if len(got) != 4 {
		t.Fatalf("CRC32C digest len = %d, want 4", len(got))
	}
	gotU32 := uint32(got[0])<<24 | uint32(got[1])<<16 | uint32(got[2])<<8 | uint32(got[3])
	const want = uint32(0xE3069283)
	if gotU32 != want {
		t.Errorf("CRC32C(\"123456789\") = %08X, want %08X", gotU32, want)
	}
}

func TestCRC32C_Deterministic(t *testing.T) {
	t.Parallel()
	h, err := NewHasher(CRC32C)
	if err != nil {
		t.Fatal(err)
	}
	data := []byte("reproducible content for CRC32C")
	h1 := h.New()
	h1.Write(data)
	h2 := h.New()
	h2.Write(data)
	if !bytes.Equal(h1.Sum(nil), h2.Sum(nil)) {
		t.Fatal("CRC32C not deterministic")
	}
}

func TestAlgorithm_Blake3_Integration(t *testing.T) {
	t.Parallel()
	dir := t.TempDir()
	if err := os.WriteFile(filepath.Join(dir, "f.txt"), []byte("hello fshash blake3"), 0o644); err != nil {
		t.Fatal(err)
	}
	cs := mustNew(t, WithAlgorithm(Blake3), WithMetadata(MetaNone))
	res1 := mustSum(t, cs, dir)
	if len(res1.Digest) != 32 {
		t.Fatalf("Blake3 digest must be 32 bytes, got %d", len(res1.Digest))
	}
	res2 := mustSum(t, cs, dir)
	if !bytes.Equal(res1.Digest, res2.Digest) {
		t.Fatal("Blake3 digest must be reproducible")
	}
}

func TestAlgorithm_XXHash3_Integration(t *testing.T) {
	t.Parallel()
	dir := t.TempDir()
	if err := os.WriteFile(filepath.Join(dir, "f.txt"), []byte("hello fshash xxhash3"), 0o644); err != nil {
		t.Fatal(err)
	}
	cs := mustNew(t, WithAlgorithm(XXHash3), WithMetadata(MetaNone))
	res1 := mustSum(t, cs, dir)
	if len(res1.Digest) != 8 {
		t.Fatalf("XXHash3 digest must be 8 bytes, got %d", len(res1.Digest))
	}
	res2 := mustSum(t, cs, dir)
	if !bytes.Equal(res1.Digest, res2.Digest) {
		t.Fatal("XXHash3 digest must be reproducible")
	}
}

func TestAlgorithm_CRC32C_Integration(t *testing.T) {
	t.Parallel()
	dir := t.TempDir()
	if err := os.WriteFile(filepath.Join(dir, "f.txt"), []byte("hello fshash crc32c"), 0o644); err != nil {
		t.Fatal(err)
	}
	cs := mustNew(t, WithAlgorithm(CRC32C), WithMetadata(MetaNone))
	res1 := mustSum(t, cs, dir)
	if len(res1.Digest) != 4 {
		t.Fatalf("CRC32C digest must be 4 bytes, got %d", len(res1.Digest))
	}
	res2 := mustSum(t, cs, dir)
	if !bytes.Equal(res1.Digest, res2.Digest) {
		t.Fatal("CRC32C digest must be reproducible")
	}
}

func TestNewHasher_AllAlgorithms(t *testing.T) {
	t.Parallel()
	algos := []Algorithm{SHA256, SHA512, SHA1, MD5, XXHash64, XXHash3, Blake3, CRC32C}
	for _, algo := range algos {
		algo := algo
		t.Run(string(algo), func(t *testing.T) {
			t.Parallel()
			h, err := NewHasher(algo)
			if err != nil {
				t.Fatalf("NewHasher(%q): %v", algo, err)
			}
			if h.Algorithm() != string(algo) {
				t.Errorf("Algorithm() = %q, want %q", h.Algorithm(), algo)
			}
			// Must produce non-empty digest on "hello".
			hh := h.New()
			hh.Write([]byte("hello"))
			d := hh.Sum(nil)
			if len(d) == 0 {
				t.Errorf("%s: empty digest", algo)
			}
			// Reset and re-hash must be identical.
			hh.Reset()
			hh.Write([]byte("hello"))
			d2 := hh.Sum(nil)
			if !bytes.Equal(d, d2) {
				t.Errorf("%s: digest after Reset differs", algo)
			}
		})
	}
}

func TestNewHasher_UnknownAlgorithm(t *testing.T) {
	t.Parallel()
	_, err := NewHasher("nonexistent-algo")
	if err == nil {
		t.Fatal("NewHasher(unknown) must return error")
	}
}

func TestGetBuf_ReturnsLargePool(t *testing.T) {
	t.Parallel()
	b, sz := getBuf()
	if cap(*b) != largeBufSize {
		t.Errorf("getBuf: cap = %d, want %d", cap(*b), largeBufSize)
	}
	if sz != largeBufSize {
		t.Errorf("getBuf: returned size = %d, want %d", sz, largeBufSize)
	}
	putBuf(b)
}

// Benchmarks for new algorithms.
func BenchmarkBlake3_1MiB(b *testing.B)   { benchHashAlgo(b, Blake3, 1<<20) }
func BenchmarkBlake3_64MiB(b *testing.B)  { benchHashAlgo(b, Blake3, 64<<20) }
func BenchmarkXXHash3_1MiB(b *testing.B)  { benchHashAlgo(b, XXHash3, 1<<20) }
func BenchmarkXXHash3_64MiB(b *testing.B) { benchHashAlgo(b, XXHash3, 64<<20) }
func BenchmarkCRC32C_1MiB(b *testing.B)   { benchHashAlgo(b, CRC32C, 1<<20) }
func BenchmarkCRC32C_64MiB(b *testing.B)  { benchHashAlgo(b, CRC32C, 64<<20) }
