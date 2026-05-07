---
name: golang-concurrency
description: >
  Advanced Go concurrency: CSP patterns, actor model, lock-free data structures, worker pools,
  pipeline stages, fan-out/fan-in, pub/sub, rate-limiting, backpressure, goroutine lifecycle
  management, sync primitives (Mutex, RWMutex, Once, Map, Pool, Cond), atomic operations,
  memory model guarantees, and Go scheduler internals. Use for any concurrent code, parallel
  processing, event-driven goroutine design, or performance-critical shared state. Always combine
  with packages/sync/SKILL.md and packages/errgroup/SKILL.md.
---

# Go Concurrency — Production Patterns

## 1. Go Memory Model Fundamentals

```go
// The Go memory model guarantee: a send on a channel happens-before the receive completes.
// A close happens-before a receive that returns a zero value due to closure.
// sync.Mutex: Unlock happens-before the next Lock.
// sync.Once: the first call to Do happens-before any Do returns.

// Safe: initialization via sync.Once
type Singleton struct{ value string }
var (
    instance *Singleton
    once     sync.Once
)
func GetInstance() *Singleton {
    once.Do(func() { instance = &Singleton{value: "initialized"} })
    return instance
}

// RACE: two goroutines write/read without synchronization
// FIX: use channel, mutex, or atomic
var counter int64
// Safe atomic increment:
atomic.AddInt64(&counter, 1)
// Safe read:
v := atomic.LoadInt64(&counter)
```

---

## 2. Worker Pool with Backpressure

```go
// Production worker pool: bounded, context-aware, collects errors
type WorkerPool[T, R any] struct {
    workers   int
    jobsCh    chan T
    resultsCh chan Result[R]
    process   func(context.Context, T) (R, error)
    wg        sync.WaitGroup
}

type Result[R any] struct {
    Value R
    Err   error
    Input any // for error correlation
}

func NewWorkerPool[T, R any](workers int, bufSize int, fn func(context.Context, T) (R, error)) *WorkerPool[T, R] {
    return &WorkerPool[T, R]{
        workers:   workers,
        jobsCh:    make(chan T, bufSize),
        resultsCh: make(chan Result[R], bufSize),
        process:   fn,
    }
}

func (p *WorkerPool[T, R]) Start(ctx context.Context) {
    for i := 0; i < p.workers; i++ {
        p.wg.Add(1)
        go func() {
            defer p.wg.Done()
            for {
                select {
                case job, ok := <-p.jobsCh:
                    if !ok { return } // channel closed
                    v, err := p.process(ctx, job)
                    select {
                    case p.resultsCh <- Result[R]{Value: v, Err: err}:
                    case <-ctx.Done(): return
                    }
                case <-ctx.Done(): return
                }
            }
        }()
    }
    // Close results when all workers done
    go func() { p.wg.Wait(); close(p.resultsCh) }()
}

// Submit enqueues a job — blocks if pool is full (backpressure)
func (p *WorkerPool[T, R]) Submit(ctx context.Context, job T) error {
    select {
    case p.jobsCh <- job: return nil
    case <-ctx.Done(): return ctx.Err()
    }
}

func (p *WorkerPool[T, R]) Close()              { close(p.jobsCh) }
func (p *WorkerPool[T, R]) Results() <-chan Result[R] { return p.resultsCh }
```

---

## 3. Pipeline — Typed Stages

```go
// Each stage is a goroutine reading from input and writing to output.
// Context cancellation propagates through all stages automatically.

// Stage function signature
type Stage[In, Out any] func(ctx context.Context, in <-chan In) <-chan Out

// Generator: source of items
func Generate[T any](ctx context.Context, items []T) <-chan T {
    ch := make(chan T, len(items))
    go func() {
        defer close(ch)
        for _, item := range items {
            select {
            case ch <- item:
            case <-ctx.Done(): return
            }
        }
    }()
    return ch
}

// Transform stage
func Transform[In, Out any](ctx context.Context, in <-chan In, fn func(In) (Out, error)) <-chan Out {
    out := make(chan Out, cap(in))
    go func() {
        defer close(out)
        for item := range in {
            result, err := fn(item)
            if err != nil {
                slog.ErrorContext(ctx, "pipeline transform error", "err", err)
                continue // or send to error channel
            }
            select {
            case out <- result:
            case <-ctx.Done(): return
            }
        }
    }()
    return out
}

// Fan-out: distribute work across N workers
func FanOut[T any](ctx context.Context, in <-chan T, n int) []<-chan T {
    outs := make([]chan T, n)
    for i := range outs { outs[i] = make(chan T, cap(in)) }

    go func() {
        defer func() { for _, o := range outs { close(o) } }()
        i := 0
        for item := range in {
            select {
            case outs[i%n] <- item:
                i++
            case <-ctx.Done(): return
            }
        }
    }()

    result := make([]<-chan T, n)
    for i, o := range outs { result[i] = o }
    return result
}

// Fan-in: merge N channels into one
func FanIn[T any](ctx context.Context, inputs ...<-chan T) <-chan T {
    out := make(chan T, len(inputs))
    var wg sync.WaitGroup
    wg.Add(len(inputs))

    for _, in := range inputs {
        in := in
        go func() {
            defer wg.Done()
            for item := range in {
                select {
                case out <- item:
                case <-ctx.Done(): return
                }
            }
        }()
    }
    go func() { wg.Wait(); close(out) }()
    return out
}
```

---

## 4. Actor Model

```go
// Actor: encapsulates state, communicates via message passing only.
// No shared memory = no locks needed on internal state.

type ActorMessage[T any] struct {
    Payload T
    Reply   chan<- ActorReply
}

type ActorReply struct {
    Value any
    Err   error
}

type OrderActor struct {
    id       order.ID
    state    *order.Order
    inbox    chan ActorMessage[ActorCommand]
    shutdown chan struct{}
}

type ActorCommand interface{ actorCommand() }
type ConfirmCommand struct{}
type GetStateCommand struct{}
func (ConfirmCommand) actorCommand()  {}
func (GetStateCommand) actorCommand() {}

func NewOrderActor(o *order.Order) *OrderActor {
    a := &OrderActor{
        id:       o.ID(),
        state:    o,
        inbox:    make(chan ActorMessage[ActorCommand], 100),
        shutdown: make(chan struct{}),
    }
    go a.run()
    return a
}

func (a *OrderActor) run() {
    for {
        select {
        case msg := <-a.inbox:
            a.handle(msg)
        case <-a.shutdown:
            return
        }
    }
}

func (a *OrderActor) handle(msg ActorMessage[ActorCommand]) {
    switch msg.Payload.(type) {
    case ConfirmCommand:
        err := a.state.Confirm()
        msg.Reply <- ActorReply{Err: err}
    case GetStateCommand:
        msg.Reply <- ActorReply{Value: a.state.Status()}
    }
}

// Thread-safe message send
func (a *OrderActor) Send(ctx context.Context, cmd ActorCommand) (any, error) {
    reply := make(chan ActorReply, 1)
    select {
    case a.inbox <- ActorMessage[ActorCommand]{Payload: cmd, Reply: reply}:
    case <-ctx.Done():
        return nil, ctx.Err()
    }
    select {
    case r := <-reply:
        return r.Value, r.Err
    case <-ctx.Done():
        return nil, ctx.Err()
    }
}

func (a *OrderActor) Stop() { close(a.shutdown) }
```

---

## 5. Pub/Sub with Type Safety

```go
// Typed, generic pub/sub broker — no interface{} casts
type Broker[T any] struct {
    mu   sync.RWMutex
    subs map[string][]chan T
}

func NewBroker[T any]() *Broker[T] {
    return &Broker[T]{subs: make(map[string][]chan T)}
}

// Subscribe returns a channel and a cancel function
func (b *Broker[T]) Subscribe(topic string, buf int) (<-chan T, func()) {
    ch := make(chan T, buf)
    b.mu.Lock()
    b.subs[topic] = append(b.subs[topic], ch)
    b.mu.Unlock()

    return ch, func() {
        b.mu.Lock()
        defer b.mu.Unlock()
        subs := b.subs[topic]
        for i, s := range subs {
            if s == ch {
                b.subs[topic] = append(subs[:i], subs[i+1:]...)
                close(ch)
                return
            }
        }
    }
}

// Publish sends to all subscribers. Drops if subscriber buffer full (non-blocking).
func (b *Broker[T]) Publish(ctx context.Context, topic string, msg T) int {
    b.mu.RLock()
    defer b.mu.RUnlock()
    delivered := 0
    for _, ch := range b.subs[topic] {
        select {
        case ch <- msg:
            delivered++
        case <-ctx.Done(): return delivered
        default: // slow consumer — drop
            slog.WarnContext(ctx, "broker: slow consumer, dropping message", "topic", topic)
        }
    }
    return delivered
}
```

---

## 6. Rate Limiter (Token Bucket)

```go
import "golang.org/x/time/rate"

// Per-key rate limiter with LRU eviction for memory bound
type KeyedRateLimiter struct {
    mu       sync.Mutex
    limiters map[string]*rateLimiterEntry
    rate     rate.Limit
    burst    int
    maxKeys  int
}

type rateLimiterEntry struct {
    limiter  *rate.Limiter
    lastSeen time.Time
}

func (krl *KeyedRateLimiter) Allow(key string) bool {
    krl.mu.Lock()
    defer krl.mu.Unlock()
    entry, ok := krl.limiters[key]
    if !ok {
        if len(krl.limiters) >= krl.maxKeys { krl.evictOldest() }
        entry = &rateLimiterEntry{limiter: rate.NewLimiter(krl.rate, krl.burst)}
        krl.limiters[key] = entry
    }
    entry.lastSeen = time.Now()
    return entry.limiter.Allow()
}

func (krl *KeyedRateLimiter) evictOldest() {
    var oldestKey string
    var oldestTime time.Time
    for k, v := range krl.limiters {
        if oldestKey == "" || v.lastSeen.Before(oldestTime) {
            oldestKey, oldestTime = k, v.lastSeen
        }
    }
    delete(krl.limiters, oldestKey)
}
```

---

## 7. Singleflight (Deduplicate Concurrent Calls)

```go
import "golang.org/x/sync/singleflight"

type CachedService struct {
    sfg   singleflight.Group
    cache Cache
    repo  Repository
}

// GetUser: concurrent callers for same ID share a single DB fetch
func (s *CachedService) GetUser(ctx context.Context, id string) (*User, error) {
    // Check cache first
    if u, ok, _ := s.cache.Get(ctx, "user:"+id); ok {
        return u.(*User), nil
    }

    // Singleflight: only one fetch for duplicate in-flight requests
    v, err, _ := s.sfg.Do("user:"+id, func() (any, error) {
        u, err := s.repo.FindByID(ctx, id)
        if err != nil { return nil, err }
        _ = s.cache.Set(ctx, "user:"+id, u, 5*time.Minute)
        return u, nil
    })
    if err != nil { return nil, err }
    return v.(*User), nil
}
```

---

## 8. Concurrency Checklist

- [ ] Every goroutine has a clear owner and lifetime (started in, stopped by)
- [ ] All goroutines respect `ctx.Done()` — no goroutine leaks
- [ ] Channels are created at the right size (0=sync, N=async-bounded, never unbounded)
- [ ] Send and receive select on `ctx.Done()` to prevent deadlocks
- [ ] Mutex fields documented with which data they protect
- [ ] No naked `time.Sleep` in goroutines — use `time.After` or `time.NewTicker` with select
- [ ] `sync.Pool` used for high-churn objects to reduce GC pressure
- [ ] `singleflight` used for cache stampede / thundering herd scenarios
- [ ] `atomic` used only for simple counters/flags — not for complex state
- [ ] Race detector passes: `go test -race ./...`
- [ ] Worker pool size tuned to CPU cores for CPU-bound, higher for IO-bound
- [ ] Backpressure implemented at every stage — never unbounded queues
