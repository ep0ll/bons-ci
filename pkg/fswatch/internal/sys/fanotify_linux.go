//go:build linux

// Package sys wraps the raw Linux fanotify system calls using only stdlib.
// No code outside this package and watcher_linux.go should call syscall directly.
package sys

import (
	"fmt"
	"os"
	"syscall"
	"unsafe"
)

// ─────────────────────────────────────────────────────────────────────────────
// fanotify_event_metadata — mirrors <linux/fanotify.h>
// ─────────────────────────────────────────────────────────────────────────────

type fanotifyEventMetadata struct {
	EventLen    uint32
	Vers        uint8
	Reserved    uint8
	MetadataLen uint16
	Mask        uint64
	Fd          int32
	Pid         int32
}

// MetadataVersion is the expected Vers field (FANOTIFY_METADATA_VERSION = 3).
const MetadataVersion = uint8(3)

// EventMetadataSize is sizeof(fanotify_event_metadata).
const EventMetadataSize = int(unsafe.Sizeof(fanotifyEventMetadata{}))

// ─────────────────────────────────────────────────────────────────────────────
// fanotify flags from <linux/fanotify.h> and <fcntl.h>
// ─────────────────────────────────────────────────────────────────────────────

const (
	fanClassNotif uint = 0x00000000
	fanCloexec    uint = 0x00000001
	fanNonblock   uint = 0x00000002
	fanMarkAdd    uint = 0x00000001
	fanMarkMount  uint = 0x00000010
	oRdonly            = 0
	oCloexec           = 0x80000
)

// ─────────────────────────────────────────────────────────────────────────────
// Init — create a fanotify notification group
// ─────────────────────────────────────────────────────────────────────────────

// Init creates a fanotify fd. Returns ErrPermission when CAP_SYS_ADMIN absent.
func Init() (int, error) {
	flags := fanClassNotif | fanCloexec | fanNonblock
	eventFlags := uint(oRdonly | oCloexec)

	fd, _, errno := syscall.RawSyscall(syscall.SYS_FANOTIFY_INIT,
		uintptr(flags), uintptr(eventFlags), 0)
	if errno != 0 {
		if errno == syscall.EPERM {
			return -1, fmt.Errorf("fanotify init: CAP_SYS_ADMIN required: %w", errno)
		}
		return -1, fmt.Errorf("fanotify init: %w", errno)
	}
	return int(fd), nil
}

// ─────────────────────────────────────────────────────────────────────────────
// MarkMount — watch an entire mount point
// ─────────────────────────────────────────────────────────────────────────────

// MarkMount registers a watch on the mount containing path.
func MarkMount(fanotifyFD int, mask uint64, path string) error {
	pathPtr, err := syscall.BytePtrFromString(path)
	if err != nil {
		return fmt.Errorf("fanotify mark mount %q: %w", path, err)
	}
	flags := fanMarkAdd | fanMarkMount
	_, _, errno := syscall.RawSyscall6(syscall.SYS_FANOTIFY_MARK,
		uintptr(fanotifyFD),
		uintptr(flags),
		uintptr(mask),
		uintptr(^uintptr(99)), // AT_FDCWD = -100 as uintptr (two's complement)
		uintptr(unsafe.Pointer(pathPtr)),
		0,
	)
	if errno != 0 {
		return fmt.Errorf("fanotify mark mount %q: %w", path, errno)
	}
	return nil
}

// ─────────────────────────────────────────────────────────────────────────────
// ReadEvents — parse a kernel read buffer into EventRecords
// ─────────────────────────────────────────────────────────────────────────────

// EventRecord is one parsed fanotify event, ready for path resolution.
type EventRecord struct {
	Mask uint64
	PID  int32
	FD   int32 // caller must close after FDToPath
}

// ReadEvents reads all available events from fanotifyFD into buf.
// Returns nil (not an error) when no events are pending (EAGAIN).
func ReadEvents(fanotifyFD int, buf []byte) ([]EventRecord, error) {
	n, err := syscall.Read(fanotifyFD, buf)
	if err != nil {
		if err == syscall.EAGAIN || err == syscall.EWOULDBLOCK {
			return nil, nil
		}
		return nil, fmt.Errorf("fanotify read: %w", err)
	}
	if n == 0 {
		return nil, nil
	}
	return parseEventBuf(buf[:n])
}

func parseEventBuf(buf []byte) ([]EventRecord, error) {
	var records []EventRecord
	for len(buf) >= EventMetadataSize {
		meta := (*fanotifyEventMetadata)(unsafe.Pointer(&buf[0]))
		if meta.Vers != MetadataVersion {
			return records, fmt.Errorf("fanotify: unexpected metadata version %d (want %d)",
				meta.Vers, MetadataVersion)
		}
		n := int(meta.EventLen)
		if n < EventMetadataSize || n > len(buf) {
			break
		}
		records = append(records, EventRecord{
			Mask: meta.Mask,
			PID:  meta.Pid,
			FD:   meta.Fd,
		})
		buf = buf[n:]
	}
	return records, nil
}

// ─────────────────────────────────────────────────────────────────────────────
// FDToPath — resolve per-event fd → absolute path
// ─────────────────────────────────────────────────────────────────────────────

// FDToPath resolves fd via /proc/self/fd/<fd> and closes fd unconditionally.
func FDToPath(fd int32) (string, error) {
	defer syscall.Close(int(fd)) //nolint:errcheck
	if fd < 0 {
		return "", nil // overflow events have fd=-1
	}
	path, err := os.Readlink(fmt.Sprintf("/proc/self/fd/%d", fd))
	if err != nil {
		return "", fmt.Errorf("fanotify: resolve fd %d: %w", fd, err)
	}
	return path, nil
}

// ─────────────────────────────────────────────────────────────────────────────
// WaitReadable — poll(2) wrapper with stop-pipe cancellation
// ─────────────────────────────────────────────────────────────────────────────

// pollfd mirrors struct pollfd for the SYS_POLL syscall.
type pollfd struct {
	fd      int32
	events  int16
	revents int16
}

const pollin = int16(0x0001)

// WaitReadable blocks until fanotifyFD has data or stopFD is written.
// Returns (true, nil) when fanotifyFD is readable; (false, nil) when stopFD fires.
func WaitReadable(fanotifyFD, stopFD int) (bool, error) {
	fds := [2]pollfd{
		{fd: int32(fanotifyFD), events: pollin},
		{fd: int32(stopFD), events: pollin},
	}
	for {
		n, _, errno := syscall.RawSyscall(syscall.SYS_POLL,
			uintptr(unsafe.Pointer(&fds[0])),
			2,
			uintptr(^uint(0)), // block indefinitely
		)
		if errno != 0 {
			if errno == syscall.EINTR {
				continue
			}
			return false, fmt.Errorf("poll: %w", errno)
		}
		if int(n) == 0 {
			continue
		}
		if fds[1].revents&pollin != 0 {
			return false, nil
		}
		if fds[0].revents&pollin != 0 {
			return true, nil
		}
	}
}

// ─────────────────────────────────────────────────────────────────────────────
// StopPipe — self-pipe cancellation pair
// ─────────────────────────────────────────────────────────────────────────────

// StopPipe creates a pipe for WaitReadable cancellation.
func StopPipe() (readFD, writeFD int, err error) {
	var fds [2]int
	if err = syscall.Pipe2(fds[:], syscall.O_CLOEXEC|syscall.O_NONBLOCK); err != nil {
		return -1, -1, fmt.Errorf("fanotify: stop pipe: %w", err)
	}
	return fds[0], fds[1], nil
}

// SignalStop writes one byte to writeFD to unblock WaitReadable.
func SignalStop(writeFD int) { syscall.Write(writeFD, []byte{1}) } //nolint:errcheck

// Close closes fd.
func Close(fd int) { syscall.Close(fd) } //nolint:errcheck
