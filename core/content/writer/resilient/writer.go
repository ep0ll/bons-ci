// Package resilient provides a content.Writer wrapper that absorbs errors
// from the underlying writer.
//
// A resilient writer is intended for use as a "best-effort secondary" — one
// whose failures should not abort the primary write path. Once the wrapped
// writer encounters an error, all subsequent calls become no-ops.
package resilient

import (
	"context"
	"sync/atomic"

	"github.com/containerd/containerd/v2/core/content"
	digest "github.com/opencontainers/go-digest"
)

// Wrap returns a content.Writer that delegates to inner but silently absorbs
// any error. Once inner returns an error, the writer enters a permanently
// degraded state: all subsequent calls succeed vacuously and inner.Close()
// is called exactly once to release resources.
func Wrap(inner content.Writer) content.Writer {
	return &resilientWriter{inner: inner}
}

type resilientWriter struct {
	inner  content.Writer
	failed atomic.Bool
}

// Write forwards p to the inner writer. On failure, the writer is marked
// degraded, inner is closed, and len(p) is returned with no error — allowing
// the caller to proceed as if the write succeeded.
func (r *resilientWriter) Write(p []byte) (int, error) {
	if r.failed.Load() {
		return len(p), nil
	}
	if _, err := r.inner.Write(p); err != nil {
		r.markFailed()
		return len(p), nil // absorb error; caller must not be blocked
	}
	return len(p), nil
}

// Commit forwards the commit to the inner writer. On failure, the writer is
// marked degraded and the error is silently discarded.
func (r *resilientWriter) Commit(ctx context.Context, size int64, expected digest.Digest, opts ...content.Opt) error {
	if r.failed.Load() {
		return nil
	}
	if err := r.inner.Commit(ctx, size, expected, opts...); err != nil {
		r.markFailed()
	}
	return nil
}

// Truncate forwards the truncation. On failure, the writer is marked degraded.
func (r *resilientWriter) Truncate(size int64) error {
	if r.failed.Load() {
		return nil
	}
	if err := r.inner.Truncate(size); err != nil {
		r.markFailed()
	}
	return nil
}

// Close closes the inner writer unless it has already been closed due to a
// prior failure.
func (r *resilientWriter) Close() error {
	if r.failed.Load() {
		return nil // already closed via markFailed
	}
	return r.inner.Close()
}

// Digest delegates to the inner writer. It may return stale or empty results
// if the writer is in a degraded state.
func (r *resilientWriter) Digest() digest.Digest {
	return r.inner.Digest()
}

// Status delegates to the inner writer.
func (r *resilientWriter) Status() (content.Status, error) {
	return r.inner.Status()
}

// markFailed atomically transitions to the degraded state and releases the
// inner writer. It is safe to call from any goroutine.
func (r *resilientWriter) markFailed() {
	if r.failed.CompareAndSwap(false, true) {
		_ = r.inner.Close() // best-effort resource release
	}
}

var _ content.Writer = (*resilientWriter)(nil)
