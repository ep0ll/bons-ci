//go:build linux

package fshash

import (
	"os"
	"syscall"
)

// openForHash opens absPath for sequential hashing with Linux I/O optimisations
// (SKILL §4):
//
//   - O_NOATIME   — suppresses inode atime updates, avoiding dirty-inode
//                   write-back on every read. Falls back to plain O_RDONLY
//                   on EPERM (non-owner without CAP_FOWNER).
//   - O_CLOEXEC   — prevents fd leaking into child processes.
//   - POSIX_FADV_SEQUENTIAL (2) — hints the kernel to double the read-ahead
//                   window (128 KiB → 256 KiB), increasing throughput on HDDs
//                   and SATA SSDs.
func openForHash(absPath string, size int64) (*os.File, error) {
	fd, err := syscall.Open(absPath,
		syscall.O_RDONLY|syscall.O_NOATIME|syscall.O_CLOEXEC, 0)
	if err != nil {
		// EPERM when not the file owner and lacking CAP_FOWNER.
		fd, err = syscall.Open(absPath, syscall.O_RDONLY|syscall.O_CLOEXEC, 0)
		if err != nil {
			return nil, &os.PathError{Op: "open", Path: absPath, Err: err}
		}
	}
	f := os.NewFile(uintptr(fd), absPath)
	if size > 0 {
		// syscall.SYS_FADVISE64 is arch-specific but defined in the stdlib
		// syscall package for every supported Linux architecture.
		syscall.Syscall6(syscall.SYS_FADVISE64,
			uintptr(fd), 0, uintptr(size), 2, 0, 0)
	}
	return f, nil
}

// releasePageCache advises the kernel (POSIX_FADV_DONTNEED = 4) to evict
// hashed pages from the page cache, preventing backup/scan workloads from
// displacing hot application data. No-op when size == 0.
func releasePageCache(f *os.File, size int64) {
	if size > 0 {
		syscall.Syscall6(syscall.SYS_FADVISE64,
			f.Fd(), 0, uintptr(size), 4, 0, 0)
	}
}
