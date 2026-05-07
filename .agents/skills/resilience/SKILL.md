---
name: golang-resilience
description: >
  Go resilience patterns: circuit breakers, bulkheads, timeouts at every boundary, retry
  with exponential backoff + jitter, hedged requests, fallbacks, health-based load balancing,
  and chaos engineering hooks. Use for any service making network calls to external dependencies.
  Cross-references: networking/SKILL.md, concurrency/SKILL.md, observability/SKILL.md.
---

# Go Resilience — Production Hardening Patterns

## 1. Timeout Strategy (Every Boundary)

```go
// Timeouts must be set at EVERY network boundary — never rely on defaults
// Hierarchy: caller timeout > callee timeout (caller controls the deadline)

const (
    DBQueryTimeout       = 5 * time.Second
    CacheGetTimeout      = 100 * time.Millisecond
    ExternalAPITimeout   = 10 * time.Second
    InternalRPCTimeout   = 3 * time.Second
    KafkaPublishTimeout  = 5 * time.Second
)

// Wrap every external call with a scoped timeout
func (r *OrderRepo) FindByID(ctx context.Context, id string) (*Order, error) {
    ctx, cancel := context.WithTimeout(ctx, DBQueryTimeout)
    defer cancel()
    // ... DB query
}

// Never allow a timeout > the parent context's remaining time
func withBoundedTimeout(ctx context.Context, d time.Duration) (context.Context, context.CancelFunc) {
    deadline, ok := ctx.Deadline()
    if ok {
        remaining := time.Until(deadline)
        if remaining < d { d = remaining }
    }
    return context.WithTimeout(ctx, d)
}
```

## 2. Circuit Breaker

```go
// Circuit Breaker states: Closed (normal) → Open (failing) → Half-Open (probing)
import "github.com/sony/gobreaker"

func NewCircuitBreaker(name string, cfg CBConfig) *gobreaker.CircuitBreaker {
    return gobreaker.NewCircuitBreaker(gobreaker.Settings{
        Name:        name,
        MaxRequests: 3,           // half-open: allow N requests to probe
        Interval:    60 * time.Second, // closed: reset counts every interval
        Timeout:     30 * time.Second, // open: wait before half-open

        ReadyToTrip: func(counts gobreaker.Counts) bool {
            // Open when: >10 requests AND failure rate >60%
            failureRatio := float64(counts.TotalFailures) / float64(counts.Requests)
            return counts.Requests >= 10 && failureRatio >= 0.6
        },

        OnStateChange: func(name string, from, to gobreaker.State) {
            slog.Warn("circuit breaker state changed",
                slog.String("name", name),
                slog.String("from", from.String()),
                slog.String("to", to.String()),
            )
            // Record metric for alerting
            circuitBreakerState.WithLabelValues(name, to.String()).Set(1)
        },
    })
}

// Wrap external calls
type ResilientInventoryClient struct {
    client  InventoryClient
    breaker *gobreaker.CircuitBreaker
}

func (c *ResilientInventoryClient) CheckStock(ctx context.Context, req StockRequest) (*StockResponse, error) {
    result, err := c.breaker.Execute(func() (any, error) {
        return c.client.CheckStock(ctx, req)
    })
    if err != nil {
        if errors.Is(err, gobreaker.ErrOpenState) {
            // Circuit is open — use fallback
            return c.fallbackStock(ctx, req)
        }
        return nil, fmt.Errorf("CheckStock: %w", err)
    }
    return result.(*StockResponse), nil
}

func (c *ResilientInventoryClient) fallbackStock(_ context.Context, _ StockRequest) (*StockResponse, error) {
    // Fallback: assume available (optimistic) or return cached last-known value
    return &StockResponse{Available: true, Source: "fallback"}, nil
}
```

## 3. Retry with Exponential Backoff + Jitter

```go
type RetryConfig struct {
    MaxAttempts int
    BaseDelay   time.Duration
    MaxDelay    time.Duration
    Multiplier  float64
    JitterFactor float64    // 0.0–1.0: fraction of delay to randomize
}

var DefaultRetryConfig = RetryConfig{
    MaxAttempts:  4,
    BaseDelay:    100 * time.Millisecond,
    MaxDelay:     10 * time.Second,
    Multiplier:   2.0,
    JitterFactor: 0.2,
}

func WithRetry(ctx context.Context, cfg RetryConfig, fn func(ctx context.Context) error) error {
    var lastErr error
    for attempt := 0; attempt < cfg.MaxAttempts; attempt++ {
        if err := fn(ctx); err == nil { return nil } else { lastErr = err }

        if !IsRetryable(lastErr) { return lastErr }
        if attempt == cfg.MaxAttempts-1 { break } // don't sleep after last attempt

        delay := min(
            time.Duration(float64(cfg.BaseDelay)*math.Pow(cfg.Multiplier, float64(attempt))),
            cfg.MaxDelay,
        )
        // Add jitter: delay ± (delay * jitterFactor)
        jitter := time.Duration(float64(delay) * cfg.JitterFactor * (rand.Float64()*2 - 1))
        delay += jitter

        slog.WarnContext(ctx, "retrying",
            slog.Int("attempt", attempt+1),
            slog.Duration("delay", delay),
            slog.Any("err", lastErr),
        )
        select {
        case <-time.After(delay):
        case <-ctx.Done(): return fmt.Errorf("retry cancelled: %w", ctx.Err())
        }
    }
    return fmt.Errorf("all %d attempts failed: %w", cfg.MaxAttempts, lastErr)
}

// Classify errors as retryable or not
func IsRetryable(err error) bool {
    if err == nil { return false }
    // Never retry: client errors, validation, auth
    if errors.Is(err, domain.ErrValidation)   { return false }
    if errors.Is(err, domain.ErrUnauthorized) { return false }
    if errors.Is(err, domain.ErrForbidden)    { return false }
    if errors.Is(err, domain.ErrNotFound)     { return false }
    // Retry: transient network and server errors
    if errors.Is(err, context.DeadlineExceeded) { return true }
    var httpErr *HTTPError
    if errors.As(err, &httpErr) {
        return httpErr.StatusCode == 429 || httpErr.StatusCode >= 500
    }
    return true // assume retryable for unknown errors
}
```

## 4. Bulkhead (Concurrency Limiter per Dependency)

```go
// Bulkhead: limit concurrent calls to each dependency independently
// Prevents one slow dependency from exhausting all goroutines

type Bulkhead struct {
    sem  chan struct{}
    name string
}

func NewBulkhead(name string, maxConcurrent int) *Bulkhead {
    return &Bulkhead{
        sem:  make(chan struct{}, maxConcurrent),
        name: name,
    }
}

func (b *Bulkhead) Execute(ctx context.Context, fn func() error) error {
    // Try to acquire slot
    select {
    case b.sem <- struct{}{}:
        // Acquired
    case <-ctx.Done():
        return fmt.Errorf("bulkhead %s: %w", b.name, ctx.Err())
    default:
        // Bulkhead full — fast fail
        return fmt.Errorf("bulkhead %s: at capacity (%d concurrent)", b.name, cap(b.sem))
    }

    defer func() { <-b.sem }()
    return fn()
}

// Usage: separate bulkheads per external dependency
var (
    dbBulkhead        = NewBulkhead("postgres", 20)
    inventoryBulkhead = NewBulkhead("inventory-svc", 10)
    paymentBulkhead   = NewBulkhead("payment-svc", 5)
)
```

## 5. Hedged Requests (Tail Latency Reduction)

```go
// Hedge: send same request to multiple replicas after a delay;
// use first successful response. Improves p99 latency at cost of extra load.

func HedgedRequest(ctx context.Context, hedgeDelay time.Duration, fn func(ctx context.Context) (any, error)) (any, error) {
    type result struct {
        value any
        err   error
    }

    results := make(chan result, 2) // buffer for 2 attempts
    ctx, cancel := context.WithCancel(ctx)
    defer cancel()

    attempt := func() {
        v, err := fn(ctx)
        select {
        case results <- result{v, err}:
        case <-ctx.Done():
        }
    }

    // First attempt
    go attempt()

    // Hedge: launch second attempt after delay
    select {
    case r := <-results:
        if r.err == nil { return r.value, nil }
        // First attempt failed — wait for hedge
    case <-time.After(hedgeDelay):
        go attempt() // launch hedge
    case <-ctx.Done():
        return nil, ctx.Err()
    }

    // Return first successful result
    for i := 0; i < 2; i++ {
        select {
        case r := <-results:
            if r.err == nil { cancel(); return r.value, nil }
        case <-ctx.Done():
            return nil, ctx.Err()
        }
    }
    return nil, fmt.Errorf("all hedged attempts failed")
}
```

## 6. Fallback Strategies

```go
// Strategy 1: Cached fallback (stale but better than nothing)
func (s *ProductService) GetPrice(ctx context.Context, productID string) (decimal.Decimal, error) {
    price, err := s.priceService.GetCurrentPrice(ctx, productID)
    if err != nil {
        // Try cache as fallback
        if cached, ok := s.priceCache.Get(productID); ok {
            slog.WarnContext(ctx, "using cached price fallback", "product", productID)
            return cached, nil
        }
        return decimal.Zero, fmt.Errorf("GetPrice: primary and cache both failed: %w", err)
    }
    s.priceCache.Set(productID, price, 5*time.Minute)
    return price, nil
}

// Strategy 2: Default fallback (safe default value)
func (s *RecommendationService) GetRecommendations(ctx context.Context, userID string) []Product {
    recs, err := s.mlService.Recommend(ctx, userID)
    if err != nil {
        slog.WarnContext(ctx, "recommendations unavailable, using defaults", "err", err)
        return s.defaultRecommendations() // curated top products
    }
    return recs
}

// Strategy 3: Partial failure (return what you have)
func (s *ProductService) GetProductDetails(ctx context.Context, id string) (*ProductDetails, error) {
    product, err := s.repo.FindByID(ctx, id)
    if err != nil { return nil, err } // critical — cannot continue

    // Non-critical enrichments — degrade gracefully
    reviews, err := s.reviewService.GetSummary(ctx, id)
    if err != nil { reviews = nil; slog.WarnContext(ctx, "reviews unavailable", "err", err) }

    inventory, err := s.inventoryService.GetCount(ctx, id)
    if err != nil { inventory = -1; slog.WarnContext(ctx, "inventory unavailable", "err", err) }

    return &ProductDetails{Product: product, Reviews: reviews, InventoryCount: inventory}, nil
}
```

## Resilience Checklist
- [ ] Every external call wrapped with `context.WithTimeout` scoped to that call
- [ ] Circuit breaker on all external service calls (not DB — handled by pool)
- [ ] Retry with exponential backoff + jitter — never fixed delay
- [ ] `IsRetryable` function distinguishes transient from permanent failures
- [ ] Bulkhead per external dependency — not shared global limit
- [ ] Fallback defined for every non-critical dependency
- [ ] Fallbacks logged as WARN — never silently degrade without observability
- [ ] Circuit breaker state changes emit metrics and alerts
- [ ] Hedged requests considered for p99-sensitive read paths
- [ ] All resilience wrappers tested with fault injection
