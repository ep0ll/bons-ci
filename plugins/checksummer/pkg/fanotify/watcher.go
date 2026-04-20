//go:build linux

// Package fanotify wraps the Linux fanotify(7) API.
//
// Uses golang.org/x/sys/unix for all syscall operations.
// fanotify is preferred over inotify for overlayfs monitoring because:
//   - Events include an open fd to the file (no TOCTOU race for hashing).
//   - FAN_MARK_MOUNT watches an entire overlay mergedview at once.
//   - The fd is handed directly to unix.NameToHandleAt + the hasher.
package fanotify

import (
	"context"
	"fmt"
	"os"
	"sync"
	"unsafe"

	"golang.org/x/sys/unix"

	"github.com/bons/bons-ci/plugins/checksummer/pkg/hooks"
	"github.com/bons/bons-ci/plugins/checksummer/pkg/metrics"
)

// ─────────────────────────── Event masks ─────────────────────────────────────

const (
	MaskAccess       = unix.FAN_ACCESS
	MaskOpen         = unix.FAN_OPEN
	MaskOpenExec     = unix.FAN_OPEN_EXEC
	MaskCloseWrite   = unix.FAN_CLOSE_WRITE
	MaskCloseNoWrite = unix.FAN_CLOSE_NOWRITE
	DefaultMask      = MaskOpen | MaskOpenExec

	MarkMount      = unix.FAN_MARK_ADD | unix.FAN_MARK_MOUNT
	MarkFilesystem = unix.FAN_MARK_ADD | unix.FAN_MARK_FILESYSTEM
	MarkDir        = unix.FAN_MARK_ADD
)

// ─────────────────────────── Mark ────────────────────────────────────────────

type Mark struct {
	Path  string
	Flags uint
	Mask  uint64
}

func DefaultMark(mergedPath string) Mark {
	return Mark{Path: mergedPath, Flags: MarkMount, Mask: DefaultMask}
}

// ─────────────────────────── Event ───────────────────────────────────────────

// Event is a single fanotify file-access notification.
// Fd is owned by the receiver and MUST be closed after use.
type Event struct {
	Fd   int
	Pid  int32
	Mask uint64
	Path string
}

func (e *Event) Close() error {
	if e.Fd <= 0 {
		return nil
	}
	err := unix.Close(e.Fd)
	e.Fd = -1
	return err
}

func (e *Event) ResolvePath() (string, error) {
	if e.Fd <= 0 || e.Path != "" {
		return e.Path, nil
	}
	link := fmt.Sprintf("/proc/self/fd/%d", e.Fd)
	path, err := func() (string, error) {
		buf := make([]byte, unix.PathMax)
		n, err := unix.Readlink(link, buf)
		if err != nil {
			return "", err
		}
		return string(buf[:n]), nil
	}()
	if err != nil {
		return "", fmt.Errorf("readlink %s: %w", link, err)
	}
	e.Path = path
	return path, nil
}

func (e *Event) IsOpen() bool   { return e.Mask&(MaskOpen|MaskOpenExec) != 0 }
func (e *Event) IsAccess() bool { return e.Mask&MaskAccess != 0 }
func (e *Event) IsExec() bool   { return e.Mask&MaskOpenExec != 0 }

func CmdlineForPid(pid int32) string {
	data, _ := os.ReadFile(fmt.Sprintf("/proc/%d/cmdline", pid))
	for i, b := range data {
		if b == 0 {
			data[i] = ' '
		}
	}
	return string(data)
}

// ─────────────────────────── Options / Handler ────────────────────────────────

type EventHandler func(ctx context.Context, e *Event)

type Options struct {
	Marks              []Mark
	Workers            int
	EventBufSize       int
	ReadBufSize        int
	FanotifyInitFlags  uint
	FanotifyEventFlags uint
	Hooks              *hooks.HookSet
	Metrics            *metrics.Recorder
	Handler            EventHandler
}

func applyDefaults(o *Options) {
	if o.Workers <= 0 {
		o.Workers = 16
	}
	if o.EventBufSize <= 0 {
		o.EventBufSize = 4096
	}
	if o.ReadBufSize <= 0 {
		o.ReadBufSize = 4096 * int(unsafe.Sizeof(unix.FanotifyEventMetadata{}))
	}
	if o.FanotifyInitFlags == 0 {
		o.FanotifyInitFlags = unix.FAN_CLOEXEC | unix.FAN_NONBLOCK | unix.FAN_CLASS_NOTIF
	}
	if o.FanotifyEventFlags == 0 {
		o.FanotifyEventFlags = unix.O_RDONLY | unix.O_LARGEFILE | unix.O_CLOEXEC
	}
	if o.Hooks == nil {
		o.Hooks = hooks.NewHookSet()
	}
	if o.Metrics == nil {
		o.Metrics = &metrics.Recorder{}
	}
}

// ─────────────────────────── Watcher ─────────────────────────────────────────

type Watcher struct {
	opts     Options
	faFd     int
	epollFd  int
	events   chan *Event
	cancel   context.CancelFunc
	wg       sync.WaitGroup
	once     sync.Once
	stopOnce sync.Once
}

func New(opts Options) (*Watcher, error) {
	applyDefaults(&opts)
	if opts.Handler == nil {
		return nil, fmt.Errorf("fanotify: Handler must not be nil")
	}
	return &Watcher{
		opts:    opts,
		faFd:    -1,
		epollFd: -1,
		events:  make(chan *Event, opts.EventBufSize),
	}, nil
}

func (w *Watcher) Start(ctx context.Context) error {
	var err error
	w.once.Do(func() { err = w.start(ctx) })
	return err
}

func (w *Watcher) start(ctx context.Context) error {
	fd, err := unix.FanotifyInit(w.opts.FanotifyInitFlags, w.opts.FanotifyEventFlags)
	if err != nil {
		return fmt.Errorf("fanotify_init: %w", err)
	}
	w.faFd = fd

	epfd, err := unix.EpollCreate1(unix.EPOLL_CLOEXEC)
	if err != nil {
		unix.Close(fd)
		return fmt.Errorf("epoll_create1: %w", err)
	}
	w.epollFd = epfd

	if err := unix.EpollCtl(epfd, unix.EPOLL_CTL_ADD, fd, &unix.EpollEvent{
		Events: unix.EPOLLIN,
		Fd:     int32(fd),
	}); err != nil {
		w.closeFDs()
		return fmt.Errorf("epoll_ctl: %w", err)
	}

	for _, m := range w.opts.Marks {
		if err := w.installMark(m); err != nil {
			w.closeFDs()
			return err
		}
	}

	pctx, cancel := context.WithCancel(ctx)
	w.cancel = cancel

	w.wg.Add(1)
	go w.readLoop(pctx)
	for i := 0; i < w.opts.Workers; i++ {
		w.wg.Add(1)
		go w.worker(pctx)
	}
	return nil
}

func (w *Watcher) Stop() {
	w.stopOnce.Do(func() {
		if w.cancel != nil {
			w.cancel()
		}
		w.closeFDs()
		close(w.events)
		w.wg.Wait()
	})
}

func (w *Watcher) AddMark(m Mark) error  { return w.installMark(m) }
func (w *Watcher) FanotifyFd() int       { return w.faFd }
func (w *Watcher) Hooks() *hooks.HookSet { return w.opts.Hooks }

func (w *Watcher) RemoveMark(m Mark) error {
	flags := (m.Flags &^ uint(unix.FAN_MARK_ADD)) | uint(unix.FAN_MARK_REMOVE)
	return unix.FanotifyMark(w.faFd, flags, m.Mask, unix.AT_FDCWD, m.Path)
}

func (w *Watcher) installMark(m Mark) error {
	mask := m.Mask
	if mask == 0 {
		mask = DefaultMask
	}
	if err := unix.FanotifyMark(w.faFd, m.Flags, mask, unix.AT_FDCWD, m.Path); err != nil {
		return fmt.Errorf("fanotify_mark %q: %w", m.Path, err)
	}
	return nil
}

// ─────────────────────────── Read loop ───────────────────────────────────────

func (w *Watcher) readLoop(ctx context.Context) {
	defer w.wg.Done()

	buf := make([]byte, w.opts.ReadBufSize)
	epollEvents := make([]unix.EpollEvent, 1)
	metaSize := int(unsafe.Sizeof(unix.FanotifyEventMetadata{}))

	for {
		if ctx.Err() != nil {
			return
		}
		n, err := unix.EpollWait(w.epollFd, epollEvents, 200)
		if err != nil {
			if err == unix.EINTR {
				continue
			}
			return
		}
		if n == 0 {
			continue
		}

		// fanotify fd is not seekable – use Read, not Pread.
		nr, err := unix.Read(w.faFd, buf)
		if err != nil {
			if err == unix.EAGAIN || err == unix.EWOULDBLOCK {
				continue
			}
			return
		}
		if nr <= 0 {
			continue
		}

		for i := 0; i+metaSize <= nr; {
			meta := (*unix.FanotifyEventMetadata)(unsafe.Pointer(&buf[i]))
			i += int(meta.Event_len)

			if meta.Vers != unix.FANOTIFY_METADATA_VERSION {
				continue
			}
			if meta.Fd < 0 {
				continue // FAN_Q_OVERFLOW
			}

			evt := &Event{Fd: int(meta.Fd), Pid: meta.Pid, Mask: meta.Mask}
			w.opts.Metrics.EventsReceived.Inc()

			if err := w.opts.Hooks.OnFilter.Execute(ctx, hooks.EventPayload{
				Fd: evt.Fd, Pid: evt.Pid, Mask: evt.Mask,
			}, hooks.StopOnError); err != nil {
				_ = evt.Close()
				w.opts.Metrics.EventsFiltered.Inc()
				continue
			}

			select {
			case w.events <- evt:
			default:
				_ = evt.Close()
				w.opts.Metrics.EventsDropped.Inc()
			}
		}
	}
}

func (w *Watcher) worker(ctx context.Context) {
	defer w.wg.Done()
	for evt := range w.events {
		if ctx.Err() != nil {
			_ = evt.Close()
			continue
		}
		_ = w.opts.Hooks.OnEvent.Execute(ctx, hooks.EventPayload{
			Fd: evt.Fd, Pid: evt.Pid, Mask: evt.Mask,
		}, hooks.ContinueOnError)
		w.opts.Handler(ctx, evt)
		if evt.Fd > 0 {
			_ = evt.Close()
		}
	}
}

func (w *Watcher) closeFDs() {
	if w.faFd > 0 {
		_ = unix.Close(w.faFd)
		w.faFd = -1
	}
	if w.epollFd > 0 {
		_ = unix.Close(w.epollFd)
		w.epollFd = -1
	}
}
