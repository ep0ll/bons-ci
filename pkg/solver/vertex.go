// Package solver implements a production-grade DAG solver with leaf-first
// traversal, reactive scheduling, and pluggable caching. It walks upward from
// leaf vertices to their parent vertices, short-circuiting on cache hits and
// reusing partial parent caches.
//
// Design is informed by BuildKit's solver package but makes different
// concurrency trade-offs: instead of a single-threaded event loop, we use a
// concurrent worker pool with singleflight deduplication. This is safe because
// each vertex's state is protected by its own future (see execFuture).
package solver

import (
	"context"
	"fmt"
	"sync"
	"sync/atomic"
	"time"

	digest "github.com/opencontainers/go-digest"
)

// ─── Core graph types ────────────────────────────────────────────────────────

// Vertex is a node in the build DAG. It is content-addressable via Digest(),
// which must incorporate all transitive inputs. Two vertices with equal digests
// are treated as identical by the solver (same as BuildKit's design).
type Vertex interface {
	// Digest returns a stable, content-addressable checksum of this vertex and
	// all transitive inputs. IMPORTANT: like BuildKit, this digest is only valid
	// within a solve session. For cross-session cache lookup use CacheMap.Digest.
	Digest() digest.Digest

	// Inputs returns the upstream edges this vertex depends on. Ordering is
	// significant: index i corresponds to CacheMap.Deps[i].
	Inputs() []Edge

	// Name is a human-readable label for progress reporting and logging.
	Name() string

	// Sys returns the opaque operation descriptor used by ResolveOpFunc to
	// create an executable Op. In an LLB-like system this would be the
	// serialised proto. The solver never inspects this value directly.
	Sys() any

	// Options returns non-digest-affecting metadata: ignore-cache flag,
	// per-vertex cache sources, estimated cost, etc.
	Options() VertexOptions
}

// Index is a zero-based index into the result slice returned by Op.Exec.
type Index int

// Edge connects a consumer vertex to a specific output of a producer vertex.
type Edge struct {
	Index  Index
	Vertex Vertex
}

// String returns a compact debug representation.
func (e Edge) String() string {
	if e.Vertex == nil {
		return "<nil edge>"
	}
	return fmt.Sprintf("%s[%d]", e.Vertex.Name(), e.Index)
}

// ─── Vertex options ───────────────────────────────────────────────────────────

// VertexOptions carry metadata that does not affect the vertex digest.
type VertexOptions struct {
	// IgnoreCache forces re-execution even when a valid cache entry exists.
	IgnoreCache bool

	// CacheSources lists additional cache backends to probe for this vertex.
	// Checked after the primary store; results are written back to primary.
	CacheSources []CacheSource

	// Description holds arbitrary key-value display metadata.
	Description map[string]string

	// ExportCache, when non-nil, overrides the default export decision.
	ExportCache *bool

	// EstimatedCost is a scheduler hint (higher → run earlier).
	EstimatedCost float64
}

// CacheSource is a read-only cache backend for a specific vertex.
type CacheSource interface {
	Probe(ctx context.Context, key *CacheKey) (Result, bool, error)
}

// ─── Result types ─────────────────────────────────────────────────────────────

// Result is the abstract output of solving a vertex. Callers must call
// Release exactly once when they are done with it.
type Result interface {
	// ID returns a unique, stable identifier for this result within the store.
	ID() string
	// Release frees resources. Must be called exactly once.
	Release(ctx context.Context) error
	// Sys returns the underlying system object (e.g. a snapshot reference).
	Sys() any
	// Clone creates an additional reference to the same underlying data.
	Clone() Result
	// String returns a human-readable description for logging.
	String() string
}

// CachedResult is a Result that carries the cache keys which produced it.
// Required for cache export chains.
type CachedResult interface {
	Result
	CacheKeys() []ExportableCacheKey
}

// ExportableCacheKey pairs a CacheKey with an exporter for serialising the
// cache chain to a remote store.
type ExportableCacheKey struct {
	*CacheKey
	Exporter CacheExporter
}

// CacheExporter serialises a cache chain to a remote target.
type CacheExporter interface {
	ExportTo(ctx context.Context, target CacheExportTarget) error
}

// CacheExportTarget receives exported cache records.
type CacheExportTarget interface {
	Add(dgst digest.Digest, result Result) error
}

// ─── Cache key ────────────────────────────────────────────────────────────────

// CacheKey is a content-addressable identifier for a cached result. Unlike the
// vertex digest (which is session-scoped), CacheKey.ID is derived from
// CacheMap.Digest combined with dependency cache keys, making it stable across
// solver sessions and suitable for remote caching.
type CacheKey struct {
	mu sync.RWMutex

	// id is the computed stable string identifier.
	id string

	// digest is the operation-level cache map digest (from Op.CacheMap).
	digest digest.Digest

	// vtx is the LLB vertex digest that produced this key (session-scoped).
	vtx digest.Digest

	// output is the output index within the vertex.
	output Index

	// deps holds per-input cache keys, matching CacheMap.Deps ordering.
	deps [][]CacheKeyWithSelector
}

// NewCacheKey creates a root CacheKey (no dependency inputs).
func NewCacheKey(dgst, vtx digest.Digest, output Index) *CacheKey {
	return &CacheKey{
		id:     RootKeyDigest(dgst, output).String(),
		digest: dgst,
		vtx:    vtx,
		output: output,
	}
}

// NewCacheKeyWithDeps creates a CacheKey that incorporates dependency keys.
// The resulting id is derived by combining dgst with all dep key ids.
func NewCacheKeyWithDeps(dgst, vtx digest.Digest, output Index, deps [][]CacheKeyWithSelector) *CacheKey {
	ck := &CacheKey{
		digest: dgst,
		vtx:    vtx,
		output: output,
		deps:   deps,
	}
	ck.id = ck.computeID()
	return ck
}

func (ck *CacheKey) computeID() string {
	if len(ck.deps) == 0 {
		return RootKeyDigest(ck.digest, ck.output).String()
	}
	// Combine op digest with all dependency identifiers deterministically.
	h := digest.Canonical.Hash()
	fmt.Fprintf(h, "op:%s@%d", ck.digest, ck.output)
	for i, depSlice := range ck.deps {
		for _, d := range depSlice {
			fmt.Fprintf(h, "dep[%d]:%s+sel:%s", i, d.CacheKey.ID(), d.Selector)
		}
	}
	return digest.NewDigest(digest.Canonical, h).String()
}

// ID returns the stable string identifier.
func (ck *CacheKey) ID() string {
	ck.mu.RLock()
	defer ck.mu.RUnlock()
	return ck.id
}

// Digest returns the CacheMap-level digest.
func (ck *CacheKey) Digest() digest.Digest {
	ck.mu.RLock()
	defer ck.mu.RUnlock()
	return ck.digest
}

// VtxDigest returns the session-scoped vertex digest.
func (ck *CacheKey) VtxDigest() digest.Digest {
	ck.mu.RLock()
	defer ck.mu.RUnlock()
	return ck.vtx
}

// Output returns the output index.
func (ck *CacheKey) Output() Index { return ck.output }

// Deps returns a defensive copy of the dependency key slices.
func (ck *CacheKey) Deps() [][]CacheKeyWithSelector {
	ck.mu.RLock()
	defer ck.mu.RUnlock()
	out := make([][]CacheKeyWithSelector, len(ck.deps))
	for i, d := range ck.deps {
		out[i] = make([]CacheKeyWithSelector, len(d))
		copy(out[i], d)
	}
	return out
}

// SetDeps atomically sets the dependency keys and recomputes the id.
func (ck *CacheKey) SetDeps(deps [][]CacheKeyWithSelector) {
	ck.mu.Lock()
	defer ck.mu.Unlock()
	ck.deps = deps
	ck.id = ck.computeID()
}

// CacheKeyWithSelector pairs a cache key with an optional selector digest to
// narrow cache matching scope (e.g. a file path within a snapshot).
type CacheKeyWithSelector struct {
	Selector digest.Digest
	CacheKey ExportableCacheKey
}

// ─── CacheMap ─────────────────────────────────────────────────────────────────

// CacheMap describes how to derive the cache key for an operation. Returned by
// Op.CacheMap. This is the equivalent of BuildKit's CacheMap.
type CacheMap struct {
	// Digest is the operation-level digest. For stable cross-session caching it
	// must be content-addressable (e.g. image manifest digest, git commit SHA).
	Digest digest.Digest

	// Deps configures per-input cache behaviour.
	Deps []CacheMapDep

	// Opts are opaque options forwarded to cache load calls.
	Opts map[string]any
}

// CacheMapDep configures caching for one input of the operation.
type CacheMapDep struct {
	// Selector is merged with the input's cache key. In LLB, used for
	// file-path-scoped cache matching.
	Selector digest.Digest

	// ComputeDigestFunc derives a content-based cache key from an already-
	// executed dependency result. This is BuildKit's "slow cache" path.
	ComputeDigestFunc func(ctx context.Context, result Result) (digest.Digest, error)

	// PreprocessFunc runs on an input before it is passed to Op.Exec.
	PreprocessFunc func(ctx context.Context, result Result) error
}

// ─── Op ──────────────────────────────────────────────────────────────────────

// Op is the execution contract for a single vertex. The solver obtains an Op
// from ResolveOpFunc(vertex) and calls its methods in order.
type Op interface {
	// CacheMap returns the cache description. index supports multi-map ops
	// (currently only root vertices may return more than one map; done=true
	// signals the last map).
	CacheMap(ctx context.Context, index int) (*CacheMap, bool, error)

	// Exec executes the operation using already-resolved input results.
	// Returns one result per output index.
	Exec(ctx context.Context, inputs []Result) (outputs []Result, err error)

	// Acquire acquires resources needed for Exec (semaphore slots, worker
	// slots, file descriptors, …). The returned release func must be called
	// after Exec completes, even on error.
	Acquire(ctx context.Context) (release func(), err error)
}

// ResolveOpFunc converts a vertex's Sys() value into an executable Op.
// Equivalent to BuildKit's ResolveOpFunc.
type ResolveOpFunc func(vtx Vertex) (Op, error)

// ─── Internal result implementations ─────────────────────────────────────────

// simpleResult is the concrete Result returned by Op.Exec for normal vertices.
type simpleResult struct {
	id  string
	sys any
}

func (r *simpleResult) ID() string                      { return r.id }
func (r *simpleResult) Release(_ context.Context) error { return nil }
func (r *simpleResult) Sys() any                        { return r.sys }
func (r *simpleResult) Clone() Result                   { return &simpleResult{id: r.id, sys: r.sys} }
func (r *simpleResult) String() string                  { return fmt.Sprintf("result(%s)", r.id) }

// NewResult creates a simple result. Useful for tests and simple ops.
func NewResult(id string, sys any) Result {
	return &simpleResult{id: id, sys: sys}
}

// cachedResultRef is the lightweight Result returned for cache-hit vertices.
// It carries its CacheKeys so downstream exporters can walk the chain.
type cachedResultRef struct {
	id   string
	vtx  digest.Digest
	keys []ExportableCacheKey
}

func (r *cachedResultRef) ID() string                      { return r.id }
func (r *cachedResultRef) Release(_ context.Context) error { return nil }
func (r *cachedResultRef) Sys() any                        { return nil }
func (r *cachedResultRef) Clone() Result {
	return &cachedResultRef{id: r.id, vtx: r.vtx, keys: r.keys}
}
func (r *cachedResultRef) String() string              { return fmt.Sprintf("cached(%s)", r.id) }
func (r *cachedResultRef) CacheKeys() []ExportableCacheKey { return r.keys }

// wrappedCachedResult wraps a Result with CacheKeys to implement CachedResult.
type wrappedCachedResult struct {
	Result
	keys []ExportableCacheKey
}

func (r *wrappedCachedResult) CacheKeys() []ExportableCacheKey { return r.keys }
func (r *wrappedCachedResult) String() string                  { return r.Result.String() }

// NewCachedResult wraps result with cache keys.
func NewCachedResult(res Result, keys []ExportableCacheKey) CachedResult {
	return &wrappedCachedResult{Result: res, keys: keys}
}

// ─── Cache record ─────────────────────────────────────────────────────────────

// CacheRecord is a loadable cache entry — equivalent to BuildKit's CacheRecord.
type CacheRecord struct {
	ID        string
	Size      int
	CreatedAt time.Time
	Priority  int
	Key       *CacheKey
}

// ─── Helpers ──────────────────────────────────────────────────────────────────

// RootKeyDigest computes a deterministic digest from an op digest and output
// index. Used for root (no-input) vertices. Exported for test use.
func RootKeyDigest(dgst digest.Digest, output Index) digest.Digest {
	return digest.FromString(fmt.Sprintf("%s@%d", dgst, output))
}

// ─── Reference-counted results ───────────────────────────────────────────────
// These types implement BuildKit's SharedResult/splitResult pattern for safe
// concurrent result sharing. A SharedResult can be Clone()'d multiple times;
// Release() on the SharedResult releases the underlying result only once.
// splitResult uses atomic counters to ensure the underlying result is released
// only when both halves have been released.

// SharedResult enables safe concurrent cloning of a result. Each Clone() call
// returns a new reference; Release() releases the original.
type SharedResult struct {
	mu   sync.Mutex
	main Result
}

// NewSharedResult wraps a result for safe concurrent sharing.
func NewSharedResult(main Result) *SharedResult {
	return &SharedResult{main: main}
}

// Clone creates a new reference to the same underlying result.
// Both the original and the clone can be released independently.
func (r *SharedResult) Clone() Result {
	r.mu.Lock()
	defer r.mu.Unlock()
	r1, r2 := dup(r.main)
	r.main = r1
	return r2
}

// Release releases the original reference.
func (r *SharedResult) Release(ctx context.Context) error {
	r.mu.Lock()
	defer r.mu.Unlock()
	return r.main.Release(ctx)
}

// dup creates two split references from one result. The underlying result
// is only released when BOTH split references have been released.
func dup(res Result) (Result, Result) {
	sem := int64(0)
	return &splitResult{Result: res, sem: &sem}, &splitResult{Result: res, sem: &sem}
}

// splitResult is a reference-counted wrapper. Two splitResults share the same
// semaphore; the underlying result is only released when both have called
// Release(). This prevents double-release bugs in concurrent code.
type splitResult struct {
	released int64
	sem      *int64
	Result
}

// Release releases this half of the split reference. The underlying result
// is only released when both halves have been released.
func (r *splitResult) Release(ctx context.Context) error {
	if atomic.AddInt64(&r.released, 1) > 1 {
		return fmt.Errorf("releasing already released reference %s", r.ID())
	}
	if atomic.AddInt64(r.sem, 1) == 2 {
		return r.Result.Release(ctx)
	}
	return nil
}

// SharedCachedResult combines SharedResult with CachedResult for types that
// need both reference counting and cache key access.
type SharedCachedResult struct {
	*SharedResult
	CachedResult
}

// NewSharedCachedResult wraps a CachedResult for shared concurrent access.
func NewSharedCachedResult(res CachedResult) *SharedCachedResult {
	return &SharedCachedResult{
		SharedResult: NewSharedResult(res),
		CachedResult: res,
	}
}

// CloneCachedResult clones the result while preserving cache keys.
func (r *SharedCachedResult) CloneCachedResult() CachedResult {
	return &clonedCachedResult{Result: r.SharedResult.Clone(), cr: r.CachedResult}
}

// Clone returns the cloned result as a plain Result.
func (r *SharedCachedResult) Clone() Result {
	return r.CloneCachedResult()
}

// Release releases the shared result.
func (r *SharedCachedResult) Release(ctx context.Context) error {
	return r.SharedResult.Release(ctx)
}

type clonedCachedResult struct {
	Result
	cr CachedResult
}

func (ccr *clonedCachedResult) ID() string                      { return ccr.Result.ID() }
func (ccr *clonedCachedResult) CacheKeys() []ExportableCacheKey { return ccr.cr.CacheKeys() }

// ─── ResultProxy ──────────────────────────────────────────────────────────────
// ResultProxy is a lazy reference to a result that may not have been computed
// yet. This is equivalent to BuildKit's ResultProxy which wraps a future result.

// ResultProxy is a lazy, releasable reference to a result.
type ResultProxy interface {
	Result(ctx context.Context) (CachedResult, error)
	Release(ctx context.Context) error
	Definition() any
}

// SplitResultProxy creates two independent references to a ResultProxy.
// The underlying proxy is only released when both halves have been released.
func SplitResultProxy(res ResultProxy) (ResultProxy, ResultProxy) {
	sem := int64(0)
	return &splitResultProxy{ResultProxy: res, sem: &sem},
		&splitResultProxy{ResultProxy: res, sem: &sem}
}

type splitResultProxy struct {
	released int64
	sem      *int64
	ResultProxy
}

func (r *splitResultProxy) Release(ctx context.Context) error {
	if atomic.AddInt64(&r.released, 1) > 1 {
		return fmt.Errorf("releasing already released result proxy")
	}
	if atomic.AddInt64(r.sem, 1) == 2 {
		return r.ResultProxy.Release(ctx)
	}
	return nil
}

// ─── CacheManager interface ──────────────────────────────────────────────────
// This is the full CacheManager contract matching BuildKit's types.go.

// CacheManager provides query, load, and save operations for the solve cache.
type CacheManager interface {
	// ID returns a unique identifier for this cache manager.
	ID() string

	// Query looks up cache keys given dependency inputs and an op digest.
	// This is the core of BuildKit's cache matching algorithm.
	Query(deps []CacheKeyWithSelector, input Index, dgst digest.Digest, output Index) ([]*CacheKey, error)

	// Records returns all loadable cache records for a given key.
	Records(ctx context.Context, ck *CacheKey) ([]*CacheRecord, error)

	// Load loads a result from a cache record.
	Load(ctx context.Context, rec *CacheRecord) (Result, error)

	// Save persists a result under the given cache key.
	Save(key *CacheKey, result Result, createdAt time.Time) (*ExportableCacheKey, error)
}

// ResolverCache deduplicates concurrent Op resolutions for the same key.
// When multiple goroutines try to resolve the same vertex, only one actually
// does the work; others wait and receive the cached result.
type ResolverCache interface {
	// Lock acquires exclusive access for the given key. Returns previously
	// cached values. The release function must be called when done, passing
	// the new value (or nil if no value should be cached).
	Lock(key any) (values []any, release func(any) error, err error)
}
