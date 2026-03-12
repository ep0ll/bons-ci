package compression

import (
	"bytes"
	"encoding/binary"
)

const (
	zstdMagicSkippableStart = 0x184D2A50
	zstdMagicSkippableMask  = 0xFFFFFFF0
)

var zstdMagic = []byte{0x28, 0xb5, 0x2f, 0xfd}

// zstdMatcher detects zstd compression algorithm.
// There are two frame formats defined by Zstandard: Zstandard frames and Skippable frames.
// See https://datatracker.ietf.org/doc/html/rfc8878#section-3 for more details.
func zstdMatcher() matcher {
	return func(source []byte) bool {
		if bytes.HasPrefix(source, zstdMagic) {
			// Zstandard frame
			return true
		}
		// skippable frame
		if len(source) < 8 {
			return false
		}
		// magic number from 0x184D2A50 to 0x184D2A5F.
		if binary.LittleEndian.Uint32(source[:4])&zstdMagicSkippableMask == zstdMagicSkippableStart {
			return true
		}
		return false
	}
}
