//go:build linux

package fsmonitor

import (
	"context"
	"fmt"
	"os"
	"path/filepath"
	"sync"
	"sync/atomic"
	"unsafe"

	"golang.org/x/sys/unix"
)

const (
	// eventBatchSize is the number of events to read in one syscall
	eventBatchSize = 4096
	// workerCount is the number of concurrent checksum workers
	workerCount = 4
	// eventChanSize is the size of the internal event buffer
	eventChanSize = 1024
)

type linuxMonitor struct {
	fd     int
	marks  map[string]struct{}
	mu     sync.RWMutex
	engine *eventProcessor

	eventChan chan eventTask
	wg        sync.WaitGroup

	useRange bool // Detect if kernel supports pre-content range
}

type eventTask struct {
	path         string
	mask         uint64
	fd           int // If >= 0, needs to be closed
	offset       uint64
	count        uint64
	hasR         bool
	data         []byte // For range data
	fullChecksum string // For close_write
}

// New creates a new production-ready fanotify monitor.
func New() (Monitor, error) {
	// Initialize fanotify with high-performance flags
	// FAN_CLASS_PRE_CONTENT is the gold standard for byte-level tracking (6.14+)
	// FAN_CLASS_NOTIF is the reliable fallback.
	initFlags := uint(FAN_CLASS_PRE_CONTENT | unix.FAN_CLOEXEC | unix.FAN_UNLIMITED_QUEUE | unix.FAN_UNLIMITED_MARKS)
	eventFlags := uint(os.O_RDONLY | unix.O_LARGEFILE | unix.O_CLOEXEC)

	var useRange bool
	fd, err := unix.FanotifyInit(initFlags, eventFlags)
	if err == nil {
		useRange = true
	} else {
		// Fallback to standard notification
		initFlags = uint(unix.FAN_CLASS_NOTIF | unix.FAN_CLOEXEC | unix.FAN_UNLIMITED_QUEUE | unix.FAN_UNLIMITED_MARKS)
		fd, err = unix.FanotifyInit(initFlags, eventFlags)
		if err != nil {
			// Second fallback for unprivileged
			initFlags = uint(unix.FAN_CLASS_NOTIF | unix.FAN_CLOEXEC)
			fd, err = unix.FanotifyInit(initFlags, eventFlags)
			if err != nil {
				return nil, fmt.Errorf("fsmonitor: init failed: %w", err)
			}
		}
	}

	return &linuxMonitor{
		fd:        fd,
		marks:     make(map[string]struct{}),
		engine:    newEventProcessor(),
		eventChan: make(chan eventTask, eventChanSize),
		useRange:  useRange,
	}, nil
}

func (m *linuxMonitor) Add(path string) error {
	return filepath.Walk(path, func(walkPath string, info os.FileInfo, err error) error {
		if err != nil {
			return err
		}
		if !info.IsDir() {
			return nil
		}

		m.mu.Lock()
		if _, ok := m.marks[walkPath]; ok {
			m.mu.Unlock()
			return nil
		}
		m.mu.Unlock()

		// We watch for ACCESS (reads) and CLOSE_WRITE (completed modifications)
		// If range support is detected, use FAN_PRE_ACCESS for real-time byte tracking.
		mask := uint64(unix.FAN_ACCESS | unix.FAN_CLOSE_WRITE | unix.FAN_EVENT_ON_CHILD)
		if m.useRange {
			mask |= FAN_PRE_ACCESS
		}

		if err := unix.FanotifyMark(m.fd, unix.FAN_MARK_ADD, mask, unix.AT_FDCWD, walkPath); err != nil {
			return fmt.Errorf("fsmonitor: mark failed for %s: %w", walkPath, err)
		}

		m.mu.Lock()
		m.marks[walkPath] = struct{}{}
		m.mu.Unlock()

		return nil
	})
}

func (m *linuxMonitor) Run(ctx context.Context) error {
	// Start workers
	for i := 0; i < workerCount; i++ {
		m.wg.Add(1)
		go m.worker(ctx)
	}

	// Start reader loop
	errChan := make(chan error, 1)
	go func() {
		defer close(m.eventChan)
		defer unix.Close(m.fd)

		buf := make([]byte, eventBatchSize*unix.FAN_EVENT_METADATA_LEN)
		for {
			n, err := unix.Read(m.fd, buf)
			if err != nil {
				if err == unix.EINTR {
					continue
				}
				if err == unix.EBADF {
					return // FD closed
				}
				errChan <- fmt.Errorf("read error: %w", err)
				return
			}

			if n == 0 {
				return
			}

			m.processBuffer(buf[:n])
		}
	}()

	select {
	case <-ctx.Done():
		// Graceful stop
		unix.Close(m.fd) // This breaks the Read loop
		m.wg.Wait()
		return nil
	case err := <-errChan:
		return err
	}
}

func (m *linuxMonitor) processBuffer(buf []byte) {
	pos := 0
	for pos < len(buf) {
		meta, infos, eventLen, err := parseEvent(buf[pos:])
		if err != nil {
			break
		}
		pos += eventLen

		atomic.AddUint64(&m.engine.eventCount, 1)

		if meta.Mask&unix.FAN_Q_OVERFLOW != 0 {
			atomic.AddUint64(&m.engine.overflowCount, 1)
			continue
		}

		// Resolve path from FD
		eventFd := int(meta.Fd)
		if eventFd == unix.FAN_NOFD {
			continue
		}

		// Handle permission events if using non-notification class (PRE_CONTENT)
		// We MUST respond to allowed access even if we take a snapshot.
		if meta.Mask&FAN_PRE_ACCESS != 0 {
			// Response structure to allow access
			resp := unix.FanotifyResponse{
				Fd:       meta.Fd,
				Response: unix.FAN_ALLOW,
			}
			// Write response back to the kernel to unblock the process
			unix.Write(m.fd, ((*[4]byte)(unsafe.Pointer(&resp)))[:])
		}

		path, err := getPathFromFD(eventFd)
		if err != nil {
			unix.Close(eventFd)
			continue
		}

		task := eventTask{path: path, mask: meta.Mask, fd: eventFd}

		// Check for range info
		for _, info := range infos {
			if info.Type == FAN_EVENT_INFO_TYPE_RANGE && info.Range != nil {
				task.offset = info.Range.Offset
				task.count = info.Range.Count
				task.hasR = true
			}
		}

		// Dispatch to workers
		select {
		case m.eventChan <- task:
		default:
			unix.Close(eventFd)
			atomic.AddUint64(&m.engine.overflowCount, 1)
		}
	}
}

func (m *linuxMonitor) worker(ctx context.Context) {
	defer m.wg.Done()
	for task := range m.eventChan {
		m.handleTask(task)
	}
}

func (m *linuxMonitor) handleTask(task eventTask) {
	defer unix.Close(task.fd)

	if task.mask&unix.FAN_ACCESS != 0 || task.mask&FAN_PRE_ACCESS != 0 {
		// If we have range info, read the data synchronously in the worker
		if task.hasR {
			if data, err := readRange(task.fd, task.offset, task.count); err == nil {
				task.data = data
			}
		}
	}

	if task.mask&unix.FAN_CLOSE_WRITE != 0 {
		// Calculate full checksum on close
		if cs, err := calculateSHA256FromFD(task.fd); err == nil {
			task.fullChecksum = cs
		}
	}

	// Ship task to engine for state update
	m.engine.processTask(task)
}

func readRange(fd int, offset, count uint64) ([]byte, error) {
	data := make([]byte, count)
	n, err := unix.Pread(fd, data, int64(offset))
	if err != nil {
		return nil, err
	}
	return data[:n], nil
}

func (m *linuxMonitor) Snapshot() Stats {
	return m.engine.snapshot()
}
