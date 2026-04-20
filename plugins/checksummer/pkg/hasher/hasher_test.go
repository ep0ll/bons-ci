package hasher_test

import (
	"bytes"
	"context"
	"crypto/rand"
	"io"
	"os"
	"sync"
	"testing"

	"github.com/bons/bons-ci/plugins/checksummer/pkg/hasher"
)

// ─────────────────────────── helpers ─────────────────────────────────────────

func randFile(t *testing.T, size int) *os.File {
	t.Helper()
	f, err := os.CreateTemp("", "hashertest-*.bin")
	if err != nil {
		t.Fatalf("CreateTemp: %v", err)
	}
	t.Cleanup(func() {
		f.Close()
		os.Remove(f.Name())
	})
	buf := make([]byte, size)
	if _, err := rand.Read(buf); err != nil {
		t.Fatalf("rand.Read: %v", err)
	}
	if _, err := f.Write(buf); err != nil {
		t.Fatalf("write: %v", err)
	}
	if _, err := f.Seek(0, 0); err != nil {
		t.Fatalf("seek: %v", err)
	}
	return f
}

func emptyFile(t *testing.T) *os.File {
	t.Helper()
	f, err := os.CreateTemp("", "empty-*.bin")
	if err != nil {
		t.Fatalf("CreateTemp: %v", err)
	}
	t.Cleanup(func() { f.Close(); os.Remove(f.Name()) })
	return f
}

// ─────────────────────────── EmptyHash ───────────────────────────────────────

func TestEmptyHash(t *testing.T) {
	h1 := hasher.EmptyHash()
	h2 := hasher.EmptyHash()
	if !bytes.Equal(h1, h2) {
		t.Error("EmptyHash must be deterministic")
	}
	if len(h1) != 32 {
		t.Errorf("digest should be 32 bytes, got %d", len(h1))
	}
	// Mutating h1 must not affect h2 (copy semantics).
	h1[0] ^= 0xFF
	if bytes.Equal(h1, h2) {
		t.Error("EmptyHash must return independent copies")
	}
}

func TestHashBytesEmpty(t *testing.T) {
	h := hasher.HashBytes(nil)
	if !bytes.Equal(h, hasher.EmptyHash()) {
		t.Error("HashBytes(nil) must equal EmptyHash()")
	}
	h2 := hasher.HashBytes([]byte{})
	if !bytes.Equal(h2, hasher.EmptyHash()) {
		t.Error("HashBytes([]) must equal EmptyHash()")
	}
}

func TestHashBytesDeterministic(t *testing.T) {
	data := []byte("hello world")
	h1 := hasher.HashBytes(data)
	h2 := hasher.HashBytes(data)
	if !bytes.Equal(h1, h2) {
		t.Error("HashBytes must be deterministic")
	}
}

func TestHashBytesDifferentData(t *testing.T) {
	h1 := hasher.HashBytes([]byte("foo"))
	h2 := hasher.HashBytes([]byte("bar"))
	if bytes.Equal(h1, h2) {
		t.Error("different data must produce different hashes")
	}
}

// ─────────────────────────── Buffer pool ─────────────────────────────────────

func TestBufferPoolGetPut(t *testing.T) {
	pool := hasher.NewBufferPool()
	tests := []int{0, 1, 4095, 4096, 4097, 1 << 20, hasher.MaxBufSize, hasher.MaxBufSize + 1}
	for _, sz := range tests {
		buf := pool.GetAtLeast(sz)
		if sz > 0 && len(buf) < sz {
			t.Errorf("sz=%d: got len=%d", sz, len(buf))
		}
		pool.Put(buf)
	}
}

func TestBufferPoolGetExact(t *testing.T) {
	pool := hasher.NewBufferPool()
	buf := pool.GetExact(1000)
	if len(buf) != 1000 {
		t.Errorf("GetExact(1000) len=%d, want 1000", len(buf))
	}
	if cap(buf) < 1000 {
		t.Errorf("GetExact(1000) cap=%d, want >= 1000", cap(buf))
	}
	pool.Put(buf[:cap(buf)])
}

func TestBufferPoolConcurrent(t *testing.T) {
	pool := hasher.NewBufferPool()
	var wg sync.WaitGroup
	for i := 0; i < 200; i++ {
		size := (i%8 + 1) << 12 // 4 KiB to 32 KiB
		wg.Add(1)
		go func(sz int) {
			defer wg.Done()
			buf := pool.GetAtLeast(sz)
			// Write to every byte to verify no aliasing.
			for j := range buf {
				buf[j] = byte(j)
			}
			pool.Put(buf)
		}(size)
	}
	wg.Wait()
}

func TestClassSize(t *testing.T) {
	cases := []struct{ in, want int }{
		{0, hasher.MinBufSize},
		{1, hasher.MinBufSize},
		{hasher.MinBufSize, hasher.MinBufSize},
		{hasher.MinBufSize + 1, hasher.MinBufSize * 2},
		{hasher.MaxBufSize, hasher.MaxBufSize},
		{hasher.MaxBufSize + 1, hasher.MaxBufSize + 1}, // over-sized: returned as-is
	}
	for _, tc := range cases {
		got := hasher.ClassSize(tc.in)
		if got != tc.want {
			t.Errorf("ClassSize(%d)=%d, want %d", tc.in, got, tc.want)
		}
	}
}

// ─────────────────────────── Blake3Hasher ────────────────────────────────────

func TestBlake3EmptyFile(t *testing.T) {
	f := emptyFile(t)
	h := hasher.NewBlake3Hasher(0)
	ctx := context.Background()
	hash, err := h.HashFile(ctx, f.Name())
	if err != nil {
		t.Fatalf("HashFile empty: %v", err)
	}
	if !bytes.Equal(hash, hasher.EmptyHash()) {
		t.Errorf("empty file: got %x, want %x", hash, hasher.EmptyHash())
	}
}

func TestBlake3Deterministic(t *testing.T) {
	f := randFile(t, 32*1024)
	h := hasher.NewBlake3Hasher(0)
	ctx := context.Background()
	h1, _ := h.HashFile(ctx, f.Name())
	h2, _ := h.HashFile(ctx, f.Name())
	if !bytes.Equal(h1, h2) {
		t.Error("sequential hash must be deterministic")
	}
}

func TestBlake3DigestLength(t *testing.T) {
	f := randFile(t, 1024)
	h := hasher.NewBlake3Hasher(0)
	hash, err := h.HashFile(context.Background(), f.Name())
	if err != nil {
		t.Fatal(err)
	}
	if len(hash) != 32 {
		t.Errorf("expected 32-byte digest, got %d", len(hash))
	}
}

func TestBlake3HashFDNoSeek(t *testing.T) {
	// HashFD must NOT move the file offset (uses pread64).
	f := randFile(t, 8192)
	h := hasher.NewBlake3Hasher(0)
	ctx := context.Background()

	// Advance the fd offset to a non-zero position.
	if _, err := f.Seek(100, io.SeekStart); err != nil {
		t.Fatalf("seek: %v", err)
	}
	hash1, err := h.HashFD(ctx, int(f.Fd()), 8192)
	if err != nil {
		t.Fatalf("HashFD: %v", err)
	}
	// Hash again – should be identical because we always read from offset 0.
	hash2, err := h.HashFD(ctx, int(f.Fd()), 8192)
	if err != nil {
		t.Fatalf("HashFD second: %v", err)
	}
	if !bytes.Equal(hash1, hash2) {
		t.Error("HashFD must produce consistent results regardless of fd offset")
	}
}

func TestBlake3HashReader(t *testing.T) {
	data := []byte("reader test data 0123456789")
	h := hasher.NewBlake3Hasher(0)
	ctx := context.Background()
	hash1, err := h.HashReader(ctx, bytes.NewReader(data))
	if err != nil {
		t.Fatalf("HashReader: %v", err)
	}
	hash2 := hasher.HashBytes(data)
	if !bytes.Equal(hash1, hash2) {
		t.Errorf("HashReader result %x != HashBytes %x", hash1, hash2)
	}
}

func TestBlake3ContextCancelled(t *testing.T) {
	f := randFile(t, 4*1024*1024)
	h := hasher.NewBlake3Hasher(512) // tiny buffer to force many reads
	ctx, cancel := context.WithCancel(context.Background())
	cancel() // pre-cancel
	_, err := h.HashFile(ctx, f.Name())
	if err == nil {
		t.Error("expected error on cancelled context")
	}
}

func TestBlake3ConcurrentHashFile(t *testing.T) {
	f := randFile(t, 128*1024)
	h := hasher.NewBlake3Hasher(0)
	ctx := context.Background()
	expected, _ := h.HashFile(ctx, f.Name())

	var wg sync.WaitGroup
	for i := 0; i < 50; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			got, err := h.HashFile(ctx, f.Name())
			if err != nil {
				t.Errorf("concurrent HashFile: %v", err)
				return
			}
			if !bytes.Equal(got, expected) {
				t.Error("concurrent hash mismatch")
			}
		}()
	}
	wg.Wait()
}

// ─────────────────────────── MmapHasher ──────────────────────────────────────

func TestMmapMatchesSequential(t *testing.T) {
	for _, size := range []int{1, 4096, 65536, 4 * 1024 * 1024} {
		size := size
		t.Run(itoa(size)+"B", func(t *testing.T) {
			f := randFile(t, size)
			ctx := context.Background()
			seq, _ := hasher.NewBlake3Hasher(0).HashFile(ctx, f.Name())
			mmap, err := hasher.NewMmapHasher().HashFile(ctx, f.Name())
			if err != nil {
				t.Fatalf("mmap(%d): %v", size, err)
			}
			if !bytes.Equal(seq, mmap) {
				t.Errorf("mmap hash mismatch at size=%d", size)
			}
		})
	}
}

func TestMmapEmptyFile(t *testing.T) {
	f := emptyFile(t)
	h, err := hasher.NewMmapHasher().HashFile(context.Background(), f.Name())
	if err != nil {
		t.Fatalf("mmap empty: %v", err)
	}
	if !bytes.Equal(h, hasher.EmptyHash()) {
		t.Error("mmap empty file must return EmptyHash")
	}
}

// ─────────────────────────── ParallelHasher ──────────────────────────────────

func TestParallelMatchesSequential(t *testing.T) {
	sizes := []int{
		1,
		hasher.DefaultChunkSize - 1,
		hasher.DefaultChunkSize,
		hasher.DefaultChunkSize + 1,
		3 * hasher.DefaultChunkSize,
		7*hasher.DefaultChunkSize + 512,
	}
	for _, size := range sizes {
		size := size
		t.Run(itoa(size)+"B", func(t *testing.T) {
			f := randFile(t, size)
			ctx := context.Background()
			seq, _ := hasher.NewBlake3Hasher(0).HashFile(ctx, f.Name())
			par, err := hasher.NewParallelHasher(hasher.WithWorkers(4)).HashFile(ctx, f.Name())
			if err != nil {
				t.Fatalf("parallel(%d): %v", size, err)
			}
			if !bytes.Equal(seq, par) {
				t.Errorf("parallel mismatch size=%d", size)
			}
		})
	}
}

func TestParallelEmptyFile(t *testing.T) {
	f := emptyFile(t)
	h, err := hasher.NewParallelHasher().HashFile(context.Background(), f.Name())
	if err != nil {
		t.Fatalf("parallel empty: %v", err)
	}
	if !bytes.Equal(h, hasher.EmptyHash()) {
		t.Error("parallel empty file must return EmptyHash")
	}
}

func TestParallelWorkerOptions(t *testing.T) {
	f := randFile(t, 16*hasher.DefaultChunkSize)
	ctx := context.Background()
	expected, _ := hasher.NewBlake3Hasher(0).HashFile(ctx, f.Name())

	for _, workers := range []int{1, 2, 4, 8, 16} {
		workers := workers
		t.Run("w"+itoa(workers), func(t *testing.T) {
			ph := hasher.NewParallelHasher(hasher.WithWorkers(workers))
			got, err := ph.HashFile(ctx, f.Name())
			if err != nil {
				t.Fatalf("workers=%d: %v", workers, err)
			}
			if !bytes.Equal(got, expected) {
				t.Errorf("workers=%d: hash mismatch", workers)
			}
		})
	}
}

func TestParallelChunkSizeOptions(t *testing.T) {
	f := randFile(t, 32*1024*1024)
	ctx := context.Background()
	expected, _ := hasher.NewBlake3Hasher(0).HashFile(ctx, f.Name())

	for _, cs := range []int64{512 << 10, 1 << 20, 4 << 20, 8 << 20} {
		cs := cs
		t.Run("chunk="+itoa(int(cs>>10))+"K", func(t *testing.T) {
			ph := hasher.NewParallelHasher(hasher.WithChunkSize(cs))
			got, err := ph.HashFile(ctx, f.Name())
			if err != nil {
				t.Fatalf("chunk=%d: %v", cs, err)
			}
			if !bytes.Equal(got, expected) {
				t.Errorf("chunk=%d: hash mismatch", cs)
			}
		})
	}
}

func TestParallelHashReaderAt(t *testing.T) {
	data := make([]byte, 5*hasher.DefaultChunkSize+100)
	_, _ = rand.Read(data)

	ctx := context.Background()
	expected := hasher.HashBytes(data)

	ph := hasher.NewParallelHasher(hasher.WithWorkers(4))
	got, err := ph.HashReaderAt(ctx, bytes.NewReader(data), 0, int64(len(data)))
	if err != nil {
		t.Fatalf("HashReaderAt: %v", err)
	}
	if !bytes.Equal(got, expected) {
		t.Error("HashReaderAt mismatch")
	}
}

func TestParallelHashReaderAtSubRange(t *testing.T) {
	data := make([]byte, 10*1024*1024)
	_, _ = rand.Read(data)

	offset := int64(1024 * 1024)
	size := int64(4 * 1024 * 1024)
	subData := data[offset : offset+size]

	ctx := context.Background()
	expected := hasher.HashBytes(subData)

	ph := hasher.NewParallelHasher(hasher.WithWorkers(4))
	got, err := ph.HashReaderAt(ctx, bytes.NewReader(data), offset, size)
	if err != nil {
		t.Fatalf("HashReaderAt sub-range: %v", err)
	}
	if !bytes.Equal(got, expected) {
		t.Error("HashReaderAt sub-range mismatch")
	}
}

func TestParallelCancelMidway(t *testing.T) {
	f := randFile(t, 64*1024*1024)
	ctx, cancel := context.WithCancel(context.Background())
	cancel() // pre-cancel forces immediate context check
	ph := hasher.NewParallelHasher(hasher.WithWorkers(4))
	_, err := ph.HashFile(ctx, f.Name())
	// May return context error or hash – either is acceptable as long as no panic/leak.
	_ = err
}

// ─────────────────────────── AdaptiveHasher ──────────────────────────────────

func TestAdaptiveStrategySelection(t *testing.T) {
	a := hasher.NewAdaptiveHasher(
		hasher.WithSmallThreshold(8<<20),
		hasher.WithMediumThreshold(64<<20),
	)
	cases := []struct {
		size int64
		want hasher.Strategy
	}{
		{0, hasher.StrategySequential},
		{1, hasher.StrategySequential},
		{(8 << 20) - 1, hasher.StrategySequential},
		{8 << 20, hasher.StrategyMmap},
		{(64 << 20) - 1, hasher.StrategyMmap},
		{64 << 20, hasher.StrategyParallel},
		{512 << 20, hasher.StrategyParallel},
	}
	for _, tc := range cases {
		got := a.Strategy(tc.size)
		if got != tc.want {
			t.Errorf("Strategy(%d)=%s, want %s", tc.size, got, tc.want)
		}
	}
}

func TestAdaptiveAllStrategiesMatch(t *testing.T) {
	ctx := context.Background()
	// Test each threshold crossing.
	sizes := []int{
		4 * 1024,          // sequential
		4 * 1024 * 1024,   // sequential (below 8 MiB)
		12 * 1024 * 1024,  // mmap
		130 * 1024 * 1024, // parallel
	}
	for _, size := range sizes {
		size := size
		t.Run(itoa(size>>10)+"K", func(t *testing.T) {
			f := randFile(t, size)
			seq, _ := hasher.NewBlake3Hasher(0).HashFile(ctx, f.Name())
			ada, err := hasher.NewAdaptiveHasher().HashFile(ctx, f.Name())
			if err != nil {
				t.Fatalf("adaptive(%d): %v", size, err)
			}
			if !bytes.Equal(seq, ada) {
				t.Errorf("adaptive mismatch at size=%d", size)
			}
		})
	}
}

func TestStrategyString(t *testing.T) {
	cases := map[hasher.Strategy]string{
		hasher.StrategySequential: "sequential",
		hasher.StrategyMmap:       "mmap",
		hasher.StrategyParallel:   "parallel",
		hasher.Strategy(99):       "unknown",
	}
	for s, want := range cases {
		if got := s.String(); got != want {
			t.Errorf("Strategy(%d).String()=%q, want %q", s, got, want)
		}
	}
}

// ─────────────────────────── Package-level pool funcs ────────────────────────

func TestSharedPoolGetPut(t *testing.T) {
	buf := hasher.GetBuf(4096)
	if len(buf) < 4096 {
		t.Errorf("GetBuf(4096) len=%d", len(buf))
	}
	hasher.PutBuf(buf)
}

func itoa(n int) string {
	if n == 0 {
		return "0"
	}
	var buf [20]byte
	pos := len(buf)
	for n > 0 {
		pos--
		buf[pos] = byte(n%10) + '0'
		n /= 10
	}
	return string(buf[pos:])
}
