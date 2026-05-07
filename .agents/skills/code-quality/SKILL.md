---
name: golang-code-quality
description: >
  Go code quality properties: cohesion, composability, pluggability, extensibility, robustness,
  non-false-positive code, defensive programming, design by contract, invariant enforcement,
  graceful degradation, and idempotency. Use for code reviews, architectural decisions, and as
  a companion to every other skill when quality guarantees matter (which is always).
  This skill defines the WHAT and WHY; other skills define the HOW.
---

# Go Code Quality — Cohesion, Composability, Robustness

## 1. Cohesion — Things That Change Together, Live Together

**High cohesion**: A module/package/type has ONE clear purpose and all its parts serve that purpose.
**Low cohesion (god object)**: Parts serve different purposes bundled for convenience.

```go
// ✗ LOW COHESION — User "service" does 5 unrelated things
type UserService struct{}
func (s *UserService) Register(...)        // user domain ✓
func (s *UserService) SendEmail(...)       // notification domain ✗
func (s *UserService) GenerateReport(...)  // reporting domain ✗
func (s *UserService) ChargeCard(...)      // payment domain ✗
func (s *UserService) ResizeAvatar(...)    // media domain ✗

// ✓ HIGH COHESION — each package owns its concern completely
package user      // entity, validation, repository interface
package notify    // email/SMS/push — all notification methods
package report    // report generation, templates, export
package payment   // charge, refund, subscription
package media     // upload, resize, CDN

// Package cohesion test: can you describe this package in one sentence
// without using "and"? If not, split it.
```

---

## 2. Composability — Small Units That Combine

```go
// Composable: each piece works standalone AND combines cleanly

// Single-purpose middleware functions compose into a chain
type Middleware func(http.Handler) http.Handler

func Chain(h http.Handler, mw ...Middleware) http.Handler {
    for i := len(mw) - 1; i >= 0; i-- { h = mw[i](h) }
    return h
}

// Usage: each middleware is self-contained, independently testable
handler := Chain(userHandler,
    middleware.RequestID,
    middleware.Logging(logger),
    middleware.Auth(tokenValidator),
    middleware.RateLimit(limiter),
    middleware.Recover(logger),
)

// Composable options
type Config struct {
    Timeout    time.Duration
    MaxRetries int
    Logger     *slog.Logger
}

// Options compose without conflict
func With(opts ...Option) *Client {
    cfg := defaultConfig()
    for _, o := range opts { o(&cfg) }
    return newClient(cfg)
}
```

---

## 3. Pluggability — Swap Implementations Without Changing Consumers

```go
// Plugin pattern: define interface, swap implementations at wire-up
// The consumer never knows which implementation it's using.

// Port (interface) — defined at consumer site
type MetricsRecorder interface {
    Inc(metric string, tags ...string)
    Histogram(metric string, value float64, tags ...string)
    Gauge(metric string, value float64, tags ...string)
}

// Plugin 1: Prometheus
type PrometheusRecorder struct { /* ... */ }
func (p *PrometheusRecorder) Inc(metric string, tags ...string)              { /* ... */ }
func (p *PrometheusRecorder) Histogram(metric string, v float64, tags ...string) { /* ... */ }
func (p *PrometheusRecorder) Gauge(metric string, v float64, tags ...string)    { /* ... */ }

// Plugin 2: Datadog
type DatadogRecorder struct { /* ... */ }
func (d *DatadogRecorder) Inc(metric string, tags ...string)              { /* ... */ }
// ...

// Plugin 3: No-op (for tests — zero overhead, never panics)
type NoopRecorder struct{}
func (n NoopRecorder) Inc(_ string, _ ...string)                  {}
func (n NoopRecorder) Histogram(_ string, _ float64, _ ...string) {}
func (n NoopRecorder) Gauge(_ string, _ float64, _ ...string)     {}

// Registry pattern: register plugins at startup
type PluginRegistry[T any] struct {
    mu      sync.RWMutex
    plugins map[string]T
}

func (r *PluginRegistry[T]) Register(name string, plugin T) {
    r.mu.Lock(); defer r.mu.Unlock()
    r.plugins[name] = plugin
}

func (r *PluginRegistry[T]) Get(name string) (T, bool) {
    r.mu.RLock(); defer r.mu.RUnlock()
    p, ok := r.plugins[name]
    return p, ok
}
```

---

## 4. Extensibility — Open for Extension, Closed for Modification

```go
// Hook system: callers register hooks, core code is never modified
type ProcessingHook interface {
    Before(ctx context.Context, item any) error
    After(ctx context.Context, item any, result any, err error)
}

type Processor struct {
    hooks []ProcessingHook
}

func (p *Processor) AddHook(h ProcessingHook) { p.hooks = append(p.hooks, h) }

func (p *Processor) Process(ctx context.Context, item any) (any, error) {
    for _, h := range p.hooks {
        if err := h.Before(ctx, item); err != nil {
            return nil, fmt.Errorf("hook.Before: %w", err)
        }
    }
    result, err := p.coreProcess(ctx, item)
    for _, h := range p.hooks {
        h.After(ctx, item, result, err)
    }
    return result, err
}

// New behaviors = new hook implementations. Core never changes.
type AuditHook struct{ log *slog.Logger }
type MetricsHook struct{ rec MetricsRecorder }
type TracingHook struct{ tracer trace.Tracer }
```

---

## 5. Robustness — Never Crash, Never Silently Corrupt

### Defensive Programming
```go
// Validate all inputs at boundaries — never assume callers are correct
func (s *Service) CreateUser(ctx context.Context, req CreateUserRequest) (*User, error) {
    // 1. Nil safety
    if ctx == nil { return nil, errors.New("CreateUser: ctx must not be nil") }

    // 2. Input validation — explicit, exhaustive
    if err := req.Validate(); err != nil {
        return nil, fmt.Errorf("CreateUser: invalid request: %w", err)
    }

    // 3. Defensive copy of mutable inputs
    reqCopy := req.Clone()

    // 4. Proceed with validated, copied data
    return s.create(ctx, reqCopy)
}

// Invariant enforcement: panic only for programmer errors, not user errors
func (c *Cache) Get(key string) (any, bool) {
    if key == "" {
        panic("Cache.Get: key must not be empty") // programmer error
    }
    // ...
}

// Design by contract: document preconditions, postconditions
// Get returns the value for key. Precondition: key is non-empty.
// Postcondition: if ok is false, returned value is the zero value.
func (c *Cache) Get(key string) (value any, ok bool) { /* ... */ }
```

### Defensive Copy
```go
// Return copies of slices/maps to prevent external mutation
func (o *Order) Items() []LineItem {
    result := make([]LineItem, len(o.items))
    copy(result, o.items)
    return result
}

// Accept copies of inputs (when method stores them)
func (s *Set[T]) AddAll(items []T) {
    for _, item := range items { s.items[item] = struct{}{} }
    // items slice is iterated but not stored — no copy needed here
}
```

---

## 6. Non-False-Positive Code

**False positive**: code that signals success/valid but produces wrong results silently.

```go
// ✗ FALSE POSITIVE — returns success when it should fail
func ParseDate(s string) time.Time {
    t, _ := time.Parse(time.RFC3339, s) // silently returns zero time on error
    return t
}
// Caller gets zero time.Time and doesn't know it's wrong.

// ✓ HONEST — propagates the error
func ParseDate(s string) (time.Time, error) {
    t, err := time.Parse(time.RFC3339, s)
    if err != nil { return time.Time{}, fmt.Errorf("ParseDate(%q): %w", s, err) }
    return t, nil
}

// ✗ FALSE POSITIVE — silently truncates data
func (r *Ring) Write(data []byte) {
    copy(r.buf[r.head:], data) // silently truncates if data > remaining capacity
}

// ✓ HONEST — caller knows when truncation would occur
func (r *Ring) Write(data []byte) (int, error) {
    available := cap(r.buf) - r.head
    if len(data) > available {
        return 0, fmt.Errorf("Ring.Write: buffer full (have %d, need %d)", available, len(data))
    }
    n := copy(r.buf[r.head:], data)
    r.head += n
    return n, nil
}

// ✗ FALSE POSITIVE — empty result looks like valid empty result
func (r *Repo) FindAll(ctx context.Context) []*User {
    users, err := r.db.Query(ctx, "SELECT ...")
    if err != nil { return nil } // silent! caller sees nil, not error
    return users
}

// ✓ HONEST — separate empty from error
func (r *Repo) FindAll(ctx context.Context) ([]*User, error) {
    users, err := r.db.Query(ctx, "SELECT ...")
    if err != nil { return nil, fmt.Errorf("Repo.FindAll: %w", err) }
    return users, nil // nil slice is valid "empty" result
}
```

---

## 7. Idempotency

```go
// Idempotent operations produce the same result regardless of how many times called.
// Critical for: API endpoints, message consumers, scheduled jobs.

// HTTP: PUT and DELETE must be idempotent. POST may not be.
// Database: use UPSERT (INSERT ... ON CONFLICT DO UPDATE)
// Kafka/MQ: use idempotency key in the message

func (h *Handler) CreatePayment(w http.ResponseWriter, r *http.Request) {
    idempotencyKey := r.Header.Get("Idempotency-Key")
    if idempotencyKey == "" {
        http.Error(w, "Idempotency-Key header required", http.StatusBadRequest)
        return
    }

    // Check if we already processed this key
    existing, err := h.idempotencyStore.Get(r.Context(), idempotencyKey)
    if err == nil {
        // Already processed — return cached result
        h.respondJSON(w, existing.Status, existing.Response)
        return
    }
    if !errors.Is(err, ErrNotFound) {
        h.respondError(w, r, err, http.StatusInternalServerError)
        return
    }

    // Process and store result under idempotency key
    result, err := h.service.CreatePayment(r.Context(), req)
    status := http.StatusCreated
    if err != nil { status = httpStatusFor(err) }
    h.idempotencyStore.Set(r.Context(), idempotencyKey, status, result, 24*time.Hour)
    h.respondJSON(w, status, result)
}
```

---

## 8. Graceful Degradation

```go
// When a non-critical dependency fails, degrade gracefully — don't fail the whole request.
func (h *Handler) GetProduct(w http.ResponseWriter, r *http.Request) {
    product, err := h.repo.FindByID(r.Context(), r.PathValue("id"))
    if err != nil {
        h.respondError(w, r, err, http.StatusInternalServerError)
        return // critical dependency failed — cannot serve
    }

    // Non-critical: enrichment from recommendation service
    recommendations, err := h.recService.GetSimilar(r.Context(), product.ID)
    if err != nil {
        // Degrade gracefully — product still serves without recommendations
        slog.WarnContext(r.Context(), "recommendations unavailable",
            "product_id", product.ID, "err", err)
        recommendations = nil // zero value is valid empty
    }

    h.respondJSON(w, http.StatusOK, ProductResponse{
        Product:         product,
        Recommendations: recommendations, // may be nil — frontend handles it
    })
}
```

---

## Code Quality Checklist

- [ ] **Cohesion**: Each package/type has one purpose expressible in one sentence without "and"
- [ ] **Composability**: Middleware/decorators/options combine without side effects
- [ ] **Pluggability**: All IO dependencies behind interfaces; swappable in tests and prod
- [ ] **Extensibility**: New behaviors added via new types/hooks, not modifying existing code
- [ ] **Robustness**: All nil inputs checked at boundary; panics only for programmer errors
- [ ] **Non-false-positive**: Every error returned explicitly; no silent zero-value returns on failure
- [ ] **Idempotency**: Repeated calls with same input produce same result; endpoints accept idempotency keys
- [ ] **Defensive copies**: Aggregate methods return copies of internal slices/maps
- [ ] **Invariant enforcement**: Constructors validate all fields; state machines enforce transitions
- [ ] **Graceful degradation**: Non-critical failures logged+skipped, not propagated to caller
