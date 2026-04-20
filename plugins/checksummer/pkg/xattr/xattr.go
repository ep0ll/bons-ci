//go:build linux

// Package xattr reads and writes Linux extended attributes to cache hash
// results directly on the filesystem.
//
// Uses github.com/pkg/xattr which handles all architecture-specific syscall
// details, ENOTSUP graceful degradation, and proper two-shot sizing.
//
// Attribute layout:
//
//	user.ovlhash.hash  = <64 hex chars>   (32-byte BLAKE3 digest)
//	user.ovlhash.mtime = <unix-nano>      (decimal string)
//	user.ovlhash.size  = <file size>      (decimal string)
//
// The mtime+size pair guards against stale cached values: if either changes
// the cached hash is rejected and a fresh computation is triggered.
package xattr

import (
	"encoding/hex"
	"fmt"
	"strconv"

	pkgxattr "github.com/pkg/xattr"
	"golang.org/x/sys/unix"
)

// FileStat holds the fields needed for cache validation.
type FileStat struct {
	MtimeNs int64 // ModTime().UnixNano()
	Size    int64
}

// StatFromUnix converts unix.Stat_t to FileStat.
func StatFromUnix(st *unix.Stat_t) FileStat {
	return FileStat{
		MtimeNs: st.Mtim.Sec*1e9 + st.Mtim.Nsec,
		Size:    st.Size,
	}
}

// Cache reads and writes hash xattrs. All methods are safe for concurrent use:
// each call is a sequence of independent syscalls with no shared state.
type Cache struct {
	prefix string // e.g. "user.ovlhash" or "trusted.ovlhash"
}

// NewCache creates an xattr Cache with the given attribute name prefix.
//
// Prefix recommendations:
//   - "user.ovlhash"    – writable by file owner (requires filesystem support)
//   - "trusted.ovlhash" – requires CAP_SYS_ADMIN; works on most ext4/xfs
//   - "security.ovlhash"– requires CAP_SYS_ADMIN; survives across remounts
func NewCache(prefix string) *Cache { return &Cache{prefix: prefix} }

func (c *Cache) hashAttr() string  { return c.prefix + ".hash" }
func (c *Cache) mtimeAttr() string { return c.prefix + ".mtime" }
func (c *Cache) sizeAttr() string  { return c.prefix + ".size" }

// Load retrieves the cached hash for path, validating against stat.
//
// Returns (nil, false, nil) on cache miss or stale entry (file modified).
// Returns an error only for unexpected failures (corrupt hex, etc.).
func (c *Cache) Load(path string, stat FileStat) ([]byte, bool, error) {
	mtimeB, err := pkgxattr.Get(path, c.mtimeAttr())
	if err != nil {
		return nil, false, nil // attribute absent
	}
	sizeB, err := pkgxattr.Get(path, c.sizeAttr())
	if err != nil {
		return nil, false, nil
	}

	cachedMtime, err := strconv.ParseInt(string(mtimeB), 10, 64)
	if err != nil || cachedMtime != stat.MtimeNs {
		return nil, false, nil // stale
	}
	cachedSize, err := strconv.ParseInt(string(sizeB), 10, 64)
	if err != nil || cachedSize != stat.Size {
		return nil, false, nil // stale
	}

	hashB, err := pkgxattr.Get(path, c.hashAttr())
	if err != nil {
		return nil, false, nil
	}
	hash, err := hex.DecodeString(string(hashB))
	if err != nil {
		return nil, false, fmt.Errorf("xattr: invalid hash hex for %q: %w", path, err)
	}
	return hash, true, nil
}

// Save writes the hash and validation attributes for path.
//
// Silently ignores EPERM / EOPNOTSUPP / EROFS so that xattr is truly
// advisory: the engine continues correctly even when xattrs are unsupported.
func (c *Cache) Save(path string, stat FileStat, hash []byte) error {
	if err := set(path, c.hashAttr(), hex.EncodeToString(hash)); isFatal(err) {
		return fmt.Errorf("xattr: set hash %q: %w", path, err)
	}
	if err := set(path, c.mtimeAttr(), strconv.FormatInt(stat.MtimeNs, 10)); isFatal(err) {
		return fmt.Errorf("xattr: set mtime %q: %w", path, err)
	}
	if err := set(path, c.sizeAttr(), strconv.FormatInt(stat.Size, 10)); isFatal(err) {
		return fmt.Errorf("xattr: set size %q: %w", path, err)
	}
	return nil
}

// Remove deletes all xattrs written by this Cache for the given path.
func (c *Cache) Remove(path string) {
	_ = pkgxattr.Remove(path, c.hashAttr())
	_ = pkgxattr.Remove(path, c.mtimeAttr())
	_ = pkgxattr.Remove(path, c.sizeAttr())
}

// ─────────────────────────── helpers ─────────────────────────────────────────

func set(path, attr, val string) error {
	return pkgxattr.Set(path, attr, []byte(val))
}

// isFatal returns true for unexpected errors that should be surfaced.
// Permission and support errors are non-fatal: xattr is advisory.
func isFatal(err error) bool {
	if err == nil {
		return false
	}
	if e, ok := err.(*pkgxattr.Error); ok {
		switch e.Err {
		case unix.EPERM, unix.EOPNOTSUPP, unix.EROFS:
			return false
		}
	}
	return true
}
