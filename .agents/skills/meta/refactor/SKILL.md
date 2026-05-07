---
name: meta-refactor
description: >
  Go refactoring workflow: safe refactoring sequences, strangler fig pattern, god object
  decomposition, extract interface, introduce repository, add observability without changing
  behavior, and incremental architecture migration. Load when asked to refactor, improve,
  or restructure existing Go code. Always: tests green before and after every step.
---

# Go Refactoring — Safe Incremental Patterns

## 0. The Golden Rule

```
Tests must be GREEN before you start.
Tests must be GREEN after every step.
If tests break mid-refactor → revert the last step, not the whole thing.
Commit after each safe step (small commits = safe recovery points).
```

---

## 1. Extract Interface (DIP Introduction)

```go
// BEFORE: concrete dependency, impossible to test in isolation
type OrderService struct {
    db *sql.DB  // concrete
}
func (s *OrderService) GetOrder(id string) (*Order, error) {
    return dbQuery(s.db, id)
}

// STEP 1: Identify the methods actually used on the concrete type
// s.db → only uses: QueryRowContext, ExecContext

// STEP 2: Define minimal interface at the consumer site
type orderDB interface {
    QueryRowContext(ctx context.Context, q string, args ...any) *sql.Row
    ExecContext(ctx context.Context, q string, args ...any) (sql.Result, error)
}

// STEP 3: Replace concrete field with interface (no behavior change)
type OrderService struct {
    db orderDB  // now an interface
}

// STEP 4: Verify: existing code still compiles + tests still pass
// STEP 5: Write tests with mock implementation of orderDB
// STEP 6: Move interface to domain layer if it represents a domain concept
```

---

## 2. Decompose God Object

```go
// BEFORE: UserService does everything
type UserService struct {
    db     *sql.DB
    mailer *smtp.Client
    cache  *redis.Client
    s3     *s3.Client
    stripe *stripe.Client
}
// 2000 lines of mixed concerns

// REFACTOR SEQUENCE:
// 1. Identify responsibility clusters by marking each method with its concern:
//    auth, profile, notifications, payments, avatar
// 2. Start with LOWEST coupling cluster (fewest cross-method calls)
// 3. Extract ONE cluster at a time

// Step A: Extract notification concern
type NotificationService struct {
    mailer *smtp.Client  // moved from UserService
}
func (n *NotificationService) SendWelcome(ctx context.Context, email string) error { ... }
func (n *NotificationService) SendPasswordReset(ctx context.Context, email string) error { ... }

// Step B: Update UserService to delegate (not duplicate)
type UserService struct {
    db       *sql.DB
    cache    *redis.Client
    s3       *s3.Client
    stripe   *stripe.Client
    notifier *NotificationService  // delegation, not duplication
}

// Step C: After all tests pass, define interface for notifier
type Notifier interface {
    SendWelcome(ctx context.Context, email string) error
    SendPasswordReset(ctx context.Context, email string) error
}

// Repeat for each cluster: payments → PaymentService, avatar → AvatarService
```

---

## 3. Introduce Repository Pattern

```go
// BEFORE: DB queries scattered through service
type OrderService struct{ db *sql.DB }
func (s *OrderService) GetOrder(ctx context.Context, id string) (*Order, error) {
    row := s.db.QueryRowContext(ctx, `SELECT * FROM orders WHERE id = $1`, id)
    // scan...
}

// STEP 1: Create repository struct alongside service (don't move yet)
type OrderRepository struct{ db *sql.DB }
func (r *OrderRepository) FindByID(ctx context.Context, id string) (*Order, error) {
    row := r.db.QueryRowContext(ctx, `SELECT * FROM orders WHERE id = $1`, id)
    // same scan logic
}

// STEP 2: Make service delegate to repository (tests still pass)
type OrderService struct {
    db   *sql.DB
    repo *OrderRepository  // added
}
func (s *OrderService) GetOrder(ctx context.Context, id string) (*Order, error) {
    return s.repo.FindByID(ctx, id)  // delegate
}

// STEP 3: Remove duplicated DB query from service. Tests still pass.
// STEP 4: Define interface, make service use it
// STEP 5: Move repository to adapters/postgres package
```

---

## 4. Add Observability (Non-Behavioral Refactor)

```go
// Adding tracing/logging/metrics must NOT change behavior.
// Use the decorator pattern — wrap, don't modify.

// BEFORE: service with no observability
type OrderService struct{ repo Repository }
func (s *OrderService) Create(ctx context.Context, cmd Command) (*Order, error) {
    return s.repo.Save(ctx, newOrder(cmd))
}

// REFACTOR: traced wrapper (behavior identical)
type TracedOrderService struct {
    inner  *OrderService  // unchanged
    tracer trace.Tracer
}
func (t *TracedOrderService) Create(ctx context.Context, cmd Command) (*Order, error) {
    ctx, span := t.tracer.Start(ctx, "OrderService.Create")
    defer span.End()

    o, err := t.inner.Create(ctx, cmd)  // delegates to original
    if err != nil { span.RecordError(err); span.SetStatus(codes.Error, err.Error()) }
    return o, err
}

// Wire up: svc := &TracedOrderService{inner: realSvc, tracer: otel.Tracer("...")}
// All existing tests of OrderService still pass — not broken
```

---

## 5. Strangler Fig (Incremental Service Migration)

```go
// Migrating legacy monolith endpoint to new microservice:
// 1. Keep old handler working
// 2. Add feature flag to route to new handler
// 3. Run both in parallel (compare responses)
// 4. Flip flag — new becomes default
// 5. Remove old code

func (h *Handler) GetOrder(w http.ResponseWriter, r *http.Request) {
    id := chi.URLParam(r, "id")

    if h.features.IsEnabled("new_order_service") {
        // New path: calls microservice
        order, err := h.orderClient.GetOrder(r.Context(), id)
        if err != nil {
            // Fallback to old path on error (during migration)
            h.getOrderLegacy(w, r, id)
            return
        }
        h.respondJSON(w, http.StatusOK, order)
        return
    }
    h.getOrderLegacy(w, r, id)  // old path
}

// When confident: remove feature flag + legacy path
```

---

## 6. Safe Refactoring Sequence Checklist

```
Before starting:
  [ ] git status: clean working tree
  [ ] go test -race ./... PASSES
  [ ] Identify the SINGLE change to make (do not combine)

Each step:
  [ ] Make one mechanical change (rename, extract, move)
  [ ] go build ./... — must compile
  [ ] go test -race ./... — must pass
  [ ] git commit -m "refactor: [what changed]"
  [ ] Repeat

After completing:
  [ ] go vet ./... — zero issues
  [ ] golangci-lint run — zero issues
  [ ] No TODO or FIXME introduced
  [ ] Package SKILL.md updated to reflect new structure
```

---

## Common Safe Refactoring Catalog

| Refactoring | Risk | Steps |
|---|---|---|
| Rename variable/function | Low | IDE rename → build → test |
| Extract function | Low | Copy body → replace with call → test |
| Extract interface | Medium | Define interface → change field → test |
| Move function to new package | Medium | Copy → update imports → delete original → test |
| Introduce parameter object | Medium | Add struct → change callers → test |
| Extract service/repository | High | Add alongside → delegate → test → move → test |
| Migrate to new pattern | High | Strangler fig → feature flag → gradual rollout |
