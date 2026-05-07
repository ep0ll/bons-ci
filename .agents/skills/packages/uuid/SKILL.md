---
name: pkg-uuid-decimal-sqlc
description: >
  Exhaustive reference for three essential utility packages:
  1. github.com/google/uuid — UUID generation and parsing
  2. github.com/shopspring/decimal — Precise decimal arithmetic for money
  3. github.com/sqlc-dev/sqlc — Type-safe SQL code generation
  Load this when using any of these three packages. Cross-references:
  architecture/SKILL.md (value objects), database/SKILL.md, packages/pgx/SKILL.md.
---

# Packages: uuid + decimal + sqlc

---

## Part 1: google/uuid

### Import
```go
import "github.com/google/uuid"
```

### Core Patterns
```go
// Generate
id := uuid.New()                        // UUIDv4 (random)
id, err := uuid.NewRandom()            // same, with explicit error
id, err = uuid.NewV7()                 // UUIDv7 (time-ordered, better for DB indexes)

// Parse
id, err = uuid.Parse("550e8400-e29b-41d4-a716-446655440000")
if errors.Is(err, uuid.ErrInvalidUUID) { /* invalid format */ }
id, err = uuid.ParseBytes(rawBytes)

// String representation
str := id.String()      // "550e8400-e29b-41d4-a716-446655440000"

// Typed ID pattern (prevents mixing IDs of different entities)
type OrderID uuid.UUID
type UserID  uuid.UUID

func NewOrderID() OrderID { return OrderID(uuid.New()) }
func (id OrderID) String() string { return uuid.UUID(id).String() }
func (id OrderID) IsZero() bool   { return id == OrderID(uuid.Nil) }

// Parse typed ID
func ParseOrderID(s string) (OrderID, error) {
    id, err := uuid.Parse(s)
    if err != nil { return OrderID{}, fmt.Errorf("invalid order ID %q: %w", s, err) }
    return OrderID(id), nil
}

// Database scanning
var id uuid.UUID
row.Scan(&id)  // implements sql.Scanner

// JSON: marshals as string "550e8400-..."
type Order struct {
    ID uuid.UUID `json:"id" db:"id"`
}
```

---

## Part 2: shopspring/decimal

### Import
```go
import "github.com/shopspring/decimal"
```

### Why Never Use float for Money
```go
// float64 precision problem:
0.1 + 0.2 == 0.3  // FALSE in float64: 0.30000000000000004
// Always use decimal or integer cents for money
```

### Core Patterns
```go
// Construction
price := decimal.NewFromFloat(19.99)           // from float (safe at construction)
price  = decimal.New(1999, -2)                  // 19.99 (mantissa + exponent)
price  = decimal.RequireFromString("19.99")     // panics on invalid — use in tests only
price, err = decimal.NewFromString("19.99")     // safe parsing

// Arithmetic (returns new Decimal — immutable)
total := price.Mul(decimal.NewFromInt(3))     // 59.97
tax   := total.Mul(decimal.NewFromFloat(0.1)) // 5.997
final := total.Add(tax)                        // 65.967

// Rounding (always explicit)
rounded := final.Round(2)                      // 65.97 (banker's rounding)
rounded  = final.RoundUp(2)                    // 65.97 (ceiling)
rounded  = final.RoundDown(2)                  // 65.96 (floor)
rounded  = final.RoundCeil(2)                  // 65.97
rounded  = final.RoundFloor(2)                 // 65.96

// Comparison
a.Equal(b)           // exact equality
a.GreaterThan(b)     // a > b
a.LessThan(b)        // a < b
a.IsZero()           // == 0
a.IsNegative()       // < 0
a.IsPositive()       // > 0
a.Cmp(b)             // -1, 0, 1

// Conversion
f, exact := price.Float64()  // (19.99, true) — exact=false means precision lost
i := price.IntPart()         // 19
s := price.String()          // "19.99"
s  = price.StringFixed(4)    // "19.9900"

// Database (implements sql.Scanner and driver.Valuer)
type Product struct {
    Price decimal.Decimal `db:"price"`
}

// JSON (marshals as string "19.99" — preserves precision)
type Invoice struct {
    Amount decimal.Decimal `json:"amount"`
}

// Value object pattern
type Money struct {
    Amount   decimal.Decimal
    Currency string
}

func (m Money) Add(other Money) (Money, error) {
    if m.Currency != other.Currency {
        return Money{}, fmt.Errorf("currency mismatch: %s != %s", m.Currency, other.Currency)
    }
    return Money{Amount: m.Amount.Add(other.Amount), Currency: m.Currency}, nil
}

func (m Money) IsPositive() bool { return m.Amount.IsPositive() }
```

---

## Part 3: sqlc

### What sqlc Does
```
Write SQL queries → sqlc generates type-safe Go functions + structs
No runtime reflection, no ORM overhead, full SQL control.
```

### Setup
```yaml
# sqlc.yaml
version: "2"
sql:
  - engine: "postgresql"
    queries:  "internal/adapters/postgres/queries/"
    schema:   "migrations/"
    gen:
      go:
        package:                    "pgstore"
        out:                        "internal/adapters/postgres/pgstore"
        sql_package:                "pgx/v5"      # use pgx, not database/sql
        emit_json_tags:             true
        emit_db_tags:               true
        emit_interface:             true           # generates Querier interface
        emit_empty_slices:          true           # [] not nil for empty results
        emit_pointers_for_null_types: true         # *string for nullable TEXT
        emit_params_struct_pointers: false
        overrides:
          - db_type: "uuid"
            go_type: "github.com/google/uuid.UUID"
          - db_type: "numeric"
            go_type: "github.com/shopspring/decimal.Decimal"
          - db_type: "timestamptz"
            go_type: "time.Time"
```

### SQL Query File
```sql
-- internal/adapters/postgres/queries/orders.sql

-- name: GetOrder :one
SELECT id, customer_id, status, total_amount, created_at
FROM orders
WHERE id = $1 AND deleted_at IS NULL;

-- name: ListOrdersByCustomer :many
SELECT id, customer_id, status, total_amount, created_at
FROM orders
WHERE customer_id = $1
  AND deleted_at IS NULL
  AND ($2::text IS NULL OR status = $2)
ORDER BY created_at DESC
LIMIT $3 OFFSET $4;

-- name: CreateOrder :one
INSERT INTO orders (id, customer_id, status, total_amount, version)
VALUES ($1, $2, $3, $4, 1)
RETURNING *;

-- name: UpdateOrderStatus :execrows
UPDATE orders
SET status = $2, version = version + 1, updated_at = NOW()
WHERE id = $1
  AND version = $3
  AND deleted_at IS NULL;

-- name: SoftDeleteOrder :exec
UPDATE orders
SET deleted_at = NOW(), updated_at = NOW()
WHERE id = $1 AND deleted_at IS NULL;
```

### Generated Usage
```go
// Generated in pgstore package — use Querier interface for testability
q := pgstore.New(pool)  // or pgstore.New(tx) for transactions

// Get one
order, err := q.GetOrder(ctx, orderID)
if errors.Is(err, pgx.ErrNoRows) { return nil, domain.ErrNotFound }

// List
orders, err := q.ListOrdersByCustomer(ctx, pgstore.ListOrdersByCustomerParams{
    CustomerID: customerID,
    Column2:    pgtype.Text{String: "pending", Valid: true}, // nullable filter
    Limit:      20,
    Offset:     0,
})

// Create
order, err = q.CreateOrder(ctx, pgstore.CreateOrderParams{
    ID:          uuid.New(),
    CustomerID:  customerID,
    Status:      "pending",
    TotalAmount: decimal.NewFromFloat(99.99),
})

// Update (returns rows affected)
affected, err := q.UpdateOrderStatus(ctx, pgstore.UpdateOrderStatusParams{
    ID: orderID, Status: "confirmed", Version: currentVersion,
})
if affected == 0 { return domain.ErrConflict } // optimistic lock miss
```

### Generate Commands
```bash
go generate ./internal/adapters/postgres/...
# or
sqlc generate

# Verify queries at CI time (no output)
sqlc vet
```

---

## Checklist
**uuid:** UUIDv7 for DB-indexed IDs; typed ID wrappers to prevent mixing; ParseXxx returns error
**decimal:** Never float for money; always Round(2) before persisting; String() for JSON
**sqlc:** `emit_empty_slices: true`; `pgx/v5` sql_package; `Querier` interface for test mocks; `execrows` for UPDATE with optimistic lock check
