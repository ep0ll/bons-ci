---
name: pkg-ent
description: >
  Exhaustive reference for entgo.io/ent: schema definition, code generation, query API,
  eager loading edges, transactions, hooks, privacy policies, and migration integration.
  Use when type-safe graph-based ORM is preferred over raw SQL. Cross-references: database/SKILL.md.
---

# Package: entgo.io/ent — Complete Reference

## Import
```go
import (
    "github.com/org/project/ent"
    "github.com/org/project/ent/user"
    "github.com/org/project/ent/order"
    "entgo.io/ent/dialect/sql"
)
```

## 1. Schema Definition

```go
// ent/schema/user.go
package schema

import (
    "entgo.io/ent"
    "entgo.io/ent/schema/field"
    "entgo.io/ent/schema/edge"
    "entgo.io/ent/schema/index"
    "entgo.io/ent/schema/mixin"
)

// TimeMixin: shared created_at, updated_at fields
type TimeMixin struct{ mixin.Schema }

func (TimeMixin) Fields() []ent.Field {
    return []ent.Field{
        field.Time("created_at").Immutable().Default(time.Now),
        field.Time("updated_at").Default(time.Now).UpdateDefault(time.Now),
    }
}

type User struct{ ent.Schema }

func (User) Mixin() []ent.Mixin {
    return []ent.Mixin{TimeMixin{}}
}

func (User) Fields() []ent.Field {
    return []ent.Field{
        field.UUID("id", uuid.UUID{}).Default(uuid.New).Immutable(),
        field.String("name").NotEmpty().MaxLen(255),
        field.String("email").NotEmpty().MaxLen(254).
            Validate(func(s string) error {
                if !strings.Contains(s, "@") { return fmt.Errorf("invalid email") }
                return nil
            }),
        field.Enum("status").Values("active", "inactive", "suspended").Default("active"),
        field.Time("deleted_at").Optional().Nillable(), // soft delete
    }
}

func (User) Edges() []ent.Edge {
    return []ent.Edge{
        edge.To("orders", Order.Type), // has-many
    }
}

func (User) Indexes() []ent.Index {
    return []ent.Index{
        index.Fields("email").Unique().
            Annotations(entsql.IndexWhere("deleted_at IS NULL")), // partial unique index
        index.Fields("status", "created_at"),
    }
}
```

## 2. Code Generation

```bash
# Generate ent code from schema
go generate ./ent

# Or directly
go run entgo.io/ent/cmd/ent generate ./ent/schema

# ent.go in schema dir:
//go:generate go run entgo.io/ent/cmd/ent generate ./schema
```

## 3. Client Setup

```go
func NewEntClient(dsn string) (*ent.Client, error) {
    // Use pgx driver via stdlib adapter
    db, err := sql.Open("pgx", dsn)
    if err != nil { return nil, fmt.Errorf("ent.Open: %w", err) }

    db.SetMaxOpenConns(25)
    db.SetMaxIdleConns(5)
    db.SetConnMaxLifetime(1 * time.Hour)

    client := ent.NewClient(ent.Driver(entsql.OpenDB(dialect.Postgres, db)))

    // Run auto-migration (dev only — use golang-migrate in production)
    if err := client.Schema.Create(context.Background(),
        migrate.WithDropIndex(true),
        migrate.WithDropColumn(true),
    ); err != nil {
        return nil, fmt.Errorf("ent.migrate: %w", err)
    }

    return client, nil
}
```

## 4. Query Patterns

```go
// Get one (returns ErrNotFound if absent)
u, err := client.User.Get(ctx, userID)
if ent.IsNotFound(err) { return nil, domain.ErrNotFound }
if err != nil { return nil, fmt.Errorf("UserRepo.FindByID: %w", err) }

// Query with filters
users, err := client.User.
    Query().
    Where(
        user.StatusEQ("active"),
        user.DeletedAtIsNil(),          // soft delete filter
        user.NameContainsFold("alice"), // case-insensitive contains
    ).
    Order(ent.Desc(user.FieldCreatedAt)).
    Limit(20).
    Offset(0).
    All(ctx)
if err != nil { return nil, fmt.Errorf("UserRepo.FindAll: %w", err) }

// Eager load edges (prevent N+1)
users, err = client.User.
    Query().
    WithOrders(func(oq *ent.OrderQuery) {
        oq.Where(order.StatusEQ("pending")).Limit(5)
    }).
    All(ctx)
// Access: user.Edges.Orders

// Existence check
exists, err := client.User.
    Query().
    Where(user.Email(email), user.DeletedAtIsNil()).
    Exist(ctx)

// Count
n, err := client.User.Query().Where(user.StatusEQ("active")).Count(ctx)
```

## 5. Mutations

```go
// Create
u, err := client.User.
    Create().
    SetName("Alice").
    SetEmail("alice@example.com").
    SetStatus("active").
    Save(ctx)

// Update
u, err = client.User.
    UpdateOneID(userID).
    SetName("Alice Smith").
    Save(ctx)
if ent.IsNotFound(err) { return nil, domain.ErrNotFound }

// Soft delete
_, err = client.User.
    UpdateOneID(userID).
    SetDeletedAt(time.Now()).
    Save(ctx)

// Bulk create
bulk := make([]*ent.UserCreate, len(users))
for i, u := range users {
    bulk[i] = client.User.Create().SetName(u.Name).SetEmail(u.Email)
}
created, err := client.User.CreateBulk(bulk...).Save(ctx)
```

## 6. Transactions

```go
func (r *EntOrderRepo) CreateWithItems(ctx context.Context, cmd CreateOrderCommand) (*ent.Order, error) {
    tx, err := r.client.Tx(ctx)
    if err != nil { return nil, fmt.Errorf("EntOrderRepo.Tx: %w", err) }
    defer func() {
        if p := recover(); p != nil { _ = tx.Rollback(); panic(p) }
    }()

    o, err := tx.Order.Create().
        SetCustomerID(cmd.CustomerID).
        SetStatus("pending").
        Save(ctx)
    if err != nil { _ = tx.Rollback(); return nil, fmt.Errorf("create order: %w", err) }

    for _, item := range cmd.Items {
        if _, err := tx.LineItem.Create().
            SetOrderID(o.ID).
            SetProductID(item.ProductID).
            SetQuantity(item.Quantity).
            Save(ctx); err != nil {
            _ = tx.Rollback()
            return nil, fmt.Errorf("create item: %w", err)
        }
    }

    if err := tx.Commit(); err != nil {
        return nil, fmt.Errorf("commit: %w", err)
    }
    return o, nil
}
```

## 7. Hooks (Cross-Cutting Concerns)

```go
// Audit hook: record every mutation
client.Use(func(next ent.Mutator) ent.Mutator {
    return ent.MutateFunc(func(ctx context.Context, m ent.Mutation) (ent.Value, error) {
        start := time.Now()
        v, err := next.Mutate(ctx, m)
        slog.InfoContext(ctx, "ent mutation",
            slog.String("schema", m.Type()),
            slog.String("op", m.Op().String()),
            slog.Duration("elapsed", time.Since(start)),
        )
        return v, err
    })
})
```

## ent Checklist
- [ ] UUID primary keys with `Default(uuid.New).Immutable()`
- [ ] `TimeMixin` on all schemas for `created_at` / `updated_at`
- [ ] Soft delete: `deleted_at` nullable + `DeletedAtIsNil()` in all queries
- [ ] Edges defined for all relationships — use `WithX()` to prevent N+1
- [ ] Partial unique indexes for soft-delete-aware uniqueness
- [ ] `ent.IsNotFound(err)` for missing record → `domain.ErrNotFound`
- [ ] Transactions: `defer tx.Rollback()` immediately after `client.Tx()`
- [ ] Production migrations via golang-migrate, not `client.Schema.Create`
- [ ] Generated files committed to repo (`ent/` directory)
- [ ] Hooks for audit logging, not manual repetition in each mutation
