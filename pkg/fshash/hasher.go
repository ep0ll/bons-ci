package fshash

import (
	"crypto/md5"  //nolint:gosec
	"crypto/sha1" //nolint:gosec
	"crypto/sha256"
	"crypto/sha512"
	"encoding/binary"
	"fmt"
	"hash"
	"io/fs"
)

// Algorithm identifies a hash algorithm.
type Algorithm string

const (
	SHA256 Algorithm = "sha256"
	SHA512 Algorithm = "sha512"
	SHA1   Algorithm = "sha1" // legacy; avoid in new code
	MD5    Algorithm = "md5"  // legacy; avoid in new code
)

// Hasher is the single-method interface that the core checksummer calls to
// obtain a fresh hash.Hash for each hashing operation. Implement this
// interface to plug in any hash algorithm (e.g. BLAKE3, xxHash).
//
// Implementations MUST be safe for concurrent use.
type Hasher interface {
	// New returns a new, empty hash.Hash.
	New() hash.Hash
	// Algorithm returns a human-readable name used in error messages.
	Algorithm() string
}

// stdHasher wraps a standard-library hash constructor.
type stdHasher struct {
	algo    Algorithm
	newFunc func() hash.Hash
}

func (s *stdHasher) New() hash.Hash    { return s.newFunc() }
func (s *stdHasher) Algorithm() string { return string(s.algo) }

// NewHasher returns the built-in [Hasher] for the named algorithm.
func NewHasher(algo Algorithm) (Hasher, error) {
	switch algo {
	case SHA256:
		return &stdHasher{algo: SHA256, newFunc: sha256.New}, nil
	case SHA512:
		return &stdHasher{algo: SHA512, newFunc: sha512.New}, nil
	case SHA1:
		return &stdHasher{algo: SHA1, newFunc: sha1.New}, nil //nolint:gosec
	case MD5:
		return &stdHasher{algo: MD5, newFunc: md5.New}, nil //nolint:gosec
	default:
		return nil, fmt.Errorf("fshash: unknown algorithm %q", algo)
	}
}

// mustHasher panics if NewHasher returns an error.
func mustHasher(algo Algorithm) Hasher {
	h, err := NewHasher(algo)
	if err != nil {
		panic(err)
	}
	return h
}

// MetaFlag controls which metadata fields are mixed into a file's digest.
type MetaFlag uint8

const (
	MetaNone    MetaFlag = 0
	MetaMode    MetaFlag = 1 << 0 // include unix permission bits
	MetaSize    MetaFlag = 1 << 1 // include file size
	MetaMtime   MetaFlag = 1 << 2 // include modification time (breaks hermeticity)
	MetaSymlink MetaFlag = 1 << 3 // include symlink target string

	// MetaModeAndSize is the default: mode bits affect executable semantics;
	// timestamps do not.
	MetaModeAndSize MetaFlag = MetaMode | MetaSize
)

// mustWrite panics if hash.Hash.Write returns an error, surfacing bad custom
// Hasher implementations immediately instead of silently producing wrong digests.
func mustWrite(h hash.Hash, p []byte) {
	if _, err := h.Write(p); err != nil {
		panic("fshash: hash.Write: " + err.Error())
	}
}

// metaHeaderMaxSize is the maximum number of bytes writeMetaHeader can emit:
//   1 sentinel + 4 mode + 8 size + 8 mtime = 21 bytes
//   (symlink target is variable-length and appended separately)
const metaHeaderMaxSize = 21

// writeMetaHeader serialises the selected metadata fields into h in a single
// Write call (for the fixed-width portion) to minimise hash-interface dispatch.
//
// Encoding (big-endian, fixed-width):
//
//	[0xFF]                  1-byte sentinel
//	[mode uint32 BE]        present when MetaMode is set
//	[size uint64 BE]        present when MetaSize is set
//	[mtime int64 BE]        present when MetaMtime is set
//	[target string] [0x00]  present when MetaSymlink is set and target != ""
//
// All bytes are written in a single h.Write call (fixed part) followed by at
// most one more call (symlink target), saving multiple interface dispatches per
// file compared to the previous per-field approach.
func writeMetaHeader(h hash.Hash, fi fs.FileInfo, flags MetaFlag, symlinkTarget string) {
	if flags == MetaNone {
		return
	}

	// Pack the fixed-width portion into a stack buffer — no heap allocation.
	var buf [metaHeaderMaxSize]byte
	n := 0

	buf[n] = 0xFF // sentinel distinguishes "has metadata header" from raw content
	n++

	if flags&MetaMode != 0 {
		binary.BigEndian.PutUint32(buf[n:], uint32(fi.Mode()))
		n += 4
	}
	if flags&MetaSize != 0 {
		binary.BigEndian.PutUint64(buf[n:], uint64(fi.Size()))
		n += 8
	}
	if flags&MetaMtime != 0 {
		binary.BigEndian.PutUint64(buf[n:], uint64(fi.ModTime().UnixNano()))
		n += 8
	}

	mustWrite(h, buf[:n]) // single Write for the fixed portion

	if flags&MetaSymlink != 0 && symlinkTarget != "" {
		writeString(h, symlinkTarget)
		mustWrite(h, []byte{0x00}) // NUL terminator
	}
}
