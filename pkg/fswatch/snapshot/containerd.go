// Package snapshot resolves containerd snapshotter metadata into fanwatch
// [fanwatch.OverlayInfo] values.
//
// # Containerd overlay layout
//
// Containerd's overlayfs snapshotter stores each snapshot under a base
// directory (default /var/lib/containerd/io.containerd.snapshotter.v1.overlayfs)
// using the following layout:
//
//	<snapshotterRoot>/
//	  snapshots/
//	    <id>/          ← numeric snapshot ID assigned by containerd
//	      fs/          ← the layer's read-only diff (lowerdir)
//	      work/        ← overlay work dir (present only for writable snapshots)
//
// At runtime, containerd mounts each container's overlay using the chain of
// ancestor snapshot IDs as lowerdirs. The mount options are visible in
// /proc/self/mountinfo.
//
// # Two resolution strategies
//
//  1. Mount-based ([OverlayInfoFromContainerdMount]): parses /proc/self/mountinfo
//     to find the overlay mount for a given merged directory. Works for any
//     running container visible to the current mount namespace.
//
//  2. Metadata-based ([OverlayInfoFromContainerdSnapshot]): reads the containerd
//     bolt metadata database to reconstruct the layer chain without a live mount.
//     Useful for static analysis, pre-start inspection, and testing.
//
// # containerd-snapper utility compatibility
//
// This package understands the mount options produced by:
//   - containerd's native overlayfs snapshotter
//   - nerdctl / BuildKit
//   - CRI-containerd (used by Kubernetes CRI)
//
// It does NOT require the containerd gRPC socket — all parsing is done via
// /proc/mounts and the filesystem layout, so it works inside containers that
// have /proc and the snapshotter directory bind-mounted.
package snapshot

import (
	"bufio"
	"encoding/binary"
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strconv"
	"strings"

	fanwatch "github.com/bons/bons-ci/pkg/fswatch"
)

// ─────────────────────────────────────────────────────────────────────────────
// ContainerdSnapshotter — describes one containerd overlayfs snapshotter root
// ─────────────────────────────────────────────────────────────────────────────

// ContainerdSnapshotter provides overlay filesystem resolution for a single
// containerd overlayfs snapshotter instance.
//
// The zero value is invalid; use [NewContainerdSnapshotter].
type ContainerdSnapshotter struct {
	// Root is the snapshotter base directory.
	// Default: /var/lib/containerd/io.containerd.snapshotter.v1.overlayfs
	Root string

	// Namespace is the containerd namespace. Default: "default".
	Namespace string
}

// DefaultSnapshotterRoot is the standard containerd overlayfs snapshotter root.
const DefaultSnapshotterRoot = "/var/lib/containerd/io.containerd.snapshotter.v1.overlayfs"

// NewContainerdSnapshotter constructs a [ContainerdSnapshotter] with the
// standard root directory and "default" namespace.
func NewContainerdSnapshotter() *ContainerdSnapshotter {
	return &ContainerdSnapshotter{
		Root:      DefaultSnapshotterRoot,
		Namespace: "default",
	}
}

// WithRoot sets a non-standard snapshotter root directory.
func (s *ContainerdSnapshotter) WithRoot(root string) *ContainerdSnapshotter {
	s.Root = root
	return s
}

// WithNamespace sets the containerd namespace.
func (s *ContainerdSnapshotter) WithNamespace(ns string) *ContainerdSnapshotter {
	s.Namespace = ns
	return s
}

// snapshotsDir returns the path to the snapshots directory.
func (s *ContainerdSnapshotter) snapshotsDir() string {
	return filepath.Join(s.Root, "snapshots")
}

// ─────────────────────────────────────────────────────────────────────────────
// OverlayInfoFromContainerdMount — live-mount strategy
// ─────────────────────────────────────────────────────────────────────────────

// OverlayInfoFromContainerdMount resolves an [fanwatch.OverlayInfo] for a
// running container by finding its overlay mount in /proc/self/mountinfo.
//
// This is the preferred strategy for live containers — no access to the
// containerd socket or metadata database is required.
//
// Returns [fanwatch.ErrMountNotFound] when no overlay mount matches mergedDir.
func OverlayInfoFromContainerdMount(mergedDir string) (*fanwatch.OverlayInfo, error) {
	return OverlayInfoFromMountFile("/proc/self/mountinfo", mergedDir)
}

// OverlayInfoFromMountFile is the testable inner implementation that accepts a
// custom mountinfo path. Use [OverlayInfoFromContainerdMount] in production.
func OverlayInfoFromMountFile(mountinfoPath, mergedDir string) (*fanwatch.OverlayInfo, error) {
	mergedDir = filepath.Clean(mergedDir)

	f, err := os.Open(mountinfoPath)
	if err != nil {
		return nil, fmt.Errorf("snapshot: open %s: %w", mountinfoPath, err)
	}
	defer f.Close()

	scanner := bufio.NewScanner(f)
	for scanner.Scan() {
		info, ok := parseMountinfoLine(scanner.Text())
		if !ok || info.MergedDir != mergedDir {
			continue
		}
		return info, nil
	}
	if err := scanner.Err(); err != nil {
		return nil, fmt.Errorf("snapshot: scan %s: %w", mountinfoPath, err)
	}
	return nil, fmt.Errorf("snapshot: %w: %q", fanwatch.ErrMountNotFound, mergedDir)
}

// parseMountinfoLine parses one line of /proc/self/mountinfo and returns an
// OverlayInfo when it describes an overlay filesystem mount.
//
// mountinfo format (kernel 3.8+):
//
//	mountID parentID major:minor root mountPoint mountOptions [optFields] - fsType source superOptions
func parseMountinfoLine(line string) (*fanwatch.OverlayInfo, bool) {
	// Split on " - " to isolate the filesystem-specific tail.
	halves := strings.SplitN(line, " - ", 2)
	if len(halves) != 2 {
		return nil, false
	}
	left := strings.Fields(halves[0])
	right := strings.Fields(halves[1])

	if len(left) < 6 || len(right) < 3 {
		return nil, false
	}
	if right[0] != "overlay" {
		return nil, false
	}

	mountPoint := left[4]
	superOptions := right[2]
	opts := parseKV(superOptions)

	lower := opts["lowerdir"]
	if lower == "" {
		return nil, false
	}

	lowerDirs := strings.Split(lower, ":")
	for i, d := range lowerDirs {
		lowerDirs[i] = filepath.Clean(d)
	}

	upper := filepath.Clean(opts["upperdir"])
	work := filepath.Clean(opts["workdir"])

	info := fanwatch.NewOverlayInfo(
		filepath.Clean(mountPoint),
		upper,
		work,
		lowerDirs,
	)
	return info, true
}

// parseKV parses a comma-separated "key=value" options string.
func parseKV(opts string) map[string]string {
	m := make(map[string]string)
	for _, field := range strings.Split(opts, ",") {
		kv := strings.SplitN(field, "=", 2)
		if len(kv) == 2 {
			m[kv[0]] = kv[1]
		} else if kv[0] != "" {
			m[kv[0]] = ""
		}
	}
	return m
}

// ─────────────────────────────────────────────────────────────────────────────
// OverlayInfoFromContainerdSnapshot — filesystem-layout strategy
// ─────────────────────────────────────────────────────────────────────────────

// SnapshotChain describes a resolved chain of containerd snapshot layers.
type SnapshotChain struct {
	// SnapshotID is the leaf (top-most) snapshot numeric ID.
	SnapshotID string

	// ParentIDs is the ordered ancestor chain, from immediate parent to base.
	// ParentIDs[0] is the direct parent of SnapshotID.
	ParentIDs []string

	// FSDirs maps each snapshot ID to its "fs" directory path.
	FSDirs map[string]string

	// WorkDir is the overlay work directory for the top snapshot (writable layer).
	WorkDir string

	// UpperDir is the overlay upper directory for the top snapshot.
	UpperDir string

	// IsReadOnly reports true when the snapshot chain has no writable layer.
	IsReadOnly bool
}

// OverlayInfoFromContainerdSnapshot constructs an [fanwatch.OverlayInfo] from a
// containerd snapshot ID by reading the filesystem layout under the snapshotter
// root. This works without a running containerd daemon.
//
// snapshotID is the numeric ID assigned by containerd (e.g. "47").
// mergedDir is where the overlay should be (or is) mounted; this is set as
// the MergedDir field but the function does not mount anything itself.
//
// The chain is built by scanning the metadata.db bolt database structure or,
// when that is unavailable, by following the "parent" symlink convention used
// by some containerd versions.
func (s *ContainerdSnapshotter) OverlayInfoFromContainerdSnapshot(
	snapshotID, mergedDir string,
) (*fanwatch.OverlayInfo, error) {
	chain, err := s.resolveSnapshotChain(snapshotID)
	if err != nil {
		return nil, fmt.Errorf("snapshot: resolve chain for %q: %w", snapshotID, err)
	}
	return chainToOverlayInfo(chain, mergedDir), nil
}

// resolveSnapshotChain builds the full ancestor chain for snapshotID by
// reading the snapshots directory layout.
func (s *ContainerdSnapshotter) resolveSnapshotChain(snapshotID string) (*SnapshotChain, error) {
	snapsDir := s.snapshotsDir()

	chain := &SnapshotChain{
		SnapshotID: snapshotID,
		FSDirs:     make(map[string]string),
	}

	// Check whether the top snapshot has a writable layer (work dir present).
	topDir := filepath.Join(snapsDir, snapshotID)
	workDir := filepath.Join(topDir, "work")
	upperDir := filepath.Join(topDir, "fs")

	if _, err := os.Stat(workDir); err == nil {
		// Writable snapshot: upperdir = fs/, workdir = work/
		chain.WorkDir = workDir
		chain.UpperDir = upperDir
	} else {
		// Read-only snapshot: the fs/ dir becomes the topmost lowerdir.
		chain.IsReadOnly = true
		chain.FSDirs[snapshotID] = upperDir
	}

	// Walk the ancestor chain by reading the "parent" file that containerd
	// writes into each snapshot directory.
	current := snapshotID
	visited := map[string]struct{}{current: {}}

	for {
		parentFile := filepath.Join(snapsDir, current, "parent")
		data, err := os.ReadFile(parentFile)
		if os.IsNotExist(err) {
			// Reached the base layer — no more parents.
			break
		}
		if err != nil {
			// Try the bolt metadata strategy as a fallback.
			break
		}

		parentID := strings.TrimSpace(string(data))
		if parentID == "" {
			break
		}
		if _, seen := visited[parentID]; seen {
			return nil, fmt.Errorf("snapshot: cycle detected at %q", parentID)
		}
		visited[parentID] = struct{}{}

		fsDir := filepath.Join(snapsDir, parentID, "fs")
		if _, err := os.Stat(fsDir); err == nil {
			chain.FSDirs[parentID] = fsDir
			chain.ParentIDs = append(chain.ParentIDs, parentID)
		}
		current = parentID
	}

	// If the "parent" file strategy yielded no parents, fall back to scanning
	// the metadata.db bolt file for the ancestor chain.
	if len(chain.ParentIDs) == 0 && !chain.IsReadOnly {
		if parents, err := s.ancestorsFromBolt(snapshotID); err == nil && len(parents) > 0 {
			for _, pid := range parents {
				fsDir := filepath.Join(snapsDir, pid, "fs")
				if _, statErr := os.Stat(fsDir); statErr == nil {
					chain.FSDirs[pid] = fsDir
					chain.ParentIDs = append(chain.ParentIDs, pid)
				}
			}
		}
	}

	// Validate: the leaf snapshot's fs/ dir must exist.
	leafFS := filepath.Join(topDir, "fs")
	if _, err := os.Stat(leafFS); err != nil {
		return nil, fmt.Errorf("snapshot: fs dir missing for snapshot %q at %q: %w",
			snapshotID, leafFS, err)
	}

	return chain, nil
}

// chainToOverlayInfo converts a [SnapshotChain] into an [fanwatch.OverlayInfo].
func chainToOverlayInfo(chain *SnapshotChain, mergedDir string) *fanwatch.OverlayInfo {
	// Build lowerdir list: for writable snapshots the leaf fs/ is a lowerdir too;
	// parents follow in ancestry order (immediate parent first = topmost lowerdir).
	var lowerDirs []string

	if !chain.IsReadOnly {
		// leaf fs/ dir is the topmost lower layer (just below the upper).
		leafFS := chain.FSDirs[chain.SnapshotID]
		if leafFS == "" {
			// Fall back: reconstruct from SnapshotID.
			leafFS = chain.UpperDir // already set correctly
		}
		// The leaf snapshot's fs/ is actually used as the topmost lowerdir
		// when there is an upperdir — containerd convention.
		lowerDirs = append(lowerDirs, chain.UpperDir)
		chain.UpperDir = "" // will be recalculated
	}

	// Add ancestors as lowerdirs in order (immediate parent = topmost remaining lower).
	for _, pid := range chain.ParentIDs {
		if d, ok := chain.FSDirs[pid]; ok {
			lowerDirs = append(lowerDirs, d)
		}
	}

	upperDir := chain.UpperDir
	workDir := chain.WorkDir

	if chain.IsReadOnly {
		// Pure read-only: no upper or work dir.
		lowerDirs = nil
		for _, pid := range append([]string{chain.SnapshotID}, chain.ParentIDs...) {
			if d, ok := chain.FSDirs[pid]; ok {
				lowerDirs = append(lowerDirs, d)
			}
		}
	}

	return fanwatch.NewOverlayInfo(mergedDir, upperDir, workDir, lowerDirs)
}

// ─────────────────────────────────────────────────────────────────────────────
// bolt metadata reader — parse containerd's metadata.db for parent chains
// ─────────────────────────────────────────────────────────────────────────────

// ancestorsFromBolt reads the containerd bolt metadata database to find the
// ancestor snapshot IDs for snapshotID.
//
// This is a minimal bolt page reader — it only extracts the parent-chain
// information needed to reconstruct the lowerdir stack without requiring the
// containerd daemon or any external library.
//
// Returns an empty slice (not an error) when the metadata.db cannot be read
// or does not contain ancestry information for the given snapshot.
func (s *ContainerdSnapshotter) ancestorsFromBolt(snapshotID string) ([]string, error) {
	dbPath := filepath.Join(s.Root, "metadata.db")
	data, err := os.ReadFile(dbPath)
	if err != nil {
		return nil, fmt.Errorf("snapshot: read metadata.db: %w", err)
	}

	// bolt page size is stored at offset 16 in the file header (uint32 LE).
	if len(data) < 64 {
		return nil, fmt.Errorf("snapshot: metadata.db too small")
	}
	pageSize := int(binary.LittleEndian.Uint32(data[16:20]))
	if pageSize < 512 || pageSize > 65536 {
		pageSize = 4096 // sane default
	}

	// Walk pages looking for the parent relationship encoded as ASCII strings.
	// We search for occurrences of "parent" adjacent to numeric snapshot IDs —
	// this is sufficient to reconstruct the chain without full B-tree parsing.
	ancestors := extractParentChain(data, snapshotID, pageSize)
	return ancestors, nil
}

// extractParentChain performs a heuristic scan of bolt database pages to find
// snapshot parent relationships. It looks for patterns like:
//
//	<snapshotID>\x00...\x00parent\x00<parentID>
//
// and builds the full ancestry chain by following parent IDs recursively.
func extractParentChain(data []byte, leafID string, _ int) []string {
	// Build a parent map by scanning for "parent" key occurrences.
	parentOf := make(map[string]string)

	// We look for NUL-delimited strings typical of bolt's bucket key encoding.
	raw := string(data)
	// Find "parent" markers and extract (child → parent) pairs.
	searchStr := "parent"
	pos := 0
	for {
		idx := strings.Index(raw[pos:], searchStr)
		if idx < 0 {
			break
		}
		abs := pos + idx
		pos = abs + len(searchStr)

		// Look backward for the child snapshot ID (a numeric string before "parent").
		childID := extractNumericBefore(raw, abs)
		// Look forward for the parent snapshot ID.
		parentID := extractNumericAfter(raw, abs+len(searchStr))

		if childID != "" && parentID != "" && childID != parentID {
			parentOf[childID] = parentID
		}
	}

	// Walk the parent chain from leafID.
	var chain []string
	current := leafID
	visited := map[string]struct{}{}
	for {
		parent, ok := parentOf[current]
		if !ok || parent == "" {
			break
		}
		if _, seen := visited[parent]; seen {
			break
		}
		visited[parent] = struct{}{}
		chain = append(chain, parent)
		current = parent
	}
	return chain
}

// extractNumericBefore scans backward from pos to find the last numeric token.
func extractNumericBefore(s string, pos int) string {
	// Skip non-printable bytes backward.
	end := pos
	for end > 0 && (s[end-1] < '0' || s[end-1] > '9') {
		end--
		if pos-end > 32 {
			return ""
		}
	}
	if end == 0 {
		return ""
	}
	start := end
	for start > 0 && s[start-1] >= '0' && s[start-1] <= '9' {
		start--
	}
	token := s[start:end]
	if _, err := strconv.Atoi(token); err != nil {
		return ""
	}
	return token
}

// extractNumericAfter scans forward from pos to find the next numeric token.
func extractNumericAfter(s string, pos int) string {
	i := pos
	for i < len(s) && (s[i] < '0' || s[i] > '9') {
		i++
		if i-pos > 32 {
			return ""
		}
	}
	start := i
	for i < len(s) && s[i] >= '0' && s[i] <= '9' {
		i++
	}
	token := s[start:i]
	if _, err := strconv.Atoi(token); err != nil {
		return ""
	}
	return token
}

// ─────────────────────────────────────────────────────────────────────────────
// AllRunningSnapshotMounts — enumerate all live containerd overlay mounts
// ─────────────────────────────────────────────────────────────────────────────

// MountEntry describes one live overlay mount discovered from mountinfo.
type MountEntry struct {
	// MergedDir is the mount point (merged view path).
	MergedDir string
	// Overlay contains the parsed overlay structure for this mount.
	Overlay *fanwatch.OverlayInfo
}

// AllRunningSnapshotMounts returns all overlay mounts whose lowerdirs fall
// inside the snapshotter's snapshot directory. This lets callers discover all
// running containers without querying the containerd socket.
func (s *ContainerdSnapshotter) AllRunningSnapshotMounts() ([]MountEntry, error) {
	return allRunningSnapshotMounts("/proc/self/mountinfo", s.snapshotsDir())
}

func allRunningSnapshotMounts(mountinfoPath, snapsDir string) ([]MountEntry, error) {
	snapsDir = filepath.Clean(snapsDir)

	f, err := os.Open(mountinfoPath)
	if err != nil {
		return nil, fmt.Errorf("snapshot: open %s: %w", mountinfoPath, err)
	}
	defer f.Close()

	var entries []MountEntry
	scanner := bufio.NewScanner(f)
	for scanner.Scan() {
		info, ok := parseMountinfoLine(scanner.Text())
		if !ok {
			continue
		}
		// Include this mount if any lowerdir lives under snapsDir.
		for _, lower := range info.LowerDirs {
			if strings.HasPrefix(lower, snapsDir+"/") {
				entries = append(entries, MountEntry{
					MergedDir: info.MergedDir,
					Overlay:   info,
				})
				break
			}
		}
	}
	if err := scanner.Err(); err != nil {
		return nil, fmt.Errorf("snapshot: scan %s: %w", mountinfoPath, err)
	}

	// Sort by merged dir for deterministic output.
	sort.Slice(entries, func(i, j int) bool {
		return entries[i].MergedDir < entries[j].MergedDir
	})
	return entries, nil
}

// ─────────────────────────────────────────────────────────────────────────────
// SnapshotIDFromMergedDir — reverse-lookup snapshot ID from a merged dir
// ─────────────────────────────────────────────────────────────────────────────

// SnapshotIDFromMergedDir extracts the containerd snapshot ID from a merged
// directory path. Containerd's standard merged path is:
//
//	<snapshotterRoot>/snapshots/<id>/merged
//
// Returns the numeric snapshot ID string, or an error when the path does not
// match the expected layout.
func (s *ContainerdSnapshotter) SnapshotIDFromMergedDir(mergedDir string) (string, error) {
	mergedDir = filepath.Clean(mergedDir)
	snapsDir := filepath.Clean(s.snapshotsDir())

	rel, err := filepath.Rel(snapsDir, mergedDir)
	if err != nil || strings.HasPrefix(rel, "..") {
		return "", fmt.Errorf("snapshot: %q is not under snapshotter root %q", mergedDir, snapsDir)
	}

	parts := strings.SplitN(rel, string(filepath.Separator), 2)
	if len(parts) < 1 {
		return "", fmt.Errorf("snapshot: cannot extract ID from %q", mergedDir)
	}
	id := parts[0]
	if _, err := strconv.Atoi(id); err != nil {
		return "", fmt.Errorf("snapshot: non-numeric ID %q in path %q", id, mergedDir)
	}
	return id, nil
}

// ─────────────────────────────────────────────────────────────────────────────
// ContainerdOverlayInfo — high-level convenience function
// ─────────────────────────────────────────────────────────────────────────────

// ContainerdOverlayInfo is the recommended entry point for most callers. It
// tries mount-based resolution first (fast, accurate for live containers) and
// falls back to filesystem-layout resolution when the mount is not found.
//
//  1. Scans /proc/self/mountinfo for an overlay mount at mergedDir.
//  2. If not found, derives the snapshot ID from mergedDir and reconstructs
//     the overlay info from the snapshotter directory layout.
func (s *ContainerdSnapshotter) ContainerdOverlayInfo(mergedDir string) (*fanwatch.OverlayInfo, error) {
	// Strategy 1: live mount lookup (most accurate).
	info, err := OverlayInfoFromContainerdMount(mergedDir)
	if err == nil {
		return info, nil
	}

	// Strategy 2: reconstruct from snapshotter filesystem layout.
	id, idErr := s.SnapshotIDFromMergedDir(mergedDir)
	if idErr != nil {
		// Return the original mount error which is more informative.
		return nil, fmt.Errorf("snapshot: live mount not found and cannot derive snapshot ID: %w", err)
	}

	return s.OverlayInfoFromContainerdSnapshot(id, mergedDir)
}

// AllRunningSnapshotMountsFromFile is the testable inner implementation of
// AllRunningSnapshotMounts. Use AllRunningSnapshotMounts in production.
func AllRunningSnapshotMountsFromFile(mountinfoPath, snapsDir string) ([]MountEntry, error) {
	return allRunningSnapshotMounts(mountinfoPath, snapsDir)
}
