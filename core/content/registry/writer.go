package registry

import (
	"context"
	"fmt"
	"sync"
	"sync/atomic"
	"time"

	"github.com/containerd/containerd/v2/core/content"
	digest "github.com/opencontainers/go-digest"
	v1 "github.com/opencontainers/image-spec/specs-go/v1"
)

// contentWriter implements content.Writer with a concurrent dual-write strategy:
//
//   - Remote (primary): synchronous — caller's Write blocks only until the remote
//     registry has accepted the bytes.
//   - Local (best-effort cache): asynchronous — bytes are copied into a pool
//     buffer and enqueued to a single background goroutine, so the caller NEVER
//     waits on local disk I/O.
//
// A single sequential goroutine drains the channel so the local writer receives
// bytes in order (required for digest correctness).
//
// Commit uses atomic CAS on `committed` to prevent double-commit without a mutex.
// Write offset is tracked with atomic.AddInt64 — no lock on the hot Write path.
type contentWriter struct {
	// Hot fields: touched on every Write call. First cache line.
	offset    int64 // bytes accepted by remote; updated atomically
	committed int32 // 0 or 1; guarded by CAS in Commit
	_         [56]byte

	// Cold fields: set at construction, then read-only.
	store  *Store
	ref    string
	desc   v1.Descriptor
	remote content.Writer
	local  content.Writer // nil when no local cache is available

	// Async local-write pipeline.
	localCh   chan localChunk // bounded channel; deep enough to absorb bursts
	localWg   sync.WaitGroup  // tracks the single localWorker goroutine
	localOnce sync.Once       // ensures localCh is closed exactly once
}

// localChunk is a pool-owned byte slice enqueued for async local writing.
type localChunk struct {
	buf *[]byte
	n   int
}

const defaultLocalChanCap = 32

func newContentWriter(
	s *Store,
	ref string,
	desc v1.Descriptor,
	remote content.Writer,
	local content.Writer,
	chanCap int,
) *contentWriter {
	if chanCap <= 0 {
		chanCap = defaultLocalChanCap
	}
	cw := &contentWriter{
		store:  s,
		ref:    ref,
		desc:   desc,
		remote: remote,
		local:  local,
	}
	if local != nil {
		cw.localCh = make(chan localChunk, chanCap)
		cw.localWg.Add(1)
		go cw.localWorker()
	}
	return cw
}

// localWorker drains localCh and writes each chunk to the local cache writer
// sequentially (ordering is mandatory for digest correctness). Exits when closed.
func (w *contentWriter) localWorker() {
	defer w.localWg.Done()
	for chunk := range w.localCh {
		_, _ = w.local.Write((*chunk.buf)[:chunk.n]) // best-effort; errors ignored
		poolPut(chunk.buf)
	}
}

// closeLocalCh closes the async channel exactly once (safe from Write/Close/Commit).
func (w *contentWriter) closeLocalCh() {
	if w.local != nil {
		w.localOnce.Do(func() { close(w.localCh) })
	}
}

// Write pushes p to the remote registry (synchronous) and enqueues a pool-copy
// to the local cache goroutine (asynchronous, best-effort).
//
// If the local channel is full the local write is silently dropped — the caller
// is never blocked on local I/O.
func (w *contentWriter) Write(p []byte) (int, error) {
	n, err := w.remote.Write(p)
	if err != nil {
		return n, err
	}

	if w.local != nil && n > 0 {
		buf := poolGet(n)
		copy(*buf, p[:n]) // one copy so the goroutine owns its slice
		select {
		case w.localCh <- localChunk{buf: buf, n: n}:
		default:
			poolPut(buf) // channel full; drop (best-effort)
		}
	}

	atomic.AddInt64(&w.offset, int64(n))
	w.store.ingestions.Touch(w.ref)
	return n, nil
}

// Commit finalises the upload:
//  1. CAS committed 0→1 (prevents double-commit without a mutex).
//  2. Commits to remote (primary; resets CAS on failure to allow retry).
//  3. Drains async local writes via localWg.Wait(), then commits local cache.
//  4. Removes the ingestion, invalidates info cache, emits EventBlobPushed.
func (w *contentWriter) Commit(ctx context.Context, size int64, expected digest.Digest, opts ...content.Opt) error {
	if !atomic.CompareAndSwapInt32(&w.committed, 0, 1) {
		return storeErr(ErrKindPrecondition, "Commit", "already committed", nil)
	}

	// Primary commit — must succeed.
	if err := w.remote.Commit(ctx, size, expected, opts...); err != nil {
		atomic.StoreInt32(&w.committed, 0) // permit retry
		return fmt.Errorf("registry: remote commit: %w", err)
	}

	// Drain async writes, then best-effort commit local cache.
	if w.local != nil {
		w.closeLocalCh()
		w.localWg.Wait()
		_ = w.local.Commit(ctx, size, expected, opts...)
	}

	w.store.ingestions.Remove(w.ref)
	if expected != "" {
		w.store.infoCache.Delete(expected)
	}
	w.store.emit(ctx, Event{
		Kind:      EventBlobPushed,
		Digest:    expected,
		Size:      size,
		Ref:       w.ref,
		Timestamp: time.Now(),
	})
	return nil
}

// Close shuts down the local goroutine and closes both writers.
// Removes the active ingestion record.
func (w *contentWriter) Close() error {
	w.closeLocalCh()
	if w.local != nil {
		w.localWg.Wait()
		_ = w.local.Close()
	}
	w.store.ingestions.Remove(w.ref)
	return w.remote.Close()
}

// Digest returns the digest of bytes written to the remote writer so far.
func (w *contentWriter) Digest() digest.Digest { return w.remote.Digest() }

// Status returns the current ingestion status from the remote writer.
func (w *contentWriter) Status() (content.Status, error) { return w.remote.Status() }

// Truncate resets the remote writer to size. Local async goroutine is not
// rewound; the local cache copy is abandoned on truncate.
func (w *contentWriter) Truncate(size int64) error { return w.remote.Truncate(size) }

// compile-time check
var _ content.Writer = (*contentWriter)(nil)
