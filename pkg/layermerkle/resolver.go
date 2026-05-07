package layermerkle

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"sync"
)

// ─────────────────────────────────────────────────────────────────────────────
// LayerFileResolver interface
// ─────────────────────────────────────────────────────────────────────────────

// LayerFileResolver determines which layer in a stack owns a given relative path.
// "Owns" means the file was added or modified in that layer's diff directory.
// All implementations must be safe for concurrent use.
type LayerFileResolver interface {
	// FindOwnerLayer walks the stack top-to-bottom and returns the ID of the
	// first layer whose diff contains relPath.
	// Returns ("", ErrWhiteout) when the file was deleted (whiteout entry).
	// Returns ("", ErrLayerNotFound) when no layer in the stack contains relPath.
	FindOwnerLayer(ctx context.Context, stack LayerStack, relPath string) (LayerID, error)

	// DiffAbsPath returns the absolute path to relPath within layerID's diff.
	// Returns ("", ErrLayerNotFound) when layerID is not registered.
	// Returns ("", ErrWhiteout) when the path is whiteout in that layer.
	DiffAbsPath(ctx context.Context, layerID LayerID, relPath string) (string, error)
}

// ─────────────────────────────────────────────────────────────────────────────
// OverlayResolver — filesystem-backed resolver for overlay diffs
// ─────────────────────────────────────────────────────────────────────────────

// OverlayResolver implements LayerFileResolver by performing filesystem Lstat
// calls on each layer's diff directory. It caches positive lookups to avoid
// repeated stat calls for the same (layer, path) pair.
//
// Cache key: (LayerID, relPath) → ExistenceResult
// This is separate from the hash cache to allow independent eviction policies.
type OverlayResolver struct {
	registry *LayerRegistry
	mu       sync.RWMutex
	statCache map[statKey]statResult
	cacheSize int
}

type statKey struct {
	layerID LayerID
	relPath string
}

// StatResult is the outcome of a filesystem stat check in a layer diff.
type StatResult int8

const (
	// StatUnknown means the result has not been computed yet.
	StatUnknown StatResult = 0
	// StatPresent means the file exists in this layer's diff.
	StatPresent StatResult = 1
	// StatAbsent means the file is not in this layer's diff.
	StatAbsent StatResult = 2
	// StatWhiteout means the file is masked by an overlay whiteout marker.
	StatWhiteout StatResult = 3
)

// statResult is a package-internal alias kept for backward compatibility
// within the package. External code should use StatResult.
type statResult = StatResult

const (
	statUnknown  = StatUnknown
	statPresent  = StatPresent
	statAbsent   = StatAbsent
	statWhiteout = StatWhiteout
)

// NewOverlayResolver creates a resolver backed by the given registry.
// cacheCapacity bounds the number of stat results cached in memory.
func NewOverlayResolver(registry *LayerRegistry, cacheCapacity int) *OverlayResolver {
	if cacheCapacity <= 0 {
		cacheCapacity = 100_000
	}
	return &OverlayResolver{
		registry:  registry,
		statCache: make(map[statKey]statResult, cacheCapacity),
		cacheSize: cacheCapacity,
	}
}

// FindOwnerLayer implements LayerFileResolver.
// Walks the stack top-to-bottom (newest to oldest) and returns the first layer
// whose diff contains relPath, respecting overlay whiteout semantics.
func (r *OverlayResolver) FindOwnerLayer(ctx context.Context, stack LayerStack, relPath string) (LayerID, error) {
	// Walk top-to-bottom — the most recently added layer is checked first.
	for i := len(stack) - 1; i >= 0; i-- {
		if ctx.Err() != nil {
			return "", ctx.Err()
		}
		layerID := stack[i]
		result, err := r.statInLayer(layerID, relPath)
		if err != nil {
			continue // layer not registered — skip
		}
		switch result {
		case statPresent:
			return layerID, nil
		case statWhiteout:
			return "", ErrWhiteout
		case statAbsent:
			continue
		}
	}
	return "", ErrLayerNotFound
}

// DiffAbsPath implements LayerFileResolver.
func (r *OverlayResolver) DiffAbsPath(_ context.Context, layerID LayerID, relPath string) (string, error) {
	info, ok := r.registry.Get(layerID)
	if !ok {
		return "", ErrLayerNotFound
	}
	result, err := r.statInLayer(layerID, relPath)
	if err != nil {
		return "", err
	}
	if result == statWhiteout {
		return "", ErrWhiteout
	}
	if result != statPresent {
		return "", ErrLayerNotFound
	}
	return info.DiffFilePath(relPath), nil
}

// statInLayer checks whether relPath is present, absent, or whiteout in layerID.
func (r *OverlayResolver) statInLayer(layerID LayerID, relPath string) (statResult, error) {
	k := statKey{layerID: layerID, relPath: relPath}

	r.mu.RLock()
	if result, ok := r.statCache[k]; ok {
		r.mu.RUnlock()
		return result, nil
	}
	r.mu.RUnlock()

	info, ok := r.registry.Get(layerID)
	if !ok {
		return statUnknown, ErrLayerNotFound
	}

	result := r.fsStat(info, relPath)

	r.mu.Lock()
	if len(r.statCache) < r.cacheSize {
		r.statCache[k] = result
	}
	r.mu.Unlock()

	return result, nil
}

// fsStat performs actual Lstat calls to determine file existence in a diff dir.
func (r *OverlayResolver) fsStat(info *LayerInfo, relPath string) statResult {
	// Check opaque whiteout first (directory was completely replaced).
	opaqueDir := filepath.Dir(filepath.FromSlash(relPath))
	if _, err := os.Lstat(filepath.Join(info.DiffPath, opaqueWhiteoutName(opaqueDir))); err == nil {
		return statWhiteout
	}

	// Check per-file whiteout.
	whiteout := whiteoutName(filepath.FromSlash(relPath))
	if _, err := os.Lstat(filepath.Join(info.DiffPath, whiteout)); err == nil {
		return statWhiteout
	}

	// Check the actual file.
	candidate := filepath.Join(info.DiffPath, filepath.FromSlash(relPath))
	if _, err := os.Lstat(candidate); err == nil {
		return statPresent
	}

	return statAbsent
}

// InvalidateLayer clears all stat cache entries for the given layer.
// Call after a layer's diff changes (e.g. when a snapshot is committed).
func (r *OverlayResolver) InvalidateLayer(layerID LayerID) {
	r.mu.Lock()
	for k := range r.statCache {
		if k.layerID == layerID {
			delete(r.statCache, k)
		}
	}
	r.mu.Unlock()
}

// ─────────────────────────────────────────────────────────────────────────────
// StaticResolver — resolver backed by a static file→layer mapping
// ─────────────────────────────────────────────────────────────────────────────

// StaticResolver is a LayerFileResolver backed by a pre-computed map.
// Useful in tests and for scenarios where the full layer ownership is known
// upfront (e.g. from a snapshot manifest).
type StaticResolver struct {
	// ownership maps relPath → ownerLayerID
	ownership map[string]LayerID

	// registry provides DiffAbsPath computation.
	registry *LayerRegistry
}

// NewStaticResolver creates a StaticResolver from a pre-computed ownership map.
func NewStaticResolver(ownership map[string]LayerID, registry *LayerRegistry) *StaticResolver {
	cp := make(map[string]LayerID, len(ownership))
	for k, v := range ownership {
		cp[normalizeRelPath(k)] = v
	}
	return &StaticResolver{ownership: cp, registry: registry}
}

// FindOwnerLayer implements LayerFileResolver.
func (s *StaticResolver) FindOwnerLayer(_ context.Context, _ LayerStack, relPath string) (LayerID, error) {
	id, ok := s.ownership[normalizeRelPath(relPath)]
	if !ok {
		return "", ErrLayerNotFound
	}
	return id, nil
}

// DiffAbsPath implements LayerFileResolver.
func (s *StaticResolver) DiffAbsPath(_ context.Context, layerID LayerID, relPath string) (string, error) {
	info, ok := s.registry.Get(layerID)
	if !ok {
		return "", ErrLayerNotFound
	}
	return info.DiffFilePath(relPath), nil
}

// normalizeRelPath cleans and forward-slashes a relative path.
func normalizeRelPath(p string) string {
	p = filepath.ToSlash(filepath.Clean(p))
	return strings.TrimPrefix(p, "/")
}
