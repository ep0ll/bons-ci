---
name: golang-core
description: >
  Core Go craftsmanship: idiomatic code, micro and macro performance optimization, project layout,
  memory management, concurrency patterns, generics, and Go toolchain mastery. Use for any Go
  implementation task. Always combine with clean-code/SKILL.md and solid-principles/SKILL.md for
  architecture work.
---

# Golang Core — Idioms, Performance & Craftsmanship

## 1. Idiomatic Go — The Pillars

### Naming
```go
// Packages: short, lowercase, no underscores, no plurals
package user      // ✓
package userStore // ✗ (camelCase in package names)
package users     // ✗ (plural)

// Interfaces: -er suffix for single-method interfaces
type Stringer interface { String() string }
type Reader   interface { Read(p []byte) (n int, err error) }
type Executor interface { Execute(ctx context.Context) error }

// Unexported before exported in declarations
// Receiver names: short, consistent, never 'self' or 'this'
func (u *User) Validate() error { ... }  // 'u', not 'user' or 'self'

// Acronyms: all-caps
type HTTPClient struct{}   // ✓
type HttpClient struct{}   // ✗
var userID string          // ✓
var userId string          // ✗

// Boolean variables/functions: is/has/can prefix
func (u *User) IsActive() bool { return u.status == StatusActive }
```

### Zero Values & Constructors
```go
// Design types so the zero value is useful
type Buffer struct {
    buf []byte  // nil slice is valid empty buffer — no constructor needed
}

// Constructor only when zero value is insufficient
func NewRateLimiter(rps int) *RateLimiter {
    return &RateLimiter{
        ticker: time.NewTicker(time.Second / time.Duration(rps)),
        tokens: make(chan struct{}, rps),
    }
}

// Functional options for configurable types (see design-patterns/SKILL.md)
type ServerOption func(*Server)
func WithTimeout(d time.Duration) ServerOption {
    return func(s *Server) { s.timeout = d }
}
```

### Defer & Resource Management
```go
// Always defer Close immediately after open+err check
f, err := os.Open(path)
if err != nil { return fmt.Errorf("open %s: %w", path, err) }
defer f.Close()

// Capture defer errors (critical for writers/flushers)
defer func() {
    if cerr := w.Close(); cerr != nil && err == nil {
        err = fmt.Errorf("close writer: %w", cerr)
    }
}()
```

---

## 2. Memory Performance

### Allocation Reduction
```go
// Pre-allocate slices when length is known
users := make([]User, 0, expectedCount) // cap set, avoids re-allocs
result := make(map[string]int, len(input))

// Reuse buffers with sync.Pool
var bufPool = sync.Pool{
    New: func() any { return new(bytes.Buffer) },
}
func process(data []byte) string {
    buf := bufPool.Get().(*bytes.Buffer)
    defer func() { buf.Reset(); bufPool.Put(buf) }()
    buf.Write(data)
    return buf.String()
}

// Avoid string<->[]byte conversions in hot paths
// Use unsafe only when profiled and justified:
func bytesToString(b []byte) string {
    return unsafe.String(unsafe.SliceData(b), len(b)) // Go 1.20+
}

// Struct field ordering — largest to smallest to minimize padding
type Efficient struct {
    ptr     *Thing    // 8 bytes
    size    int64     // 8 bytes
    count   int32     // 4 bytes
    flags   uint16    // 2 bytes
    enabled bool      // 1 byte
    _       [1]byte   // explicit padding for clarity
}
```

### Escape Analysis
```go
// Run: go build -gcflags="-m=2" to see escape decisions
// Keep hot-path objects on stack — avoid returning pointers to small structs
func sum(a, b int) int { return a + b } // stays on stack ✓

// Use value receivers for small, immutable types
func (p Point) Distance(q Point) float64 { ... } // no heap alloc ✓
```

### String Building
```go
// Never concatenate in loops
var sb strings.Builder
sb.Grow(estimatedLen) // pre-allocate
for _, s := range parts {
    sb.WriteString(s)
}
result := sb.String()
```

---

## 3. Concurrency Patterns

### Channel Idioms
```go
// Done channel for signaling (prefer context.Context)
done := make(chan struct{})
close(done) // broadcast to all receivers

// Bounded concurrency with semaphore channel
sem := make(chan struct{}, maxConcurrency)
for _, job := range jobs {
    sem <- struct{}{} // acquire
    go func(j Job) {
        defer func() { <-sem }() // release
        process(j)
    }(job)
}
// Drain semaphore to wait for all
for i := 0; i < cap(sem); i++ { sem <- struct{}{} }

// Fan-out / fan-in
func merge(cs ...<-chan Result) <-chan Result {
    out := make(chan Result)
    var wg sync.WaitGroup
    wg.Add(len(cs))
    for _, c := range cs {
        go func(c <-chan Result) {
            defer wg.Done()
            for v := range c { out <- v }
        }(c)
    }
    go func() { wg.Wait(); close(out) }()
    return out
}
```

### Context Propagation
```go
// Always first parameter, always named ctx
func (s *Service) DoWork(ctx context.Context, id ID) (*Result, error) {
    // Respect cancellation in all blocking calls
    select {
    case <-ctx.Done():
        return nil, ctx.Err()
    case result := <-s.workCh:
        return result, nil
    }
}

// Timeout wrapping at boundary
func (h *Handler) ServeHTTP(w http.ResponseWriter, r *http.Request) {
    ctx, cancel := context.WithTimeout(r.Context(), 30*time.Second)
    defer cancel()
    // pass ctx down
}

// Context values: typed keys only, never string keys
type contextKey struct{ name string }
var userKey = contextKey{"user"}
ctx = context.WithValue(ctx, userKey, user)
u, ok := ctx.Value(userKey).(*User)
```

### Mutex Best Practices
```go
// Embed mutex with the data it protects
type SafeCounter struct {
    mu    sync.Mutex
    count int  // guarded by mu
}
// Minimize lock scope
func (c *SafeCounter) Inc() {
    c.mu.Lock()
    c.count++
    c.mu.Unlock() // prefer explicit Unlock over defer in hot paths
}

// RWMutex for read-heavy workloads
type Cache struct {
    mu    sync.RWMutex
    items map[string]any
}
func (c *Cache) Get(key string) (any, bool) {
    c.mu.RLock()
    defer c.mu.RUnlock()
    v, ok := c.items[key]
    return v, ok
}
```

---

## 4. Generics (Go 1.18+)

```go
// Use generics to eliminate type assertions and boilerplate
func Map[T, U any](s []T, f func(T) U) []U {
    result := make([]U, len(s))
    for i, v := range s {
        result[i] = f(v)
    }
    return result
}

// Constraints for numeric types
type Number interface { ~int | ~int32 | ~int64 | ~float32 | ~float64 }
func Sum[T Number](nums []T) T {
    var total T
    for _, n := range nums { total += n }
    return total
}

// Generic Result type for explicit error handling
type Result[T any] struct {
    Value T
    Err   error
}
func (r Result[T]) Unwrap() (T, error) { return r.Value, r.Err }
```

---

## 5. Performance Profiling Workflow

```bash
# CPU profile
go test -cpuprofile=cpu.prof -bench=. ./...
go tool pprof -http=:6060 cpu.prof

# Memory profile
go test -memprofile=mem.prof -bench=. ./...
go tool pprof -http=:6060 mem.prof

# Trace (goroutine activity)
go test -trace=trace.out ./...
go tool trace trace.out

# Benchmarks: always use b.ReportAllocs()
func BenchmarkProcess(b *testing.B) {
    b.ReportAllocs()
    b.ResetTimer()
    for i := 0; i < b.N; i++ {
        _ = Process(testData)
    }
}
```

### Micro-Optimizations (apply only after profiling)
```go
// Branch prediction: likely/unlikely hints via ordering
// Put the common case first in if-else chains

// SIMD-friendly loops: keep loop bodies simple, avoid pointer chasing
// The compiler auto-vectorizes simple range loops over []float64/[]int32

// Inline small functions: go build -gcflags="-m" shows inlining decisions
// Functions >80 AST nodes won't be inlined — keep hot-path functions small

// Integer division by power-of-2: compiler converts to bit shift automatically
x / 8   // compiler emits SAR, not IDIV

// Avoid interface in hot loops — use concrete types or generics
```

---

## 6. Module & Toolchain Hygiene

```go
// go.mod: always pin direct deps, use minimal version selection
module github.com/org/project

go 1.22

require (
    golang.org/x/sync v0.7.0
    // ... only direct deps
)
```

```makefile
# Makefile targets every Go project should have
.PHONY: build test lint vet tidy check

build:
	go build -trimpath -ldflags="-s -w" ./cmd/...

test:
	go test -race -count=1 -coverprofile=coverage.out ./...

lint:
	golangci-lint run --fix

vet:
	go vet ./...

tidy:
	go mod tidy

check: vet lint test  ## run before every commit
```

---

## 7. Core Checklist

Before outputting any Go code, verify:
- [ ] All errors wrapped with `%w` and contextual message
- [ ] All goroutines have exit conditions (context, channel close, or errgroup)
- [ ] No global mutable state (use dependency injection)
- [ ] Interfaces defined at point of use (consumer side)
- [ ] Exported types/funcs have godoc comments
- [ ] No `init()` functions (prefer explicit initialization)
- [ ] `defer` used correctly (not in loops for resource close)
- [ ] Benchmarks exist for performance-critical paths
