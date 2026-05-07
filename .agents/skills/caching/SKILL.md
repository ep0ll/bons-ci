---
name: golang-caching
description: >
  Go caching strategies: cache-aside, write-through, write-behind, read-through, cache
  stampede prevention (singleflight), TTL design, cache warming, invalidation patterns,
  multi-level caching (L1 in-process + L2 Redis), and cache observability.
  Cross-references: packages/redis/SKILL.md, packages/ristretto/SKILL.md, concurrency/SKILL.md.
---

# Go Caching — Production Strategies

## 1. Cache Abstraction (Pluggable)

```go
// Generic cache interface — swap implementations without changing consumers
type Cache[K comparable, V any] interface {
    Get(ctx context.Context, key K) (V, bool, error)
    Set(ctx context.Context, key K, value V, ttl time.Duration) error
    Delete(ctx context.Context, key K) error
    DeleteMany(ctx context.Context, keys ...K) error
}

// Typed key helpers to prevent cache key collisions
func userKey(id string) string    { return "user:v1:" + id }
func orderKey(id string) string   { return "order:v1:" + id }
func sessionKey(id string) string { return "session:v1:" + id }
// Versioned keys: bump v1→v2 when schema changes (old keys expire naturally)
```

## 2. Cache-Aside (Lazy Loading)

```go
// Most common pattern: check cache, on miss load from DB, populate cache
type CachedUserRepo struct {
    db    UserRepository
    cache Cache[string, *User]
    sfg   singleflight.Group   // stampede prevention
    ttl   time.Duration
}

func (r *CachedUserRepo) FindByID(ctx context.Context, id string) (*User, error) {
    key := userKey(id)

    // L1: check cache
    if user, ok, err := r.cache.Get(ctx, key); err == nil && ok {
        return user, nil
    }

    // L2: singleflight — only one goroutine fetches; others wait for result
    v, err, _ := r.sfg.Do(key, func() (any, error) {
        user, err := r.db.FindByID(ctx, id)
        if err != nil { return nil, err }

        // Populate cache (non-fatal if this fails)
        if setErr := r.cache.Set(ctx, key, user, r.ttl); setErr != nil {
            slog.WarnContext(ctx, "cache set failed", "key", key, "err", setErr)
        }
        return user, nil
    })
    if err != nil { return nil, err }
    return v.(*User), nil
}

// Invalidate on write (write-invalidate pattern)
func (r *CachedUserRepo) Save(ctx context.Context, user *User) error {
    if err := r.db.Save(ctx, user); err != nil { return err }
    // Delete AFTER successful write — next read repopulates
    _ = r.cache.Delete(ctx, userKey(user.ID))
    return nil
}
```

## 3. Multi-Level Cache (L1 + L2)

```go
// L1: in-process (ristretto) — nanoseconds
// L2: Redis — milliseconds
// L3: Database — milliseconds to seconds

type MultiLevelCache[K comparable, V any] struct {
    l1  Cache[K, V]          // in-process (ristretto)
    l2  Cache[K, V]          // distributed (Redis)
    l1TTL time.Duration
    l2TTL time.Duration
}

func (c *MultiLevelCache[K, V]) Get(ctx context.Context, key K) (V, bool, error) {
    // L1 hit
    if v, ok, _ := c.l1.Get(ctx, key); ok {
        return v, true, nil
    }

    // L2 hit — backfill L1
    if v, ok, err := c.l2.Get(ctx, key); err == nil && ok {
        _ = c.l1.Set(ctx, key, v, c.l1TTL)
        return v, true, nil
    } else if err != nil {
        slog.WarnContext(ctx, "L2 cache error", "err", err)
        // Don't fail the request on cache error — fall through to DB
    }

    var zero V
    return zero, false, nil
}

func (c *MultiLevelCache[K, V]) Set(ctx context.Context, key K, value V) error {
    // Write to both levels
    _ = c.l1.Set(ctx, key, value, c.l1TTL)
    return c.l2.Set(ctx, key, value, c.l2TTL)
}

func (c *MultiLevelCache[K, V]) Invalidate(ctx context.Context, key K) {
    _ = c.l1.Delete(ctx, key)
    _ = c.l2.Delete(ctx, key)
}
```

## 4. Write-Through Cache

```go
// Write to cache AND DB synchronously — always consistent
type WriteThroughRepo struct {
    db    Repository
    cache Cache[string, *User]
    ttl   time.Duration
}

func (r *WriteThroughRepo) Save(ctx context.Context, user *User) error {
    // 1. Write DB first (source of truth)
    if err := r.db.Save(ctx, user); err != nil { return err }

    // 2. Update cache immediately
    if err := r.cache.Set(ctx, userKey(user.ID), user, r.ttl); err != nil {
        // Log but don't fail — DB write succeeded, cache will be eventually consistent
        slog.WarnContext(ctx, "write-through cache update failed", "err", err)
    }
    return nil
}
```

## 5. Cache Warming

```go
// Pre-populate cache on startup for hot data
func WarmCache(ctx context.Context, repo UserRepository, cache Cache[string, *User]) error {
    slog.InfoContext(ctx, "warming cache")
    start := time.Now()

    // Load most-accessed users (e.g., last 24h active users)
    users, err := repo.FindRecentlyActive(ctx, 1000)
    if err != nil { return fmt.Errorf("cache warm: %w", err) }

    var wg sync.WaitGroup
    sem := make(chan struct{}, 20) // bounded concurrency

    for _, user := range users {
        user := user
        wg.Add(1)
        sem <- struct{}{}
        go func() {
            defer func() { <-sem; wg.Done() }()
            _ = cache.Set(ctx, userKey(user.ID), user, 1*time.Hour)
        }()
    }
    wg.Wait()

    slog.InfoContext(ctx, "cache warmed",
        slog.Int("count", len(users)),
        slog.Duration("elapsed", time.Since(start)))
    return nil
}
```

## 6. TTL Strategy Guide

```go
// TTL selection guide:
// Immutable data (product catalog, config):   1h - 24h
// Semi-stable (user profile, settings):       15m - 1h
// Session data:                               TTL = session timeout
// Hot computed data (order counts, stats):    1m - 5m
// Real-time feeds (prices, inventory):        10s - 60s
// Never cache:                                Auth tokens (revocability), PII without user consent

// Jitter: add ±10% to TTL to prevent thundering herd on mass expiry
func jitteredTTL(base time.Duration) time.Duration {
    jitter := time.Duration(rand.Int63n(int64(base / 10)))
    if rand.Intn(2) == 0 { return base + jitter }
    return base - jitter
}
```

## 7. Cache Observability

```go
// Track hit rate, miss rate, and errors per cache level
type InstrumentedCache[K comparable, V any] struct {
    inner Cache[K, V]
    name  string
    hits  prometheus.Counter
    misses prometheus.Counter
    errors prometheus.Counter
}

func (c *InstrumentedCache[K, V]) Get(ctx context.Context, key K) (V, bool, error) {
    v, ok, err := c.inner.Get(ctx, key)
    switch {
    case err != nil:  c.errors.Inc()
    case ok:          c.hits.Inc()
    default:          c.misses.Inc()
    }
    return v, ok, err
}
```

## Caching Checklist
- [ ] All cache keys versioned (e.g., `user:v1:id`) — bump version on schema change
- [ ] `singleflight` wraps all cache-miss DB fetches (stampede prevention)
- [ ] TTL set on every entry — never infinite TTL in production
- [ ] TTL jittered ±10% to prevent thundering herd
- [ ] Cache errors are WARN not ERROR — don't fail request on cache miss/error
- [ ] Write-invalidate pattern: delete cache entry AFTER successful DB write
- [ ] Multi-level: L1 (in-process) for hot data, L2 (Redis) for distributed state
- [ ] Hit rate monitored — alert if drops below 80% (indicates cache thrash)
- [ ] Cache warming on startup for predictably hot data sets
- [ ] PII only cached with explicit TTL and user-aware key namespace
