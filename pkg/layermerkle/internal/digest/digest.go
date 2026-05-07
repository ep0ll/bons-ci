// Package digest provides a content-addressable digest type backed entirely
// by the Go standard library. It intentionally mirrors the API surface of
// github.com/opencontainers/go-digest so callers can swap the import later
// without changing call sites.
package digest

import (
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"strings"
)

// Digest is a type-safe content-addressable identifier.
// Format: "<algorithm>:<hex-encoded-hash>" (e.g. "sha256:abc123...").
type Digest string

// SHA256 is the canonical algorithm name.
const SHA256 = "sha256"

// FromBytes computes the SHA-256 digest of b.
func FromBytes(b []byte) Digest {
	h := sha256.Sum256(b)
	return newDigest(SHA256, h[:])
}

// FromString computes the SHA-256 digest of the UTF-8 encoding of s.
func FromString(s string) Digest {
	return FromBytes([]byte(s))
}

// NewDigestFromBytes constructs a Digest from a pre-computed hash.
// algorithm should be the lowercase algorithm name (e.g. "sha256").
func NewDigestFromBytes(algorithm string, sum []byte) Digest {
	return newDigest(algorithm, sum)
}

// Validate returns an error when d is not a well-formed digest.
func (d Digest) Validate() error {
	s := string(d)
	colon := strings.IndexByte(s, ':')
	if colon <= 0 {
		return fmt.Errorf("digest: invalid format %q (missing ':')", d)
	}
	algo := s[:colon]
	if algo == "" {
		return fmt.Errorf("digest: empty algorithm in %q", d)
	}
	hex := s[colon+1:]
	if len(hex) == 0 {
		return fmt.Errorf("digest: empty hex in %q", d)
	}
	for _, c := range hex {
		if !isHexRune(c) {
			return fmt.Errorf("digest: invalid hex character %q in %q", c, d)
		}
	}
	return nil
}

// Algorithm returns the algorithm prefix of d.
func (d Digest) Algorithm() string {
	s := string(d)
	if i := strings.IndexByte(s, ':'); i > 0 {
		return s[:i]
	}
	return ""
}

// Hex returns the hex portion of d.
func (d Digest) Hex() string {
	s := string(d)
	if i := strings.IndexByte(s, ':'); i >= 0 {
		return s[i+1:]
	}
	return s
}

// String implements fmt.Stringer.
func (d Digest) String() string { return string(d) }

// newDigest formats a hash as a typed digest string.
func newDigest(algorithm string, sum []byte) Digest {
	return Digest(algorithm + ":" + hex.EncodeToString(sum))
}

// isHexRune reports whether c is a valid lowercase hex character.
func isHexRune(c rune) bool {
	return (c >= '0' && c <= '9') || (c >= 'a' && c <= 'f')
}
