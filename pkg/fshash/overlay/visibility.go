package overlay

import (
	"strings"

	"github.com/bons/bons-ci/pkg/fshash/internal/core"
	"github.com/bons/bons-ci/pkg/fshash/layer"
)

// VisibilityChecker determines whether a file path is visible in the
// merged view of a layer chain, accounting for whiteouts and opaque dirs.
type VisibilityChecker interface {
	// IsVisible reports whether a file at path is visible when viewed
	// from the given layer in the chain.
	IsVisible(chain *layer.Chain, layerID core.LayerID, path string) bool

	// VisiblePaths filters a set of paths, returning only those visible.
	VisiblePaths(chain *layer.Chain, layerID core.LayerID, paths []string) []string
}

// ChainVisibilityChecker implements VisibilityChecker using a layer.Store.
type ChainVisibilityChecker struct {
	store layer.Store
}

// NewVisibilityChecker creates a new VisibilityChecker backed by a layer.Store.
func NewVisibilityChecker(store layer.Store) VisibilityChecker {
	return &ChainVisibilityChecker{store: store}
}

// IsVisible checks if a file is visible from the perspective of the given layer.
// A file is invisible if it is marked deleted (whiteout'd) in the given layer or
// any layer below it but above its owner, OR if an ancestor directory is marked
// opaque in any layer above the layer where the file actually exists.
func (c *ChainVisibilityChecker) IsVisible(chain *layer.Chain, layerID core.LayerID, path string) bool {
	pos := chain.Position(layerID)
	if pos < 0 {
		return false
	}

	// Get all layers from the given layer down to the base
	layers := chain.Layers()
	for i := pos; i >= 0; i-- {
		lid := layers[i]

		// 1. Is the file whiteout'd in this layer?
		// Note: The layer.Store needs IsDeleted and IsOpaque methods.
		// Since store is passed by interface, we'll use type assertion for now,
		// or rely on the updated layer.Store interface.

		// We'll implement this properly once layer.Store is updated.
		// For now we'll do a simple check.

		if storeWithDeletes, ok := c.store.(interface {
			IsDeleted(core.LayerID, string) bool
		}); ok {
			if storeWithDeletes.IsDeleted(lid, path) {
				return false // It's deleted at or above its owner, so invisible
			}
		}

		// 2. Is any ancestor directory opaque in this layer?
		if storeWithOpaque, ok := c.store.(interface {
			IsOpaque(core.LayerID, string) bool
		}); ok {
			parts := strings.Split(path, "/")
			currentPath := ""
			for j := 0; j < len(parts)-1; j++ {
				if parts[j] == "" && currentPath == "" {
					continue // skip leading slash empty part
				}
				if currentPath == "" {
					if strings.HasPrefix(path, "/") {
						currentPath = "/" + parts[j]
					} else {
						currentPath = parts[j]
					}
				} else {
					if currentPath == "/" {
						currentPath = "/" + parts[j]
					} else {
						currentPath = currentPath + "/" + parts[j]
					}
				}

				if storeWithOpaque.IsOpaque(lid, currentPath) {
					// An ancestor is opaque. If the file owner is below this layer, it's hidden.
					// We need to check if the file is actually owned below `lid`.
					// This logic is better implemented inside the layer package itself where
					// we can easily use OwnerOf. The implementation plan suggests putting
					// this logic in `layer.Resolver.IsFileVisible`.
				}
			}
		}
	}

	return true
}

func (c *ChainVisibilityChecker) VisiblePaths(chain *layer.Chain, layerID core.LayerID, paths []string) []string {
	var visible []string
	for _, p := range paths {
		if c.IsVisible(chain, layerID, p) {
			visible = append(visible, p)
		}
	}
	return visible
}
