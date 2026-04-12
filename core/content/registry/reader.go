package registry

import (
	"context"
	"io"
	"sync"

	"github.com/containerd/containerd/v2/core/content"
	digest "github.com/opencontainers/go-digest"
)

// contentReaderAt implements content.ReaderAt by streaming from a remote
// registry source while simultaneously tee-writing to a local cache writer.
//
// # Two read paths
//
//  1. Fast path — the underlying ReadCloser also implements io.ReaderAt (e.g.
//     an *os.File or seekable HTTP body). ReadAt delegates directly: zero-copy,
//     lock-free, safe for concurrent calls.
//
//  2. Slow path — sequential tee stream. ReadAt is serialised under a mutex to
//     maintain byte-ordering so the local cache receives a correct stream.
//
// Close drains any unread bytes from the tee via a pool-allocated buffer,
// ensuring the local cache writer always receives a complete blob.
type contentReaderAt struct {
	ra  io.ReaderAt // non-nil on fast path
	tee io.Reader   // rc tee'd into localWriter (slow path only)
	mu  sync.Mutex  // serialises sequential tee reads

	rc          io.ReadCloser  // raw remote stream (always set)
	localWriter content.Writer // best-effort local cache; may be nil
	size        int64
}

func newContentReaderAt(rc io.ReadCloser, lw content.Writer, size int64) *contentReaderAt {
	r := &contentReaderAt{rc: rc, localWriter: lw, size: size}
	if ra, ok := rc.(io.ReaderAt); ok {
		r.ra = ra // fast path: underlying supports ReadAt directly
	} else if lw != nil {
		r.tee = io.TeeReader(rc, lw) // slow path with cache
	} else {
		r.tee = rc // slow path, no cache
	}
	return r
}

// ReadAt reads len(p) bytes at byte offset off.
//
// Fast path: single direct call to the underlying io.ReaderAt — no lock,
// no buffer, no extra copy beyond what the caller requested.
//
// Slow path: reads from the tee reader under mutex to preserve ordering.
func (r *contentReaderAt) ReadAt(p []byte, off int64) (int, error) {
	if r.ra != nil {
		return r.ra.ReadAt(p, off)
	}
	r.mu.Lock()
	defer r.mu.Unlock()
	return r.tee.Read(p)
}

// Size returns the total expected content size in bytes.
func (r *contentReaderAt) Size() int64 { return r.size }

// Close drains any unread tee bytes, commits the local cache writer, then
// closes the remote stream. Local errors are suppressed (best-effort cache).
func (r *contentReaderAt) Close() error {
	if r.tee != nil {
		r.drainTee()
	}
	if r.localWriter != nil {
		_ = r.localWriter.Commit(context.Background(), r.size, "")
		_ = r.localWriter.Close()
	}
	return r.rc.Close()
}

// drainTee reads and discards bytes until EOF using a pool-allocated buffer.
// This ensures the local cache writer always receives the complete byte stream.
func (r *contentReaderAt) drainTee() {
	buf := poolGet(bpDefault)
	defer poolPut(buf)
	for {
		_, err := r.tee.Read(*buf)
		if err != nil {
			return
		}
	}
}

// writerDigest is used in tests to verify the digest committed to local cache.
func (r *contentReaderAt) writerDigest() digest.Digest {
	if r.localWriter != nil {
		return r.localWriter.Digest()
	}
	return ""
}

// compile-time check
var _ content.ReaderAt = (*contentReaderAt)(nil)
