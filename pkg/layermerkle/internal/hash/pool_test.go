package hash_test

import (
	"bytes"
	"crypto/sha256"
	"testing"

	"github.com/bons/bons-ci/pkg/layermerkle/internal/hash"
)

func TestSHA256Pool_AcquireRelease_Idempotent(t *testing.T) {
	h1 := hash.SHA256Pool.Acquire()
	h1.Write([]byte("hello"))
	sum1 := h1.Sum(nil)
	hash.SHA256Pool.Release(h1)

	h2 := hash.SHA256Pool.Acquire()
	h2.Write([]byte("hello"))
	sum2 := h2.Sum(nil)
	hash.SHA256Pool.Release(h2)

	if !bytes.Equal(sum1, sum2) {
		t.Error("pool returned inconsistent hash for same input")
	}

	// Verify correct SHA-256.
	want := sha256.Sum256([]byte("hello"))
	if !bytes.Equal(sum1, want[:]) {
		t.Errorf("SHA-256 mismatch: got %x, want %x", sum1, want)
	}
}

func TestSHA256Pool_Reset_OnRelease(t *testing.T) {
	h := hash.SHA256Pool.Acquire()
	h.Write([]byte("dirty"))
	hash.SHA256Pool.Release(h)

	// After release and re-acquire, hash must be reset.
	h = hash.SHA256Pool.Acquire()
	h.Write([]byte("clean"))
	sum := h.Sum(nil)
	hash.SHA256Pool.Release(h)

	want := sha256.Sum256([]byte("clean"))
	if !bytes.Equal(sum, want[:]) {
		t.Error("pool hash was not reset on Release")
	}
}

func TestSumBytes_MatchesDirectHash(t *testing.T) {
	input := []byte("test content for hashing")
	got := hash.SumBytes(input, hash.SHA256Pool)
	want := sha256.Sum256(input)
	if !bytes.Equal(got, want[:]) {
		t.Errorf("SumBytes mismatch: got %x, want %x", got, want)
	}
}

func TestSumFile_ReadsReader(t *testing.T) {
	content := []byte("file content for sum")
	r := bytes.NewReader(content)
	got, n, err := hash.SumFile(r, hash.SHA256Pool)
	if err != nil {
		t.Fatalf("SumFile error: %v", err)
	}
	if n != int64(len(content)) {
		t.Errorf("n = %d, want %d", n, len(content))
	}
	want := sha256.Sum256(content)
	if !bytes.Equal(got, want[:]) {
		t.Errorf("SumFile hash mismatch: got %x, want %x", got, want)
	}
}

func BenchmarkSumBytes_4KB(b *testing.B) {
	input := bytes.Repeat([]byte("x"), 4096)
	b.ResetTimer()
	b.ReportAllocs()
	for i := 0; i < b.N; i++ {
		hash.SumBytes(input, hash.SHA256Pool)
	}
}

func BenchmarkSumBytes_64KB(b *testing.B) {
	input := bytes.Repeat([]byte("x"), 65536)
	b.ResetTimer()
	b.ReportAllocs()
	for i := 0; i < b.N; i++ {
		hash.SumBytes(input, hash.SHA256Pool)
	}
}
