---
name: pkg-gorm
description: >
  Exhaustive reference for gorm.io/gorm: model definition, auto-migrate vs golang-migrate,
  associations, hooks, scopes, transactions, raw SQL, and production hardening. Use when gorm
  is the project's ORM. Prefer pgx for performance-critical services. Cross-references:
  database/SKILL.md, packages/migrate/SKILL.md.
---

# Package: gorm.io/gorm — Complete Reference

## Import
```go
import (
    "gorm.io/gorm"
    "gorm.io/driver/postgres"
    "gorm.io/gorm/logger"
)
```

## 1. DB Setup

```go
func NewGORMDB(dsn string, log *slog.Logger) (*gorm.DB, error) {
    gormLogger := logger.New(
        slog.NewLogLogger(log.Handler(), slog.LevelInfo),
        logger.Config{
            SlowThreshold:             200 * time.Millisecond,
            LogLevel:                  logger.Warn,  // only log slow queries + errors
            IgnoreRecordNotFoundError: true,          // don't log ErrRecordNotFound
            ParameterizedQueries:      true,          // don't log parameter values (security)
        },
    )

    db, err := gorm.Open(postgres.New(postgres.Config{DSN: dsn}), &gorm.Config{
        Logger:                                    gormLogger,
        PrepareStmt:                               true,   // cache prepared statements
        DisableForeignKeyConstraintWhenMigrating:  false,
        SkipDefaultTransaction:                    true,   // perf: skip auto-wrapping in tx
        QueryFields:                               true,   // SELECT explicit columns, not *
    })
    if err != nil { return nil, fmt.Errorf("gorm.Open: %w", err) }

    // Pool configuration (via underlying *sql.DB)
    sqlDB, _ := db.DB()
    sqlDB.SetMaxOpenConns(25)
    sqlDB.SetMaxIdleConns(5)
    sqlDB.SetConnMaxLifetime(1 * time.Hour)
    sqlDB.SetConnMaxIdleTime(30 * time.Minute)

    return db, nil
}
```

## 2. Model Definition

```go
// Always embed gorm.Model OR define fields explicitly
// gorm.Model adds: ID(uint), CreatedAt, UpdatedAt, DeletedAt (soft delete)

// Preferred: explicit UUID primary key
type Order struct {
    ID         uuid.UUID      `gorm:"type:uuid;primaryKey;default:gen_random_uuid()"`
    CustomerID uuid.UUID      `gorm:"type:uuid;not null;index"`
    Status     string         `gorm:"type:text;not null;default:'pending'"`
    Total      decimal.Decimal `gorm:"type:numeric(12,2);not null"`
    Version    int64          `gorm:"not null;default:1"`
    CreatedAt  time.Time      `gorm:"autoCreateTime"`
    UpdatedAt  time.Time      `gorm:"autoUpdateTime"`
    DeletedAt  gorm.DeletedAt `gorm:"index"`  // soft delete support

    // Associations
    Items      []OrderItem    `gorm:"foreignKey:OrderID"`
}

type OrderItem struct {
    ID        uuid.UUID `gorm:"type:uuid;primaryKey;default:gen_random_uuid()"`
    OrderID   uuid.UUID `gorm:"type:uuid;not null;index"`
    ProductID uuid.UUID `gorm:"type:uuid;not null"`
    Quantity  int       `gorm:"not null"`
    Price     decimal.Decimal `gorm:"type:numeric(12,2);not null"`
}
```

## 3. Query Patterns

```go
// Find one (returns error if not found)
var order Order
result := db.WithContext(ctx).First(&order, "id = ?", id)
if errors.Is(result.Error, gorm.ErrRecordNotFound) {
    return nil, domain.ErrNotFound
}
if result.Error != nil { return nil, fmt.Errorf("FindByID: %w", result.Error) }

// Find with preload (eager loading — prevent N+1)
db.WithContext(ctx).
    Preload("Items").
    Preload("Items.Product").
    Where("customer_id = ? AND status = ?", customerID, "pending").
    Order("created_at DESC").
    Limit(20).
    Find(&orders)

// Scopes: reusable query conditions
func ActiveScope(db *gorm.DB) *gorm.DB {
    return db.Where("deleted_at IS NULL")
}
func PaginateScope(limit, offset int) func(*gorm.DB) *gorm.DB {
    return func(db *gorm.DB) *gorm.DB {
        return db.Limit(limit).Offset(offset)
    }
}

db.Scopes(ActiveScope, PaginateScope(20, 0)).Find(&orders)

// Select specific columns (avoid SELECT *)
db.Select("id", "customer_id", "status", "total").Find(&orders)

// Count
var count int64
db.Model(&Order{}).Where("status = ?", "pending").Count(&count)

// Exists check
var exists bool
db.Model(&Order{}).
    Where("id = ? AND deleted_at IS NULL", id).
    Select("count(*) > 0").
    Find(&exists)
```

## 4. Create & Update

```go
// Create
order := &Order{CustomerID: customerID, Status: "pending", Total: total}
if err := db.WithContext(ctx).Create(order).Error; err != nil {
    if isUniqueViolation(err) { return nil, domain.ErrConflict }
    return nil, fmt.Errorf("Create: %w", err)
}

// Update specific fields (avoid accidental zero-value overwrites)
result := db.WithContext(ctx).
    Model(&Order{}).
    Where("id = ? AND version = ?", order.ID, order.Version).
    Updates(map[string]any{
        "status":  "confirmed",
        "version": gorm.Expr("version + 1"),
    })
if result.Error != nil { return fmt.Errorf("Update: %w", result.Error) }
if result.RowsAffected == 0 { return domain.ErrConflict } // optimistic lock

// Save: AVOID — saves ALL fields including zero values (accidental data loss)
// Use Updates(map) or Updates(struct) with Select instead
db.Model(&order).Select("status", "updated_at").Updates(&order)
```

## 5. Transactions

```go
func (r *OrderRepo) CreateWithItems(ctx context.Context, order *Order, items []OrderItem) error {
    return r.db.WithContext(ctx).Transaction(func(tx *gorm.DB) error {
        if err := tx.Create(order).Error; err != nil {
            return fmt.Errorf("create order: %w", err)
        }
        for i := range items {
            items[i].OrderID = order.ID
        }
        if err := tx.CreateInBatches(items, 100).Error; err != nil {
            return fmt.Errorf("create items: %w", err)
        }
        return nil
    })
}
```

## 6. Hooks

```go
// Hooks: BeforeCreate, AfterCreate, BeforeUpdate, AfterUpdate, BeforeDelete, AfterDelete

func (o *Order) BeforeCreate(tx *gorm.DB) error {
    if o.ID == uuid.Nil { o.ID = uuid.New() }
    o.Version = 1
    return nil
}

func (o *Order) AfterCreate(tx *gorm.DB) error {
    // Example: publish domain event in same transaction context
    return tx.Statement.Context.Value(eventPublisherKey{}).(EventPublisher).
        Publish(tx.Statement.Context, OrderCreatedEvent{OrderID: o.ID})
}
```

## 7. Raw SQL (when GORM ORM is insufficient)

```go
// Raw SQL for complex queries
db.WithContext(ctx).Raw(`
    SELECT o.*, COUNT(oi.id) as item_count
    FROM orders o
    LEFT JOIN order_items oi ON oi.order_id = o.id
    WHERE o.customer_id = ? AND o.deleted_at IS NULL
    GROUP BY o.id
    ORDER BY o.created_at DESC
    LIMIT ?`, customerID, limit).Scan(&results)

// Exec for non-select
db.WithContext(ctx).Exec(
    "UPDATE orders SET status = ? WHERE id = ? AND version = ?",
    "confirmed", id, version)
```

## gorm Checklist
- [ ] `WithContext(ctx)` on every DB call — never `db.First(...)` without context
- [ ] `SkipDefaultTransaction: true` — avoid implicit transaction per operation
- [ ] `ParameterizedQueries: true` in logger — never log param values
- [ ] `Preload` for associations — never access `order.Items` without preloading (N+1)
- [ ] `Updates(map)` not `Save` — avoid accidental zero-value overwrites
- [ ] `RowsAffected == 0` checked on UPDATE with version check (optimistic lock)
- [ ] Auto-migrate NEVER in production — use golang-migrate
- [ ] `QueryFields: true` — explicit column selection, not SELECT *
- [ ] UUID primary keys via `gen_random_uuid()` default
- [ ] Soft delete via `gorm.DeletedAt` — all queries filter `deleted_at IS NULL` via gorm
