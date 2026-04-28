package fshash

import (
	"context"

	"github.com/bons/bons-ci/pkg/fshash/internal/core"
)

// Hooks provides lifecycle callbacks for observability and extensibility.
// All hook functions are optional — nil hooks are safely skipped.
type Hooks struct {
	OnAccessDeduped   func(ctx context.Context, result core.DeduplicationResult)
	OnHashComputed    func(ctx context.Context, hash core.FileHash)
	OnTreeBuilt       func(ctx context.Context, layerID core.LayerID, root []byte)
	OnLayerRegistered func(ctx context.Context, layerID core.LayerID)
	OnCacheHit        func(ctx context.Context, layerID core.LayerID, path string)
	OnCacheMiss       func(ctx context.Context, layerID core.LayerID, path string)
}

func (h *Hooks) fireAccessDeduped(ctx context.Context, result core.DeduplicationResult) {
	if h.OnAccessDeduped != nil {
		h.OnAccessDeduped(ctx, result)
	}
}

func (h *Hooks) fireHashComputed(ctx context.Context, hash core.FileHash) {
	if h.OnHashComputed != nil {
		h.OnHashComputed(ctx, hash)
	}
}

func (h *Hooks) fireTreeBuilt(ctx context.Context, layerID core.LayerID, root []byte) {
	if h.OnTreeBuilt != nil {
		h.OnTreeBuilt(ctx, layerID, root)
	}
}

func (h *Hooks) fireLayerRegistered(ctx context.Context, layerID core.LayerID) {
	if h.OnLayerRegistered != nil {
		h.OnLayerRegistered(ctx, layerID)
	}
}

func (h *Hooks) fireCacheHit(ctx context.Context, layerID core.LayerID, path string) {
	if h.OnCacheHit != nil {
		h.OnCacheHit(ctx, layerID, path)
	}
}

func (h *Hooks) fireCacheMiss(ctx context.Context, layerID core.LayerID, path string) {
	if h.OnCacheMiss != nil {
		h.OnCacheMiss(ctx, layerID, path)
	}
}
