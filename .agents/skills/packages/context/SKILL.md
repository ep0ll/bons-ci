---
name: pkg-context
description: >
  Exhaustive reference for Go's context package: propagation rules, value keys, cancellation
  chains, timeout vs deadline, WithValue patterns, context-aware APIs, goroutine leak prevention,
  and context in tests. Load whenever writing code that passes, creates, or reads context.Context.
  Cross-references: golang-core/SKILL.md, concurrency/SKILL.md.
---

# Package: context — Complete Reference

## Import
```go
import "context"
```

## Core Types
```go
type Context interface {
    Deadline() (deadline time.Time, ok bool)
    Done() <-chan struct{}      // closed when context cancelled/expired; nil if never cancelled
    Err() error                // nil, context.Canceled, or context.DeadlineExceeded
    Value(key any) any
}
```

## 1. Creating Contexts

```go
// Root contexts — only at program entry points
ctx := context.Background()  // never cancelled, no values, no deadline — use in main/init/tests
ctx := context.TODO()        // placeholder: "I don't know which context to use yet"
// TODO is a code smell — replace before production

// Cancellation
ctx, cancel := context.WithCancel(parent)
defer cancel() // ALWAYS defer cancel — prevents goroutine leak

// Timeout — relative deadline
ctx, cancel := context.WithTimeout(parent, 30*time.Second)
defer cancel()

// Deadline — absolute time
deadline := time.Now().Add(30 * time.Second)
ctx, cancel := context.WithDeadline(parent, deadline)
defer cancel()

// Value — typed key pattern (MANDATORY — never use string keys)
type contextKey struct{ name string }  // unexported, prevents collision
var userKey = contextKey{"user"}

ctx = context.WithValue(parent, userKey, user)
user, ok := ctx.Value(userKey).(*User)
```

## 2. Cancellation Propagation

```go
// Child contexts are cancelled when parent is cancelled — chain propagates
parent, pCancel := context.WithCancel(context.Background())
child, cCancel := context.WithTimeout(parent, 10*time.Second)
defer cCancel()

// If pCancel() called: child is also cancelled (even before 10s)
// If 10s elapses: only child is cancelled, parent continues
// Always cancel the INNERMOST context with defer — outer cancels propagate down

// Check cancellation in blocking operations
func doWork(ctx context.Context, items []Item) error {
    for _, item := range items {
        select {
        case <-ctx.Done():
            return fmt.Errorf("doWork cancelled: %w", ctx.Err())
        default:
        }
        if err := processItem(ctx, item); err != nil {
            return fmt.Errorf("doWork.processItem: %w", err)
        }
    }
    return nil
}
```

## 3. Passing Context Correctly

```go
// RULE: ctx is ALWAYS the first parameter
func (s *Service) Method(ctx context.Context, req Request) (Response, error)

// RULE: never store context in a struct — always pass as parameter
// ✗ BAD
type Client struct { ctx context.Context }
// ✓ GOOD
func (c *Client) Call(ctx context.Context, req Request) error

// RULE: never pass nil context — use context.Background() if no context available
// ✗ BAD
repo.FindByID(nil, id)
// ✓ GOOD
repo.FindByID(context.Background(), id)

// RULE: do not modify context after passing to another goroutine
go func(ctx context.Context) {  // pass, not capture
    doWork(ctx)
}(ctx)
```

## 4. Context Values — Safe Patterns

```go
// Values for cross-cutting concerns ONLY: request IDs, auth tokens, logger
// NOT for passing function parameters — that's what function args are for

// Request ID middleware pattern
type requestIDKey struct{}

func WithRequestID(ctx context.Context, id string) context.Context {
    return context.WithValue(ctx, requestIDKey{}, id)
}

func RequestIDFromContext(ctx context.Context) string {
    id, _ := ctx.Value(requestIDKey{}).(string)
    return id  // empty string is safe zero value
}

// Logger in context
type loggerKey struct{}

func WithLogger(ctx context.Context, l *slog.Logger) context.Context {
    return context.WithValue(ctx, loggerKey{}, l)
}

func LoggerFromContext(ctx context.Context) *slog.Logger {
    if l, ok := ctx.Value(loggerKey{}).(*slog.Logger); ok { return l }
    return slog.Default()  // always return usable logger
}
```

## 5. Preventing Goroutine Leaks

```go
// Every goroutine launched with a context MUST exit when context is done
func startWorker(ctx context.Context, jobs <-chan Job) {
    go func() {
        for {
            select {
            case job, ok := <-jobs:
                if !ok { return }  // channel closed
                processJob(ctx, job)
            case <-ctx.Done():
                return  // context cancelled — goroutine exits
            }
        }
    }()
}

// HTTP handler: request context is cancelled when connection closes
// Always use r.Context() — do NOT create a new Background context in handlers
func (h *Handler) Serve(w http.ResponseWriter, r *http.Request) {
    result, err := h.svc.Fetch(r.Context(), id)  // ✓
    // NOT: h.svc.Fetch(context.Background(), id) — ignores client disconnection
}
```

## 6. Testing with Context

```go
// Test contexts
func TestSomething(t *testing.T) {
    ctx := context.Background()  // no timeout in unit tests (use t.Deadline())

    // Use t.Deadline() for test timeouts
    deadline, ok := t.Deadline()
    if ok {
        var cancel context.CancelFunc
        ctx, cancel = context.WithDeadline(ctx, deadline)
        defer cancel()
    }

    // Cancelable test context
    ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
    defer cancel()
    // ...
}

// context.WithoutCancel (Go 1.21+): detach from parent cancellation
// Use when you need to continue work after request context is done
// e.g., async audit log after HTTP response is sent
ctx = context.WithoutCancel(r.Context())
go auditLog(ctx, event)  // won't be cancelled when request ends
```

## 7. Common Mistakes

```go
// ✗ Capturing context in closure that outlives the context
ctx, cancel := context.WithTimeout(ctx, 5*time.Second)
defer cancel()
go func() {
    time.Sleep(10 * time.Second)
    doSomething(ctx)  // ctx already cancelled — race condition
}()

// ✓ Pass new background context for work that must outlive request
go func() {
    doSomething(context.Background())
}()

// ✗ Using context.TODO() in production paths
func (s *Service) Run() { s.repo.FindAll(context.TODO(), ...) }  // never ship TODO

// ✗ Using string key type
ctx = context.WithValue(ctx, "userID", id)  // collides with other packages
// ✓ Use unexported struct key
ctx = context.WithValue(ctx, userKey{}, id)

// ✗ Not checking context in long loops
for i := 0; i < 1_000_000; i++ { expensiveOp() }  // ignores cancellation
// ✓ Check periodically
for i := 0; i < 1_000_000; i++ {
    if i%100 == 0 {
        select { case <-ctx.Done(): return ctx.Err(); default: }
    }
    expensiveOp()
}
```

## Context Checklist
- [ ] `ctx` always first parameter in all functions that do I/O
- [ ] `defer cancel()` immediately after every `WithCancel/WithTimeout/WithDeadline`
- [ ] All goroutines exit on `ctx.Done()`
- [ ] Value keys are unexported struct types — never strings or ints
- [ ] `r.Context()` used in HTTP handlers — not `context.Background()`
- [ ] `context.TODO()` only in stubs — never in production paths
