package snapshot

import (
	"context"
	"fmt"
	"sync"

	fanwatch "github.com/bons/bons-ci/pkg/fswatch"
)

// ─────────────────────────────────────────────────────────────────────────────
// ContainerdEnricher — transformer that resolves overlay info per event
// ─────────────────────────────────────────────────────────────────────────────

// ContainerdEnricher is a [fanwatch.Transformer] that resolves the overlay
// layer structure for each event using containerd snapshot metadata.
//
// It maintains an in-memory cache of merged-dir → OverlayInfo to avoid
// repeated mountinfo scans for the same container. Cache entries are populated
// on first access and are valid for the lifetime of the enricher.
//
// Use [NewContainerdEnricher] to construct.
type ContainerdEnricher struct {
	snapshotter *ContainerdSnapshotter

	mu    sync.Mutex
	cache map[string]*fanwatch.OverlayInfo // mergedDir → resolved info
}

// NewContainerdEnricher constructs a [ContainerdEnricher] backed by the given
// snapshotter. It can be used as a drop-in replacement for
// [fanwatch.NewOverlayEnricher] when the overlay structure is not known ahead
// of time.
func NewContainerdEnricher(snapshotter *ContainerdSnapshotter) fanwatch.Transformer {
	return &ContainerdEnricher{
		snapshotter: snapshotter,
		cache:       make(map[string]*fanwatch.OverlayInfo),
	}
}

// Transform implements [fanwatch.Transformer].
//
// Determines the merged directory from the event path, resolves the overlay
// info (with caching), and sets e.Overlay and e.SourceLayer.
func (c *ContainerdEnricher) Transform(_ context.Context, e *fanwatch.EnrichedEvent) error {
	// Derive the merged dir: the event path is inside mergedDir, so we need
	// to find which mount it belongs to.
	//
	// Strategy: check the cache for any known mergedDir that is a prefix of
	// e.Path. On a miss, scan all running snapshot mounts.
	overlay, err := c.resolveOverlayForPath(e.Path)
	if err != nil {
		// Non-fatal — leave overlay fields nil for this event.
		return nil
	}
	if overlay == nil {
		return nil
	}

	relPath, err := overlay.RelPath(e.Path)
	if err != nil {
		return nil
	}

	e.Overlay = overlay
	e.SourceLayer = overlay.ResolveLayer(relPath)
	return nil
}

// resolveOverlayForPath finds the OverlayInfo whose MergedDir is a prefix of path.
func (c *ContainerdEnricher) resolveOverlayForPath(path string) (*fanwatch.OverlayInfo, error) {
	c.mu.Lock()
	// Fast path: check existing cache entries.
	for mergedDir, info := range c.cache {
		if info.ContainsPath(path) {
			c.mu.Unlock()
			return info, nil
		}
		_ = mergedDir
	}
	c.mu.Unlock()

	// Cache miss: enumerate all running containerd mounts and populate cache.
	mounts, err := c.snapshotter.AllRunningSnapshotMounts()
	if err != nil {
		return nil, fmt.Errorf("containerd enricher: enumerate mounts: %w", err)
	}

	c.mu.Lock()
	defer c.mu.Unlock()

	for _, m := range mounts {
		if _, exists := c.cache[m.MergedDir]; !exists {
			c.cache[m.MergedDir] = m.Overlay
		}
	}

	// Second lookup with populated cache.
	for _, info := range c.cache {
		if info.ContainsPath(path) {
			return info, nil
		}
	}
	return nil, nil
}

// InvalidateCache clears the merged-dir cache. Call this when containers are
// started or stopped so new mounts are discovered on the next event.
func (c *ContainerdEnricher) InvalidateCache() {
	c.mu.Lock()
	c.cache = make(map[string]*fanwatch.OverlayInfo)
	c.mu.Unlock()
}

// CachedMounts returns a snapshot of all currently cached overlay infos.
func (c *ContainerdEnricher) CachedMounts() []*fanwatch.OverlayInfo {
	c.mu.Lock()
	defer c.mu.Unlock()
	out := make([]*fanwatch.OverlayInfo, 0, len(c.cache))
	for _, v := range c.cache {
		out = append(out, v)
	}
	return out
}

// ─────────────────────────────────────────────────────────────────────────────
// SnapshotFilter — restrict events to a specific containerd snapshot
// ─────────────────────────────────────────────────────────────────────────────

// SnapshotIDFilter returns a [fanwatch.Filter] that passes only events whose
// overlay SnapshotLayer path contains the given snapshotID segment. This lets
// callers watch a single container's file accesses within a multi-container
// pipeline.
//
// Requires that [ContainerdEnricher] or [fanwatch.NewOverlayEnricher] has run
// before this filter is evaluated (e.Overlay must be non-nil).
func SnapshotIDFilter(snapshotID string) fanwatch.Filter {
	return fanwatch.FilterFunc(func(_ context.Context, e *fanwatch.EnrichedEvent) bool {
		if e.Overlay == nil {
			return false
		}
		// Check whether any layer path contains the snapshot ID segment.
		for _, layer := range e.Overlay.Layers {
			if containsSegment(layer.Path, snapshotID) {
				return true
			}
		}
		return false
	})
}

// MergedDirFilter returns a [fanwatch.Filter] that passes only events whose
// path falls inside the given merged directory. Use to restrict a multi-mount
// pipeline to a single container.
func MergedDirFilter(mergedDir string) fanwatch.Filter {
	return fanwatch.PathPrefixFilter(mergedDir)
}

// UpperDirWritesFilter passes only events for files that exist in an overlay
// upperdir — i.e. files created or modified by the container itself, not
// inherited read-only from image layers.
//
// Alias for [fanwatch.UpperDirOnlyFilter] provided here for discoverability.
func UpperDirWritesFilter() fanwatch.Filter {
	return fanwatch.UpperDirOnlyFilter()
}

// ImageLayerFilter passes only events for files that originate from read-only
// image layers (lowerdirs). These represent accesses to inherited image content
// with no container-specific modifications.
func ImageLayerFilter() fanwatch.Filter {
	return fanwatch.LowerDirOnlyFilter()
}

// containsSegment checks whether path contains segment as a path component.
func containsSegment(path, segment string) bool {
	// Use string search for the segment surrounded by path separators.
	if segment == "" {
		return false
	}
	if path == segment {
		return true
	}
	// Check /segment/ /segment (end) or segment/ (start).
	sep := "/"
	return containsStr(path, sep+segment+sep) ||
		hasSuffix(path, sep+segment) ||
		hasPrefix(path, segment+sep)
}

func containsStr(s, sub string) bool {
	if len(sub) > len(s) {
		return false
	}
	for i := 0; i <= len(s)-len(sub); i++ {
		if s[i:i+len(sub)] == sub {
			return true
		}
	}
	return false
}

func hasSuffix(s, suffix string) bool {
	return len(s) >= len(suffix) && s[len(s)-len(suffix):] == suffix
}

func hasPrefix(s, prefix string) bool {
	return len(s) >= len(prefix) && s[:len(prefix)] == prefix
}
