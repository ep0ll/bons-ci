// Package snapshot resolves containerd snapshotter mounts into fanwatch
// [fanwatch.OverlayInfo] values using the official containerd libraries.
//
// # Import dependencies
//
//   - github.com/containerd/containerd/mount  — Mount struct and Lookup()
//   - github.com/containerd/containerd/snapshots — Snapshotter interface, Info, Kind
//   - github.com/moby/sys/mountinfo — GetMounts, FSTypeFilter, ParentsFilter
//
// # Two resolution strategies
//
//  1. Live-mount strategy ([OverlayInfoFromMergedDir]): uses
//     containerd/mount.Lookup and moby/sys/mountinfo.GetMounts to find the
//     running overlay mount for a merged directory and parse its options
//     (lowerdir, upperdir, workdir) directly from the kernel's mountinfo.
//
//  2. Snapshotter strategy ([OverlayInfoFromSnapshotter]): calls
//     snapshots.Snapshotter.Mounts() which returns []mount.Mount — the same
//     data structure the containerd daemon uses to actually mount the snapshot.
//     From those Mount.Options we extract lowerdir/upperdir/workdir.
//
// Both strategies produce the same [fanwatch.OverlayInfo] shape so the rest of
// the pipeline is oblivious to which was used.
package snapshot

import (
	"context"
	"fmt"
	"path/filepath"
	"sort"
	"strings"

	ctdmount "github.com/containerd/containerd/mount"
	"github.com/containerd/containerd/snapshots"
	"github.com/moby/sys/mountinfo"

	fanwatch "github.com/bons/bons-ci/pkg/fswatch"
)

// ─────────────────────────────────────────────────────────────────────────────
// Strategy 1 — live mount lookup via moby/sys/mountinfo
// ─────────────────────────────────────────────────────────────────────────────

// OverlayInfoFromMergedDir returns an [fanwatch.OverlayInfo] for a running
// container by finding its overlay mount via the kernel's mountinfo.
//
// It uses:
//   - moby/sys/mountinfo.GetMounts with FSTypeFilter("overlay") to enumerate
//     all overlay mounts efficiently.
//   - containerd/mount.Lookup to resolve the exact mount for mergedDir.
//
// Returns [fanwatch.ErrMountNotFound] when no overlay mount matches mergedDir.
func OverlayInfoFromMergedDir(mergedDir string) (*fanwatch.OverlayInfo, error) {
	mergedDir = filepath.Clean(mergedDir)

	// Use moby/sys/mountinfo to get all overlay mounts in one pass.
	// FSTypeFilter restricts the kernel parse to overlay-type entries only.
	mounts, err := mountinfo.GetMounts(mountinfo.FSTypeFilter("overlay"))
	if err != nil {
		return nil, fmt.Errorf("snapshot: get overlay mounts: %w", err)
	}

	for _, m := range mounts {
		if filepath.Clean(m.Mountpoint) != mergedDir {
			continue
		}
		return overlayInfoFromMountinfo(m, mergedDir)
	}
	return nil, fmt.Errorf("snapshot: %w: %q", fanwatch.ErrMountNotFound, mergedDir)
}

// overlayInfoFromMountinfo converts a moby/sys/mountinfo.Info entry into a
// fanwatch.OverlayInfo by parsing the VFSOptions field for lowerdir/upperdir/workdir.
//
// The mountinfo VFSOptions field (column 11 of /proc/self/mountinfo) contains
// the superblock options string: "lowerdir=/l1:/l2,upperdir=/u,workdir=/w,..."
func overlayInfoFromMountinfo(m *mountinfo.Info, mergedDir string) (*fanwatch.OverlayInfo, error) {
	opts := parseKVOptions(m.VFSOptions)

	lower := opts["lowerdir"]
	if lower == "" {
		return nil, fmt.Errorf("snapshot: overlay mount at %q has no lowerdir", mergedDir)
	}

	lowerDirs := strings.Split(lower, ":")
	for i, d := range lowerDirs {
		lowerDirs[i] = filepath.Clean(d)
	}

	upper := opts["upperdir"]
	work := opts["workdir"]

	info := fanwatch.NewOverlayInfo(
		mergedDir,
		upper, // empty string for read-only mounts — NewOverlayInfo handles this
		work,
		lowerDirs,
	)
	return info, nil
}

// ─────────────────────────────────────────────────────────────────────────────
// Strategy 2 — containerd Snapshotter.Mounts()
// ─────────────────────────────────────────────────────────────────────────────

// OverlayInfoFromSnapshotter resolves an [fanwatch.OverlayInfo] from a
// containerd Snapshotter by calling Mounts(ctx, snapshotKey).
//
// Mounts() returns []mount.Mount populated by the snapshotter with the correct
// lowerdir/upperdir/workdir options for the snapshot chain. This is the same
// data the containerd daemon uses to actually mount the snapshot, making it
// the most accurate source of truth for the layer stack.
//
// snapshotKey is the containerd snapshot name/key (not a numeric ID).
// mergedDir is the directory where the overlay is (or will be) mounted.
//
// The method works without a live mount — it reconstructs the overlay info
// from the snapshotter metadata alone, so it can be called before or after
// the container mounts are applied.
func OverlayInfoFromSnapshotter(
	ctx context.Context,
	sn snapshots.Snapshotter,
	snapshotKey string,
	mergedDir string,
) (*fanwatch.OverlayInfo, error) {
	mounts, err := sn.Mounts(ctx, snapshotKey)
	if err != nil {
		return nil, fmt.Errorf("snapshot: snapshotter.Mounts(%q): %w", snapshotKey, err)
	}

	info, err := overlayInfoFromMounts(mounts, mergedDir)
	if err != nil {
		return nil, fmt.Errorf("snapshot: overlay info from mounts for %q: %w", snapshotKey, err)
	}
	return info, nil
}

// overlayInfoFromMounts converts the []mount.Mount returned by
// snapshots.Snapshotter.Mounts() into an [fanwatch.OverlayInfo].
//
// containerd's overlayfs snapshotter produces exactly one mount of type
// "overlay" with options containing lowerdir/upperdir/workdir, or a "bind"
// mount for single-layer snapshots.
func overlayInfoFromMounts(mounts []ctdmount.Mount, mergedDir string) (*fanwatch.OverlayInfo, error) {
	for _, m := range mounts {
		switch m.Type {
		case "overlay":
			return overlayInfoFromOverlayMount(m, mergedDir)
		case "bind":
			// Single-layer snapshot: containerd returns a bind mount when there
			// is only one layer and no parent. The bind source is the fs/ dir.
			return fanwatch.NewOverlayInfo(
				mergedDir,
				"", // no upperdir — read-only bind
				"", // no workdir
				[]string{m.Source},
			), nil
		}
	}
	return nil, fmt.Errorf("snapshot: no overlay or bind mount found in %d mounts", len(mounts))
}

// overlayInfoFromOverlayMount parses a containerd mount.Mount of type "overlay"
// into an [fanwatch.OverlayInfo].
//
// containerd encodes the overlay options in mount.Mount.Options as a string
// slice where each element is "key=value" or "key". For example:
//
//	Options: ["workdir=/snapshots/3/work", "upperdir=/snapshots/3/fs",
//	           "lowerdir=/snapshots/2/fs:/snapshots/1/fs"]
func overlayInfoFromOverlayMount(m ctdmount.Mount, mergedDir string) (*fanwatch.OverlayInfo, error) {
	opts := parseSliceKVOptions(m.Options)

	lower := opts["lowerdir"]
	if lower == "" {
		return nil, fmt.Errorf("snapshot: overlay mount missing lowerdir option")
	}

	lowerDirs := strings.Split(lower, ":")
	for i, d := range lowerDirs {
		lowerDirs[i] = filepath.Clean(d)
	}

	return fanwatch.NewOverlayInfo(
		mergedDir,
		opts["upperdir"],
		opts["workdir"],
		lowerDirs,
	), nil
}

// ─────────────────────────────────────────────────────────────────────────────
// Strategy 3 — unified resolution with automatic fallback
// ─────────────────────────────────────────────────────────────────────────────

// ResolveOverlayInfo resolves an [fanwatch.OverlayInfo] using the best
// available strategy:
//
//  1. Live-mount via moby/sys/mountinfo (fastest, most accurate for running containers).
//  2. If not mounted, falls back to sn.Mounts() when a Snapshotter is provided.
//
// Pass nil for sn to skip the snapshotter fallback.
func ResolveOverlayInfo(
	ctx context.Context,
	mergedDir string,
	sn snapshots.Snapshotter,
	snapshotKey string,
) (*fanwatch.OverlayInfo, error) {
	// Preferred: live mount lookup.
	info, err := OverlayInfoFromMergedDir(mergedDir)
	if err == nil {
		return info, nil
	}

	// Fallback: snapshotter Mounts().
	if sn != nil && snapshotKey != "" {
		info, snErr := OverlayInfoFromSnapshotter(ctx, sn, snapshotKey, mergedDir)
		if snErr == nil {
			return info, nil
		}
		return nil, fmt.Errorf("snapshot: both strategies failed: mount=%w, snapshotter=%v", err, snErr)
	}

	return nil, err
}

// ─────────────────────────────────────────────────────────────────────────────
// AllOverlayMounts — enumerate every live overlay mount
// ─────────────────────────────────────────────────────────────────────────────

// MountEntry describes one live overlay mount discovered from the kernel.
type MountEntry struct {
	// MergedDir is the mount point (merged overlay view).
	MergedDir string
	// Overlay contains the parsed overlay structure for this mount.
	Overlay *fanwatch.OverlayInfo
}

// AllOverlayMounts returns every currently active overlay mount visible in
// /proc/self/mountinfo, using moby/sys/mountinfo.GetMounts with FSTypeFilter.
//
// Results are sorted by MergedDir for deterministic output.
func AllOverlayMounts() ([]MountEntry, error) {
	mounts, err := mountinfo.GetMounts(mountinfo.FSTypeFilter("overlay"))
	if err != nil {
		return nil, fmt.Errorf("snapshot: enumerate overlay mounts: %w", err)
	}

	entries := make([]MountEntry, 0, len(mounts))
	for _, m := range mounts {
		mergedDir := filepath.Clean(m.Mountpoint)
		info, err := overlayInfoFromMountinfo(m, mergedDir)
		if err != nil {
			// Skip malformed entries.
			continue
		}
		entries = append(entries, MountEntry{
			MergedDir: mergedDir,
			Overlay:   info,
		})
	}

	sort.Slice(entries, func(i, j int) bool {
		return entries[i].MergedDir < entries[j].MergedDir
	})
	return entries, nil
}

// ContainerdSnapshotMounts returns only the overlay mounts whose lowerdirs
// fall under snapshotterRoot (e.g. the containerd overlayfs snapshotter root).
//
// This filters AllOverlayMounts to show only containerd-managed containers,
// excluding other overlay mounts (Docker, BuildKit workspaces, etc.).
func ContainerdSnapshotMounts(snapshotterRoot string) ([]MountEntry, error) {
	snapshotterRoot = filepath.Clean(snapshotterRoot)
	all, err := AllOverlayMounts()
	if err != nil {
		return nil, err
	}

	var filtered []MountEntry
	for _, e := range all {
		for _, lower := range e.Overlay.LowerDirs {
			if strings.HasPrefix(lower, snapshotterRoot+"/") {
				filtered = append(filtered, e)
				break
			}
		}
	}
	return filtered, nil
}

// ─────────────────────────────────────────────────────────────────────────────
// Snapshotter walk — build ancestry chain from containerd metadata
// ─────────────────────────────────────────────────────────────────────────────

// SnapshotAncestors walks the containerd snapshotter to collect the full
// ancestor chain of snapshotKey in parent-first order.
//
// Uses snapshots.Snapshotter.Stat() to follow the Parent chain without
// requiring any direct database access.
func SnapshotAncestors(
	ctx context.Context,
	sn snapshots.Snapshotter,
	snapshotKey string,
) ([]snapshots.Info, error) {
	var chain []snapshots.Info

	current := snapshotKey
	visited := map[string]struct{}{}

	for current != "" {
		if _, seen := visited[current]; seen {
			return nil, fmt.Errorf("snapshot: cycle detected at key %q", current)
		}
		visited[current] = struct{}{}

		info, err := sn.Stat(ctx, current)
		if err != nil {
			return nil, fmt.Errorf("snapshot: stat %q: %w", current, err)
		}
		chain = append(chain, info)
		current = info.Parent
	}
	return chain, nil
}

// ─────────────────────────────────────────────────────────────────────────────
// Mount.ReadOnly helper
// ─────────────────────────────────────────────────────────────────────────────

// IsMountReadOnly reports whether a containerd mount.Mount is read-only.
// Delegates to the containerd library's own ReadOnly() method.
func IsMountReadOnly(m ctdmount.Mount) bool {
	return m.ReadOnly()
}

// ─────────────────────────────────────────────────────────────────────────────
// Option parsing helpers
// ─────────────────────────────────────────────────────────────────────────────

// parseKVOptions parses a comma-separated "key=value" string (as found in
// mountinfo VFSOptions) into a map.
func parseKVOptions(opts string) map[string]string {
	m := make(map[string]string)
	for _, field := range strings.Split(opts, ",") {
		kv := strings.SplitN(field, "=", 2)
		if len(kv) == 2 {
			m[strings.TrimSpace(kv[0])] = strings.TrimSpace(kv[1])
		} else if kv[0] != "" {
			m[strings.TrimSpace(kv[0])] = ""
		}
	}
	return m
}

// parseSliceKVOptions parses a []string of "key=value" entries (as found in
// containerd mount.Mount.Options) into a map.
func parseSliceKVOptions(options []string) map[string]string {
	m := make(map[string]string)
	for _, opt := range options {
		kv := strings.SplitN(opt, "=", 2)
		if len(kv) == 2 {
			m[strings.TrimSpace(kv[0])] = strings.TrimSpace(kv[1])
		} else if kv[0] != "" {
			m[strings.TrimSpace(kv[0])] = ""
		}
	}
	return m
}
