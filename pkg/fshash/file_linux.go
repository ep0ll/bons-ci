//go:build linux

package fshash

import (
	"os"
	"syscall"
)

// openForHash opens absPath for sequential hashing using Linux-specific flags:
//
//   - O_NOATIME  — avoids updating the atime inode field, eliminating a
//     dirty-inode write-back on every read.  Requires ownership or
//     CAP_FOWNER; falls back to plain O_RDONLY on EPERM.
//   - O_CLOEXEC  — prevents fd leaking to child processes.
//   - POSIX_FADV_SEQUENTIAL — hints the kernel to double the read-ahead
//     window, increasing disk bandwidth utilisation.
//
// SKILL §4.
func openForHash(absPath string, size int64) (*os.File, error) {
	const oNoatime = syscall.O_NOATIME

	fd, err := syscall.Open(absPath, syscall.O_RDONLY|oNoatime|syscall.O_CLOEXEC, 0)
	if err != nil {
		// EPERM if caller is not the file owner and lacks CAP_FOWNER.
		fd, err = syscall.Open(absPath, syscall.O_RDONLY|syscall.O_CLOEXEC, 0)
		if err != nil {
			return nil, &os.PathError{Op: "open", Path: absPath, Err: err}
		}
	}

	f := os.NewFile(uintptr(fd), absPath)

	// POSIX_FADV_SEQUENTIAL = 2 on all Linux architectures.
	if size > 0 {
		_ = syscall.Fadvise(fd, 0, size, 2)
	}
	return f, nil
}

// releasePageCache hints the kernel to drop the file's pages from the page
// cache after hashing.  This prevents backup/scan workloads from evicting
// hot application data.
//
// SKILL §4: POSIX_FADV_DONTNEED = 4.
func releasePageCache(f *os.File, size int64) {
	if size > 0 {
		_ = syscall.Fadvise(int(f.Fd()), 0, size, 4)
	}
}
