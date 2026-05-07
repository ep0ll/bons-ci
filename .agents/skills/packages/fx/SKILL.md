---
name: pkg-fx
description: >
  Exhaustive reference for uber-go/fx: application lifecycle, dependency injection with
  fx.Provide, fx.Invoke, fx.Module, parameter objects, result objects, named values,
  lifecycle hooks (OnStart/OnStop), and testing with fxtest. Alternative to google/wire —
  runtime DI vs compile-time. Cross-references: architecture/SKILL.md, packages/wire/SKILL.md.
---

# Package: uber-go/fx — Complete Reference

## Import
```go
import (
    "go.uber.org/fx"
    "go.uber.org/fx/fxtest"
)
```

## 1. Application Setup

```go
func main() {
    app := fx.New(
        // Modules group related providers
        InfrastructureModule,
        RepositoryModule,
        ServiceModule,
        HandlerModule,

        // Invoke: run side effects after all deps are constructed
        fx.Invoke(registerRoutes),
        fx.Invoke(startServer),
    )

    // Start (blocks until signal or error)
    app.Run()
}

// Module: group related providers
var InfrastructureModule = fx.Module("infrastructure",
    fx.Provide(NewConfig),
    fx.Provide(NewPool),
    fx.Provide(NewRedisClient),
    fx.Provide(NewLogger),
)

var RepositoryModule = fx.Module("repository",
    fx.Provide(
        fx.Annotate(NewOrderRepository,
            fx.As(new(order.Repository)),  // bind interface
        ),
    ),
)
```

## 2. Providers

```go
// Simple provider
func NewPool(cfg *Config) (*pgxpool.Pool, error) {
    return pgxpool.New(context.Background(), cfg.Database.DSN)
}

// Provider with lifecycle hooks (OnStart/OnStop)
func NewHTTPServer(cfg *Config, handler http.Handler, lc fx.Lifecycle) *http.Server {
    srv := &http.Server{Addr: cfg.Server.Addr, Handler: handler}

    lc.Append(fx.Hook{
        OnStart: func(ctx context.Context) error {
            go func() {
                if err := srv.ListenAndServe(); err != nil && !errors.Is(err, http.ErrServerClosed) {
                    slog.Error("server error", "err", err)
                }
            }()
            slog.Info("server started", "addr", cfg.Server.Addr)
            return nil
        },
        OnStop: func(ctx context.Context) error {
            return srv.Shutdown(ctx)
        },
    })
    return srv
}

// Provider returning multiple values
func NewDBClients(cfg *Config) (*pgxpool.Pool, *redis.Client, error) {
    pool, err := pgxpool.New(context.Background(), cfg.Database.DSN)
    if err != nil { return nil, nil, err }
    rdb, err := newRedis(cfg.Redis.Addr)
    if err != nil { return nil, nil, err }
    return pool, rdb, nil
}
```

## 3. Parameter Objects (fx.In)

```go
// Group multiple deps into a struct — avoids long constructor arg lists
type OrderServiceParams struct {
    fx.In

    Repo      order.Repository
    Publisher EventPublisher
    Logger    *slog.Logger
    Config    *Config
    Metrics   MetricsRecorder `optional:"true"` // optional dependency
}

func NewOrderService(p OrderServiceParams) *OrderService {
    return &OrderService{
        repo:      p.Repo,
        publisher: p.Publisher,
        logger:    p.Logger.Named("order_service"),
    }
}
```

## 4. Result Objects (fx.Out)

```go
// Return multiple values as named dependencies
type RepositoryResult struct {
    fx.Out

    OrderRepo  order.Repository
    UserRepo   user.Repository
    ProductRepo product.Repository
}

func NewRepositories(pool *pgxpool.Pool) RepositoryResult {
    return RepositoryResult{
        OrderRepo:   postgres.NewOrderRepository(pool),
        UserRepo:    postgres.NewUserRepository(pool),
        ProductRepo: postgres.NewProductRepository(pool),
    }
}
```

## 5. Named Values

```go
// When you need multiple values of the same type
fx.Provide(
    fx.Annotate(NewPrimaryPool,
        fx.ResultTags(`name:"primary"`),
    ),
    fx.Annotate(NewReplicaPool,
        fx.ResultTags(`name:"replica"`),
    ),
)

// Consume named value
type ReaderParams struct {
    fx.In
    Pool *pgxpool.Pool `name:"replica"`
}
```

## 6. Testing with fxtest

```go
func TestOrderService(t *testing.T) {
    mockRepo := mocks.NewMockOrderRepository(t)
    mockRepo.EXPECT().Save(mock.Anything, mock.Anything).Return(nil)

    app := fxtest.New(t,
        fx.Provide(func() order.Repository { return mockRepo }),
        fx.Provide(func() EventPublisher { return &noopPublisher{} }),
        fx.Provide(NewOrderService),
        fx.Invoke(func(svc *OrderService) {
            // Test the service
            _, err := svc.CreateOrder(context.Background(), testCmd)
            assert.NoError(t, err)
        }),
    )
    app.RequireStart()
    app.RequireStop()
}
```

## 7. fx vs wire Decision

```
USE wire when:
  - Compile-time DI graph verification preferred
  - Large teams (catches DI errors before runtime)
  - Simpler applications without complex lifecycle management

USE fx when:
  - Runtime DI flexibility needed
  - Complex lifecycle management (OnStart/OnStop ordering matters)
  - Module-based extensibility (plugins, feature flags at startup)
  - Rapid prototyping (no codegen step)
```

## fx Checklist
- [ ] All services use `fx.In` parameter objects when >3 dependencies
- [ ] `lc.Append(fx.Hook{OnStart, OnStop})` for all long-running resources
- [ ] `fx.Module` groups related providers — not one giant `fx.New` call
- [ ] `optional:"true"` for non-critical dependencies (metrics, tracing)
- [ ] `fxtest.New(t, ...)` for unit tests — not global app instance
- [ ] `fx.As(new(Interface))` for interface binding instead of concrete types
- [ ] `app.Run()` in main — handles OS signals automatically
- [ ] Lifecycle hooks are non-blocking — server starts in goroutine within OnStart
