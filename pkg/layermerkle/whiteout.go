package layermerkle

import (
	"context"
	"os"
	"path/filepath"
	"strings"
)

// ─────────────────────────────────────────────────────────────────────────────
// OCICompliantResolver — fully OCI-spec-compliant overlay resolver
// ─────────────────────────────────────────────────────────────────────────────

// OCICompliantResolver extends OverlayResolver with full OCI image layer
// whiteout compliance including recursive opaque whiteout detection.
//
// The OCI distribution spec defines two whiteout types:
//  1. Per-file:  .wh.<name>   — masks a specific file named <name>
//  2. Opaque:    .wh..wh..opq — masks the entire directory in lower layers
//
// The original OverlayResolver only checked the immediate parent directory
// for an opaque whiteout. This resolver walks the full ancestor chain so that
// a whiteout at e.g. /usr/.wh..wh..opq correctly masks /usr/lib/libssl.so.
type OCICompliantResolver struct {
	inner *OverlayResolver
}

// NewOCICompliantResolver creates a resolver with full OCI whiteout handling.
func NewOCICompliantResolver(registry *LayerRegistry, cacheCapacity int) *OCICompliantResolver {
	return &OCICompliantResolver{
		inner: NewOverlayResolver(registry, cacheCapacity),
	}
}

// FindOwnerLayer implements LayerFileResolver with recursive opaque whiteout
// checking. Walks the stack top-to-bottom and respects full ancestor chains.
func (r *OCICompliantResolver) FindOwnerLayer(
	ctx context.Context,
	stack LayerStack,
	relPath string,
) (LayerID, error) {
	for i := len(stack) - 1; i >= 0; i-- {
		if ctx.Err() != nil {
			return "", ctx.Err()
		}
		layerID := stack[i]
		info, ok := r.inner.registry.Get(layerID)
		if !ok {
			continue
		}

		// Check per-file whiteout in this layer.
		if r.hasFileWhiteout(info.DiffPath, relPath) {
			return "", ErrWhiteout
		}

		// Check opaque whiteout for every ancestor directory of relPath.
		if r.ancestorIsOpaque(info.DiffPath, relPath) {
			return "", ErrWhiteout
		}

		// Check whether the actual file exists in this layer's diff.
		candidate := filepath.Join(info.DiffPath, filepath.FromSlash(relPath))
		if _, err := os.Lstat(candidate); err == nil {
			return layerID, nil
		}
	}
	return "", ErrLayerNotFound
}

// DiffAbsPath implements LayerFileResolver.
func (r *OCICompliantResolver) DiffAbsPath(ctx context.Context, layerID LayerID, relPath string) (string, error) {
	return r.inner.DiffAbsPath(ctx, layerID, relPath)
}

// InvalidateLayer clears cached stat results for layerID.
func (r *OCICompliantResolver) InvalidateLayer(layerID LayerID) {
	r.inner.InvalidateLayer(layerID)
}

// hasFileWhiteout checks for a per-file whiteout (.wh.<name>) in diffPath.
func (r *OCICompliantResolver) hasFileWhiteout(diffPath, relPath string) bool {
	dir, name := filepath.Split(filepath.FromSlash(relPath))
	whiteoutPath := filepath.Join(diffPath, dir, ".wh."+name)
	_, err := os.Lstat(whiteoutPath)
	return err == nil
}

// ancestorIsOpaque checks whether any ancestor directory of relPath has an
// opaque whiteout marker in diffPath.
//
// For relPath "usr/lib/libssl.so", this checks:
//
//	<diffPath>/usr/.wh..wh..opq
//	<diffPath>/usr/lib/.wh..wh..opq
func (r *OCICompliantResolver) ancestorIsOpaque(diffPath, relPath string) bool {
	segments := strings.Split(filepath.ToSlash(relPath), "/")
	// Walk all ancestor directories (not including the file itself).
	for depth := 1; depth < len(segments); depth++ {
		ancestor := strings.Join(segments[:depth], "/")
		opaqueMarkerPath := filepath.Join(diffPath, filepath.FromSlash(ancestor), ".wh..wh..opq")
		if _, err := os.Lstat(opaqueMarkerPath); err == nil {
			return true
		}
	}
	return false
}

// ─────────────────────────────────────────────────────────────────────────────
// WhiteoutAwareLayerRegistry — Registry that indexes whiteout files at registration
// ─────────────────────────────────────────────────────────────────────────────

// WhiteoutIndex pre-scans a layer's diff directory and builds an in-memory
// set of whiteout paths. This avoids repeated Lstat calls for the same layer
// during resolution — trading registration time for lookup speed.
//
// Suitable for long-lived watchers over large image layers where the diff
// contents are stable after the container starts.
type WhiteoutIndex struct {
	// fileWhiteouts maps relPath → true for .wh.<name> entries.
	fileWhiteouts map[string]struct{}
	// opaqueDirectories maps dirRelPath → true for .wh..wh..opq entries.
	opaqueDirectories map[string]struct{}
}

// BuildWhiteoutIndex scans diffPath and indexes all whiteout entries.
// Returns an empty index (not nil) when no whiteouts are found.
func BuildWhiteoutIndex(diffPath string) (*WhiteoutIndex, error) {
	idx := &WhiteoutIndex{
		fileWhiteouts:    make(map[string]struct{}),
		opaqueDirectories: make(map[string]struct{}),
	}
	err := filepath.Walk(diffPath, func(path string, info os.FileInfo, err error) error {
		if err != nil {
			return nil // skip unreadable entries
		}
		name := filepath.Base(path)
		rel, relErr := filepath.Rel(diffPath, path)
		if relErr != nil {
			return nil
		}
		rel = filepath.ToSlash(rel)
		dir := filepath.ToSlash(filepath.Dir(rel))

		switch {
		case name == ".wh..wh..opq":
			idx.opaqueDirectories[dir] = struct{}{}
		case strings.HasPrefix(name, ".wh.") && !strings.HasPrefix(name, ".wh..wh."):
			orig := strings.TrimPrefix(name, ".wh.")
			maskedPath := dir + "/" + orig
			if dir == "." {
				maskedPath = orig
			}
			idx.fileWhiteouts[maskedPath] = struct{}{}
		}
		return nil
	})
	if err != nil && !os.IsNotExist(err) {
		return nil, err
	}
	return idx, nil
}

// IsMasked reports whether relPath is masked by a whiteout in this layer.
func (idx *WhiteoutIndex) IsMasked(relPath string) bool {
	// Per-file whiteout.
	if _, ok := idx.fileWhiteouts[relPath]; ok {
		return true
	}
	// Opaque whiteout in any ancestor directory.
	parts := strings.Split(relPath, "/")
	for depth := 1; depth < len(parts); depth++ {
		ancestor := strings.Join(parts[:depth], "/")
		if _, ok := idx.opaqueDirectories[ancestor]; ok {
			return true
		}
	}
	return false
}

// FileWhiteoutCount returns the number of per-file whiteouts indexed.
func (idx *WhiteoutIndex) FileWhiteoutCount() int { return len(idx.fileWhiteouts) }

// OpaqueDirectoryCount returns the number of opaque directories indexed.
func (idx *WhiteoutIndex) OpaqueDirectoryCount() int { return len(idx.opaqueDirectories) }
