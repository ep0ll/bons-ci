package fanwatch

import (
	"bufio"
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
// fanwatch watches only the merged directory via fanotify and enriches events
// with the layer information recorded in this struct.
type OverlayInfo struct {
	// MergedDir is the path watched by fanotify.
	MergedDir string

	// UpperDir is the writable layer receiving mutations from the container.
	UpperDir string

	// WorkDir is the overlay workdir (used internally by the kernel).
	WorkDir string

	// LowerDirs is the ordered stack of read-only layers, outermost first.
	// LowerDirs[0] is the topmost read-only layer; LowerDirs[len-1] is the base.
	LowerDirs []string

	// Layers is the structured representation of each lower layer.
	// Populated when constructed via [OverlayInfoFromMount] or [NewOverlayInfo].
	Layers []SnapshotLayer

	// ID is an optional caller-supplied identifier (e.g. snapshot ID, container ID).
	ID string

	// Labels holds arbitrary key-value metadata (e.g. image name, container name).
	Labels map[string]string
}

// SnapshotLayer describes a single layer within an overlay filesystem stack.
type SnapshotLayer struct {
	// Index is the position in the layer stack (0 = topmost read-only layer).
	Index int

	// Path is the absolute filesystem path of this layer.
	Path string

	// IsUpper reports true for the writable upperdir layer.
	IsUpper bool

	// ChainID is an optional content-addressable identifier for this layer.
	ChainID string
}

// AllDirs returns every directory that forms the overlay mount in priority order:
// upper, then each lower from topmost to base.
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
// forward-slash relative path. Returns an error if absPath is not inside MergedDir.
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
// OverlayInfoFromMount — parse from /proc/self/mountinfo
// ─────────────────────────────────────────────────────────────────────────────

// OverlayInfoFromMount finds and parses the overlay mount whose merged directory
// is mergedDir by scanning /proc/self/mountinfo.
//
// Returns [ErrMountNotFound] when no overlay mount matches mergedDir.
func OverlayInfoFromMount(mergedDir string) (*OverlayInfo, error) {
	return overlayInfoFromMountFile("/proc/self/mountinfo", mergedDir)
}

// overlayInfoFromMountFile is the testable inner implementation.
func overlayInfoFromMountFile(mountinfoPath, mergedDir string) (*OverlayInfo, error) {
	mergedDir = filepath.Clean(mergedDir)

	f, err := os.Open(mountinfoPath)
	if err != nil {
		return nil, fmt.Errorf("overlay: open %s: %w", mountinfoPath, err)
	}
	defer f.Close()

	scanner := bufio.NewScanner(f)
	for scanner.Scan() {
		line := scanner.Text()
		info, ok := parseMountinfoLine(line)
		if !ok || info.MergedDir != mergedDir {
			continue
		}
		info.buildLayers()
		return info, nil
	}
	if err := scanner.Err(); err != nil {
		return nil, fmt.Errorf("overlay: scan %s: %w", mountinfoPath, err)
	}
	return nil, fmt.Errorf("overlay: %w: %q", ErrMountNotFound, mergedDir)
}

// parseMountinfoLine parses a single line from /proc/self/mountinfo.
//
// Format:
//
//	mountID parentID major:minor root mountPoint mountOptions [optFields] - fsType source superOptions
//
// Example overlay line:
//
//	69 64 0:46 / /merged rw,... - overlay overlay rw,lowerdir=/l1:/l2,upperdir=/u,workdir=/w
func parseMountinfoLine(line string) (*OverlayInfo, bool) {
	// Split on " - " separator first to isolate the optional fields section.
	parts := strings.SplitN(line, " - ", 2)
	if len(parts) != 2 {
		return nil, false
	}

	leftFields := strings.Fields(parts[0])
	rightFields := strings.Fields(parts[1])

	// leftFields: mountID parentID major:minor root mountPoint mountOptions [optFields...]
	if len(leftFields) < 6 {
		return nil, false
	}
	// rightFields: fsType source superOptions
	if len(rightFields) < 3 {
		return nil, false
	}

	fsType := rightFields[0]
	if fsType != "overlay" {
		return nil, false
	}

	mountPoint := leftFields[4]
	superOptions := rightFields[2]

	opts := parseKeyValueOptions(superOptions)

	lower := opts["lowerdir"]
	upper := opts["upperdir"]
	work := opts["workdir"]

	if lower == "" {
		return nil, false
	}

	lowerDirs := strings.Split(lower, ":")
	for i, d := range lowerDirs {
		lowerDirs[i] = filepath.Clean(d)
	}

	return &OverlayInfo{
		MergedDir: filepath.Clean(mountPoint),
		UpperDir:  filepath.Clean(upper),
		WorkDir:   filepath.Clean(work),
		LowerDirs: lowerDirs,
		Labels:    make(map[string]string),
	}, true
}

// buildLayers populates the Layers field from UpperDir and LowerDirs.
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

// parseKeyValueOptions parses a comma-separated key=value (or key) option string.
func parseKeyValueOptions(opts string) map[string]string {
	result := make(map[string]string)
	for _, field := range strings.Split(opts, ",") {
		kv := strings.SplitN(field, "=", 2)
		if len(kv) == 2 {
			result[kv[0]] = kv[1]
		} else if len(kv) == 1 && kv[0] != "" {
			result[kv[0]] = ""
		}
	}
	return result
}

// ─────────────────────────────────────────────────────────────────────────────
// NewOverlayInfo — programmatic construction
// ─────────────────────────────────────────────────────────────────────────────

// NewOverlayInfo constructs an [OverlayInfo] from explicit directory paths.
// Use when the overlay metadata is known without needing to parse /proc/mounts.
// cleanPath applies filepath.Clean but returns "" for empty paths or ".".
func cleanPath(p string) string {
	if p == "" {
		return ""
	}
	c := filepath.Clean(p)
	if c == "." {
		return ""
	}
	return c
}

func NewOverlayInfo(mergedDir, upperDir, workDir string, lowerDirs []string) *OverlayInfo {
	dirs := make([]string, len(lowerDirs))
	copy(dirs, lowerDirs)

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
