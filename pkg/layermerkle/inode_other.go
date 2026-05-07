//go:build !linux

package layermerkle

import "io/fs"

// inodeFromFileInfo returns a zero InodeKey on non-Linux platforms.
// Hard-link deduplication is not supported outside Linux.
func inodeFromFileInfo(_ fs.FileInfo) InodeKey {
	return InodeKey{}
}
