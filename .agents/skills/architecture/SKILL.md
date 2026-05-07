---
name: golang-architecture
description: >
  Go software architecture patterns: Hexagonal Architecture (Ports & Adapters), Clean Architecture,
  Domain-Driven Design (DDD) with aggregates/value-objects/domain-events, CQRS, Event Sourcing,
  Bounded Context integration via Anti-Corruption Layers, and Vertical Slice Architecture.
  Use for any service structure decision, bounded context design, aggregate design, or architecture
  review. Always combine with solid-principles/SKILL.md, event-driven/SKILL.md, and database/SKILL.md.
---

# Go Architecture — Hexagonal, DDD, CQRS, Event Sourcing

## 1. Hexagonal Architecture — The Invariant Rule

**The domain NEVER imports from adapters, infrastructure, or frameworks.**
Everything flows inward. Dependencies point toward the domain.

```
  ┌──── Driving Adapters ─────┐   ┌──── Driven Adapters ────┐
  │  HTTP Handler             │   │  Postgres Repo           │
  │  gRPC Server              │   │  Redis Cache             │
  │  Kafka Consumer           │   │  SMTP Mailer             │
  │  CLI Command              │   │  S3 Storage              │
  └─────────┬─────────────────┘   └───────────┬─────────────┘
            │ calls                            │ implements
            ▼                                  ▼
  ┌─── Application Layer ────────────────────────────────────┐
  │  Use Cases / Command Handlers / Query Handlers            │
  │  Orchestrates domain, calls ports (interfaces)            │
  └─────────────────────────┬────────────────────────────────┘
                             │ calls
                             ▼
  ┌─── Domain Layer (Pure Go — zero external imports) ───────┐
  │  Aggregates  •  Value Objects  •  Domain Events          │
  │  Domain Services  •  Repository Interfaces (ports)       │
  │  Domain Errors  •  Business Rules                        │
  └──────────────────────────────────────────────────────────┘
```

### Canonical Directory Layout
```
internal/
├── domain/
│   ├── order/
│   │   ├── order.go         # Aggregate root
│   │   ├── line_item.go     # Entity within aggregate
│   │   ├── money.go         # Value object
│   │   ├── events.go        # Domain events
│   │   ├── errors.go        # Typed + sentinel domain errors
│   │   ├── repository.go    # Port interface (defined HERE, implemented in adapters)
│   │   └── service.go       # Domain service (pure logic, stateless)
│   └── customer/
├── app/
│   ├── order/
│   │   ├── create_order.go  # Command handler (use case)
│   │   ├── cancel_order.go  # Command handler
│   │   ├── get_order.go     # Query handler
│   │   └── dto.go           # Command/Query/Response DTOs
│   └── ports/               # App-level ports (notifier, event publisher)
│       ├── notifier.go
│       └── event_publisher.go
├── adapters/
│   ├── http/                # Driving: chi/gin/stdlib handlers
│   ├── grpc/                # Driving: gRPC server implementations
│   ├── postgres/            # Driven: implements domain.Repository
│   ├── redis/               # Driven: implements cache ports
│   ├── kafka/               # Driving (consumer) + Driven (producer)
│   └── smtp/                # Driven: implements app.Notifier
└── infrastructure/
    ├── postgres/             # DB pool, migrations, tx manager
    ├── kafka/                # Producer/consumer clients
    └── server/               # HTTP/gRPC server setup
```

---

## 2. Aggregate Root Pattern

```go
// internal/domain/order/order.go
package order

// Order is the aggregate root.
// ALL mutations go through its methods — no direct field access from outside.
// Invariants are enforced at construction and every state transition.
type Order struct {
    id         ID
    customerID CustomerID
    items      []LineItem
    status     Status
    total      Money
    version    int64        // optimistic locking — incremented on every save
    events     []DomainEvent // unpublished domain events — drained by app layer
    createdAt  time.Time
    updatedAt  time.Time
}

// NewOrder is the ONLY constructor. Enforces all creation invariants.
func NewOrder(customerID CustomerID, items []LineItem) (*Order, error) {
    if err := customerID.Validate(); err != nil {
        return nil, fmt.Errorf("%w: invalid customerID: %v", ErrValidation, err)
    }
    if len(items) == 0 {
        return nil, ErrEmptyOrder
    }
    total, err := calculateTotal(items)
    if err != nil {
        return nil, fmt.Errorf("NewOrder.calculateTotal: %w", err)
    }
    now := time.Now().UTC()
    o := &Order{
        id: NewID(), customerID: customerID,
        items: items, status: StatusPending,
        total: total, version: 1,
        createdAt: now, updatedAt: now,
    }
    o.record(OrderCreated{
        OrderID: o.id, CustomerID: customerID,
        Items: items, Total: total, At: now,
    })
    return o, nil
}

// Confirm transitions Pending → Confirmed.
// Returns ErrInvalidTransition if called in wrong state.
func (o *Order) Confirm() error {
    if o.status != StatusPending {
        return fmt.Errorf("%w: got %s, need %s", ErrInvalidTransition, o.status, StatusPending)
    }
    now := time.Now().UTC()
    o.status = StatusConfirmed
    o.updatedAt = now
    o.version++
    o.record(OrderConfirmed{OrderID: o.id, At: now})
    return nil
}

// PopEvents drains accumulated domain events. Called by app layer after save.
func (o *Order) PopEvents() []DomainEvent {
    evts := make([]DomainEvent, len(o.events))
    copy(evts, o.events)
    o.events = o.events[:0]
    return evts
}

// Getters — no public setters. Immutable from outside.
func (o *Order) ID() ID           { return o.id }
func (o *Order) Status() Status   { return o.status }
func (o *Order) Total() Money     { return o.total }
func (o *Order) Version() int64   { return o.version }
func (o *Order) Items() []LineItem { return append([]LineItem(nil), o.items...) } // defensive copy

func (o *Order) record(e DomainEvent) { o.events = append(o.events, e) }
```

---

## 3. Value Objects

```go
// internal/domain/order/money.go
// Value objects are immutable and compared by value, not identity.
type Money struct {
    amount   int64  // stored in cents — NEVER float64 for money
    currency string // ISO 4217 three-letter code
}

func NewMoney(amount int64, currency string) (Money, error) {
    if amount < 0 { return Money{}, fmt.Errorf("%w: amount=%d", ErrNegativeAmount, amount) }
    currency = strings.ToUpper(currency)
    if len(currency) != 3 { return Money{}, fmt.Errorf("%w: %q", ErrInvalidCurrency, currency) }
    return Money{amount: amount, currency: currency}, nil
}

// Operations return new Money — never mutate
func (m Money) Add(other Money) (Money, error) {
    if m.currency != other.currency {
        return Money{}, fmt.Errorf("%w: %s + %s", ErrCurrencyMismatch, m.currency, other.currency)
    }
    return Money{amount: m.amount + other.amount, currency: m.currency}, nil
}

func (m Money) Multiply(factor int64) Money {
    return Money{amount: m.amount * factor, currency: m.currency}
}

func (m Money) Equal(other Money) bool {
    return m.amount == other.amount && m.currency == other.currency
}

func (m Money) String() string {
    return fmt.Sprintf("%s %.2f", m.currency, float64(m.amount)/100)
}

// Getters only
func (m Money) AmountCents() int64 { return m.amount }
func (m Money) Currency() string   { return m.currency }
```

---

## 4. Repository Interface (Port)

```go
// internal/domain/order/repository.go
// Interface defined in domain. Implemented in adapters/postgres.
// Consumer (domain/app) owns this interface — not the implementor.

type Repository interface {
    // FindByID returns the order or ErrNotFound. NEVER returns (nil, nil).
    FindByID(ctx context.Context, id ID) (*Order, error)

    // Save persists new or updated order.
    // Checks o.Version() against stored version → ErrConflict if mismatch (optimistic lock).
    Save(ctx context.Context, o *Order) error

    // FindByCustomer returns active orders. Empty slice (not nil) if none found.
    FindByCustomer(ctx context.Context, customerID CustomerID) ([]*Order, error)

    // Delete soft-deletes by setting deleted_at. Returns ErrNotFound if missing.
    Delete(ctx context.Context, id ID) error
}

// Typed sentinel errors — use errors.Is() / errors.As() to check
var (
    ErrNotFound          = errors.New("order: not found")
    ErrConflict          = errors.New("order: optimistic lock conflict")
    ErrValidation        = errors.New("order: validation failed")
    ErrEmptyOrder        = errors.New("order: must have at least one item")
    ErrInvalidTransition = errors.New("order: invalid state transition")
    ErrNegativeAmount    = errors.New("order: negative money amount")
    ErrInvalidCurrency   = errors.New("order: invalid currency code")
    ErrCurrencyMismatch  = errors.New("order: currency mismatch")
)
```

---

## 5. CQRS — Command and Query Separation

```go
// internal/app/order/create_order.go
type CreateOrderCommand struct {
    CustomerID string      `validate:"required,uuid"`
    Items      []ItemInput `validate:"required,min=1,dive"`
}

type CreateOrderHandler struct {
    repo      order.Repository
    publisher ports.EventPublisher
    validator *validator.Validate
}

func NewCreateOrderHandler(r order.Repository, p ports.EventPublisher, v *validator.Validate) *CreateOrderHandler {
    return &CreateOrderHandler{repo: r, publisher: p, validator: v}
}

func (h *CreateOrderHandler) Handle(ctx context.Context, cmd CreateOrderCommand) (*OrderResponse, error) {
    // 1. Validate command (input boundary)
    if err := h.validator.StructCtx(ctx, cmd); err != nil {
        return nil, fmt.Errorf("%w: %v", order.ErrValidation, err)
    }

    // 2. Translate to domain types
    items, err := toLineItems(cmd.Items)
    if err != nil { return nil, fmt.Errorf("CreateOrderHandler.toLineItems: %w", err) }

    // 3. Create aggregate (domain logic)
    o, err := order.NewOrder(order.CustomerID(cmd.CustomerID), items)
    if err != nil { return nil, fmt.Errorf("CreateOrderHandler.NewOrder: %w", err) }

    // 4. Persist
    if err := h.repo.Save(ctx, o); err != nil {
        return nil, fmt.Errorf("CreateOrderHandler.Save: %w", err)
    }

    // 5. Publish domain events (after successful persist — outbox handles failure)
    for _, evt := range o.PopEvents() {
        if err := h.publisher.Publish(ctx, evt); err != nil {
            slog.ErrorContext(ctx, "event publish failed",
                "event", evt.EventName(), "order_id", o.ID(), "err", err)
            // Non-fatal: outbox/retry will eventually deliver
        }
    }

    return toOrderResponse(o), nil
}

// internal/app/order/get_order.go — READ SIDE (separate, optimized)
type GetOrderQuery struct { OrderID string }

type GetOrderHandler struct {
    readDB *pgxpool.Pool // can be read replica
}

func (h *GetOrderHandler) Handle(ctx context.Context, q GetOrderQuery) (*OrderView, error) {
    // Direct SQL for reads — bypass domain model entirely for performance
    row := h.readDB.QueryRow(ctx,
        `SELECT id, customer_id, status, total_amount, total_currency, created_at
         FROM order_views WHERE id = $1 AND deleted_at IS NULL`, q.OrderID)
    var v OrderView
    if err := row.Scan(&v.ID, &v.CustomerID, &v.Status,
        &v.TotalAmount, &v.TotalCurrency, &v.CreatedAt); err != nil {
        if errors.Is(err, pgx.ErrNoRows) { return nil, order.ErrNotFound }
        return nil, fmt.Errorf("GetOrderHandler.Scan: %w", err)
    }
    return &v, nil
}
```

---

## 6. Anti-Corruption Layer (Bounded Context Integration)

```go
// internal/adapters/acl/inventory_acl.go
// Translates between THIS bounded context's model and the INVENTORY context's model.
// Prevents inventory's concepts from leaking into our domain.

type InventoryACL struct {
    client inventory.StockServiceClient
}

func (a *InventoryACL) CheckAvailability(ctx context.Context, items []order.LineItem) (bool, error) {
    ctx, span := tracer.Start(ctx, "InventoryACL.CheckAvailability")
    defer span.End()

    req := &inventory.CheckStockRequest{Items: a.toInventoryItems(items)}
    resp, err := a.client.CheckStock(ctx, req)
    if err != nil {
        span.RecordError(err)
        return false, fmt.Errorf("InventoryACL.CheckAvailability: %w", err)
    }
    return resp.AllAvailable, nil
}

// Translates OUR line items to THEIR stock items — insulates domain from external changes
func (a *InventoryACL) toInventoryItems(items []order.LineItem) []*inventory.StockItem {
    result := make([]*inventory.StockItem, len(items))
    for i, item := range items {
        result[i] = &inventory.StockItem{
            Sku:      string(item.ProductID()),  // their concept: SKU ≠ our ProductID
            Quantity: int32(item.Quantity()),
        }
    }
    return result
}
```

---

## Architecture Checklist

- [ ] `domain/` has zero imports from `adapters/`, `infrastructure/`, or external frameworks
- [ ] All repository interfaces live in `domain/`, implemented in `adapters/`
- [ ] Aggregate roots are sole entry point for all state mutations
- [ ] Domain events recorded on every state transition, published after persist
- [ ] Commands (write) and Queries (read) use separate handlers and models
- [ ] Optimistic locking via `version` field on all aggregates
- [ ] Anti-Corruption Layer insulates domain from external bounded contexts
- [ ] All domain errors are typed/sentinel — never raw `errors.New` in adapters
- [ ] DTOs used at application boundary — domain objects never escape to HTTP/gRPC layer
- [ ] Use cases are small (≤60 lines), single-purpose, fully tested
