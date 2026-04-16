package core_test

import (
	"bytes"
	"context"
	"crypto/sha256"
	"fmt"
	"hash"
	"io"
	"io/fs"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/bons/bons-ci/pkg/fshash/core"
)

// ── Registry / algorithm tests ────────────────────────────────────────────────

func TestRegistry_AllBuiltins(t *testing.T) {
	t.Parallel()
	algos := []core.Algorithm{
		core.SHA256, core.SHA512, core.SHA1, core.MD5,
		core.XXHash64, core.XXHash3, core.Blake3, core.CRC32C,
	}
	for _, algo := range algos {
		algo := algo
		t.Run(string(algo), func(t *testing.T) {
			t.Parallel()
			h, err := core.NewHasher(algo)
			if err != nil {
				t.Fatalf("NewHasher(%q): %v", algo, err)
			}
			if h.Algorithm() != string(algo) {
				t.Errorf("Algorithm()=%q want %q", h.Algorithm(), algo)
			}
			if h.DigestSize() == 0 {
				t.Error("DigestSize must be > 0")
			}
			hh := h.New()
			hh.Write([]byte("hello"))
			d1 := hh.Sum(nil)
			if len(d1) != h.DigestSize() {
				t.Errorf("digest len %d != DigestSize %d", len(d1), h.DigestSize())
			}
			hh.Reset()
			hh.Write([]byte("hello"))
			d2 := hh.Sum(nil)
			if !bytes.Equal(d1, d2) {
				t.Error("digest after Reset must match fresh digest")
			}
		})
	}
}

func TestRegistry_UnknownAlgorithm(t *testing.T) {
	t.Parallel()
	_, err := core.NewHasher("no-such-algo")
	if err == nil {
		t.Fatal("expected error for unknown algorithm")
	}
}

func TestRegistry_MustGet_Panic(t *testing.T) {
	t.Parallel()
	defer func() {
		if r := recover(); r == nil {
			t.Fatal("MustGet with bad algo must panic")
		}
	}()
	core.DefaultRegistry.MustGet("bad-algo")
}

func TestRegistry_CustomAlgorithm(t *testing.T) {
	t.Parallel()
	r := core.NewRegistry()
	r.Register("noop", &noopHasher{})
	h, err := r.Get("noop")
	if err != nil {
		t.Fatal(err)
	}
	if h.Algorithm() != "noop" {
		t.Errorf("got %q", h.Algorithm())
	}
}

func TestRegistry_Algorithms_Listed(t *testing.T) {
	t.Parallel()
	algos := core.DefaultRegistry.Algorithms()
	if len(algos) < 8 {
		t.Errorf("expected at least 8 algorithms, got %d", len(algos))
	}
}

// ── DigestSize correctness ────────────────────────────────────────────────────

func TestDigestSizes(t *testing.T) {
	t.Parallel()
	cases := []struct {
		algo core.Algorithm
		want int
	}{
		{core.SHA256, 32}, {core.SHA512, 64}, {core.SHA1, 20}, {core.MD5, 16},
		{core.XXHash64, 8}, {core.XXHash3, 8}, {core.Blake3, 32}, {core.CRC32C, 4},
	}
	for _, tc := range cases {
		h := core.MustHasher(tc.algo)
		if h.DigestSize() != tc.want {
			t.Errorf("%s: DigestSize=%d want %d", tc.algo, h.DigestSize(), tc.want)
		}
		hh := h.New()
		hh.Write([]byte("x"))
		if got := len(hh.Sum(nil)); got != tc.want {
			t.Errorf("%s: actual digest len=%d want %d", tc.algo, got, tc.want)
		}
	}
}

// ── Known test vectors ────────────────────────────────────────────────────────

func TestBlake3_EmptyVector(t *testing.T) {
	t.Parallel()
	h := core.MustHasher(core.Blake3).New()
	got := fmt.Sprintf("%x", h.Sum(nil))
	want := "af1349b9f5f9a1a6a0404dea36dcc9499bcb25c9adc112b7cc9a93cae41f3262"
	if got != want {
		t.Errorf("Blake3 empty:\n got  %s\n want %s", got, want)
	}
}

func TestCRC32C_KnownVector(t *testing.T) {
	t.Parallel()
	h := core.MustHasher(core.CRC32C).New()
	h.Write([]byte("123456789"))
	d := h.Sum(nil)
	got := uint32(d[0])<<24 | uint32(d[1])<<16 | uint32(d[2])<<8 | uint32(d[3])
	const want = uint32(0xE3069283)
	if got != want {
		t.Errorf("CRC32C(\"123456789\") = %08X, want %08X", got, want)
	}
}

func TestSHA256_EmptyVector(t *testing.T) {
	t.Parallel()
	h := core.MustHasher(core.SHA256).New()
	got := fmt.Sprintf("%x", h.Sum(nil))
	want := fmt.Sprintf("%x", sha256.New().Sum(nil))
	if got != want {
		t.Errorf("SHA256 empty mismatch: %s vs %s", got, want)
	}
}

// ── All algorithms produce distinct outputs ───────────────────────────────────

func TestAllAlgorithms_Distinct(t *testing.T) {
	t.Parallel()
	algos := []core.Algorithm{
		core.SHA256, core.SHA512, core.SHA1, core.MD5,
		core.XXHash64, core.XXHash3, core.Blake3, core.CRC32C,
	}
	seen := map[string]core.Algorithm{}
	for _, algo := range algos {
		h := core.MustHasher(algo).New()
		h.Write([]byte("test data for collision check"))
		d := fmt.Sprintf("%x|%d", h.Sum(nil), len(h.Sum(nil)))
		if prev, ok := seen[d]; ok {
			t.Errorf("collision between %s and %s", algo, prev)
		}
		seen[d] = algo
	}
}

// ── TieredPool ────────────────────────────────────────────────────────────────

func TestTieredPool_TierSelection(t *testing.T) {
	t.Parallel()
	p := core.NewTieredPool()
	cases := []struct {
		size    int64
		wantCap int
	}{
		{-1, core.SmallBufSize},
		{0, core.SmallBufSize},
		{1, core.SmallBufSize},
		{int64(core.SmallBufSize), core.SmallBufSize},
		{int64(core.SmallBufSize) + 1, core.MediumBufSize},
		{int64(core.MediumBufSize), core.MediumBufSize},
		{int64(core.MediumBufSize) + 1, core.LargeBufSize},
		{int64(core.LargeBufSize), core.LargeBufSize},
		{int64(core.LargeBufSize) + 1, core.XLargeBufSize},
	}
	for _, tc := range cases {
		b := p.Get(tc.size)
		if cap(*b) != tc.wantCap {
			t.Errorf("Get(%d): cap=%d want %d", tc.size, cap(*b), tc.wantCap)
		}
		p.Put(b)
	}
}

func TestTieredPool_GetStream(t *testing.T) {
	t.Parallel()
	p := core.NewTieredPool()
	b := p.GetStream()
	if cap(*b) != core.LargeBufSize {
		t.Errorf("GetStream: cap=%d want %d", cap(*b), core.LargeBufSize)
	}
	p.Put(b)
}

// ── DigestSink ────────────────────────────────────────────────────────────────

func TestDigestSink_CorrectOutput(t *testing.T) {
	t.Parallel()
	h := core.MustHasher(core.SHA256).New()
	h.Write([]byte("test"))
	var s core.DigestSink
	d := s.Sum(h)
	if len(d) != 32 {
		t.Fatalf("expected 32 bytes, got %d", len(d))
	}
}

func TestCloneDigest_Independence(t *testing.T) {
	t.Parallel()
	orig := []byte{1, 2, 3, 4}
	c := core.CloneDigest(orig)
	orig[0] = 0xFF
	if c[0] == 0xFF {
		t.Fatal("CloneDigest must return an independent copy")
	}
}

// ── WriteString / MustWrite ───────────────────────────────────────────────────

func TestWriteString_MatchesWriteBytes(t *testing.T) {
	t.Parallel()
	s := "hello, core package"
	h1 := core.MustHasher(core.SHA256).New()
	h2 := core.MustHasher(core.SHA256).New()
	core.WriteString(h1, s)
	h2.Write([]byte(s))
	if !bytes.Equal(h1.Sum(nil), h2.Sum(nil)) {
		t.Fatal("WriteString must produce same output as Write([]byte(s))")
	}
}

func TestWriteString_Empty_Noop(t *testing.T) {
	t.Parallel()
	h1 := core.MustHasher(core.SHA256).New()
	h2 := core.MustHasher(core.SHA256).New()
	core.WriteString(h1, "")
	if !bytes.Equal(h1.Sum(nil), h2.Sum(nil)) {
		t.Fatal("WriteString('') must be a no-op")
	}
}

func TestMustWrite_PanicsOnError(t *testing.T) {
	t.Parallel()
	defer func() {
		if r := recover(); r == nil {
			t.Fatal("MustWrite must panic on write error")
		}
	}()
	core.MustWrite(&errHash{}, []byte("data"))
}

// ── MetaFlag / WriteMetaHeader ────────────────────────────────────────────────

func TestWriteMetaHeader_None_Noop(t *testing.T) {
	t.Parallel()
	fi := &fakeFileInfo{size: 100}
	h1 := core.MustHasher(core.SHA256).New()
	h2 := core.MustHasher(core.SHA256).New()
	core.WriteMetaHeader(h1, fi, core.MetaNone, "")
	if !bytes.Equal(h1.Sum(nil), h2.Sum(nil)) {
		t.Fatal("WriteMetaHeader(MetaNone) must write nothing")
	}
}

func TestWriteMetaHeader_DifferentMeta_Differ(t *testing.T) {
	t.Parallel()
	fi1 := &fakeFileInfo{size: 100, mode: 0o644}
	fi2 := &fakeFileInfo{size: 200, mode: 0o755}
	h1 := core.MustHasher(core.SHA256).New()
	core.WriteMetaHeader(h1, fi1, core.MetaModeAndSize, "")
	h2 := core.MustHasher(core.SHA256).New()
	core.WriteMetaHeader(h2, fi2, core.MetaModeAndSize, "")
	if bytes.Equal(h1.Sum(nil), h2.Sum(nil)) {
		t.Fatal("different meta must produce different header digests")
	}
}

func TestWriteMetaHeader_SymlinkTarget_Differs(t *testing.T) {
	t.Parallel()
	fi := &fakeFileInfo{size: 0}
	h1 := core.MustHasher(core.SHA256).New()
	h2 := core.MustHasher(core.SHA256).New()
	core.WriteMetaHeader(h1, fi, core.MetaSymlink, "/some/target")
	core.WriteMetaHeader(h2, fi, core.MetaSymlink, "/other/target")
	if bytes.Equal(h1.Sum(nil), h2.Sum(nil)) {
		t.Fatal("different symlink targets must produce different header digests")
	}
}

// ── WorkerPool ────────────────────────────────────────────────────────────────

func TestWorkerPool_AllJobsRun(t *testing.T) {
	t.Parallel()
	var counter int64
	wp := core.NewWorkerPool(4)
	var wg sync.WaitGroup
	for range 100 {
		wg.Add(1)
		wp.Submit(func() {
			defer wg.Done()
			atomic.AddInt64(&counter, 1)
		})
	}
	wg.Wait()
	wp.Stop()
	if counter != 100 {
		t.Errorf("expected 100 jobs, got %d", counter)
	}
}

func TestWorkerPool_DefaultWorkers(t *testing.T) {
	t.Parallel()
	wp := core.NewWorkerPool(0)
	if wp.Workers() < 1 {
		t.Error("Workers() must be >= 1")
	}
	wp.Stop()
}

func TestWorkerPool_CapAt64(t *testing.T) {
	t.Parallel()
	wp := core.NewWorkerPool(1000)
	if wp.Workers() != 64 {
		t.Errorf("Workers() should be capped at 64, got %d", wp.Workers())
	}
	wp.Stop()
}

// ── Stream[T] ─────────────────────────────────────────────────────────────────

func TestStream_EmitAndReceive(t *testing.T) {
	t.Parallel()
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	s := core.NewStream[int](ctx, 10)
	for i := range 5 {
		s.Emit(i)
	}
	s.Close()
	var got []int
	for v := range s.Chan() {
		got = append(got, v)
	}
	if len(got) != 5 {
		t.Errorf("expected 5 values, got %d", len(got))
	}
}

func TestStream_ClosedOnCtxCancel(t *testing.T) {
	t.Parallel()
	ctx, cancel := context.WithCancel(context.Background())
	s := core.NewStream[string](ctx, 0)
	cancel()
	time.Sleep(20 * time.Millisecond)
	ok := s.Emit("after-cancel")
	if ok {
		t.Error("Emit after context cancel should return false")
	}
}

func TestStream_TryEmit_FullBuffer(t *testing.T) {
	t.Parallel()
	s := core.NewStream[int](context.Background(), 1)
	s.Emit(1) // fill
	emitted, open := s.TryEmit(2)
	if !open {
		t.Fatal("stream should be open")
	}
	if emitted {
		t.Error("TryEmit on full buffer should return emitted=false")
	}
	s.Close()
}

// ── EventBus ──────────────────────────────────────────────────────────────────

func TestEventBus_MultipleSubscribers(t *testing.T) {
	t.Parallel()
	bus := core.NewEventBus[int]()
	defer bus.Close()

	const N = 4
	chs := make([]<-chan int, N)
	ids := make([]uint64, N)
	for i := range N {
		ids[i], chs[i] = bus.Subscribe(4)
	}
	bus.Publish(99)
	for i, ch := range chs {
		select {
		case v := <-ch:
			if v != 99 {
				t.Errorf("sub %d: got %d want 99", i, v)
			}
		case <-time.After(100 * time.Millisecond):
			t.Errorf("sub %d: timeout", i)
		}
	}
}

func TestEventBus_UnsubscribeClosesChannel(t *testing.T) {
	t.Parallel()
	bus := core.NewEventBus[string]()
	id, ch := bus.Subscribe(4)
	bus.Unsubscribe(id)
	select {
	case _, ok := <-ch:
		if ok {
			t.Error("channel should be closed after Unsubscribe")
		}
	case <-time.After(100 * time.Millisecond):
		t.Error("channel not closed")
	}
}

// ── CombineShards ─────────────────────────────────────────────────────────────

func TestCombineShards_Deterministic(t *testing.T) {
	t.Parallel()
	shards := [][]byte{{1, 2, 3}, {4, 5, 6}, {7, 8, 9}}
	d1 := core.CombineShards(core.MustHasher(core.SHA256).New(), shards)
	d2 := core.CombineShards(core.MustHasher(core.SHA256).New(), shards)
	if !bytes.Equal(d1, d2) {
		t.Fatal("CombineShards must be deterministic")
	}
}

func TestCombineShards_OrderSensitive(t *testing.T) {
	t.Parallel()
	d1 := core.CombineShards(core.MustHasher(core.SHA256).New(), [][]byte{{1}, {2}})
	d2 := core.CombineShards(core.MustHasher(core.SHA256).New(), [][]byte{{2}, {1}})
	if bytes.Equal(d1, d2) {
		t.Fatal("CombineShards must be order-sensitive")
	}
}

// ── Benchmarks ────────────────────────────────────────────────────────────────

func BenchmarkBlake3_1MiB(b *testing.B)   { benchAlgo(b, core.Blake3, 1<<20) }
func BenchmarkXXHash3_1MiB(b *testing.B)  { benchAlgo(b, core.XXHash3, 1<<20) }
func BenchmarkXXHash64_1MiB(b *testing.B) { benchAlgo(b, core.XXHash64, 1<<20) }
func BenchmarkSHA256_1MiB(b *testing.B)   { benchAlgo(b, core.SHA256, 1<<20) }
func BenchmarkCRC32C_1MiB(b *testing.B)   { benchAlgo(b, core.CRC32C, 1<<20) }
func BenchmarkBlake3_64MiB(b *testing.B)  { benchAlgo(b, core.Blake3, 64<<20) }
func BenchmarkXXHash3_64MiB(b *testing.B) { benchAlgo(b, core.XXHash3, 64<<20) }

func benchAlgo(b *testing.B, algo core.Algorithm, sz int) {
	b.Helper()
	data := bytes.Repeat([]byte("A"), sz)
	h := core.MustHasher(algo)
	b.SetBytes(int64(sz))
	b.ResetTimer()
	b.ReportAllocs()
	for range b.N {
		hh := h.New()
		hh.Write(data)
		hh.Sum(nil)
	}
}

func BenchmarkTieredPool_SmallGet(b *testing.B) {
	p := core.NewTieredPool()
	b.ReportAllocs()
	b.ResetTimer()
	for range b.N {
		buf := p.Get(1024)
		p.Put(buf)
	}
}

// ── helpers ───────────────────────────────────────────────────────────────────

type noopHasher struct{}

func (noopHasher) New() hash.Hash    { return sha256.New() }
func (noopHasher) Algorithm() string { return "noop" }
func (noopHasher) DigestSize() int   { return 32 }

type errHash struct{}

func (*errHash) Write(_ []byte) (int, error) { return 0, fmt.Errorf("forced error") }
func (*errHash) Sum(b []byte) []byte         { return b }
func (*errHash) Reset()                      {}
func (*errHash) Size() int                   { return 0 }
func (*errHash) BlockSize() int              { return 1 }

type fakeFileInfo struct {
	size int64
	mode fs.FileMode
}

func (f *fakeFileInfo) Name() string       { return "fake" }
func (f *fakeFileInfo) Size() int64        { return f.size }
func (f *fakeFileInfo) Mode() fs.FileMode  { return f.mode }
func (f *fakeFileInfo) ModTime() time.Time { return time.Time{} }
func (f *fakeFileInfo) IsDir() bool        { return false }
func (f *fakeFileInfo) Sys() any           { return nil }

// ── Pipeline combinators ──────────────────────────────────────────────────────

func TestMapStream(t *testing.T) {
	t.Parallel()
	ctx := context.Background()
	src := core.NewStream[int](ctx, 10)
	for i := range 5 {
		src.Emit(i)
	}
	src.Close()
	mapped := core.MapStream(ctx, src, func(v int) int { return v * v })
	want := []int{0, 1, 4, 9, 16}
	got := core.DrainStream(mapped)
	if len(got) != len(want) {
		t.Fatalf("expected %v got %v", want, got)
	}
	for i, v := range got {
		if v != want[i] {
			t.Errorf("[%d] got %d want %d", i, v, want[i])
		}
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
	got := core.DrainStream(evens)
	if len(got) != 5 {
		t.Fatalf("expected 5 evens, got %v", got)
	}
}

func TestTeeStream(t *testing.T) {
	t.Parallel()
	ctx := context.Background()
	src := core.NewStream[int](ctx, 8)
	for _, v := range []int{1, 2, 3} {
		src.Emit(v)
	}
	src.Close()
	a, b := core.TeeStream(ctx, src)
	ga := core.DrainStream(a)
	gb := core.DrainStream(b)
	if len(ga) != 3 || len(gb) != 3 {
		t.Errorf("tee: a=%d b=%d (want 3 each)", len(ga), len(gb))
	}
}

func TestSortStream(t *testing.T) {
	t.Parallel()
	ctx := context.Background()
	s := core.NewStream[int](ctx, 8)
	for _, v := range []int{5, 1, 3, 2, 4} {
		s.Emit(v)
	}
	s.Close()
	sorted := core.SortStream(s, func(a, b int) bool { return a < b })
	for i := 1; i < len(sorted); i++ {
		if sorted[i] < sorted[i-1] {
			t.Fatalf("not sorted: %v", sorted)
		}
	}
}

func TestHashReaderStream_Deterministic(t *testing.T) {
	t.Parallel()
	payloads := [][]byte{
		[]byte("alpha"), []byte("beta"), []byte("gamma"),
	}
	readers := func() []io.Reader {
		r := make([]io.Reader, len(payloads))
		for i, p := range payloads {
			r[i] = bytes.NewReader(p)
		}
		return r
	}
	h := core.MustHasher(core.SHA256)
	pool := core.NewTieredPool()
	ctx := context.Background()

	r1 := core.SortStream(
		core.HashReaderStream(ctx, readers(), h.New, pool, 2),
		func(a, b core.HashResult) bool { return a.Index < b.Index },
	)
	r2 := core.SortStream(
		core.HashReaderStream(ctx, readers(), h.New, pool, 2),
		func(a, b core.HashResult) bool { return a.Index < b.Index },
	)
	for i := range r1 {
		if r1[i].Err != nil {
			t.Fatalf("[%d] err: %v", i, r1[i].Err)
		}
		if !bytes.Equal(r1[i].Digest, r2[i].Digest) {
			t.Errorf("[%d] digests differ", i)
		}
	}
}

func TestStream_Ctx_Accessible(t *testing.T) {
	t.Parallel()
	s := core.NewStream[int](context.Background(), 4)
	defer s.Close()
	if s.Ctx() == nil {
		t.Fatal("Ctx() must not be nil")
	}
}

func TestEventBus_Subscribers_Count(t *testing.T) {
	t.Parallel()
	bus := core.NewEventBus[string]()
	if bus.Subscribers() != 0 {
		t.Fatalf("want 0, got %d", bus.Subscribers())
	}
	id, _ := bus.Subscribe(2)
	if bus.Subscribers() != 1 {
		t.Fatalf("want 1, got %d", bus.Subscribers())
	}
	bus.Unsubscribe(id)
	if bus.Subscribers() != 0 {
		t.Fatalf("want 0 after unsub, got %d", bus.Subscribers())
	}
}

func TestHashSharded_Correctness(t *testing.T) {
	t.Parallel()
	// Create a synthetic ReaderAt from a byte slice
	data := bytes.Repeat([]byte("ABCDE"), core.ShardSize/5+100)
	ra := &bytesReaderAt{data: data}
	pool := core.NewTieredPool()
	h := core.MustHasher(core.SHA256)
	shards, err := core.HashSharded(ra, int64(len(data)), h.New, pool, 4)
	if err != nil {
		t.Fatalf("HashSharded: %v", err)
	}
	if len(shards) == 0 {
		t.Fatal("expected at least one shard digest")
	}
	// Deterministic: same data + same hash = same shards
	shards2, _ := core.HashSharded(ra, int64(len(data)), h.New, pool, 2)
	if len(shards) != len(shards2) {
		t.Fatalf("shard count mismatch: %d vs %d", len(shards), len(shards2))
	}
	for i := range shards {
		if !bytes.Equal(shards[i], shards2[i]) {
			t.Errorf("shard[%d] differs between worker counts", i)
		}
	}
}

// bytesReaderAt implements core.ReaderAt over a []byte for testing.
type bytesReaderAt struct{ data []byte }

func (r *bytesReaderAt) ReadAt(p []byte, off int64) (int, error) {
	if off >= int64(len(r.data)) {
		return 0, io.EOF
	}
	n := copy(p, r.data[off:])
	return n, nil
}
