package reactdag

import (
	"crypto/sha256"
	"encoding/binary"
	"fmt"
	"sort"
)

// ---------------------------------------------------------------------------
// DefaultKeyComputer
// ---------------------------------------------------------------------------

// DefaultKeyComputer derives a CacheKey by hashing:
//  1. The vertex's operation ID (stable content-addressable op identity).
//  2. For each input file, in sorted-path order: the path + blake3 hash.
//
// NOTE: This implementation uses SHA-256 as the outer hash. Replace with
// blake3 (github.com/zeebo/blake3) in production for maximum throughput.
// The FileRef.Hash field is already a blake3 digest provided by the FileTracker.
type DefaultKeyComputer struct{}

// Compile-time assertion that DefaultKeyComputer satisfies the interface.
var _ CacheKeyComputer = DefaultKeyComputer{}

// Compute derives a CacheKey from the vertex's operation identity and the
// hashes of its input files. The result is deterministic given the same inputs.
func (DefaultKeyComputer) Compute(v *Vertex, inputFiles []FileRef) (CacheKey, error) {
	h := sha256.New()

	if err := writeString(h, v.OpID()); err != nil {
		return CacheKey{}, fmt.Errorf("key compute: write op id: %w", err)
	}

	sorted := sortedByPath(inputFiles)
	for _, f := range sorted {
		if err := writeFileRef(h, f); err != nil {
			return CacheKey{}, fmt.Errorf("key compute: write file ref %q: %w", f.Path, err)
		}
	}

	var key CacheKey
	copy(key[:], h.Sum(nil))
	return key, nil
}

// ---------------------------------------------------------------------------
// Internal serialisation helpers
// ---------------------------------------------------------------------------

type hashWriter interface {
	Write(p []byte) (n int, err error)
}

func writeString(h hashWriter, s string) error {
	// Prefix with length to avoid path/opID collisions.
	var lenBuf [4]byte
	binary.BigEndian.PutUint32(lenBuf[:], uint32(len(s)))
	if _, err := h.Write(lenBuf[:]); err != nil {
		return err
	}
	_, err := h.Write([]byte(s))
	return err
}

func writeFileRef(h hashWriter, f FileRef) error {
	if err := writeString(h, f.Path); err != nil {
		return err
	}
	// Write the blake3 hash of the file's content directly.
	_, err := h.Write(f.Hash[:])
	return err
}

func sortedByPath(refs []FileRef) []FileRef {
	cp := make([]FileRef, len(refs))
	copy(cp, refs)
	sort.Slice(cp, func(i, j int) bool { return cp[i].Path < cp[j].Path })
	return cp
}
