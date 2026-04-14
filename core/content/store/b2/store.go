package b2

import (
	"context"
	"fmt"
	"maps"
	"time"

	"github.com/containerd/containerd/v2/core/content"
	"github.com/containerd/containerd/v2/pkg/filters"
	"github.com/containerd/errdefs"
	"github.com/minio/minio-go/v7"
	"github.com/minio/minio-go/v7/pkg/credentials"
	digest "github.com/opencontainers/go-digest"
	v1 "github.com/opencontainers/image-spec/specs-go/v1"
)

// Store is the high-level orchestrator implementing content.Store.
// It delegates all object operations to an ObjectStorage backend and
// emits lifecycle events to registered hooks.
type Store struct {
	backend ObjectStorage
	cfg     Config
	paths   ObjectPaths
	hooks   []Hook
}

// compile-time check
var _ content.Store = (*Store)(nil)

// New creates a Store from an existing ObjectStorage backend.
// This is the primary constructor following the Dependency Inversion Principle:
// callers supply the backend, enabling pluggability and testability.
func New(backend ObjectStorage, cfg Config, opts ...StoreOption) (*Store, error) {
	paths, err := NewObjectPaths(cfg)
	if err != nil {
		return nil, fmt.Errorf("b2: build object paths: %w", err)
	}

	var so storeOptions
	for _, opt := range opts {
		opt(&so)
	}

	store := &Store{
		backend: backend,
		cfg:     cfg,
		paths:   paths,
		hooks:   so.hooks,
	}

	// If a tracer is provided, wrap the store in a TracedStore.
	if so.tracer != nil {
		return store, nil // TracedStore wrapping is done by the caller via AsTraced()
	}

	return store, nil
}

// NewWithMinio is a convenience constructor that creates a minio-backed Store.
// It handles client creation, bucket provisioning, and option application.
func NewWithMinio(cfg Config, creds *credentials.Credentials, opts ...StoreOption) (content.Store, error) {
	endpoint := cfg.EndpointURL
	if endpoint == "" {
		endpoint = fmt.Sprintf("s3.%s.backblazeb2.com", cfg.Region)
	}

	client, err := minio.New(endpoint, &minio.Options{
		Creds:  creds,
		Secure: true,
		Region: cfg.Region,
	})
	if err != nil {
		return nil, fmt.Errorf("b2: create minio client: %w", err)
	}

	backend := NewMinioBackend(client)

	if err := ensureBucket(context.Background(), backend, cfg.Bucket, cfg.Region); err != nil {
		return nil, err
	}

	var so storeOptions
	for _, opt := range opts {
		opt(&so)
	}

	store := &Store{
		backend: backend,
		cfg:     cfg,
		hooks:   so.hooks,
	}

	paths, err := NewObjectPaths(cfg)
	if err != nil {
		return nil, fmt.Errorf("b2: build object paths: %w", err)
	}
	store.paths = paths

	// Wrap with OTel tracing if a TracerProvider was supplied.
	if so.tracer != nil {
		return NewTracedStore(store, so.tracer), nil
	}

	return store, nil
}

// ---------------------------------------------------------------------------
// content.Store — Abort
// ---------------------------------------------------------------------------

func (s *Store) Abort(ctx context.Context, ref string) error {
	key := s.paths.Folder(s.cfg.BlobsPrefix, ref)
	if err := s.backend.RemoveIncompleteUpload(ctx, s.cfg.Bucket, key); err != nil {
		return storeErr(classifyMinioErr(err), "Abort", ref, err)
	}
	return nil
}

// ---------------------------------------------------------------------------
// content.Store — Delete
// ---------------------------------------------------------------------------

func (s *Store) Delete(ctx context.Context, dgst digest.Digest) error {
	key := s.paths.BlobPath(dgst)
	if err := s.backend.RemoveObject(ctx, s.cfg.Bucket, key); err != nil {
		return storeErr(classifyMinioErr(err), "Delete", dgst.String(), err)
	}

	s.emit(ctx, Event{
		Kind:      EventBlobDeleted,
		Digest:    dgst,
		Timestamp: time.Now(),
	})
	return nil
}

// ---------------------------------------------------------------------------
// content.Store — Info
// ---------------------------------------------------------------------------

func (s *Store) Info(ctx context.Context, dgst digest.Digest) (content.Info, error) {
	key := s.paths.BlobPath(dgst)
	meta, err := s.backend.StatObject(ctx, s.cfg.Bucket, key)
	if err != nil {
		if isNotFound(err) {
			return content.Info{}, fmt.Errorf("b2: blob %s: %w", dgst, errdefs.ErrNotFound)
		}
		return content.Info{}, storeErr(classifyMinioErr(err), "Info", dgst.String(), err)
	}

	info := metaToContentInfo(dgst, meta)

	s.emit(ctx, Event{
		Kind:      EventBlobAccessed,
		Digest:    info.Digest,
		Size:      info.Size,
		Labels:    info.Labels,
		Timestamp: time.Now(),
	})

	return info, nil
}

// ---------------------------------------------------------------------------
// content.Store — ListStatuses
// ---------------------------------------------------------------------------

func (s *Store) ListStatuses(ctx context.Context, fs ...string) ([]content.Status, error) {
	prefix := s.paths.Folder()

	filter, err := filters.ParseAll(fs...)
	if err != nil {
		return nil, storeErr(ErrKindInvalidArgument, "ListStatuses", "parse filters", err)
	}

	var statuses []content.Status
	for entry := range s.backend.ListIncompleteUploads(ctx, s.cfg.Bucket, prefix) {
		if entry.Err != nil {
			return statuses, storeErr(ErrKindUnknown, "ListStatuses", "list", entry.Err)
		}

		st := content.Status{
			Ref:       entry.Key,
			Total:     entry.Size,
			StartedAt: entry.Initiated,
			UpdatedAt: time.Now(),
		}

		if len(fs) > 0 && !filter.Match(statusAdaptor(st)) {
			continue
		}
		statuses = append(statuses, st)
	}

	return statuses, nil
}

// ---------------------------------------------------------------------------
// content.Store — ReaderAt
// ---------------------------------------------------------------------------

func (s *Store) ReaderAt(ctx context.Context, desc v1.Descriptor) (content.ReaderAt, error) {
	key := s.paths.BlobPath(desc.Digest)
	reader, err := s.backend.GetObject(ctx, s.cfg.Bucket, key)
	if err != nil {
		if isNotFound(err) {
			return nil, fmt.Errorf("b2: blob %s: %w", desc.Digest, errdefs.ErrNotFound)
		}
		return nil, storeErr(classifyMinioErr(err), "ReaderAt", desc.Digest.String(), err)
	}

	s.emit(ctx, Event{
		Kind:      EventBlobAccessed,
		Digest:    desc.Digest,
		Size:      desc.Size,
		Timestamp: time.Now(),
	})

	return &contentReaderAt{ObjectReader: reader}, nil
}

// ---------------------------------------------------------------------------
// content.Store — Status
// ---------------------------------------------------------------------------

func (s *Store) Status(ctx context.Context, ref string) (content.Status, error) {
	prefix := s.paths.Folder(ref)
	for entry := range s.backend.ListIncompleteUploads(ctx, s.cfg.Bucket, prefix) {
		if entry.Err != nil {
			return content.Status{}, storeErr(ErrKindUnknown, "Status", ref, entry.Err)
		}
		return content.Status{
			Ref:       entry.Key,
			Total:     entry.Size,
			StartedAt: entry.Initiated,
			UpdatedAt: time.Now(),
		}, nil
	}

	return content.Status{}, fmt.Errorf("b2: no incomplete upload for ref %q: %w", ref, errdefs.ErrNotFound)
}

// ---------------------------------------------------------------------------
// content.Store — Update
// ---------------------------------------------------------------------------

func (s *Store) Update(ctx context.Context, info content.Info, fieldpaths ...string) (content.Info, error) {
	key := s.paths.BlobPath(info.Digest)
	meta, err := s.backend.StatObject(ctx, s.cfg.Bucket, key)
	if err != nil {
		if isNotFound(err) {
			return content.Info{}, fmt.Errorf("b2: blob %s: %w", info.Digest, errdefs.ErrNotFound)
		}
		return content.Info{}, storeErr(classifyMinioErr(err), "Update", info.Digest.String(), err)
	}

	// Parse fieldpath filters.
	if len(fieldpaths) > 0 {
		filter, err := filters.ParseAll(fieldpaths...)
		if err != nil {
			return content.Info{}, storeErr(ErrKindInvalidArgument, "Update", "parse fieldpaths", err)
		}
		filter.Match(objectEntryAdaptor(meta.Key, meta.Metadata))
	}

	// Merge incoming labels into existing metadata.
	metadata := meta.Metadata
	if metadata == nil {
		metadata = make(map[string]string)
	}
	maps.Copy(metadata, info.Labels)

	result, err := s.backend.CopyObjectMetadata(ctx, s.cfg.Bucket, key, metadata)
	if err != nil {
		return content.Info{}, storeErr(classifyMinioErr(err), "Update", info.Digest.String(), err)
	}

	return content.Info{
		Digest:    info.Digest,
		Size:      result.Size,
		CreatedAt: meta.LastModified,
		UpdatedAt: result.LastModified,
		Labels:    metadata,
	}, nil
}

// ---------------------------------------------------------------------------
// content.Store — Walk
// ---------------------------------------------------------------------------

func (s *Store) Walk(ctx context.Context, fn content.WalkFunc, fs ...string) error {
	filter, err := filters.ParseAll(fs...)
	if err != nil {
		return storeErr(ErrKindInvalidArgument, "Walk", "parse filters", err)
	}

	for entry := range s.backend.ListObjects(ctx, s.cfg.Bucket, s.paths.Folder(), true) {
		if entry.Err != nil {
			return storeErr(ErrKindUnknown, "Walk", "list", entry.Err)
		}

		if len(fs) > 0 && !filter.Match(objectEntryAdaptor(entry.Key, entry.Metadata)) {
			continue
		}

		dgst, err := digestFromPath(entry.Key, s.paths)
		if err != nil {
			continue // skip non-blob entries
		}

		labels := make(map[string]string)
		maps.Copy(labels, entry.Metadata)

		info := content.Info{
			Digest:    dgst,
			Size:      entry.Size,
			CreatedAt: entry.LastModified,
			UpdatedAt: entry.LastModified,
			Labels:    labels,
		}

		s.emit(ctx, Event{
			Kind:      EventBlobWalked,
			Digest:    dgst,
			Size:      entry.Size,
			Labels:    labels,
			Timestamp: time.Now(),
		})

		if err := fn(info); err != nil {
			return err
		}
	}

	return nil
}

// ---------------------------------------------------------------------------
// content.Store — Writer
// ---------------------------------------------------------------------------

func (s *Store) Writer(ctx context.Context, opts ...content.WriterOpt) (content.Writer, error) {
	var wopt content.WriterOpts
	for _, op := range opts {
		if err := op(&wopt); err != nil {
			return nil, storeErr(ErrKindInvalidArgument, "Writer", "apply option", err)
		}
	}

	objectPath, err := s.resolveObjectPath(wopt)
	if err != nil {
		return nil, err
	}

	size := int64(-1)
	if wopt.Desc.Size > 0 {
		size = wopt.Desc.Size
	}

	return newContentWriter(ctx, s.backend, s.cfg.Bucket, objectPath, wopt.Ref, size, s.hooks)
}

// resolveObjectPath builds the tenant-scoped S3 key from writer options.
func (s *Store) resolveObjectPath(wopt content.WriterOpts) (string, error) {
	if wopt.Desc.Digest != "" {
		return s.paths.BlobPath(wopt.Desc.Digest), nil
	}
	if wopt.Ref != "" {
		dgst, err := digest.Parse(wopt.Ref)
		if err != nil {
			// Ref is not a digest — use it as a literal key segment.
			return s.paths.Folder(s.cfg.BlobsPrefix, wopt.Ref), nil
		}
		return s.paths.BlobPath(dgst), nil
	}
	return "", fmt.Errorf("b2: writer requires a descriptor digest or ref: %w", errdefs.ErrInvalidArgument)
}

// ---------------------------------------------------------------------------
// Helpers
// ---------------------------------------------------------------------------

// emit notifies all registered hooks of an event.
func (s *Store) emit(ctx context.Context, evt Event) {
	for _, h := range s.hooks {
		h.OnEvent(ctx, evt)
	}
}

// metaToContentInfo converts ObjectMeta to content.Info.
func metaToContentInfo(dgst digest.Digest, meta ObjectMeta) content.Info {
	info := content.Info{
		Digest:    dgst,
		Size:      meta.Size,
		CreatedAt: meta.LastModified,
		UpdatedAt: meta.LastModified,
		Labels:    make(map[string]string),
	}

	maps.Copy(info.Labels, meta.Metadata)

	if meta.StorageClass != "" {
		info.Labels["b2.storage-class"] = meta.StorageClass
	}
	if meta.ETag != "" {
		info.Labels["b2.etag"] = meta.ETag
	}
	if meta.VersionID != "" {
		info.Labels["b2.version"] = meta.VersionID
	}
	if meta.Checksums.SHA256 != "" {
		info.Digest = digest.Digest("sha256:" + meta.Checksums.SHA256)
	}
	if meta.Checksums.SHA1 != "" {
		info.Digest = digest.Digest("sha1:" + meta.Checksums.SHA1)
	}
	if meta.Checksums.CRC32 != "" {
		info.Digest = digest.Digest("crc32:" + meta.Checksums.CRC32)
	}
	if meta.Checksums.CRC32C != "" {
		info.Digest = digest.Digest("crc32c:" + meta.Checksums.CRC32C)
	}

	return info
}
