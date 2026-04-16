//go:build !linux

package fshash

import "os"

func openForHash(absPath string, _ int64) (*os.File, error) { return os.Open(absPath) } //nolint:gosec
func releasePageCache(_ *os.File, _ int64)                  {}
