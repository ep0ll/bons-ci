package snapshot

import (
	"context"
	"fmt"
	"strings"
	"sync"

	ctdmount "github.com/containerd/containerd/mount"
	"github.com/containerd/containerd/snapshots"

	fanwatch "github.com/bons/bons-ci/pkg/fswatch"
)

// ─────────────────────────────────────────────────────────────────────────────
// ContainerdEnricher — transformer using live containerd mount resolution
// ─────────────────────────────────────────────────────────────────────────────

// ContainerdEnricher is a [fanwatch.Transformer] that populates
// [fanwatch.EnrichedEvent.Overlay] and [fanwatch.EnrichedEvent.SourceLayer]
// by resolving each event's path against the live containerd overlay mounts.
//
// Resolution strategy:
//  1. Check an in-process cache of mergedDir → OverlayInfo (O(1) average).
//  2. On cache miss, call moby/sys/mountinfo.GetMounts(FSTypeFilter("overlay"))
//     to discover all live overlay mounts and populate the cache.
//  3. Optionally, fall back to a snapshots.Snapshotter.Mounts() call when
//     a Snapshotter is injected (useful before the container mount is live).
//
// The cache is refreshed on explicit [InvalidateCache] calls. In long-running
// watchers, call InvalidateCache whenever containers are started or stopped.
type ContainerdEnricher struct {
	// snapshotter is optional; used for pre-mount/post-unmount resolution.
	snapshotter snapshots.Snapshotter

	mu    sync.RWMutex
	cache map[string]*fanwatch.OverlayInfo // mergedDir → OverlayInfo
}

// NewContainerdEnricher constructs a [ContainerdEnricher].
//
// sn is optional: pass nil to use only live-mount resolution. When provided,
// sn.Mounts() is used as a fallback when the path cannot be matched to any
// known live overlay mount.
func NewContainerdEnricher(sn snapshots.Snapshotter) fanwatch.Transformer {
	return &ContainerdEnricher{
		snapshotter: sn,
		cache:       make(map[string]*fanwatch.OverlayInfo),
	}
}

// Transform implements [fanwatch.Transformer].
func (c *ContainerdEnricher) Transform(ctx context.Context, e *fanwatch.EnrichedEvent) error {
	overlay, err := c.resolveForPath(ctx, e.Path)
	if err != nil {
		// Non-fatal: leave overlay fields nil so downstream filters can decide.
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

// resolveForPath finds the OverlayInfo whose MergedDir contains path.
func (c *ContainerdEnricher) resolveForPath(ctx context.Context, path string) (*fanwatch.OverlayInfo, error) {
	// Fast path: check cache with read lock.
	c.mu.RLock()
	for _, info := range c.cache {
		if info.ContainsPath(path) {
			c.mu.RUnlock()
			return info, nil
		}
	}
	c.mu.RUnlock()

	// Slow path: enumerate live overlay mounts and repopulate the cache.
	mounts, err := AllOverlayMounts()
	if err != nil {
		return nil, fmt.Errorf("containerd enricher: enumerate overlay mounts: %w", err)
	}

	c.mu.Lock()
	for _, m := range mounts {
		if _, exists := c.cache[m.MergedDir]; !exists {
			c.cache[m.MergedDir] = m.Overlay
		}
	}
	c.mu.Unlock()

	// Second lookup with repopulated cache.
	c.mu.RLock()
	defer c.mu.RUnlock()
	for _, info := range c.cache {
		if info.ContainsPath(path) {
			return info, nil
		}
	}
	return nil, nil
}

// InvalidateCache clears the merged-dir → OverlayInfo cache. Call whenever
// containers are started or stopped so new mounts are discovered on next access.
func (c *ContainerdEnricher) InvalidateCache() {
	c.mu.Lock()
	c.cache = make(map[string]*fanwatch.OverlayInfo)
	c.mu.Unlock()
}

// CachedEntries returns a snapshot of the current cache contents.
func (c *ContainerdEnricher) CachedEntries() map[string]*fanwatch.OverlayInfo {
	c.mu.RLock()
	defer c.mu.RUnlock()
	out := make(map[string]*fanwatch.OverlayInfo, len(c.cache))
	for k, v := range c.cache {
		out[k] = v
	}
	return out
}

// ─────────────────────────────────────────────────────────────────────────────
// SnapshotterEnricher — enricher backed by snapshots.Snapshotter.Mounts()
// ─────────────────────────────────────────────────────────────────────────────

// SnapshotterEnricher is a [fanwatch.Transformer] that calls
// snapshots.Snapshotter.Mounts() for each event to resolve overlay structure.
//
// Unlike [ContainerdEnricher], this enricher uses the containerd Snapshotter
// API directly and does not parse /proc/self/mountinfo. It is useful when:
//   - The snapshotter is available but the overlay has not been mounted yet.
//   - You need to resolve layer structure for a specific named snapshot key.
//
// keyFn maps each event to a containerd snapshot key. Return "" to skip
// enrichment for that event.
type SnapshotterEnricher struct {
	snapshotter snapshots.Snapshotter
	mergedDir   string
	keyFn       func(*fanwatch.EnrichedEvent) string

	mu    sync.Mutex
	cache map[string]*fanwatch.OverlayInfo // snapshotKey → OverlayInfo
}

// NewSnapshotterEnricher constructs a [SnapshotterEnricher].
//
// sn is the containerd Snapshotter instance (e.g. from overlay.NewSnapshotter).
// mergedDir is the mount point used as MergedDir in the resulting OverlayInfo.
// keyFn maps each event to a snapshot key; return "" to skip that event.
func NewSnapshotterEnricher(
	sn snapshots.Snapshotter,
	mergedDir string,
	keyFn func(*fanwatch.EnrichedEvent) string,
) fanwatch.Transformer {
	return &SnapshotterEnricher{
		snapshotter: sn,
		mergedDir:   mergedDir,
		keyFn:       keyFn,
		cache:       make(map[string]*fanwatch.OverlayInfo),
	}
}

// Transform implements [fanwatch.Transformer].
func (s *SnapshotterEnricher) Transform(ctx context.Context, e *fanwatch.EnrichedEvent) error {
	key := s.keyFn(e)
	if key == "" {
		return nil
	}

	overlay, err := s.resolveForKey(ctx, key)
	if err != nil {
		return nil // non-fatal
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

func (s *SnapshotterEnricher) resolveForKey(ctx context.Context, key string) (*fanwatch.OverlayInfo, error) {
	s.mu.Lock()
	if info, ok := s.cache[key]; ok {
		s.mu.Unlock()
		return info, nil
	}
	s.mu.Unlock()

	info, err := OverlayInfoFromSnapshotter(ctx, s.snapshotter, key, s.mergedDir)
	if err != nil {
		return nil, err
	}

	s.mu.Lock()
	s.cache[key] = info
	s.mu.Unlock()
	return info, nil
}

// ─────────────────────────────────────────────────────────────────────────────
// ContainerdMountEnricher — one-shot enricher for a known set of Mounts
// ─────────────────────────────────────────────────────────────────────────────

// MountEnricher is a [fanwatch.Transformer] that resolves overlay structure
// from a pre-computed []mount.Mount slice (as returned by Snapshotter.Mounts).
//
// This is the simplest enricher: construct it once for each container/snapshot,
// register it in the pipeline, and it resolves every event path.
type MountEnricher struct {
	overlay *fanwatch.OverlayInfo
}

// NewMountEnricher constructs a [MountEnricher] from a []mount.Mount slice
// (as returned by snapshots.Snapshotter.Mounts or snapshots.Snapshotter.View).
//
// mergedDir is the mount point for the overlay.
func NewMountEnricher(mounts []ctdmount.Mount, mergedDir string) (fanwatch.Transformer, error) {
	info, err := overlayInfoFromMounts(mounts, mergedDir)
	if err != nil {
		return nil, fmt.Errorf("snapshot: build mount enricher: %w", err)
	}
	return &MountEnricher{overlay: info}, nil
}

// Transform implements [fanwatch.Transformer].
func (m *MountEnricher) Transform(_ context.Context, e *fanwatch.EnrichedEvent) error {
	relPath, err := m.overlay.RelPath(e.Path)
	if err != nil {
		return nil // path not inside this overlay
	}
	e.Overlay = m.overlay
	e.SourceLayer = m.overlay.ResolveLayer(relPath)
	return nil
}

// Overlay returns the resolved OverlayInfo for inspection.
func (m *MountEnricher) Overlay() *fanwatch.OverlayInfo { return m.overlay }

// ─────────────────────────────────────────────────────────────────────────────
// Snapshot-aware filters
// ─────────────────────────────────────────────────────────────────────────────

// SnapshotKeyFilter returns a [fanwatch.Filter] that passes only events whose
// overlay layer paths contain the given snapshot key segment. Useful to isolate
// a single container's events in a multi-container watcher.
//
// Requires [ContainerdEnricher] or [MountEnricher] to have run (Overlay != nil).
func SnapshotKeyFilter(snapshotKey string) fanwatch.Filter {
	return fanwatch.FilterFunc(func(_ context.Context, e *fanwatch.EnrichedEvent) bool {
		if e.Overlay == nil {
			return false
		}
		for _, layer := range e.Overlay.Layers {
			if pathContainsSegment(layer.Path, snapshotKey) {
				return true
			}
		}
		return false
	})
}

// MergedDirFilter returns a [fanwatch.Filter] that passes only events whose
// path falls inside mergedDir. Use to target a specific container in a
// multi-overlay watcher.
func MergedDirFilter(mergedDir string) fanwatch.Filter {
	return fanwatch.PathPrefixFilter(mergedDir)
}

// UpperDirWritesFilter passes only events for files that exist in the overlay
// upperdir — files created or modified by the container rather than inherited
// from read-only image layers.
func UpperDirWritesFilter() fanwatch.Filter {
	return fanwatch.UpperDirOnlyFilter()
}

// ImageLayerFilter passes only events for files originating from read-only
// lower layers — accesses to inherited image content.
func ImageLayerFilter() fanwatch.Filter {
	return fanwatch.LowerDirOnlyFilter()
}

// ContainerdKindFilter returns a [fanwatch.Filter] that passes events whose
// overlay's label "containerd.io/snapshot/kind" matches kind
// (e.g. "committed", "active", "view"). Requires OverlayInfo.Labels to be
// populated — use alongside [SnapshotterEnricher] which copies Stat labels.
func ContainerdKindFilter(kind string) fanwatch.Filter {
	const labelKey = "containerd.io/snapshot/kind"
	return fanwatch.AttrValueFilter(labelKey, kind)
}

// ─────────────────────────────────────────────────────────────────────────────
// SnapshotInfoTransformer — attach containerd snapshot labels to event attrs
// ─────────────────────────────────────────────────────────────────────────────

// SnapshotInfoTransformer is a [fanwatch.Transformer] that calls
// snapshots.Snapshotter.Stat() and writes the snapshot Info fields into
// e.Attrs under the "containerd.snapshot.*" namespace.
//
// Fields set:
//   - "containerd.snapshot.name"   — snapshot key/name
//   - "containerd.snapshot.kind"   — "committed", "active", or "view"
//   - "containerd.snapshot.parent" — parent snapshot name (may be empty)
//   - "containerd.snapshot.labels" — map[string]string of snapshot labels
//
// keyFn maps each event to a snapshot key; return "" to skip enrichment.
func SnapshotInfoTransformer(
	sn snapshots.Snapshotter,
	keyFn func(*fanwatch.EnrichedEvent) string,
) fanwatch.Transformer {
	return fanwatch.TransformerFunc(func(ctx context.Context, e *fanwatch.EnrichedEvent) error {
		key := keyFn(e)
		if key == "" {
			return nil
		}

		info, err := sn.Stat(ctx, key)
		if err != nil {
			// Non-fatal: snapshot may have been removed.
			return nil
		}

		e.SetAttr("containerd.snapshot.name", info.Name)
		e.SetAttr("containerd.snapshot.kind", info.Kind.String())
		e.SetAttr("containerd.snapshot.parent", info.Parent)
		if len(info.Labels) > 0 {
			e.SetAttr("containerd.snapshot.labels", info.Labels)
		}
		return nil
	})
}

// ─────────────────────────────────────────────────────────────────────────────
// MountsToOverlayInfo — utility for callers that already have []mount.Mount
// ─────────────────────────────────────────────────────────────────────────────

// MountsToOverlayInfo converts a []mount.Mount (as returned by
// snapshots.Snapshotter.Mounts, Prepare, or View) into a [fanwatch.OverlayInfo].
//
// This is the canonical bridge between the containerd mount representation and
// the fanwatch overlay representation.
func MountsToOverlayInfo(mounts []ctdmount.Mount, mergedDir string) (*fanwatch.OverlayInfo, error) {
	return overlayInfoFromMounts(mounts, mergedDir)
}

// ─────────────────────────────────────────────────────────────────────────────
// internal helpers
// ─────────────────────────────────────────────────────────────────────────────

// pathContainsSegment checks whether path contains segment as a path component.
func pathContainsSegment(path, segment string) bool {
	if segment == "" || path == "" {
		return false
	}
	return path == segment ||
		strings.Contains(path, "/"+segment+"/") ||
		strings.HasSuffix(path, "/"+segment) ||
		strings.HasPrefix(path, segment+"/")
}
