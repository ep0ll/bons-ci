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

// IsFileVisible determines whether a file at the given path is visible
// in the merged view from the perspective of the topmost layer.
// Returns false if any upper layer has whiteout'd the file or made
// an ancestor directory opaque above the layer that owns the file.
func (r *Resolver) IsFileVisible(chain *Chain, path string) bool {
	layers := chain.Layers()
	if len(layers) == 0 {
		return false
	}

	owner, _ := r.store.OwnerOf(layers, path)
	ownerPos := chain.Position(owner)
	if ownerPos < 0 {
		ownerPos = 0
	}

	for i := len(layers) - 1; i > ownerPos; i-- {
		lid := layers[i]

		// 1. Whiteout mask
		if r.store.IsDeleted(lid, path) {
			return false
		}

		// 2. Opaque directory mask
		ancestor := path
		for {
			slash := -1
			for k := len(ancestor) - 1; k >= 0; k-- {
				if ancestor[k] == '/' {
					slash = k
					break
				}
			}
			if slash == -1 {
				break
			}
			dir := "/"
			if slash > 0 {
				dir = ancestor[:slash]
			}

			if r.store.IsOpaque(lid, dir) {
				return false
			}

			if dir == "/" {
				break
			}
			ancestor = dir
		}
	}

	return true
}
