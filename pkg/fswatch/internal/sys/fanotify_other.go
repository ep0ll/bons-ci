//go:build !linux

// Package sys wraps raw fanotify syscalls. This file is the non-Linux stub.
package sys

import "errors"

// ErrNotSupported is returned on non-Linux platforms.
var ErrNotSupported = errors.New("fanotify: not supported on this platform")

// MetadataVersion is 0 on non-Linux platforms.
const MetadataVersion = uint8(0)

// EventMetadataSize is 0 on non-Linux platforms.
const EventMetadataSize = 0

// EventRecord is a no-op type on non-Linux platforms.
type EventRecord struct {
	Mask uint64
	PID  int32
	FD   int32
}

func Init() (int, error)                                { return -1, ErrNotSupported }
func MarkMount(_ int, _ uint64, _ string) error         { return ErrNotSupported }
func ReadEvents(_ int, _ []byte) ([]EventRecord, error) { return nil, ErrNotSupported }
func FDToPath(_ int32) (string, error)                  { return "", ErrNotSupported }
func WaitReadable(_, _ int) (bool, error)               { return false, ErrNotSupported }
func StopPipe() (int, int, error)                       { return -1, -1, ErrNotSupported }
func SignalStop(_ int)                                  {}
func Close(_ int)                                       {}
