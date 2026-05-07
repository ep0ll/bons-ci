---
name: golang-linux
description: >
  Comprehensive Linux systems programming in Go: syscalls, namespaces (user/pid/net/mount/ipc/uts),
  cgroups v2 (memory/cpu/io/pid limits), signals, file descriptors, epoll, inotify, proc/sys
  filesystem, Linux capabilities, seccomp BPF, eBPF (cilium/ebpf), NUMA, hugepages, io_uring,
  socket programming (SO_REUSEPORT, TCP tuning), process management, daemonization, and
  container runtime internals. Use for any Go program interacting with Linux kernel interfaces.
  Cross-references: docker-containerd/SKILL.md, security/SKILL.md, performance/SKILL.md.
---

# Go Linux Systems Programming — Complete Reference

## 1. Syscall Layer — Foundation

```go
// ALWAYS use golang.org/x/sys/unix — never syscall (deprecated, incomplete)
import "golang.org/x/sys/unix"

// Direct syscall: SYS_xxx constants
// Prefer high-level wrappers when they exist; drop to raw syscall only when needed

// File operations with O_CLOEXEC (mandatory in multi-process code)
fd, err := unix.Open("/var/run/myapp.lock",
    unix.O_RDWR|unix.O_CREAT|unix.O_CLOEXEC, 0644)
if err != nil { return fmt.Errorf("open lock: %w", err) }
defer unix.Close(fd)

// Essential flags:
// O_CLOEXEC  — close fd on exec() (prevents fd leaks to child processes)
// O_NONBLOCK — non-blocking I/O (pair with epoll)
// O_NOFOLLOW — don't follow symlinks (security: prevent TOCTOU)
// O_PATH     — open path without file access (for fstatat, etc.)

// Atomic file write (rename trick — guaranteed on Linux)
func atomicWrite(path string, data []byte, mode os.FileMode) error {
    dir := filepath.Dir(path)
    tmp, err := os.CreateTemp(dir, ".tmp-")
    if err != nil { return fmt.Errorf("atomicWrite.CreateTemp: %w", err) }
    tmpPath := tmp.Name()

    if _, err := tmp.Write(data); err != nil {
        tmp.Close(); os.Remove(tmpPath)
        return fmt.Errorf("atomicWrite.Write: %w", err)
    }
    if err := tmp.Chmod(mode); err != nil {
        tmp.Close(); os.Remove(tmpPath)
        return err
    }
    if err := tmp.Sync(); err != nil { // fsync before rename
        tmp.Close(); os.Remove(tmpPath)
        return fmt.Errorf("atomicWrite.Sync: %w", err)
    }
    tmp.Close()
    return os.Rename(tmpPath, path) // atomic on same filesystem
}
```

---

## 2. Namespaces — Complete Reference

```go
// Linux namespace types and their CLONE_* flags
// CLONE_NEWUSER  — user/group ID mapping (unprivileged container root)
// CLONE_NEWPID   — pid namespace (process tree isolation)
// CLONE_NEWNET   — network namespace (separate interfaces, routes, iptables)
// CLONE_NEWNS    — mount namespace (separate mount tree)
// CLONE_NEWIPC   — IPC namespace (separate message queues, semaphores, shm)
// CLONE_NEWUTS   — UTS namespace (separate hostname/domainname)
// CLONE_NEWCGROUP — cgroup namespace (separate cgroup root)
// CLONE_NEWTIME  — time namespace (separate clock offsets, Go 1.21+)

// Create isolated child process
func SpawnIsolated(cmd string, args []string) (*exec.Cmd, error) {
    c := exec.Command(cmd, args...)
    c.SysProcAttr = &unix.SysProcAttr{
        Cloneflags: unix.CLONE_NEWUSER |
                    unix.CLONE_NEWPID  |
                    unix.CLONE_NEWNET  |
                    unix.CLONE_NEWNS   |
                    unix.CLONE_NEWIPC  |
                    unix.CLONE_NEWUTS,
        // Map container UID 0 → host UID 1000
        UidMappings: []unix.SysProcIDMap{
            {ContainerID: 0, HostID: os.Getuid(), Size: 1},
        },
        GidMappings: []unix.SysProcIDMap{
            {ContainerID: 0, HostID: os.Getgid(), Size: 1},
        },
        // New session leader (detach from terminal)
        Setsid: true,
        // Set controlling terminal
        Setctty: false,
    }
    return c, c.Start()
}

// Enter existing namespace (e.g., enter container's network namespace)
func WithNetNS(pid int, fn func() error) error {
    // Open namespace file
    nsPath := fmt.Sprintf("/proc/%d/ns/net", pid)
    nsFile, err := os.Open(nsPath)
    if err != nil { return fmt.Errorf("open netns %d: %w", pid, err) }
    defer nsFile.Close()

    // Save current namespace
    selfNS, err := os.Open("/proc/self/ns/net")
    if err != nil { return err }
    defer selfNS.Close()

    // CRITICAL: namespace ops are per-thread, not per-goroutine
    runtime.LockOSThread()
    defer runtime.UnlockOSThread()

    // Enter target namespace
    if err := unix.Setns(int(nsFile.Fd()), unix.CLONE_NEWNET); err != nil {
        return fmt.Errorf("setns: %w", err)
    }
    // Ensure we restore original namespace
    defer unix.Setns(int(selfNS.Fd()), unix.CLONE_NEWNET)

    return fn()
}

// Create and configure new network namespace
func NewNetNS(name string) error {
    // Create named netns (stored in /run/netns/)
    nsDir := "/run/netns"
    os.MkdirAll(nsDir, 0755)
    nsPath := filepath.Join(nsDir, name)

    // Create bind mount point
    f, err := os.Create(nsPath)
    if err != nil { return err }
    f.Close()

    // Unshare creates new network namespace
    if err := unix.Unshare(unix.CLONE_NEWNET); err != nil { return err }

    // Bind mount /proc/self/ns/net → nsPath for persistence
    return unix.Mount("/proc/self/ns/net", nsPath, "bind", unix.MS_BIND, "")
}

// Mount namespace — pivot_root for container isolation
func PivotRoot(newRoot, putOld string) error {
    // 1. Bind mount newRoot to itself (required for pivot_root)
    if err := unix.Mount(newRoot, newRoot, "", unix.MS_BIND|unix.MS_REC, ""); err != nil {
        return fmt.Errorf("bind mount: %w", err)
    }
    // 2. Create putold directory
    if err := os.MkdirAll(putOld, 0700); err != nil { return err }
    // 3. Pivot
    if err := unix.PivotRoot(newRoot, putOld); err != nil {
        return fmt.Errorf("pivot_root: %w", err)
    }
    // 4. Change to new root
    if err := unix.Chdir("/"); err != nil { return err }
    // 5. Unmount old root
    putOldRel := "/" + filepath.Base(putOld)
    if err := unix.Unmount(putOldRel, unix.MNT_DETACH); err != nil {
        return fmt.Errorf("unmount old root: %w", err)
    }
    return os.Remove(putOldRel)
}
```

---

## 3. Cgroups v2 — Complete API

```go
const cgroupRoot = "/sys/fs/cgroup"

// Cgroup v2 controller
type Cgroup struct {
    path string
}

// NewCgroup creates a new cgroup (idempotent)
func NewCgroup(name string) (*Cgroup, error) {
    path := filepath.Join(cgroupRoot, name)
    if err := os.MkdirAll(path, 0755); err != nil {
        return nil, fmt.Errorf("NewCgroup(%s): %w", name, err)
    }
    return &Cgroup{path: path}, nil
}

// --- Memory Controller ---

// SetMemoryMax sets hard memory limit (OOM kill at this point)
func (c *Cgroup) SetMemoryMax(limitBytes int64) error {
    return c.writeFile("memory.max", fmt.Sprintf("%d", limitBytes))
}

// SetMemoryHigh sets soft limit (process throttled above this)
func (c *Cgroup) SetMemoryHigh(limitBytes int64) error {
    return c.writeFile("memory.high", fmt.Sprintf("%d", limitBytes))
}

// SetMemorySwapMax disables swap (0 = no swap)
func (c *Cgroup) SetMemorySwapMax(limitBytes int64) error {
    return c.writeFile("memory.swap.max", fmt.Sprintf("%d", limitBytes))
}

// SetMemoryOOMGroup — kill entire cgroup on OOM (not just triggering process)
func (c *Cgroup) SetMemoryOOMGroup(enabled bool) error {
    v := "0"; if enabled { v = "1" }
    return c.writeFile("memory.oom.group", v)
}

// ReadMemoryUsage returns current memory usage in bytes
func (c *Cgroup) ReadMemoryUsage() (uint64, error) {
    return c.readUint64("memory.current")
}

// ReadMemoryPressure reads memory pressure events
func (c *Cgroup) ReadMemoryPressure() (string, error) {
    return c.readFile("memory.pressure")
}

// --- CPU Controller ---

// SetCPUMax sets CPU quota: maxMicros per periodMicros
// e.g., SetCPUMax(50000, 100000) = 50% of one CPU
func (c *Cgroup) SetCPUMax(maxMicros, periodMicros int64) error {
    return c.writeFile("cpu.max", fmt.Sprintf("%d %d", maxMicros, periodMicros))
}

// SetCPUWeight sets relative weight (100 = default, 1-10000)
func (c *Cgroup) SetCPUWeight(weight int) error {
    return c.writeFile("cpu.weight", strconv.Itoa(weight))
}

// ReadCPUStats reads CPU statistics
func (c *Cgroup) ReadCPUStats() (map[string]uint64, error) {
    data, err := c.readFile("cpu.stat")
    if err != nil { return nil, err }
    stats := make(map[string]uint64)
    for _, line := range strings.Split(data, "\n") {
        parts := strings.Fields(line)
        if len(parts) == 2 {
            n, _ := strconv.ParseUint(parts[1], 10, 64)
            stats[parts[0]] = n
        }
    }
    return stats, nil
}

// --- I/O Controller ---

// SetIOMax sets read/write BPS and IOPS limits per device
func (c *Cgroup) SetIOMax(major, minor int, rbps, wbps, riops, wiops int64) error {
    v := fmt.Sprintf("%d:%d rbps=%d wbps=%d riops=%d wiops=%d",
        major, minor, rbps, wbps, riops, wiops)
    return c.writeFile("io.max", v)
}

// ReadIOPressure reads I/O pressure
func (c *Cgroup) ReadIOPressure() (string, error) {
    return c.readFile("io.pressure")
}

// --- PID Controller ---

// SetPIDMax limits number of processes in cgroup
func (c *Cgroup) SetPIDMax(max int) error {
    return c.writeFile("pids.max", strconv.Itoa(max))
}

// --- Process Management ---

// AddPID moves a process into this cgroup
func (c *Cgroup) AddPID(pid int) error {
    return c.writeFile("cgroup.procs", strconv.Itoa(pid))
}

// AddThread moves a thread (not whole process) into this cgroup
func (c *Cgroup) AddThread(tid int) error {
    return c.writeFile("cgroup.threads", strconv.Itoa(tid))
}

// ListPIDs returns all PIDs in this cgroup
func (c *Cgroup) ListPIDs() ([]int, error) {
    data, err := c.readFile("cgroup.procs")
    if err != nil { return nil, err }
    var pids []int
    for _, line := range strings.Split(strings.TrimSpace(data), "\n") {
        if line == "" { continue }
        pid, err := strconv.Atoi(line)
        if err != nil { return nil, err }
        pids = append(pids, pid)
    }
    return pids, nil
}

// EnableControllers enables sub-controllers
func (c *Cgroup) EnableControllers(controllers ...string) error {
    return c.writeFile("cgroup.subtree_control",
        "+"+strings.Join(controllers, " +"))
}

// Freeze freezes all processes in cgroup (SIGSTOP equivalent)
func (c *Cgroup) Freeze() error { return c.writeFile("cgroup.freeze", "1") }
func (c *Cgroup) Thaw() error   { return c.writeFile("cgroup.freeze", "0") }

// Kill sends signal to all processes in cgroup atomically
func (c *Cgroup) Kill() error { return c.writeFile("cgroup.kill", "1") }

// Delete removes cgroup (must be empty)
func (c *Cgroup) Delete() error { return unix.Rmdir(c.path) }

// --- Pressure Stall Information (PSI) ---

// WatchPressure sets up pressure notifications
func (c *Cgroup) WatchPressure(resource, level string, window, threshold time.Duration) (<-chan struct{}, error) {
    path := filepath.Join(c.path, resource+".pressure")
    f, err := os.OpenFile(path, os.O_RDWR, 0)
    if err != nil { return nil, err }

    trigger := fmt.Sprintf("%s %d %d",
        level,
        threshold.Microseconds(),
        window.Microseconds())

    if _, err := f.WriteString(trigger); err != nil {
        f.Close(); return nil, err
    }

    ch := make(chan struct{}, 1)
    go func() {
        defer f.Close()
        buf := make([]byte, 1)
        for {
            if _, err := f.Read(buf); err != nil { return }
            select { case ch <- struct{}{}: default: }
        }
    }()
    return ch, nil
}

// --- Internal helpers ---
func (c *Cgroup) writeFile(name, value string) error {
    path := filepath.Join(c.path, name)
    if err := os.WriteFile(path, []byte(value), 0); err != nil {
        return fmt.Errorf("cgroup.write(%s=%s): %w", name, value, err)
    }
    return nil
}
func (c *Cgroup) readFile(name string) (string, error) {
    data, err := os.ReadFile(filepath.Join(c.path, name))
    return strings.TrimSpace(string(data)), err
}
func (c *Cgroup) readUint64(name string) (uint64, error) {
    s, err := c.readFile(name)
    if err != nil { return 0, err }
    return strconv.ParseUint(s, 10, 64)
}
```

---

## 4. Signal Handling — Production Patterns

```go
// Complete signal handling for production daemons
func RunWithSignals(ctx context.Context, app *App) error {
    // Use signal.NotifyContext for clean cancellation
    ctx, stop := signal.NotifyContext(ctx,
        unix.SIGTERM, // graceful shutdown (k8s, systemd)
        unix.SIGINT,  // Ctrl+C
    )
    defer stop()

    // SIGHUP: config reload (do NOT use NotifyContext — must not cancel)
    hupCh := make(chan os.Signal, 1)
    signal.Notify(hupCh, unix.SIGHUP)
    defer signal.Stop(hupCh)

    // SIGUSR1: toggle debug logging
    usr1Ch := make(chan os.Signal, 1)
    signal.Notify(usr1Ch, unix.SIGUSR1)
    defer signal.Stop(usr1Ch)

    // SIGUSR2: trigger goroutine dump
    usr2Ch := make(chan os.Signal, 1)
    signal.Notify(usr2Ch, unix.SIGUSR2)
    defer signal.Stop(usr2Ch)

    // Start application
    errCh := make(chan error, 1)
    go func() { errCh <- app.Run(ctx) }()

    for {
        select {
        case <-ctx.Done():
            slog.Info("shutdown signal received, draining...")
            shutCtx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
            defer cancel()
            return app.Shutdown(shutCtx)

        case <-hupCh:
            slog.Info("SIGHUP received, reloading config")
            if err := app.ReloadConfig(); err != nil {
                slog.Error("config reload failed", "err", err)
            }

        case <-usr1Ch:
            slog.Info("SIGUSR1: toggling debug logging")
            app.ToggleDebugLogging()

        case <-usr2Ch:
            slog.Info("SIGUSR2: dumping goroutine stacks")
            buf := make([]byte, 1<<20)
            n := runtime.Stack(buf, true)
            slog.Info("goroutine dump", "stacks", string(buf[:n]))

        case err := <-errCh:
            return err
        }
    }
}

// Child process reaping (avoid zombie processes)
func reapChildren() {
    ch := make(chan os.Signal, 1)
    signal.Notify(ch, unix.SIGCHLD)
    go func() {
        for range ch {
            for {
                var wstatus unix.WaitStatus
                pid, err := unix.Wait4(-1, &wstatus, unix.WNOHANG, nil)
                if pid <= 0 || err != nil { break }
                slog.Debug("reaped child", "pid", pid, "status", wstatus)
            }
        }
    }()
}
```

---

## 5. epoll — Event-Driven I/O

```go
// epoll: efficient event notification for many file descriptors
// Go's runtime uses this internally — use directly for custom I/O loops

type EPoll struct {
    fd    int
    conns map[int]net.Conn  // fd → connection
    mu    sync.RWMutex
}

func NewEPoll() (*EPoll, error) {
    fd, err := unix.EpollCreate1(unix.EPOLL_CLOEXEC)
    if err != nil { return nil, fmt.Errorf("epoll_create1: %w", err) }
    return &EPoll{fd: fd, conns: make(map[int]net.Conn)}, nil
}

func (ep *EPoll) Add(conn net.Conn) error {
    fd := int(conn.(*net.TCPConn).File().Fd()) // get raw fd
    err := unix.EpollCtl(ep.fd, unix.EPOLL_CTL_ADD, fd, &unix.EpollEvent{
        Events: unix.EPOLLIN | unix.EPOLLRDHUP | unix.EPOLLET, // edge-triggered
        Fd:     int32(fd),
    })
    if err != nil { return fmt.Errorf("epoll_ctl ADD: %w", err) }
    ep.mu.Lock()
    ep.conns[fd] = conn
    ep.mu.Unlock()
    return nil
}

func (ep *EPoll) Wait(events []unix.EpollEvent, timeout int) (int, error) {
    n, err := unix.EpollWait(ep.fd, events, timeout)
    if err != nil && !errors.Is(err, unix.EINTR) {
        return 0, fmt.Errorf("epoll_wait: %w", err)
    }
    return n, nil
}

func (ep *EPoll) Remove(conn net.Conn) error {
    fd := int(conn.(*net.TCPConn).File().Fd())
    ep.mu.Lock()
    delete(ep.conns, fd)
    ep.mu.Unlock()
    return unix.EpollCtl(ep.fd, unix.EPOLL_CTL_DEL, fd, nil)
}

func (ep *EPoll) Close() error { return unix.Close(ep.fd) }
```

---

## 6. inotify — Filesystem Events

```go
// Production-quality filesystem watcher using inotify
type Watcher struct {
    fd      int
    watches map[string]int         // path → watch descriptor
    rWatches map[int]string        // watch descriptor → path
    Events  chan WatchEvent
    Errors  chan error
    done    chan struct{}
    mu      sync.Mutex
}

type WatchEvent struct {
    Path   string
    Name   string   // filename within watched dir
    Op     WatchOp
}

type WatchOp uint32
const (
    Create WatchOp = iota
    Write
    Remove
    Rename
    Chmod
)

func NewWatcher() (*Watcher, error) {
    fd, err := unix.InotifyInit1(unix.IN_CLOEXEC | unix.IN_NONBLOCK)
    if err != nil { return nil, fmt.Errorf("inotify_init1: %w", err) }
    w := &Watcher{
        fd:       fd,
        watches:  make(map[string]int),
        rWatches: make(map[int]string),
        Events:   make(chan WatchEvent, 16),
        Errors:   make(chan error, 1),
        done:     make(chan struct{}),
    }
    go w.readLoop()
    return w, nil
}

func (w *Watcher) Add(path string) error {
    wd, err := unix.InotifyAddWatch(w.fd, path,
        unix.IN_CREATE|unix.IN_WRITE|unix.IN_REMOVE|
        unix.IN_RENAME|unix.IN_CHMOD|unix.IN_CLOSE_WRITE|
        unix.IN_MOVED_FROM|unix.IN_MOVED_TO)
    if err != nil { return fmt.Errorf("inotify_add_watch(%s): %w", path, err) }
    w.mu.Lock()
    w.watches[path] = wd
    w.rWatches[wd] = path
    w.mu.Unlock()
    return nil
}

func (w *Watcher) readLoop() {
    buf := make([]byte, unix.SizeofInotifyEvent*1024+unix.NAME_MAX+1)
    for {
        select {
        case <-w.done: return
        default:
        }
        n, err := unix.Read(w.fd, buf)
        if err != nil {
            if errors.Is(err, unix.EAGAIN) { time.Sleep(10 * time.Millisecond); continue }
            select { case w.Errors <- err: default: }
            return
        }
        if n < unix.SizeofInotifyEvent { continue }

        offset := 0
        for offset < n {
            event := (*unix.InotifyEvent)(unsafe.Pointer(&buf[offset]))
            name := ""
            if event.Len > 0 {
                nameBytes := buf[offset+unix.SizeofInotifyEvent : offset+unix.SizeofInotifyEvent+int(event.Len)]
                name = strings.TrimRight(string(nameBytes), "\x00")
            }

            w.mu.Lock()
            path := w.rWatches[int(event.Wd)]
            w.mu.Unlock()

            if path != "" {
                w.Events <- WatchEvent{
                    Path: path,
                    Name: name,
                    Op:   inotifyMaskToOp(event.Mask),
                }
            }
            offset += unix.SizeofInotifyEvent + int(event.Len)
        }
    }
}

func (w *Watcher) Close() error {
    close(w.done)
    return unix.Close(w.fd)
}

func inotifyMaskToOp(mask uint32) WatchOp {
    switch {
    case mask&unix.IN_CREATE != 0 || mask&unix.IN_MOVED_TO != 0: return Create
    case mask&unix.IN_CLOSE_WRITE != 0 || mask&unix.IN_WRITE != 0: return Write
    case mask&unix.IN_DELETE != 0 || mask&unix.IN_MOVED_FROM != 0: return Remove
    case mask&unix.IN_RENAME != 0: return Rename
    default: return Chmod
    }
}
```

---

## 7. Linux Capabilities

```go
import "kernel.org/pub/linux/libs/security/libcap/cap"

// Drop all capabilities except specified ones (principle of least privilege)
func DropToMinimalCapabilities(keep ...cap.Value) error {
    c := cap.NewSet()
    for _, v := range keep {
        if err := c.SetFlag(cap.Permitted, true, v); err != nil { return err }
        if err := c.SetFlag(cap.Effective, true, v); err != nil { return err }
        if err := c.SetFlag(cap.Inheritable, true, v); err != nil { return err }
    }
    if err := c.SetProc(); err != nil {
        return fmt.Errorf("drop capabilities: %w", err)
    }
    // Also set PR_SET_NO_NEW_PRIVS
    return unix.Prctl(unix.PR_SET_NO_NEW_PRIVS, 1, 0, 0, 0)
}

// Common capability sets by service type:
// HTTP server on :80/:443:  cap.NET_BIND_SERVICE only
// Packet capture:           cap.NET_RAW only
// Container runtime:        cap.SYS_ADMIN, cap.SETUID, cap.SETGID, cap.CHOWN
// Network setup:            cap.NET_ADMIN

// Verify capabilities
func GetCurrentCapabilities() (string, error) {
    c, err := cap.GetPID(os.Getpid())
    if err != nil { return "", err }
    return c.String(), nil
}

// Ambient capabilities (for setuid-free privilege passing to child)
func SetAmbientCapability(v cap.Value) error {
    return unix.Prctl(unix.PR_CAP_AMBIENT, unix.PR_CAP_AMBIENT_RAISE, uintptr(v), 0, 0)
}
```

---

## 8. seccomp BPF

```go
import "github.com/seccomp/libseccomp-golang"

// Apply allowlist seccomp filter — called AFTER all setup is complete
func ApplySeccompFilter(allowedSyscalls []string) error {
    // Default action: return EPERM (safer than SIGSYS for debugging)
    filter, err := seccomp.NewFilter(seccomp.ActErrno.SetReturnCode(int16(unix.EPERM)))
    if err != nil { return fmt.Errorf("seccomp.NewFilter: %w", err) }
    defer filter.Release()

    for _, name := range allowedSyscalls {
        call, err := seccomp.GetSyscallFromName(name)
        if err != nil { return fmt.Errorf("unknown syscall %q: %w", name, err) }
        if err := filter.AddRule(call, seccomp.ActAllow); err != nil {
            return fmt.Errorf("AddRule(%s): %w", name, err)
        }
    }

    return filter.Load()
}

// Minimal syscall set for a Go HTTP microservice:
var HTTPServiceSyscalls = []string{
    // Memory management
    "brk", "mmap", "munmap", "mprotect", "mremap", "madvise",
    // File I/O
    "read", "write", "close", "openat", "newfstatat", "fstat",
    "fcntl", "dup", "dup2", "dup3",
    // Network
    "socket", "bind", "listen", "accept4", "connect",
    "sendto", "recvfrom", "sendmsg", "recvmsg",
    "getsockopt", "setsockopt", "getsockname", "getpeername", "shutdown",
    // Multiplexing
    "epoll_create1", "epoll_ctl", "epoll_pwait", "epoll_wait",
    "select", "pselect6", "poll", "ppoll",
    // Time
    "nanosleep", "clock_nanosleep", "clock_gettime", "gettimeofday",
    "timer_create", "timer_settime", "timer_delete",
    // Process/thread
    "clone", "clone3", "exit", "exit_group", "futex",
    "getpid", "gettid", "getuid", "getgid", "getppid",
    "rt_sigaction", "rt_sigprocmask", "rt_sigreturn", "tgkill",
    "sched_yield", "sched_getaffinity",
    // Misc
    "getrandom", "prlimit64", "uname", "pipe2",
    "timerfd_create", "timerfd_settime", "eventfd2",
    "readlinkat", "getcwd",
}
```

---

## 9. /proc Filesystem — Key Interfaces

```go
// /proc/self/status — process information
func ReadProcessStatus() (map[string]string, error) {
    data, err := os.ReadFile("/proc/self/status")
    if err != nil { return nil, err }
    status := make(map[string]string)
    for _, line := range strings.Split(string(data), "\n") {
        parts := strings.SplitN(line, ":", 2)
        if len(parts) == 2 {
            status[strings.TrimSpace(parts[0])] = strings.TrimSpace(parts[1])
        }
    }
    return status, nil
}

// /proc/self/maps — virtual memory layout
func IsAddressInHeap(addr uintptr) (bool, error) {
    data, err := os.ReadFile("/proc/self/maps")
    if err != nil { return false, err }
    for _, line := range strings.Split(string(data), "\n") {
        if !strings.Contains(line, "heap") { continue }
        parts := strings.Fields(line)
        if len(parts) == 0 { continue }
        addrs := strings.Split(parts[0], "-")
        if len(addrs) != 2 { continue }
        start, _ := strconv.ParseUint(addrs[0], 16, 64)
        end, _ := strconv.ParseUint(addrs[1], 16, 64)
        if addr >= uintptr(start) && addr < uintptr(end) { return true, nil }
    }
    return false, nil
}

// /proc/net/tcp — active connections
func CountTCPConnections() (int, error) {
    data, err := os.ReadFile("/proc/net/tcp")
    if err != nil { return 0, err }
    lines := strings.Split(strings.TrimSpace(string(data)), "\n")
    return max(0, len(lines)-1), nil // subtract header
}

// /proc/sys/net — runtime network tuning
func SetTCPSomaxconn(n int) error {
    return os.WriteFile("/proc/sys/net/core/somaxconn",
        []byte(strconv.Itoa(n)), 0)
}

// /proc/pressure — PSI (Pressure Stall Information)
type PSIStats struct {
    Avg10  float64
    Avg60  float64
    Avg300 float64
    Total  uint64
}
func ReadPSI(resource string) (*PSIStats, error) {
    // resource: "cpu", "memory", "io"
    path := fmt.Sprintf("/proc/pressure/%s", resource)
    data, err := os.ReadFile(path)
    if err != nil { return nil, err }
    // Parse: "some avg10=X avg60=Y avg300=Z total=N"
    var stats PSIStats
    fmt.Sscanf(string(data),
        "some avg10=%f avg60=%f avg300=%f total=%d",
        &stats.Avg10, &stats.Avg60, &stats.Avg300, &stats.Total)
    return &stats, nil
}
```

---

## 10. Daemonization & Service Management

```go
// Production daemon initialization
func InitDaemon(pidFile string) error {
    // 1. Verify not already running (PID file lock)
    if err := acquirePIDLock(pidFile); err != nil { return err }

    // 2. Set safe umask
    unix.Umask(0022)

    // 3. Change to root directory (avoid blocking unmounts)
    if err := unix.Chdir("/"); err != nil { return err }

    // 4. Redirect stdin/stdout/stderr to /dev/null
    devNull, err := os.OpenFile("/dev/null", os.O_RDWR, 0)
    if err != nil { return err }
    for _, fd := range []int{0, 1, 2} {
        unix.Dup2(int(devNull.Fd()), fd)
    }
    devNull.Close()

    // 5. New session (detach from terminal)
    unix.Setsid()

    return nil
}

func acquirePIDLock(path string) error {
    f, err := os.OpenFile(path, os.O_RDWR|os.O_CREATE|os.O_CLOEXEC, 0644)
    if err != nil { return fmt.Errorf("open pid file: %w", err) }

    // Exclusive non-blocking lock
    if err := unix.Flock(int(f.Fd()), unix.LOCK_EX|unix.LOCK_NB); err != nil {
        // Read existing PID to give helpful error
        buf := make([]byte, 16)
        n, _ := f.Read(buf)
        return fmt.Errorf("already running (pid %s): %w", string(buf[:n]), err)
    }

    // Write our PID
    if err := f.Truncate(0); err != nil { return err }
    fmt.Fprintf(f, "%d\n", os.Getpid())
    return f.Sync()
    // Note: keep f open (lock released on close)
}

// systemd socket activation (inherits pre-bound listener)
func systemdListener() (net.Listener, error) {
    // SD_LISTEN_FDS_START = 3 (first inherited fd)
    const SD_LISTEN_FDS_START = 3
    fds := os.Getenv("LISTEN_FDS")
    if fds == "" { return nil, errors.New("not running under systemd socket activation") }
    n, _ := strconv.Atoi(fds)
    if n < 1 { return nil, errors.New("no inherited fds") }

    f := os.NewFile(SD_LISTEN_FDS_START, "systemd-socket")
    return net.FileListener(f)
}
```

---

## Linux Checklist

```
SYSCALLS:
  □ golang.org/x/sys/unix used — not syscall package
  □ All file opens use O_CLOEXEC
  □ All paths validated for traversal before use

NAMESPACES:
  □ runtime.LockOSThread() before any setns/unshare call
  □ Namespace restoration (original ns re-entered after fn)
  □ User namespace UID/GID mappings set before other namespace ops

CGROUPS v2:
  □ EnableControllers() called on parent before using child controllers
  □ memory.max ≠ memory.high (use both: high for throttle, max for OOM)
  □ cgroup.kill used for cleanup — not per-process SIGKILL
  □ PSI notifications set up for memory pressure alerting

SIGNALS:
  □ Buffered signal channel (size ≥ 1)
  □ signal.Stop(ch) deferred immediately after signal.Notify
  □ SIGTERM → graceful shutdown; SIGHUP → reload; no SIGKILL in normal flow
  □ Child reaping for processes that fork

CAPABILITIES:
  □ Capabilities dropped to minimum after setup
  □ PR_SET_NO_NEW_PRIVS set (prevents re-acquiring via setuid)
  □ Ambient capabilities only when truly needed

SECCOMP:
  □ Applied AFTER all setup syscalls are complete
  □ Default action EPERM (not SIGSYS) for debuggability
  □ Tested with strace -c to verify allowlist coverage
```
