package writer

import (
	"context"
	"errors"
	"fmt"
	"io"
	"maps"
	"sync"
	"time"

	"github.com/containerd/containerd/v2/core/content"
	"github.com/containerd/errdefs"
	"github.com/minio/minio-go/v7"
	"github.com/opencontainers/go-digest"
)

type Writer interface {
	content.Writer
}

type writer struct {
	offset    int64
	size      int64
	object    string
	bucket    string
	checksum  digest.Digester
	client    *minio.Client
	reader    *io.PipeReader
	writer    *io.PipeWriter
	once      sync.Once
	ctx       context.Context
	cancel    context.CancelCauseFunc
	info      *minio.UploadInfo
	ref       string
	StartedAt time.Time
	UpdatedAt time.Time
	done      chan struct{} // signals PutObject goroutine completion
	putErr    error        // error from PutObject goroutine
}

// Close implements Writer.
func (w *writer) Close() error {
	// Close the pipe writer to signal end of stream to PutObject
	if err := w.writer.Close(); err != nil {
		return err
	}

	// Wait for PutObject to finish if it was started
	select {
	case <-w.done:
	default:
	}

	return nil
}

// Commit implements Writer.
func (w *writer) Commit(ctx context.Context, size int64, expected digest.Digest, opts ...content.Opt) error {
	// Close the pipe writer to signal end of stream
	if w.writer != nil {
		if err := w.writer.Close(); err != nil && !errors.Is(err, io.ErrClosedPipe) {
			return fmt.Errorf("cannot close writer for commit: %w", err)
		}
	}

	// Wait for PutObject goroutine to complete
	select {
	case <-w.done:
	case <-ctx.Done():
		return ctx.Err()
	}

	// Check if PutObject had an error
	if w.putErr != nil {
		return fmt.Errorf("upload failed: %w", w.putErr)
	}

	if w.info == nil {
		return fmt.Errorf("upload was not initiated: %w", errdefs.ErrFailedPrecondition)
	}

	predictedSize := w.offset
	if w.size > 0 {
		predictedSize = w.size
	}

	if size > 0 && predictedSize != size {
		return fmt.Errorf("unexpected commit size %d, expected %d: %w", predictedSize, size, errdefs.ErrFailedPrecondition)
	}

	if dgst := w.checksum.Digest(); expected != "" && dgst != expected {
		return fmt.Errorf("unexpected commit digest %s, expected %s: %w", dgst, expected, errdefs.ErrFailedPrecondition)
	}

	var base content.Info
	for _, opt := range opts {
		if err := opt(&base); err != nil {
			return err
		}
	}

	// Update metadata on the uploaded object if labels are provided
	if len(base.Labels) > 0 {
		stat, err := w.client.StatObject(ctx, w.info.Bucket, w.info.Key, minio.GetObjectOptions{})
		if err != nil {
			return err
		}

		if stat.UserMetadata == nil {
			stat.UserMetadata = make(map[string]string)
		}
		maps.Copy(stat.UserMetadata, base.Labels)

		_, err = w.client.CopyObject(ctx, minio.CopyDestOptions{
			Bucket:       w.info.Bucket,
			Object:       w.info.Key,
			ChecksumType: minio.ChecksumSHA256,
			UserMetadata: stat.UserMetadata,
		}, minio.CopySrcOptions{
			Bucket:             w.info.Bucket,
			Object:             w.info.Key,
			MatchETag:          stat.ETag,
			MatchModifiedSince: stat.LastModified,
			VersionID:          stat.VersionID,
		})
		if err != nil {
			return err
		}
	}

	return nil
}

// Digest implements Writer.
func (w *writer) Digest() digest.Digest {
	return w.checksum.Digest()
}

// Status implements Writer.
func (w *writer) Status() (content.Status, error) {
	st := content.Status{
		Ref:       w.ref,
		Offset:    w.offset,
		Total:     w.size,
		Expected:  w.Digest(),
		StartedAt: w.StartedAt,
	}

	if !w.UpdatedAt.IsZero() {
		st.UpdatedAt = w.UpdatedAt
	} else if w.info != nil {
		st.UpdatedAt = w.info.LastModified
	}

	return st, nil
}

// Truncate implements Writer.
func (w *writer) Truncate(size int64) error {
	return errors.New("cannot truncate b2 upload")
}

// Write implements Writer.
func (w *writer) Write(p []byte) (n int, err error) {
	// Start PutObject on first write (reads from the pipe reader)
	w.once.Do(func() {
		go func() {
			defer close(w.done)
			info, putErr := w.client.PutObject(w.ctx, w.bucket, w.object, w.reader, w.size, minio.PutObjectOptions{
				ContentType: "application/octet-stream",
			})
			if putErr != nil {
				w.putErr = putErr
				w.cancel(putErr)
				return
			}
			w.info = &info
		}()
	})

	n, err = w.writer.Write(p)
	if err != nil {
		return n, err
	}

	w.checksum.Hash().Write(p[:n])
	w.offset += int64(n)
	w.UpdatedAt = time.Now()

	return n, nil
}
