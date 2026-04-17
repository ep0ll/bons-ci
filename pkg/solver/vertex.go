// Package solver implements a production-grade DAG solver with leaf-first
// traversal, reactive scheduling, and pluggable caching. It walks upward from
// leaf vertices to parent vertices, short-circuiting on cache hits and reusing
// partial parent caches.
//
// The design is inspired by BuildKit's solver but improves upon it with a
// concurrent worker pool, priority-based scheduling, decomposed state
// management, and streaming transformation support.
package solver

import (
	"context"
	"fmt"
	"sync"
	"time"

	digest "github.com/opencontainers/go-digest"
)

// Vertex is a node in the build DAG. Each vertex represents an operation
// whose result can be cached. Vertices are content-addressable via their
// digest, which incorporates all inputs.
type Vertex interface {
	// Digest returns a content-addressable checksum of this vertex and all
	// its transitive inputs. Two vertices with the same digest are
	// semantically equivalent.
	Digest() digest.Digest

	// Inputs returns the edges this vertex depends on. Each edge references
	// a specific output index of a parent vertex. A vertex with zero inputs
	// is a root (source) vertex.
	Inputs() []Edge

	// Name returns a human-readable label for progress reporting.
	Name() string

	// Sys returns the opaque operation descriptor used to resolve an Op
	// implementation. In an LLB-like system this would be the operation
	// protobuf.
	Sys() any

	// Options returns metadata that does not affect the vertex digest
	// but controls solver behavior (cache sources, ignore-cache, etc).
	Options() VertexOptions
}

// Index is a zero-based index into the result array of an operation.
type Index int

// Edge connects a consumer vertex to a specific output of a producer vertex.
// Edges are the directed connections in the DAG.
type Edge struct {
	Index  Index
	Vertex Vertex
}

// String returns a debug-friendly representation.
func (e Edge) String() string {
	if e.Vertex == nil {
		return "<nil edge>"
	}
	return fmt.Sprintf("%s[%d]", e.Vertex.Name(), e.Index)
}

// VertexOptions carry metadata associated with a vertex that does not change
// its digest. These options influence caching and scheduling behavior.
type VertexOptions struct {
	// IgnoreCache forces re-execution even if a cache entry exists.
	IgnoreCache bool

	// CacheSources provides additional cache backends to probe for this vertex.
	CacheSources []CacheSource

	// Description holds arbitrary key-value metadata for display.
	Description map[string]string

	// ExportCache, when non-nil, controls whether this vertex's result
	// should be included in cache exports.
	ExportCache *bool

	// EstimatedCost is a hint for the scheduler. Higher values indicate
	// more expensive operations, biasing the scheduler toward earlier
	// execution of costly paths.
	EstimatedCost float64
}

// CacheSource is a pluggable cache backend that can provide cached results
// for vertices.
type CacheSource interface {
	// Probe checks if a cached result exists for the given cache key.
	// Returns the result and true if found, nil and false otherwise.
	Probe(ctx context.Context, key *CacheKey) (Result, bool, error)
}

// Result is the abstract output of solving a vertex. Results are reference-
// counted; callers must Release when done.
type Result interface {
	// ID returns a unique identifier for this result.
	ID() string

	// Release frees resources associated with this result. Must be called
	// exactly once when the result is no longer needed.
	Release(ctx context.Context) error

	// Sys returns the underlying system object (e.g., a snapshot reference).
	Sys() any

	// Clone creates a new reference to the same underlying data.
	Clone() Result
}

// CachedResult is a Result associated with the cache keys that produced it.
type CachedResult interface {
	Result
	CacheKeys() []ExportableCacheKey
}

// ExportableCacheKey pairs a CacheKey with an optional exporter for cache
// export chains.
type ExportableCacheKey struct {
	*CacheKey
	Exporter CacheExporter
}

// CacheExporter can serialize cache chains for remote storage.
type CacheExporter interface {
	ExportTo(ctx context.Context, target CacheExportTarget) error
}

// CacheExportTarget receives exported cache records.
type CacheExportTarget interface {
	Add(dgst digest.Digest, result Result) error
}

// CacheKey is a content-addressable identifier for a cached result.
// It incorporates the vertex's operation digest and the cache keys of
// all its dependencies.
type CacheKey struct {
	mu sync.RWMutex

	// id is the computed string identifier.
	id string

	// digest is the operation-level cache map digest.
	digest digest.Digest

	// vtx is the LLB digest of the vertex that produced this key.
	vtx digest.Digest

	// output is the output index within the vertex.
	output Index

	// deps holds cache keys from dependencies, one slice per input.
	deps [][]CacheKeyWithSelector
}

// NewCacheKey creates a CacheKey for a specific vertex output.
func NewCacheKey(dgst, vtx digest.Digest, output Index) *CacheKey {
	return &CacheKey{
		id:     rootKeyDigest(dgst, output).String(),
		digest: dgst,
		vtx:    vtx,
		output: output,
	}
}

// ID returns the string identifier of this cache key.
func (ck *CacheKey) ID() string {
	ck.mu.RLock()
	defer ck.mu.RUnlock()
	return ck.id
}

// Digest returns the operation digest component.
func (ck *CacheKey) Digest() digest.Digest {
	ck.mu.RLock()
	defer ck.mu.RUnlock()
	return ck.digest
}

// Output returns the output index.
func (ck *CacheKey) Output() Index {
	return ck.output
}

// Deps returns a snapshot of the dependency cache keys.
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

// SetDeps atomically sets the dependency cache keys.
func (ck *CacheKey) SetDeps(deps [][]CacheKeyWithSelector) {
	ck.mu.Lock()
	defer ck.mu.Unlock()
	ck.deps = deps
}

// CacheKeyWithSelector pairs a cache key with an optional selector digest
// to narrow matching scope.
type CacheKeyWithSelector struct {
	Selector digest.Digest
	CacheKey ExportableCacheKey
}

// CacheMap describes how to compute the cache key for a vertex's operation.
type CacheMap struct {
	// Digest is the operation-level digest (e.g., image manifest digest,
	// git commit SHA).
	Digest digest.Digest

	// Deps contains per-input cache configuration.
	Deps []CacheMapDep

	// Opts are opaque options passed to cache load calls.
	Opts map[string]any
}

// CacheMapDep configures caching behavior for a single dependency.
type CacheMapDep struct {
	// Selector narrows the cache key matching for this input.
	Selector digest.Digest

	// ComputeDigestFunc computes a content-based cache key from the
	// dependency's result (e.g., file content checksums).
	ComputeDigestFunc func(ctx context.Context, result Result) (digest.Digest, error)

	// PreprocessFunc runs on an input before it is passed to the op.
	PreprocessFunc func(ctx context.Context, result Result) error
}

// Op defines the execution contract for a vertex operation. The solver
// resolves an Op from a vertex via the configured ResolveOpFunc.
type Op interface {
	// CacheMap returns the cache description for this operation.
	// The index parameter supports operations that expose multiple
	// cache maps (currently only roots may return more than one).
	CacheMap(ctx context.Context, index int) (*CacheMap, bool, error)

	// Exec executes the operation given results from dependencies.
	Exec(ctx context.Context, inputs []Result) (outputs []Result, err error)

	// Acquire acquires resources needed for execution (semaphore slots,
	// file handles, etc). The returned release function must be called
	// after Exec completes.
	Acquire(ctx context.Context) (release func(), err error)
}

// ResolveOpFunc converts a vertex's Sys() value into an executable Op.
type ResolveOpFunc func(vtx Vertex) (Op, error)

// CacheRecord identifies a loadable cache entry.
type CacheRecord struct {
	ID        string
	Size      int
	CreatedAt time.Time
	Priority  int
	Key       *CacheKey
}

// cachedResult is the internal implementation of CachedResult.
type cachedResult struct {
	Result
	keys []ExportableCacheKey
}

func (cr *cachedResult) CacheKeys() []ExportableCacheKey {
	return cr.keys
}

// NewCachedResult wraps a Result with cache keys.
func NewCachedResult(r Result, keys []ExportableCacheKey) CachedResult {
	return &cachedResult{Result: r, keys: keys}
}

// rootKeyDigest computes a deterministic digest from operation digest and
// output index.
func rootKeyDigest(dgst digest.Digest, output Index) digest.Digest {
	return digest.FromString(fmt.Sprintf("%s@%d", dgst, output))
}
