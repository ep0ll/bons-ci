// Package testutil provides shared test helpers for the dagstore module.
package testutil

import (
	"crypto/sha256"
	"encoding/hex"
	"fmt"

	dagstore "github.com/bons/bons-ci/plugins/rbe/dag"
)

// SHA256Hasher is a deterministic test Hasher backed by SHA-256.
// It concatenates all data slices before hashing.
type SHA256Hasher struct{}

func (SHA256Hasher) Algorithm() dagstore.HashAlgorithm { return dagstore.HashSHA256 }

func (SHA256Hasher) Hash(data ...[]byte) (string, error) {
	h := sha256.New()
	for _, d := range data {
		if _, err := h.Write(d); err != nil {
			return "", fmt.Errorf("sha256 write: %w", err)
		}
	}
	return hex.EncodeToString(h.Sum(nil)), nil
}

// MustHash computes the hash of s; panics on error.
func MustHash(h dagstore.Hasher, data ...[]byte) string {
	v, err := h.Hash(data...)
	if err != nil {
		panic(err)
	}
	return v
}

// FixedHasher always returns a pre-set string — useful for deterministic IDs.
type FixedHasher struct {
	Value string
}

func (f FixedHasher) Algorithm() dagstore.HashAlgorithm { return dagstore.HashCustom }
func (f FixedHasher) Hash(...[]byte) (string, error)    { return f.Value, nil }
