package fswatch

import (
	"fmt"
	"os"
	"path/filepath"
	"strings"
)

// ─────────────────────────────────────────────────────────────────────────────
// OverlayInfo — describes a single overlay mount
// ─────────────────────────────────────────────────────────────────────────────

// OverlayInfo describes the structure of a Linux overlay filesystem mount.
// In container runtimes (Docker, containerd, BuildKit) each running container
// or build snapshot has exactly one overlay mount composed of:
//
//   - One or more read-only lowerdirs forming the image layer stack.
//   - One writable upperdir accumulating container mutations.
//   - One workdir used by the kernel for atomic operations.
//   - One merged directory presenting the unified view.
//
// fswatch watches only the merged directory via fanotify and enriches events
// with the layer information recorded in this struct.
type OverlayInfo struct {
	// MergedDir is the path watched by fanotify.
	MergedDir string

	// UpperDir is the writable layer receiving mutations from the container.
	// Empty for read-only snapshots (image layers, view snapshots).
	UpperDir string

	// WorkDir is the overlay workdir (used internally by the kernel).
	// Empty for read-only snapshots.
	WorkDir string

	// LowerDirs is the ordered stack of read-only layers, outermost first.
	// LowerDirs[0] is the topmost read-only layer; LowerDirs[len-1] is the base.
	LowerDirs []string

	// Layers is the structured representation of each layer.
	// Populated automatically by [NewOverlayInfo] and [OverlayInfoFromMount].
	Layers []SnapshotLayer

	// ID is an optional caller-supplied identifier (e.g. snapshot ID, container ID).
	ID string

	// Labels holds arbitrary key-value metadata (e.g. image name, container name).
	Labels map[string]string
}

// SnapshotLayer describes a single layer within an overlay filesystem stack.
type SnapshotLayer struct {
	// Index is the position in the layer stack (0 = upperdir or topmost lower).
	Index int

	// Path is the absolute filesystem path of this layer directory.
	Path string

	// IsUpper reports true for the writable upperdir layer.
	IsUpper bool

	// ChainID is an optional content-addressable identifier for this layer.
	ChainID string
}

// AllDirs returns every directory that forms the overlay mount in priority order:
// upper first, then each lower from topmost to base.
func (o *OverlayInfo) AllDirs() []string {
	dirs := make([]string, 0, 1+len(o.LowerDirs))
	if o.UpperDir != "" {
		dirs = append(dirs, o.UpperDir)
	}
	dirs = append(dirs, o.LowerDirs...)
	return dirs
}

// ContainsPath reports whether absPath is inside the merged directory.
func (o *OverlayInfo) ContainsPath(absPath string) bool {
	rel, err := filepath.Rel(o.MergedDir, absPath)
	return err == nil && !strings.HasPrefix(rel, "..")
}

// ResolveLayer returns the first [SnapshotLayer] that contains relPath.
// It checks the upperdir first, then lowerdirs in priority order (topmost first).
// Returns nil when the path does not exist in any known layer.
func (o *OverlayInfo) ResolveLayer(relPath string) *SnapshotLayer {
	for i := range o.Layers {
		layer := &o.Layers[i]
		candidate := filepath.Join(layer.Path, filepath.FromSlash(relPath))
		if _, err := os.Lstat(candidate); err == nil {
			return layer
		}
	}
	return nil
}

// RelPath converts an absolute path within the merged directory to a
// forward-slash relative path. Returns an error when absPath is not inside MergedDir.
func (o *OverlayInfo) RelPath(absPath string) (string, error) {
	rel, err := filepath.Rel(o.MergedDir, absPath)
	if err != nil {
		return "", fmt.Errorf("overlay: path %q not under merged dir %q: %w",
			absPath, o.MergedDir, err)
	}
	if strings.HasPrefix(rel, "..") {
		return "", fmt.Errorf("overlay: path %q escapes merged dir %q", absPath, o.MergedDir)
	}
	return filepath.ToSlash(rel), nil
}

// ─────────────────────────────────────────────────────────────────────────────
// NewOverlayInfo — programmatic construction (all platforms)
// ─────────────────────────────────────────────────────────────────────────────

// NewOverlayInfo constructs an [OverlayInfo] from explicit directory paths.
// Use when the overlay metadata is already known (e.g. from Snapshotter.Mounts).
func NewOverlayInfo(mergedDir, upperDir, workDir string, lowerDirs []string) *OverlayInfo {
	dirs := make([]string, len(lowerDirs))
	for i, d := range lowerDirs {
		dirs[i] = filepath.Clean(d)
	}

	o := &OverlayInfo{
		MergedDir: filepath.Clean(mergedDir),
		UpperDir:  cleanPath(upperDir),
		WorkDir:   cleanPath(workDir),
		LowerDirs: dirs,
		Labels:    make(map[string]string),
	}
	o.buildLayers()
	return o
}

// buildLayers populates Layers from UpperDir and LowerDirs in priority order.
func (o *OverlayInfo) buildLayers() {
	idx := 0
	if o.UpperDir != "" {
		o.Layers = append(o.Layers, SnapshotLayer{
			Index:   idx,
			Path:    o.UpperDir,
			IsUpper: true,
		})
		idx++
	}
	for _, d := range o.LowerDirs {
		o.Layers = append(o.Layers, SnapshotLayer{
			Index:   idx,
			Path:    d,
			IsUpper: false,
		})
		idx++
	}
}

// ─────────────────────────────────────────────────────────────────────────────
// Shared helpers (all platforms)
// ─────────────────────────────────────────────────────────────────────────────

// cleanPath applies filepath.Clean but preserves empty strings.
// filepath.Clean("") returns "." which is wrong for absent upperdir/workdir.
func cleanPath(p string) string {
	if p == "" {
		return ""
	}
	if c := filepath.Clean(p); c != "." {
		return c
	}
	return ""
}

// parseKVOptions parses a comma-separated "key=value" string into a map.
// Handles key-only fields (empty string value) and trims surrounding spaces.
// Used by overlay_linux.go and overlay_parser.go to decode VFSOptions.
func parseKVOptions(opts string) map[string]string {
	m := make(map[string]string)
	for _, field := range strings.Split(opts, ",") {
		kv := strings.SplitN(field, "=", 2)
		switch len(kv) {
		case 2:
			m[strings.TrimSpace(kv[0])] = strings.TrimSpace(kv[1])
		case 1:
			if key := strings.TrimSpace(kv[0]); key != "" {
				m[key] = ""
			}
		}
	}
	return m
}

// overlayInfoFromVFSOptions builds an [OverlayInfo] from a VFSOptions string
// and a mount point. This helper is shared between the Linux moby path and the
// cross-platform pure-Go parser so the conversion logic is never duplicated.
func overlayInfoFromVFSOptions(vfsOptions, mergedDir string) (*OverlayInfo, error) {
	opts := parseKVOptions(vfsOptions)

	lower := opts["lowerdir"]
	if lower == "" {
		return nil, fmt.Errorf("overlay: mount at %q has no lowerdir in options %q",
			mergedDir, vfsOptions)
	}

	lowerDirs := strings.Split(lower, ":")
	for i, d := range lowerDirs {
		lowerDirs[i] = filepath.Clean(d)
	}

	return NewOverlayInfo(
		mergedDir,
		opts["upperdir"],
		opts["workdir"],
		lowerDirs,
	), nil
}
