//go:build linux

package layermerkle

import (
	"io/fs"
	"syscall"
)

// inodeFromFileInfo extracts the device+inode pair from a FileInfo on Linux.
func inodeFromFileInfo(info fs.FileInfo) InodeKey {
	stat, ok := info.Sys().(*syscall.Stat_t)
	if !ok {
		return InodeKey{}
	}
	return InodeKey{
		Dev:   stat.Dev,
		Inode: stat.Ino,
	}
}
