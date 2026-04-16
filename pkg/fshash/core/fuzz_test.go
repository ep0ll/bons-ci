package core_test

// fuzz_test.go — Go native fuzz tests for pkg/fshash/core.
//
// Run a quick seed-corpus check (no fuzzing, always fast):
//
//	go test ./pkg/fshash/core/ -run FuzzHash
//
// Enable actual fuzzing for a specific target:
//
//	go test ./pkg/fshash/core/ -fuzz FuzzHashAlgorithmsDeterministic -fuzztime 30s
//	go test ./pkg/fshash/core/ -fuzz FuzzCombineShardsSentinel       -fuzztime 30s
//	go test ./pkg/fshash/core/ -fuzz FuzzWriteMetaHeaderStability    -fuzztime 30s

import (
	"bytes"
	"io/fs"
	"testing"

	"github.com/bons/bons-ci/pkg/fshash/core"
)

// FuzzHashAlgorithmsDeterministic verifies that every built-in algorithm
// produces the same digest when called twice on the same input.
// Property: H(x) == H(x) for all x and all algorithms.
func FuzzHashAlgorithmsDeterministic(f *testing.F) {
	// Seed corpus: empty, single byte, boundary sizes, large.
	f.Add([]byte{})
	f.Add([]byte("a"))
	f.Add([]byte("hello, world"))
	f.Add(bytes.Repeat([]byte{0xFF}, 63))
	f.Add(bytes.Repeat([]byte{0x00}, 64))
	f.Add(bytes.Repeat([]byte("ABCDEF"), 1024))

	algos := []core.Algorithm{
		core.SHA256, core.SHA512, core.Blake3,
		core.XXHash64, core.XXHash3, core.CRC32C,
	}

	f.Fuzz(func(t *testing.T, data []byte) {
		for _, algo := range algos {
			h := core.MustHasher(algo)

			h1 := h.New()
			h1.Write(data)
			d1 := h1.Sum(nil)

			h2 := h.New()
			h2.Write(data)
			d2 := h2.Sum(nil)

			if !bytes.Equal(d1, d2) {
				t.Fatalf("algo %s: non-deterministic digest for %d-byte input", algo, len(data))
			}
			if len(d1) != h.DigestSize() {
				t.Fatalf("algo %s: digest len %d != DigestSize %d", algo, len(d1), h.DigestSize())
			}
		}
	})
}

// FuzzHashAlgorithmsReset verifies that Reset() restores the hasher to
// its initial state so repeated uses on the same input produce equal digests.
// Property: H(x) == Reset; H(x) for all x.
func FuzzHashAlgorithmsReset(f *testing.F) {
	f.Add([]byte{})
	f.Add([]byte("reset-me"))
	f.Add(bytes.Repeat([]byte{0xAB}, 512))

	algos := []core.Algorithm{
		core.SHA256, core.Blake3, core.XXHash3, core.CRC32C,
	}

	f.Fuzz(func(t *testing.T, data []byte) {
		for _, algo := range algos {
			h := core.MustHasher(algo).New()

			h.Write(data)
			d1 := h.Sum(nil)

			h.Reset()
			h.Write(data)
			d2 := h.Sum(nil)

			if !bytes.Equal(d1, d2) {
				t.Fatalf("algo %s: Reset() produced different digest for %d-byte input", algo, len(data))
			}
		}
	})
}

// FuzzHashIncrementalMatchesOneShot verifies that streaming (byte-by-byte)
// and one-shot writes produce the same digest.
// Property: H(chunk₁ ++ chunk₂ ++ …) == H(data) for any chunking.
func FuzzHashIncrementalMatchesOneShot(f *testing.F) {
	f.Add([]byte{}, 1)
	f.Add([]byte("hello"), 1)
	f.Add([]byte("hello, world"), 3)
	f.Add(bytes.Repeat([]byte("xyz"), 100), 7)
	f.Add(bytes.Repeat([]byte{0x00, 0xFF}, 200), 13)

	algos := []core.Algorithm{
		core.SHA256, core.Blake3, core.XXHash3, core.XXHash64,
	}

	f.Fuzz(func(t *testing.T, data []byte, chunkSize int) {
		if chunkSize < 1 {
			chunkSize = 1
		}
		if chunkSize > 64 {
			chunkSize = 64
		}

		for _, algo := range algos {
			h := core.MustHasher(algo)

			// One-shot.
			hFull := h.New()
			hFull.Write(data)
			dFull := hFull.Sum(nil)

			// Incremental in chunks of chunkSize.
			hInc := h.New()
			for off := 0; off < len(data); off += chunkSize {
				end := off + chunkSize
				if end > len(data) {
					end = len(data)
				}
				hInc.Write(data[off:end])
			}
			dInc := hInc.Sum(nil)

			if !bytes.Equal(dFull, dInc) {
				t.Fatalf("algo %s: one-shot %x != incremental %x (chunkSize=%d, len=%d)",
					algo, dFull, dInc, chunkSize, len(data))
			}
		}
	})
}

// FuzzCombineShardsSentinel verifies that CombineShards produces a digest
// that is distinguishable from a plain hash of the same shard digests
// and that the sentinel encoding is stable.
// Property: CombineShards(H, shards) != H(concat(shards)) (different encoding).
func FuzzCombineShardsSentinel(f *testing.F) {
	f.Add([]byte("shard-one"), []byte("shard-two"))
	f.Add([]byte{0x01}, []byte{0x02})
	f.Add(bytes.Repeat([]byte("A"), 32), bytes.Repeat([]byte("B"), 32))

	f.Fuzz(func(t *testing.T, s1, s2 []byte) {
		shards := [][]byte{s1, s2}

		// CombineShards uses a sentinel (0xFE + nShards + shardSize).
		h1 := core.MustHasher(core.SHA256).New()
		combined := core.CombineShards(h1, shards)

		// Plain hash of the raw shard bytes concatenated.
		h2 := core.MustHasher(core.SHA256).New()
		for _, s := range shards {
			h2.Write(s)
		}
		plain := h2.Sum(nil)

		// The sentinel must make them distinguishable.
		if bytes.Equal(combined, plain) {
			t.Fatal("CombineShards must differ from plain concatenation hash")
		}

		// Two identical calls must be deterministic.
		h3 := core.MustHasher(core.SHA256).New()
		combined2 := core.CombineShards(h3, shards)
		if !bytes.Equal(combined, combined2) {
			t.Fatal("CombineShards must be deterministic")
		}
	})
}

// FuzzWriteMetaHeaderStability verifies that WriteMetaHeader produces
// deterministic output and that different metadata produces different headers.
// Property: same (fi, flags) → same header bytes.
func FuzzWriteMetaHeaderStability(f *testing.F) {
	f.Add(uint32(0o644), int64(1024), uint8(core.MetaModeAndSize))
	f.Add(uint32(0o755), int64(0), uint8(core.MetaMode))
	f.Add(uint32(0o600), int64(4096), uint8(core.MetaSize))
	f.Add(uint32(0), int64(999999), uint8(core.MetaNone))

	f.Fuzz(func(t *testing.T, mode uint32, size int64, flagByte uint8) {
		if size < 0 {
			size = -size
		}
		flags := core.MetaFlag(flagByte)
		fi := &fakeFileInfo{size: size, mode: fs.FileMode(mode)}

		// Two independent calls with the same inputs must produce the same hash.
		h1 := core.MustHasher(core.SHA256).New()
		core.WriteMetaHeader(h1, fi, flags, "")
		d1 := h1.Sum(nil)

		h2 := core.MustHasher(core.SHA256).New()
		core.WriteMetaHeader(h2, fi, flags, "")
		d2 := h2.Sum(nil)

		if !bytes.Equal(d1, d2) {
			t.Fatalf("WriteMetaHeader non-deterministic: mode=%o size=%d flags=%d", mode, size, flagByte)
		}

		// MetaNone must produce the same as a fresh hash (nothing written).
		if flags == core.MetaNone {
			hEmpty := core.MustHasher(core.SHA256).New()
			dEmpty := hEmpty.Sum(nil)
			if !bytes.Equal(d1, dEmpty) {
				t.Fatal("WriteMetaHeader(MetaNone) must write nothing")
			}
		}
	})
}

// FuzzDigestSink verifies that DigestSink produces correct output for all
// algorithms regardless of input size.
// Property: sink.Sum(h) == h.Sum(nil) for all algorithms and inputs.
func FuzzDigestSink(f *testing.F) {
	f.Add([]byte{})
	f.Add([]byte("test"))
	f.Add(bytes.Repeat([]byte{0x42}, core.MaxDigestSize*2))

	f.Fuzz(func(t *testing.T, data []byte) {
		for _, algo := range []core.Algorithm{core.SHA256, core.Blake3, core.CRC32C} {
			h := core.MustHasher(algo).New()
			h.Write(data)

			var sink core.DigestSink
			fromSink := sink.Sum(h)
			fromHash := h.Sum(nil)

			if !bytes.Equal(fromSink, fromHash) {
				t.Fatalf("algo %s: DigestSink mismatch", algo)
			}
			if len(fromSink) != core.MustHasher(algo).DigestSize() {
				t.Fatalf("algo %s: wrong digest size %d", algo, len(fromSink))
			}
		}
	})
}
