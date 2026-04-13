// Package storage defines the pluggable blob-storage interface used by the
// registry, cache, log, and mount-cache subsystems.
package storage

import (
	"context"
	"io"
	"time"
)

// BlobInfo holds metadata about a stored blob.
type BlobInfo struct {
	Digest      string
	Size        int64
	ContentType string
	Labels      map[string]string
	CreatedAt   time.Time
}

// PutOptions controls blob ingestion behaviour.
type PutOptions struct {
	ContentType string
	Labels      map[string]string
	// Overwrite an existing blob with the same digest.
	Overwrite bool
	// If set, the backend should verify the digest after write.
	VerifyDigest bool
}

// GetOptions allows byte-range reads.
type GetOptions struct {
	Offset int64
	Length int64 // 0 = read all
}

// ListOptions controls pagination for List operations.
type ListOptions struct {
	Limit     int
	PageToken string
}

// ListResult is returned from List.
type ListResult struct {
	Blobs         []BlobInfo
	NextPageToken string
}

// Part represents a completed segment of a multipart upload.
type Part struct {
	Number int
	ETag   string
	Size   int64
}

// UploadStatus describes an active multipart upload.
type UploadStatus struct {
	UploadID    string
	Repository  string
	Parts       []Part
	BytesUpload int64
	Metadata    map[string]string
	CreatedAt   time.Time
	ExpiresAt   time.Time
}

// Store is the pluggable blob storage interface.
// All implementations must be safe for concurrent use.
type Store interface {
	// Put stores blob data at the given digest.
	// The digest format is "algorithm:hex" (e.g. "sha256:abc…").
	Put(ctx context.Context, digest string, r io.Reader, size int64, opts PutOptions) error

	// Get retrieves a blob. Callers must close the returned ReadCloser.
	Get(ctx context.Context, digest string, opts GetOptions) (io.ReadCloser, int64, error)

	// Stat returns metadata without reading data.
	Stat(ctx context.Context, digest string) (*BlobInfo, error)

	// Exists returns (true, size) when the blob is present.
	Exists(ctx context.Context, digest string) (bool, int64, error)

	// Delete removes the blob. Returns nil if already absent.
	Delete(ctx context.Context, digest string) error

	// List returns blobs whose storage keys begin with prefix.
	List(ctx context.Context, prefix string, opts ListOptions) (*ListResult, error)

	// ── Multipart upload ──────────────────────────────────────────────────

	// InitiateUpload opens a multipart upload session.
	InitiateUpload(ctx context.Context, uploadID string, metadata map[string]string) error

	// UploadPart writes one part and returns its ETag.
	UploadPart(ctx context.Context, uploadID string, partNum int, r io.Reader, size int64) (string, error)

	// CompleteUpload finalises the session and verifies the provided digest.
	CompleteUpload(ctx context.Context, uploadID, digest string, parts []Part) error

	// AbortUpload cancels the session and reclaims staging storage.
	AbortUpload(ctx context.Context, uploadID string) error

	// GetUploadStatus returns info about an in-progress upload.
	GetUploadStatus(ctx context.Context, uploadID string) (*UploadStatus, error)

	// ── Utility ───────────────────────────────────────────────────────────

	// Copy duplicates a blob under a new digest without re-streaming data.
	Copy(ctx context.Context, srcDigest, dstDigest string) error

	// URL returns a presigned URL for direct client access (optional feature).
	// Returns ("", nil) when presigned URLs are not supported.
	URL(ctx context.Context, digest string, expiry time.Duration) (string, error)

	// Close releases backend resources.
	Close() error
}

// WriterTo extends Store with a streaming writer path used by the
// OCI chunked upload handler.
type WriterTo interface {
	Store
	// Writer returns a writer for the active upload session at the given offset.
	// The caller is responsible for closing the writer.
	Writer(ctx context.Context, uploadID string, offset int64) (io.WriteCloser, error)
}
