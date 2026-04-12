package registry

import (
	"context"
	"fmt"
	"io"
	"math/rand"
	"strings"
	"sync"
	"time"

	"github.com/containerd/containerd/v2/core/content"
	"github.com/containerd/errdefs"
	"github.com/distribution/reference"
	digest "github.com/opencontainers/go-digest"
	v1 "github.com/opencontainers/image-spec/specs-go/v1"
)

const (
	defaultCacheTTL    = 5 * time.Minute
	defaultRetryMax    = 3
	defaultWorkerLimit = 32
	retryBase          = 100 * time.Millisecond
	retryJitter        = 0.2
)

// Store implements content.Store for remote OCI registries with:
//   - Transparent local caching (read-through / write-through)
//   - 256-shard false-sharing-padded InfoCache
//   - 64-shard ingestion tracker (no global lock on concurrent Writers)
//   - Async concurrent dual-write: local cache via buffered channel worker
//   - Exponential-backoff retry for transient remote errors
//   - Parallel lifecycle hook fan-out
//
// Field layout is ordered hot → cold to maximise first-cache-line utility.
type Store struct {
	infoCache  *InfoCache
	ingestions *ingestionTracker
	hooks      []Hook

	remote      RegistryBackend
	local       content.Store
	ref         string
	retryMax    int
	workerLimit int
}

// compile-time interface assertions
var (
	_ content.Store         = (*Store)(nil)
	_ content.Provider      = (*Store)(nil)
	_ content.Ingester      = (*Store)(nil)
	_ content.IngestManager = (*Store)(nil)
	_ content.Manager       = (*Store)(nil)
)

// New creates a registry-backed content Store.
//
// remote provides remote OCI operations; local provides committed content
// storage and caching; ref is the base OCI reference (e.g. "docker.io/library/nginx").
func New(remote RegistryBackend, local content.Store, ref string, opts ...StoreOption) (*Store, error) {
	if remote == nil {
		return nil, fmt.Errorf("registry: backend must not be nil")
	}
	if local == nil {
		return nil, fmt.Errorf("registry: local store must not be nil")
	}
	if ref == "" {
		return nil, fmt.Errorf("registry: reference must not be empty: %w", errdefs.ErrInvalidArgument)
	}
	if _, err := reference.ParseNamed(ref); err != nil {
		return nil, fmt.Errorf("registry: invalid reference %q: %w", ref, err)
	}

	var so storeOptions
	for _, opt := range opts {
		opt(&so)
	}
	ttl := so.cacheTTL
	if ttl <= 0 {
		ttl = defaultCacheTTL
	}
	retryMax := so.retryMax
	if retryMax <= 0 {
		retryMax = defaultRetryMax
	}
	workerLimit := so.workerLimit
	if workerLimit <= 0 {
		workerLimit = defaultWorkerLimit
	}

	return &Store{
		remote:      remote,
		local:       local,
		ref:         ref,
		hooks:       so.hooks,
		retryMax:    retryMax,
		workerLimit: workerLimit,
		ingestions:  newIngestionTracker(),
		infoCache:   newInfoCache(ttl),
	}, nil
}

// ---------------------------------------------------------------------------
// Provider — query committed content by digest
// ---------------------------------------------------------------------------

// Info returns metadata for committed content identified by dgst.
//
// Lookup order:
//  1. Sharded in-memory InfoCache (lock-free read on hot-shard hit, O(1)).
//  2. Local committed store (disk / in-process cache).
//  3. Remote registry with exponential-backoff retry.
func (s *Store) Info(ctx context.Context, dgst digest.Digest) (content.Info, error) {
	if dgst == "" {
		return content.Info{}, storeErr(ErrKindInvalidArgument, "Info", "empty digest", nil)
	}

	// 1. Sharded in-memory cache (hot path — O(1), no syscall).
	if info, ok := s.infoCache.Get(dgst); ok {
		s.emit(ctx, Event{Kind: EventBlobCached, Digest: dgst, Size: info.Size, Timestamp: time.Now()})
		return info, nil
	}

	// 2. Local committed store.
	if info, err := s.local.Info(ctx, dgst); err == nil {
		s.infoCache.Set(dgst, info)
		s.emit(ctx, Event{Kind: EventBlobCached, Digest: dgst, Size: info.Size, Timestamp: time.Now()})
		return info, nil
	}

	// 3. Remote registry with retry.
	info, err := s.fetchRemoteInfoWithRetry(ctx, dgst)
	if err != nil {
		return content.Info{}, err
	}
	s.infoCache.Set(dgst, info)
	s.emit(ctx, Event{Kind: EventBlobAccessed, Digest: dgst, Size: info.Size, Timestamp: time.Now()})
	return info, nil
}

// ReaderAt returns a ReaderAt for committed content.
//
// Local cache hit: returned directly (zero network I/O).
// Cache miss: fetches from remote with retry, tees data into local cache as it
// is read. Uses an io.ReaderAt fast path (zero-copy, lock-free) when possible.
func (s *Store) ReaderAt(ctx context.Context, desc v1.Descriptor) (content.ReaderAt, error) {
	if err := desc.Digest.Validate(); err != nil {
		return nil, storeErr(ErrKindInvalidArgument, "ReaderAt", "invalid digest", err)
	}

	// Local cache hit — fast path.
	if ra, err := s.local.ReaderAt(ctx, desc); err == nil {
		s.emit(ctx, Event{Kind: EventBlobCached, Digest: desc.Digest, Size: desc.Size, Timestamp: time.Now()})
		return ra, nil
	}

	// Remote fetch with retry.
	rc, err := s.fetchWithRetry(ctx, desc)
	if err != nil {
		return nil, storeErr(ErrKindUnavailable, "ReaderAt", desc.Digest.String(), err)
	}

	// Best-effort local writer for tee-caching (ignore error — cache is optional).
	var lw content.Writer
	if w, werr := s.local.Writer(ctx,
		content.WithDescriptor(desc),
		content.WithRef(desc.Digest.String()),
	); werr == nil {
		lw = w
	}

	s.emit(ctx, Event{Kind: EventBlobFetched, Digest: desc.Digest, Size: desc.Size, Timestamp: time.Now()})
	return newContentReaderAt(rc, lw, desc.Size), nil
}

// ---------------------------------------------------------------------------
// Ingester — initiate write operations
// ---------------------------------------------------------------------------

// Writer initiates a new content ingestion that pushes data to the remote registry.
// Local cache writes run concurrently via an async goroutine channel so the caller
// only blocks on remote I/O.
func (s *Store) Writer(ctx context.Context, opts ...content.WriterOpt) (content.Writer, error) {
	var wOpts content.WriterOpts
	for _, opt := range opts {
		if err := opt(&wOpts); err != nil {
			return nil, storeErr(ErrKindInvalidArgument, "Writer", "apply option", err)
		}
	}

	dgst := wOpts.Desc.Digest
	ref := wOpts.Ref

	if dgst == "" {
		var err error
		dgst, err = digestFromRef(ref)
		if err != nil {
			return nil, storeErr(ErrKindInvalidArgument, "Writer", "resolve digest", err)
		}
		wOpts.Desc.Digest = dgst
	}

	ingRef := ref
	if ingRef == "" {
		ingRef = dgst.String()
	}

	// Reject duplicate active ingestion.
	if _, exists := s.ingestions.Get(ingRef); exists {
		return nil, storeErr(ErrKindAlreadyExists, "Writer",
			fmt.Sprintf("ingestion %q already active", ingRef), nil)
	}

	// Remote push with retry.
	rw, err := s.pushWithRetry(ctx, wOpts.Desc)
	if err != nil {
		return nil, storeErr(ErrKindUnavailable, "Writer", "remote push", err)
	}

	// Best-effort local cache writer (nil is fine — Write handles it).
	var lw content.Writer
	if w, werr := s.local.Writer(ctx,
		content.WithDescriptor(wOpts.Desc),
		content.WithRef(ingRef),
	); werr == nil {
		lw = w
	}

	cw := newContentWriter(s, ingRef, wOpts.Desc, rw, lw, s.workerLimit)

	now := time.Now()
	s.ingestions.Add(ingRef, &activeIngestion{
		ref:       ingRef,
		desc:      wOpts.Desc,
		writer:    cw,
		startedAt: now,
		updatedAt: now,
	})
	return cw, nil
}

// ---------------------------------------------------------------------------
// IngestManager — manage active ingestions
// ---------------------------------------------------------------------------

// Status returns the status of an active ingestion by ref.
// Falls through to the local store if the ref is not tracked here.
func (s *Store) Status(ctx context.Context, ref string) (content.Status, error) {
	if ing, ok := s.ingestions.Get(ref); ok {
		st, err := ing.writer.Status()
		if err != nil {
			return content.Status{}, err
		}
		st.Ref = ref
		st.StartedAt = ing.startedAt
		st.UpdatedAt = ing.updatedAt
		return st, nil
	}
	return s.local.Status(ctx, ref)
}

// ListStatuses returns all active ingestions matching the optional filters.
func (s *Store) ListStatuses(ctx context.Context, filters ...string) ([]content.Status, error) {
	all := s.ingestions.All()
	out := make([]content.Status, 0, len(all))
	for _, ing := range all {
		st, err := ing.writer.Status()
		if err != nil {
			continue
		}
		st.Ref = ing.ref
		st.StartedAt = ing.startedAt
		st.UpdatedAt = ing.updatedAt
		if matchesFilters(st, filters) {
			out = append(out, st)
		}
	}
	// Include local-store ingestions (e.g. background cache writers).
	if localStatuses, lerr := s.local.ListStatuses(ctx, filters...); lerr == nil {
		out = append(out, localStatuses...)
	}
	return out, nil
}

// Abort cancels an active ingestion by ref.
func (s *Store) Abort(ctx context.Context, ref string) error {
	if ing := s.ingestions.Remove(ref); ing != nil {
		if c, ok := ing.writer.(io.Closer); ok {
			return c.Close()
		}
		return nil
	}
	return s.local.Abort(ctx, ref)
}

// ---------------------------------------------------------------------------
// Manager — manage committed content
// ---------------------------------------------------------------------------

// Update modifies labels for committed content and invalidates the info cache.
func (s *Store) Update(ctx context.Context, info content.Info, fieldpaths ...string) (content.Info, error) {
	updated, err := s.local.Update(ctx, info, fieldpaths...)
	if err == nil {
		s.infoCache.Delete(info.Digest)
	}
	return updated, err
}

// Walk iterates over all committed content in the local store.
func (s *Store) Walk(ctx context.Context, fn content.WalkFunc, filters ...string) error {
	return s.local.Walk(ctx, fn, filters...)
}

// Delete removes content from the local store, invalidates the info cache, and
// emits EventBlobDeleted.
func (s *Store) Delete(ctx context.Context, dgst digest.Digest) error {
	if err := s.local.Delete(ctx, dgst); err != nil {
		return err
	}
	s.infoCache.Delete(dgst)
	s.emit(ctx, Event{Kind: EventBlobDeleted, Digest: dgst, Timestamp: time.Now()})
	return nil
}

// Close cancels all active ingestions, flushes the info cache, and closes
// the local store if it implements io.Closer.
func (s *Store) Close() error {
	for _, ing := range s.ingestions.RemoveAll() {
		if c, ok := ing.writer.(io.Closer); ok {
			_ = c.Close()
		}
	}
	s.infoCache.Flush()
	if c, ok := s.local.(io.Closer); ok {
		return c.Close()
	}
	return nil
}

// ---------------------------------------------------------------------------
// Internal helpers
// ---------------------------------------------------------------------------

// emit fans out evt to all registered hooks.
//
// Single-hook path: inline call — no goroutine overhead.
// Multi-hook path: parallel fan-out with WaitGroup — hooks never serialize each other.
func (s *Store) emit(ctx context.Context, evt Event) {
	switch len(s.hooks) {
	case 0:
		return
	case 1:
		s.hooks[0].OnEvent(ctx, evt)
	default:
		var wg sync.WaitGroup
		wg.Add(len(s.hooks))
		for _, h := range s.hooks {
			h := h
			go func() { defer wg.Done(); h.OnEvent(ctx, evt) }()
		}
		wg.Wait()
	}
}

// fetchRemoteInfoWithRetry resolves content.Info from the remote registry
// using exponential-backoff retry for transient errors.
func (s *Store) fetchRemoteInfoWithRetry(ctx context.Context, dgst digest.Digest) (content.Info, error) {
	named, err := reference.ParseNamed(s.ref)
	if err != nil {
		return content.Info{}, storeErr(ErrKindInvalidArgument, "Info", "parse reference", err)
	}
	dref, err := reference.WithDigest(named, dgst)
	if err != nil {
		return content.Info{}, storeErr(ErrKindInvalidArgument, "Info", "build digest ref", err)
	}

	var lastErr error
	for i := 0; i < s.retryMax; i++ {
		if i > 0 {
			if err := sleepWithJitter(ctx, i); err != nil {
				return content.Info{}, err
			}
		}
		_, desc, rerr := s.remote.Resolve(ctx, dref.String())
		if rerr == nil {
			return content.Info{
				Digest: desc.Digest,
				Size:   desc.Size,
				Labels: desc.Annotations,
			}, nil
		}
		lastErr = rerr
	}
	return content.Info{}, notFoundErr("Info", lastErr)
}

// fetchWithRetry fetches content from the remote registry with retry.
func (s *Store) fetchWithRetry(ctx context.Context, desc v1.Descriptor) (io.ReadCloser, error) {
	var lastErr error
	for i := 0; i < s.retryMax; i++ {
		if i > 0 {
			if err := sleepWithJitter(ctx, i); err != nil {
				return nil, err
			}
		}
		rc, err := s.remote.Fetch(ctx, s.ref, desc)
		if err == nil {
			return rc, nil
		}
		lastErr = err
	}
	return nil, lastErr
}

// pushWithRetry initiates a remote push with retry.
func (s *Store) pushWithRetry(ctx context.Context, desc v1.Descriptor) (content.Writer, error) {
	var lastErr error
	for i := 0; i < s.retryMax; i++ {
		if i > 0 {
			if err := sleepWithJitter(ctx, i); err != nil {
				return nil, err
			}
		}
		w, err := s.remote.Push(ctx, s.ref, desc)
		if err == nil {
			return w, nil
		}
		lastErr = err
	}
	return nil, lastErr
}

// sleepWithJitter waits for full-jitter exponential backoff duration.
// Returns ctx.Err() if the context is cancelled during the sleep.
func sleepWithJitter(ctx context.Context, attempt int) error {
	exp := retryBase << uint(attempt-1)
	jitter := time.Duration((rand.Float64()*2 - 1) * float64(exp) * retryJitter)
	d := exp + jitter
	if d <= 0 {
		d = retryBase
	}
	select {
	case <-ctx.Done():
		return ctx.Err()
	case <-time.After(d):
		return nil
	}
}

// digestFromRef extracts a digest from a bare "sha256:…" string or
// a "name@sha256:…" reference string.
func digestFromRef(ref string) (digest.Digest, error) {
	if ref == "" {
		return "", fmt.Errorf("either descriptor digest or ref with digest is required")
	}
	// Try bare digest first.
	if dgst, err := digest.Parse(ref); err == nil {
		return dgst, dgst.Validate()
	}
	// Try name@digest.
	if !strings.Contains(ref, "@") {
		return "", fmt.Errorf("ref %q must be a valid digest or contain @digest", ref)
	}
	parts := strings.SplitN(ref, "@", 2)
	dgst, err := digest.Parse(parts[1])
	if err != nil {
		return "", fmt.Errorf("parse digest from ref %q: %w", ref, err)
	}
	return dgst, dgst.Validate()
}

// matchesFilters returns true when st.Ref has at least one filter as a prefix,
// or when filters is empty (match-all).
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
