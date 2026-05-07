---
name: pkg-migrate
description: >
  Exhaustive reference for golang-migrate/migrate: migration file conventions, programmatic
  migration in Go, embedding migration files, rollback strategy, migration in CI/CD,
  and safe migration patterns for production (zero-downtime). Cross-references: database/SKILL.md.
---

# Package: golang-migrate/migrate — Complete Reference

## Import
```go
import (
    "github.com/golang-migrate/migrate/v4"
    _ "github.com/golang-migrate/migrate/v4/database/postgres"
    _ "github.com/golang-migrate/migrate/v4/source/iofs"
    "github.com/golang-migrate/migrate/v4/source/iofs"
)
```

## 1. Migration File Conventions

```
migrations/
├── 000001_create_users.up.sql
├── 000001_create_users.down.sql
├── 000002_add_user_email_index.up.sql
├── 000002_add_user_email_index.down.sql
├── 000003_create_orders.up.sql
├── 000003_create_orders.down.sql
└── 000004_add_order_status_index.up.sql   ← down not always needed for additive index

# Naming: {version}_{description}.{direction}.sql
# Version: zero-padded 6-digit integer (allows up to 999,999 migrations)
# Description: snake_case, brief
# Direction: up (apply) or down (rollback)
```

### Example Migration Files

```sql
-- migrations/000001_create_users.up.sql
CREATE EXTENSION IF NOT EXISTS "uuid-ossp";

CREATE TABLE users (
    id          UUID PRIMARY KEY DEFAULT gen_random_uuid(),
    name        TEXT NOT NULL,
    email       TEXT NOT NULL,
    status      TEXT NOT NULL DEFAULT 'active',
    version     BIGINT NOT NULL DEFAULT 1,
    created_at  TIMESTAMPTZ NOT NULL DEFAULT NOW(),
    updated_at  TIMESTAMPTZ NOT NULL DEFAULT NOW(),
    deleted_at  TIMESTAMPTZ
);

CREATE UNIQUE INDEX users_email_unique ON users(email) WHERE deleted_at IS NULL;
CREATE INDEX users_status ON users(status, created_at DESC) WHERE deleted_at IS NULL;

-- Updated_at trigger
CREATE OR REPLACE FUNCTION trigger_set_updated_at()
RETURNS TRIGGER AS $$
BEGIN NEW.updated_at = NOW(); RETURN NEW; END;
$$ LANGUAGE plpgsql;

CREATE TRIGGER set_users_updated_at
    BEFORE UPDATE ON users
    FOR EACH ROW EXECUTE FUNCTION trigger_set_updated_at();
```

```sql
-- migrations/000001_create_users.down.sql
DROP TRIGGER IF EXISTS set_users_updated_at ON users;
DROP TABLE IF EXISTS users;
DROP FUNCTION IF EXISTS trigger_set_updated_at;
```

## 2. Embedded Migrations (preferred — ships with binary)

```go
//go:embed migrations/*.sql
var migrationsFS embed.FS

func RunMigrations(ctx context.Context, dsn string) error {
    d, err := iofs.New(migrationsFS, "migrations")
    if err != nil { return fmt.Errorf("iofs.New: %w", err) }

    m, err := migrate.NewWithSourceInstance("iofs", d, dsn)
    if err != nil { return fmt.Errorf("migrate.New: %w", err) }
    defer m.Close()

    if err := m.Up(); err != nil {
        if errors.Is(err, migrate.ErrNoChange) {
            slog.InfoContext(ctx, "migrations: no changes")
            return nil
        }
        return fmt.Errorf("migrate.Up: %w", err)
    }

    v, _, _ := m.Version()
    slog.InfoContext(ctx, "migrations: applied", "version", v)
    return nil
}
```

## 3. Migration in main (run before serving)

```go
func main() {
    cfg, err := config.Load("")
    if err != nil { log.Fatalf("config: %v", err) }

    ctx := context.Background()

    // Run migrations BEFORE starting the application
    if err := db.RunMigrations(ctx, cfg.Database.DSN); err != nil {
        log.Fatalf("migrations: %v", err)
    }

    // Then start the server
    app, cleanup, err := wire.InitializeApp(ctx, cfg)
    // ...
}
```

## 4. Version Inspection

```go
func MigrationStatus(dsn string) (version uint, dirty bool, err error) {
    d, _ := iofs.New(migrationsFS, "migrations")
    m, err := migrate.NewWithSourceInstance("iofs", d, dsn)
    if err != nil { return 0, false, err }
    defer m.Close()
    return m.Version()
}

// dirty=true means last migration failed mid-way — DANGEROUS
// Must manually fix and call m.Force(version) to clear dirty state
```

## 5. Zero-Downtime Migration Patterns

```
SAFE (non-breaking — no downtime):
  ✓ Add new column WITH DEFAULT (Postgres 11+: instant, no rewrite)
  ✓ Add NOT NULL column with DEFAULT
  ✓ Add new table
  ✓ Add new index CONCURRENTLY
  ✓ Drop index
  ✓ Add nullable column
  ✓ Widen column type (e.g., INT → BIGINT)
  ✓ Add new enum value (Postgres 12+)

UNSAFE (breaking — requires careful multi-phase deploy):
  ✗ Remove column (code reads it → crash)
  ✗ Rename column (code uses old name → crash)
  ✗ Change column type (may break reads)
  ✗ Add NOT NULL without DEFAULT (fails for existing rows)
  ✗ Create index without CONCURRENTLY (table lock)

SAFE COLUMN REMOVAL PATTERN (3-phase):
  Phase 1: Deploy code that stops writing/reading the column
  Phase 2: Run migration to DROP COLUMN
  Phase 3: Remove column from struct definition

SAFE COLUMN RENAME (4-phase):
  Phase 1: Add new column, write to both old + new
  Phase 2: Backfill new column from old
  Phase 3: Read from new column, stop writing to old
  Phase 4: Drop old column
```

```sql
-- ALWAYS use CONCURRENTLY for indexes in production:
-- migrations/000010_add_orders_customer_index.up.sql
-- migrate_run_in_transaction: false  ← required for CONCURRENTLY

CREATE INDEX CONCURRENTLY IF NOT EXISTS orders_customer_id_idx
    ON orders(customer_id, created_at DESC)
    WHERE deleted_at IS NULL;
```

## 6. CI/CD Integration

```bash
# Makefile
migrate-up:
    migrate -path ./migrations -database "$(DATABASE_URL)" up

migrate-down:
    migrate -path ./migrations -database "$(DATABASE_URL)" down 1

migrate-version:
    migrate -path ./migrations -database "$(DATABASE_URL)" version

migrate-create:
    migrate create -ext sql -dir ./migrations -seq $(NAME)

# In CI: apply migrations to test DB before running integration tests
test-integration:
    migrate -path ./migrations -database "$(TEST_DATABASE_URL)" up
    go test -tags=integration -race ./test/...
```

## migrate Checklist
- [ ] Migration files: 6-digit zero-padded sequential version numbers
- [ ] Every `.up.sql` has a corresponding `.down.sql`
- [ ] Migrations embedded via `//go:embed` — ship in binary
- [ ] `migrate.Up()` called at app startup before serving traffic
- [ ] `ErrNoChange` handled as success (not error)
- [ ] Dirty state monitored — alert when `dirty=true` in production
- [ ] Production indexes created with `CONCURRENTLY` (with transaction disabled)
- [ ] Zero-downtime patterns followed for all schema changes
- [ ] Migrations tested against real DB in CI (not mocked)
- [ ] `migrate create -seq` used for new migrations — never manual file naming
