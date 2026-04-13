// Package storage defines the pluggable blob-storage abstraction used by
// every subsystem in the RBE server. All data — OCI blobs, log chunks,
// mount-cache snapshots, cache exports — flows through this interface.
package storage

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"io"
	"time"
)

// ErrNotFound is returned when a requested object does not exist.
var ErrNotFound = errors.New("storage: object not found")

// ErrAlreadyExists is returned when attempting to create an object that exists
// and the backend does not support overwrites in that mode.
var ErrAlreadyExists = errors.New("storage: object already exists")

// ErrUploadNotFound is returned when an upload session is unknown / expired.
var ErrUploadNotFound = errors.New("storage: upload session not found")

// ─── Core types ─────────────────────────────────────────────────────────────

// ObjectInfo describes a stored object without downloading its content.
type ObjectInfo struct {
	Key          string
	Size         int64
	ETag         string
	ContentType  string
	LastModified time.Time
	Metadata     map[string]string
}

// Part describes one chunk in a multipart upload.
type Part struct {
	PartNumber int32
	ETag       string
	Size       int64
}

// PutOptions controls how a single-shot Put is stored.
type PutOptions struct {
	ContentType string
	Metadata    map[string]string
	// If non-empty the backend MUST verify the object's sha256 matches.
	ExpectedDigest string
	// Overwrite existing? Defaults to true.
	Overwrite bool
}

// ListOptions controls object listing.
type ListOptions struct {
	Recursive bool
	// If non-empty only return objects with keys that sort after Marker.
	Marker string
	Limit  int
}

// ─── Backend interface ───────────────────────────────────────────────────────

// Backend is the single abstraction every storage consumer depends on.
// Implementations must be safe for concurrent use.
type Backend interface {
	// ── Single-object operations ──────────────────────────────────────────

	// Put stores the entirety of r under key.
	// size may be -1 if unknown; backends should tolerate this.
	Put(ctx context.Context, key string, r io.Reader, size int64, opts PutOptions) error

	// Get opens a reader for key. Callers must close the returned reader.
	// Returns ErrNotFound if the key does not exist.
	Get(ctx context.Context, key string) (io.ReadCloser, int64, error)

	// GetRange opens a reader for [offset, offset+length) bytes of key.
	// length == 0 means "to end of object".
	GetRange(ctx context.Context, key string, offset, length int64) (io.ReadCloser, error)

	// Stat returns object metadata without downloading content.
	Stat(ctx context.Context, key string) (*ObjectInfo, error)

	// Exists is a cheap existence check; implementations may use HEAD/Stat.
	Exists(ctx context.Context, key string) (bool, error)

	// Delete removes an object. Returns nil if the object did not exist.
	Delete(ctx context.Context, key string) error

	// ── Listing ───────────────────────────────────────────────────────────

	// List emits ObjectInfo records for all objects whose key has the given
	// prefix. The channel is closed (and the error channel receives nil or
	// one fatal error) when listing is complete. Callers must drain both.
	List(ctx context.Context, prefix string, opts ListOptions) (<-chan ObjectInfo, <-chan error)

	// ── Server-side copy ──────────────────────────────────────────────────

	// Copy duplicates src to dst atomically in the backend.
	Copy(ctx context.Context, srcKey, dstKey string) error

	// ── Multipart / resumable upload ──────────────────────────────────────

	// CreateMultipartUpload initiates a multipart session and returns an
	// opaque uploadID.
	CreateMultipartUpload(ctx context.Context, key string, opts PutOptions) (uploadID string, err error)

	// UploadPart stores one chunk and returns a Part descriptor that must
	// be collected and passed to CompleteMultipartUpload.
	UploadPart(ctx context.Context, key, uploadID string, partNum int32, r io.Reader, size int64) (Part, error)

	// CompleteMultipartUpload assembles all parts into the final object.
	CompleteMultipartUpload(ctx context.Context, key, uploadID string, parts []Part) (*ObjectInfo, error)

	// AbortMultipartUpload discards a partial upload and frees its storage.
	AbortMultipartUpload(ctx context.Context, key, uploadID string) error

	// ListParts returns the parts already uploaded for an upload session.
	ListParts(ctx context.Context, key, uploadID string) ([]Part, error)

	// ── Presigned URLs (optional) ─────────────────────────────────────────

	// PresignGet returns a time-limited URL for a GET operation.
	// Implementations that don't support presigning should return
	// ErrNotSupported.
	PresignGet(ctx context.Context, key string, expiry time.Duration) (string, error)

	// PresignPut returns a time-limited URL for a PUT operation.
	PresignPut(ctx context.Context, key string, expiry time.Duration) (string, error)

	// ── Lifecycle ─────────────────────────────────────────────────────────

	// Close releases all resources held by the backend.
	Close() error
}

// ErrNotSupported is returned by optional methods that the backend does not
// implement (e.g. presigned URLs for the local-FS backend).
var ErrNotSupported = errors.New("storage: operation not supported by this backend")

// ─── Key helpers ─────────────────────────────────────────────────────────────

const (
	// PrefixBlobs is the S3 key prefix for OCI content-addressable blobs.
	// Layout: blobs/<algo>/<first2>/<hex>
	PrefixBlobs = "blobs"

	// PrefixLogs is the S3 key prefix for per-vertex log chunks.
	// Layout: logs/<build>/<vertex>/<fd>/<seq16>
	PrefixLogs = "logs"

	// PrefixMountCache is the prefix for cache-mount snapshot blobs.
	// Layout: mountcache/<scope>/<id>/<platform_hash>/blob
	PrefixMountCache = "mountcache"

	// PrefixCacheExport is the prefix for vertex-cache OCI export blobs.
	// Layout: cacheexport/<cache_key_hash>/<format>
	PrefixCacheExport = "cacheexport"

	// PrefixManifests stores raw manifest bytes as a fast path.
	// Layout: manifests/<repo>/<digest>
	PrefixManifests = "manifests"
)

// BlobKey returns the canonical S3 key for an OCI blob.
// algo is "sha256", "sha512", etc.
func BlobKey(algo, hex string) string {
	if len(hex) < 2 {
		return PrefixBlobs + "/" + algo + "/" + hex
	}
	return PrefixBlobs + "/" + algo + "/" + hex[:2] + "/" + hex
}

// LogChunkKey returns the S3 key for a single log chunk.
// seq is zero-padded to 16 digits for correct lexicographic ordering.
func LogChunkKey(buildID, vertexID string, fd int32, seq int64) string {
	return fmt.Sprintf("%s/%s/%s/%d/%016d", PrefixLogs, buildID, vertexID, fd, seq)
}

// MountCacheBlobKey returns the S3 key for a mount-cache snapshot.
func MountCacheBlobKey(scope, id, platformHash string) string {
	return PrefixMountCache + "/" + scope + "/" + id + "/" + platformHash + "/blob"
}

// ManifestKey returns the S3 key for a manifest blob.
func ManifestKey(repo, digest string) string {
	return PrefixManifests + "/" + repo + "/" + digest
}

// ─── Noop / helpers ──────────────────────────────────────────────────────────

// ReadAll is a convenience wrapper that reads the full object body.
func ReadAll(ctx context.Context, b Backend, key string) ([]byte, error) {
	rc, _, err := b.Get(ctx, key)
	if err != nil {
		return nil, err
	}
	defer rc.Close()
	return io.ReadAll(rc)
}

// PutBytes is a convenience wrapper for small in-memory blobs.
func PutBytes(ctx context.Context, b Backend, key string, data []byte, opts PutOptions) error {
	return b.Put(ctx, key, bytes.NewReader(data), int64(len(data)), opts)
}

// ─── Registry (MultiBackend) ─────────────────────────────────────────────────

// MultiBackend routes reads and writes across a primary and optional
// secondary backends (e.g. local fast-cache + S3).
type MultiBackend struct {
	primary   Backend
	secondary []Backend // read-fallback chain
}

// NewMultiBackend creates a MultiBackend. Writes always go to primary.
// Reads try primary first, then secondaries in order.
func NewMultiBackend(primary Backend, fallbacks ...Backend) *MultiBackend {
	return &MultiBackend{primary: primary, secondary: fallbacks}
}

func (m *MultiBackend) Put(ctx context.Context, key string, r io.Reader, size int64, opts PutOptions) error {
	return m.primary.Put(ctx, key, r, size, opts)
}

func (m *MultiBackend) Get(ctx context.Context, key string) (io.ReadCloser, int64, error) {
	rc, sz, err := m.primary.Get(ctx, key)
	if err == nil {
		return rc, sz, nil
	}
	if !errors.Is(err, ErrNotFound) {
		return nil, 0, err
	}
	for _, b := range m.secondary {
		rc, sz, err = b.Get(ctx, key)
		if err == nil {
			return rc, sz, nil
		}
		if !errors.Is(err, ErrNotFound) {
			return nil, 0, err
		}
	}
	return nil, 0, ErrNotFound
}

func (m *MultiBackend) GetRange(ctx context.Context, key string, off, length int64) (io.ReadCloser, error) {
	rc, err := m.primary.GetRange(ctx, key, off, length)
	if err == nil {
		return rc, nil
	}
	if !errors.Is(err, ErrNotFound) {
		return nil, err
	}
	for _, b := range m.secondary {
		rc, err = b.GetRange(ctx, key, off, length)
		if err == nil {
			return rc, nil
		}
	}
	return nil, ErrNotFound
}

func (m *MultiBackend) Stat(ctx context.Context, key string) (*ObjectInfo, error) {
	info, err := m.primary.Stat(ctx, key)
	if err == nil {
		return info, nil
	}
	for _, b := range m.secondary {
		info, err = b.Stat(ctx, key)
		if err == nil {
			return info, nil
		}
	}
	return nil, ErrNotFound
}

func (m *MultiBackend) Exists(ctx context.Context, key string) (bool, error) {
	ok, err := m.primary.Exists(ctx, key)
	if err == nil && ok {
		return true, nil
	}
	for _, b := range m.secondary {
		ok, err = b.Exists(ctx, key)
		if err == nil && ok {
			return true, nil
		}
	}
	return false, nil
}

func (m *MultiBackend) Delete(ctx context.Context, key string) error {
	return m.primary.Delete(ctx, key)
}

func (m *MultiBackend) List(ctx context.Context, prefix string, opts ListOptions) (<-chan ObjectInfo, <-chan error) {
	return m.primary.List(ctx, prefix, opts)
}

func (m *MultiBackend) Copy(ctx context.Context, src, dst string) error {
	return m.primary.Copy(ctx, src, dst)
}

func (m *MultiBackend) CreateMultipartUpload(ctx context.Context, key string, opts PutOptions) (string, error) {
	return m.primary.CreateMultipartUpload(ctx, key, opts)
}

func (m *MultiBackend) UploadPart(ctx context.Context, key, uploadID string, partNum int32, r io.Reader, size int64) (Part, error) {
	return m.primary.UploadPart(ctx, key, uploadID, partNum, r, size)
}

func (m *MultiBackend) CompleteMultipartUpload(ctx context.Context, key, uploadID string, parts []Part) (*ObjectInfo, error) {
	return m.primary.CompleteMultipartUpload(ctx, key, uploadID, parts)
}

func (m *MultiBackend) AbortMultipartUpload(ctx context.Context, key, uploadID string) error {
	return m.primary.AbortMultipartUpload(ctx, key, uploadID)
}

func (m *MultiBackend) ListParts(ctx context.Context, key, uploadID string) ([]Part, error) {
	return m.primary.ListParts(ctx, key, uploadID)
}

func (m *MultiBackend) PresignGet(ctx context.Context, key string, expiry time.Duration) (string, error) {
	return m.primary.PresignGet(ctx, key, expiry)
}

func (m *MultiBackend) PresignPut(ctx context.Context, key string, expiry time.Duration) (string, error) {
	return m.primary.PresignPut(ctx, key, expiry)
}

func (m *MultiBackend) Close() error {
	var errs []error
	if err := m.primary.Close(); err != nil {
		errs = append(errs, err)
	}
	for _, b := range m.secondary {
		if err := b.Close(); err != nil {
			errs = append(errs, err)
		}
	}
	return errors.Join(errs...)
}
