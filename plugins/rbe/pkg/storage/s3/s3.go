// Package s3 implements the storage.Store interface backed by an
// S3-compatible object store (AWS S3, MinIO, Cloudflare R2, etc.).
package s3

import (
	"bytes"
	"context"
	"fmt"
	"io"
	"strings"
	"time"

	"github.com/aws/aws-sdk-go-v2/aws"
	awsconfig "github.com/aws/aws-sdk-go-v2/config"
	"github.com/aws/aws-sdk-go-v2/credentials"
	"github.com/aws/aws-sdk-go-v2/feature/s3/manager"
	awss3 "github.com/aws/aws-sdk-go-v2/service/s3"
	"github.com/aws/aws-sdk-go-v2/service/s3/types"
	"github.com/bons/bons-ci/plugins/rbe/pkg/errors"
	"github.com/bons/bons-ci/plugins/rbe/pkg/storage"
)

// Config holds S3 connection options.
type Config struct {
	Endpoint          string
	Region            string
	Bucket            string
	AccessKeyID       string
	SecretAccessKey   string
	ForcePathStyle    bool
	UploadConcurrency int
	// Prefix for all keys (useful when sharing a bucket).
	KeyPrefix string
	// TTL for presigned URL generation.
	PresignExpiry time.Duration
}

// Store implements storage.Store on top of S3.
type Store struct {
	cfg      Config
	client   *awss3.Client
	uploader *manager.Uploader
	presign  *awss3.PresignClient
}

// New creates a new S3 Store.
func New(ctx context.Context, cfg Config) (*Store, error) {
	opts := []func(*awsconfig.LoadOptions) error{
		awsconfig.WithRegion(cfg.Region),
	}
	if cfg.AccessKeyID != "" {
		opts = append(opts, awsconfig.WithCredentialsProvider(
			credentials.NewStaticCredentialsProvider(cfg.AccessKeyID, cfg.SecretAccessKey, ""),
		))
	}
	awsCfg, err := awsconfig.LoadDefaultConfig(ctx, opts...)
	if err != nil {
		return nil, fmt.Errorf("s3: load config: %w", err)
	}

	clientOpts := []func(*awss3.Options){}
	if cfg.Endpoint != "" {
		clientOpts = append(clientOpts, func(o *awss3.Options) {
			o.BaseEndpoint = aws.String(cfg.Endpoint)
			o.UsePathStyle = cfg.ForcePathStyle
		})
	}

	client := awss3.NewFromConfig(awsCfg, clientOpts...)
	conc := cfg.UploadConcurrency
	if conc == 0 {
		conc = 8
	}
	return &Store{
		cfg:      cfg,
		client:   client,
		uploader: manager.NewUploader(client, func(u *manager.Uploader) { u.Concurrency = conc }),
		presign:  awss3.NewPresignClient(client),
	}, nil
}

func (s *Store) key(digest string) string {
	// Convert "sha256:abc123" → "blobs/sha256/abc123"
	parts := strings.SplitN(digest, ":", 2)
	if len(parts) == 2 {
		return s.cfg.KeyPrefix + "blobs/" + parts[0] + "/" + parts[1]
	}
	return s.cfg.KeyPrefix + "blobs/" + digest
}

func (s *Store) uploadKey(uploadID string) string {
	return s.cfg.KeyPrefix + "uploads/" + uploadID + "/staging"
}

// Put stores a blob. If the blob already exists and Overwrite=false, returns nil (idempotent).
func (s *Store) Put(ctx context.Context, digest string, r io.Reader, size int64, opts storage.PutOptions) error {
	if !opts.Overwrite {
		exists, _, _ := s.Exists(ctx, digest)
		if exists {
			return nil
		}
	}
	ct := opts.ContentType
	if ct == "" {
		ct = "application/octet-stream"
	}
	_, err := s.uploader.Upload(ctx, &awss3.PutObjectInput{
		Bucket:      aws.String(s.cfg.Bucket),
		Key:         aws.String(s.key(digest)),
		Body:        r,
		ContentType: aws.String(ct),
	})
	if err != nil {
		return errors.Wrapf(err, "s3: put %s", digest)
	}
	return nil
}

// Get retrieves a blob, optionally with byte-range.
func (s *Store) Get(ctx context.Context, digest string, opts storage.GetOptions) (io.ReadCloser, int64, error) {
	in := &awss3.GetObjectInput{
		Bucket: aws.String(s.cfg.Bucket),
		Key:    aws.String(s.key(digest)),
	}
	if opts.Offset > 0 || opts.Length > 0 {
		var r string
		if opts.Length > 0 {
			r = fmt.Sprintf("bytes=%d-%d", opts.Offset, opts.Offset+opts.Length-1)
		} else {
			r = fmt.Sprintf("bytes=%d-", opts.Offset)
		}
		in.Range = aws.String(r)
	}
	out, err := s.client.GetObject(ctx, in)
	if err != nil {
		if isNotFound(err) {
			return nil, 0, errors.NewBlobUnknown(digest)
		}
		return nil, 0, errors.Wrapf(err, "s3: get %s", digest)
	}
	return out.Body, aws.ToInt64(out.ContentLength), nil
}

// Stat returns metadata for a blob without reading it.
func (s *Store) Stat(ctx context.Context, digest string) (*storage.BlobInfo, error) {
	out, err := s.client.HeadObject(ctx, &awss3.HeadObjectInput{
		Bucket: aws.String(s.cfg.Bucket),
		Key:    aws.String(s.key(digest)),
	})
	if err != nil {
		if isNotFound(err) {
			return nil, errors.NewBlobUnknown(digest)
		}
		return nil, errors.Wrapf(err, "s3: stat %s", digest)
	}
	return &storage.BlobInfo{
		Digest:      digest,
		Size:        aws.ToInt64(out.ContentLength),
		ContentType: aws.ToString(out.ContentType),
		CreatedAt:   aws.ToTime(out.LastModified),
	}, nil
}

// Exists checks whether a blob is present.
func (s *Store) Exists(ctx context.Context, digest string) (bool, int64, error) {
	info, err := s.Stat(ctx, digest)
	if err != nil {
		if errors.Is(err, errors.ErrNotFound) {
			return false, 0, nil
		}
		return false, 0, err
	}
	return true, info.Size, nil
}

// Delete removes a blob.
func (s *Store) Delete(ctx context.Context, digest string) error {
	_, err := s.client.DeleteObject(ctx, &awss3.DeleteObjectInput{
		Bucket: aws.String(s.cfg.Bucket),
		Key:    aws.String(s.key(digest)),
	})
	return err
}

// List returns blobs whose storage keys match prefix.
func (s *Store) List(ctx context.Context, prefix string, opts storage.ListOptions) (*storage.ListResult, error) {
	p := s.cfg.KeyPrefix + "blobs/"
	if prefix != "" {
		p += prefix
	}
	in := &awss3.ListObjectsV2Input{
		Bucket: aws.String(s.cfg.Bucket),
		Prefix: aws.String(p),
	}
	if opts.PageToken != "" {
		in.ContinuationToken = aws.String(opts.PageToken)
	}
	if opts.Limit > 0 {
		in.MaxKeys = aws.Int32(int32(opts.Limit))
	}
	out, err := s.client.ListObjectsV2(ctx, in)
	if err != nil {
		return nil, errors.Wrapf(err, "s3: list")
	}
	res := &storage.ListResult{}
	for _, obj := range out.Contents {
		res.Blobs = append(res.Blobs, storage.BlobInfo{
			Digest:    keyToDigest(aws.ToString(obj.Key), s.cfg.KeyPrefix),
			Size:      aws.ToInt64(obj.Size),
			CreatedAt: aws.ToTime(obj.LastModified),
		})
	}
	if out.NextContinuationToken != nil {
		res.NextPageToken = *out.NextContinuationToken
	}
	return res, nil
}

// ── Multipart upload ──────────────────────────────────────────────────────────

// InitiateUpload creates an empty staging object to mark the session.
func (s *Store) InitiateUpload(ctx context.Context, uploadID string, metadata map[string]string) error {
	_, err := s.client.PutObject(ctx, &awss3.PutObjectInput{
		Bucket:      aws.String(s.cfg.Bucket),
		Key:         aws.String(s.uploadKey(uploadID) + "/.meta"),
		Body:        bytes.NewReader([]byte(uploadID)),
		ContentType: aws.String("application/octet-stream"),
	})
	return err
}

// UploadPart writes one part of a chunked upload.
// For S3-backed chunked uploads we buffer the part and write to a numbered key.
func (s *Store) UploadPart(ctx context.Context, uploadID string, partNum int, r io.Reader, size int64) (string, error) {
	key := fmt.Sprintf("%s/part-%05d", s.uploadKey(uploadID), partNum)
	data, err := io.ReadAll(r)
	if err != nil {
		return "", errors.Wrapf(err, "s3: read part %d", partNum)
	}
	out, err := s.client.PutObject(ctx, &awss3.PutObjectInput{
		Bucket: aws.String(s.cfg.Bucket),
		Key:    aws.String(key),
		Body:   bytes.NewReader(data),
	})
	if err != nil {
		return "", errors.Wrapf(err, "s3: upload part %d", partNum)
	}
	return aws.ToString(out.ETag), nil
}

// CompleteUpload concatenates all parts and writes the final blob.
func (s *Store) CompleteUpload(ctx context.Context, uploadID, digest string, parts []storage.Part) error {
	// Stream parts in order into a multipart upload targeting the final blob key.
	resp, err := s.client.CreateMultipartUpload(ctx, &awss3.CreateMultipartUploadInput{
		Bucket: aws.String(s.cfg.Bucket),
		Key:    aws.String(s.key(digest)),
	})
	if err != nil {
		return errors.Wrapf(err, "s3: create mpu for %s", digest)
	}
	mpuID := resp.UploadId

	var completedParts []types.CompletedPart
	for _, p := range parts {
		partKey := fmt.Sprintf("%s/part-%05d", s.uploadKey(uploadID), p.Number)
		r, _, err := s.Get(ctx, partKey, storage.GetOptions{}) // reuse Get for part
		if err != nil {
			// Try direct object read
			obj, err2 := s.client.GetObject(ctx, &awss3.GetObjectInput{
				Bucket: aws.String(s.cfg.Bucket),
				Key:    aws.String(partKey),
			})
			if err2 != nil {
				_, _ = s.client.AbortMultipartUpload(ctx, &awss3.AbortMultipartUploadInput{
					Bucket: aws.String(s.cfg.Bucket), Key: aws.String(s.key(digest)), UploadId: mpuID,
				})
				return errors.Wrapf(err, "s3: read part %d", p.Number)
			}
			r = obj.Body
		}
		cpOut, err := s.client.UploadPart(ctx, &awss3.UploadPartInput{
			Bucket:     aws.String(s.cfg.Bucket),
			Key:        aws.String(s.key(digest)),
			UploadId:   mpuID,
			PartNumber: aws.Int32(int32(p.Number)),
			Body:       r,
		})
		_ = r.Close()
		if err != nil {
			_, _ = s.client.AbortMultipartUpload(ctx, &awss3.AbortMultipartUploadInput{
				Bucket: aws.String(s.cfg.Bucket), Key: aws.String(s.key(digest)), UploadId: mpuID,
			})
			return errors.Wrapf(err, "s3: upload reassembled part %d", p.Number)
		}
		completedParts = append(completedParts, types.CompletedPart{
			ETag:       cpOut.ETag,
			PartNumber: aws.Int32(int32(p.Number)),
		})
	}

	_, err = s.client.CompleteMultipartUpload(ctx, &awss3.CompleteMultipartUploadInput{
		Bucket:          aws.String(s.cfg.Bucket),
		Key:             aws.String(s.key(digest)),
		UploadId:        mpuID,
		MultipartUpload: &types.CompletedMultipartUpload{Parts: completedParts},
	})
	if err != nil {
		return errors.Wrapf(err, "s3: complete mpu")
	}
	// Clean up staging objects
	go s.cleanupUpload(context.Background(), uploadID) //nolint:errcheck
	return nil
}

func (s *Store) cleanupUpload(ctx context.Context, uploadID string) error {
	prefix := s.uploadKey(uploadID)
	out, err := s.client.ListObjectsV2(ctx, &awss3.ListObjectsV2Input{
		Bucket: aws.String(s.cfg.Bucket),
		Prefix: aws.String(prefix),
	})
	if err != nil {
		return err
	}
	var objs []types.ObjectIdentifier
	for _, o := range out.Contents {
		objs = append(objs, types.ObjectIdentifier{Key: o.Key})
	}
	if len(objs) == 0 {
		return nil
	}
	_, err = s.client.DeleteObjects(ctx, &awss3.DeleteObjectsInput{
		Bucket: aws.String(s.cfg.Bucket),
		Delete: &types.Delete{Objects: objs},
	})
	return err
}

// AbortUpload cancels a staged upload.
func (s *Store) AbortUpload(ctx context.Context, uploadID string) error {
	return s.cleanupUpload(ctx, uploadID)
}

// GetUploadStatus returns current status of an upload.
func (s *Store) GetUploadStatus(ctx context.Context, uploadID string) (*storage.UploadStatus, error) {
	prefix := s.uploadKey(uploadID)
	out, err := s.client.ListObjectsV2(ctx, &awss3.ListObjectsV2Input{
		Bucket: aws.String(s.cfg.Bucket),
		Prefix: aws.String(prefix + "/part-"),
	})
	if err != nil {
		return nil, err
	}
	status := &storage.UploadStatus{UploadID: uploadID}
	for i, o := range out.Contents {
		status.Parts = append(status.Parts, storage.Part{Number: i + 1, Size: aws.ToInt64(o.Size)})
		status.BytesUpload += aws.ToInt64(o.Size)
	}
	return status, nil
}

// Copy duplicates an object without re-downloading.
func (s *Store) Copy(ctx context.Context, srcDigest, dstDigest string) error {
	_, err := s.client.CopyObject(ctx, &awss3.CopyObjectInput{
		Bucket:     aws.String(s.cfg.Bucket),
		Key:        aws.String(s.key(dstDigest)),
		CopySource: aws.String(s.cfg.Bucket + "/" + s.key(srcDigest)),
	})
	return err
}

// URL generates a presigned GET URL.
func (s *Store) URL(ctx context.Context, digest string, expiry time.Duration) (string, error) {
	if s.cfg.PresignExpiry == 0 && expiry == 0 {
		return "", nil
	}
	if expiry == 0 {
		expiry = s.cfg.PresignExpiry
	}
	req, err := s.presign.PresignGetObject(ctx, &awss3.GetObjectInput{
		Bucket: aws.String(s.cfg.Bucket),
		Key:    aws.String(s.key(digest)),
	}, awss3.WithPresignExpires(expiry))
	if err != nil {
		return "", err
	}
	return req.URL, nil
}

func (s *Store) Close() error { return nil }

// ── helpers ───────────────────────────────────────────────────────────────────

func keyToDigest(key, prefix string) string {
	// "prefix/blobs/sha256/abc…" → "sha256:abc…"
	trimmed := strings.TrimPrefix(key, prefix+"blobs/")
	parts := strings.SplitN(trimmed, "/", 2)
	if len(parts) == 2 {
		return parts[0] + ":" + parts[1]
	}
	return trimmed
}

func isNotFound(err error) bool {
	if err == nil {
		return false
	}
	s := err.Error()
	return strings.Contains(s, "NoSuchKey") || strings.Contains(s, "NotFound") || strings.Contains(s, "404")
}
