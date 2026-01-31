package b2

import (
	"context"
	"fmt"
	"maps"
	"slices"
	"time"

	"github.com/bons/bons-ci/content/b2/reader"
	b2writer "github.com/bons/bons-ci/content/b2/writer"
	"github.com/containerd/containerd/v2/core/content"
	"github.com/containerd/containerd/v2/pkg/filters"
	"github.com/containerd/errdefs"
	"github.com/minio/minio-go/v7"
	digest "github.com/opencontainers/go-digest"
	v1 "github.com/opencontainers/image-spec/specs-go/v1"
)

type S3ContentStore interface {
	content.Store
}

// Abort implements S3ContentStore.
func (b *b2Store) Abort(ctx context.Context, ref string) error {
	return b.client.RemoveIncompleteUpload(ctx, b.cfg.Bucket, b.tenant_prefixer.AsFolder(b.cfg.BlobsPrefix, ref))
}

// Delete implements S3ContentStore.
func (b *b2Store) Delete(ctx context.Context, dgst digest.Digest) error {
	return b.client.RemoveObject(ctx, b.cfg.Bucket, b.tenant_prefixer.BlobPath(dgst), minio.RemoveObjectOptions{
		ForceDelete: true,
	})
}

// Info implements S3ContentStore.
func (b *b2Store) Info(ctx context.Context, dgst digest.Digest) (info content.Info, err error) {
	attr, err := b.client.StatObject(ctx, b.cfg.Bucket, b.tenant_prefixer.BlobPath(dgst), minio.GetObjectOptions{})
	if len(attr.UserMetadata) > 0 {
		info.Labels = make(map[string]string)
		for k, v := range attr.UserMetadata {
			info.Labels[k] = v
		}
	}

	maps.Copy(info.Labels, map[string]string{
		"storage": attr.StorageClass,
		"etag":    attr.ETag,
		"version": attr.VersionID,
		"CRC32":   attr.ChecksumCRC32,
		"CRC32C":  attr.ChecksumCRC32C,
		"SHA1":    attr.ChecksumSHA1,
	})

	info.Digest = digest.Digest(attr.ChecksumSHA256)
	info.Size = attr.Size
	info.UpdatedAt = attr.LastModified
	return info, err
}

func appendStatus(v minio.ObjectMultipartInfo) (content.Status, error) {
	return content.Status{
		Ref:       v.Key,
		Total:     v.Size,
		StartedAt: v.Initiated,
		UpdatedAt: time.Now(),
		Expected:  digest.Digest(v.UploadID),
	}, v.Err
}

// ListStatuses implements S3ContentStore.
func (b *b2Store) ListStatuses(ctx context.Context, filters ...string) (st []content.Status, err error) {
	if len(filters) > 0 {
		for _, filter := range filters {
			for v := range b.client.ListIncompleteUploads(ctx, b.cfg.Bucket, b.tenant_prefixer.AsFolder(filter), true) {
				if s, err := appendStatus(v); err != nil {
					return st, err
				} else {
					st = append(st, s)
				}
			}
		}

		return st, err
	}

	info := b.client.ListIncompleteUploads(ctx, b.cfg.Bucket, b.tenant_prefixer.AsFolder(), true)
	for v := range info {
		if s, err := appendStatus(v); err != nil {
			return st, err
		} else {
			st = append(st, s)
		}
	}

	return st, err
}

// ReaderAt implements S3ContentStore.
func (b *b2Store) ReaderAt(ctx context.Context, desc v1.Descriptor) (content.ReaderAt, error) {
	obj, err := b.client.GetObject(ctx, b.cfg.Bucket, desc.Digest.String(), minio.GetObjectOptions{})
	if err != nil {
		return nil, err
	}

	stat, err := obj.Stat()
	return reader.NewReader(obj, stat), err
}

// Status implements S3ContentStore.
func (b *b2Store) Status(ctx context.Context, ref string) (content.Status, error) {
	for v := range b.client.ListIncompleteUploads(ctx, b.cfg.Bucket, b.tenant_prefixer.AsFolder(ref), false) {
		return appendStatus(v)
	}

	return content.Status{}, fmt.Errorf("%s: %w", fmt.Sprintf("no incomplete uploads by given ref %q", ref), errdefs.ErrNotFound)
}

// Update implements S3ContentStore.
func (b *b2Store) Update(ctx context.Context, info content.Info, fieldpaths ...string) (content.Info, error) {
	obj_folder, err := RetriveTenantPrefix(b.cfg)
	if err != nil {
		return content.Info{}, err
	}
	object := obj_folder.BlobPath(info.Digest)
	stat, err := b.client.StatObject(ctx, b.cfg.Bucket, object, minio.GetObjectOptions{})
	if err != nil {
		return content.Info{}, err
	}

	filter, err := filters.ParseAll(fieldpaths...)
	if err != nil {
		return content.Info{}, err
	}

	filter.Match(adaptUpdate(stat))

	maps.Copy(stat.UserMetadata, info.Labels)

	uinfo, err := b.client.CopyObject(ctx, minio.CopyDestOptions{
		Bucket: b.cfg.Bucket,
		Object: object,
		ChecksumType: minio.ChecksumSHA256,
		UserMetadata: stat.UserMetadata,
	}, minio.CopySrcOptions{
		Bucket: b.cfg.Bucket,
		Object: object,
		MatchETag: stat.ETag,
		MatchModifiedSince: stat.LastModified,
		VersionID: stat.VersionID,
	})

	return content.Info{
		Digest: info.Digest,
		Size: uinfo.Size,
		CreatedAt: info.CreatedAt,
		UpdatedAt: uinfo.LastModified,
		Labels: stat.UserMetadata,
	}, err
}

// Walk implements S3ContentStore.
func (b *b2Store) Walk(ctx context.Context, fn content.WalkFunc, fs ...string) error {
	opts := minio.ListObjectsOptions{
		Prefix:    b.tenant_prefixer.AsFolder(),
		Recursive: true,
	}

	filter, err := filters.ParseAll(fs...)
	if err != nil {
		return err
	}

	for object := range b.client.ListObjects(ctx, b.cfg.Bucket, opts) {
		if !slices.Contains(fs, b.tenant_prefixer.Trim(object.Key)) {
			continue
		}
		
		if object.Err != nil {
			return object.Err
		}

		if !filter.Match(adaptWalk(object)) {
			continue
		}

		// Extract digest from path
		dgst, err := digestFromPath(object.Key)
		if err != nil {
			continue // Skip invalid entries
		}

		info := content.Info{
			Digest:    dgst,
			Size:      object.Size,
			CreatedAt: object.LastModified,
			UpdatedAt: object.LastModified,
		}

		if err := fn(info); err != nil {
			return err
		}
	}

	return nil
}

// Writer implements S3ContentStore.
func (b *b2Store) Writer(ctx context.Context, opts ...content.WriterOpt) (content.Writer, error) {
	var opt = &content.WriterOpts{}
	for _, op := range opts {
		if err := op(opt); err != nil {
			return nil, err
		}
	}

	size := int64(-1)
	if s := opt.Desc.Size; s != 0 {
		size = s
	}

	return b2writer.B2Writer(ctx, b.client, b.cfg.Bucket, b2writer.WithSize(size), b2writer.WithDesc(opt.Desc), b2writer.WithRef(opt.Ref))
}

var _ S3ContentStore = &b2Store{}
