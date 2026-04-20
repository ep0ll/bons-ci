// Package overlay parses /proc/self/mountinfo and resolves backing files for
// paths accessed through an overlayfs or RAFS merged view.
package overlay

import (
	"bufio"
	"fmt"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"sync"
	"time"
)

const DefaultMountInfoPath = "/proc/self/mountinfo"

// ─────────────────────────── MountEntry ──────────────────────────────────────

// MountEntry represents one line of /proc/self/mountinfo.
type MountEntry struct {
	MountID    int
	ParentID   int
	Major      int
	Minor      int
	Root       string
	MountPoint string
	MountOpts  string
	FSType     string
	Source     string
	SuperOpts  string

	// Overlay-specific fields (only populated when FSType == "overlay").
	LowerDirs []string // ordered top (most-derived) → bottom (base)
	UpperDir  string
	WorkDir   string
}

// IsOverlay reports whether this is an overlayfs mount.
func (m *MountEntry) IsOverlay() bool { return m.FSType == "overlay" }

// AllLayers returns upper + lowers in search order (upper first).
func (m *MountEntry) AllLayers() []string {
	var layers []string
	if m.UpperDir != "" {
		layers = append(layers, m.UpperDir)
	}
	layers = append(layers, m.LowerDirs...)
	return layers
}

// ─────────────────────────── MountInfo ───────────────────────────────────────

// MountInfo parses and caches /proc/self/mountinfo.  Safe for concurrent use.
type MountInfo struct {
	mu       sync.RWMutex
	entries  []*MountEntry
	byID     map[int]*MountEntry
	byMount  map[string]*MountEntry
	path     string
	cachedAt time.Time
	cacheTTL time.Duration
}

// NewMountInfo creates a MountInfo.  cacheTTL=0 disables caching.
func NewMountInfo(path string, cacheTTL time.Duration) *MountInfo {
	if path == "" {
		path = DefaultMountInfoPath
	}
	return &MountInfo{
		path:     path,
		cacheTTL: cacheTTL,
		byID:     make(map[int]*MountEntry),
		byMount:  make(map[string]*MountEntry),
	}
}

// Refresh re-reads and re-parses the mountinfo file unconditionally.
func (mi *MountInfo) Refresh() error {
	entries, err := parseMountInfo(mi.path)
	if err != nil {
		return err
	}
	mi.mu.Lock()
	defer mi.mu.Unlock()
	mi.entries = entries
	mi.byID = make(map[int]*MountEntry, len(entries))
	mi.byMount = make(map[string]*MountEntry, len(entries))
	for _, e := range entries {
		mi.byID[e.MountID] = e
		mi.byMount[e.MountPoint] = e
	}
	mi.cachedAt = time.Now()
	return nil
}

func (mi *MountInfo) ensure() error {
	mi.mu.RLock()
	stale := len(mi.entries) == 0 ||
		(mi.cacheTTL > 0 && time.Since(mi.cachedAt) > mi.cacheTTL)
	mi.mu.RUnlock()
	if stale {
		return mi.Refresh()
	}
	return nil
}

// OverlayMounts returns all overlayfs mount entries.
func (mi *MountInfo) OverlayMounts() ([]*MountEntry, error) {
	if err := mi.ensure(); err != nil {
		return nil, err
	}
	mi.mu.RLock()
	defer mi.mu.RUnlock()
	var out []*MountEntry
	for _, e := range mi.entries {
		if e.IsOverlay() {
			out = append(out, e)
		}
	}
	return out, nil
}

// ForMountPoint returns the entry for the exact mount point path.
func (mi *MountInfo) ForMountPoint(mp string) (*MountEntry, bool, error) {
	if err := mi.ensure(); err != nil {
		return nil, false, err
	}
	mi.mu.RLock()
	defer mi.mu.RUnlock()
	e, ok := mi.byMount[mp]
	return e, ok, nil
}

// ContainingMount returns the deepest mount that contains path.
func (mi *MountInfo) ContainingMount(path string) (*MountEntry, error) {
	if err := mi.ensure(); err != nil {
		return nil, err
	}
	mi.mu.RLock()
	defer mi.mu.RUnlock()
	best := ""
	var bestEntry *MountEntry
	for mp, e := range mi.byMount {
		if strings.HasPrefix(path, mp) && len(mp) > len(best) {
			best = mp
			bestEntry = e
		}
	}
	if bestEntry == nil {
		return nil, fmt.Errorf("overlay: no mount for path %q", path)
	}
	return bestEntry, nil
}

// Entries returns all parsed mount entries (caller must not modify the slice).
func (mi *MountInfo) Entries() []*MountEntry {
	_ = mi.ensure()
	mi.mu.RLock()
	defer mi.mu.RUnlock()
	return mi.entries
}

// ─────────────────────────── BackingResolver ─────────────────────────────────

// BackingResolver resolves the real backing file for a path inside an
// overlayfs merged view.
type BackingResolver struct{ mi *MountInfo }

// NewBackingResolver creates a BackingResolver.
func NewBackingResolver(mi *MountInfo) *BackingResolver { return &BackingResolver{mi: mi} }

// BackingPath returns the physical path of file in its backing layer.
// inOverlay is false if path is not inside an overlayfs mount.
func (r *BackingResolver) BackingPath(path string) (backing string, inOverlay bool, err error) {
	mount, merr := r.mi.ContainingMount(path)
	if merr != nil || !mount.IsOverlay() {
		return path, false, nil
	}
	rel, rerr := filepath.Rel(mount.MountPoint, path)
	if rerr != nil {
		return path, false, nil
	}
	for _, layer := range mount.AllLayers() {
		candidate := filepath.Join(layer, rel)
		if fileExists(candidate) {
			return candidate, true, nil
		}
	}
	return path, true, fmt.Errorf("overlay: %q not found in any layer of %q", path, mount.MountPoint)
}

// LayersForMount returns upper + lower dirs for the named mount point.
func (r *BackingResolver) LayersForMount(mountPoint string) (upper string, lowers []string, err error) {
	e, ok, err := r.mi.ForMountPoint(mountPoint)
	if err != nil || !ok {
		return "", nil, fmt.Errorf("overlay: no entry for %q: %v", mountPoint, err)
	}
	return e.UpperDir, e.LowerDirs, nil
}

// ─────────────────────────── parser ──────────────────────────────────────────

func parseMountInfo(path string) ([]*MountEntry, error) {
	f, err := os.Open(path)
	if err != nil {
		return nil, fmt.Errorf("mountinfo open %q: %w", path, err)
	}
	defer f.Close()

	var entries []*MountEntry
	sc := bufio.NewScanner(f)
	for sc.Scan() {
		e, err := parseLine(sc.Text())
		if err != nil {
			continue // skip malformed lines
		}
		entries = append(entries, e)
	}
	return entries, sc.Err()
}

// parseLine parses one mountinfo line.
// Format: mountID parentID major:minor root mountPoint opts [optFields] - fsType source superOpts
func parseLine(line string) (*MountEntry, error) {
	parts := strings.SplitN(line, " - ", 2)
	if len(parts) != 2 {
		return nil, fmt.Errorf("malformed: %q", line)
	}
	pre := strings.Fields(parts[0])
	post := strings.Fields(parts[1])
	if len(pre) < 6 || len(post) < 3 {
		return nil, fmt.Errorf("too few fields: %q", line)
	}

	e := &MountEntry{
		Root:       pre[3],
		MountPoint: pre[4],
		MountOpts:  pre[5],
		FSType:     post[0],
		Source:     post[1],
		SuperOpts:  post[2],
	}
	var err error
	e.MountID, err = strconv.Atoi(pre[0])
	if err != nil {
		return nil, err
	}
	e.ParentID, _ = strconv.Atoi(pre[1])

	mm := strings.SplitN(pre[2], ":", 2)
	if len(mm) == 2 {
		e.Major, _ = strconv.Atoi(mm[0])
		e.Minor, _ = strconv.Atoi(mm[1])
	}

	if e.FSType == "overlay" {
		parseOverlayOpts(e)
	}
	return e, nil
}

func parseOverlayOpts(e *MountEntry) {
	for _, opt := range strings.Split(e.SuperOpts, ",") {
		kv := strings.SplitN(opt, "=", 2)
		if len(kv) != 2 {
			continue
		}
		k, v := strings.TrimSpace(kv[0]), strings.TrimSpace(kv[1])
		switch k {
		case "lowerdir":
			e.LowerDirs = strings.Split(v, ":")
		case "upperdir":
			e.UpperDir = v
		case "workdir":
			e.WorkDir = v
		}
	}
}

func fileExists(path string) bool {
	_, err := os.Lstat(path)
	return err == nil
}
