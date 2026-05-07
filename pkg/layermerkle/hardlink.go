package layermerkle

import (
	"context"
	"fmt"
	"os"
	"sync"
)

// ─────────────────────────────────────────────────────────────────────────────
// InodeKey — device+inode pair for hard-link deduplication
// ─────────────────────────────────────────────────────────────────────────────

// InodeKey uniquely identifies a file across hard links by its device+inode pair.
// Two paths sharing the same InodeKey have identical content — hashing one
// is sufficient for both.
type InodeKey struct {
	Dev   uint64
	Inode uint64
}

// ─────────────────────────────────────────────────────────────────────────────
// HardLinkAwareHasher — avoids hashing the same inode twice
// ─────────────────────────────────────────────────────────────────────────────

// HardLinkAwareHasher wraps a FileHasher and maintains an inode→hash cache.
// When two paths within the same layer share an inode (hard link), the hash
// is computed once and reused for all subsequent paths with the same inode.
//
// This is a Decorator over FileHasher that adds hard-link awareness without
// modifying the underlying algorithm.
type HardLinkAwareHasher struct {
	inner    FileHasher
	mu       sync.RWMutex
	inodeMap map[InodeKey]FileHash
}

// NewHardLinkAwareHasher wraps inner with hard-link deduplication.
func NewHardLinkAwareHasher(inner FileHasher) *HardLinkAwareHasher {
	return &HardLinkAwareHasher{
		inner:    inner,
		inodeMap: make(map[InodeKey]FileHash),
	}
}

// Hash implements FileHasher. Checks the inode cache before delegating to inner.
func (h *HardLinkAwareHasher) Hash(ctx context.Context, absPath string) (FileHash, error) {
	inode, err := readInode(absPath)
	if err != nil {
		// Cannot stat — fall through to regular hash (file may still exist).
		return h.inner.Hash(ctx, absPath)
	}

	// Fast path: check inode cache.
	h.mu.RLock()
	if cached, ok := h.inodeMap[inode]; ok {
		h.mu.RUnlock()
		return cached, nil
	}
	h.mu.RUnlock()

	// Slow path: compute hash and populate inode cache.
	hash, err := h.inner.Hash(ctx, absPath)
	if err != nil {
		return "", err
	}

	h.mu.Lock()
	h.inodeMap[inode] = hash
	h.mu.Unlock()

	return hash, nil
}

// Algorithm implements FileHasher.
func (h *HardLinkAwareHasher) Algorithm() string { return h.inner.Algorithm() }

// InvalidateLayer clears all inode entries whose path prefix matches the
// layer's diff directory. Call after a layer is committed or removed.
// Since inode→hash mappings are cross-path, this must clear the full cache
// when any layer changes. Conservative but correct.
func (h *HardLinkAwareHasher) InvalidateAll() {
	h.mu.Lock()
	h.inodeMap = make(map[InodeKey]FileHash)
	h.mu.Unlock()
}

// InodeCacheLen returns the number of cached inode→hash entries.
func (h *HardLinkAwareHasher) InodeCacheLen() int {
	h.mu.RLock()
	n := len(h.inodeMap)
	h.mu.RUnlock()
	return n
}

// readInode performs an Lstat and extracts the device+inode pair.
func readInode(absPath string) (InodeKey, error) {
	info, err := os.Lstat(absPath)
	if err != nil {
		return InodeKey{}, fmt.Errorf("lstat %s: %w", absPath, err)
	}
	return inodeFromFileInfo(info), nil
}
