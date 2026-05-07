---
name: golang-database
description: >
  Go database patterns: transaction management, repository pattern implementation, query building,
  N+1 prevention, connection pool tuning, read replicas, optimistic locking, soft deletes,
  audit trails, database migrations strategy, and multi-tenancy. Applies to PostgreSQL (primary),
  MySQL, and SQLite. Always combine with packages/pgx/SKILL.md or packages/sqlx/SKILL.md.
---

# Go Database — Production Patterns

## 1. Transaction Manager

```go
// UnitOfWork / TransactionManager — abstracts tx lifecycle
type TxManager interface {
    WithTx(ctx context.Context, fn func(ctx context.Context) error) error
    WithTxOptions(ctx context.Context, opts pgx.TxOptions, fn func(ctx context.Context) error) error
}

// Context key for active transaction
type txKey struct{}

func TxFromContext(ctx context.Context) pgx.Tx {
    tx, _ := ctx.Value(txKey{}).(pgx.Tx)
    return tx
}

// pgxTxManager uses pgxpool
type pgxTxManager struct{ pool *pgxpool.Pool }

func (m *pgxTxManager) WithTx(ctx context.Context, fn func(context.Context) error) error {
    return m.WithTxOptions(ctx, pgx.TxOptions{}, fn)
}

func (m *pgxTxManager) WithTxOptions(ctx context.Context, opts pgx.TxOptions, fn func(context.Context) error) error {
    tx, err := m.pool.BeginTx(ctx, opts)
    if err != nil { return fmt.Errorf("TxManager.Begin: %w", err) }

    defer func() {
        if p := recover(); p != nil {
            _ = tx.Rollback(ctx)
            panic(p) // re-panic after rollback
        }
    }()

    txCtx := context.WithValue(ctx, txKey{}, tx)
    if err := fn(txCtx); err != nil {
        if rbErr := tx.Rollback(ctx); rbErr != nil {
            return fmt.Errorf("rollback failed (%v) after: %w", rbErr, err)
        }
        return err
    }
    if err := tx.Commit(ctx); err != nil {
        return fmt.Errorf("TxManager.Commit: %w", err)
    }
    return nil
}

// Repository uses tx from context when available
type pgxOrderRepo struct{ pool *pgxpool.Pool }

func (r *pgxOrderRepo) db(ctx context.Context) pgxDB {
    if tx := TxFromContext(ctx); tx != nil { return tx }
    return r.pool
}

func (r *pgxOrderRepo) Save(ctx context.Context, o *order.Order) error {
    _, err := r.db(ctx).Exec(ctx, `
        INSERT INTO orders (id, customer_id, status, total_amount, total_currency, version)
        VALUES ($1, $2, $3, $4, $5, $6)
        ON CONFLICT (id) DO UPDATE SET
            status = EXCLUDED.status,
            total_amount = EXCLUDED.total_amount,
            version = EXCLUDED.version,
            updated_at = NOW()
        WHERE orders.version = $7`,  // optimistic lock
        o.ID(), o.CustomerID(), o.Status(), o.Total().AmountCents(),
        o.Total().Currency(), o.Version(), o.Version()-1,
    )
    if err != nil {
        if isOptimisticLockError(err) { return domain.ErrConflict }
        return fmt.Errorf("pgxOrderRepo.Save: %w", err)
    }
    return nil
}
```

---

## 2. Repository Pattern

```go
// Generic base repository — reduces boilerplate
type BaseRepository[T any, ID comparable] struct {
    pool  *pgxpool.Pool
    table string
    scan  func(pgx.Rows) (T, error)
}

func (r *BaseRepository[T, ID]) FindByID(ctx context.Context, id ID) (*T, error) {
    rows, err := r.db(ctx).Query(ctx,
        fmt.Sprintf(`SELECT * FROM %s WHERE id = $1 AND deleted_at IS NULL`, r.table), id)
    if err != nil { return nil, fmt.Errorf("%s.FindByID: %w", r.table, err) }
    defer rows.Close()

    item, err := pgx.CollectOneRow(rows, r.scan)
    if err != nil {
        if errors.Is(err, pgx.ErrNoRows) { return nil, domain.ErrNotFound }
        return nil, fmt.Errorf("%s.FindByID.scan: %w", r.table, err)
    }
    return &item, nil
}

func (r *BaseRepository[T, ID]) FindAll(ctx context.Context, filter Filter) ([]T, error) {
    q, args := filter.ToSQL(r.table)
    rows, err := r.db(ctx).Query(ctx, q, args...)
    if err != nil { return nil, fmt.Errorf("%s.FindAll: %w", r.table, err) }
    defer rows.Close()

    items, err := pgx.CollectRows(rows, r.scan)
    if err != nil { return nil, fmt.Errorf("%s.FindAll.scan: %w", r.table, err) }
    if items == nil { items = []T{} } // never return nil for empty result
    return items, nil
}
```

---

## 3. Query Builder (Type-Safe)

```go
// Prevent N+1: always use batch loading for relationships
type OrderFilter struct {
    CustomerID *string
    Status     *string
    CreatedAfter *time.Time
    Limit      int
    Cursor     *Cursor
}

func (f OrderFilter) ToSQL(table string) (string, []any) {
    var conditions []string
    var args []any
    n := 1

    conditions = append(conditions, fmt.Sprintf("%s.deleted_at IS NULL", table))

    if f.CustomerID != nil {
        conditions = append(conditions, fmt.Sprintf("customer_id = $%d", n))
        args = append(args, *f.CustomerID); n++
    }
    if f.Status != nil {
        conditions = append(conditions, fmt.Sprintf("status = $%d", n))
        args = append(args, *f.Status); n++
    }
    if f.CreatedAfter != nil {
        conditions = append(conditions, fmt.Sprintf("created_at > $%d", n))
        args = append(args, *f.CreatedAfter); n++
    }
    if f.Cursor != nil {
        conditions = append(conditions, fmt.Sprintf("(created_at, id) < ($%d, $%d)", n, n+1))
        args = append(args, f.Cursor.CreatedAt, f.Cursor.ID); n += 2
    }

    limit := 20
    if f.Limit > 0 && f.Limit <= 100 { limit = f.Limit }

    where := ""
    if len(conditions) > 0 { where = "WHERE " + strings.Join(conditions, " AND ") }

    q := fmt.Sprintf(
        `SELECT * FROM %s %s ORDER BY created_at DESC, id DESC LIMIT $%d`,
        table, where, n)
    args = append(args, limit+1) // +1 for HasMore detection
    return q, args
}
```

---

## 4. Soft Delete & Audit

```go
// Every table has: created_at, updated_at, deleted_at, created_by, updated_by
// Soft delete: set deleted_at — never physical delete for auditable entities

// SQL schema pattern
/*
CREATE TABLE orders (
    id          UUID PRIMARY KEY DEFAULT gen_random_uuid(),
    customer_id UUID NOT NULL REFERENCES customers(id),
    status      TEXT NOT NULL,
    version     BIGINT NOT NULL DEFAULT 1,
    created_at  TIMESTAMPTZ NOT NULL DEFAULT NOW(),
    updated_at  TIMESTAMPTZ NOT NULL DEFAULT NOW(),
    deleted_at  TIMESTAMPTZ,
    created_by  UUID,
    updated_by  UUID,
    CONSTRAINT orders_version_positive CHECK (version > 0)
);

-- Partial index: fast lookup of non-deleted rows
CREATE INDEX orders_customer_active ON orders(customer_id, created_at DESC)
    WHERE deleted_at IS NULL;

-- Trigger: auto-update updated_at
CREATE TRIGGER set_updated_at
    BEFORE UPDATE ON orders
    FOR EACH ROW EXECUTE FUNCTION trigger_set_updated_at();
*/

// ALL queries include: WHERE deleted_at IS NULL
// Soft delete:
func (r *Repo) Delete(ctx context.Context, id string, actorID string) error {
    result, err := r.db(ctx).Exec(ctx, `
        UPDATE orders SET deleted_at = NOW(), updated_by = $2
        WHERE id = $1 AND deleted_at IS NULL`, id, actorID)
    if err != nil { return fmt.Errorf("Repo.Delete: %w", err) }
    if result.RowsAffected() == 0 { return domain.ErrNotFound }
    return nil
}
```

---

## 5. Connection Pool Tuning

```go
func NewPool(ctx context.Context, dsn string) (*pgxpool.Pool, error) {
    cfg, err := pgxpool.ParseConfig(dsn)
    if err != nil { return nil, fmt.Errorf("pool.ParseConfig: %w", err) }

    // Tune for your workload:
    cfg.MaxConns = 25            // max open connections (match DB max_connections / num_instances)
    cfg.MinConns = 5             // keep warm connections
    cfg.MaxConnLifetime = 1 * time.Hour  // rotate to avoid server-side limits
    cfg.MaxConnIdleTime = 30 * time.Minute
    cfg.HealthCheckPeriod = 1 * time.Minute
    cfg.ConnConfig.ConnectTimeout = 5 * time.Second

    // Statement cache: speeds up repeated parameterized queries
    cfg.ConnConfig.DefaultQueryExecMode = pgx.QueryExecModeCacheDescribe

    pool, err := pgxpool.NewWithConfig(ctx, cfg)
    if err != nil { return nil, fmt.Errorf("pool.New: %w", err) }

    // Verify connectivity
    if err := pool.Ping(ctx); err != nil {
        return nil, fmt.Errorf("pool.Ping: %w", err)
    }
    return pool, nil
}
```

---

## 6. Read Replica Routing

```go
// Route reads to replica, writes to primary
type DBCluster struct {
    primary  *pgxpool.Pool
    replicas []*pgxpool.Pool
    rr       atomic.Uint64 // round-robin counter
}

func (c *DBCluster) Writer() *pgxpool.Pool { return c.primary }

func (c *DBCluster) Reader() *pgxpool.Pool {
    if len(c.replicas) == 0 { return c.primary }
    idx := c.rr.Add(1) % uint64(len(c.replicas))
    return c.replicas[idx]
}

// Context key for forcing primary read (e.g., immediately after write)
type readPrimaryKey struct{}

func WithReadPrimary(ctx context.Context) context.Context {
    return context.WithValue(ctx, readPrimaryKey{}, true)
}

func (c *DBCluster) ReaderForContext(ctx context.Context) *pgxpool.Pool {
    if ctx.Value(readPrimaryKey{}) != nil { return c.primary }
    return c.Reader()
}
```

---

## 7. Bulk Operations

```go
// Batch insert: far faster than individual INSERTs
func (r *Repo) BulkInsert(ctx context.Context, orders []*order.Order) error {
    if len(orders) == 0 { return nil }

    batch := &pgx.Batch{}
    for _, o := range orders {
        batch.Queue(`
            INSERT INTO orders (id, customer_id, status, version)
            VALUES ($1, $2, $3, $4)`,
            o.ID(), o.CustomerID(), o.Status(), o.Version())
    }

    results := r.db(ctx).SendBatch(ctx, batch)
    defer results.Close()

    for i := range orders {
        if _, err := results.Exec(); err != nil {
            return fmt.Errorf("BulkInsert[%d]: %w", i, err)
        }
    }
    return results.Close()
}

// pgx COPY protocol — fastest for large imports
func (r *Repo) CopyFrom(ctx context.Context, orders []*order.Order) error {
    rows := make([][]any, len(orders))
    for i, o := range orders {
        rows[i] = []any{o.ID(), o.CustomerID(), o.Status(), o.Version(), time.Now()}
    }
    _, err := r.pool.CopyFrom(ctx,
        pgx.Identifier{"orders"},
        []string{"id", "customer_id", "status", "version", "created_at"},
        pgx.CopyFromRows(rows))
    return err
}
```

---

## Database Checklist

- [ ] Transaction manager injects tx via context — repos don't manage transactions
- [ ] All reads include `WHERE deleted_at IS NULL`
- [ ] All queries use `$1` parameters — never string interpolation
- [ ] Connection pool sized to `DB max_connections / num_service_instances`
- [ ] Optimistic locking on all mutable aggregates via `version` column
- [ ] Soft delete with `deleted_at` + partial index for performance
- [ ] `pgx.CollectRows` / `pgx.CollectOneRow` used (not manual scan loops)
- [ ] N+1 prevention: batch load relationships, not per-row queries
- [ ] Read replica routing for read-heavy workloads
- [ ] Bulk insert via `SendBatch` or `CopyFrom` for large datasets
- [ ] Migrations are sequential, reversible, and committed with code changes
- [ ] All schema has `created_at`, `updated_at`, `created_by`, `updated_by`
