package fshash

import (
	"os"
	"sync"
	"time"
)

// ── FileCache interface ────────────────────────────────────────────────────────

// FileCache maps absolute paths to pre-computed digests.
// All implementations MUST be safe for concurrent use.
type FileCache interface {
	Get(absPath string) (digest []byte, ok bool)
	Set(absPath string, digest []byte)
	Invalidate(absPath string)
}

// ── MemoryCache ───────────────────────────────────────────────────────────────

// MemoryCache is a thread-safe in-memory FileCache backed by sync.Map.
// Entries are never evicted automatically; call Invalidate or InvalidateAll
// explicitly, or use MtimeCache for automatic invalidation.
type MemoryCache struct{ m sync.Map }

func (c *MemoryCache) Get(absPath string) ([]byte, bool) {
	v, ok := c.m.Load(absPath)
	if !ok {
		return nil, false
	}
	return v.([]byte), true
}

func (c *MemoryCache) Set(absPath string, dgst []byte) {
	d := make([]byte, len(dgst))
	copy(d, dgst)
	c.m.Store(absPath, d)
}

func (c *MemoryCache) Invalidate(absPath string) { c.m.Delete(absPath) }

// InvalidateAll clears all entries.
func (c *MemoryCache) InvalidateAll() {
	c.m.Range(func(k, _ any) bool { c.m.Delete(k); return true })
}

// ── MtimeCache ────────────────────────────────────────────────────────────────

// MtimeCache is a FileCache that auto-invalidates when a file's mtime or size
// changes. Each Get call performs one os.Lstat to validate freshness.
//
// Trade-off: one extra syscall per cached file per Sum call — negligible
// compared to the file read it saves on a hit. Sub-second mtime granularity
// can cause stale hits on FAT32 or coarse-grained NFS; use MemoryCache with
// explicit Invalidate if that matters.
//
// MtimeCache is safe for concurrent use.
type MtimeCache struct {
	mu    sync.RWMutex
	items map[string]mtimeEntry
}

type mtimeEntry struct {
	digest []byte
	mtime  time.Time
	size   int64
}

// Get returns the cached digest when the file's mtime and size are unchanged.
// Performs a double-checked stat outside the lock to avoid holding a mutex
// during a syscall.
func (c *MtimeCache) Get(absPath string) ([]byte, bool) {
	c.mu.RLock()
	e, ok := c.items[absPath]
	c.mu.RUnlock()
	if !ok {
		return nil, false
	}

	fi, statErr := os.Lstat(absPath) // stat outside lock

	c.mu.Lock()
	defer c.mu.Unlock()

	current, still := c.items[absPath]
	if !still {
		return nil, false
	}
	if statErr != nil {
		delete(c.items, absPath)
		return nil, false
	}
	// Another goroutine may have refreshed the entry between our reads.
	if current.mtime != e.mtime || current.size != e.size {
		e = current
	}
	if fi.ModTime() != e.mtime || fi.Size() != e.size {
		delete(c.items, absPath)
		return nil, false
	}
	out := make([]byte, len(e.digest))
	copy(out, e.digest)
	return out, true
}

// Set stores the digest with the file's current mtime/size. Silently no-ops
// when the file cannot be stat'd (e.g. deleted immediately after hashing).
func (c *MtimeCache) Set(absPath string, dgst []byte) {
	fi, err := os.Lstat(absPath)
	if err != nil {
		return
	}
	d := make([]byte, len(dgst))
	copy(d, dgst)
	c.mu.Lock()
	if c.items == nil {
		c.items = make(map[string]mtimeEntry)
	}
	c.items[absPath] = mtimeEntry{digest: d, mtime: fi.ModTime(), size: fi.Size()}
	c.mu.Unlock()
}

func (c *MtimeCache) Invalidate(absPath string) {
	c.mu.Lock()
	delete(c.items, absPath)
	c.mu.Unlock()
}

// InvalidateAll removes all entries.
func (c *MtimeCache) InvalidateAll() {
	c.mu.Lock()
	c.items = nil
	c.mu.Unlock()
}

// Len returns the current entry count.
func (c *MtimeCache) Len() int {
	c.mu.RLock()
	defer c.mu.RUnlock()
	return len(c.items)
}

// Prune evicts entries whose file no longer exists or has changed. Optional
// maintenance; Get already self-heals on individual lookups.
func (c *MtimeCache) Prune() {
	c.mu.Lock()
	defer c.mu.Unlock()
	for p, e := range c.items {
		fi, err := os.Lstat(p)
		if err != nil || fi.ModTime() != e.mtime || fi.Size() != e.size {
			delete(c.items, p)
		}
	}
}

// ── instrumentedCache ─────────────────────────────────────────────────────────

// instrumentedCache wraps a FileCache recording hit/miss counts and paths.
// Used internally by Inspector; not exported.
type instrumentedCache struct {
	delegate FileCache
	mu       sync.Mutex
	hitPaths map[string]struct{}
	nHits    int
	nTotal   int
}

func (ic *instrumentedCache) reset() {
	ic.mu.Lock()
	ic.hitPaths = make(map[string]struct{})
	ic.nHits, ic.nTotal = 0, 0
	ic.mu.Unlock()
}

func (ic *instrumentedCache) hitSnapshot() map[string]struct{} {
	ic.mu.Lock()
	defer ic.mu.Unlock()
	out := make(map[string]struct{}, len(ic.hitPaths))
	for k := range ic.hitPaths {
		out[k] = struct{}{}
	}
	return out
}

func (ic *instrumentedCache) stats() (hits, total int) {
	ic.mu.Lock()
	defer ic.mu.Unlock()
	return ic.nHits, ic.nTotal
}

func (ic *instrumentedCache) Get(absPath string) ([]byte, bool) {
	if ic.delegate == nil {
		ic.mu.Lock()
		ic.nTotal++
		ic.mu.Unlock()
		return nil, false
	}
	d, ok := ic.delegate.Get(absPath)
	ic.mu.Lock()
	ic.nTotal++
	if ok {
		ic.nHits++
		ic.hitPaths[absPath] = struct{}{}
	}
	ic.mu.Unlock()
	return d, ok
}

func (ic *instrumentedCache) Set(absPath string, dgst []byte) {
	if ic.delegate != nil {
		ic.delegate.Set(absPath, dgst)
	}
}

func (ic *instrumentedCache) Invalidate(absPath string) {
	ic.mu.Lock()
	delete(ic.hitPaths, absPath)
	ic.mu.Unlock()
	if ic.delegate != nil {
		ic.delegate.Invalidate(absPath)
	}
}
