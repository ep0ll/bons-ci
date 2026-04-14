package dagstore

import (
	"encoding/binary"
	"fmt"
	"sort"
)

// ——— Hasher interface ————————————————————————————————————————————————————————

// Hasher abstracts the hash algorithm.  Callers supply their own implementation
// (blake3, sha256, etc.) via this interface — this package never imports a
// concrete hashing library.
//
// Hash MUST be:
//   - Deterministic: same inputs always produce the same output.
//   - Collision-resistant to a level appropriate for content addressing.
//   - Returning a lowercase hex-encoded string.
//
// data slices are concatenated logically; the implementation must process them
// as a single byte stream (e.g. hash.Write each slice in order).
type Hasher interface {
	Hash(data ...[]byte) (string, error)
	// Algorithm returns the name of the algorithm, e.g. "blake3".
	Algorithm() HashAlgorithm
}

// ——— tree-hash computation ————————————————————————————————————————————————————

// ComputeVertexHash derives the content-addressed hash for a vertex.
//
// The hash encodes the vertex's complete DAG ancestry:
//
//	root vertex (no inputs):
//	    hash(len(operationHash) ‖ operationHash)
//
//	non-root vertex:
//	    hash(len(operationHash) ‖ operationHash
//	         ‖ uint32(n) ‖ sorted(len(inputHash_i) ‖ inputHash_i)...)
//
// Input hashes are sorted before hashing so that the result is independent of
// declaration order (DAG edges are a set, not a sequence).
func ComputeVertexHash(h Hasher, operationHash string, inputHashes []string) (string, error) {
	if operationHash == "" {
		return "", &InvalidArgumentError{Field: "operationHash", Reason: "must not be empty"}
	}

	// Sort a copy so the caller's slice is not mutated.
	sorted := make([]string, len(inputHashes))
	copy(sorted, inputHashes)
	sort.Strings(sorted)

	parts := make([][]byte, 0, 2+len(sorted)*2)

	// length-prefixed operation hash
	parts = append(parts, uint32Bytes(uint32(len(operationHash))), []byte(operationHash))

	// number of inputs + each length-prefixed input hash
	if len(sorted) > 0 {
		parts = append(parts, uint32Bytes(uint32(len(sorted))))
		for _, ih := range sorted {
			parts = append(parts, uint32Bytes(uint32(len(ih))), []byte(ih))
		}
	}

	digest, err := h.Hash(parts...)
	if err != nil {
		return "", fmt.Errorf("compute vertex hash: %w", err)
	}
	return digest, nil
}

// ComputeDAGHash derives the content-addressed hash for a whole DAG from
// its leaf-vertex hashes (the final output vertices — those with no children).
//
//	dag_hash = hash(uint32(n) ‖ sorted(len(leafHash_i) ‖ leafHash_i)...)
func ComputeDAGHash(h Hasher, leafHashes []string) (string, error) {
	if len(leafHashes) == 0 {
		return "", &InvalidArgumentError{Field: "leafHashes", Reason: "at least one leaf hash is required"}
	}

	sorted := make([]string, len(leafHashes))
	copy(sorted, leafHashes)
	sort.Strings(sorted)

	parts := make([][]byte, 0, 1+len(sorted)*2)
	parts = append(parts, uint32Bytes(uint32(len(sorted))))
	for _, lh := range sorted {
		parts = append(parts, uint32Bytes(uint32(len(lh))), []byte(lh))
	}

	digest, err := h.Hash(parts...)
	if err != nil {
		return "", fmt.Errorf("compute dag hash: %w", err)
	}
	return digest, nil
}

// ComputeInputsHash derives a single hash summarising a set of parent-vertex
// hashes.  This is the value the optional vertex ID "replaces" — when a vertex
// has a stable ID, callers can look it up without recomputing the full inputs
// hash.
func ComputeInputsHash(h Hasher, inputHashes []string) (string, error) {
	return ComputeDAGHash(h, inputHashes) // same structure
}

// ——— helpers ————————————————————————————————————————————————————————————————

// uint32Bytes returns b as a 4-byte big-endian slice.
func uint32Bytes(v uint32) []byte {
	b := make([]byte, 4)
	binary.BigEndian.PutUint32(b, v)
	return b
}
