// Package s3 implements the storage.Backend interface using an S3-compatible
// object store (AWS S3, MinIO, Cloudflare R2, etc.).
package s3

import (
	"context"
	"errors"
	"fmt"
	"io"
	"time"

	"github.com/aws/aws-sdk-go-v2/aws"
	awsconfig "github.com/aws/aws-sdk-go-v2/config"
	"github.com/aws/aws-sdk-go-v2/credentials"
	"github.com/aws/aws-sdk-go-v2/service/s3"
	"github.com/aws/aws-sdk-go-v2/service/s3/types"
	"go.uber.org/zap"

	"github.com/bons/bons-ci/plugins/rbe/internal/storage"
)

// Config holds all tunables for the S3 backend.
type Config struct {
	Bucket          string
	Region          string
	Endpoint        string // custom endpoint for MinIO / R2
	AccessKeyID     string
	SecretAccessKey string
	SessionToken    string
	ForcePathStyle  bool // required for MinIO
	// ChunkSize is the minimum chunk size for multipart uploads (default 8 MiB).
	ChunkSize int64
	// PresignExpiry is the default TTL for presigned URLs.
	PresignExpiry time.Duration
	// KeyPrefix is prepended to every object key (useful for multi-tenant).
	KeyPrefix string
}

const defaultChunkSize = 8 << 20 // 8 MiB

// Backend is the S3 implementation of storage.Backend.
type Backend struct {
	cfg    Config
	client *s3.Client
	psign  *s3.PresignClient
	log    *zap.Logger
}

// New creates and validates an S3 Backend.
func New(ctx context.Context, cfg Config, log *zap.Logger) (*Backend, error) {
	if cfg.Bucket == "" {
		return nil, fmt.Errorf("s3: bucket must not be empty")
	}
	if cfg.ChunkSize == 0 {
		cfg.ChunkSize = defaultChunkSize
	}
	if cfg.PresignExpiry == 0 {
		cfg.PresignExpiry = 15 * time.Minute
	}

	opts := []func(*awsconfig.LoadOptions) error{
		awsconfig.WithRegion(cfg.Region),
	}
	if cfg.AccessKeyID != "" {
		opts = append(opts, awsconfig.WithCredentialsProvider(
			credentials.NewStaticCredentialsProvider(cfg.AccessKeyID, cfg.SecretAccessKey, cfg.SessionToken),
		))
	}

	awsCfg, err := awsconfig.LoadDefaultConfig(ctx, opts...)
	if err != nil {
		return nil, fmt.Errorf("s3: load aws config: %w", err)
	}

	s3Opts := []func(*s3.Options){}
	if cfg.Endpoint != "" {
		s3Opts = append(s3Opts, func(o *s3.Options) {
			o.BaseEndpoint = aws.String(cfg.Endpoint)
			o.UsePathStyle = cfg.ForcePathStyle
		})
	}

	client := s3.NewFromConfig(awsCfg, s3Opts...)
	return &Backend{
		cfg:    cfg,
		client: client,
		psign:  s3.NewPresignClient(client),
		log:    log.Named("s3"),
	}, nil
}

func (b *Backend) key(k string) string {
	if b.cfg.KeyPrefix == "" {
		return k
	}
	return b.cfg.KeyPrefix + "/" + k
}

// ─── Single object ────────────────────────────────────────────────────────────

func (b *Backend) Put(ctx context.Context, k string, r io.Reader, size int64, opts storage.PutOptions) error {
	in := &s3.PutObjectInput{
		Bucket: &b.cfg.Bucket,
		Key:    aws.String(b.key(k)),
		Body:   r,
	}
	if size >= 0 {
		in.ContentLength = aws.Int64(size)
	}
	if opts.ContentType != "" {
		in.ContentType = &opts.ContentType
	}
	if len(opts.Metadata) > 0 {
		in.Metadata = opts.Metadata
	}
	if opts.ExpectedDigest != "" {
		in.ChecksumSHA256 = aws.String(opts.ExpectedDigest)
	}
	_, err := b.client.PutObject(ctx, in)
	if err != nil {
		return fmt.Errorf("s3 put %q: %w", k, err)
	}
	return nil
}

func (b *Backend) Get(ctx context.Context, k string) (io.ReadCloser, int64, error) {
	out, err := b.client.GetObject(ctx, &s3.GetObjectInput{
		Bucket: &b.cfg.Bucket,
		Key:    aws.String(b.key(k)),
	})
	if err != nil {
		if isNotFound(err) {
			return nil, 0, storage.ErrNotFound
		}
		return nil, 0, fmt.Errorf("s3 get %q: %w", k, err)
	}
	var sz int64
	if out.ContentLength != nil {
		sz = *out.ContentLength
	}
	return out.Body, sz, nil
}

func (b *Backend) GetRange(ctx context.Context, k string, offset, length int64) (io.ReadCloser, error) {
	var rng string
	if length > 0 {
		rng = fmt.Sprintf("bytes=%d-%d", offset, offset+length-1)
	} else {
		rng = fmt.Sprintf("bytes=%d-", offset)
	}
	out, err := b.client.GetObject(ctx, &s3.GetObjectInput{
		Bucket: &b.cfg.Bucket,
		Key:    aws.String(b.key(k)),
		Range:  &rng,
	})
	if err != nil {
		if isNotFound(err) {
			return nil, storage.ErrNotFound
		}
		return nil, fmt.Errorf("s3 getrange %q: %w", k, err)
	}
	return out.Body, nil
}

func (b *Backend) Stat(ctx context.Context, k string) (*storage.ObjectInfo, error) {
	out, err := b.client.HeadObject(ctx, &s3.HeadObjectInput{
		Bucket: &b.cfg.Bucket,
		Key:    aws.String(b.key(k)),
	})
	if err != nil {
		if isNotFound(err) {
			return nil, storage.ErrNotFound
		}
		return nil, fmt.Errorf("s3 stat %q: %w", k, err)
	}
	info := &storage.ObjectInfo{Key: k}
	if out.ContentLength != nil {
		info.Size = *out.ContentLength
	}
	if out.ETag != nil {
		info.ETag = *out.ETag
	}
	if out.ContentType != nil {
		info.ContentType = *out.ContentType
	}
	if out.LastModified != nil {
		info.LastModified = *out.LastModified
	}
	info.Metadata = out.Metadata
	return info, nil
}

func (b *Backend) Exists(ctx context.Context, k string) (bool, error) {
	_, err := b.Stat(ctx, k)
	if err == nil {
		return true, nil
	}
	if errors.Is(err, storage.ErrNotFound) {
		return false, nil
	}
	return false, err
}

func (b *Backend) Delete(ctx context.Context, k string) error {
	_, err := b.client.DeleteObject(ctx, &s3.DeleteObjectInput{
		Bucket: &b.cfg.Bucket,
		Key:    aws.String(b.key(k)),
	})
	if err != nil && !isNotFound(err) {
		return fmt.Errorf("s3 delete %q: %w", k, err)
	}
	return nil
}

func (b *Backend) List(ctx context.Context, prefix string, opts storage.ListOptions) (<-chan storage.ObjectInfo, <-chan error) {
	outCh := make(chan storage.ObjectInfo, 256)
	errCh := make(chan error, 1)

	go func() {
		defer close(outCh)
		defer close(errCh)

		p := s3.NewListObjectsV2Paginator(b.client, &s3.ListObjectsV2Input{
			Bucket: &b.cfg.Bucket,
			Prefix: aws.String(b.key(prefix)),
			StartAfter: func() *string {
				if opts.Marker != "" {
					m := b.key(opts.Marker)
					return &m
				}
				return nil
			}(),
		})

		var count int
		for p.HasMorePages() {
			page, err := p.NextPage(ctx)
			if err != nil {
				errCh <- fmt.Errorf("s3 list: %w", err)
				return
			}
			for _, obj := range page.Contents {
				k := ""
				if obj.Key != nil {
					k = *obj.Key
					// Strip KeyPrefix
					if b.cfg.KeyPrefix != "" && len(k) > len(b.cfg.KeyPrefix)+1 {
						k = k[len(b.cfg.KeyPrefix)+1:]
					}
				}
				info := storage.ObjectInfo{Key: k}
				if obj.Size != nil {
					info.Size = *obj.Size
				}
				if obj.ETag != nil {
					info.ETag = *obj.ETag
				}
				if obj.LastModified != nil {
					info.LastModified = *obj.LastModified
				}
				select {
				case outCh <- info:
				case <-ctx.Done():
					errCh <- ctx.Err()
					return
				}
				count++
				if opts.Limit > 0 && count >= opts.Limit {
					return
				}
			}
		}
		errCh <- nil
	}()

	return outCh, errCh
}

func (b *Backend) Copy(ctx context.Context, src, dst string) error {
	source := b.cfg.Bucket + "/" + b.key(src)
	_, err := b.client.CopyObject(ctx, &s3.CopyObjectInput{
		Bucket:     &b.cfg.Bucket,
		CopySource: &source,
		Key:        aws.String(b.key(dst)),
	})
	if err != nil {
		return fmt.Errorf("s3 copy %q→%q: %w", src, dst, err)
	}
	return nil
}

// ─── Multipart ────────────────────────────────────────────────────────────────

func (b *Backend) CreateMultipartUpload(ctx context.Context, k string, opts storage.PutOptions) (string, error) {
	in := &s3.CreateMultipartUploadInput{
		Bucket: &b.cfg.Bucket,
		Key:    aws.String(b.key(k)),
	}
	if opts.ContentType != "" {
		in.ContentType = &opts.ContentType
	}
	if len(opts.Metadata) > 0 {
		in.Metadata = opts.Metadata
	}
	out, err := b.client.CreateMultipartUpload(ctx, in)
	if err != nil {
		return "", fmt.Errorf("s3 create multipart %q: %w", k, err)
	}
	return aws.ToString(out.UploadId), nil
}

func (b *Backend) UploadPart(ctx context.Context, k, uploadID string, partNum int32, r io.Reader, size int64) (storage.Part, error) {
	in := &s3.UploadPartInput{
		Bucket:     &b.cfg.Bucket,
		Key:        aws.String(b.key(k)),
		UploadId:   &uploadID,
		PartNumber: aws.Int32(partNum),
		Body:       r,
	}
	if size > 0 {
		in.ContentLength = aws.Int64(size)
	}
	out, err := b.client.UploadPart(ctx, in)
	if err != nil {
		return storage.Part{}, fmt.Errorf("s3 upload part %d %q: %w", partNum, k, err)
	}
	return storage.Part{PartNumber: partNum, ETag: aws.ToString(out.ETag)}, nil
}

func (b *Backend) CompleteMultipartUpload(ctx context.Context, k, uploadID string, parts []storage.Part) (*storage.ObjectInfo, error) {
	s3Parts := make([]types.CompletedPart, len(parts))
	for i, p := range parts {
		p := p
		s3Parts[i] = types.CompletedPart{PartNumber: aws.Int32(p.PartNumber), ETag: &p.ETag}
	}
	_, err := b.client.CompleteMultipartUpload(ctx, &s3.CompleteMultipartUploadInput{
		Bucket:          &b.cfg.Bucket,
		Key:             aws.String(b.key(k)),
		UploadId:        &uploadID,
		MultipartUpload: &types.CompletedMultipartUpload{Parts: s3Parts},
	})
	if err != nil {
		return nil, fmt.Errorf("s3 complete multipart %q: %w", k, err)
	}
	return b.Stat(ctx, k)
}

func (b *Backend) AbortMultipartUpload(ctx context.Context, k, uploadID string) error {
	_, err := b.client.AbortMultipartUpload(ctx, &s3.AbortMultipartUploadInput{
		Bucket:   &b.cfg.Bucket,
		Key:      aws.String(b.key(k)),
		UploadId: &uploadID,
	})
	if err != nil {
		return fmt.Errorf("s3 abort multipart %q: %w", k, err)
	}
	return nil
}

func (b *Backend) ListParts(ctx context.Context, k, uploadID string) ([]storage.Part, error) {
	out, err := b.client.ListParts(ctx, &s3.ListPartsInput{
		Bucket:   &b.cfg.Bucket,
		Key:      aws.String(b.key(k)),
		UploadId: &uploadID,
	})
	if err != nil {
		if isNotFound(err) {
			return nil, storage.ErrUploadNotFound
		}
		return nil, fmt.Errorf("s3 list parts %q: %w", k, err)
	}
	parts := make([]storage.Part, len(out.Parts))
	for i, p := range out.Parts {
		parts[i] = storage.Part{
			PartNumber: aws.ToInt32(p.PartNumber),
			ETag:       aws.ToString(p.ETag),
			Size:       aws.ToInt64(p.Size),
		}
	}
	return parts, nil
}

// ─── Presigning ───────────────────────────────────────────────────────────────

func (b *Backend) PresignGet(ctx context.Context, k string, expiry time.Duration) (string, error) {
	req, err := b.psign.PresignGetObject(ctx, &s3.GetObjectInput{
		Bucket: &b.cfg.Bucket,
		Key:    aws.String(b.key(k)),
	}, s3.WithPresignExpires(expiry))
	if err != nil {
		return "", fmt.Errorf("s3 presign get %q: %w", k, err)
	}
	return req.URL, nil
}

func (b *Backend) PresignPut(ctx context.Context, k string, expiry time.Duration) (string, error) {
	req, err := b.psign.PresignPutObject(ctx, &s3.PutObjectInput{
		Bucket: &b.cfg.Bucket,
		Key:    aws.String(b.key(k)),
	}, s3.WithPresignExpires(expiry))
	if err != nil {
		return "", fmt.Errorf("s3 presign put %q: %w", k, err)
	}
	return req.URL, nil
}

func (b *Backend) Close() error { return nil }

// ─── helpers ──────────────────────────────────────────────────────────────────

func isNotFound(err error) bool {
	var nf *types.NoSuchKey
	var nfb *types.NotFound
	return errors.As(err, &nf) || errors.As(err, &nfb)
}
