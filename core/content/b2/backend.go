package b2

import (
	"context"
	"io"
	"time"
)

// ---------------------------------------------------------------------------
// ObjectStorage — Port Interface (Dependency Inversion Principle)
// ---------------------------------------------------------------------------

// ObjectStorage abstracts all object-level operations against a remote store.
//
// Implementations may back this with Minio, AWS SDK, or any S3-compatible
// client. The Store depends exclusively on this interface, never on a
// concrete client — making backends pluggable and testable.
type ObjectStorage interface {
	// StatObject returns metadata for a single object.
	// Returns ErrKindNotFound when the key does not exist.
	StatObject(ctx context.Context, bucket, key string) (ObjectMeta, error)

	// GetObject opens a readable handle to a remote object.
	// The caller must close the returned ObjectReader when done.
	GetObject(ctx context.Context, bucket, key string) (ObjectReader, error)

	// PutObject uploads data from r as a new object.
	// size may be -1 if unknown.
	PutObject(ctx context.Context, bucket, key string, r io.Reader, size int64, contentType string) (UploadResult, error)

	// CopyObjectMetadata updates an object's user metadata via a server-side
	// self-copy. The metadata map fully replaces the existing metadata.
	CopyObjectMetadata(ctx context.Context, bucket, key string, meta map[string]string) (UploadResult, error)

	// RemoveObject permanently deletes an object.
	RemoveObject(ctx context.Context, bucket, key string) error

	// RemoveIncompleteUpload cancels an in-progress multipart upload.
	RemoveIncompleteUpload(ctx context.Context, bucket, key string) error

	// ListObjects returns a channel of objects under prefix.
	// Errors are delivered inline via ObjectEntry.Err.
	ListObjects(ctx context.Context, bucket, prefix string, recursive bool) <-chan ObjectEntry

	// ListIncompleteUploads returns a channel of in-progress uploads.
	// Errors are delivered inline via UploadEntry.Err.
	ListIncompleteUploads(ctx context.Context, bucket, prefix string) <-chan UploadEntry

	// BucketExists checks whether a bucket exists.
	BucketExists(ctx context.Context, bucket string) (bool, error)

	// MakeBucket creates a new bucket in the given region.
	MakeBucket(ctx context.Context, bucket, region string) error
}

// ---------------------------------------------------------------------------
// Domain Value Types
// ---------------------------------------------------------------------------

// ObjectMeta holds metadata about a stored object.
// All fields use stdlib types — no storage-client leakage.
type ObjectMeta struct {
	Key          string
	Size         int64
	LastModified time.Time
	ETag         string
	ContentType  string
	VersionID    string
	StorageClass string
	Metadata     map[string]string // user-defined metadata
	Checksums    Checksums
}

// Checksums holds server-computed integrity checksums.
type Checksums struct {
	SHA256 string
	SHA1   string
	CRC32  string
	CRC32C string
}

// UploadResult is returned after a successful upload or metadata copy.
type UploadResult struct {
	Bucket       string
	Key          string
	Size         int64
	LastModified time.Time
	ETag         string
	VersionID    string
}

// ObjectEntry is a single item yielded by ListObjects.
type ObjectEntry struct {
	Key          string
	Size         int64
	LastModified time.Time
	ETag         string
	Metadata     map[string]string
	Err          error
}

// UploadEntry is a single item yielded by ListIncompleteUploads.
type UploadEntry struct {
	Key       string
	UploadID  string
	Size      int64
	Initiated time.Time
	Err       error
}

// ObjectReader is a readable handle to a remote object.
// It combines random-access reads (ReaderAt) with sequential reads
// and cleanup (ReadCloser), plus a Size for range calculations.
type ObjectReader interface {
	io.ReaderAt
	io.ReadCloser
	Size() int64
}
