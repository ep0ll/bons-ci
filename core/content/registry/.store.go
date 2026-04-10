package registry

import (
	"context"
	"io"
	"strings"
	"sync"
	"time"

	"github.com/bons/bons-ci/core/content/registry/reader"
	"github.com/containerd/containerd/v2/core/content"
	"github.com/containerd/containerd/v2/core/transfer/registry"
	"github.com/distribution/reference"
	"github.com/opencontainers/go-digest"
	v1 "github.com/opencontainers/image-spec/specs-go/v1"
	"github.com/pkg/errors"
)

var (
	// ErrInvalidReference is returned when the reference cannot be parsed
	ErrInvalidReference = errors.New("invalid reference")
	// ErrMissingDescriptor is returned when a descriptor is required but not provided
	ErrMissingDescriptor = errors.New("missing descriptor")
	// ErrNotFound is returned when content is not found in either remote or local store
	ErrNotFound = errors.New("content not found")
)

// RegistryStore implements content.Store for remote registries with local caching
// It strictly follows the content lifecycle:
// 1. Ingester: Writer() initiates write operations
// 2. IngestManager: Status(), ListStatuses(), Abort() manage active ingestions
// 3. Provider: Info(), ReaderAt() query committed content by digest
// 4. Manager: Update(), Walk(), Delete() manage committed content
type RegistryStore struct {
	ref      string
	registry *registry.OCIRegistry
	store    content.Store // Local backing store for committed content
	opts     []registry.Opt

	// Active ingestions tracking (IngestManager responsibility)
	ingestionsMu sync.RWMutex
	ingestions   map[string]*activeIngestion

	// Cache for remote Info lookups (Provider optimization)
	infoCache    sync.Map // digest.Digest -> cacheEntry
	infoCacheTTL time.Duration

	// Registry pooling to avoid repeated initialization
	registryMu    sync.RWMutex
	registryCache map[string]*registry.OCIRegistry
}

type activeIngestion struct {
	ref       string
	desc      v1.Descriptor
	writer    content.Writer
	startedAt time.Time
	updatedAt time.Time
}

type cacheEntry struct {
	info      content.Info
	timestamp time.Time
}

// NewRegistryStore creates a new registry-backed content store
func NewRegistryStore(ref string, localStore content.Store, opts ...registry.Opt) (*RegistryStore, error) {
	if ref == "" {
		return nil, errors.Wrap(ErrInvalidReference, "reference cannot be empty")
	}

	if localStore == nil {
		return nil, errors.New("local store cannot be nil")
	}

	// Validate reference format
	if _, err := reference.ParseNamed(ref); err != nil {
		return nil, errors.Wrap(ErrInvalidReference, err.Error())
	}

	return &RegistryStore{
		ref:           ref,
		store:         localStore,
		opts:          opts,
		ingestions:    make(map[string]*activeIngestion),
		infoCacheTTL:  5 * time.Minute,
		registryCache: make(map[string]*registry.OCIRegistry),
	}, nil
}

// ============================================================================
// Provider Interface - Query committed content by digest
// ============================================================================

// Info returns information about committed content by digest
// Per content.Store semantics: only returns info for COMMITTED content
// Active ingestions are NOT visible through this method
func (r *RegistryStore) Info(ctx context.Context, dgst digest.Digest) (content.Info, error) {
	if dgst == "" {
		return content.Info{}, errors.Wrap(digest.ErrDigestInvalidFormat, "empty digest")
	}

	// Check cache first
	if entry, ok := r.infoCache.Load(dgst); ok {
		cached := entry.(cacheEntry)
		if time.Since(cached.timestamp) < r.infoCacheTTL {
			return cached.info, nil
		}
		r.infoCache.Delete(dgst)
	}

	// Try local committed store first
	if info, err := r.store.Info(ctx, dgst); err == nil {
		r.cacheInfo(dgst, info)
		return info, nil
	}

	// Fall back to remote registry for committed content
	info, err := r.fetchRemoteInfo(ctx, dgst)
	if err != nil {
		return content.Info{}, err
	}

	r.cacheInfo(dgst, info)
	return info, nil
}

// ReaderAt returns a ReaderAt for committed content by descriptor
// Per content.Store semantics: only reads COMMITTED content
// This will fetch from remote registry if not in local cache
func (r *RegistryStore) ReaderAt(ctx context.Context, desc v1.Descriptor) (content.ReaderAt, error) {
	if err := desc.Digest.Validate(); err != nil {
		return nil, errors.Wrap(err, "invalid descriptor digest")
	}

	// Try local committed store first
	if ra, err := r.store.ReaderAt(ctx, desc); err == nil {
		return ra, nil
	}

	// Fetch from remote registry - this is a read operation for committed content
	return r.fetchRemoteContent(ctx, desc)
}

// fetchRemoteContent fetches content from the remote registry
func (r *RegistryStore) fetchRemoteContent(ctx context.Context, desc v1.Descriptor) (content.ReaderAt, error) {
	reg, err := r.getOrCreateRegistry(ctx, r.ref)
	if err != nil {
		return nil, err
	}

	fetcher, err := reg.Fetcher(ctx, r.ref)
	if err != nil {
		return nil, errors.Wrap(err, "failed to get fetcher")
	}

	rc, err := fetcher.Fetch(ctx, desc)
	if err != nil {
		return nil, errors.Wrap(err, "failed to fetch content")
	}

	// Cache the content locally for future reads
	// This creates a NEW ingestion in the local store (separate from registry ingestions)
	writer, err := r.store.Writer(ctx, content.WithDescriptor(desc))
	if err != nil {
		rc.Close()
		return nil, errors.Wrap(err, "failed to create local writer for caching")
	}

	readerAt, err := reader.RegistryReader(rc, writer, desc.Size)
	if err != nil {
		rc.Close()
		writer.Close()
		return nil, errors.Wrap(err, "failed to create registry reader")
	}

	return readerAt, nil
}

// ============================================================================
// Ingester Interface - Initiate write operations
// ============================================================================

// Writer initiates a new ingestion (write operation)
// Per content.Store semantics: creates an active ingestion that is NOT visible
// through Provider or Manager until committed
func (r *RegistryStore) Writer(ctx context.Context, opts ...content.WriterOpt) (content.Writer, error) {
	var wOpts content.WriterOpts
	for _, opt := range opts {
		if err := opt(&wOpts); err != nil {
			return nil, errors.Wrap(err, "failed to apply writer option")
		}
	}

	// Extract and validate digest
	dgst := wOpts.Desc.Digest
	ref := wOpts.Ref

	if dgst == "" {
		if ref == "" {
			return nil, errors.Wrap(ErrMissingDescriptor, "either descriptor or ref with digest is required")
		}

		// Try to extract digest from ref (format: name@sha256:...)
		if !strings.Contains(ref, "@") {
			return nil, errors.Wrap(ErrMissingDescriptor, "ref must contain digest (@sha256:...)")
		}

		parts := strings.Split(ref, "@")
		if len(parts) != 2 {
			return nil, errors.Wrap(ErrInvalidReference, "invalid ref format")
		}

		var err error
		dgst, err = digest.Parse(parts[1])
		if err != nil {
			return nil, errors.Wrap(err, "failed to parse digest from ref")
		}
		wOpts.Desc.Digest = dgst
	}

	if err := dgst.Validate(); err != nil {
		return nil, errors.Wrap(err, "invalid digest")
	}

	// Use ref or digest as ingestion key
	ingestionRef := ref
	if ingestionRef == "" {
		ingestionRef = dgst.String()
	}

	// Check if this ingestion is already active
	r.ingestionsMu.RLock()
	if existing, ok := r.ingestions[ingestionRef]; ok {
		r.ingestionsMu.RUnlock()
		// Return error or the existing writer based on your policy
		return nil, errors.Errorf("ingestion %s already active (started %v)", ingestionRef, existing.startedAt)
	}
	r.ingestionsMu.RUnlock()

	// Get or create registry instance
	reg, err := r.getOrCreateRegistry(ctx, r.ref)
	if err != nil {
		return nil, err
	}

	// Get pusher for the descriptor
	pusher, err := reg.Pusher(ctx, wOpts.Desc)
	if err != nil {
		return nil, errors.Wrap(err, "failed to get pusher")
	}

	// Create the remote writer
	remoteWriter, err := pusher.Push(ctx, wOpts.Desc)
	if err != nil {
		return nil, errors.Wrap(err, "failed to create push writer")
	}

	// Create tracked writer that manages ingestion lifecycle
	trackedWriter := &trackedWriter{
		Writer:     remoteWriter,
		store:      r,
		ref:        ingestionRef,
		desc:       wOpts.Desc,
		localStore: r.store,
		ctx:        ctx,
	}

	// Register active ingestion
	r.ingestionsMu.Lock()
	r.ingestions[ingestionRef] = &activeIngestion{
		ref:       ingestionRef,
		desc:      wOpts.Desc,
		writer:    trackedWriter,
		startedAt: time.Now(),
		updatedAt: time.Now(),
	}
	r.ingestionsMu.Unlock()

	return trackedWriter, nil
}

// ============================================================================
// IngestManager Interface - Manage active ingestions
// ============================================================================

// Status returns the status of an active ingestion by ref
// Per content.Store semantics: only returns status for ACTIVE ingestions
func (r *RegistryStore) Status(ctx context.Context, ref string) (content.Status, error) {
	r.ingestionsMu.RLock()
	ingestion, ok := r.ingestions[ref]
	r.ingestionsMu.RUnlock()

	if !ok {
		// Not an active ingestion, check local store
		return r.store.Status(ctx, ref)
	}

	status, err := ingestion.writer.Status()
	if err != nil {
		return content.Status{}, err
	}

	status.Ref = ref
	status.StartedAt = ingestion.startedAt
	status.UpdatedAt = ingestion.updatedAt

	return status, nil
}

// ListStatuses returns all active ingestions
// Per content.Store semantics: only lists ACTIVE ingestions, not committed content
func (r *RegistryStore) ListStatuses(ctx context.Context, filters ...string) ([]content.Status, error) {
	r.ingestionsMu.RLock()
	defer r.ingestionsMu.RUnlock()

	statuses := make([]content.Status, 0, len(r.ingestions))

	for ref, ingestion := range r.ingestions {
		status, err := ingestion.writer.Status()
		if err != nil {
			continue // Skip failed ingestions
		}

		status.Ref = ref
		status.StartedAt = ingestion.startedAt
		status.UpdatedAt = ingestion.updatedAt

		// Apply filters
		if matchesFilters(status, filters) {
			statuses = append(statuses, status)
		}
	}

	// Also include local store's active ingestions (for caching operations)
	localStatuses, err := r.store.ListStatuses(ctx, filters...)
	if err == nil {
		statuses = append(statuses, localStatuses...)
	}

	return statuses, nil
}

// Abort aborts an active ingestion
// Per content.Store semantics: only aborts ACTIVE ingestions
func (r *RegistryStore) Abort(ctx context.Context, ref string) error {
	r.ingestionsMu.Lock()
	ingestion, ok := r.ingestions[ref]
	if ok {
		delete(r.ingestions, ref)
	}
	r.ingestionsMu.Unlock()

	if !ok {
		// Not a registry ingestion, try local store
		return r.store.Abort(ctx, ref)
	}

	// Abort the writer
	if closer, ok := ingestion.writer.(io.Closer); ok {
		return closer.Close()
	}

	return nil
}

// ============================================================================
// Manager Interface - Manage committed content
// ============================================================================

// Update modifies metadata for committed content
// Per content.Store semantics: only updates COMMITTED content
func (r *RegistryStore) Update(ctx context.Context, info content.Info, fieldpaths ...string) (content.Info, error) {
	updated, err := r.store.Update(ctx, info, fieldpaths...)
	if err == nil {
		// Invalidate cache for updated content
		r.infoCache.Delete(info.Digest)
	}
	return updated, err
}

// Walk iterates over all committed content
// Per content.Store semantics: only walks COMMITTED content, not active ingestions
func (r *RegistryStore) Walk(ctx context.Context, fn content.WalkFunc, filters ...string) error {
	return r.store.Walk(ctx, fn, filters...)
}

// Delete removes committed content
// Per content.Store semantics: only deletes COMMITTED content
func (r *RegistryStore) Delete(ctx context.Context, dgst digest.Digest) error {
	err := r.store.Delete(ctx, dgst)
	if err == nil {
		r.infoCache.Delete(dgst)
	}
	return err
}

// ============================================================================
// Writer implementation that tracks ingestion lifecycle
// ============================================================================

type trackedWriter struct {
	content.Writer
	store      *RegistryStore
	ref        string
	desc       v1.Descriptor
	localStore content.Store
	ctx        context.Context

	localWriter content.Writer
	initOnce    sync.Once
	initErr     error
}

func (tw *trackedWriter) Write(p []byte) (n int, err error) {
	// Lazy init local writer for caching
	tw.initOnce.Do(func() {
		tw.localWriter, tw.initErr = tw.localStore.Writer(tw.ctx, content.WithDescriptor(tw.desc))
	})

	// Write to remote registry (primary operation)
	n, err = tw.Writer.Write(p)
	if err != nil {
		return n, err
	}

	// Best-effort write to local cache
	if tw.localWriter != nil && tw.initErr == nil {
		tw.localWriter.Write(p)
	}

	// Update ingestion timestamp
	tw.store.ingestionsMu.Lock()
	if ingestion, ok := tw.store.ingestions[tw.ref]; ok {
		ingestion.updatedAt = time.Now()
	}
	tw.store.ingestionsMu.Unlock()

	return n, nil
}

func (tw *trackedWriter) Commit(ctx context.Context, size int64, expected digest.Digest, opts ...content.Opt) error {
	// Commit to remote registry (primary operation)
	err := tw.Writer.Commit(ctx, size, expected, opts...)
	if err != nil {
		return err
	}

	// Best-effort commit to local cache
	if tw.localWriter != nil && tw.initErr == nil {
		tw.localWriter.Commit(ctx, size, expected, opts...)
	}

	// Remove from active ingestions (now it's committed and visible via Provider/Manager)
	tw.store.ingestionsMu.Lock()
	delete(tw.store.ingestions, tw.ref)
	tw.store.ingestionsMu.Unlock()

	// Invalidate info cache since we have new content
	tw.store.infoCache.Delete(expected)

	return nil
}

func (tw *trackedWriter) Close() error {
	var errs []error

	if err := tw.Writer.Close(); err != nil {
		errs = append(errs, errors.Wrap(err, "remote close failed"))
	}

	if tw.localWriter != nil {
		if err := tw.localWriter.Close(); err != nil {
			errs = append(errs, errors.Wrap(err, "local close failed"))
		}
	}

	// Remove from active ingestions on close
	tw.store.ingestionsMu.Lock()
	delete(tw.store.ingestions, tw.ref)
	tw.store.ingestionsMu.Unlock()

	if len(errs) > 0 {
		return errors.Errorf("close errors: %v", errs)
	}
	return nil
}

// ============================================================================
// Helper methods
// ============================================================================

func (r *RegistryStore) getOrCreateRegistry(ctx context.Context, ref string) (*registry.OCIRegistry, error) {
	r.registryMu.RLock()
	reg, ok := r.registryCache[ref]
	r.registryMu.RUnlock()

	if ok {
		return reg, nil
	}

	r.registryMu.Lock()
	defer r.registryMu.Unlock()

	// Double-check after acquiring write lock
	if reg, ok := r.registryCache[ref]; ok {
		return reg, nil
	}

	reg, err := registry.NewOCIRegistry(ctx, ref, r.opts...)
	if err != nil {
		return nil, errors.Wrap(err, "failed to create registry")
	}

	r.registryCache[ref] = reg
	return reg, nil
}

func (r *RegistryStore) fetchRemoteInfo(ctx context.Context, dgst digest.Digest) (content.Info, error) {
	ref, err := reference.ParseNamed(r.ref)
	if err != nil {
		return content.Info{}, errors.Wrap(ErrInvalidReference, err.Error())
	}

	dref, err := reference.WithDigest(ref, dgst)
	if err != nil {
		return content.Info{}, errors.Wrap(err, "failed to create digest reference")
	}

	reg, err := r.getOrCreateRegistry(ctx, dref.String())
	if err != nil {
		return content.Info{}, err
	}

	_, desc, err := reg.Resolve(ctx)
	if err != nil {
		return content.Info{}, errors.Wrap(ErrNotFound, err.Error())
	}

	return content.Info{
		Digest: desc.Digest,
		Size:   desc.Size,
		Labels: desc.Annotations,
	}, nil
}

func (r *RegistryStore) cacheInfo(dgst digest.Digest, info content.Info) {
	r.infoCache.Store(dgst, cacheEntry{
		info:      info,
		timestamp: time.Now(),
	})
}

func matchesFilters(status content.Status, filters []string) bool {
	if len(filters) == 0 {
		return true
	}

	for _, filter := range filters {
		// Simple prefix matching for ref
		if strings.HasPrefix(status.Ref, filter) {
			return true
		}
	}
	return false
}

// Close cleans up resources
func (r *RegistryStore) Close() error {
	r.ingestionsMu.Lock()
	defer r.ingestionsMu.Unlock()

	r.registryMu.Lock()
	defer r.registryMu.Unlock()

	// Abort any active ingestions
	for ref, ingestion := range r.ingestions {
		if closer, ok := ingestion.writer.(io.Closer); ok {
			closer.Close()
		}
		delete(r.ingestions, ref)
	}

	// Clear caches
	r.infoCache = sync.Map{}
	r.registryCache = make(map[string]*registry.OCIRegistry)

	// Close underlying store if it implements io.Closer
	if closer, ok := r.store.(io.Closer); ok {
		return closer.Close()
	}

	return nil
}

// Ensure RegistryStore implements content.Store and its constituent interfaces
var _ content.Store = (*RegistryStore)(nil)
var _ content.Provider = (*RegistryStore)(nil)
var _ content.Ingester = (*RegistryStore)(nil)
var _ content.IngestManager = (*RegistryStore)(nil)
var _ content.Manager = (*RegistryStore)(nil)
