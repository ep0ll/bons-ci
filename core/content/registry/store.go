package registry

import (
	"context"
	"fmt"
	"io"
	"strings"
	"sync"
	"time"

	"github.com/containerd/containerd/v2/core/content"
	"github.com/containerd/errdefs"
	"github.com/distribution/reference"
	digest "github.com/opencontainers/go-digest"
	v1 "github.com/opencontainers/image-spec/specs-go/v1"
)

const defaultCacheTTL = 5 * time.Minute

// Store implements content.Store for remote OCI registries with transparent
// local caching. It delegates remote operations to a [RegistryBackend] port
// and committed-content storage to a local [content.Store].
//
// Lifecycle:
//   - Ingester:      Writer() initiates remote push + local cache writes
//   - IngestManager: Status(), ListStatuses(), Abort() manage active ingestions
//   - Provider:      Info(), ReaderAt() query committed content by digest
//   - Manager:       Update(), Walk(), Delete() manage committed content
type Store struct {
	ref     string // base registry reference (e.g. "docker.io/library/nginx")
	backend RegistryBackend
	local   content.Store
	hooks   []Hook

	// Active ingestions tracking
	ingestionsMu sync.RWMutex
	ingestions   map[string]*activeIngestion

	// Cache for Info lookups to reduce remote calls
	infoCache    sync.Map // digest.Digest → infoCacheEntry
	infoCacheTTL time.Duration
}

type activeIngestion struct {
	ref       string
	desc      v1.Descriptor
	writer    content.Writer
	startedAt time.Time
	updatedAt time.Time
}

type infoCacheEntry struct {
	info      content.Info
	timestamp time.Time
}

// compile-time checks
var (
	_ content.Store        = (*Store)(nil)
	_ content.Provider     = (*Store)(nil)
	_ content.Ingester     = (*Store)(nil)
	_ content.IngestManager = (*Store)(nil)
	_ content.Manager      = (*Store)(nil)
)

// New creates a registry-backed content store.
//
// backend provides remote OCI registry operations, local provides committed
// content storage and caching. Options configure tracing, hooks, and cache TTL.
//
// If WithTracer is supplied, callers should wrap the result with NewTracedStore.
func New(backend RegistryBackend, local content.Store, ref string, opts ...StoreOption) (*Store, error) {
	if backend == nil {
		return nil, fmt.Errorf("registry: backend must not be nil")
	}
	if local == nil {
		return nil, fmt.Errorf("registry: local store must not be nil")
	}
	if ref == "" {
		return nil, fmt.Errorf("registry: reference must not be empty: %w", errdefs.ErrInvalidArgument)
	}

	// Validate reference format.
	if _, err := reference.ParseNamed(ref); err != nil {
		return nil, fmt.Errorf("registry: invalid reference %q: %w", ref, err)
	}

	var so storeOptions
	for _, opt := range opts {
		opt(&so)
	}

	ttl := so.cacheTTL
	if ttl == 0 {
		ttl = defaultCacheTTL
	}

	return &Store{
		ref:          ref,
		backend:      backend,
		local:        local,
		hooks:        so.hooks,
		ingestions:   make(map[string]*activeIngestion),
		infoCacheTTL: ttl,
	}, nil
}

// ---------------------------------------------------------------------------
// Provider Interface — Query committed content by digest
// ---------------------------------------------------------------------------

// Info returns information about committed content.
// It checks the in-memory cache first, then the local store, then the remote
// registry. Successful lookups are cached and emit EventBlobAccessed (or
// EventBlobCached for local hits).
func (s *Store) Info(ctx context.Context, dgst digest.Digest) (content.Info, error) {
	if dgst == "" {
		return content.Info{}, storeErr(ErrKindInvalidArgument, "Info", "empty digest", nil)
	}

	// 1. In-memory cache.
	if entry, ok := s.infoCache.Load(dgst); ok {
		cached := entry.(infoCacheEntry)
		if time.Since(cached.timestamp) < s.infoCacheTTL {
			s.emit(ctx, Event{Kind: EventBlobCached, Digest: dgst, Size: cached.info.Size, Timestamp: time.Now()})
			return cached.info, nil
		}
		s.infoCache.Delete(dgst)
	}

	// 2. Local committed store.
	if info, err := s.local.Info(ctx, dgst); err == nil {
		s.cacheInfo(dgst, info)
		s.emit(ctx, Event{Kind: EventBlobCached, Digest: dgst, Size: info.Size, Timestamp: time.Now()})
		return info, nil
	}

	// 3. Remote registry.
	info, err := s.fetchRemoteInfo(ctx, dgst)
	if err != nil {
		return content.Info{}, err
	}

	s.cacheInfo(dgst, info)
	s.emit(ctx, Event{Kind: EventBlobAccessed, Digest: dgst, Size: info.Size, Timestamp: time.Now()})
	return info, nil
}

// ReaderAt returns a ReaderAt for committed content.
// It tries the local store first; on miss it fetches from the remote registry
// and tees the data into the local cache.
func (s *Store) ReaderAt(ctx context.Context, desc v1.Descriptor) (content.ReaderAt, error) {
	if err := desc.Digest.Validate(); err != nil {
		return nil, storeErr(ErrKindInvalidArgument, "ReaderAt", "invalid digest", err)
	}

	// Try local store first.
	if ra, err := s.local.ReaderAt(ctx, desc); err == nil {
		s.emit(ctx, Event{Kind: EventBlobCached, Digest: desc.Digest, Size: desc.Size, Timestamp: time.Now()})
		return ra, nil
	}

	// Fetch from remote and tee into local cache.
	rc, err := s.backend.Fetch(ctx, s.ref, desc)
	if err != nil {
		return nil, storeErr(ErrKindUnavailable, "ReaderAt", desc.Digest.String(), err)
	}

	// Create local writer for caching.
	localWriter, err := s.local.Writer(ctx, content.WithDescriptor(desc), content.WithRef(desc.Digest.String()))
	if err != nil {
		rc.Close()
		return nil, storeErr(ErrKindUnknown, "ReaderAt", "create local cache writer", err)
	}

	s.emit(ctx, Event{Kind: EventBlobFetched, Digest: desc.Digest, Size: desc.Size, Timestamp: time.Now()})
	return newContentReaderAt(rc, localWriter, desc.Size), nil
}

// ---------------------------------------------------------------------------
// Ingester Interface — Initiate write operations
// ---------------------------------------------------------------------------

// Writer initiates a new ingestion pushing content to the remote registry.
// Data is simultaneously tee'd into the local cache (best-effort).
func (s *Store) Writer(ctx context.Context, opts ...content.WriterOpt) (content.Writer, error) {
	var wOpts content.WriterOpts
	for _, opt := range opts {
		if err := opt(&wOpts); err != nil {
			return nil, storeErr(ErrKindInvalidArgument, "Writer", "apply option", err)
		}
	}

	dgst := wOpts.Desc.Digest
	ref := wOpts.Ref

	// Resolve digest from ref if not in descriptor.
	if dgst == "" {
		var err error
		dgst, err = digestFromRef(ref)
		if err != nil {
			return nil, storeErr(ErrKindInvalidArgument, "Writer", "resolve digest", err)
		}
		wOpts.Desc.Digest = dgst
	}

	// Ingestion key.
	ingestionRef := ref
	if ingestionRef == "" {
		ingestionRef = dgst.String()
	}

	// Check for duplicate active ingestion.
	s.ingestionsMu.RLock()
	if existing, ok := s.ingestions[ingestionRef]; ok {
		s.ingestionsMu.RUnlock()
		return nil, storeErr(ErrKindAlreadyExists, "Writer",
			fmt.Sprintf("ingestion %q already active (started %v)", ingestionRef, existing.startedAt), nil)
	}
	s.ingestionsMu.RUnlock()

	// Push to remote registry.
	remoteWriter, err := s.backend.Push(ctx, s.ref, wOpts.Desc)
	if err != nil {
		return nil, storeErr(ErrKindUnavailable, "Writer", "remote push", err)
	}

	// Create local cache writer (best-effort).
	var localWriter content.Writer
	if lw, err := s.local.Writer(ctx, content.WithDescriptor(wOpts.Desc), content.WithRef(ingestionRef)); err == nil {
		localWriter = lw
	}

	cw := newContentWriter(s, ingestionRef, wOpts.Desc, remoteWriter, localWriter)

	// Register active ingestion.
	now := time.Now()
	s.ingestionsMu.Lock()
	s.ingestions[ingestionRef] = &activeIngestion{
		ref:       ingestionRef,
		desc:      wOpts.Desc,
		writer:    cw,
		startedAt: now,
		updatedAt: now,
	}
	s.ingestionsMu.Unlock()

	return cw, nil
}

// ---------------------------------------------------------------------------
// IngestManager Interface — Manage active ingestions
// ---------------------------------------------------------------------------

// Status returns the status of an active ingestion by ref.
func (s *Store) Status(ctx context.Context, ref string) (content.Status, error) {
	s.ingestionsMu.RLock()
	ingestion, ok := s.ingestions[ref]
	s.ingestionsMu.RUnlock()

	if !ok {
		return s.local.Status(ctx, ref)
	}

	st, err := ingestion.writer.Status()
	if err != nil {
		return content.Status{}, err
	}
	st.Ref = ref
	st.StartedAt = ingestion.startedAt
	st.UpdatedAt = ingestion.updatedAt
	return st, nil
}

// ListStatuses returns all active ingestions matching the filters.
func (s *Store) ListStatuses(ctx context.Context, filters ...string) ([]content.Status, error) {
	s.ingestionsMu.RLock()
	defer s.ingestionsMu.RUnlock()

	statuses := make([]content.Status, 0, len(s.ingestions))
	for ref, ingestion := range s.ingestions {
		st, err := ingestion.writer.Status()
		if err != nil {
			continue // skip failed ingestions
		}
		st.Ref = ref
		st.StartedAt = ingestion.startedAt
		st.UpdatedAt = ingestion.updatedAt

		if matchesFilters(st, filters) {
			statuses = append(statuses, st)
		}
	}

	// Include local store's active ingestions (for caching operations).
	if localStatuses, err := s.local.ListStatuses(ctx, filters...); err == nil {
		statuses = append(statuses, localStatuses...)
	}

	return statuses, nil
}

// Abort cancels an active ingestion.
func (s *Store) Abort(ctx context.Context, ref string) error {
	s.ingestionsMu.Lock()
	ingestion, ok := s.ingestions[ref]
	if ok {
		delete(s.ingestions, ref)
	}
	s.ingestionsMu.Unlock()

	if !ok {
		return s.local.Abort(ctx, ref)
	}

	if closer, ok := ingestion.writer.(io.Closer); ok {
		return closer.Close()
	}
	return nil
}

// ---------------------------------------------------------------------------
// Manager Interface — Manage committed content
// ---------------------------------------------------------------------------

// Update modifies metadata for committed content.
func (s *Store) Update(ctx context.Context, info content.Info, fieldpaths ...string) (content.Info, error) {
	updated, err := s.local.Update(ctx, info, fieldpaths...)
	if err == nil {
		s.infoCache.Delete(info.Digest) // invalidate cache
	}
	return updated, err
}

// Walk iterates over all committed content.
func (s *Store) Walk(ctx context.Context, fn content.WalkFunc, filters ...string) error {
	return s.local.Walk(ctx, fn, filters...)
}

// Delete removes committed content from the local store.
func (s *Store) Delete(ctx context.Context, dgst digest.Digest) error {
	err := s.local.Delete(ctx, dgst)
	if err == nil {
		s.infoCache.Delete(dgst)
		s.emit(ctx, Event{Kind: EventBlobDeleted, Digest: dgst, Timestamp: time.Now()})
	}
	return err
}

// Close cleans up resources: aborts active ingestions and clears caches.
func (s *Store) Close() error {
	// Collect writers under lock, then close outside to avoid deadlock
	// (contentWriter.Close calls removeIngestion which also locks).
	s.ingestionsMu.Lock()
	writers := make([]content.Writer, 0, len(s.ingestions))
	for _, ingestion := range s.ingestions {
		writers = append(writers, ingestion.writer)
	}
	s.ingestions = make(map[string]*activeIngestion)
	s.ingestionsMu.Unlock()

	for _, w := range writers {
		if closer, ok := w.(io.Closer); ok {
			closer.Close()
		}
	}

	s.infoCache = sync.Map{}

	if closer, ok := s.local.(io.Closer); ok {
		return closer.Close()
	}
	return nil
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

// cacheInfo stores info in the in-memory cache.
func (s *Store) cacheInfo(dgst digest.Digest, info content.Info) {
	s.infoCache.Store(dgst, infoCacheEntry{
		info:      info,
		timestamp: time.Now(),
	})
}

// removeIngestion removes an active ingestion by ref.
func (s *Store) removeIngestion(ref string) {
	s.ingestionsMu.Lock()
	delete(s.ingestions, ref)
	s.ingestionsMu.Unlock()
}

// updateIngestion updates the updatedAt timestamp for an active ingestion.
func (s *Store) updateIngestion(ref string) {
	s.ingestionsMu.Lock()
	if ing, ok := s.ingestions[ref]; ok {
		ing.updatedAt = time.Now()
	}
	s.ingestionsMu.Unlock()
}

// fetchRemoteInfo resolves content info from the remote registry.
func (s *Store) fetchRemoteInfo(ctx context.Context, dgst digest.Digest) (content.Info, error) {
	named, err := reference.ParseNamed(s.ref)
	if err != nil {
		return content.Info{}, storeErr(ErrKindInvalidArgument, "Info", "parse reference", err)
	}

	dref, err := reference.WithDigest(named, dgst)
	if err != nil {
		return content.Info{}, storeErr(ErrKindInvalidArgument, "Info", "create digest reference", err)
	}

	_, desc, err := s.backend.Resolve(ctx, dref.String())
	if err != nil {
		return content.Info{}, notFoundErr("Info", err)
	}

	return content.Info{
		Digest: desc.Digest,
		Size:   desc.Size,
		Labels: desc.Annotations,
	}, nil
}

// digestFromRef extracts a digest from a reference string.
// Supports bare digest ("sha256:...") and qualified refs ("name@sha256:...").
func digestFromRef(ref string) (digest.Digest, error) {
	if ref == "" {
		return "", fmt.Errorf("either descriptor digest or ref with digest is required")
	}

	// Try parsing as a bare digest first (e.g. "sha256:abc123...").
	if dgst, err := digest.Parse(ref); err == nil {
		return dgst, dgst.Validate()
	}

	// Try name@sha256:... format.
	if !strings.Contains(ref, "@") {
		return "", fmt.Errorf("ref %q must be a valid digest or contain @digest", ref)
	}

	parts := strings.SplitN(ref, "@", 2)
	dgst, err := digest.Parse(parts[1])
	if err != nil {
		return "", fmt.Errorf("parse digest from ref: %w", err)
	}
	return dgst, dgst.Validate()
}

// matchesFilters checks whether a status matches at least one filter.
// Empty filters match everything.
func matchesFilters(st content.Status, filters []string) bool {
	if len(filters) == 0 {
		return true
	}
	for _, f := range filters {
		if strings.HasPrefix(st.Ref, f) {
			return true
		}
	}
	return false
}
