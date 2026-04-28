package layer

import "github.com/bons/bons-ci/pkg/fshash/internal/core"

// Resolver determines whether a file needs rehashing by examining the
// layer chain for modifications above the cached hash's source layer.
type Resolver struct {
	store Store
}

// NewResolver creates a Resolver backed by the given layer store.
func NewResolver(store Store) *Resolver {
	return &Resolver{store: store}
}

// NeedsRehash checks whether a file needs re-hashing for the current
// layer given a cached hash from cachedLayerID. Returns true if any
// layer above cachedLayerID has modified the file.
func (r *Resolver) NeedsRehash(chain *Chain, path string, cachedLayerID core.LayerID) bool {
	cachedPos := chain.Position(cachedLayerID)
	if cachedPos < 0 {
		return true
	}
	above := chain.Above(cachedLayerID)
	for _, layerID := range above {
		if r.store.IsModified(layerID, path) {
			return true
		}
	}
	return false
}

// ResolveOwner finds which layer in the chain owns the file at path.
func (r *Resolver) ResolveOwner(chain *Chain, path string) (core.LayerID, bool) {
	return r.store.OwnerOf(chain.Layers(), path)
}
