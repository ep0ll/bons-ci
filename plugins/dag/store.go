package reactdag

import (
	"context"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"sync"
	"time"
)

// ---------------------------------------------------------------------------
// ChainedCacheStore — transparent two-tier cache
// ---------------------------------------------------------------------------

// ChainedCacheStore chains two CacheStore tiers: fast (local/memory) and slow
// (remote/disk). It is itself a CacheStore, so it can be used anywhere a
// single store is expected — for example as the fastCache in the Scheduler
// while leaving the scheduler's slowCache slot for a third tier.
//
// Read path:  fast → slow (back-fills fast on slow hit)
// Write path: fast + slow (both receive every Set)
type ChainedCacheStore struct {
	fast CacheStore
	slow CacheStore
}

// NewChainedCacheStore constructs a two-tier store.
func NewChainedCacheStore(fast, slow CacheStore) *ChainedCacheStore {
	return &ChainedCacheStore{fast: fast, slow: slow}
}

// Get queries the fast tier, then the slow tier on a miss, back-filling fast.
func (c *ChainedCacheStore) Get(ctx context.Context, key CacheKey) (*CacheEntry, error) {
	entry, err := c.fast.Get(ctx, key)
	if err != nil {
		return nil, fmt.Errorf("chained cache: fast get: %w", err)
	}
	if entry != nil {
		return entry, nil
	}

	entry, err = c.slow.Get(ctx, key)
	if err != nil {
		return nil, fmt.Errorf("chained cache: slow get: %w", err)
	}
	if entry != nil {
		_ = c.fast.Set(ctx, key, entry) // back-fill; best-effort
	}
	return entry, nil
}

// Set writes to both tiers.
func (c *ChainedCacheStore) Set(ctx context.Context, key CacheKey, entry *CacheEntry) error {
	if err := c.fast.Set(ctx, key, entry); err != nil {
		return fmt.Errorf("chained cache: fast set: %w", err)
	}
	if err := c.slow.Set(ctx, key, entry); err != nil {
		return fmt.Errorf("chained cache: slow set: %w", err)
	}
	return nil
}

// Delete removes the entry from both tiers.
func (c *ChainedCacheStore) Delete(ctx context.Context, key CacheKey) error {
	_ = c.fast.Delete(ctx, key)
	return c.slow.Delete(ctx, key)
}

// Exists checks the fast tier first, then the slow tier.
func (c *ChainedCacheStore) Exists(ctx context.Context, key CacheKey) (bool, error) {
	ok, err := c.fast.Exists(ctx, key)
	if err != nil || ok {
		return ok, err
	}
	return c.slow.Exists(ctx, key)
}

// ---------------------------------------------------------------------------
// DiskCacheStore — JSON-serialised file-system cache
// ---------------------------------------------------------------------------

// diskEntry is the on-disk representation of a CacheEntry.
// It uses hex strings for byte arrays so the JSON is human-readable.
type diskEntry struct {
	Key         string        `json:"key"`
	OutputFiles []diskFileRef `json:"output_files,omitempty"`
	CachedErr   string        `json:"cached_err,omitempty"`
	CachedAt    time.Time     `json:"cached_at"`
	HitCount    int           `json:"hit_count"`
	DurationMS  int64         `json:"duration_ms"`
}

type diskFileRef struct {
	Path    string    `json:"path"`
	Hash    string    `json:"hash"` // hex-encoded [32]byte
	Size    int64     `json:"size"`
	ModTime time.Time `json:"mod_time"`
}

// DiskCacheStore is a persistent CacheStore backed by the local filesystem.
// Each entry is a JSON file named after the hex-encoded CacheKey.
// Suitable as the slow tier in a two-tier setup.
//
// All operations are serialised per-key via a striped mutex, giving safe
// concurrent access without a global lock.
type DiskCacheStore struct {
	dir string
	mu  [256]sync.Mutex // stripe by key[0] to reduce contention
}

// NewDiskCacheStore creates (or reuses) a directory-backed cache store.
// dir is created if it does not exist.
func NewDiskCacheStore(dir string) (*DiskCacheStore, error) {
	if err := os.MkdirAll(dir, 0o755); err != nil {
		return nil, fmt.Errorf("disk cache: create dir %q: %w", dir, err)
	}
	return &DiskCacheStore{dir: dir}, nil
}

// Get retrieves a cache entry from disk. Returns (nil, nil) on a miss.
func (d *DiskCacheStore) Get(_ context.Context, key CacheKey) (*CacheEntry, error) {
	mu := d.lockFor(key)
	mu.Lock()
	defer mu.Unlock()

	data, err := os.ReadFile(d.pathFor(key))
	if os.IsNotExist(err) {
		return nil, nil
	}
	if err != nil {
		return nil, fmt.Errorf("disk cache: read %s: %w", d.pathFor(key), err)
	}

	var de diskEntry
	if err := json.Unmarshal(data, &de); err != nil {
		return nil, fmt.Errorf("disk cache: unmarshal: %w", err)
	}
	return d.fromDisk(de), nil
}

// Set writes a cache entry to disk atomically (write-to-temp + rename).
func (d *DiskCacheStore) Set(_ context.Context, key CacheKey, entry *CacheEntry) error {
	mu := d.lockFor(key)
	mu.Lock()
	defer mu.Unlock()

	de := d.toDisk(key, entry)
	data, err := json.MarshalIndent(de, "", "  ")
	if err != nil {
		return fmt.Errorf("disk cache: marshal: %w", err)
	}

	tmp := d.pathFor(key) + ".tmp"
	if err := os.WriteFile(tmp, data, 0o644); err != nil {
		return fmt.Errorf("disk cache: write tmp: %w", err)
	}
	if err := os.Rename(tmp, d.pathFor(key)); err != nil {
		_ = os.Remove(tmp)
		return fmt.Errorf("disk cache: rename: %w", err)
	}
	return nil
}

// Delete removes a cache entry from disk.
func (d *DiskCacheStore) Delete(_ context.Context, key CacheKey) error {
	mu := d.lockFor(key)
	mu.Lock()
	defer mu.Unlock()

	if err := os.Remove(d.pathFor(key)); err != nil && !os.IsNotExist(err) {
		return fmt.Errorf("disk cache: delete: %w", err)
	}
	return nil
}

// Exists checks whether a key file is present on disk.
func (d *DiskCacheStore) Exists(_ context.Context, key CacheKey) (bool, error) {
	mu := d.lockFor(key)
	mu.Lock()
	defer mu.Unlock()

	_, err := os.Stat(d.pathFor(key))
	if err == nil {
		return true, nil
	}
	if os.IsNotExist(err) {
		return false, nil
	}
	return false, fmt.Errorf("disk cache: stat: %w", err)
}

// Prune deletes all entries older than maxAge.
func (d *DiskCacheStore) Prune(maxAge time.Duration) (int, error) {
	entries, err := os.ReadDir(d.dir)
	if err != nil {
		return 0, fmt.Errorf("disk cache: readdir: %w", err)
	}
	cutoff := time.Now().Add(-maxAge)
	removed := 0
	for _, e := range entries {
		if e.IsDir() || filepath.Ext(e.Name()) != ".json" {
			continue
		}
		info, err := e.Info()
		if err != nil {
			continue
		}
		if info.ModTime().Before(cutoff) {
			_ = os.Remove(filepath.Join(d.dir, e.Name()))
			removed++
		}
	}
	return removed, nil
}

// ---------------------------------------------------------------------------
// DiskCacheStore — internal helpers
// ---------------------------------------------------------------------------

func (d *DiskCacheStore) pathFor(key CacheKey) string {
	return filepath.Join(d.dir, fmt.Sprintf("%x.json", key))
}

func (d *DiskCacheStore) lockFor(key CacheKey) *sync.Mutex {
	return &d.mu[key[0]] // stripe by first byte of key
}

func (d *DiskCacheStore) toDisk(key CacheKey, e *CacheEntry) diskEntry {
	de := diskEntry{
		Key:        fmt.Sprintf("%x", key),
		CachedErr:  e.CachedErr,
		CachedAt:   e.CachedAt,
		HitCount:   e.HitCount,
		DurationMS: e.DurationMS,
	}
	for _, f := range e.OutputFiles {
		de.OutputFiles = append(de.OutputFiles, diskFileRef{
			Path:    f.Path,
			Hash:    fmt.Sprintf("%x", f.Hash),
			Size:    f.Size,
			ModTime: f.ModTime,
		})
	}
	return de
}

func (d *DiskCacheStore) fromDisk(de diskEntry) *CacheEntry {
	entry := &CacheEntry{
		CachedErr:  de.CachedErr,
		CachedAt:   de.CachedAt,
		HitCount:   de.HitCount,
		DurationMS: de.DurationMS,
	}
	for _, f := range de.OutputFiles {
		ref := FileRef{
			Path:    f.Path,
			Size:    f.Size,
			ModTime: f.ModTime,
		}
		fmt.Sscanf(f.Hash, "%x", &ref.Hash)
		entry.OutputFiles = append(entry.OutputFiles, ref)
	}
	return entry
}
