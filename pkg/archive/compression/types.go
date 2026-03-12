package compression

import (
	"bufio"
	"io"
	"sync"
)

var (
	bufioReader32KPool = &sync.Pool{
		New: func() interface{} { return bufio.NewReaderSize(nil, 1<<20) },
	}
)

type (
	// Compression is the state represents if compressed or not.
	Compression int
)

const (
	// Uncompressed represents the uncompressed.
	Uncompressed Compression = iota
	// Gzip is gzip compression algorithm.
	Gzip
	// Zstd is zstd compression algorithm.
	Zstd
	// Unknown is used when a plugin handles the algorithm.
	Unknown
)

// DecompressReadCloser include the stream after decompress and the compress method detected.
type DecompressReadCloser interface {
	io.ReadCloser
	// GetCompression returns the compress method which is used before decompressing
	GetCompression() Compression
}

// Extension returns the extension of a file that uses the specified compression algorithm.
func (compression *Compression) Extension() string {
	switch *compression {
	case Gzip:
		return "gz"
	case Zstd:
		return "zst"
	case Unknown:
		return "unknown"
	}
	return ""
}
