---
name: pkg-sync
description: >
  Exhaustive reference for Go's sync and sync/atomic packages: Mutex, RWMutex, WaitGroup,
  Once, Map, Pool, Cond, atomic.Value, atomic integers, memory ordering guarantees, and common
  concurrency bugs. Load whenever using any sync primitive. Cross-references: concurrency/SKILL.md.
---

# Package: sync + sync/atomic — Complete Reference

## Imports
```go
import (
    "sync"
    "sync/atomic"
)
```

## 1. sync.Mutex

```go
// RULE: mutex is embedded with the data it protects
// RULE: document which fields the mutex guards
type SafeMap struct {
    mu    sync.Mutex
    items map[string]any  // guarded by mu
}

// Lock for writes
func (m *SafeMap) Set(key string, val any) {
    m.mu.Lock()
    m.items[key] = val
    m.mu.Unlock()  // prefer explicit Unlock in hot paths — defer has ~25ns overhead
}

// For complex operations or early returns: use defer
func (m *SafeMap) SetIfAbsent(key string, val any) bool {
    m.mu.Lock()
    defer m.mu.Unlock()
    if _, ok := m.items[key]; ok { return false }
    m.items[key] = val
    return true
}

// CRITICAL: never copy a Mutex after first use
// ✗ BAD: s2 := s1 (copies the mutex — race condition)
// ✓ GOOD: pass by pointer — &SafeMap{}
```

## 2. sync.RWMutex

```go
// RWMutex: multiple concurrent readers OR one writer
// Use when reads >> writes (read-heavy workloads)

type Cache struct {
    mu    sync.RWMutex
    items map[string]any  // guarded by mu
}

func (c *Cache) Get(key string) (any, bool) {
    c.mu.RLock()          // multiple goroutines can hold RLock simultaneously
    defer c.mu.RUnlock()
    v, ok := c.items[key]
    return v, ok
}

func (c *Cache) Set(key string, val any) {
    c.mu.Lock()           // exclusive — blocks all readers and other writers
    defer c.mu.Unlock()
    c.items[key] = val
}

// CAUTION: RWMutex is slower than Mutex when writes are frequent
// Benchmark before choosing: RWMutex adds overhead for the RW tracking
```

## 3. sync.WaitGroup

```go
// WaitGroup: wait for a collection of goroutines to finish

func processAll(ctx context.Context, items []Item) error {
    var wg sync.WaitGroup
    errs := make([]error, len(items))

    for i, item := range items {
        wg.Add(1)  // Add BEFORE launching goroutine (never inside)
        go func(idx int, it Item) {
            defer wg.Done()  // Done deferred — even on panic
            errs[idx] = process(ctx, it)
        }(i, item)  // pass as args — capture by value
    }

    wg.Wait()
    return errors.Join(errs...)  // nil elements are ignored by errors.Join
}

// RULE: Add() must be called before the goroutine is launched
// RULE: Done() always deferred — not called conditionally
// RULE: never pass WaitGroup by value — always by pointer
```

## 4. sync.Once

```go
// Once: run exactly once, even under concurrent calls
// Subsequent calls to Do() are no-ops (even from different goroutines)

type DB struct {
    once    sync.Once
    conn    *sql.DB
    connErr error
}

func (d *DB) Connect(dsn string) (*sql.DB, error) {
    d.once.Do(func() {
        d.conn, d.connErr = sql.Open("postgres", dsn)
    })
    return d.conn, d.connErr
}

// CAUTION: if Do's fn panics, Once considers it done — fn never re-runs
// CAUTION: if Do's fn returns error, Once still considers it done
// For retry-on-error: use atomic.Bool + mutex, not Once
```

## 5. sync.Map

```go
// sync.Map: concurrent map — optimized for two specific cases:
//   1. Write-once, read-many (cache-like)
//   2. Multiple goroutines write disjoint key sets
// For general use: prefer map + RWMutex (better performance)

var cache sync.Map

// Store
cache.Store("key", value)

// Load
if v, ok := cache.Load("key"); ok {
    user := v.(*User)  // type assertion required
}

// LoadOrStore: atomic get-or-set
actual, loaded := cache.LoadOrStore("key", newValue)
// If loaded=true: another goroutine stored first, actual is their value
// If loaded=false: we stored newValue

// Delete
cache.Delete("key")

// Range: iterate (snapshot semantics — safe during concurrent modification)
cache.Range(func(k, v any) bool {
    key := k.(string)
    val := v.(*User)
    fmt.Println(key, val)
    return true  // return false to stop iteration
})

// LoadAndDelete: atomic get+delete
if v, ok := cache.LoadAndDelete("key"); ok { /* ... */ }
```

## 6. sync.Pool

```go
// Pool: reuse objects to reduce GC pressure
// Objects MAY be collected by GC at any time — Pool is a hint, not a guarantee
// Never store state that must survive across Get/Put cycles

var bufPool = sync.Pool{
    New: func() any {
        b := make([]byte, 0, 4096)
        return &b
    },
}

func compress(data []byte) []byte {
    bufPtr := bufPool.Get().(*[]byte)
    buf := *bufPtr
    buf = buf[:0]  // reset length, keep capacity

    defer func() {
        *bufPtr = buf
        bufPool.Put(bufPtr)
    }()

    // ... use buf ...
    return append([]byte(nil), buf...)  // copy result out before returning buf to pool
}

// ✗ BAD: pooling objects that hold resources (connections, file handles)
// ✓ GOOD: pooling plain memory buffers, builder objects, scratch space
```

## 7. sync.Cond

```go
// Cond: condition variable — block goroutines until a condition is met
// Use when: "wait until some data is available AND you need to wake multiple waiters"
// Usually prefer channels — Cond is for specific patterns

type Queue struct {
    mu    sync.Mutex
    cond  *sync.Cond
    items []any
}

func NewQueue() *Queue {
    q := &Queue{}
    q.cond = sync.NewCond(&q.mu)
    return q
}

func (q *Queue) Enqueue(item any) {
    q.mu.Lock()
    q.items = append(q.items, item)
    q.mu.Unlock()
    q.cond.Signal()  // wake one waiter; Broadcast() wakes all
}

func (q *Queue) Dequeue() any {
    q.mu.Lock()
    defer q.mu.Unlock()
    for len(q.items) == 0 {
        q.cond.Wait()  // atomically: release mu + sleep; on wake: re-acquire mu
        // MUST be in loop — spurious wakeups are possible
    }
    item := q.items[0]
    q.items = q.items[1:]
    return item
}
```

## 8. sync/atomic

```go
// Atomic operations: lock-free, cheapest synchronization for simple types

// Integer atomics
var counter int64
atomic.AddInt64(&counter, 1)
atomic.AddInt64(&counter, -1)
v := atomic.LoadInt64(&counter)
atomic.StoreInt64(&counter, 0)
old := atomic.SwapInt64(&counter, 100)
swapped := atomic.CompareAndSwapInt64(&counter, expected, newVal)

// Go 1.19+: typed atomics (cleaner API)
var flag atomic.Bool
flag.Store(true)
if flag.Load() { /* ... */ }
flag.Swap(false)
flag.CompareAndSwap(true, false)

var count atomic.Int64
count.Add(1)
count.Load()

// atomic.Value: store/load any value atomically
// Constraint: all stored values must have SAME concrete type
var config atomic.Value
config.Store(&Config{/* ... */})       // Store *Config
cfg := config.Load().(*Config)         // Load *Config

// atomic.Pointer[T] (Go 1.19+): type-safe pointer swap
var ptr atomic.Pointer[Config]
ptr.Store(newConfig)
cfg := ptr.Load()
ptr.CompareAndSwap(oldCfg, newCfg)
```

## Common Concurrency Bugs

```go
// BUG 1: Copying a mutex
type Safe struct { mu sync.Mutex }
s := Safe{}
s2 := s  // ✗ copies mu — both share zero state but are independent (not what you want)
// ✓ pass *Safe everywhere

// BUG 2: Unlock in defer when lock not always acquired
func (m *Map) trySet(key string, val any) {
    if key == "" { return }  // no lock acquired — defer would unlock unacquired mutex!
    m.mu.Lock()
    defer m.mu.Unlock()  // safe: only reached after Lock()
    m.items[key] = val
}

// BUG 3: wg.Add inside goroutine (race condition)
for _, item := range items {
    go func(it Item) {
        wg.Add(1)  // ✗ — wg.Wait() may return before this Add is called
        defer wg.Done()
    }(item)
}
// ✓ Add before launching goroutine

// BUG 4: Returning slice/map element while holding no lock
func (m *SafeMap) GetSlice(key string) []string {
    m.mu.RLock()
    defer m.mu.RUnlock()
    return m.items[key]  // ✗ returns reference to internal slice — caller can mutate
}
// ✓ return a copy:
result := make([]string, len(m.items[key]))
copy(result, m.items[key])
return result
```

## sync Checklist
- [ ] Mutex embedded next to the data it protects
- [ ] `defer mu.Unlock()` when there are multiple return paths in the locked section
- [ ] `wg.Add(n)` called before goroutine launch, never inside
- [ ] `sync.Pool.Get` result always reset before use (stale data risk)
- [ ] `sync.Map` only for write-once-read-many or disjoint-key patterns
- [ ] All `atomic.Value` stores use same concrete type
- [ ] Mutex/WaitGroup always passed by pointer — never copied
