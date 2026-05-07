package layermerkle

import (
	"context"
	"fmt"
)

// ─────────────────────────────────────────────────────────────────────────────
// CacheWarmer — pre-populates the hash cache from a prior run's provenance
// ─────────────────────────────────────────────────────────────────────────────

// CacheWarmer reads an OCIProvenance record and populates a HashCache with the
// recorded file hashes. This enables a "warm start" where the engine skips
// re-hashing files that were unchanged since the last build.
//
// Warming is best-effort: unknown layerIDs in the provenance are skipped
// without error since layers may have been evicted between builds.
type CacheWarmer struct {
	cache    HashCache
	registry *LayerRegistry
}

// NewCacheWarmer creates a CacheWarmer that populates cache using layer
// information from registry.
func NewCacheWarmer(cache HashCache, registry *LayerRegistry) *CacheWarmer {
	return &CacheWarmer{cache: cache, registry: registry}
}

// WarmResult summarises a Warm call.
type WarmResult struct {
	// Warmed is the number of (layerID, relPath) entries loaded into the cache.
	Warmed int
	// Skipped is the number of entries skipped (unknown layer or bad digest).
	Skipped int
	// Vertices is the number of vertices processed.
	Vertices int
}

// Warm reads the provenance record and populates the cache.
// ctx is checked between vertices so warming can be cancelled.
func (w *CacheWarmer) Warm(ctx context.Context, p *OCIProvenance) (WarmResult, error) {
	var result WarmResult
	for _, vtx := range p.Vertices {
		if ctx.Err() != nil {
			return result, ctx.Err()
		}
		result.Vertices++
		for _, f := range vtx.Files {
			if f.OwnerLayer == "" || f.ContentDigest == "" {
				result.Skipped++
				continue
			}
			layerID := LayerID(f.OwnerLayer)
			if _, ok := w.registry.Get(layerID); !ok {
				result.Skipped++
				continue
			}
			hash := FileHash(f.ContentDigest)
			w.cache.Set(layerID, f.RelPath, hash)
			result.Warmed++
		}
	}
	return result, nil
}

// WarmFromForest populates the cache from a MerkleForest directly, bypassing
// serialization. Useful within the same process after the first build run.
func (w *CacheWarmer) WarmFromForest(ctx context.Context, forest *MerkleForest) (WarmResult, error) {
	var result WarmResult
	for _, tree := range forest.All() {
		if ctx.Err() != nil {
			return result, ctx.Err()
		}
		result.Vertices++
		for _, leaf := range tree.Leaves {
			if leaf.OwnerLayerID == "" || leaf.Hash == "" {
				result.Skipped++
				continue
			}
			if _, ok := w.registry.Get(leaf.OwnerLayerID); !ok {
				result.Skipped++
				continue
			}
			w.cache.Set(leaf.OwnerLayerID, leaf.RelPath, leaf.Hash)
			result.Warmed++
		}
	}
	return result, nil
}

// ─────────────────────────────────────────────────────────────────────────────
// EngineWithWarmStart — Engine factory that warms the cache before starting
// ─────────────────────────────────────────────────────────────────────────────

// WarmStartConfig configures a warm-start Engine.
type WarmStartConfig struct {
	// Provenance is the prior build's provenance record. If nil, no warming.
	Provenance *OCIProvenance

	// Forest is the prior build's MerkleForest. Used when Provenance is nil.
	Forest *MerkleForest

	// EngineOptions are the engine construction options.
	EngineOptions []EngineOption
}

// NewEngineWithWarmStart constructs an Engine and pre-warms the hash cache from
// a prior provenance record or forest before the engine starts accepting events.
func NewEngineWithWarmStart(ctx context.Context, cfg WarmStartConfig) (*Engine, WarmResult, error) {
	engine := NewEngine(cfg.EngineOptions...)
	warmer := NewCacheWarmer(engine.cache, engine.registry)

	var result WarmResult
	var err error

	switch {
	case cfg.Provenance != nil:
		result, err = warmer.Warm(ctx, cfg.Provenance)
	case cfg.Forest != nil:
		result, err = warmer.WarmFromForest(ctx, cfg.Forest)
	}

	if err != nil {
		return nil, result, fmt.Errorf("layermerkle: warm start: %w", err)
	}

	return engine, result, nil
}
