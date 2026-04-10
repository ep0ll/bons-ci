package b2

import (
	"context"
	"errors"
	"fmt"
	"hash"
	"io"
	"maps"
	"sync"
	"time"

	"github.com/containerd/containerd/v2/core/content"
	"github.com/containerd/errdefs"
	"github.com/opencontainers/go-digest"
)

// contentWriter implements content.Writer by streaming data to a B2/S3
// object via the ObjectStorage.PutObject method.
//
// The upload is lazily started on the first Write call: a goroutine reads
// from an internal pipe while Write pushes data into that pipe and
// simultaneously feeds a SHA-256 digester.
type contentWriter struct {
	backend ObjectStorage
	bucket  string
	object  string

	ctx    context.Context
	cancel context.CancelCauseFunc

	pipeR *io.PipeReader
	pipeW *io.PipeWriter

	offset    int64
	size      int64
	digester  digest.Digester
	hashW     hash.Hash
	startedAt time.Time
	ref       string
	hooks     []Hook

	once      sync.Once
	mu        sync.Mutex
	done      chan struct{}
	putErr    error
	result    *UploadResult
	committed bool
	updatedAt time.Time
}

// newContentWriter creates a contentWriter. objectPath must be the fully
// qualified tenant-scoped S3 key.
func newContentWriter(
	ctx context.Context,
	backend ObjectStorage,
	bucket, objectPath, ref string,
	size int64,
	hooks []Hook,
) (*contentWriter, error) {
	if objectPath == "" {
		return nil, fmt.Errorf("b2/writer: object path must not be empty")
	}

	pr, pw := io.Pipe()
	gctx, cancel := context.WithCancelCause(ctx)
	digester := digest.Canonical.Digester()

	return &contentWriter{
		backend:   backend,
		bucket:    bucket,
		object:    objectPath,
		ctx:       gctx,
		cancel:    cancel,
		pipeR:     pr,
		pipeW:     pw,
		offset:    0,
		size:      size,
		digester:  digester,
		hashW:     digester.Hash(),
		startedAt: time.Now(),
		ref:       ref,
		hooks:     hooks,
		done:      make(chan struct{}),
	}, nil
}

// Write pushes p into the upload stream and updates the rolling digest.
func (w *contentWriter) Write(p []byte) (int, error) {
	w.once.Do(func() {
		go func() {
			defer close(w.done)
			result, err := w.backend.PutObject(
				w.ctx, w.bucket, w.object,
				w.pipeR, w.size,
				"application/octet-stream",
			)
			w.mu.Lock()
			defer w.mu.Unlock()
			if err != nil {
				w.putErr = err
				w.cancel(err)
				return
			}
			w.result = &result
		}()
	})

	n, err := w.pipeW.Write(p)
	if err != nil {
		return n, err
	}
	w.hashW.Write(p[:n])

	w.mu.Lock()
	w.offset += int64(n)
	w.updatedAt = time.Now()
	w.mu.Unlock()

	return n, nil
}

// Close closes the write side and waits for the upload goroutine.
func (w *contentWriter) Close() error {
	if err := w.pipeW.Close(); err != nil && !errors.Is(err, io.ErrClosedPipe) {
		return fmt.Errorf("b2/writer: close pipe: %w", err)
	}
	<-w.done

	w.mu.Lock()
	defer w.mu.Unlock()
	return w.putErr
}

// Commit finalises the upload, verifying size and digest constraints.
func (w *contentWriter) Commit(ctx context.Context, size int64, expected digest.Digest, opts ...content.Opt) error {
	w.mu.Lock()
	if w.committed {
		w.mu.Unlock()
		return fmt.Errorf("b2/writer: already committed: %w", errdefs.ErrFailedPrecondition)
	}
	w.mu.Unlock()

	if err := w.pipeW.Close(); err != nil && !errors.Is(err, io.ErrClosedPipe) {
		return fmt.Errorf("b2/writer: close pipe for commit: %w", err)
	}

	select {
	case <-w.done:
	case <-ctx.Done():
		return fmt.Errorf("b2/writer: commit cancelled: %w", ctx.Err())
	}

	w.mu.Lock()
	defer w.mu.Unlock()

	if w.putErr != nil {
		return fmt.Errorf("b2/writer: upload failed: %w", w.putErr)
	}
	if w.result == nil {
		return fmt.Errorf("b2/writer: upload not initiated: %w", errdefs.ErrFailedPrecondition)
	}

	// Verify size.
	actualSize := w.offset
	if w.size > 0 {
		actualSize = w.size
	}
	if size > 0 && actualSize != size {
		return fmt.Errorf("b2/writer: size mismatch: got %d, want %d: %w",
			actualSize, size, errdefs.ErrFailedPrecondition)
	}

	// Verify digest.
	actualDigest := w.digester.Digest()
	if expected != "" && actualDigest != expected {
		return fmt.Errorf("b2/writer: digest mismatch: got %s, want %s: %w",
			actualDigest, expected, errdefs.ErrFailedPrecondition)
	}

	// Apply commit options (labels).
	var base content.Info
	for _, opt := range opts {
		if err := opt(&base); err != nil {
			return fmt.Errorf("b2/writer: apply commit option: %w", err)
		}
	}

	if len(base.Labels) > 0 {
		if err := w.applyLabels(ctx, base.Labels); err != nil {
			return err
		}
	}

	w.committed = true

	// Emit committed event.
	evt := Event{
		Kind:      EventBlobCommitted,
		Digest:    actualDigest,
		Size:      actualSize,
		Ref:       w.ref,
		Labels:    base.Labels,
		Timestamp: time.Now(),
	}
	for _, h := range w.hooks {
		h.OnEvent(ctx, evt)
	}

	return nil
}

// applyLabels merges labels into the existing object metadata.
func (w *contentWriter) applyLabels(ctx context.Context, labels map[string]string) error {
	meta, err := w.backend.StatObject(ctx, w.result.Bucket, w.result.Key)
	if err != nil {
		return fmt.Errorf("b2/writer: stat for label update: %w", err)
	}

	metadata := meta.Metadata
	if metadata == nil {
		metadata = make(map[string]string)
	}
	maps.Copy(metadata, labels)

	_, err = w.backend.CopyObjectMetadata(ctx, w.result.Bucket, w.result.Key, metadata)
	if err != nil {
		return fmt.Errorf("b2/writer: update labels: %w", err)
	}
	return nil
}

// Digest returns the digest of all data written so far.
func (w *contentWriter) Digest() digest.Digest {
	return w.digester.Digest()
}

// Status returns the current upload status.
func (w *contentWriter) Status() (content.Status, error) {
	w.mu.Lock()
	defer w.mu.Unlock()

	st := content.Status{
		Ref:       w.ref,
		Offset:    w.offset,
		Total:     w.size,
		Expected:  w.digester.Digest(),
		StartedAt: w.startedAt,
	}

	if !w.updatedAt.IsZero() {
		st.UpdatedAt = w.updatedAt
	} else if w.result != nil {
		st.UpdatedAt = w.result.LastModified
	}

	return st, nil
}

// Truncate is not supported for B2 uploads.
func (w *contentWriter) Truncate(size int64) error {
	return fmt.Errorf("b2/writer: truncate not supported: %w", errdefs.ErrNotImplemented)
}

// compile-time check
var _ content.Writer = (*contentWriter)(nil)
