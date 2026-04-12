//go:build linux

package fsmonitor

import (
	"crypto/sha256"
	"encoding/hex"
	"io"
	"os"
	"sync"
)

var bufferPool = sync.Pool{
	New: func() interface{} {
		return make([]byte, 32*1024) // 32KB buffer
	},
}

// calculateSHA256 computes the SHA256 checksum of the file at the given path.
func calculateSHA256(path string) (string, error) {
	f, err := os.Open(path)
	if err != nil {
		return "", err
	}
	defer f.Close()

	h := sha256.New()
	buf := bufferPool.Get().([]byte)
	defer bufferPool.Put(buf)

	if _, err := io.CopyBuffer(h, f, buf); err != nil {
		return "", err
	}

	return hex.EncodeToString(h.Sum(nil)), nil
}

// calculateSHA256FromFD computes the SHA256 checksum using an open file descriptor.
func calculateSHA256FromFD(fd int) (string, error) {
	f := os.NewFile(uintptr(fd), "")
	if f == nil {
		return "", os.ErrInvalid
	}
	// We don't close f as the caller (handleEvent) manages the FD

	// Seek to beginning if possible
	if _, err := f.Seek(0, io.SeekStart); err != nil {
		// Non-seekable file, just read what's left or return error
		// For fanotify FAN_CLOSE_WRITE, we usually want the whole file.
	}

	h := sha256.New()
	buf := bufferPool.Get().([]byte)
	defer bufferPool.Put(buf)

	if _, err := io.CopyBuffer(h, f, buf); err != nil {
		return "", err
	}

	return hex.EncodeToString(h.Sum(nil)), nil
}
