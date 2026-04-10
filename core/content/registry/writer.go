package registry

import (
	"context"
	"fmt"
	"sync"
	"time"

	"github.com/containerd/containerd/v2/core/content"
	digest "github.com/opencontainers/go-digest"
	v1 "github.com/opencontainers/image-spec/specs-go/v1"
)

// contentWriter implements content.Writer by dual-writing to a remote
// registry (primary) and a local cache store (best-effort).
//
// On Commit, the writer removes itself from the Store's active ingestion
// tracker and emits an EventBlobPushed event.
type contentWriter struct {
	store  *Store
	ref    string
	desc   v1.Descriptor
	remote content.Writer // primary: remote registry push
	local  content.Writer // best-effort: local cache

	mu        sync.Mutex
	offset    int64
	committed bool
}

func newContentWriter(
	store *Store,
	ref string,
	desc v1.Descriptor,
	remote content.Writer,
	local content.Writer,
) *contentWriter {
	return &contentWriter{
		store:  store,
		ref:    ref,
		desc:   desc,
		remote: remote,
		local:  local,
	}
}

// Write pushes data to the remote registry and best-effort mirrors it to
// the local cache.
func (w *contentWriter) Write(p []byte) (int, error) {
	n, err := w.remote.Write(p)
	if err != nil {
		return n, err
	}

	// Best-effort write to local cache.
	if w.local != nil {
		w.local.Write(p[:n])
	}

	w.mu.Lock()
	w.offset += int64(n)
	w.mu.Unlock()

	w.store.updateIngestion(w.ref)
	return n, nil
}

// Commit finalises the upload to the remote registry and best-effort commits
// the local cache copy. Removes the active ingestion and emits EventBlobPushed.
func (w *contentWriter) Commit(ctx context.Context, size int64, expected digest.Digest, opts ...content.Opt) error {
	w.mu.Lock()
	if w.committed {
		w.mu.Unlock()
		return storeErr(ErrKindPrecondition, "Commit", "already committed", nil)
	}
	w.mu.Unlock()

	// Commit to remote (primary).
	if err := w.remote.Commit(ctx, size, expected, opts...); err != nil {
		return fmt.Errorf("registry: remote commit: %w", err)
	}

	// Best-effort commit to local cache.
	if w.local != nil {
		w.local.Commit(ctx, size, expected, opts...)
	}

	w.mu.Lock()
	w.committed = true
	w.mu.Unlock()

	// Remove from active ingestions.
	w.store.removeIngestion(w.ref)

	// Invalidate info cache.
	if expected != "" {
		w.store.infoCache.Delete(expected)
	}

	// Emit event.
	w.store.emit(ctx, Event{
		Kind:      EventBlobPushed,
		Digest:    expected,
		Size:      size,
		Ref:       w.ref,
		Timestamp: time.Now(),
	})

	return nil
}

// Close closes both writers and removes the active ingestion.
func (w *contentWriter) Close() error {
	var errs []error

	if err := w.remote.Close(); err != nil {
		errs = append(errs, fmt.Errorf("remote: %w", err))
	}
	if w.local != nil {
		if err := w.local.Close(); err != nil {
			errs = append(errs, fmt.Errorf("local: %w", err))
		}
	}

	w.store.removeIngestion(w.ref)

	if len(errs) > 0 {
		return fmt.Errorf("registry/writer: close errors: %v", errs)
	}
	return nil
}

// Digest returns the digest of all data written so far.
func (w *contentWriter) Digest() digest.Digest {
	return w.remote.Digest()
}

// Status returns the current write status.
func (w *contentWriter) Status() (content.Status, error) {
	return w.remote.Status()
}

// Truncate resets the writer to the given offset.
func (w *contentWriter) Truncate(size int64) error {
	return w.remote.Truncate(size)
}

// compile-time check
var _ content.Writer = (*contentWriter)(nil)
