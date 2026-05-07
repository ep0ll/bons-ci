---
name: golang-performance
description: >
  Comprehensive Go performance engineering: CPU/memory profiling workflow, pprof deep-dive,
  escape analysis, allocation elimination, sync.Pool, GC tuning (GOGC/GOMEMLIMIT), SIMD-friendly
  loops, CPU cache optimization, struct padding, string/byte optimization, goroutine scheduler
  tuning, Linux kernel performance (epoll, huge pages, NUMA, network tuning, CPU affinity),
  Docker/container performance (cgroup limits, overlay FS, seccomp overhead, image optimization).
  PROFILE FIRST. Optimize second. Measure again. Cross-references: golang-core/SKILL.md,
  linux/SKILL.md, docker-containerd/SKILL.md, concurrency/SKILL.md.
---

# Go + Linux + Docker Performance Engineering

---

## 0. The Commandments

```
1. MEASURE before optimizing. Never guess at bottlenecks.
2. Profile with REAL load, REAL data sizes.
3. Optimize algorithm first (O(n²) → O(n log n) always beats micro-opts).
4. Optimize data structures second (cache-friendly > pointer-chasing).
5. Reduce allocations third (GC pressure kills latency).
6. Micro-optimize last (only after 1-4 are exhausted and profiled).
7. Benchmark EVERY change: regressions happen silently.
8. The compiler is smarter than you — trust pprof over intuition.
```

---

## 1. Profiling Workflow — Full Detail

### CPU Profiling

```bash
# Method 1: benchmark-based (best for library code)
go test -cpuprofile=cpu.prof -bench=BenchmarkHotPath -benchtime=30s -count=1 ./pkg/...
go tool pprof -http=:6060 cpu.prof

# In pprof browser UI:
# - Top: hot functions by cumulative/self time
# - Graph: call graph, edge = time
# - Flame: flame graph (most intuitive for hot paths)
# - Source: annotated source lines

# Method 2: HTTP endpoint (production sampling)
# Add to internal port only — NEVER expose publicly
import _ "net/http/pprof"
go func() { http.ListenAndServe("localhost:6060", nil) }()

# Capture 30s profile from running service
curl http://localhost:6060/debug/pprof/profile?seconds=30 > cpu.prof
go tool pprof -http=:6061 cpu.prof

# Method 3: programmatic (precise code section)
import "runtime/pprof"
f, _ := os.Create("cpu.prof")
pprof.StartCPUProfile(f)
defer pprof.StopCPUProfile()
// ... code to profile ...
```

### Memory Profiling

```bash
# Allocation profiling (what's allocating?)
go test -memprofile=mem.prof -memprofilerate=1 -bench=. -benchtime=10s ./...
# -memprofilerate=1 samples every allocation (expensive but complete)
# default rate=512*1024 — samples proportionally

# View: alloc_objects = count; alloc_space = bytes
go tool pprof -alloc_objects mem.prof  # most useful: find allocation COUNT
go tool pprof -alloc_space  mem.prof   # find largest allocations by BYTES
go tool pprof -inuse_objects mem.prof  # currently live objects
go tool pprof -inuse_space  mem.prof   # currently live bytes (heap size)

# HTTP endpoint
curl http://localhost:6060/debug/pprof/heap > mem.prof
go tool pprof -http=:6061 -alloc_objects mem.prof
```

### Goroutine / Blocking Profiles

```bash
# Goroutine profile — detect leaks, contention
curl http://localhost:6060/debug/pprof/goroutine?debug=2 | head -100
# debug=2 gives full stacks — look for unexpected goroutine counts

# Block profile — where goroutines are blocked waiting
curl http://localhost:6060/debug/pprof/block > block.prof
go tool pprof -http=:6061 block.prof
# Enable first: runtime.SetBlockProfileRate(1)

# Mutex profile — where mutexes are contended
curl http://localhost:6060/debug/pprof/mutex > mutex.prof
go tool pprof -http=:6061 mutex.prof
# Enable first: runtime.SetMutexProfileFraction(1)
```

### Execution Trace (Goroutine Scheduling)

```bash
# Best tool for: GC pauses, goroutine scheduling latency, syscall time
go test -trace=trace.out -bench=BenchmarkX -benchtime=2s ./...
go tool trace trace.out

# In browser:
# - Goroutines view: see all goroutines over time
# - Heap view: GC events and heap size
# - Threads view: OS thread usage
# - Network/syscall blocking

# From HTTP
curl http://localhost:6060/debug/pprof/trace?seconds=5 > trace.out
go tool trace trace.out
```

---

## 2. Benchmark Design — Production Quality

```go
func BenchmarkProcessOrder(b *testing.B) {
    // Setup — NOT counted
    orders := generateTestOrders(1000)
    svc := newOrderService()

    b.ReportAllocs()                        // show allocs/op and B/op
    b.SetBytes(int64(len(orders) * 100))    // report MB/s
    b.ResetTimer()                          // exclude setup from measurement

    for i := 0; i < b.N; i++ {
        // Prevent dead-code elimination
        result, _ := svc.ProcessBatch(context.Background(), orders)
        _ = result
    }
}

// Sub-benchmarks for complexity analysis
func BenchmarkSearch(b *testing.B) {
    for _, size := range []int{100, 1_000, 10_000, 100_000, 1_000_000} {
        size := size
        b.Run(fmt.Sprintf("n=%d", size), func(b *testing.B) {
            data := generateSorted(size)
            target := data[size/2]
            b.ResetTimer()
            for i := 0; i < b.N; i++ {
                _ = binarySearch(data, target)
            }
        })
    }
}

// Parallel benchmark — finds contention
func BenchmarkConcurrent(b *testing.B) {
    cache := newCache()
    b.RunParallel(func(pb *testing.PB) {
        for pb.Next() {
            v, _ := cache.Get(context.Background(), "hot-key")
            _ = v
        }
    })
}

// Compare results statistically
// go test -bench=. -benchmem -count=10 ./... > new.txt
// benchstat old.txt new.txt    ← shows statistical significance
```

---

## 3. Escape Analysis — Keeping Objects on Stack

```bash
# Show escape decisions
go build -gcflags="-m=2" ./... 2>&1 | grep "escapes to heap"

# Show inlining decisions
go build -gcflags="-m=1" ./... 2>&1 | grep "can inline\|too complex"

# Show all compiler decisions
go build -gcflags="-m=2 -l=4" ./...
```

```go
// What causes heap escape:
// 1. Taking address of local variable and returning it
func bad() *int {
    x := 42
    return &x  // x escapes to heap
}

// 2. Storing in interface (boxing)
var w io.Writer = os.Stdout
fmt.Fprintln(w, "hello")  // args escape to heap for interface call

// 3. Sending to channel
ch <- bigStruct  // bigStruct escapes if channel is interface{}

// 4. Closure capturing
x := 42
go func() { use(x) }()  // x escapes because goroutine outlives function

// Tricks to keep on stack:
// a) Value receivers for small structs
func (p Point) Distance(q Point) float64 {   // no allocation
    dx := p.X - q.X; dy := p.Y - q.Y
    return math.Sqrt(dx*dx + dy*dy)
}

// b) Return value, not pointer (for small types)
func newPoint(x, y float64) Point { return Point{x, y} }  // stays on stack

// c) Fixed-size arrays instead of slices for small, known sizes
var buf [256]byte  // stack-allocated
n := binary.PutVarint(buf[:], value)
```

---

## 4. Allocation Elimination Techniques

### Pre-Allocation
```go
// ✗ BAD — slice grows with repeated append (multiple allocations)
var results []Result
for _, item := range items {
    results = append(results, process(item))
}

// ✓ GOOD — single allocation
results := make([]Result, 0, len(items))
for _, item := range items {
    results = append(results, process(item))
}

// Map with size hint
index := make(map[string]*User, len(users))
for _, u := range users { index[u.ID] = u }

// String builder with growth hint
var sb strings.Builder
sb.Grow(estimatedBytes)
for _, s := range parts { sb.WriteString(s) }
```

### sync.Pool — Object Reuse
```go
// Pool: reduce GC pressure for high-frequency short-lived objects
var bufPool = sync.Pool{
    New: func() any {
        b := make([]byte, 0, 32*1024)  // 32KB starting capacity
        return &b
    },
}

func encode(data any) ([]byte, error) {
    // Borrow from pool
    bufPtr := bufPool.Get().(*[]byte)
    buf := (*bufPtr)[:0]  // reset length, keep capacity

    // Use the buffer
    enc := json.NewEncoder(bytes.NewBuffer(buf))
    if err := enc.Encode(data); err != nil {
        bufPool.Put(bufPtr)  // return on error
        return nil, err
    }

    // COPY result before returning buffer (buffer goes back to pool)
    result := make([]byte, len(buf))
    copy(result, buf)

    *bufPtr = buf
    bufPool.Put(bufPtr)  // return to pool
    return result, nil
}

// Pool for encoder objects (expensive to create)
var encoderPool = sync.Pool{
    New: func() any { return json.NewEncoder(io.Discard) },
}
```

### String/Byte Zero-Copy (Go 1.20+)
```go
import "unsafe"

// Zero-copy string → []byte (treat as read-only!)
func unsafeStringToBytes(s string) []byte {
    return unsafe.Slice(unsafe.StringData(s), len(s))
    // WARNING: returned slice is read-only — do NOT modify!
    // Valid only while s is alive
}

// Zero-copy []byte → string
func unsafeBytesToString(b []byte) string {
    return unsafe.String(unsafe.SliceData(b), len(b))
    // WARNING: string is invalidated if b is modified!
}

// Safe alternative: use in hot path with documented contract
// Always benchmark to confirm the saving justifies the risk

// String concatenation in loops: ALWAYS use strings.Builder
func buildQuery(conditions []string) string {
    if len(conditions) == 0 { return "TRUE" }
    var sb strings.Builder
    sb.Grow(len(conditions) * 30)  // estimated
    for i, cond := range conditions {
        if i > 0 { sb.WriteString(" AND ") }
        sb.WriteString(cond)
    }
    return sb.String()
}
```

---

## 5. CPU Cache Optimization

### Cache Line Fundamentals
```
Cache line size: 64 bytes on x86-64 and ARM64
L1 cache hit:   ~4 cycles  (~1 ns)
L2 cache hit:   ~12 cycles (~3 ns)
L3 cache hit:   ~40 cycles (~10 ns)
RAM access:     ~200 cycles (~50 ns)
→ Sequential access is 50x faster than random access
```

### Struct Field Ordering — Minimize Padding
```go
// Run: go install golang.org/x/tools/go/analysis/passes/fieldalignment/cmd/fieldalignment@latest
// fieldalignment -fix ./...

// ✗ BAD — 40 bytes due to padding
type Padded struct {
    A bool      // 1 byte + 7 padding
    B int64     // 8 bytes
    C bool      // 1 byte + 7 padding
    D float64   // 8 bytes
    E int32     // 4 bytes + 4 padding
}
// Total: 40 bytes

// ✓ GOOD — 32 bytes, largest → smallest
type Tight struct {
    B int64     // 8 bytes
    D float64   // 8 bytes
    E int32     // 4 bytes
    A bool      // 1 byte
    C bool      // 1 byte
    _  [2]byte  // explicit padding for documentation
}
// Total: 24 bytes (40% reduction)

// Check with unsafe.Sizeof:
fmt.Println(unsafe.Sizeof(Padded{}))  // 40
fmt.Println(unsafe.Sizeof(Tight{}))   // 24
```

### False Sharing (Concurrent Counters)
```go
// ✗ BAD — two counters share a cache line → cacheline ping-pong
type Counters struct {
    c1 uint64  // offset 0
    c2 uint64  // offset 8 — SAME cache line as c1!
}
// goroutine A writes c1, goroutine B writes c2 → cache line invalidated every write

// ✓ GOOD — pad to separate cache lines
type Counters struct {
    c1  uint64
    _   [56]byte  // pad to 64 bytes (one cache line)
    c2  uint64
    _   [56]byte
}

// Even better for atomic ops: per-CPU sharding
type ShardedCounter struct {
    shards [256]struct {
        count uint64
        _     [56]byte  // pad each shard to its own cache line
    }
}
func (c *ShardedCounter) Inc() {
    shard := uint64(runtime_procPin()) % 256
    atomic.AddUint64(&c.shards[shard].count, 1)
    runtime_procUnpin()
}
func (c *ShardedCounter) Val() uint64 {
    var total uint64
    for i := range c.shards { total += atomic.LoadUint64(&c.shards[i].count) }
    return total
}
```

### Array of Structs vs Struct of Arrays
```go
// AoS — bad for field-specific iteration (loads all fields for one)
type ParticleAoS struct { X, Y, Z, Mass float64; Alive bool }
particles := []ParticleAoS{...}
for i := range particles { particles[i].X += 1.0 }  // loads Y,Z,Mass,Alive too

// SoA — good for field-specific iteration (100% cache utilization)
type Particles struct {
    X, Y, Z []float64
    Mass     []float64
    Alive    []bool
}
ps := Particles{...}
for i := range ps.X { ps.X[i] += 1.0 }  // only X loaded — perfect cache use
```

---

## 6. GC Tuning

```go
import (
    "runtime"
    "runtime/debug"
    "os"
)

// GOGC: target heap growth ratio (default 100 = double before GC)
// Lower = more frequent GC = less memory, more CPU
// Higher = less frequent GC = more memory, less CPU

// GOMEMLIMIT (Go 1.19+): hard memory limit — use in all containerized deployments
// Set to ~75-80% of container memory limit
os.Setenv("GOMEMLIMIT", "400MiB")  // or via runtime package

// Programmatic (batch jobs that need different settings)
func processBatchHighThroughput() {
    oldGC := debug.SetGCPercent(-1)   // disable GC during batch
    defer debug.SetGCPercent(oldGC)    // re-enable after
    
    debug.SetMemoryLimit(512 * 1024 * 1024)  // 512MB hard limit

    // ... process batch ...

    // Force GC + release to OS after large batch
    runtime.GC()
    debug.FreeOSMemory()
}

// Read GC stats for metrics
var ms runtime.MemStats
runtime.ReadMemStats(&ms)
fmt.Printf(
    "HeapAlloc=%d HeapSys=%d NumGC=%d PauseTotalNs=%d\n",
    ms.HeapAlloc, ms.HeapSys, ms.NumGC, ms.PauseTotalNs,
)
// Expose as Prometheus gauge
gcPauseHistogram.Observe(float64(ms.PauseNs[(ms.NumGC+255)%256]) / 1e6)

// GC-friendly patterns:
// 1. Avoid pointer-heavy data structures (GC must scan all pointers)
//    Prefer []byte over []string where possible
//    Prefer index into slice over map[key]*Value

// 2. Reduce heap object count (each object = GC work)
//    Use value types, not pointers, for small objects
//    Use arrays instead of linked lists

// 3. Finalize large allocations explicitly
//    Don't rely on GC for cleanup — use defer + explicit Close()
```

---

## 7. Goroutine Scheduler Tuning

```go
import (
    "runtime"
    "go.uber.org/automaxprocs/maxprocs"
)

// GOMAXPROCS: number of OS threads for goroutines (default = CPU count)
// For containers: cgroup quota ≠ host CPU count — automaxprocs fixes this
func init() {
    // Reads cgroup CPU quota → sets correct GOMAXPROCS
    maxprocs.Set(maxprocs.Logger(slog.Default().Info))
}

// Worker pool sizing guidelines:
const (
    // CPU-bound: GOMAXPROCS workers (adding more creates context-switch overhead)
    CPUWorkers = runtime.GOMAXPROCS(0)

    // I/O-bound: much higher (goroutines block on I/O, not CPU)
    // Tune empirically: start at 10x, benchmark, adjust
    IOWorkers = 10 * runtime.GOMAXPROCS(0)

    // Mixed: profile to find blocking ratio
    // blocking_ratio = time_blocked / total_time
    // optimal_workers = GOMAXPROCS / (1 - blocking_ratio)
)

// Goroutine local storage pattern (avoid per-goroutine heap allocation)
// Use sync.Pool for goroutine-local scratch space
var scratchPool = sync.Pool{New: func() any { return make([]byte, 0, 4096) }}
```

---

## 8. Linux Kernel Performance

### Network Tuning (for high-throughput Go servers)
```bash
# /etc/sysctl.conf — tune for high-connection servers

# TCP connection backlog
net.core.somaxconn = 65535
net.ipv4.tcp_max_syn_backlog = 65535

# Socket buffer sizes (increase for high-bandwidth)
net.core.rmem_max = 16777216      # 16MB receive buffer
net.core.wmem_max = 16777216      # 16MB send buffer
net.ipv4.tcp_rmem = 4096 87380 16777216
net.ipv4.tcp_wmem = 4096 65536 16777216

# TIME_WAIT reuse (for services making many outbound connections)
net.ipv4.tcp_tw_reuse = 1
net.ipv4.tcp_fin_timeout = 15

# Increase file descriptor limit
fs.file-max = 1000000

# Apply immediately
sysctl -p
```

```go
// Go: set SO_REUSEPORT for multi-process load balancing
import "golang.org/x/sys/unix"

func listenWithReusePort(addr string) (net.Listener, error) {
    lc := net.ListenConfig{
        Control: func(network, address string, conn syscall.RawConn) error {
            return conn.Control(func(fd uintptr) {
                unix.SetsockoptInt(int(fd), unix.SOL_SOCKET, unix.SO_REUSEPORT, 1)
            })
        },
    }
    return lc.Listen(context.Background(), "tcp", addr)
}

// TCP_NODELAY — disable Nagle for latency-sensitive services
func setTCPNoDelay(conn net.Conn) {
    if tc, ok := conn.(*net.TCPConn); ok {
        tc.SetNoDelay(true)   // disable Nagle algorithm
        tc.SetKeepAlive(true)
        tc.SetKeepAlivePeriod(30 * time.Second)
    }
}
```

### CPU Affinity & NUMA
```go
import "golang.org/x/sys/unix"

// Pin a goroutine's OS thread to specific CPUs
// Use runtime.LockOSThread() first
func pinToCore(coreID int) error {
    runtime.LockOSThread()
    var cpuSet unix.CPUSet
    cpuSet.Set(coreID)
    return unix.SchedSetaffinity(0, &cpuSet)
}

// NUMA awareness: allocate memory on local NUMA node
// Use numactl in deployment: numactl --localalloc ./myservice
// Or set in pod spec:
// topologySpreadConstraints + kubelet topology manager
```

### Memory: Huge Pages
```bash
# Enable transparent huge pages for heap-heavy Go services
# Reduces TLB pressure for large heap workloads (>1GB)
echo always > /sys/kernel/mm/transparent_hugepage/enabled

# Or: madvise mode (let process decide)
echo madvise > /sys/kernel/mm/transparent_hugepage/enabled

# In Go: use madvise(MADV_HUGEPAGE) on large allocations
# via runtime/debug.SetMaxStack or custom mmap
```

### epoll / io_uring (Advanced I/O)
```go
// Go's runtime uses epoll internally — you get this automatically
// For maximum control (e.g., uring), use external libraries:
// github.com/pawelgaczynski/giouring  (io_uring bindings)

// inotify for efficient file watching (avoid polling)
import "golang.org/x/sys/unix"

func watchDir(path string) (<-chan string, error) {
    fd, err := unix.InotifyInit1(unix.IN_CLOEXEC | unix.IN_NONBLOCK)
    if err != nil { return nil, err }

    _, err = unix.InotifyAddWatch(fd, path,
        unix.IN_CREATE|unix.IN_MODIFY|unix.IN_DELETE|unix.IN_MOVED_TO)
    if err != nil { unix.Close(fd); return nil, err }

    ch := make(chan string, 16)
    go func() {
        buf := make([]byte, unix.SizeofInotifyEvent*128)
        for {
            n, err := unix.Read(fd, buf)
            if err != nil || n == 0 { close(ch); return }
            // parse inotify events...
            ch <- parsedPath
        }
    }()
    return ch, nil
}
```

### seccomp Performance Impact
```go
// seccomp adds syscall filtering overhead (~50-100ns per syscall)
// Minimize by using SECCOMP_RET_ALLOW for common syscalls
// Profile syscall usage first:
// strace -c -p PID  ← shows syscall frequency

// Minimal allowlist for a typical Go HTTP service:
var allowedSyscalls = []string{
    "read", "write", "close", "fstat", "mmap", "mprotect", "munmap",
    "brk", "rt_sigaction", "rt_sigprocmask", "rt_sigreturn", "ioctl",
    "pread64", "pwrite64", "readv", "writev", "access", "pipe",
    "select", "sched_yield", "mremap", "msync", "mincore", "madvise",
    "socket", "connect", "accept", "sendto", "recvfrom", "sendmsg",
    "recvmsg", "shutdown", "bind", "listen", "getsockname", "getpeername",
    "setsockopt", "getsockopt", "clone", "fork", "execve", "exit",
    "wait4", "kill", "uname", "fcntl", "fsync", "getcwd", "chdir",
    "getdents64", "rename", "mkdir", "rmdir", "unlink", "readlink",
    "getrlimit", "sysinfo", "times", "getuid", "getgid", "setuid",
    "setgid", "getpid", "getppid", "nanosleep", "clock_gettime",
    "clock_nanosleep", "futex", "sched_getaffinity", "epoll_create1",
    "epoll_ctl", "epoll_wait", "eventfd2", "timerfd_create",
    "timerfd_settime", "accept4", "epoll_pwait", "getrandom",
    "openat", "newfstatat", "prlimit64",
}
```

---

## 9. Docker/Container Performance

### Image Optimization
```dockerfile
# ── Builder stage: compile static binary ──────────────────────────
FROM golang:1.22-alpine AS builder

# Cache: copy go.mod first (changes less often than source)
WORKDIR /build
COPY go.mod go.sum ./
RUN go mod download && go mod verify  # layer cached until go.sum changes

COPY . .
ARG VERSION=dev
RUN CGO_ENABLED=0 GOOS=linux GOARCH=amd64 \
    go build \
    -trimpath \
    -ldflags="-s -w -extldflags=-static \
              -X main.version=${VERSION}" \
    -o /app/server ./cmd/server
# -trimpath: remove local paths from binary (smaller, reproducible)
# -s -w: strip debug info and DWARF (50-70% size reduction)
# -extldflags=-static: fully static (no libc dependency)
# CGO_ENABLED=0: disable cgo (required for static)

# ── Final stage: minimal attack + download surface ─────────────────
FROM gcr.io/distroless/static-debian12:nonroot
# OR: FROM scratch  (for fully static binaries with no certs/timezone)
# OR: FROM alpine:3.19 (if you need shell access for debugging)

COPY --from=builder /app/server /server
COPY --from=builder /etc/ssl/certs/ca-certificates.crt /etc/ssl/certs/

USER nonroot:nonroot
EXPOSE 8080
ENTRYPOINT ["/server"]
```

### Container Resource Configuration
```yaml
# Kubernetes: always set BOTH requests and limits
resources:
  requests:
    cpu: "100m"       # minimum guaranteed CPU
    memory: "128Mi"   # minimum guaranteed memory
  limits:
    cpu: "500m"       # max CPU (cgroup cpu.max)
    memory: "512Mi"   # max memory (cgroup memory.max) — OOM kill threshold

# Go env from cgroup limits (CRITICAL for correct GOMAXPROCS and GOMEMLIMIT)
env:
  - name: GOMAXPROCS
    valueFrom:
      resourceFieldRef:
        resource: limits.cpu    # sets GOMAXPROCS to CPU limit
  - name: GOMEMLIMIT
    valueFrom:
      resourceFieldRef:
        resource: limits.memory # sets GOMEMLIMIT to memory limit
```

### Overlay FS Performance (Docker Layer Caching)
```bash
# Layer ordering: most stable → least stable
# Layer 1: OS (changes never/rarely)
# Layer 2: Runtime dependencies (changes weekly)
# Layer 3: App dependencies (go.mod — changes monthly)
# Layer 4: Config (changes per release)
# Layer 5: Binary (changes every commit)

# Use BuildKit for parallel layer building
DOCKER_BUILDKIT=1 docker build .

# Cache mount for go module cache (speeds up CI builds)
# syntax=docker/dockerfile:1
FROM golang:1.22-alpine AS builder
RUN --mount=type=cache,target=/root/go/pkg/mod \
    --mount=type=cache,target=/root/.cache/go-build \
    go build -o /app/server ./cmd/server
```

### Container Seccomp Overhead Reduction
```json
// Custom seccomp profile (restrict to only needed syscalls)
// Reduces kernel attack surface AND eliminates per-syscall filtering overhead
// for syscalls NOT in the Go runtime's usage pattern
{
  "defaultAction": "SCMP_ACT_ERRNO",
  "architectures": ["SCMP_ARCH_X86_64", "SCMP_ARCH_AARCH64"],
  "syscalls": [
    {
      "names": ["read", "write", "close", "fstat", "mmap", "mprotect",
                "munmap", "brk", "rt_sigaction", "rt_sigprocmask",
                "socket", "connect", "accept4", "sendto", "recvfrom",
                "epoll_wait", "epoll_ctl", "epoll_create1",
                "clone", "futex", "nanosleep", "clock_gettime",
                "openat", "newfstatat", "prlimit64", "getrandom",
                "exit_group", "exit", "kill"],
      "action": "SCMP_ACT_ALLOW"
    }
  ]
}
```

### cgroup v2 — Go Service Configuration
```go
import (
    "os"
    "strconv"
    "strings"
)

// Read container memory limit from cgroup v2
func getContainerMemoryLimit() int64 {
    // cgroup v2: /sys/fs/cgroup/memory.max
    data, err := os.ReadFile("/sys/fs/cgroup/memory.max")
    if err != nil { return 0 }
    s := strings.TrimSpace(string(data))
    if s == "max" { return 0 } // unlimited
    limit, _ := strconv.ParseInt(s, 10, 64)
    return limit
}

// Read CPU quota from cgroup v2
func getContainerCPUQuota() (quota, period int64) {
    // cgroup v2: /sys/fs/cgroup/cpu.max → "quota period" or "max period"
    data, _ := os.ReadFile("/sys/fs/cgroup/cpu.max")
    parts := strings.Fields(strings.TrimSpace(string(data)))
    if len(parts) != 2 || parts[0] == "max" { return -1, 100000 }
    quota, _ = strconv.ParseInt(parts[0], 10, 64)
    period, _ = strconv.ParseInt(parts[1], 10, 64)
    return quota, period
}

// Auto-configure Go runtime from cgroup limits
func autoConfigureRuntime() {
    if limit := getContainerMemoryLimit(); limit > 0 {
        // Set GOMEMLIMIT to 80% of cgroup limit (leave headroom for OS)
        debug.SetMemoryLimit(int64(float64(limit) * 0.8))
    }
    // automaxprocs handles GOMAXPROCS from CPU quota
}
```

### Container Startup Performance
```go
// Fast startup: minimize work in main() before serving traffic
// 1. Readiness probe fails until ready — don't rush
// 2. Parallelize independent initialization
// 3. Lazy-load non-critical resources

func main() {
    ctx := context.Background()

    // Parallel initialization
    g, ctx := errgroup.WithContext(ctx)

    var (
        pool    *pgxpool.Pool
        rdb     *redis.Client
        tracer  func(context.Context) error
    )

    g.Go(func() error {
        var err error
        pool, err = initDB(ctx, cfg.DatabaseDSN)
        return err
    })
    g.Go(func() error {
        var err error
        rdb, err = initRedis(ctx, cfg.RedisURL)
        return err
    })
    g.Go(func() error {
        var err error
        tracer, err = initTelemetry(ctx, cfg.ServiceName)
        return err
    })

    if err := g.Wait(); err != nil {
        log.Fatal("startup failed:", err)
    }

    // Now serve traffic
    srv := newServer(pool, rdb, tracer)
    srv.ListenAndServe()
}
```

---

## 10. Performance Anti-Patterns — Complete List

```go
// ✗ ANTI-1: Fmt.Sprintf for simple concatenation
s := fmt.Sprintf("%s/%s", base, path)  // allocates
s  = base + "/" + path                   // faster for 2-3 strings

// ✗ ANTI-2: Converting []byte ↔ string repeatedly in hot path
for _, line := range lines {
    s := string(line)     // allocation every iteration
    if s == target { ... }
}
// ✓
targetBytes := []byte(target)  // one allocation outside loop
for _, line := range lines {
    if bytes.Equal(line, targetBytes) { ... }  // no allocation
}

// ✗ ANTI-3: defer in tight loop
for i := 0; i < 1000000; i++ {
    f, _ := os.Open(path)
    defer f.Close()  // 1M defers queued — executed all at end!
}
// ✓ explicit close in loop body
for i := 0; i < 1000000; i++ {
    f, _ := os.Open(path)
    // ... use f ...
    f.Close()  // immediate
}

// ✗ ANTI-4: interface{} in hot path (boxing + pointer chasing)
func sum(nums []interface{}) float64 { ... }
// ✓
func sum(nums []float64) float64 { ... }
// ✓ or generics
func sum[T constraints.Float](nums []T) T { ... }

// ✗ ANTI-5: Goroutine leak (unbounded goroutine creation)
for _, req := range requests {
    go handle(req)  // if len(requests) is large → OOM
}
// ✓ bounded worker pool
pool := NewWorkerPool(runtime.GOMAXPROCS(0)*2, process)
for _, req := range requests { pool.Submit(ctx, req) }

// ✗ ANTI-6: Lock contention in hot path
var mu sync.Mutex
var counter int64
func inc() { mu.Lock(); counter++; mu.Unlock() }
// ✓ atomic
var counter int64
func inc() { atomic.AddInt64(&counter, 1) }

// ✗ ANTI-7: Map lookup in hot path for small known sets
allowed := map[string]bool{"GET": true, "POST": true, "PUT": true}
if allowed[method] { ... }
// ✓ switch is faster for small sets
switch method {
case "GET", "POST", "PUT": // allowed
}

// ✗ ANTI-8: Unnecessary reflection
type Config struct{ DB string; Port int }
// Using reflect.ValueOf(cfg).FieldByName("DB")
// ✓ direct field access always
cfg.DB

// ✗ ANTI-9: time.Now() in hot path (syscall)
for i := 0; i < N; i++ {
    log.Printf("[%s] processing", time.Now().Format(time.RFC3339))
}
// ✓ batch or cache the timestamp
start := time.Now()
for i := 0; i < N; i++ {
    processItem(i) // log outside loop or sample
}
```

---

## Performance Checklist

```
PROFILING:
  □ pprof HTTP endpoint on internal port
  □ Profiled with real production-representative load
  □ benchstat used for statistically valid comparison (count=10)

ALLOCATION:
  □ go build -gcflags="-m=2" shows no unexpected heap escapes in hot path
  □ Hot-path structs: largest fields first (fieldalignment tool applied)
  □ sync.Pool used for high-frequency short-lived objects
  □ Pre-allocated slices/maps where length is known
  □ strings.Builder with Grow() for multi-part string construction
  □ No interface{} boxing in hot paths — concrete types or generics

GC:
  □ GOMEMLIMIT set in all container deployments (~80% of memory limit)
  □ GOGC tuned for workload (batch jobs: higher; latency-sensitive: lower)
  □ GC pause time monitored via runtime.ReadMemStats

CPU:
  □ automaxprocs used (correct GOMAXPROCS in containers)
  □ False sharing eliminated on concurrent counters (cache line padding)
  □ Hot structs fit within cache lines where possible

LINUX:
  □ SO_REUSEPORT for multi-process socket sharing
  □ TCP_NODELAY for latency-sensitive connections
  □ sysctl tuned for connection/buffer requirements
  □ seccomp allowlist minimized to actual syscalls used

CONTAINER:
  □ Multi-stage Docker build with distroless final image
  □ CGO_ENABLED=0 + -trimpath + -ldflags="-s -w" for binary
  □ CPU/memory limits set; GOMAXPROCS/GOMEMLIMIT from cgroup values
  □ Layer ordering: stable → unstable (go.mod before source)
  □ BuildKit enabled for parallel layer building
```
