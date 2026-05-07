package layermerkle

import (
	"fmt"
	"path/filepath"
	"strings"
	"sync"
)

// ─────────────────────────────────────────────────────────────────────────────
// LayerInfo — metadata for a registered layer
// ─────────────────────────────────────────────────────────────────────────────

// LayerInfo describes a single layer in the overlay stack.
// Layers are registered with the engine before events arrive.
type LayerInfo struct {
	// ID is the content-addressable digest of this layer.
	ID LayerID

	// DiffPath is the absolute filesystem path to this layer's diff directory
	// (the "fs/" directory in containerd overlay snapshots, or "diff/" in Docker).
	// The engine uses this to check whether a file was modified in this layer.
	DiffPath string

	// ChainID is the optional content-addressable chain digest (sha256 of this
	// layer's DiffID concatenated with the parent's ChainID). Enables cache
	// sharing across machines when chain IDs are content-stable.
	ChainID string

	// Labels carries arbitrary metadata (image reference, snapshot kind, etc.).
	Labels map[string]string
}

// DiffFilePath returns the absolute path to relPath within this layer's diff.
func (l *LayerInfo) DiffFilePath(relPath string) string {
	return filepath.Join(l.DiffPath, filepath.FromSlash(relPath))
}

// whiteoutName returns the overlay whiteout filename for relPath.
func whiteoutName(relPath string) string {
	dir, name := filepath.Split(relPath)
	return filepath.Join(dir, ".wh."+name)
}

// opaqueWhiteoutName returns the opaque whiteout directory marker.
func opaqueWhiteoutName(dir string) string {
	return filepath.Join(dir, ".wh..wh..opq")
}

// ─────────────────────────────────────────────────────────────────────────────
// LayerRegistry — thread-safe layer catalogue
// ─────────────────────────────────────────────────────────────────────────────

// LayerRegistry is a thread-safe catalogue of known layers.
// Callers register layers before submitting events; the deduplication engine
// queries the registry when resolving owner layers.
type LayerRegistry struct {
	mu     sync.RWMutex
	layers map[LayerID]*LayerInfo
}

// NewLayerRegistry returns an empty LayerRegistry.
func NewLayerRegistry() *LayerRegistry {
	return &LayerRegistry{
		layers: make(map[LayerID]*LayerInfo),
	}
}

// Register adds or replaces a layer. Safe for concurrent use.
func (r *LayerRegistry) Register(info *LayerInfo) error {
	if info == nil || info.ID == "" {
		return fmt.Errorf("layermerkle: Register: info.ID must not be empty")
	}
	if info.DiffPath == "" {
		return fmt.Errorf("layermerkle: Register: layer %s: DiffPath must not be empty", info.ID)
	}
	r.mu.Lock()
	r.layers[info.ID] = info
	r.mu.Unlock()
	return nil
}

// Unregister removes a layer from the registry. No-op when absent.
func (r *LayerRegistry) Unregister(id LayerID) {
	r.mu.Lock()
	delete(r.layers, id)
	r.mu.Unlock()
}

// Get returns the LayerInfo for id, or (nil, false) when not registered.
func (r *LayerRegistry) Get(id LayerID) (*LayerInfo, bool) {
	r.mu.RLock()
	l, ok := r.layers[id]
	r.mu.RUnlock()
	return l, ok
}

// ResolveStack returns the LayerInfo slice for the given stack in order.
// Layers not registered in the registry are skipped with an error logged to errs.
func (r *LayerRegistry) ResolveStack(stack LayerStack) ([]*LayerInfo, []error) {
	infos := make([]*LayerInfo, 0, len(stack))
	var errs []error
	r.mu.RLock()
	for _, id := range stack {
		l, ok := r.layers[id]
		if !ok {
			errs = append(errs, fmt.Errorf("%w: %s", ErrLayerNotFound, id))
			continue
		}
		infos = append(infos, l)
	}
	r.mu.RUnlock()
	return infos, errs
}

// Len returns the number of registered layers.
func (r *LayerRegistry) Len() int {
	r.mu.RLock()
	n := len(r.layers)
	r.mu.RUnlock()
	return n
}

// ─────────────────────────────────────────────────────────────────────────────
// LayerStackKey — hashable key for a complete layer stack
// ─────────────────────────────────────────────────────────────────────────────

// LayerStackKey is a stable, comparable string encoding of a LayerStack.
// Used as a map key for stack-level caches.
type LayerStackKey string

// NewLayerStackKey encodes the given stack into a comparable key.
// The encoding is deterministic and encodes the full ordering.
func NewLayerStackKey(stack LayerStack) LayerStackKey {
	parts := make([]string, len(stack))
	for i, id := range stack {
		parts[i] = string(id)
	}
	return LayerStackKey(strings.Join(parts, "|"))
}
