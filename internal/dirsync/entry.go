package dirsync

import (
	"fmt"
	"io/fs"
	"os"
	"path/filepath"
	"syscall"
)

// dirEntry wraps fs.FileInfo so the walk layer has a uniform type.
type dirEntry struct {
	info fs.FileInfo
}

// readDirEntries returns the contents of dir as a sorted slice of dirEntry.
//
// os.ReadDir is used because:
//   - It returns entries sorted by filename (mandatory for merge-scan correctness).
//   - On Linux it issues a single getdents64(2) syscall; FileInfo is populated
//     from the dirent d_type + an lstat(2) per entry (cached inside DirEntry).
//
// When followSymlinks == true, an additional stat(2) is issued for each
// symlink entry to resolve the target's type and size.
func readDirEntries(dir string, followSymlinks bool) ([]dirEntry, error) {
	raw, err := os.ReadDir(dir)
	if err != nil {
		return nil, err
	}

	out := make([]dirEntry, 0, len(raw))
	for _, re := range raw {
		info, err := resolveEntryInfo(dir, re, followSymlinks)
		if err != nil {
			if os.IsNotExist(err) {
				// Race: entry vanished between listing and stat; skip gracefully.
				continue
			}
			return nil, fmt.Errorf("stat %q: %w", filepath.Join(dir, re.Name()), err)
		}
		out = append(out, dirEntry{info: info})
	}
	return out, nil
}

// resolveEntryInfo returns FileInfo for a single DirEntry.
//
//   - Regular files/dirs/devices: re.Info() is free (lstat already cached by ReadDir).
//   - Symlinks + followSymlinks==true: issues one extra stat(2) to follow the link.
//   - Symlinks + followSymlinks==false: re.Info() (lstat), preserving ModeSymlink.
func resolveEntryInfo(dir string, re fs.DirEntry, followSymlinks bool) (fs.FileInfo, error) {
	if followSymlinks && re.Type()&fs.ModeSymlink != 0 {
		return os.Stat(filepath.Join(dir, re.Name()))
	}
	return re.Info()
}

// ─── Metadata fast-path ───────────────────────────────────────────────────────

// sameMetadata is the O(0)-I/O fast-path equality predicate.
//
// Decision tree (short-circuits in order):
//  1. Same device + inode  → hard-linked to the exact same inode; identical.
//  2. Same size + mtime    → assumed identical; skip content hash.
//     (mtime compared with nanosecond precision via time.Time.Equal)
//
// False negatives are possible (files with matching mtime but different
// content, e.g. after a same-second write).  Callers that need certainty
// should not rely on MetaEqual alone — but for overlay/layer diffing this
// heuristic matches the behaviour of tools like rsync and buildkit.
func sameMetadata(a, b fs.FileInfo) bool {
	// Tier 1: inode identity (zero extra syscalls — stat already done).
	sa, oka := a.Sys().(*syscall.Stat_t)
	sb, okb := b.Sys().(*syscall.Stat_t)
	if oka && okb {
		if sa.Dev == sb.Dev && sa.Ino == sb.Ino {
			return true // same physical inode → definitely equal
		}
	}

	// Tier 2: size + mtime heuristic.
	return a.Size() == b.Size() && a.ModTime().Equal(b.ModTime())
}

// isSymlink reports whether info describes a symbolic link.
// Only meaningful when followSymlinks==false (otherwise stat follows the link).
func isSymlink(info fs.FileInfo) bool {
	return info.Mode()&fs.ModeSymlink != 0
}
