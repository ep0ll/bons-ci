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
		id:     rootKeyDigest(dgst, output).String(),
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
		return rootKeyDigest(ck.digest, ck.output).String()
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

// rootKeyDigest computes a deterministic digest from an op digest and output
// index. Used for root (no-input) vertices.
func rootKeyDigest(dgst digest.Digest, output Index) digest.Digest {
	return digest.FromString(fmt.Sprintf("%s@%d", dgst, output))
}
