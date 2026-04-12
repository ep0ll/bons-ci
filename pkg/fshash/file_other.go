//go:build !linux

package fshash

import "os"

// openForHash opens absPath for reading.  On non-Linux platforms no special
// flags are used.
func openForHash(absPath string, _ int64) (*os.File, error) {
	return os.Open(absPath) //nolint:gosec
}

// releasePageCache is a no-op on non-Linux platforms.
func releasePageCache(_ *os.File, _ int64) {}
