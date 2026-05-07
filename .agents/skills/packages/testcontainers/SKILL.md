---
name: pkg-testcontainers
description: >
  Exhaustive reference for github.com/testcontainers/testcontainers-go: spinning up real
  PostgreSQL, Redis, Kafka, and custom containers in Go tests; wait strategies, lifecycle
  management, parallel test isolation, reuse mode, and network creation. Essential for
  integration testing. Cross-references: testing/SKILL.md, packages/pgx/SKILL.md.
---

# Package: testcontainers/testcontainers-go — Complete Reference

## Import
```go
import (
    "github.com/testcontainers/testcontainers-go"
    "github.com/testcontainers/testcontainers-go/wait"
    "github.com/testcontainers/testcontainers-go/modules/postgres"
    "github.com/testcontainers/testcontainers-go/modules/redis"
    "github.com/testcontainers/testcontainers-go/modules/kafka"
)
```

## 1. PostgreSQL Container

```go
// internal/testutil/db.go
package testutil

// MustSetupDB creates a real Postgres container for integration tests.
// Automatically cleaned up via t.Cleanup when test finishes.
func MustSetupDB(t *testing.T) *pgxpool.Pool {
    t.Helper()

    ctx := context.Background()

    pgContainer, err := postgres.RunContainer(ctx,
        testcontainers.WithImage("postgres:16-alpine"),
        postgres.WithDatabase("testdb"),
        postgres.WithUsername("testuser"),
        postgres.WithPassword("testpass"),
        testcontainers.WithWaitStrategy(
            wait.ForLog("database system is ready to accept connections").
                WithOccurrence(2).
                WithStartupTimeout(60*time.Second),
        ),
    )
    require.NoError(t, err, "start postgres container")
    t.Cleanup(func() {
        if err := pgContainer.Terminate(ctx); err != nil {
            t.Logf("terminate postgres container: %v", err)
        }
    })

    dsn, err := pgContainer.ConnectionString(ctx, "sslmode=disable")
    require.NoError(t, err)

    pool, err := pgxpool.New(ctx, dsn)
    require.NoError(t, err)
    t.Cleanup(pool.Close)

    // Run migrations
    require.NoError(t, runMigrations(ctx, pool), "run migrations")
    return pool
}

func runMigrations(ctx context.Context, pool *pgxpool.Pool) error {
    // Use golang-migrate with embedded SQL files
    d, err := iofs.New(migrationsFS, "migrations")
    if err != nil { return err }
    m, err := migrate.NewWithSourceInstance("iofs", d,
        "pgx5://"+pool.Config().ConnString())
    if err != nil { return err }
    defer m.Close()
    if err := m.Up(); err != nil && !errors.Is(err, migrate.ErrNoChange) {
        return err
    }
    return nil
}
```

## 2. Redis Container

```go
func MustSetupRedis(t *testing.T) *redis.Client {
    t.Helper()
    ctx := context.Background()

    redisContainer, err := tcredis.RunContainer(ctx,
        testcontainers.WithImage("redis:7-alpine"),
        testcontainers.WithWaitStrategy(
            wait.ForLog("Ready to accept connections").
                WithStartupTimeout(30*time.Second),
        ),
    )
    require.NoError(t, err)
    t.Cleanup(func() { redisContainer.Terminate(ctx) })

    addr, err := redisContainer.Endpoint(ctx, "")
    require.NoError(t, err)

    rdb := redis.NewClient(&redis.Options{Addr: addr})
    t.Cleanup(func() { rdb.Close() })
    return rdb
}
```

## 3. Kafka Container

```go
func MustSetupKafka(t *testing.T) (brokers []string, cleanup func()) {
    t.Helper()
    ctx := context.Background()

    kafkaContainer, err := tckafka.RunContainer(ctx,
        testcontainers.WithImage("confluentinc/cp-kafka:7.4.0"),
        tckafka.WithClusterID("test-cluster"),
    )
    require.NoError(t, err)

    brokersList, err := kafkaContainer.Brokers(ctx)
    require.NoError(t, err)

    return brokersList, func() { kafkaContainer.Terminate(ctx) }
}
```

## 4. Custom Container (Any Docker Image)

```go
func MustSetupCustomService(t *testing.T) string {
    t.Helper()
    ctx := context.Background()

    req := testcontainers.ContainerRequest{
        Image:        "myservice:latest",
        ExposedPorts: []string{"8080/tcp"},
        Env: map[string]string{
            "ENV":       "test",
            "LOG_LEVEL": "debug",
        },
        Mounts: testcontainers.ContainerMounts{
            testcontainers.BindMount("/tmp/config.yaml", "/app/config.yaml"),
        },
        WaitingFor: wait.ForHTTP("/healthz").
            WithPort("8080/tcp").
            WithStatusCodeMatcher(func(status int) bool { return status == 200 }).
            WithStartupTimeout(30*time.Second),
    }

    container, err := testcontainers.GenericContainer(ctx,
        testcontainers.GenericContainerRequest{
            ContainerRequest: req,
            Started:          true,
        })
    require.NoError(t, err)
    t.Cleanup(func() { container.Terminate(ctx) })

    endpoint, err := container.Endpoint(ctx, "http")
    require.NoError(t, err)
    return endpoint
}
```

## 5. Parallel Test Isolation

```go
// Each parallel test gets its own container (full isolation)
func TestOrderRepository_Parallel(t *testing.T) {
    t.Parallel()  // tests run concurrently — each with own container
    pool := testutil.MustSetupDB(t)  // dedicated container per test
    repo := postgres.NewOrderRepository(pool)
    // ... test freely with no shared state
}

// OR: Shared container + transaction rollback (faster, less isolated)
type OrderSuite struct {
    suite.Suite
    pool *pgxpool.Pool  // shared across all tests in suite
}

func (s *OrderSuite) SetupSuite() {
    s.pool = testutil.MustSetupDB(s.T())
}

func (s *OrderSuite) SetupTest() {
    // Truncate tables for clean state (faster than new container per test)
    _, _ = s.pool.Exec(context.Background(), `TRUNCATE orders, order_items CASCADE`)
}
```

## 6. Container Reuse (Fast Local Dev)

```go
// Reuse containers across test runs (avoids startup time locally)
// Set TESTCONTAINERS_RYUK_DISABLED=true and use WithReuse option
func MustSetupDBReuse(t *testing.T) *pgxpool.Pool {
    t.Helper()
    ctx := context.Background()

    pgContainer, err := postgres.RunContainer(ctx,
        testcontainers.WithImage("postgres:16-alpine"),
        postgres.WithDatabase("testdb"),
        postgres.WithUsername("testuser"),
        postgres.WithPassword("testpass"),
        testcontainers.CustomizeRequest(testcontainers.GenericContainerRequest{
            Reuse: true,  // reuse if already running
        }),
        // ... wait strategy
    )
    require.NoError(t, err)
    // No t.Cleanup for terminate — container persists across runs
    // ...
}
```

## 7. Docker Network (Multi-Container Tests)

```go
// When services need to communicate with each other
func setupNetwork(t *testing.T) *testcontainers.DockerNetwork {
    t.Helper()
    ctx := context.Background()

    network, err := testcontainers.GenericNetwork(ctx,
        testcontainers.GenericNetworkRequest{
            NetworkRequest: testcontainers.NetworkRequest{
                Name:           "test-network",
                CheckDuplicate: true,
            },
        })
    require.NoError(t, err)
    t.Cleanup(func() { network.Remove(ctx) })
    return network
}

// Attach containers to the same network
req := testcontainers.ContainerRequest{
    Networks:       []string{network.Name},
    NetworkAliases: map[string][]string{network.Name: {"myservice"}},
}
```

## Wait Strategies Reference

```go
wait.ForLog("ready")                           // wait for log line
wait.ForListeningPort("8080/tcp")              // wait for port to open
wait.ForHTTP("/health").WithPort("8080/tcp")   // wait for HTTP 200
wait.ForHTTP("/health").WithStatusCodeMatcher(func(s int) bool { return s < 500 })
wait.ForSQL("postgres", driverName, func(host, port string) string { return dsn })
wait.ForAll(                                   // wait for multiple conditions
    wait.ForLog("ready"),
    wait.ForListeningPort("8080/tcp"),
)
wait.ForLog("ready").WithStartupTimeout(60*time.Second)
wait.ForLog("ready").WithPollInterval(500*time.Millisecond)
```

## testcontainers Checklist
- [ ] `t.Helper()` called first in all helper functions
- [ ] `t.Cleanup` registered immediately after container start (not deferred elsewhere)
- [ ] `WithStartupTimeout` set on all wait strategies (never rely on default)
- [ ] Parallel tests use dedicated containers or transaction-rollback pattern
- [ ] `require.NoError` used for container start — not `assert` (test must stop)
- [ ] `TESTCONTAINERS_RYUK_DISABLED=true` only in CI where cleanup is guaranteed
- [ ] Integration tests guarded by `testing.Short()` skip
- [ ] Container images pinned to specific tags — never `latest` in CI
