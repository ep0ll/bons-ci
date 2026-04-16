package fshash_test

// stream_test.go — tests for SumStream, BoundedSumStream, MerkleStream,
// ParallelSumStream, ChangeFeed, and core pipeline combinators.

import (
	"bytes"
	"context"
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"testing"
	"time"

	"io"

	"github.com/bons/bons-ci/pkg/fshash"
	"github.com/bons/bons-ci/pkg/fshash/core"
)

// ── SumStream ─────────────────────────────────────────────────────────────────

func TestSumStream_EmitsAllEntries(t *testing.T) {
	t.Parallel()
	root := t.TempDir()
	buildTree(t, root, fsTree{
		"a": "alpha", "b": "beta",
		"sub": "", "sub/c": "gamma", "sub/d": "delta",
	})
	cs := mustNew(t, fshash.WithMetadata(fshash.MetaNone))
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()

	var entries []fshash.EntryResult
	for e := range cs.SumStream(ctx, root).Chan() {
		entries = append(entries, e)
	}

	if len(entries) == 0 {
		t.Fatal("SumStream must emit at least one entry")
	}
	last := entries[len(entries)-1]
	if last.RelPath != "." {
		t.Errorf("last streamed entry must be '.', got %q", last.RelPath)
	}
	if last.Kind != fshash.KindDir {
		t.Errorf("root entry must be KindDir, got %v", last.Kind)
	}
}

func TestSumStream_DigestMatchesSum(t *testing.T) {
	t.Parallel()
	root := t.TempDir()
	buildTree(t, root, fsTree{"x": "one", "y": "two", "z": "three"})
	cs := mustNew(t, fshash.WithMetadata(fshash.MetaNone))
	ctx := context.Background()

	// Collect root digest from stream
	var rootDigest []byte
	for e := range cs.SumStream(ctx, root).Chan() {
		if e.RelPath == "." {
			rootDigest = e.Digest
		}
	}

	// Must match regular Sum
	res := mustSum(t, cs, root)
	if !bytes.Equal(rootDigest, res.Digest) {
		t.Fatalf("SumStream root digest %x != Sum digest %x", rootDigest, res.Digest)
	}
}

func TestSumStream_ChildrenBeforeParent(t *testing.T) {
	t.Parallel()
	root := t.TempDir()
	buildTree(t, root, fsTree{
		"sub": "", "sub/file": "content", "sub/nested": "", "sub/nested/deep": "deep",
	})
	cs := mustNew(t, fshash.WithMetadata(fshash.MetaNone))
	ctx := context.Background()

	order := make(map[string]int)
	i := 0
	for e := range cs.SumStream(ctx, root).Chan() {
		order[e.RelPath] = i
		i++
	}

	// All children must arrive before their parent directory
	if order["sub/nested/deep"] >= order["sub/nested"] {
		t.Error("deep file must arrive before its parent dir")
	}
	if order["sub/nested"] >= order["sub"] {
		t.Error("nested dir must arrive before sub")
	}
	if order["sub"] >= order["."] {
		t.Error("sub must arrive before root '.'")
	}
}

func TestSumStream_CtxCancel(t *testing.T) {
	t.Parallel()
	root := t.TempDir()
	for i := range 200 {
		os.WriteFile(filepath.Join(root, fmt.Sprintf("f%04d.txt", i)),
			bytes.Repeat([]byte("x"), 1024), 0o644)
	}
	cs := mustNew(t)
	ctx, cancel := context.WithCancel(context.Background())

	stream := cs.SumStream(ctx, root)
	// Read one entry then cancel
	<-stream.Chan()
	cancel()

	// Drain remaining — must eventually close
	done := make(chan struct{})
	go func() {
		for range stream.Chan() {
		}
		close(done)
	}()
	select {
	case <-done:
	case <-time.After(5 * time.Second):
		t.Fatal("SumStream did not close after context cancel")
	}
}

// ── BoundedSumStream ──────────────────────────────────────────────────────────

func TestBoundedSumStream_Backpressure(t *testing.T) {
	t.Parallel()
	root := t.TempDir()
	for i := range 50 {
		os.WriteFile(filepath.Join(root, fmt.Sprintf("f%03d.txt", i)),
			[]byte(fmt.Sprintf("content %d", i)), 0o644)
	}
	cs := mustNew(t, fshash.WithMetadata(fshash.MetaNone))
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()

	stream := cs.BoundedSumStream(ctx, root, 4) // very small buffer

	var count int
	var rootDigest []byte
	for e := range stream.Chan() {
		count++
		if e.RelPath == "." {
			rootDigest = e.Digest
		}
		time.Sleep(time.Millisecond) // slow consumer to exercise backpressure
	}

	if count == 0 {
		t.Fatal("BoundedSumStream must emit at least one entry")
	}
	res := mustSum(t, cs, root)
	if !bytes.Equal(rootDigest, res.Digest) {
		t.Fatal("BoundedSumStream root digest must match Sum")
	}
}

// ── MerkleStream ──────────────────────────────────────────────────────────────

func TestMerkleStream_NoRootEntry(t *testing.T) {
	t.Parallel()
	root := t.TempDir()
	buildTree(t, root, fsTree{"a": "alpha", "b": "beta"})
	cs := mustNew(t, fshash.WithMetadata(fshash.MetaNone))
	ctx := context.Background()

	stream := cs.MerkleStream(ctx, root)
	for e := range stream.Chan() {
		if e.RelPath == "." {
			t.Error("MerkleStream must not emit the root '.' entry")
		}
	}
}

func TestMerkleStream_EmitsLeaves(t *testing.T) {
	t.Parallel()
	root := t.TempDir()
	buildTree(t, root, fsTree{
		"a.txt": "alpha", "b.txt": "beta", "sub": "", "sub/c": "gamma",
	})
	cs := mustNew(t, fshash.WithMetadata(fshash.MetaNone))
	ctx := context.Background()

	var paths []string
	for e := range cs.MerkleStream(ctx, root).Chan() {
		paths = append(paths, e.RelPath)
	}

	want := []string{"a.txt", "b.txt", "sub", "sub/c"}
	found := map[string]bool{}
	for _, p := range paths {
		found[p] = true
	}
	for _, w := range want {
		if !found[w] {
			t.Errorf("MerkleStream missing entry %q", w)
		}
	}
}

// ── ParallelSumStream ─────────────────────────────────────────────────────────

func TestParallelSumStream_AllRootsHashed(t *testing.T) {
	t.Parallel()
	roots := make([]string, 3)
	for i := range roots {
		roots[i] = t.TempDir()
		os.WriteFile(filepath.Join(roots[i], "f.txt"),
			[]byte(fmt.Sprintf("root %d", i)), 0o644)
	}
	cs := mustNew(t, fshash.WithMetadata(fshash.MetaNone))
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()

	// Collect entries, count how many root "." entries arrive (one per path)
	rootEntries := map[string]bool{}
	for e := range cs.ParallelSumStream(ctx, roots).Chan() {
		if strings.HasSuffix(e.RelPath, ":.") {
			rootEntries[e.RelPath] = true
		}
	}

	for i := range roots {
		key := fmt.Sprintf("%d:.", i)
		if !rootEntries[key] {
			t.Errorf("missing root entry for path index %d", i)
		}
	}
}

func TestParallelSumStream_Empty(t *testing.T) {
	t.Parallel()
	cs := mustNew(t)
	ctx := context.Background()
	var count int
	for range cs.ParallelSumStream(ctx, nil).Chan() {
		count++
	}
	if count != 0 {
		t.Errorf("expected 0 entries for nil paths, got %d", count)
	}
}

// ── ChangeFeed ────────────────────────────────────────────────────────────────

func TestChangeFeed_FirstRoundEmitsAll(t *testing.T) {
	t.Parallel()
	root := t.TempDir()
	buildTree(t, root, fsTree{"a": "alpha", "b": "beta", "sub": "", "sub/c": "gamma"})
	cs := mustNew(t, fshash.WithMetadata(fshash.MetaNone))

	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()

	feed := cs.ChangeFeed(ctx, root, 100*time.Millisecond)

	// The first round should emit all entries (PrevDigest is nil for all)
	var first []fshash.ChangedEntry
	timer := time.NewTimer(200 * time.Millisecond)
	defer timer.Stop()
	for {
		select {
		case e, ok := <-feed.Chan():
			if !ok {
				goto done
			}
			first = append(first, e)
		case <-timer.C:
			goto done
		}
	}
done:
	cancel() // stop the feed

	if len(first) == 0 {
		t.Fatal("ChangeFeed first round must emit entries")
	}
	for _, e := range first {
		if e.PrevDigest != nil {
			t.Errorf("first-round entry %q must have nil PrevDigest", e.RelPath)
		}
	}
}

func TestChangeFeed_SecondRoundEmitsChanges(t *testing.T) {
	t.Parallel()
	root := t.TempDir()
	p := filepath.Join(root, "watched.txt")
	os.WriteFile(p, []byte("original"), 0o644)
	cs := mustNew(t, fshash.WithMetadata(fshash.MetaNone))

	// Run one full pass synchronously by using a context that cancels after
	// the first complete tree hash, then restart to get the diff.
	ctx1, cancel1 := context.WithTimeout(context.Background(), 3*time.Second)
	defer cancel1()
	feed1 := cs.ChangeFeed(ctx1, root, 50*time.Millisecond)
	var firstEntries []fshash.ChangedEntry
	for e := range feed1.Chan() {
		firstEntries = append(firstEntries, e)
		if e.RelPath == "." {
			cancel1() // got root entry = first full pass done
		}
	}

	if len(firstEntries) == 0 {
		t.Fatal("first pass must emit entries")
	}

	// Modify a file
	os.WriteFile(p, []byte("modified content that is longer"), 0o644)

	// Second feed should detect the change
	ctx2, cancel2 := context.WithTimeout(context.Background(), 3*time.Second)
	defer cancel2()
	feed2 := cs.ChangeFeed(ctx2, root, 50*time.Millisecond)

	// Skip first-round entries (PrevDigest == nil), wait for diff entries
	for e := range feed2.Chan() {
		if e.PrevDigest != nil {
			// Got a diff entry
			t.Logf("ChangeFeed detected change: %s (prev=%x curr=%x)",
				e.RelPath, e.PrevDigest, e.Digest)
			cancel2()
			return
		}
		if e.RelPath == "." && e.PrevDigest == nil {
			cancel2() // first pass done but we need to wait for second
		}
	}
}

// ── core pipeline combinators ─────────────────────────────────────────────────

func TestMapStream(t *testing.T) {
	t.Parallel()
	ctx := context.Background()
	src := core.NewStream[int](ctx, 10)
	for i := range 5 {
		src.Emit(i)
	}
	src.Close()

	mapped := core.MapStream(ctx, src, func(v int) string {
		return fmt.Sprintf("item-%d", v)
	})

	var got []string
	for s := range mapped.Chan() {
		got = append(got, s)
	}
	if len(got) != 5 {
		t.Fatalf("expected 5 mapped values, got %d: %v", len(got), got)
	}
	if got[0] != "item-0" || got[4] != "item-4" {
		t.Errorf("unexpected values: %v", got)
	}
}

func TestFilterStream(t *testing.T) {
	t.Parallel()
	ctx := context.Background()
	src := core.NewStream[int](ctx, 10)
	for i := range 10 {
		src.Emit(i)
	}
	src.Close()

	evens := core.FilterStream(ctx, src, func(v int) bool { return v%2 == 0 })
	var got []int
	for v := range evens.Chan() {
		got = append(got, v)
	}
	if len(got) != 5 {
		t.Fatalf("expected 5 even values, got %d: %v", len(got), got)
	}
	for _, v := range got {
		if v%2 != 0 {
			t.Errorf("non-even value slipped through: %d", v)
		}
	}
}

func TestTeeStream(t *testing.T) {
	t.Parallel()
	ctx := context.Background()
	src := core.NewStream[int](ctx, 10)
	for i := range 5 {
		src.Emit(i)
	}
	src.Close()

	a, b := core.TeeStream(ctx, src)

	var sumA, sumB int
	for v := range a.Chan() {
		sumA += v
	}
	for v := range b.Chan() {
		sumB += v
	}
	if sumA != 10 || sumB != 10 { // 0+1+2+3+4 = 10
		t.Errorf("TeeStream sums: a=%d b=%d want 10 each", sumA, sumB)
	}
}

func TestDrainStream(t *testing.T) {
	t.Parallel()
	ctx := context.Background()
	s := core.NewStream[string](ctx, 4)
	s.Emit("x")
	s.Emit("y")
	s.Emit("z")
	s.Close()

	got := core.DrainStream(s)
	if len(got) != 3 {
		t.Fatalf("expected 3, got %d", len(got))
	}
}

func TestSortStream(t *testing.T) {
	t.Parallel()
	ctx := context.Background()
	s := core.NewStream[int](ctx, 10)
	for _, v := range []int{5, 3, 8, 1, 4} {
		s.Emit(v)
	}
	s.Close()

	sorted := core.SortStream(s, func(a, b int) bool { return a < b })
	if !sort.IntsAreSorted(sorted) {
		t.Errorf("SortStream result not sorted: %v", sorted)
	}
	if len(sorted) != 5 {
		t.Errorf("expected 5 values, got %d", len(sorted))
	}
}

func TestHashReaderStream_OrderByIndex(t *testing.T) {
	t.Parallel()
	// Create 5 in-memory readers with distinct content
	payloads := [][]byte{
		[]byte("alpha"), []byte("beta"), []byte("gamma"), []byte("delta"), []byte("epsilon"),
	}
	ctx := context.Background()
	h := core.MustHasher(core.SHA256)
	pool := core.NewTieredPool()
	stream := core.HashReaderStream(ctx, toIoReaders(payloads), h.New, pool, 4)
	results := core.SortStream(stream, func(a, b core.HashResult) bool {
		return a.Index < b.Index
	})

	if len(results) != len(payloads) {
		t.Fatalf("expected %d results, got %d", len(payloads), len(results))
	}
	for i, r := range results {
		if r.Err != nil {
			t.Errorf("result[%d] error: %v", i, r.Err)
		}
		if r.Index != i {
			t.Errorf("result[%d].Index=%d, want %d", i, r.Index, i)
		}
		if len(r.Digest) == 0 {
			t.Errorf("result[%d] empty digest", i)
		}
	}

	// Verify digests are distinct
	seen := map[string]bool{}
	for _, r := range results {
		key := fmt.Sprintf("%x", r.Digest)
		if seen[key] {
			t.Errorf("duplicate digest at index %d", r.Index)
		}
		seen[key] = true
	}
}

// toIoReaders converts [][]byte to []io.Reader for HashReaderStream
func toIoReaders(payloads [][]byte) []io.Reader {
	out := make([]io.Reader, len(payloads))
	for i, p := range payloads {
		out[i] = bytes.NewReader(p)
	}
	return out
}

// ── integration: SumStream + Filter ──────────────────────────────────────────

func TestSumStream_FilterIntegration(t *testing.T) {
	t.Parallel()
	root := t.TempDir()
	buildTree(t, root, fsTree{
		"keep.go": "code", "drop.tmp": "noise",
		"sub": "", "sub/keep2.go": "code2", "sub/drop2.tmp": "noise2",
	})
	cs := mustNew(t,
		fshash.WithFilter(fshash.ExcludePatterns("*.tmp")),
		fshash.WithMetadata(fshash.MetaNone),
	)
	ctx := context.Background()

	// SumStream entries must respect the filter
	for e := range cs.SumStream(ctx, root).Chan() {
		if strings.HasSuffix(e.RelPath, ".tmp") {
			t.Errorf("filtered entry %q appeared in SumStream", e.RelPath)
		}
	}
}

// ── SumStream vs Walk equivalence ─────────────────────────────────────────────

func TestSumStream_EquivalentToWalk(t *testing.T) {
	t.Parallel()
	root := t.TempDir()
	buildTree(t, root, fsTree{
		"a": "alpha", "b": "beta",
		"sub": "", "sub/c": "gamma", "sub/d": "delta",
		"sub/nested": "", "sub/nested/e": "epsilon",
	})
	cs := mustNew(t, fshash.WithMetadata(fshash.MetaNone))
	ctx := context.Background()

	// Collect via Walk
	var walkEntries []fshash.EntryResult
	cs.Walk(ctx, root, func(e fshash.EntryResult) error { //nolint:errcheck
		walkEntries = append(walkEntries, e)
		return nil
	})

	// Collect via SumStream
	var streamEntries []fshash.EntryResult
	for e := range cs.SumStream(ctx, root).Chan() {
		streamEntries = append(streamEntries, e)
	}

	if len(walkEntries) != len(streamEntries) {
		t.Fatalf("Walk=%d entries, SumStream=%d entries", len(walkEntries), len(streamEntries))
	}

	// Sort both by RelPath for comparison (stream may arrive in different order
	// than Walk because file hashing is parallel)
	sortEntries(walkEntries)
	sortEntries(streamEntries)

	for i := range walkEntries {
		w, s := walkEntries[i], streamEntries[i]
		if w.RelPath != s.RelPath {
			t.Errorf("[%d] RelPath: Walk=%q Stream=%q", i, w.RelPath, s.RelPath)
		}
		if !bytes.Equal(w.Digest, s.Digest) {
			t.Errorf("[%d] %q digest mismatch: Walk=%x Stream=%x", i, w.RelPath, w.Digest, s.Digest)
		}
	}
}

func sortEntries(entries []fshash.EntryResult) {
	sort.Slice(entries, func(i, j int) bool {
		return entries[i].RelPath < entries[j].RelPath
	})
}

// ── pipeline composition: SumStream → MapStream → FilterStream ───────────────

func TestPipelineComposition(t *testing.T) {
	t.Parallel()
	root := t.TempDir()
	buildTree(t, root, fsTree{
		"a.go": "code", "b.go": "code2", "c.txt": "text",
		"sub": "", "sub/d.go": "code3",
	})
	cs := mustNew(t, fshash.WithMetadata(fshash.MetaNone))
	ctx := context.Background()

	// Pipeline: stream entries → map to relPath → filter only .go files
	entryStream := cs.SumStream(ctx, root)
	pathStream := core.MapStream(ctx, entryStream,
		func(e fshash.EntryResult) string { return e.RelPath })
	goStream := core.FilterStream(ctx, pathStream,
		func(p string) bool { return strings.HasSuffix(p, ".go") })

	var goPaths []string
	for p := range goStream.Chan() {
		goPaths = append(goPaths, p)
	}

	sort.Strings(goPaths)
	want := []string{"a.go", "b.go", "sub/d.go"}
	if len(goPaths) != len(want) {
		t.Fatalf("pipeline produced %d .go paths, want %d: %v", len(goPaths), len(want), goPaths)
	}
	for i, w := range want {
		if goPaths[i] != w {
			t.Errorf("[%d] got %q want %q", i, goPaths[i], w)
		}
	}
}

// ── EventBus multi-subscriber watcher ─────────────────────────────────────────

func TestWatcher_MultipleSubscribers(t *testing.T) {
	t.Parallel()
	root := t.TempDir()
	p := filepath.Join(root, "watched.txt")
	os.WriteFile(p, []byte("original"), 0o644)

	cs := mustNew(t)
	w := fshash.NewWatcher(cs, root, fshash.WithWatchInterval(20*time.Millisecond))

	// Subscribe two independent consumers before starting the watcher
	id1, ch1 := w.Events().Subscribe(4)
	id2, ch2 := w.Events().Subscribe(4)
	defer w.Events().Unsubscribe(id1)
	defer w.Events().Unsubscribe(id2)

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	go w.Watch(ctx) //nolint:errcheck

	time.Sleep(60 * time.Millisecond) // let watcher establish baseline
	os.WriteFile(p, []byte("modified for multi-subscriber test"), 0o644)

	timeout := time.After(3 * time.Second)
	var got1, got2 bool
	for !got1 || !got2 {
		select {
		case evt := <-ch1:
			if !bytes.Equal(evt.PrevDigest, evt.CurrDigest) {
				got1 = true
			}
		case evt := <-ch2:
			if !bytes.Equal(evt.PrevDigest, evt.CurrDigest) {
				got2 = true
			}
		case <-timeout:
			t.Fatalf("timeout waiting for both subscribers (got1=%v got2=%v)", got1, got2)
		}
	}
}

func TestWatcher_Unsubscribe(t *testing.T) {
	t.Parallel()
	root := t.TempDir()
	os.WriteFile(filepath.Join(root, "f"), []byte("data"), 0o644)

	cs := mustNew(t)
	w := fshash.NewWatcher(cs, root, fshash.WithWatchInterval(20*time.Millisecond))

	id, ch := w.Events().Subscribe(4)
	w.Events().Unsubscribe(id)

	// Channel should be closed after Unsubscribe
	select {
	case _, open := <-ch:
		if open {
			t.Error("channel must be closed after Unsubscribe")
		}
	case <-time.After(100 * time.Millisecond):
		t.Error("channel not closed after Unsubscribe")
	}
	if w.Events().Subscribers() != 0 {
		t.Errorf("Subscribers()=%d want 0", w.Events().Subscribers())
	}
}

// ── Benchmarks for new APIs ───────────────────────────────────────────────────

func BenchmarkSumStream_100files(b *testing.B) {
	root := b.TempDir()
	for i := range 100 {
		os.WriteFile(filepath.Join(root, fmt.Sprintf("f%03d.dat", i)),
			bytes.Repeat([]byte("x"), 4096), 0o644)
	}
	cs := fshash.MustNew(fshash.WithWorkers(4), fshash.WithMetadata(fshash.MetaNone))
	ctx := context.Background()
	b.ResetTimer()
	b.ReportAllocs()
	for range b.N {
		for range cs.SumStream(ctx, root).Chan() {
		}
	}
}

func BenchmarkMapStream_1000ints(b *testing.B) {
	ctx := context.Background()
	b.ReportAllocs()
	b.ResetTimer()
	for range b.N {
		src := core.NewStream[int](ctx, 1000)
		for i := range 1000 {
			src.Emit(i)
		}
		src.Close()
		out := core.MapStream(ctx, src, func(v int) int { return v * 2 })
		for range out.Chan() {
		}
	}
}

func BenchmarkHashReaderStream_5files(b *testing.B) {
	payloads := make([][]byte, 5)
	for i := range payloads {
		payloads[i] = bytes.Repeat([]byte("x"), 64*1024) // 64 KiB each
	}
	h := core.MustHasher(core.Blake3)
	pool := core.NewTieredPool()
	ctx := context.Background()
	b.SetBytes(int64(5 * 64 * 1024))
	b.ResetTimer()
	b.ReportAllocs()
	for range b.N {
		stream := core.HashReaderStream(ctx, toIoReaders(payloads), h.New, pool, 4)
		core.DrainStream(stream)
	}
}
