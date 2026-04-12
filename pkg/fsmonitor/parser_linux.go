//go:build linux

package fsmonitor

import (
	"fmt"
	"os"
	"unsafe"

	"golang.org/x/sys/unix"
)

// infoHeader is the Go equivalent of struct fanotify_event_info_header
type infoHeader struct {
	Type uint8
	Pad  uint8
	Len  uint16
}

const (
	infoLen = 4 // size of infoHeader

	// Modern Linux (6.14+) constants
	FAN_CLASS_PRE_CONTENT     = 0x00000008
	FAN_PRE_ACCESS            = 0x00100000
	FAN_EVENT_INFO_TYPE_RANGE = 6
)

// infoRange is the Go equivalent of struct fanotify_event_info_range
type infoRange struct {
	Header infoHeader
	Pad    uint32
	Offset uint64
	Count  uint64
}

// eventInfo represents a parsed extra info record from a fanotify event.
type eventInfo struct {
	Type  uint8
	Data  []byte
	Range *infoRange
}

// parseEventMetadata extracts metadata and trailing info records from the buffer.
func parseEvent(data []byte) (*unix.FanotifyEventMetadata, []eventInfo, int, error) {
	if len(data) < unix.FAN_EVENT_METADATA_LEN {
		return nil, nil, 0, fmt.Errorf("buffer too small for metadata")
	}

	meta := (*unix.FanotifyEventMetadata)(unsafe.Pointer(&data[0]))
	if int(meta.Event_len) > len(data) {
		return nil, nil, 0, fmt.Errorf("truncated event")
	}

	var infos []eventInfo
	currentPos := int(unix.FAN_EVENT_METADATA_LEN)

	// If the kernel supports FID reporting, there might be extra info records
	for currentPos+infoLen <= int(meta.Event_len) {
		h := (*infoHeader)(unsafe.Pointer(&data[currentPos]))

		recordLen := int(h.Len)
		if currentPos+recordLen > int(meta.Event_len) {
			break // Corrupted length
		}

		info := eventInfo{
			Type: h.Type,
			Data: data[currentPos+infoLen : currentPos+recordLen],
		}

		// Handle specific info types
		switch h.Type {
		case FAN_EVENT_INFO_TYPE_RANGE:
			if recordLen >= int(unsafe.Sizeof(infoRange{})) {
				r := (*infoRange)(unsafe.Pointer(&data[currentPos]))
				// Copy by value to prevent pointer aliasing to buffer
				safeR := *r
				info.Range = &safeR
			}
		}

		infos = append(infos, info)
		currentPos += recordLen

		// Align to 4 bytes if necessary (kernel does this)
		if currentPos%4 != 0 {
			currentPos += 4 - (currentPos % 4)
		}
	}

	return meta, infos, int(meta.Event_len), nil
}

// getPathFromFD resolves the absolute path of an open file descriptor via /proc.
func getPathFromFD(fd int) (string, error) {
	if fd < 0 {
		return "", fmt.Errorf("invalid fd")
	}
	return os.Readlink(fmt.Sprintf("/proc/self/fd/%d", fd))
}
