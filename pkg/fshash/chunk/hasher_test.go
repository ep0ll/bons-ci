package chunk_test

import (
	"bytes"
	"crypto/sha256"
	"testing"

	"github.com/bons/bons-ci/pkg/fshash/chunk"
)

func TestSHA256Hasher(t *testing.T) {
	h := chunk.NewHasher(chunk.SHA256)

	data := []byte("hello world")
	got := h.Hash(data)

	expected := sha256.Sum256(data)
	if !bytes.Equal(got, expected[:]) {
		t.Errorf("SHA256 mismatch")
	}

	if h.Algorithm() != chunk.SHA256 {
		t.Errorf("Algorithm = %s, want sha256", h.Algorithm())
	}
	if h.Size() != 32 {
		t.Errorf("Size = %d, want 32", h.Size())
	}
}

func TestBLAKE3Hasher(t *testing.T) {
	h := chunk.NewHasher(chunk.BLAKE3)

	data := []byte("hello world")
	got := h.Hash(data)

	if len(got) != 32 {
		t.Errorf("BLAKE3 output size = %d, want 32", len(got))
	}
	if h.Algorithm() != chunk.BLAKE3 {
		t.Errorf("Algorithm = %s, want blake3", h.Algorithm())
	}
}

func TestXXH3Hasher(t *testing.T) {
	h := chunk.NewHasher(chunk.XXH3)

	data := []byte("hello world")
	got := h.Hash(data)

	if len(got) != 8 {
		t.Errorf("XXH3 output size = %d, want 8", len(got))
	}
	if h.Algorithm() != chunk.XXH3 {
		t.Errorf("Algorithm = %s, want xxh3", h.Algorithm())
	}
}

func TestHasherDeterministic(t *testing.T) {
	for _, algo := range []chunk.Algorithm{chunk.SHA256, chunk.BLAKE3, chunk.XXH3} {
		t.Run(string(algo), func(t *testing.T) {
			h := chunk.NewHasher(algo)
			data := []byte("deterministic test data")
			h1 := h.Hash(data)
			h2 := h.Hash(data)
			if !bytes.Equal(h1, h2) {
				t.Errorf("%s is not deterministic", algo)
			}
		})
	}
}

func TestHasherReader(t *testing.T) {
	for _, algo := range []chunk.Algorithm{chunk.SHA256, chunk.BLAKE3, chunk.XXH3} {
		t.Run(string(algo), func(t *testing.T) {
			h := chunk.NewHasher(algo)
			data := []byte("streaming hash test")

			fromDirect := h.Hash(data)
			fromReader, err := h.HashReader(bytes.NewReader(data))
			if err != nil {
				t.Fatalf("HashReader: %v", err)
			}

			if !bytes.Equal(fromDirect, fromReader) {
				t.Errorf("Hash vs HashReader mismatch for %s", algo)
			}
		})
	}
}

func TestPoolGetPut(t *testing.T) {
	pool := chunk.NewPool()

	// Small buffer.
	buf := pool.Get(1024)
	if len(buf) < 1024 {
		t.Errorf("small buffer too small: %d", len(buf))
	}
	pool.Put(buf)

	// Medium buffer.
	buf = pool.Get(16 * 1024)
	if len(buf) < 16*1024 {
		t.Errorf("medium buffer too small: %d", len(buf))
	}
	pool.Put(buf)

	// Large buffer.
	buf = pool.Get(64 * 1024)
	if len(buf) >= 64*1024 {
		pool.Put(buf)
	}

	// Oversized buffer (not pooled).
	buf = pool.Get(128 * 1024)
	if len(buf) < 128*1024 {
		t.Errorf("oversized buffer too small: %d", len(buf))
	}
	pool.Put(buf) // Should not panic.
}

func BenchmarkHashers(b *testing.B) {
	data := make([]byte, 32*1024) // 32KB
	for i := range data {
		data[i] = byte(i)
	}

	for _, algo := range []chunk.Algorithm{chunk.SHA256, chunk.BLAKE3, chunk.XXH3} {
		b.Run(string(algo), func(b *testing.B) {
			h := chunk.NewHasher(algo)
			b.SetBytes(int64(len(data)))
			b.ReportAllocs()
			for i := 0; i < b.N; i++ {
				h.Hash(data)
			}
		})
	}
}
