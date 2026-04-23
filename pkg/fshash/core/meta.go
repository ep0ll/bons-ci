package core

import (
	"encoding/binary"
	"hash"
	"io/fs"
)

// ── MetaFlag ──────────────────────────────────────────────────────────────────

// MetaFlag controls which metadata fields are included in a file's digest.
// Combine flags with |.
type MetaFlag uint8

const (
	MetaNone    MetaFlag = 0
	MetaMode    MetaFlag = 1 << 0 // unix permission bits
	MetaSize    MetaFlag = 1 << 1 // file size in bytes
	MetaMtime   MetaFlag = 1 << 2 // modification time (UnixNano)
	MetaSymlink MetaFlag = 1 << 3 // symlink target path

	// MetaModeAndSize is the recommended default: reproducible across machines,
	// sensitive to permission changes that affect executable semantics.
	MetaModeAndSize MetaFlag = MetaMode | MetaSize
)

// ── Header encoding ───────────────────────────────────────────────────────────
//
// Encoding (big-endian, fixed-width, single Write call for the fixed portion):
//
//	[0xFF]           1-byte sentinel (distinguishes meta from raw bytes)
//	[mode uint32]    iff MetaMode
//	[size uint64]    iff MetaSize
//	[mtime uint64]   iff MetaMtime
//	[target][0x00]   iff MetaSymlink && target != ""
//
// metaHeaderCap is the maximum size of the fixed portion (1+4+8+8 = 21 bytes).
const metaHeaderCap = 21

// WriteMetaHeader serialises selected metadata into h.
//
// The fixed portion (sentinel + numeric fields) is encoded into a stack buffer
// and written with a SINGLE h.Write call (SKILL §5 — minimal interface
// dispatch). The optional symlink target follows as two additional writes.
//
// WriteMetaHeader is a no-op when flags == MetaNone.
func WriteMetaHeader(h hash.Hash, fi fs.FileInfo, flags MetaFlag, symlinkTarget string) {
	if flags == MetaNone {
		return
	}
	var buf [metaHeaderCap]byte
	n := 0
	buf[n] = 0xFF // sentinel
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
	MustWrite(h, buf[:n])
	if flags&MetaSymlink != 0 && symlinkTarget != "" {
		WriteString(h, symlinkTarget)
		MustWrite(h, []byte{0x00})
	}
}
