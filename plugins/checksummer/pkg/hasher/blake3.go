//go:build linux

// hasher/blake3.go – sequential BLAKE3 hasher using pooled pread64 reads.
// Uses github.com/zeebo/blake3 for the hash function.
package hasher

import (
	"context"
	"fmt"
	"io"
	"os"

	"golang.org/x/sys/unix"
)

const defaultReadBufSize = 1 << 20 // 1 MiB

// Blake3Hasher is a sequential pread64-based BLAKE3 hasher.
// Safe for concurrent use: each call uses a pooled buffer and fresh digest.
type Blake3Hasher struct {
	pool    *BufferPool
	bufSize int
}

// NewBlake3Hasher creates a Blake3Hasher. readBufSize=0 uses 1 MiB default.
func NewBlake3Hasher(readBufSize int) *Blake3Hasher {
	if readBufSize <= 0 {
		readBufSize = defaultReadBufSize
	}
	return &Blake3Hasher{pool: NewBufferPool(), bufSize: readBufSize}
}

// HashFile hashes the named file.
func (h *Blake3Hasher) HashFile(ctx context.Context, path string) ([]byte, error) {
	fd, err := unix.Open(path, unix.O_RDONLY|unix.O_CLOEXEC, 0)
	if err != nil {
		return nil, fmt.Errorf("blake3: open %q: %w", path, err)
	}
	defer unix.Close(fd)

	var st unix.Stat_t
	if err := unix.Fstat(fd, &st); err != nil {
		return nil, fmt.Errorf("blake3: stat %q: %w", path, err)
	}
	return h.HashFD(ctx, fd, st.Size)
}

// HashFD hashes an open file descriptor via pread64 (does not seek).
func (h *Blake3Hasher) HashFD(ctx context.Context, fd int, size int64) ([]byte, error) {
	if size == 0 {
		return EmptyHash(), nil
	}

	dig := newDigest()
	buf := h.pool.GetAtLeast(h.bufSize)
	defer h.pool.Put(buf)

	var offset int64
	for {
		if ctx.Err() != nil {
			return nil, ctx.Err()
		}
		n, err := unix.Pread(fd, buf, offset)
		if n > 0 {
			if _, werr := dig.Write(buf[:n]); werr != nil {
				return nil, fmt.Errorf("blake3: digest write: %w", werr)
			}
			offset += int64(n)
		}
		if err != nil {
			if err == io.EOF {
				break
			}
			return nil, fmt.Errorf("blake3: pread fd=%d off=%d: %w", fd, offset, err)
		}
		if size > 0 && offset >= size {
			break
		}
	}
	return dig.Sum(nil), nil
}

// HashReader hashes all remaining bytes from r.
func (h *Blake3Hasher) HashReader(ctx context.Context, r io.Reader) ([]byte, error) {
	dig := newDigest()
	buf := h.pool.GetAtLeast(h.bufSize)
	defer h.pool.Put(buf)

	for {
		if ctx.Err() != nil {
			return nil, ctx.Err()
		}
		n, err := r.Read(buf)
		if n > 0 {
			if _, werr := dig.Write(buf[:n]); werr != nil {
				return nil, fmt.Errorf("blake3: reader write: %w", werr)
			}
		}
		if err == io.EOF {
			break
		}
		if err != nil {
			return nil, fmt.Errorf("blake3: read: %w", err)
		}
	}
	return dig.Sum(nil), nil
}

// HashOsFile is a convenience wrapper for *os.File.
func (h *Blake3Hasher) HashOsFile(ctx context.Context, f *os.File) ([]byte, error) {
	var st unix.Stat_t
	if err := unix.Fstat(int(f.Fd()), &st); err != nil {
		return nil, err
	}
	return h.HashFD(ctx, int(f.Fd()), st.Size)
}
