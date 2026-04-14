package b2

import (
	"context"
	"fmt"
	"io"

	"github.com/minio/minio-go/v7"
)

// minioBackend implements ObjectStorage using the minio-go client.
// This is the only file that imports minio-go directly.
type minioBackend struct {
	client *minio.Client
}

// NewMinioBackend creates an ObjectStorage backed by a minio client.
func NewMinioBackend(client *minio.Client) ObjectStorage {
	return &minioBackend{client: client}
}

// ---------------------------------------------------------------------------
// ObjectStorage implementation
// ---------------------------------------------------------------------------

func (m *minioBackend) StatObject(ctx context.Context, bucket, key string) (ObjectMeta, error) {
	info, err := m.client.StatObject(ctx, bucket, key, minio.GetObjectOptions{})
	if err != nil {
		return ObjectMeta{}, err
	}
	return toObjectMeta(info), nil
}

func (m *minioBackend) GetObject(ctx context.Context, bucket, key string) (ObjectReader, error) {
	obj, err := m.client.GetObject(ctx, bucket, key, minio.GetObjectOptions{})
	if err != nil {
		return nil, err
	}
	stat, err := obj.Stat()
	if err != nil {
		obj.Close()
		return nil, err
	}
	return &minioObjectReader{Object: obj, size: stat.Size}, nil
}

func (m *minioBackend) PutObject(ctx context.Context, bucket, key string, r io.Reader, size int64, contentType string) (UploadResult, error) {
	info, err := m.client.PutObject(ctx, bucket, key, r, size, minio.PutObjectOptions{
		ContentType: contentType,
	})
	if err != nil {
		return UploadResult{}, err
	}
	return toUploadResult(info), nil
}

func (m *minioBackend) CopyObjectMetadata(ctx context.Context, bucket, key string, meta map[string]string) (UploadResult, error) {
	// Stat the existing object to get its ETag for conditional copy.
	stat, err := m.client.StatObject(ctx, bucket, key, minio.GetObjectOptions{})
	if err != nil {
		return UploadResult{}, err
	}

	info, err := m.client.CopyObject(ctx, minio.CopyDestOptions{
		Bucket:          bucket,
		Object:          key,
		UserMetadata:    meta,
		ReplaceMetadata: true,
	}, minio.CopySrcOptions{
		Bucket:    bucket,
		Object:    key,
		MatchETag: stat.ETag,
		VersionID: stat.VersionID,
	})
	if err != nil {
		return UploadResult{}, err
	}
	return toUploadResult(info), nil
}

func (m *minioBackend) RemoveObject(ctx context.Context, bucket, key string) error {
	return m.client.RemoveObject(ctx, bucket, key, minio.RemoveObjectOptions{
		ForceDelete: true,
	})
}

func (m *minioBackend) RemoveIncompleteUpload(ctx context.Context, bucket, key string) error {
	return m.client.RemoveIncompleteUpload(ctx, bucket, key)
}

func (m *minioBackend) ListObjects(ctx context.Context, bucket, prefix string, recursive bool) <-chan ObjectEntry {
	ch := m.client.ListObjects(ctx, bucket, minio.ListObjectsOptions{
		Prefix:       prefix,
		Recursive:    recursive,
		WithMetadata: true,
	})

	out := make(chan ObjectEntry)
	go func() {
		defer close(out)
		for obj := range ch {
			out <- ObjectEntry{
				Key:          obj.Key,
				Size:         obj.Size,
				LastModified: obj.LastModified,
				ETag:         obj.ETag,
				Metadata:     obj.UserMetadata,
				Err:          obj.Err,
			}
		}
	}()
	return out
}

func (m *minioBackend) ListIncompleteUploads(ctx context.Context, bucket, prefix string) <-chan UploadEntry {
	ch := m.client.ListIncompleteUploads(ctx, bucket, prefix, true)

	out := make(chan UploadEntry)
	go func() {
		defer close(out)
		for v := range ch {
			out <- UploadEntry{
				Key:       v.Key,
				UploadID:  v.UploadID,
				Size:      v.Size,
				Initiated: v.Initiated,
				Err:       v.Err,
			}
		}
	}()
	return out
}

func (m *minioBackend) BucketExists(ctx context.Context, bucket string) (bool, error) {
	return m.client.BucketExists(ctx, bucket)
}

func (m *minioBackend) MakeBucket(ctx context.Context, bucket, region string) error {
	return m.client.MakeBucket(ctx, bucket, minio.MakeBucketOptions{
		Region: region,
	})
}

// ---------------------------------------------------------------------------
// Type conversions (minio → domain)
// ---------------------------------------------------------------------------

func toObjectMeta(info minio.ObjectInfo) ObjectMeta {
	return ObjectMeta{
		Key:          info.Key,
		Size:         info.Size,
		LastModified: info.LastModified,
		ETag:         info.ETag,
		ContentType:  info.ContentType,
		VersionID:    info.VersionID,
		StorageClass: info.StorageClass,
		Metadata:     info.UserMetadata,
		Checksums: Checksums{
			SHA256: info.ChecksumSHA256,
			SHA1:   info.ChecksumSHA1,
			CRC32:  info.ChecksumCRC32,
			CRC32C: info.ChecksumCRC32C,
		},
	}
}

func toUploadResult(info minio.UploadInfo) UploadResult {
	return UploadResult{
		Bucket:       info.Bucket,
		Key:          info.Key,
		Size:         info.Size,
		LastModified: info.LastModified,
		ETag:         info.ETag,
		VersionID:    info.VersionID,
	}
}

// ---------------------------------------------------------------------------
// minioObjectReader adapts *minio.Object to ObjectReader.
// ---------------------------------------------------------------------------

type minioObjectReader struct {
	*minio.Object
	size int64
}

func (r *minioObjectReader) Size() int64 { return r.size }

// compile-time check
var _ ObjectStorage = (*minioBackend)(nil)

// ensureBucket creates the bucket if it doesn't exist.
func ensureBucket(ctx context.Context, backend ObjectStorage, bucket, region string) error {
	exists, err := backend.BucketExists(ctx, bucket)
	if err != nil {
		return fmt.Errorf("b2: check bucket %q: %w", bucket, err)
	}
	if !exists {
		if err := backend.MakeBucket(ctx, bucket, region); err != nil {
			return fmt.Errorf("b2: create bucket %q: %w", bucket, err)
		}
	}
	return nil
}
