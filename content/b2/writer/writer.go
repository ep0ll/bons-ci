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
}

// Close implements Writer.
func (w *writer) Close() error {
	return w.writer.Close()
}

// Commit implements Writer.
func (w *writer) Commit(ctx context.Context, size int64, expected digest.Digest, opts ...content.Opt) error {
	// Check whether read has already thrown an error
	if w.writer != nil {
		if _, err := w.writer.Write([]byte{}); err != nil && !errors.Is(err, io.ErrClosedPipe) {
			return fmt.Errorf("pipe error before commit: %w", err)
		}
		if err := w.writer.Close(); err != nil {
			return fmt.Errorf("cannot commit on closed writer: %w", errdefs.ErrFailedPrecondition)
		}
	}

	predictedSize := w.size
	if predictedSize == -1 {
		predictedSize = w.info.Size
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

	stat, err := w.client.StatObject(ctx, w.info.Bucket, w.info.Key, minio.GetObjectOptions{})
	if err != nil {
		return err
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

	return err
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
	if n, err = w.writer.Write(p); err != nil {
		return n, err
	} else {
		w.checksum.Hash().Write(p[:n])
		w.offset += int64(len(p))
		w.UpdatedAt = time.Now()
	}

	go w.once.Do(func() {
		defer func() {
			if err := w.reader.Close(); err != nil {
				w.cancel(err)
			}
		}()
		info, err := w.client.PutObject(w.ctx, w.bucket, w.object, w.reader, w.size, minio.PutObjectOptions{
			ContentType: "application/octet-stream",
		})
		if err != nil {
			w.cancel(err)
		}
		w.info = &info
	})
	return n, err
}
