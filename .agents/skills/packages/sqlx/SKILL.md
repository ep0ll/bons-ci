---
name: pkg-sqlx
description: >
  Exhaustive reference for github.com/jmoiron/sqlx: named queries, struct scanning with db tags,
  sqlx.In for slice expansion, NamedExec, Get/Select helpers, transaction handling, and safe
  patterns for avoiding SQL injection. Secondary to pgx — use when database/sql compatibility
  is required or when migrating from database/sql. Cross-references: database/SKILL.md.
---

# Package: jmoiron/sqlx — Complete Reference

## Import
```go
import (
    "github.com/jmoiron/sqlx"
    _ "github.com/jackc/pgx/v5/stdlib" // pgx stdlib driver for database/sql compat
    // OR: _ "github.com/lib/pq"         // pure postgres driver
)
```

## 1. Connection Setup

```go
func NewDB(dsn string) (*sqlx.DB, error) {
    db, err := sqlx.Open("pgx", dsn) // use "pgx" driver name with pgx stdlib
    if err != nil { return nil, fmt.Errorf("sqlx.Open: %w", err) }

    // Pool configuration
    db.SetMaxOpenConns(25)
    db.SetMaxIdleConns(5)
    db.SetConnMaxLifetime(1 * time.Hour)
    db.SetConnMaxIdleTime(30 * time.Minute)

    ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
    defer cancel()
    if err := db.PingContext(ctx); err != nil {
        return nil, fmt.Errorf("sqlx.Ping: %w", err)
    }
    return db, nil
}
```

## 2. Struct Tags

```go
// Use `db` tag — must match column names exactly
type User struct {
    ID        string         `db:"id"`
    Name      string         `db:"name"`
    Email     string         `db:"email"`
    CreatedAt time.Time      `db:"created_at"`
    DeletedAt sql.NullTime   `db:"deleted_at"` // nullable column
    Bio       sql.NullString `db:"bio"`        // nullable text
}

// Embedded structs — sqlx flattens them
type Timestamps struct {
    CreatedAt time.Time `db:"created_at"`
    UpdatedAt time.Time `db:"updated_at"`
}
type Order struct {
    ID         string `db:"id"`
    CustomerID string `db:"customer_id"`
    Timestamps        // embedded — fields promoted with their db tags
}
```

## 3. Query Patterns

```go
// ── GET: fetch exactly one row into struct ──────────────────────
var user User
err := db.GetContext(ctx, &user,
    `SELECT id, name, email, created_at FROM users WHERE id = $1 AND deleted_at IS NULL`, id)
if errors.Is(err, sql.ErrNoRows) { return nil, ErrNotFound }
if err != nil { return nil, fmt.Errorf("UserRepo.FindByID: %w", err) }

// ── SELECT: fetch multiple rows into slice ──────────────────────
var users []User
err = db.SelectContext(ctx, &users,
    `SELECT id, name, email FROM users WHERE status = $1 ORDER BY created_at DESC LIMIT $2`,
    status, limit)
if err != nil { return nil, fmt.Errorf("UserRepo.FindAll: %w", err) }
if users == nil { users = []User{} } // never return nil slice

// ── NAMED QUERY: use struct fields as params ────────────────────
user := User{Name: "Alice", Email: "alice@example.com"}
result, err := db.NamedExecContext(ctx, `
    INSERT INTO users (name, email, created_at)
    VALUES (:name, :email, NOW())`,
    user)
if err != nil { return fmt.Errorf("UserRepo.Save: %w", err) }

// ── NAMED QUERY with returning ──────────────────────────────────
rows, err := db.NamedQueryContext(ctx, `
    INSERT INTO users (name, email)
    VALUES (:name, :email)
    RETURNING id, created_at`,
    user)
if err != nil { return nil, fmt.Errorf("UserRepo.Create: %w", err) }
defer rows.Close()
if rows.Next() {
    if err := rows.StructScan(&user); err != nil {
        return nil, fmt.Errorf("UserRepo.Create.scan: %w", err)
    }
}

// ── EXEC: no rows returned ──────────────────────────────────────
result, err = db.ExecContext(ctx,
    `UPDATE users SET name = $1, updated_at = NOW() WHERE id = $2`, name, id)
if err != nil { return fmt.Errorf("UserRepo.Update: %w", err) }
if n, _ := result.RowsAffected(); n == 0 {
    return ErrNotFound
}
```

## 4. sqlx.In — Slice Expansion

```go
// Expand a slice into IN clause placeholders
// sqlx.In rewrites: WHERE id IN (?) → WHERE id IN (?, ?, ?)
ids := []string{"id1", "id2", "id3"}
query, args, err := sqlx.In(`SELECT * FROM users WHERE id IN (?)`, ids)
if err != nil { return nil, fmt.Errorf("sqlx.In: %w", err) }

// IMPORTANT: rebind for your driver (postgres uses $1, $2 not ?)
query = db.Rebind(query)

var users []User
if err := db.SelectContext(ctx, &users, query, args...); err != nil {
    return nil, fmt.Errorf("UserRepo.FindByIDs: %w", err)
}
```

## 5. Transactions

```go
func (r *UserRepo) TransferWithTx(ctx context.Context, fromID, toID string, amount int) error {
    tx, err := r.db.BeginTxx(ctx, &sql.TxOptions{Isolation: sql.LevelReadCommitted})
    if err != nil { return fmt.Errorf("BeginTx: %w", err) }
    defer func() {
        if p := recover(); p != nil { tx.Rollback(); panic(p) }
    }()

    // Use tx.GetContext, tx.ExecContext — same API as db
    var fromBalance int
    if err := tx.GetContext(ctx, &fromBalance,
        `SELECT balance FROM accounts WHERE id = $1 FOR UPDATE`, fromID); err != nil {
        tx.Rollback()
        return fmt.Errorf("get fromBalance: %w", err)
    }
    if fromBalance < amount { tx.Rollback(); return ErrInsufficientFunds }

    if _, err := tx.ExecContext(ctx,
        `UPDATE accounts SET balance = balance - $1 WHERE id = $2`, amount, fromID); err != nil {
        tx.Rollback()
        return fmt.Errorf("debit: %w", err)
    }
    if _, err := tx.ExecContext(ctx,
        `UPDATE accounts SET balance = balance + $1 WHERE id = $2`, amount, toID); err != nil {
        tx.Rollback()
        return fmt.Errorf("credit: %w", err)
    }

    return tx.Commit()
}
```

## 6. Error Handling

```go
import "github.com/lib/pq"  // or pgconn for pgx driver

func isUniqueViolation(err error) bool {
    var pqErr *pq.Error
    if errors.As(err, &pqErr) { return pqErr.Code == "23505" }
    // For pgx stdlib driver:
    var pgErr *pgconn.PgError
    if errors.As(err, &pgErr) { return pgErr.Code == "23505" }
    return false
}

func (r *Repo) Save(ctx context.Context, u *User) error {
    _, err := r.db.ExecContext(ctx, `INSERT INTO users ...`, u.ID, u.Email)
    if err != nil {
        if isUniqueViolation(err) { return domain.ErrConflict }
        return fmt.Errorf("UserRepo.Save: %w", err)
    }
    return nil
}
```

## sqlx Checklist
- [ ] `db` struct tags match database column names exactly
- [ ] `GetContext` returns `sql.ErrNoRows` → map to `domain.ErrNotFound` at repo boundary
- [ ] `SelectContext` never returns nil slice — check and assign `[]T{}` if nil
- [ ] `sqlx.In` + `db.Rebind` for `IN` clause with variable-length slices
- [ ] `NamedExecContext` / `NamedQueryContext` for insert/update with struct params
- [ ] `BeginTxx` (not `Begin`) to pass context + options to transaction
- [ ] `defer tx.Rollback()` with explicit commit — safe after commit is no-op
- [ ] Unique violation (23505) mapped to `domain.ErrConflict`
- [ ] Prefer pgx native API over sqlx when pgx is already in use
