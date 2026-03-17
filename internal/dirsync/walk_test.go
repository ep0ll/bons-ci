package dirsync

// walk_test.go – white-box unit tests for walker internals.
//
// These tests live in package dirsync (not dirsync_test) so they can construct
// a walker directly, inject stub PathFilter implementations, and observe the
// channel output without going through the public Diff API.
//
// They complement the black-box integration tests in filter_integration_test.go
// and exclusive_test.go by pinning specific internal behaviours that would be
// otherwise opaque.

import (
	"context"
	"os"
	"path/filepath"
	"sync"
	"testing"
	"time"
)

// ─── joinRel ──────────────────────────────────────────────────────────────────

func TestJoinRel_RootLevel(t *testing.T) {
	// At the root (relDir=="") we want just the name, not "./name".
	got := joinRel("", "file.txt")
	if got != "file.txt" {
		t.Errorf("joinRel(%q, %q) = %q, want %q", "", "file.txt", got, "file.txt")
	}
}

func TestJoinRel_Nested(t *testing.T) {
	got := joinRel("a/b", "file.txt")
	want := filepath.Join("a", "b", "file.txt")
	if got != want {
		t.Errorf("joinRel(%q, %q) = %q, want %q", "a/b", "file.txt", got, want)
	}
}

func TestJoinRel_OneLevel(t *testing.T) {
	got := joinRel("sub", "x.go")
	if got != "sub/x.go" {
		t.Errorf("joinRel(%q, %q) = %q, want %q", "sub", "x.go", got, "sub/x.go")
	}
}

// ─── decide ──────────────────────────────────────────────────────────────────

func TestDecide_NilFilter_AlwaysAllow(t *testing.T) {
	w := &walker{filter: nil}
	for _, path := range []string{"", "a.txt", "vendor/x.go", "a/b/c"} {
		for _, isDir := range []bool{true, false} {
			if got := w.decide(path, isDir); got != FilterAllow {
				t.Errorf("nil filter: decide(%q, %v) = %v, want Allow", path, isDir, got)
			}
		}
	}
}

func TestDecide_NopFilter_AlwaysAllow(t *testing.T) {
	w := &walker{filter: NopFilter{}}
	if got := w.decide("any/path", true); got != FilterAllow {
		t.Errorf("NopFilter: decide returned %v, want Allow", got)
	}
}

func TestDecide_IncludeFilter_SkipsNonMatch(t *testing.T) {
	ps := mustPS(t, []string{"*.go"}, true)
	w := &walker{filter: &IncludeFilter{patterns: ps}}

	if got := w.decide("main.go", false); got != FilterAllow {
		t.Errorf("main.go should Allow; got %v", got)
	}
	if got := w.decide("README.md", false); got != FilterSkip {
		t.Errorf("README.md should Skip; got %v", got)
	}
	// Directory not matching *.go → Skip (not Prune).
	if got := w.decide("src", true); got != FilterSkip {
		t.Errorf("non-matching dir should Skip, not Prune; got %v", got)
	}
}

func TestDecide_ExcludeFilter_PrunesMatchingDir(t *testing.T) {
	ps := mustPS(t, []string{"vendor"}, false)
	w := &walker{filter: &ExcludeFilter{patterns: ps}}

	if got := w.decide("vendor", true); got != FilterPrune {
		t.Errorf("vendor dir should Prune; got %v", got)
	}
	if got := w.decide("cmd", true); got != FilterAllow {
		t.Errorf("cmd dir should Allow; got %v", got)
	}
}

// ─── emitLowerOnlyDir ────────────────────────────────────────────────────────

// newTestWalker builds a minimal walker for white-box testing.
// excCh has the given buffer; comCh is unbuffered (not used in these tests).
// tracker is nil (not tested here).
func newTestWalker(t *testing.T, lower string, filter PathFilter, excBuf int) (
	*walker, chan ExclusivePath,
) {
	t.Helper()
	excCh := make(chan ExclusivePath, excBuf)
	comCh := make(chan CommonPath, 1)
	w := &walker{
		ctx:       context.Background(),
		lowerRoot: lower,
		filter:    filter,
		tracker:   nil,
		excCh:     excCh,
		comCh:     comCh,
		// pool is nil — none of these tests trigger hashing.
	}
	return w, excCh
}

// drainExclusive reads all ExclusivePaths from ch until it is drained.
// Must only be called after the producer (walker method) has returned.
func drainExclusive(ch chan ExclusivePath) []ExclusivePath {
	close(ch)
	var out []ExclusivePath
	for ep := range ch {
		out = append(out, ep)
	}
	return out
}

// TestEmitLowerOnlyDir_Empty verifies that emitLowerOnlyDir returns nil and
// emits nothing when the target directory is empty.
func TestEmitLowerOnlyDir_Empty(t *testing.T) {
	lower := t.TempDir()
	emptyDir := filepath.Join(lower, "empty")
	if err := os.MkdirAll(emptyDir, 0o755); err != nil {
		t.Fatal(err)
	}

	w, excCh := newTestWalker(t, lower, NopFilter{}, 16)
	if err := w.emitLowerOnlyDir("empty"); err != nil {
		t.Fatalf("emitLowerOnlyDir on empty dir: %v", err)
	}
	eps := drainExclusive(excCh)
	if len(eps) != 0 {
		t.Errorf("expected 0 emissions from empty dir, got %d", len(eps))
	}
}

// TestEmitLowerOnlyDir_AllowAll verifies that with NopFilter all files are
// emitted and directories are pruned (not recursed).
func TestEmitLowerOnlyDir_AllowAll(t *testing.T) {
	lower := t.TempDir()
	// Structure under lower/root/:
	//   root/a.txt
	//   root/b.txt
	//   root/sub/           (directory — emitExclusive prunes it)
	//   root/sub/c.txt
	os.MkdirAll(filepath.Join(lower, "root", "sub"), 0o755)
	os.WriteFile(filepath.Join(lower, "root", "a.txt"), []byte("a"), 0o644)
	os.WriteFile(filepath.Join(lower, "root", "b.txt"), []byte("b"), 0o644)
	os.WriteFile(filepath.Join(lower, "root", "sub", "c.txt"), []byte("c"), 0o644)

	w, excCh := newTestWalker(t, lower, NopFilter{}, 16)
	if err := w.emitLowerOnlyDir("root"); err != nil {
		t.Fatalf("emitLowerOnlyDir: %v", err)
	}
	eps := drainExclusive(excCh)

	relPaths := make(map[string]bool)
	for _, ep := range eps {
		relPaths[ep.RelPath] = ep.Pruned
	}

	// a.txt and b.txt should be emitted as non-pruned files.
	for _, name := range []string{"root/a.txt", "root/b.txt"} {
		pruned, ok := relPaths[name]
		if !ok {
			t.Errorf("%s not emitted", name)
		} else if pruned {
			t.Errorf("%s emitted as Pruned=true, want false", name)
		}
	}
	// sub/ should be emitted as a pruned dir (NopFilter → Allow for the dir).
	if pruned, ok := relPaths["root/sub"]; !ok {
		t.Error("root/sub not emitted")
	} else if !pruned {
		t.Error("root/sub should be Pruned=true")
	}
	// sub/c.txt must NOT be emitted (sub/ was pruned, children not visited).
	if _, ok := relPaths["root/sub/c.txt"]; ok {
		t.Error("root/sub/c.txt should not be emitted (parent was pruned)")
	}
}

// TestEmitLowerOnlyDir_IncludeFilter_MatchingFiles verifies that only files
// matching the IncludePattern are emitted, and directories with non-matching
// names are still recursed into (FilterSkip).
func TestEmitLowerOnlyDir_IncludeFilter_MatchingFiles(t *testing.T) {
	lower := t.TempDir()
	// root/main.go   → matches *.go → emitted
	// root/README.md → no match     → dropped
	// root/pkg/      → FilterSkip   → recurse into
	// root/pkg/util.go → matches    → emitted
	// root/pkg/doc.txt → no match   → dropped
	os.MkdirAll(filepath.Join(lower, "root", "pkg"), 0o755)
	os.WriteFile(filepath.Join(lower, "root", "main.go"), []byte("x"), 0o644)
	os.WriteFile(filepath.Join(lower, "root", "README.md"), []byte("x"), 0o644)
	os.WriteFile(filepath.Join(lower, "root", "pkg", "util.go"), []byte("x"), 0o644)
	os.WriteFile(filepath.Join(lower, "root", "pkg", "doc.txt"), []byte("x"), 0o644)

	ps := mustPS(t, []string{"*.go"}, true)
	w, excCh := newTestWalker(t, lower, &IncludeFilter{patterns: ps}, 16)

	if err := w.emitLowerOnlyDir("root"); err != nil {
		t.Fatalf("emitLowerOnlyDir: %v", err)
	}
	eps := drainExclusive(excCh)

	emitted := make(map[string]bool)
	for _, ep := range eps {
		emitted[ep.RelPath] = true
	}

	if !emitted["root/main.go"] {
		t.Error("root/main.go should be emitted")
	}
	if !emitted["root/pkg/util.go"] {
		t.Error("root/pkg/util.go should be emitted (pkg/ traversed despite not matching *.go)")
	}
	if emitted["root/README.md"] {
		t.Error("root/README.md should be filtered")
	}
	if emitted["root/pkg/doc.txt"] {
		t.Error("root/pkg/doc.txt should be filtered")
	}
	// pkg/ itself must not be emitted as a pruned dir.
	if emitted["root/pkg"] {
		t.Error("root/pkg should not be emitted (traversed for children, not pruned)")
	}
}

// TestEmitLowerOnlyDir_ExcludeFilter_PrunesSubdir verifies that a
// ExcludePattern on a sub-directory hard-stops recursion inside it.
func TestEmitLowerOnlyDir_ExcludeFilter_PrunesSubdir(t *testing.T) {
	lower := t.TempDir()
	// root/keep.go        → Allow → emitted
	// root/internal/      → FilterPrune → dropped entirely
	// root/internal/x.go  → never visited
	os.MkdirAll(filepath.Join(lower, "root", "internal"), 0o755)
	os.WriteFile(filepath.Join(lower, "root", "keep.go"), []byte("x"), 0o644)
	os.WriteFile(filepath.Join(lower, "root", "internal", "x.go"), []byte("x"), 0o644)

	ps := mustPS(t, []string{"internal"}, false)
	w, excCh := newTestWalker(t, lower, &ExcludeFilter{patterns: ps}, 16)

	if err := w.emitLowerOnlyDir("root"); err != nil {
		t.Fatalf("emitLowerOnlyDir: %v", err)
	}
	eps := drainExclusive(excCh)

	emitted := make(map[string]bool)
	for _, ep := range eps {
		emitted[ep.RelPath] = true
	}

	if !emitted["root/keep.go"] {
		t.Error("root/keep.go should be emitted")
	}
	if emitted["root/internal"] {
		t.Error("root/internal should be pruned, not emitted")
	}
	if emitted["root/internal/x.go"] {
		t.Error("root/internal/x.go should never be visited")
	}
}

// TestEmitLowerOnlyDir_Vanished verifies that a directory that disappears
// between listing and recursion is silently ignored (race-condition safety).
func TestEmitLowerOnlyDir_Vanished(t *testing.T) {
	lower := t.TempDir()
	// We call emitLowerOnlyDir on a path that doesn't exist at all.
	// It must return nil, not an error.
	w, excCh := newTestWalker(t, lower, NopFilter{}, 4)
	if err := w.emitLowerOnlyDir("nonexistent"); err != nil {
		t.Errorf("vanished dir should be silently ignored, got: %v", err)
	}
	eps := drainExclusive(excCh)
	if len(eps) != 0 {
		t.Errorf("expected 0 emissions for vanished dir, got %d", len(eps))
	}
}

// ─── compareDir context cancellation ─────────────────────────────────────────

// TestCompareDir_ContextCancelled_ReturnsCtxErr verifies that compareDir
// returns the context error promptly when ctx is cancelled before it starts.
func TestCompareDir_ContextCancelled_ReturnsCtxErr(t *testing.T) {
	lower := t.TempDir()
	upper := t.TempDir()

	// Write enough files to make the loop non-trivial.
	for i := 0; i < 20; i++ {
		os.WriteFile(filepath.Join(lower, "f"+string(rune('a'+i))+".txt"), []byte("x"), 0o644)
	}

	ctx, cancel := context.WithCancel(context.Background())
	cancel() // cancel immediately

	excCh := make(chan ExclusivePath, 4)
	comCh := make(chan CommonPath, 4)
	w := &walker{
		ctx:       ctx,
		lowerRoot: lower,
		upperRoot: upper,
		filter:    NopFilter{},
		excCh:     excCh,
		comCh:     comCh,
	}

	err := w.compareDir("")
	if err == nil {
		t.Error("expected non-nil error from cancelled context")
	}
	if err != ctx.Err() {
		t.Errorf("expected ctx.Err() = %v, got %v", ctx.Err(), err)
	}
}

// TestCompareDir_LowerMissing_ReturnsError verifies that compareDir returns an
// error when the lower directory does not exist (not a recoverable race).
func TestCompareDir_LowerMissing_ReturnsError(t *testing.T) {
	upper := t.TempDir()

	excCh := make(chan ExclusivePath, 4)
	comCh := make(chan CommonPath, 4)
	w := &walker{
		ctx:       context.Background(),
		lowerRoot: "/nonexistent_lower_" + t.Name(),
		upperRoot: upper,
		filter:    NopFilter{},
		excCh:     excCh,
		comCh:     comCh,
	}

	if err := w.compareDir(""); err == nil {
		t.Error("expected error for missing lower root")
	}
}

// ─── emitExclusive ────────────────────────────────────────────────────────────

// TestEmitExclusive_File verifies AbsPath, IsDir=false, Pruned=false for files.
func TestEmitExclusive_File(t *testing.T) {
	lower := t.TempDir()
	p := filepath.Join(lower, "file.txt")
	os.WriteFile(p, []byte("x"), 0o644)

	info, err := os.Lstat(p)
	if err != nil {
		t.Fatal(err)
	}

	excCh := make(chan ExclusivePath, 2)
	w := &walker{
		ctx:       context.Background(),
		lowerRoot: lower,
		filter:    NopFilter{},
		excCh:     excCh,
	}

	if err := w.emitExclusive("file.txt", dirEntry{info: info}); err != nil {
		t.Fatalf("emitExclusive: %v", err)
	}
	close(excCh)

	ep := <-excCh
	if ep.RelPath != "file.txt" {
		t.Errorf("RelPath = %q, want %q", ep.RelPath, "file.txt")
	}
	if ep.AbsPath != p {
		t.Errorf("AbsPath = %q, want %q", ep.AbsPath, p)
	}
	if ep.IsDir {
		t.Error("IsDir should be false for a regular file")
	}
	if ep.Pruned {
		t.Error("Pruned should be false for a regular file")
	}
}

// TestEmitExclusive_Dir verifies AbsPath, IsDir=true, Pruned=true for dirs.
func TestEmitExclusive_Dir(t *testing.T) {
	lower := t.TempDir()
	d := filepath.Join(lower, "subdir")
	os.Mkdir(d, 0o755)

	info, err := os.Lstat(d)
	if err != nil {
		t.Fatal(err)
	}

	excCh := make(chan ExclusivePath, 2)
	w := &walker{
		ctx:       context.Background(),
		lowerRoot: lower,
		filter:    NopFilter{},
		excCh:     excCh,
	}

	if err := w.emitExclusive("subdir", dirEntry{info: info}); err != nil {
		t.Fatalf("emitExclusive: %v", err)
	}
	close(excCh)

	ep := <-excCh
	if !ep.IsDir {
		t.Error("IsDir should be true for a directory")
	}
	if !ep.Pruned {
		t.Error("Pruned should be true for a directory")
	}
}

// TestEmitExclusive_TrackerMarked verifies that emitExclusive calls markSeen on
// the tracker before sending to the channel.
func TestEmitExclusive_TrackerMarked(t *testing.T) {
	lower := t.TempDir()
	p := filepath.Join(lower, "go.mod")
	os.WriteFile(p, []byte("module test"), 0o644)

	info, _ := os.Lstat(p)
	tr := newRequiredTracker([]string{"go.mod"})

	excCh := make(chan ExclusivePath, 2)
	w := &walker{
		ctx:       context.Background(),
		lowerRoot: lower,
		filter:    NopFilter{},
		tracker:   tr,
		excCh:     excCh,
	}

	if err := w.emitExclusive("go.mod", dirEntry{info: info}); err != nil {
		t.Fatal(err)
	}
	close(excCh)
	<-excCh // drain

	// Tracker must have been marked before the goroutine reads the channel.
	if m := tr.missingPaths(); m != nil {
		t.Errorf("tracker should have seen go.mod; still missing: %v", m)
	}
}

// ─── concurrent excCh stress ─────────────────────────────────────────────────

// TestEmitLowerOnlyDir_ConcurrentConsumer verifies that emitLowerOnlyDir
// and a concurrent channel consumer don't race (run with -race).
func TestEmitLowerOnlyDir_ConcurrentConsumer(t *testing.T) {
	lower := t.TempDir()
	dir := filepath.Join(lower, "root")
	os.Mkdir(dir, 0o755)
	for i := 0; i < 30; i++ {
		name := "file_" + string(rune('a'+i%26)) + ".go"
		os.WriteFile(filepath.Join(dir, name), []byte("x"), 0o644)
	}

	excCh := make(chan ExclusivePath, 4) // small buffer to maximise contention
	w := &walker{
		ctx:       context.Background(),
		lowerRoot: lower,
		filter:    NopFilter{},
		excCh:     excCh,
	}

	var (
		wg    sync.WaitGroup
		count int
		mu    sync.Mutex
	)

	// Consumer goroutine.
	wg.Add(1)
	go func() {
		defer wg.Done()
		for ep := range excCh {
			_ = ep
			mu.Lock()
			count++
			mu.Unlock()
		}
	}()

	// Producer.
	if err := w.emitLowerOnlyDir("root"); err != nil {
		t.Errorf("emitLowerOnlyDir: %v", err)
	}
	close(excCh)
	wg.Wait()

	if count == 0 {
		t.Error("expected at least one exclusive path")
	}
}

// ─── mtime-precision fast-path ────────────────────────────────────────────────

// TestSameMetadata_NanosecondPrecision verifies that two files with the same
// content but different nanosecond mtimes fail the fast path and get hashed.
func TestSameMetadata_NanosecondPrecision(t *testing.T) {
	lower := t.TempDir()
	upper := t.TempDir()

	base := time.Date(2024, 1, 1, 12, 0, 0, 0, time.UTC)
	t1 := base
	t2 := base.Add(1) // 1 nanosecond difference

	p1 := filepath.Join(lower, "f.txt")
	p2 := filepath.Join(upper, "f.txt")
	os.WriteFile(p1, []byte("data"), 0o644)
	os.WriteFile(p2, []byte("data"), 0o644)
	os.Chtimes(p1, t1, t1)
	os.Chtimes(p2, t2, t2)

	i1, _ := os.Lstat(p1)
	i2, _ := os.Lstat(p2)

	// Filesystems may or may not preserve nanosecond precision.
	// If they differ at nanosecond level: sameMetadata should return false.
	// If the filesystem truncates to second: they'll be equal (acceptable).
	if i1.ModTime().Equal(i2.ModTime()) {
		t.Log("filesystem does not preserve nanoseconds — fast-path will fire (expected on some FS)")
		return
	}
	if sameMetadata(i1, i2) {
		t.Error("files with different nanosecond mtime should not be sameMetadata")
	}
}

// TestSameMetadata_SameInode verifies that hard-linked files are always
// identified as same-metadata regardless of mtime.
func TestSameMetadata_SameInode(t *testing.T) {
	lower := t.TempDir()

	src := filepath.Join(lower, "src.txt")
	lnk := filepath.Join(lower, "link.txt")
	os.WriteFile(src, []byte("data"), 0o644)
	if err := os.Link(src, lnk); err != nil {
		t.Skip("hard links not supported on this filesystem:", err)
	}

	// Give them different mtimes so the size+mtime heuristic would fail.
	t1 := time.Date(2024, 1, 1, 0, 0, 0, 0, time.UTC)
	t2 := time.Date(2025, 6, 1, 0, 0, 0, 0, time.UTC)
	os.Chtimes(src, t1, t1)
	os.Chtimes(lnk, t2, t2)

	i1, _ := os.Lstat(src)
	i2, _ := os.Lstat(lnk)

	if !sameMetadata(i1, i2) {
		t.Error("hard-linked files must be sameMetadata (same inode)")
	}
}
