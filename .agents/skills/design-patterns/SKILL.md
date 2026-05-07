---
name: golang-design-patterns
description: >
  Go-idiomatic design patterns: creational (functional options, builder, factory), structural
  (adapter, decorator/middleware, proxy, composite), behavioral (strategy, observer/event bus,
  command, pipeline, state machine), and concurrency patterns (worker pool, fan-out/fan-in,
  pub/sub, saga). Use for any architecture or structural design decision in Go. Always combine
  with solid-principles/SKILL.md.
---

# Go Design Patterns — Idiomatic Implementations

## Creational Patterns

### Functional Options (Go's primary configuration pattern)
```go
// The canonical Go way to configure complex objects
type Server struct {
    addr         string
    timeout      time.Duration
    maxConns     int
    tls          *tls.Config
    logger       *slog.Logger
}

type Option func(*Server)

func WithAddr(addr string)             Option { return func(s *Server) { s.addr = addr } }
func WithTimeout(d time.Duration)      Option { return func(s *Server) { s.timeout = d } }
func WithMaxConns(n int)               Option { return func(s *Server) { s.maxConns = n } }
func WithTLS(cfg *tls.Config)          Option { return func(s *Server) { s.tls = cfg } }
func WithLogger(l *slog.Logger)        Option { return func(s *Server) { s.logger = l } }

func NewServer(opts ...Option) (*Server, error) {
    s := &Server{
        addr:     ":8080",             // sensible defaults
        timeout:  30 * time.Second,
        maxConns: 1000,
        logger:   slog.Default(),
    }
    for _, o := range opts { o(s) }
    if err := s.validate(); err != nil {
        return nil, fmt.Errorf("invalid server config: %w", err)
    }
    return s, nil
}
```

### Builder (for complex multi-step construction)
```go
// Use when object construction has ordering requirements or validation phases
type QueryBuilder struct {
    table      string
    conditions []string
    orderBy    string
    limit      int
    args       []any
    err        error // carry error through chain
}

func NewQuery(table string) *QueryBuilder { return &QueryBuilder{table: table, limit: -1} }

func (b *QueryBuilder) Where(cond string, args ...any) *QueryBuilder {
    if b.err != nil { return b }
    b.conditions = append(b.conditions, cond)
    b.args = append(b.args, args...)
    return b
}

func (b *QueryBuilder) OrderBy(col string, desc bool) *QueryBuilder {
    if desc { b.orderBy = col + " DESC" } else { b.orderBy = col + " ASC" }
    return b
}

func (b *QueryBuilder) Limit(n int) *QueryBuilder {
    if n <= 0 { b.err = fmt.Errorf("limit must be positive, got %d", n); return b }
    b.limit = n; return b
}

func (b *QueryBuilder) Build() (string, []any, error) {
    if b.err != nil { return "", nil, b.err }
    // construct SQL...
    return query, b.args, nil
}
```

### Factory Method
```go
// Factory: returns an interface, hides implementations
type StorageBackend interface {
    Store(ctx context.Context, key string, data []byte) error
    Fetch(ctx context.Context, key string) ([]byte, error)
}

func NewStorageBackend(cfg Config) (StorageBackend, error) {
    switch cfg.Type {
    case "s3":       return newS3Backend(cfg)
    case "gcs":      return newGCSBackend(cfg)
    case "local":    return newLocalBackend(cfg)
    default:
        return nil, fmt.Errorf("unknown storage backend %q", cfg.Type)
    }
}
```

---

## Structural Patterns

### Decorator / Middleware
```go
// Stack decorators to add cross-cutting concerns without modifying core logic
type Handler[Req, Resp any] func(context.Context, Req) (Resp, error)

// Logging decorator
func WithLogging[Req, Resp any](name string, logger *slog.Logger, h Handler[Req, Resp]) Handler[Req, Resp] {
    return func(ctx context.Context, req Req) (Resp, error) {
        start := time.Now()
        resp, err := h(ctx, req)
        logger.InfoContext(ctx, "call", "op", name, "duration", time.Since(start), "err", err)
        return resp, err
    }
}

// Metrics decorator
func WithMetrics[Req, Resp any](name string, hist Histogram, h Handler[Req, Resp]) Handler[Req, Resp] {
    return func(ctx context.Context, req Req) (Resp, error) {
        start := time.Now()
        resp, err := h(ctx, req)
        hist.Observe(time.Since(start).Seconds(), statusLabel(err))
        return resp, err
    }
}

// Retry decorator
func WithRetry[Req, Resp any](maxAttempts int, h Handler[Req, Resp]) Handler[Req, Resp] {
    return func(ctx context.Context, req Req) (Resp, error) {
        var (zero Resp; err error)
        for i := 0; i < maxAttempts; i++ {
            if resp, err := h(ctx, req); err == nil { return resp, nil }
            if !isRetryable(err) { break }
            select {
            case <-time.After(backoff(i)):
            case <-ctx.Done(): return zero, ctx.Err()
            }
        }
        return zero, err
    }
}
```

### Adapter
```go
// Wrap external types to satisfy internal interfaces
// Internal interface (our domain)
type Logger interface {
    Info(msg string, fields ...any)
    Error(msg string, err error, fields ...any)
}

// Adapter for zap
type zapAdapter struct{ log *zap.Logger }

func NewZapAdapter(l *zap.Logger) Logger { return &zapAdapter{log: l} }

func (a *zapAdapter) Info(msg string, fields ...any) {
    a.log.Info(msg, toZapFields(fields)...)
}
func (a *zapAdapter) Error(msg string, err error, fields ...any) {
    a.log.Error(msg, append([]zap.Field{zap.Error(err)}, toZapFields(fields)...)...)
}
```

### Proxy (for caching, access control, lazy loading)
```go
// Caching proxy for Repository
type CachingRepository struct {
    origin Repository
    cache  Cache
    ttl    time.Duration
}

func (r *CachingRepository) FindByID(ctx context.Context, id ID) (*Entity, error) {
    key := fmt.Sprintf("entity:%d", id)
    if v, ok, _ := r.cache.Get(ctx, key); ok {
        return v.(*Entity), nil
    }
    entity, err := r.origin.FindByID(ctx, id)
    if err != nil { return nil, err }
    _ = r.cache.Set(ctx, key, entity, r.ttl)
    return entity, nil
}
```

---

## Behavioral Patterns

### Strategy
```go
// Swap algorithms at runtime without changing context
type SortStrategy[T any] interface {
    Sort([]T, func(a, b T) bool)
}

type QuickSort[T any] struct{}
func (q *QuickSort[T]) Sort(s []T, less func(a, b T) bool) { /* quicksort impl */ }

type MergeSort[T any] struct{}
func (m *MergeSort[T]) Sort(s []T, less func(a, b T) bool) { /* mergesort impl */ }

type Sorter[T any] struct{ strategy SortStrategy[T] }
func (s *Sorter[T]) SetStrategy(st SortStrategy[T]) { s.strategy = st }
func (s *Sorter[T]) Sort(items []T, less func(a, b T) bool) { s.strategy.Sort(items, less) }
```

### Observer / Event Bus
```go
// Decoupled pub/sub with typed events
type EventBus struct {
    mu          sync.RWMutex
    subscribers map[string][]chan Event
}

func (b *EventBus) Subscribe(topic string, bufSize int) (<-chan Event, func()) {
    ch := make(chan Event, bufSize)
    b.mu.Lock()
    b.subscribers[topic] = append(b.subscribers[topic], ch)
    b.mu.Unlock()
    
    cancel := func() {
        b.mu.Lock()
        defer b.mu.Unlock()
        subs := b.subscribers[topic]
        for i, s := range subs {
            if s == ch {
                b.subscribers[topic] = append(subs[:i], subs[i+1:]...)
                close(ch)
                return
            }
        }
    }
    return ch, cancel
}

func (b *EventBus) Publish(ctx context.Context, topic string, event Event) {
    b.mu.RLock()
    defer b.mu.RUnlock()
    for _, ch := range b.subscribers[topic] {
        select {
        case ch <- event:
        case <-ctx.Done(): return
        default: // drop if subscriber is slow — no blocking publisher
        }
    }
}
```

### Command (with undo)
```go
type Command interface {
    Execute(ctx context.Context) error
    Undo(ctx context.Context) error
}

type CommandHistory struct {
    stack []Command
    mu    sync.Mutex
}

func (h *CommandHistory) Execute(ctx context.Context, cmd Command) error {
    if err := cmd.Execute(ctx); err != nil { return err }
    h.mu.Lock()
    h.stack = append(h.stack, cmd)
    h.mu.Unlock()
    return nil
}

func (h *CommandHistory) Undo(ctx context.Context) error {
    h.mu.Lock()
    defer h.mu.Unlock()
    if len(h.stack) == 0 { return errors.New("nothing to undo") }
    last := h.stack[len(h.stack)-1]
    h.stack = h.stack[:len(h.stack)-1]
    return last.Undo(ctx)
}
```

### Pipeline
```go
// Type-safe processing pipeline with context cancellation
func Pipeline[T any](ctx context.Context, input <-chan T, stages ...func(context.Context, T) (T, error)) <-chan result[T] {
    out := make(chan result[T])
    go func() {
        defer close(out)
        for item := range input {
            current := item
            var err error
            for _, stage := range stages {
                if current, err = stage(ctx, current); err != nil { break }
            }
            select {
            case out <- result[T]{value: current, err: err}:
            case <-ctx.Done(): return
            }
        }
    }()
    return out
}

type result[T any] struct { value T; err error }
```

### State Machine
```go
// Explicit, type-safe state machine
type OrderState string
const (
    StateNew       OrderState = "new"
    StateConfirmed OrderState = "confirmed"
    StateShipped   OrderState = "shipped"
    StateCancelled OrderState = "cancelled"
)

type Transition struct {
    From    OrderState
    To      OrderState
    Action  func(context.Context, *Order) error
}

type OrderFSM struct {
    state       OrderState
    transitions map[OrderState]map[string]Transition
}

func (m *OrderFSM) Trigger(ctx context.Context, event string, order *Order) error {
    transitions, ok := m.transitions[m.state]
    if !ok { return fmt.Errorf("no transitions from state %q", m.state) }
    
    t, ok := transitions[event]
    if !ok { return fmt.Errorf("invalid event %q in state %q", event, m.state) }
    
    if err := t.Action(ctx, order); err != nil { return err }
    m.state = t.To
    return nil
}
```

---

## Concurrency Patterns

### Worker Pool
```go
func WorkerPool[T, R any](ctx context.Context, workers int,
    jobs <-chan T, process func(context.Context, T) (R, error)) <-chan result[R] {
    
    results := make(chan result[R], workers)
    var wg sync.WaitGroup
    wg.Add(workers)
    
    for i := 0; i < workers; i++ {
        go func() {
            defer wg.Done()
            for job := range jobs {
                if ctx.Err() != nil { return }
                v, err := process(ctx, job)
                results <- result[R]{value: v, err: err}
            }
        }()
    }
    
    go func() { wg.Wait(); close(results) }()
    return results
}
```

---

## Pattern Selection Guide

| Situation | Pattern |
|---|---|
| Configuring complex objects | Functional Options |
| Multi-step object construction with validation | Builder |
| Hide implementation family behind interface | Factory |
| Add cross-cutting concerns without modification | Decorator/Middleware |
| Wrap incompatible external types | Adapter |
| Caching/access control on existing interface | Proxy |
| Interchangeable algorithms | Strategy |
| Decoupled event notification | Observer/EventBus |
| Reversible operations | Command + History |
| Sequential data transformation | Pipeline |
| Managed lifecycle transitions | State Machine |
| Bounded concurrent processing | Worker Pool |
