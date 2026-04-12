package fshash

import (
	"crypto/md5"
	"crypto/sha1"
	"crypto/sha256"
	"crypto/sha512"
	"encoding/binary"
	"fmt"
	"hash"
	"hash/crc32"
	"io/fs"
)

// Algorithm identifies a hash algorithm.
type Algorithm string

const (
	SHA256   Algorithm = "sha256"
	SHA512   Algorithm = "sha512"
	SHA1     Algorithm = "sha1"
	MD5      Algorithm = "md5"
	XXHash64 Algorithm = "xxhash64"
	XXHash3  Algorithm = "xxhash3" // 64-bit output; ~2× faster than xxhash64
	Blake3   Algorithm = "blake3"  // 256-bit; cryptographic; ~8× faster than SHA-256
	CRC32C   Algorithm = "crc32c"  // 32-bit; hardware-accelerated; storage checksums
)

// Hasher supplies fresh hash.Hash instances. Must be safe for concurrent use.
type Hasher interface {
	New() hash.Hash
	Algorithm() string
}

type stdHasher struct {
	algo    Algorithm
	newFunc func() hash.Hash
}

func (s *stdHasher) New() hash.Hash    { return s.newFunc() }
func (s *stdHasher) Algorithm() string { return string(s.algo) }

// crc32cTable is the Castagnoli polynomial table for CRC32C.
// Computed once and reused; the Go runtime uses hardware CRC32C instructions
// (SSE4.2 / ARMv8 CRC extension) when available.
var crc32cTable = crc32.MakeTable(crc32.Castagnoli)

// NewHasher returns the built-in Hasher for the named algorithm.
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
	case XXHash64:
		return &stdHasher{algo: XXHash64, newFunc: func() hash.Hash { return newXXHash64(0) }}, nil
	case XXHash3:
		return &stdHasher{algo: XXHash3, newFunc: func() hash.Hash { return newXXHash3(0) }}, nil
	case Blake3:
		return &stdHasher{algo: Blake3, newFunc: func() hash.Hash { return newBlake3() }}, nil
	case CRC32C:
		return &stdHasher{algo: CRC32C, newFunc: func() hash.Hash { return crc32.New(crc32cTable) }}, nil
	default:
		return nil, fmt.Errorf("fshash: unknown algorithm %q", algo)
	}
}

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
	MetaMode    MetaFlag = 1 << 0
	MetaSize    MetaFlag = 1 << 1
	MetaMtime   MetaFlag = 1 << 2
	MetaSymlink MetaFlag = 1 << 3

	// MetaModeAndSize is the default: reproducible across machines, sensitive
	// to permission changes (which affect executable semantics).
	MetaModeAndSize MetaFlag = MetaMode | MetaSize
)

// mustWrite panics on hash.Write error (spec: hash.Hash.Write never errors).
func mustWrite(h hash.Hash, p []byte) {
	if _, err := h.Write(p); err != nil {
		panic("fshash: hash.Write: " + err.Error())
	}
}

// metaHeaderMaxSize: 1 sentinel + 4 mode + 8 size + 8 mtime = 21 bytes.
const metaHeaderMaxSize = 21

// writeMetaHeader serialises selected metadata into h in a SINGLE Write call
// for the fixed-width portion (SKILL §5 — minimise interface dispatch).
//
// Encoding (big-endian, fixed-width):
//
//	[0xFF]          1-byte sentinel
//	[mode uint32]   when MetaMode is set
//	[size uint64]   when MetaSize is set
//	[mtime uint64]  when MetaMtime is set
//	[target][0x00]  when MetaSymlink is set and target != ""
func writeMetaHeader(h hash.Hash, fi fs.FileInfo, flags MetaFlag, symlinkTarget string) {
	if flags == MetaNone {
		return
	}
	var buf [metaHeaderMaxSize]byte
	n := 0
	buf[n] = 0xFF // sentinel byte
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
	mustWrite(h, buf[:n])
	if flags&MetaSymlink != 0 && symlinkTarget != "" {
		writeString(h, symlinkTarget)
		mustWrite(h, []byte{0x00})
	}
}
