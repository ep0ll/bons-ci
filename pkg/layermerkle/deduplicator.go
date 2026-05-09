package layermerkle

import (
	"context"
	"errors"
	"fmt"
	"sync"

	"github.com/bons/bons-ci/pkg/layermerkle/internal/digest"
	"github.com/bons/bons-ci/pkg/layermerkle/internal/singleflight"
)

// ─────────────────────────────────────────────────────────────────────────────
// DeduplicationEngine — resolves file hashes across a stacked layer set
// ─────────────────────────────────────────────────────────────────────────────

// DeduplicationEngine is the core of the package. For each HashRequest it:
//
//  1. Checks the HashCache for (topLayer, relPath) — O(1) fast path.
//  2. Uses a singleflight.Group to coalesce concurrent identical requests.
//  3. Calls LayerFileResolver to find the owner layer in the stack.
//  4. Checks the HashCache for (ownerLayer, relPath) — second fast path.
//  5. Falls back to the FileHasher only when both cache lookups miss.
//  6. Populates the cache for both the owner layer and the requesting layer.
//
// The engine processes requests concurrently up to the configured worker count.
// All methods are safe for concurrent use.
type DeduplicationEngine struct {
	cache    HashCache
	hasher   FileHasher
	resolver LayerFileResolver
	group    singleflight.Group
	hooks    *HookChain
}

// NewDeduplicationEngine constructs the engine from its dependencies.
// All arguments are required; use NoopHashCache, NewSHA256Hasher, or
// NewOverlayResolver as defaults.
func NewDeduplicationEngine(
	cache HashCache,
	hasher FileHasher,
	resolver LayerFileResolver,
	hooks *HookChain,
) *DeduplicationEngine {
	if hooks == nil {
		hooks = &HookChain{}
	}
	return &DeduplicationEngine{
		cache:    cache,
		hasher:   hasher,
		resolver: resolver,
		hooks:    hooks,
	}
}

// Resolve returns the FileHash for the file at req.RelPath in the given stack.
// It is the primary public method of the engine.
func (e *DeduplicationEngine) Resolve(ctx context.Context, req HashRequest) *HashResult {
	if !req.LayerStack.Valid() {
		return &HashResult{
			VertexID: req.VertexID,
			RelPath:  req.RelPath,
			Err:      fmt.Errorf("%w: empty stack for %q", ErrInvalidLayerStack, req.RelPath),
		}
	}

	topLayer := req.LayerStack.Top()

	// Fast path 1: (topLayer, relPath) is already cached.
	if h, ok := e.cache.Get(topLayer, req.RelPath); ok {
		result := &HashResult{
			VertexID:     req.VertexID,
			RelPath:      req.RelPath,
			Hash:         h,
			OwnerLayerID: topLayer,
			FromCache:    true,
		}
		e.hooks.OnCacheHit(ctx, req, result)
		return result
	}

	// Coalesce concurrent requests for the same (stackKey, relPath).
	sfKey := singleflightKey(req.LayerStack, req.RelPath)
	v, err, _ := e.group.Do(sfKey, func() (any, error) {
		return e.resolve(ctx, req)
	})
	if err != nil {
		return &HashResult{VertexID: req.VertexID, RelPath: req.RelPath, Err: err}
	}
	return v.(*HashResult)
}

// resolve is the slow path: resolves owner layer and hashes the file.
func (e *DeduplicationEngine) resolve(ctx context.Context, req HashRequest) (*HashResult, error) {
	// Find the owner layer — the one whose diff contains the file.
	ownerLayer, err := e.resolver.FindOwnerLayer(ctx, req.LayerStack, req.RelPath)
	if err != nil {
		if errors.Is(err, ErrWhiteout) {
			return nil, ErrWhiteout
		}
		// File not found in any layer — may have been deleted before hashing.
		return nil, fmt.Errorf("resolve owner for %q: %w", req.RelPath, err)
	}

	// Fast path 2: (ownerLayer, relPath) may already be cached from a prior ExecOp.
	if h, ok := e.cache.Get(ownerLayer, req.RelPath); ok {
		result := &HashResult{
			VertexID:     req.VertexID,
			RelPath:      req.RelPath,
			Hash:         h,
			OwnerLayerID: ownerLayer,
			FromCache:    true,
		}
		// Propagate to topLayer cache to accelerate future lookups.
		topLayer := req.LayerStack.Top()
		if topLayer != ownerLayer {
			e.cache.Set(topLayer, req.RelPath, h)
		}
		e.hooks.OnCacheHit(context.WithoutCancel(ctx), req, result)
		return result, nil
	}

	// Slow path: compute the hash.
	absPath, err := e.resolver.DiffAbsPath(ctx, ownerLayer, req.RelPath)
	if err != nil {
		return nil, fmt.Errorf("diff abs path %q in %s: %w", req.RelPath, ownerLayer, err)
	}

	e.hooks.OnHashStart(ctx, req)
	h, err := e.hasher.Hash(ctx, absPath)
	if err != nil {
		e.hooks.OnHashError(ctx, req, err)
		return nil, fmt.Errorf("hash %q: %w", absPath, err)
	}
	e.hooks.OnHashComplete(ctx, req, h)

	// Populate cache for both owner layer and top layer.
	e.cache.Set(ownerLayer, req.RelPath, h)
	topLayer := req.LayerStack.Top()
	if topLayer != ownerLayer {
		e.cache.Set(topLayer, req.RelPath, h)
	}

	return &HashResult{
		VertexID:     req.VertexID,
		RelPath:      req.RelPath,
		Hash:         h,
		OwnerLayerID: ownerLayer,
		FromCache:    false,
	}, nil
}

// singleflightKey produces a stable string key for a (layerStack, relPath) pair.
func singleflightKey(stack LayerStack, relPath string) string {
	return string(NewLayerStackKey(stack)) + "\x00" + relPath
}

// ─────────────────────────────────────────────────────────────────────────────
// VertexProcessor — manages one MerkleTreeBuilder per active vertex
// ─────────────────────────────────────────────────────────────────────────────

// VertexProcessor tracks in-flight vertex builds and routes HashResults to
// the correct MerkleTreeBuilder.
type VertexProcessor struct {
	engine *DeduplicationEngine
	forest *MerkleForest
	onTree func(*MerkleTree)

	mu       sync.Mutex
	builders map[VertexID]*MerkleTreeBuilder
}

// NewVertexProcessor returns a VertexProcessor backed by the given engine.
// onTree is called each time a vertex is finalized.
func NewVertexProcessor(engine *DeduplicationEngine, forest *MerkleForest, onTree func(*MerkleTree)) *VertexProcessor {
	return &VertexProcessor{
		engine:   engine,
		forest:   forest,
		onTree:   onTree,
		builders: make(map[VertexID]*MerkleTreeBuilder),
	}
}

// Process handles one AccessEvent end-to-end:
// resolves the hash, updates the builder, returns the HashResult.
func (vp *VertexProcessor) Process(ctx context.Context, ev *AccessEvent) *HashResult {
	req := HashRequest{
		VertexID:   ev.VertexID,
		LayerStack: ev.LayerStack,
		RelPath:    ev.RelPath,
		AbsPath:    ev.AbsPath,
	}

	result := vp.engine.Resolve(ctx, req)
	if result.Err != nil {
		return result
	}

	b := vp.getOrCreateBuilder(ev.VertexID, ev.LayerStack)
	b.Add(result)
	return result
}

// FinalizeVertex seals the MerkleTreeBuilder for vertexID and stores the tree.
// Safe to call concurrently for different vertex IDs.
func (vp *VertexProcessor) FinalizeVertex(vertexID VertexID) (*MerkleTree, error) {
	vp.mu.Lock()
	b, ok := vp.builders[vertexID]
	if !ok {
		vp.mu.Unlock()
		return nil, fmt.Errorf("%w: %s", ErrVertexClosed, vertexID)
	}
	delete(vp.builders, vertexID)
	vp.mu.Unlock()

	tree := b.Finalize()
	vp.forest.Add(tree)
	if vp.onTree != nil {
		vp.onTree(tree)
	}
	return tree, nil
}

// ActiveVertexIDs returns the IDs of all in-flight vertices.
func (vp *VertexProcessor) ActiveVertexIDs() []digest.Digest {
	vp.mu.Lock()
	ids := make([]digest.Digest, 0, len(vp.builders))
	for id := range vp.builders {
		ids = append(ids, id)
	}
	vp.mu.Unlock()
	return ids
}

// getOrCreateBuilder returns the builder for vertexID, creating one if needed.
func (vp *VertexProcessor) getOrCreateBuilder(vertexID VertexID, stack LayerStack) *MerkleTreeBuilder {
	vp.mu.Lock()
	b, ok := vp.builders[vertexID]
	if !ok {
		b = NewMerkleTreeBuilder(vertexID, stack)
		vp.builders[vertexID] = b
	}
	vp.mu.Unlock()
	return b
}
