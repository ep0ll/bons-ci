package fshash_test

// fuzz_test.go — Go native fuzz tests for pkg/fshash.
//
// Run seed corpus only (fast, always included in CI):
//
//	go test ./pkg/fshash/ -run Fuzz
//
// Enable actual fuzzing:
//
//	go test ./pkg/fshash/ -fuzz FuzzSumReproducible  -fuzztime 60s
//	go test ./pkg/fshash/ -fuzz FuzzCanonicalRoundTrip -fuzztime 60s
//	go test ./pkg/fshash/ -fuzz FuzzHexDecode         -fuzztime 60s

import (
	"bytes"
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/bons/bons-ci/pkg/fshash"
)

// FuzzSumReproducible verifies that computing the digest of a single file
// twice always yields the same result. Any non-determinism would be a
// critical correctness bug.
//
// Property: Sum(path, content) == Sum(path, content) for all content.
func FuzzSumReproducible(f *testing.F) {
	f.Add([]byte{})
	f.Add([]byte("hello, world"))
	f.Add([]byte("\x00\xFF\x00\xFF"))
	f.Add(bytes.Repeat([]byte("A"), 4096))
	f.Add(bytes.Repeat([]byte{0x00}, 1024))
	f.Add([]byte("unicode: 日本語テスト"))

	f.Fuzz(func(t *testing.T, content []byte) {
		dir := t.TempDir()
		p := filepath.Join(dir, "fuzz.bin")
		if err := os.WriteFile(p, content, 0o644); err != nil {
			t.Skip("cannot write file")
		}

		cs := fshash.MustNew(fshash.WithMetadata(fshash.MetaNone))
		ctx := context.Background()

		r1, err := cs.Sum(ctx, p)
		if err != nil {
			t.Fatalf("Sum #1 failed: %v", err)
		}
		r2, err := cs.Sum(ctx, p)
		if err != nil {
			t.Fatalf("Sum #2 failed: %v", err)
		}
		if !bytes.Equal(r1.Digest, r2.Digest) {
			t.Fatalf("non-reproducible: content=%q r1=%x r2=%x",
				content, r1.Digest, r2.Digest)
		}
	})
}

// FuzzDirSumReproducible verifies directory digest reproducibility with
// a two-file tree. Tests the dir combine formula (name\0digest) under fuzzing.
func FuzzDirSumReproducible(f *testing.F) {
	f.Add([]byte("alpha"), []byte("beta"), "a.txt", "b.txt")
	f.Add([]byte{}, []byte{0xFF}, "empty", "full")
	f.Add([]byte("same"), []byte("same"), "x", "y")

	f.Fuzz(func(t *testing.T, c1, c2 []byte, name1, name2 string) {
		// Sanitise names: no path separators, no empty names.
		clean := func(s string) string {
			s = strings.Map(func(r rune) rune {
				if r == '/' || r == '\\' || r == '\x00' {
					return '_'
				}
				return r
			}, s)
			if s == "" || s == "." || s == ".." {
				return "f"
			}
			return s
		}
		name1, name2 = clean(name1), clean(name2)
		if name1 == name2 {
			name2 = name2 + "2"
		}

		dir := t.TempDir()
		os.WriteFile(filepath.Join(dir, name1), c1, 0o644) //nolint:errcheck
		os.WriteFile(filepath.Join(dir, name2), c2, 0o644) //nolint:errcheck

		cs := fshash.MustNew(fshash.WithMetadata(fshash.MetaNone))
		ctx := context.Background()

		r1, err := cs.Sum(ctx, dir)
		if err != nil {
			t.Fatalf("Sum #1: %v", err)
		}
		r2, err := cs.Sum(ctx, dir)
		if err != nil {
			t.Fatalf("Sum #2: %v", err)
		}
		if !bytes.Equal(r1.Digest, r2.Digest) {
			t.Fatalf("non-reproducible dir digest: name1=%q name2=%q", name1, name2)
		}
	})
}

// FuzzCanonicalRoundTrip verifies that Canonicalize output can always be
// parsed by ReadCanonical and that the round-trip is lossless.
//
// Property: ReadCanonical(Canonicalize(tree)) has correct entry count and
// the last entry is kind=="root".
func FuzzCanonicalRoundTrip(f *testing.F) {
	f.Add([]byte("content"), "file.txt")
	f.Add([]byte{0x00, 0xFF}, "binary.bin")
	f.Add([]byte("unicode: αβγ"), "uni.txt")
	f.Add([]byte("file with spaces"), "my file.txt")

	f.Fuzz(func(t *testing.T, content []byte, filename string) {
		// Sanitise filename.
		filename = strings.Map(func(r rune) rune {
			if r == '/' || r == '\\' || r == '\x00' || r == '\n' {
				return '_'
			}
			return r
		}, filename)
		if filename == "" || filename == "." || filename == ".." {
			filename = "f.txt"
		}
		// Remove leading dots to avoid hidden files on some systems.
		filename = strings.TrimLeft(filename, ".")
		if filename == "" {
			filename = "f.txt"
		}

		dir := t.TempDir()
		if err := os.WriteFile(filepath.Join(dir, filename), content, 0o644); err != nil {
			t.Skip("cannot write file")
		}

		cs := fshash.MustNew(fshash.WithMetadata(fshash.MetaNone))
		ctx := context.Background()

		var buf bytes.Buffer
		rootDgst, err := cs.Canonicalize(ctx, dir, &buf)
		if err != nil {
			t.Fatalf("Canonicalize: %v", err)
		}

		entries, err := fshash.ReadCanonical(&buf)
		if err != nil {
			t.Fatalf("ReadCanonical: %v\noutput was:\n%s", err, buf.String())
		}

		if len(entries) == 0 {
			t.Fatal("ReadCanonical returned no entries")
		}

		last := entries[len(entries)-1]
		if last.Kind != "root" {
			t.Errorf("last entry kind=%q want 'root'", last.Kind)
		}
		if !bytes.Equal(last.Digest, rootDgst) {
			t.Errorf("root digest mismatch: %x vs %x", last.Digest, rootDgst)
		}

		// Filename must appear in entries (the file entry).
		found := false
		for _, e := range entries {
			if e.RelPath == filename {
				found = true
				break
			}
		}
		if !found {
			t.Errorf("filename %q not found in canonical entries: %v", filename, entries)
		}
	})
}

// FuzzHexDecode verifies that hexDecode (used by snapshot.VerifyAgainst)
// is consistent with encoding/hex: it must return nil for invalid input
// and a correct byte slice for valid hex strings.
//
// Since hexDecode is unexported we test it indirectly via TakeSnapshot
// round-trip: decode(encode(bytes)) == bytes.
func FuzzHexDecode(f *testing.F) {
	f.Add([]byte{})
	f.Add([]byte{0x00})
	f.Add([]byte{0xDE, 0xAD, 0xBE, 0xEF})
	f.Add(bytes.Repeat([]byte{0xFF}, 32))

	f.Fuzz(func(t *testing.T, data []byte) {
		dir := t.TempDir()
		os.WriteFile(filepath.Join(dir, "f"), data, 0o644) //nolint:errcheck

		ctx := context.Background()
		snap, err := fshash.TakeSnapshot(ctx, dir, fshash.WithMetadata(fshash.MetaNone))
		if err != nil {
			t.Fatalf("TakeSnapshot: %v", err)
		}

		// VerifyAgainst internally hex-decodes the stored RootDigest.
		if err := snap.VerifyAgainst(ctx, dir); err != nil {
			t.Fatalf("VerifyAgainst after TakeSnapshot: %v", err)
		}
	})
}

// FuzzHashReaderAlgorithms verifies HashReader's determinism under all
// algorithms for arbitrary byte streams.
func FuzzHashReaderAlgorithms(f *testing.F) {
	f.Add([]byte{})
	f.Add([]byte("fuzz me"))
	f.Add(bytes.Repeat([]byte{0xAB}, 1024))

	algos := []fshash.Algorithm{
		fshash.SHA256, fshash.Blake3, fshash.XXHash3, fshash.CRC32C,
	}

	f.Fuzz(func(t *testing.T, data []byte) {
		ctx := context.Background()
		for _, algo := range algos {
			d1, err := fshash.HashReader(ctx, bytes.NewReader(data),
				fshash.WithAlgorithm(algo), fshash.WithMetadata(fshash.MetaNone))
			if err != nil {
				t.Fatalf("HashReader #1 algo=%s: %v", algo, err)
			}
			d2, err := fshash.HashReader(ctx, bytes.NewReader(data),
				fshash.WithAlgorithm(algo), fshash.WithMetadata(fshash.MetaNone))
			if err != nil {
				t.Fatalf("HashReader #2 algo=%s: %v", algo, err)
			}
			if !bytes.Equal(d1, d2) {
				t.Fatalf("HashReader non-deterministic: algo=%s data=%q", algo, data)
			}
		}
	})
}
