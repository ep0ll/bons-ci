//go:build linux

// Package dedup is the cross-overlayfs deduplication engine.
//
// Deduplication layers
// ────────────────────
//
//	L1  In-process singleflight:   concurrent goroutines racing on the same key
//	     are coalesced into one computation; all waiters share the result.
//	L2  In-process LRU cache:     subsequent calls for the same key within one
//	     process lifetime return immediately without touching singleflight.
//	L3  Persistent hashdb:        cross-process, cross-invocation dedup keyed
//	     by (mount_id, file_handle, mtime_ns, size). A file in lowerdir L is
//	     hashed at most once per content version even across engine restarts.
//
// Key insight: unix.NameToHandleAt(fd, "", AT_EMPTY_PATH) bypasses overlayfs
// and returns the LOWER filesystem's (mount_id, file_handle). Two different
// mergedviews of the same lowerdir file produce an identical filekey.Key, so
// all three cache layers de-duplicate across views automatically.
package dedup

import (
	"context"
	"fmt"
	"time"

	"golang.org/x/sync/singleflight"

	"github.com/bons/bons-ci/plugins/checksummer/pkg/cache"
	"github.com/bons/bons-ci/plugins/checksummer/pkg/filekey"
	"github.com/bons/bons-ci/plugins/checksummer/pkg/hashdb"
	"github.com/bons/bons-ci/plugins/checksummer/pkg/hooks"
	"github.com/bons/bons-ci/plugins/checksummer/pkg/metrics"
)

// HashFunc is the computation backend called when all cache layers miss.
// Receives the fully-resolved key; must return a 32-byte BLAKE3 digest.
type HashFunc func(ctx context.Context, key filekey.Key) ([]byte, error)

// StatFunc returns the mtime_ns and size for the file identified by key.
// Used to validate hashdb entries before returning them.
// nil means "skip persistent cache validation".
type StatFunc func(key filekey.Key) (mtimeNs, size int64, err error)

// Result is the outcome of a deduplicated hash computation.
type Result struct {
	Hash    []byte
	Key     filekey.Key
	Cached  bool // L2/L3 cache hit – no computation
	Shared  bool // L1 singleflight – result shared with other callers
	Elapsed time.Duration
}

// CacheStats is re-exported so engine callers don't import cache directly.
type CacheStats = cache.Stats

// ─────────────────────────── Engine ──────────────────────────────────────────

// Engine provides three-layer deduplicated hash computation.
// Safe for concurrent use.
type Engine struct {
	group   singleflight.Group
	cache   *cache.Cache
	db      *hashdb.DB // optional; nil disables L3 persistent cache
	hooks   *hooks.HookSet
	metrics *metrics.Recorder
}

// Option configures an Engine.
type Option func(*Engine)

func WithCache(c *cache.Cache) Option        { return func(e *Engine) { e.cache = c } }
func WithHashDB(db *hashdb.DB) Option        { return func(e *Engine) { e.db = db } }
func WithHooks(hs *hooks.HookSet) Option     { return func(e *Engine) { e.hooks = hs } }
func WithMetrics(m *metrics.Recorder) Option { return func(e *Engine) { e.metrics = m } }

// New creates an Engine.
func New(opts ...Option) *Engine {
	e := &Engine{
		cache:   cache.New(),
		hooks:   hooks.NewHookSet(),
		metrics: &metrics.Recorder{},
	}
	for _, o := range opts {
		o(e)
	}
	return e
}

// Compute returns the hash for key using up to three cache layers.
//
// statFn is called to validate the L3 persistent cache entry. Pass nil to
// skip L3 lookup (e.g. when the file stat is unavailable or too expensive).
//
// Flow:
//  1. L2: in-process LRU cache → immediate return.
//  2. L1: singleflight.Do(sfKey) → coalesce concurrent callers.
//     Inside singleflight:
//     2a. Re-check L2 (another goroutine may have just populated it).
//     2b. L3: check persistent hashdb (if statFn != nil and db != nil).
//     2c. Call hashFn, store in L2 + L3.
func (e *Engine) Compute(
	ctx context.Context,
	key filekey.Key,
	hashFn HashFunc,
	statFn StatFunc,
) (Result, error) {
	start := time.Now()
	// Compute the singleflight/cache key once – it is used in every branch.
	sfKey := key.SFKey()
	keyStr := key.String() // for hooks (avoid repeated allocation)

	// ── L2: in-process LRU cache ──────────────────────────────────────────
	if hash, ok := e.cache.Get(sfKey); ok {
		e.metrics.CacheHits.Inc()
		e.metrics.HashesDeduped.Inc()
		res := Result{Hash: hash, Key: key, Cached: true, Elapsed: time.Since(start)}
		e.fireHooksCacheHit(ctx, keyStr, hash)
		return res, nil
	}

	e.metrics.CacheMisses.Inc()
	_ = e.hooks.OnCacheMiss.Execute(ctx,
		hooks.CachePayload{Key: keyStr}, hooks.ContinueOnError)
	_ = e.hooks.PreHash.Execute(ctx,
		hooks.HashPayload{Key: keyStr}, hooks.ContinueOnError)

	// ── L1: singleflight coalescing ───────────────────────────────────────
	type sfResult struct {
		hash []byte
	}

	v, err, shared := e.group.Do(sfKey, func() (any, error) {
		// 2a. Re-check L2 inside singleflight.
		if hash, ok := e.cache.Get(sfKey); ok {
			e.metrics.CacheHits.Inc()
			return sfResult{hash: hash}, nil
		}

		// 2b. L3: persistent hashdb.
		if hash, ok := e.checkPersistent(key, statFn); ok {
			e.cache.Set(sfKey, hash) // promote to L2
			e.metrics.CacheHits.Inc()
			return sfResult{hash: hash}, nil
		}

		// 2c. Compute.
		hash, err := hashFn(ctx, key)
		if err != nil {
			e.metrics.HashErrors.Inc()
			return nil, fmt.Errorf("dedup compute key=%s: %w", key, err)
		}

		e.cache.Set(sfKey, hash)
		e.storePersistent(key, statFn, hash)
		e.metrics.HashesComputed.Inc()
		return sfResult{hash: hash}, nil
	})

	if err != nil {
		_ = e.hooks.OnError.Execute(ctx,
			hooks.ErrorPayload{Op: "compute", Key: keyStr, Err: err},
			hooks.ContinueOnError)
		return Result{}, err
	}

	sr := v.(sfResult)
	if shared {
		e.metrics.HashesDeduped.Inc()
	}
	res := Result{
		Hash:    sr.hash,
		Key:     key,
		Shared:  shared,
		Elapsed: time.Since(start),
	}

	_ = e.hooks.PostHash.Execute(ctx, hooks.HashPayload{
		Key:     keyStr,
		Hash:    res.Hash,
		Deduped: shared,
	}, hooks.ContinueOnError)
	e.metrics.HashLatency.Record(res.Elapsed)
	return res, nil
}

// checkPersistent looks up the hashdb, validating with statFn.
func (e *Engine) checkPersistent(key filekey.Key, statFn StatFunc) ([]byte, bool) {
	if e.db == nil || statFn == nil {
		return nil, false
	}
	mtimeNs, size, err := statFn(key)
	if err != nil {
		return nil, false
	}
	return e.db.Get(key, mtimeNs, size)
}

// storePersistent writes the hash to the hashdb, fetching stat via statFn.
func (e *Engine) storePersistent(key filekey.Key, statFn StatFunc, hash []byte) {
	if e.db == nil || statFn == nil {
		return
	}
	mtimeNs, size, err := statFn(key)
	if err != nil {
		return
	}
	e.db.Put(key, mtimeNs, size, hash)
}

func (e *Engine) fireHooksCacheHit(ctx context.Context, keyStr string, hash []byte) {
	_ = e.hooks.OnCacheHit.Execute(ctx,
		hooks.CachePayload{Key: keyStr, Hit: true, Hash: hash},
		hooks.ContinueOnError)
	_ = e.hooks.OnDedup.Execute(ctx,
		hooks.HashPayload{Key: keyStr, Hash: hash, Cached: true},
		hooks.ContinueOnError)
}

// ─────────────────────────── Lifecycle ───────────────────────────────────────

// Invalidate removes key from all in-process caches and forces recomputation.
// The persistent hashdb entry is NOT removed (it will be re-validated on next
// access via StatFunc mtime/size check).
func (e *Engine) Invalidate(key filekey.Key) {
	sfKey := key.SFKey()
	e.cache.Delete(sfKey)
	e.group.Forget(sfKey)
}

// InvalidateAll clears in-process caches. Does not touch the persistent db.
func (e *Engine) InvalidateAll() {
	e.group = singleflight.Group{}
	e.cache = cache.New()
}

// Flush writes all pending hashdb writes to disk.
func (e *Engine) Flush() error {
	if e.db == nil {
		return nil
	}
	return e.db.Flush()
}

// CacheStats returns cache statistics.
func (e *Engine) CacheStats() CacheStats { return e.cache.Stats() }

// MetricsSnapshot returns a point-in-time metrics snapshot.
func (e *Engine) MetricsSnapshot() metrics.Snapshot { return e.metrics.Snapshot() }

// Hooks returns the engine's HookSet.
func (e *Engine) Hooks() *hooks.HookSet { return e.hooks }
