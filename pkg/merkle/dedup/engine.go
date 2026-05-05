package dedup

import (
	"context"
	"fmt"
	"time"

	"github.com/user/layermerkle/cache"
	"github.com/user/layermerkle/event"
	"github.com/user/layermerkle/hash"
	"github.com/user/layermerkle/internal/singleflight"
	"github.com/user/layermerkle/layer"
	"github.com/user/layermerkle/merkle"
)

// ─────────────────────────────────────────────────────────────────────────────
// Engine
// ─────────────────────────────────────────────────────────────────────────────

// Engine is the deduplication engine. It wires together the cache,
// HashProvider, and Merkle registry into the three-phase lookup algorithm.
//
// # Concurrency model
//
// Engine is fully goroutine-safe:
//   - ShardedCache uses per-shard RWMutex for all key operations.
//   - singleflight.Group ensures at most one HashProvider.Hash call is
//     in-flight per (filePath, outputLayerDigest) at any time.
//   - MerkleRegistry protects per-layer trees with their own RWMutex.
//
// # Tombstone-after-SetIfAbsent invariant (critical correctness)
//
// A concurrent DELETE for (path, outputLayer) may store a tombstone AFTER
// we find a hit in an ancestor but BEFORE our SetIfAbsent. entryToResult
// checks entry.Tombstone first so we never add a nil-hash leaf to Merkle.
//
// # Kind accuracy
//
// ResultKind accurately reflects what happened:
//   - ResultCacheMiss: we called HashProvider.Hash and were the inserter.
//   - ResultCacheHit:  we found the entry in the cache (any phase), or a
//     racing write/insert won SetIfAbsent.
//   - ResultTombstone: the file is deleted in this layer stack position.
//   - ResultError:     validation or HashProvider returned an error.
type Engine struct {
	cache    cache.Cache
	hp       hash.HashProvider
	registry *merkle.Registry
	sf       singleflight.Group
}

// NewEngine creates an Engine. Panics if any argument is nil.
func NewEngine(c cache.Cache, hp hash.HashProvider, registry *merkle.Registry) *Engine {
	if c == nil {
		panic("dedup: NewEngine: cache must not be nil")
	}
	if hp == nil {
		panic("dedup: NewEngine: HashProvider must not be nil")
	}
	if registry == nil {
		panic("dedup: NewEngine: Registry must not be nil")
	}
	return &Engine{cache: c, hp: hp, registry: registry}
}

// ─────────────────────────────────────────────────────────────────────────────
// Process
// ─────────────────────────────────────────────────────────────────────────────

// Process handles a single FileAccessEvent. Algorithm:
//
//  1. Validate.
//  2. Route: DELETE → tombstone; mutating → fresh hash; read → 3-phase lookup.
func (e *Engine) Process(ctx context.Context, ev *event.FileAccessEvent) Result {
	start := time.Now()

	if err := ev.Validate(); err != nil {
		return Result{
			Event:    ev,
			Kind:     ResultError,
			Error:    fmt.Errorf("dedup: invalid event: %w", err),
			Duration: time.Since(start),
		}
	}

	outputLayer, _ := ev.LayerStack.Top() // safe: Validate ensures non-empty stack

	switch {
	case ev.AccessType == event.AccessDelete:
		return e.handleDelete(ev, outputLayer, start)
	case ev.AccessType.IsMutating():
		return e.handleMutating(ctx, ev, outputLayer, start)
	default:
		return e.handleRead(ctx, ev, outputLayer, start)
	}
}

// ─────────────────────────────────────────────────────────────────────────────
// handleDelete
// ─────────────────────────────────────────────────────────────────────────────

func (e *Engine) handleDelete(
	ev *event.FileAccessEvent,
	outputLayer layer.Digest,
	start time.Time,
) Result {
	key := cache.CacheKey{FilePath: ev.FilePath, LayerDigest: outputLayer}
	e.cache.Set(key, cache.CacheEntry{ // Set overwrites — delete wins over any prior hash.
		Tombstone:   true,
		SourceLayer: outputLayer,
		CachedAt:    time.Now(),
	})
	e.registry.RecordDeletion(outputLayer, ev.FilePath)
	return Result{
		Event:       ev,
		Kind:        ResultTombstone,
		SourceLayer: outputLayer,
		Duration:    time.Since(start),
	}
}

// ─────────────────────────────────────────────────────────────────────────────
// handleMutating
// ─────────────────────────────────────────────────────────────────────────────

func (e *Engine) handleMutating(
	ctx context.Context,
	ev *event.FileAccessEvent,
	outputLayer layer.Digest,
	start time.Time,
) Result {
	key := cache.CacheKey{FilePath: ev.FilePath, LayerDigest: outputLayer}

	hr, err := e.hp.Hash(ctx, hash.HashRequest{
		FilePath:    ev.FilePath,
		LayerStack:  ev.LayerStack,
		OutputLayer: outputLayer,
	})
	if err != nil {
		return Result{
			Event:    ev,
			Kind:     ResultError,
			Error:    fmt.Errorf("dedup: hash failed for %q (mutating): %w", ev.FilePath, err),
			Duration: time.Since(start),
		}
	}

	e.cache.Set(key, cache.CacheEntry{ // Set overwrites — last write wins.
		Hash:        hr.Hash,
		Algorithm:   string(hr.Algorithm),
		SourceLayer: outputLayer,
		CachedAt:    time.Now(),
	})

	leafAdded := e.registry.AddLeafWithMeta(outputLayer, ev.FilePath, hr.Hash, string(hr.Algorithm))
	return Result{
		Event:           ev,
		Kind:            ResultCacheMiss, // always a fresh computation
		Hash:            hr.Hash,
		Algorithm:       string(hr.Algorithm),
		SourceLayer:     outputLayer,
		MerkleLeafAdded: leafAdded,
		Duration:        time.Since(start),
	}
}

// ─────────────────────────────────────────────────────────────────────────────
// handleRead — 3-phase lookup
// ─────────────────────────────────────────────────────────────────────────────

func (e *Engine) handleRead(
	ctx context.Context,
	ev *event.FileAccessEvent,
	outputLayer layer.Digest,
	start time.Time,
) Result {
	key := cache.CacheKey{FilePath: ev.FilePath, LayerDigest: outputLayer}

	// Phase 1 — output-layer cache check. O(1).
	if entry, ok := e.cache.Get(key); ok {
		return e.makeResult(ev, entry, outputLayer, ResultCacheHit, start)
	}

	// Phase 2 — ancestor walk (nearest → base). O(stack depth).
	ancestors, _ := ev.LayerStack.AncestorsOf(outputLayer)
	if found, res := e.walkAncestors(ev, outputLayer, key, ancestors, start); found {
		return res
	}

	// Phase 3 — compute via HashProvider (singleflight-deduplicated).
	return e.computeAndPromote(ctx, ev, outputLayer, key, start)
}

// ─────────────────────────────────────────────────────────────────────────────
// makeResult — canonical Result builder
// ─────────────────────────────────────────────────────────────────────────────

// makeResult converts a CacheEntry into a Result with the given kind hint.
// The kind hint is overridden to ResultTombstone when entry.Tombstone == true.
//
// This is the SINGLE point that checks for tombstones after any cache
// retrieval, including after SetIfAbsent races. It also conditionally adds
// the Merkle leaf, ensuring tombstones never produce leaves.
func (e *Engine) makeResult(
	ev *event.FileAccessEvent,
	entry cache.CacheEntry,
	outputLayer layer.Digest,
	kind ResultKind,
	start time.Time,
) Result {
	if entry.Tombstone {
		// A tombstone overrides whatever kind was inferred.
		return Result{
			Event:       ev,
			Kind:        ResultTombstone,
			SourceLayer: entry.SourceLayer,
			Duration:    time.Since(start),
		}
	}
	leafAdded := e.registry.AddLeafWithMeta(outputLayer, ev.FilePath, entry.Hash, entry.Algorithm)
	return Result{
		Event:           ev,
		Kind:            kind,
		Hash:            entry.Hash,
		Algorithm:       entry.Algorithm,
		SourceLayer:     entry.SourceLayer,
		MerkleLeafAdded: leafAdded,
		Duration:        time.Since(start),
	}
}

// ─────────────────────────────────────────────────────────────────────────────
// walkAncestors
// ─────────────────────────────────────────────────────────────────────────────

// walkAncestors scans ancestors from nearest-to-output down to base.
// On any cache hit, promotes to outputLayer via SetIfAbsent and returns.
// The returned entry from SetIfAbsent may itself be a tombstone (race);
// makeResult handles that correctly.
func (e *Engine) walkAncestors(
	ev *event.FileAccessEvent,
	outputLayer layer.Digest,
	outputKey cache.CacheKey,
	ancestors layer.Stack,
	start time.Time,
) (bool, Result) {
	for i := len(ancestors) - 1; i >= 0; i-- {
		ancKey := cache.CacheKey{FilePath: ev.FilePath, LayerDigest: ancestors[i]}
		entry, ok := e.cache.Get(ancKey)
		if !ok {
			continue
		}
		if entry.Tombstone {
			// File was whited-out in this ancestor; stop walking.
			return true, Result{
				Event:       ev,
				Kind:        ResultTombstone,
				SourceLayer: ancestors[i],
				Duration:    time.Since(start),
			}
		}

		// Promote to output layer; concurrent WRITE/DELETE wins if already stored.
		promoted := cache.CacheEntry{
			Hash:        entry.Hash,
			Algorithm:   entry.Algorithm,
			SourceLayer: entry.SourceLayer,
			CachedAt:    time.Now(),
		}
		actual, _ := e.cache.SetIfAbsent(outputKey, promoted)
		// makeResult handles the tombstone-after-SetIfAbsent race.
		return true, e.makeResult(ev, actual, outputLayer, ResultCacheHit, start)
	}
	return false, Result{}
}

// ─────────────────────────────────────────────────────────────────────────────
// computeAndPromote
// ─────────────────────────────────────────────────────────────────────────────

// computeAndPromote calls HashProvider.Hash under singleflight, stores with
// SetIfAbsent (respecting concurrent writes), and maps the outcome:
//   - inserted == true  → ResultCacheMiss (we computed the canonical hash)
//   - inserted == false → ResultCacheHit  (a concurrent event already stored)
//   - stored entry is tombstone → ResultTombstone (concurrent DELETE won)
func (e *Engine) computeAndPromote(
	ctx context.Context,
	ev *event.FileAccessEvent,
	outputLayer layer.Digest,
	outputKey cache.CacheKey,
	start time.Time,
) Result {
	// Zero-byte separator prevents key ambiguity: ("ab","c") != ("a","bc").
	sfKey := string(outputLayer) + "\x00" + ev.FilePath

	type sfResult struct{ hr hash.HashResult }

	v, err, _ := e.sf.Do(sfKey, func() (any, error) {
		hr, err := e.hp.Hash(ctx, hash.HashRequest{
			FilePath:    ev.FilePath,
			LayerStack:  ev.LayerStack,
			OutputLayer: outputLayer,
		})
		if err != nil {
			return nil, err
		}
		return sfResult{hr: hr}, nil
	})
	if err != nil {
		return Result{
			Event:    ev,
			Kind:     ResultError,
			Error:    fmt.Errorf("dedup: hash failed for %q: %w", ev.FilePath, err),
			Duration: time.Since(start),
		}
	}

	sr := v.(sfResult)
	newEntry := cache.CacheEntry{
		Hash:        sr.hr.Hash,
		Algorithm:   string(sr.hr.Algorithm),
		SourceLayer: outputLayer,
		CachedAt:    time.Now(),
	}

	// SetIfAbsent: concurrent WRITE or DELETE may have stored an entry first.
	actual, inserted := e.cache.SetIfAbsent(outputKey, newEntry)

	// Determine the kind based on who won the SetIfAbsent race.
	// makeResult overrides to ResultTombstone if actual.Tombstone == true.
	kind := ResultCacheHit
	if inserted {
		kind = ResultCacheMiss
	}
	return e.makeResult(ev, actual, outputLayer, kind, start)
}
