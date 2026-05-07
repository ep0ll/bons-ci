---
name: pkg-pgx
description: >
  Exhaustive reference for github.com/jackc/pgx/v5: connection pool setup, query execution,
  row scanning (pgx.CollectRows, pgx.CollectOneRow), batch operations, COPY protocol, named
  arguments, pgxpool configuration, transactions, prepared statements, custom types, LISTEN/NOTIFY,
  and error handling. This is the PRIMARY PostgreSQL driver. Never use database/sql with pgx.
  Cross-references: database/SKILL.md, packages/migrate/SKILL.md.
---

# Package: jackc/pgx/v5 — Complete Reference

## Import
```go
import (
    "github.com/jackc/pgx/v5"
    "github.com/jackc/pgx/v5/pgxpool"
    "github.com/jackc/pgx/v5/pgtype"
)
```

## 1. Pool Setup (always use pool, never single connection)

```go
func NewPool(ctx context.Context, dsn string) (*pgxpool.Pool, error) {
    cfg, err := pgxpool.ParseConfig(dsn)
    if err != nil { return nil, fmt.Errorf("pgx.ParseConfig: %w", err) }

    // Pool sizing
    cfg.MaxConns = 25
    cfg.MinConns = 5
    cfg.MaxConnLifetime = 1 * time.Hour
    cfg.MaxConnIdleTime = 30 * time.Minute
    cfg.HealthCheckPeriod = 1 * time.Minute

    // Performance: cache query descriptions (avoids parse on each exec)
    cfg.ConnConfig.DefaultQueryExecMode = pgx.QueryExecModeCacheDescribe

    // Tracing (hook into OpenTelemetry)
    cfg.ConnConfig.Tracer = &otelpgx.Tracer{}

    // Custom type registration
    cfg.AfterConnect = func(ctx context.Context, conn *pgx.Conn) error {
        // Register custom types (e.g., UUID, JSONB)
        pgtype.NewMap() // use pgtype.Map for custom types
        return nil
    }

    pool, err := pgxpool.NewWithConfig(ctx, cfg)
    if err != nil { return nil, fmt.Errorf("pgxpool.New: %w", err) }

    if err := pool.Ping(ctx); err != nil { return nil, fmt.Errorf("pgxpool.Ping: %w", err) }
    return pool, nil
}
```

## 2. Query Patterns

```go
// ── COLLECT ONE ROW ──────────────────────────────────────────────
user, err := pgx.CollectOneRow(pool.QueryRow(ctx,
    `SELECT id, name, email, created_at FROM users WHERE id = $1 AND deleted_at IS NULL`, id),
    pgx.RowToStructByName[User])
if err != nil {
    if errors.Is(err, pgx.ErrNoRows) { return nil, ErrNotFound }
    return nil, fmt.Errorf("FindByID: %w", err)
}

// ── COLLECT MULTIPLE ROWS ────────────────────────────────────────
rows, err := pool.Query(ctx,
    `SELECT id, name, email FROM users WHERE status = $1 ORDER BY created_at DESC LIMIT $2`,
    status, limit)
if err != nil { return nil, fmt.Errorf("FindAll.Query: %w", err) }

users, err := pgx.CollectRows(rows, pgx.RowToStructByName[User])
if err != nil { return nil, fmt.Errorf("FindAll.CollectRows: %w", err) }
if users == nil { users = []User{} }  // never return nil
return users, nil

// ── SCAN OPTIONS ────────────────────────────────────────────────
// pgx.RowToStructByName — maps column names to struct field tags
type User struct {
    ID        pgtype.UUID      `db:"id"`
    Name      string           `db:"name"`
    Email     string           `db:"email"`
    CreatedAt pgtype.Timestamptz `db:"created_at"`
}

// pgx.RowToAddrOfStructByName — returns *T (for nullable / optional)
user, err := pgx.CollectOneRow(rows, pgx.RowToAddrOfStructByName[User])

// pgx.RowTo[T] — for single-column queries
ids, err := pgx.CollectRows(rows, pgx.RowTo[string])

// Manual scan (when struct tags don't match)
user, err := pgx.CollectOneRow(rows, func(row pgx.CollectableRow) (User, error) {
    var u User
    return u, row.Scan(&u.ID, &u.Name, &u.Email)
})

// ── EXEC (no rows returned) ────────────────────────────────────
result, err := pool.Exec(ctx,
    `UPDATE users SET name = $1, updated_at = NOW() WHERE id = $2 AND version = $3`,
    name, id, expectedVersion)
if err != nil { return fmt.Errorf("Update.Exec: %w", err) }
if result.RowsAffected() == 0 {
    return ErrConflict  // version mismatch — optimistic lock
}

// ── NAMED ARGS (pgx v5.4+) ─────────────────────────────────────
user, err := pgx.CollectOneRow(
    pool.QueryRow(ctx, `SELECT * FROM users WHERE id = @id`, pgx.NamedArgs{"id": userID}),
    pgx.RowToStructByName[User],
)
```

## 3. Transactions

```go
// Begin transaction
tx, err := pool.Begin(ctx)
if err != nil { return fmt.Errorf("pool.Begin: %w", err) }
defer tx.Rollback(ctx)  // no-op after Commit; safe to always defer

// Do work with tx (same API as pool)
_, err = tx.Exec(ctx, `INSERT INTO orders ...`, args...)
if err != nil { return fmt.Errorf("tx.Exec: %w", err) }

// SAVEPOINT for nested logic
_, _ = tx.Exec(ctx, `SAVEPOINT sp1`)
if err := riskyOp(ctx, tx); err != nil {
    _, _ = tx.Exec(ctx, `ROLLBACK TO SAVEPOINT sp1`)
    // handle partial failure
} else {
    _, _ = tx.Exec(ctx, `RELEASE SAVEPOINT sp1`)
}

// Commit
if err := tx.Commit(ctx); err != nil {
    return fmt.Errorf("tx.Commit: %w", err)
}

// Transaction options (isolation level, read-only)
tx, err = pool.BeginTx(ctx, pgx.TxOptions{
    IsoLevel:   pgx.Serializable,
    AccessMode: pgx.ReadOnly,
})
```

## 4. Batch Operations

```go
// Batch: send multiple queries in a single round trip
batch := &pgx.Batch{}
for _, order := range orders {
    batch.Queue(`INSERT INTO orders (id, customer_id, status) VALUES ($1, $2, $3)`,
        order.ID, order.CustomerID, order.Status)
}

results := pool.SendBatch(ctx, batch)
defer results.Close()

for i := range orders {
    if _, err := results.Exec(); err != nil {
        return fmt.Errorf("BulkInsert[%d]: %w", i, err)
    }
}
if err := results.Close(); err != nil {
    return fmt.Errorf("BulkInsert.Close: %w", err)
}
```

## 5. COPY Protocol (fastest bulk insert)

```go
func BulkCopy(ctx context.Context, pool *pgxpool.Pool, users []User) error {
    rows := make([][]any, len(users))
    for i, u := range users {
        rows[i] = []any{u.ID, u.Name, u.Email, time.Now()}
    }

    copied, err := pool.CopyFrom(ctx,
        pgx.Identifier{"users"},  // table name
        []string{"id", "name", "email", "created_at"},  // columns
        pgx.CopyFromRows(rows),
    )
    if err != nil { return fmt.Errorf("CopyFrom: %w", err) }
    if int(copied) != len(users) {
        return fmt.Errorf("CopyFrom: copied %d of %d rows", copied, len(users))
    }
    return nil
}
```

## 6. LISTEN/NOTIFY

```go
// PostgreSQL async notifications — for reactive patterns
func ListenForEvents(ctx context.Context, pool *pgxpool.Pool, channel string, handler func(string)) error {
    conn, err := pool.Acquire(ctx)
    if err != nil { return fmt.Errorf("Acquire: %w", err) }
    defer conn.Release()

    if _, err := conn.Exec(ctx, "LISTEN "+channel); err != nil {
        return fmt.Errorf("LISTEN: %w", err)
    }

    for {
        notification, err := conn.Conn().WaitForNotification(ctx)
        if err != nil {
            if ctx.Err() != nil { return nil }
            return fmt.Errorf("WaitForNotification: %w", err)
        }
        handler(notification.Payload)
    }
}
```

## 7. pgtype — Custom Types

```go
// pgtype.UUID — for UUID columns
var id pgtype.UUID
id.Scan("550e8400-e29b-41d4-a716-446655440000")
uuidStr := id.String()

// pgtype.Text — nullable text
var maybeText pgtype.Text
// Valid=true means it has a value; Valid=false means NULL
if maybeText.Valid { use(maybeText.String) }

// JSON/JSONB columns
type Metadata map[string]any
var meta pgtype.JSON  // or pgtype.JSONB
if err := meta.Scan(rawJSON); err != nil { /* ... */ }
```

## 8. Error Handling

```go
import "github.com/jackc/pgx/v5/pgconn"

func isPgError(err error, code string) bool {
    var pgErr *pgconn.PgError
    return errors.As(err, &pgErr) && pgErr.Code == code
}

// Common PostgreSQL error codes
const (
    PgErrUniqueViolation     = "23505"
    PgErrForeignKeyViolation = "23503"
    PgErrNotNullViolation    = "23502"
    PgErrDeadlockDetected    = "40P01"
    PgErrSerializationFail   = "40001"
)

func (r *Repo) Save(ctx context.Context, u *User) error {
    _, err := r.pool.Exec(ctx, `INSERT INTO users (id, email) VALUES ($1, $2)`, u.ID, u.Email)
    if err != nil {
        if isPgError(err, PgErrUniqueViolation) { return domain.ErrConflict }
        return fmt.Errorf("UserRepo.Save: %w", err)
    }
    return nil
}
```

## pgx Checklist
- [ ] Always use `pgxpool` — never single `pgx.Conn` for application code
- [ ] `pgx.CollectRows` / `pgx.CollectOneRow` — never manual row iteration loops
- [ ] `ErrNoRows` mapped to domain `ErrNotFound` at repo boundary
- [ ] Unique violation (23505) mapped to domain `ErrConflict`
- [ ] `defer tx.Rollback(ctx)` immediately after `pool.Begin` — safe after Commit
- [ ] `RowsAffected() == 0` checked for UPDATE/DELETE to detect optimistic lock misses
- [ ] `CopyFrom` for bulk inserts > 100 rows
- [ ] `SendBatch` for multiple independent queries in same round trip
- [ ] `QueryExecModeCacheDescribe` set on pool for performance
