package fshash

import (
	"os"
	"sync"
	"time"
)

// ── MtimeCache ────────────────────────────────────────────────────────────────

// MtimeCache is a [FileCache] that automatically invalidates entries when a
// file's modification time or size changes.
//
// Unlike [MemoryCache], callers do not need to manually call Invalidate after
// modifying files on disk: each Get call performs one os.Lstat to verify that
// the recorded mtime and size still match.  If they don't, the entry is
// evicted and a miss is returned, causing the file to be rehashed.
//
// # Trade-offs
//
//   - One extra os.Lstat per cached file per Sum call (negligible compared to
//     the file read it replaces).
//   - Sub-second mtime granularity can be a problem on filesystems that only
//     record 1-second timestamps (e.g. FAT32, some NFS configurations).  In
//     that case, a file modified and then read within the same second may
//     return a stale hit.  If that matters, use [MemoryCache] with explicit
//     [MemoryCache.Invalidate] calls instead.
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

// Get returns the cached digest if the file's mtime and size have not changed
// since the digest was computed.  Returns (nil, false) on any cache miss or
// validation failure.
//
// Implementation note: we stat the file outside the lock to avoid holding a
// lock during a syscall.  After the stat we re-acquire the lock and perform a
// double-checked lookup: if another goroutine wrote a newer entry between our
// read and the re-lock we return that newer entry rather than evicting it.
func (c *MtimeCache) Get(absPath string) ([]byte, bool) {
	c.mu.RLock()
	e, ok := c.items[absPath]
	c.mu.RUnlock()
	if !ok {
		return nil, false
	}

	// Stat outside the lock — syscalls should not hold mutexes.
	fi, statErr := os.Lstat(absPath)

	c.mu.Lock()
	defer c.mu.Unlock()

	// Re-read the entry: another goroutine may have updated it while we held
	// no lock.
	current, still := c.items[absPath]
	if !still {
		// Evicted by another goroutine already.
		return nil, false
	}

	if statErr != nil {
		// File disappeared or is inaccessible — evict and miss.
		delete(c.items, absPath)
		return nil, false
	}

	// If the entry changed between our initial read and now, use the fresh one.
	if current.mtime != e.mtime || current.size != e.size {
		e = current
	}

	if fi.ModTime() != e.mtime || fi.Size() != e.size {
		// File was modified — evict stale entry.
		delete(c.items, absPath)
		return nil, false
	}

	// Deep-copy so the caller cannot mutate our stored slice.
	out := make([]byte, len(e.digest))
	copy(out, e.digest)
	return out, true
}

// Set stores the digest together with the file's current mtime and size.
// It performs one os.Lstat call; if that fails (e.g., the file was deleted
// immediately after hashing) the entry is silently not stored.
func (c *MtimeCache) Set(absPath string, dgst []byte) {
	fi, err := os.Lstat(absPath)
	if err != nil {
		return // can't record mtime without a successful stat
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

// Invalidate removes the entry for absPath from the cache, if any.
func (c *MtimeCache) Invalidate(absPath string) {
	c.mu.Lock()
	delete(c.items, absPath)
	c.mu.Unlock()
}

// InvalidateAll removes all entries from the cache.
func (c *MtimeCache) InvalidateAll() {
	c.mu.Lock()
	c.items = nil
	c.mu.Unlock()
}

// Len returns the number of entries currently held in the cache.
func (c *MtimeCache) Len() int {
	c.mu.RLock()
	defer c.mu.RUnlock()
	return len(c.items)
}

// Prune removes entries for files that no longer exist or whose mtime/size
// have changed.  It is an optional maintenance operation; MtimeCache.Get
// already self-heals on individual lookups.
func (c *MtimeCache) Prune() {
	c.mu.Lock()
	defer c.mu.Unlock()

	for absPath, e := range c.items {
		fi, err := os.Lstat(absPath)
		if err != nil || fi.ModTime() != e.mtime || fi.Size() != e.size {
			delete(c.items, absPath)
		}
	}
}
