---
name: pkg-ristretto
description: >
  Exhaustive reference for dgraph-io/ristretto: in-process high-performance cache with
  admission control, cost-based eviction, and metrics. Use for hot in-process caching
  where Redis round-trip latency is too high. Cross-references: performance/SKILL.md, dsa/SKILL.md.
---

# Package: dgraph-io/ristretto — Complete Reference

## Import
```go
import "github.com/dgraph-io/ristretto"
```

## 1. Cache Setup

```go
func NewCache[K comparable, V any](maxCost int64) (*ristretto.Cache[K, V], error) {
    cache, err := ristretto.NewCache[K, V](&ristretto.Config[K, V]{
        NumCounters: maxCost * 10,  // 10x max items for frequency tracking
        MaxCost:     maxCost,       // total cost budget (bytes or item count)
        BufferItems: 64,            // goroutines that can write simultaneously
        Metrics:     true,          // enable Hit/Miss/Cost metrics
        OnEvict: func(item *ristretto.Item[V]) {
            // Called on eviction — use for cleanup (closing resources etc.)
        },
        Cost: func(value V) int64 {
            // Return cost per item (e.g., size in bytes)
            // Return 1 for simple item-count-based eviction
            return 1
        },
    })
    if err != nil { return nil, fmt.Errorf("ristretto.NewCache: %w", err) }
    return cache, nil
}

// Typed wrapper with TTL and metrics
type TypedCache[K comparable, V any] struct {
    inner   *ristretto.Cache[K, V]
    metrics CacheMetrics
}

func (c *TypedCache[K, V]) Get(key K) (V, bool) {
    v, ok := c.inner.Get(key)
    if ok {
        c.metrics.Hit()
    } else {
        c.metrics.Miss()
    }
    return v, ok
}

func (c *TypedCache[K, V]) Set(key K, value V, ttl time.Duration) bool {
    return c.inner.SetWithTTL(key, value, 1, ttl)
}

func (c *TypedCache[K, V]) Delete(key K) {
    c.inner.Del(key)
}
```

## 2. Cache-Aside Pattern

```go
type CachedUserRepo struct {
    repo  UserRepository   // underlying DB repo
    cache *TypedCache[string, *User]
    ttl   time.Duration
}

func (r *CachedUserRepo) FindByID(ctx context.Context, id string) (*User, error) {
    // 1. Check cache
    if u, ok := r.cache.Get(id); ok {
        return u, nil
    }

    // 2. Cache miss — fetch from DB
    u, err := r.repo.FindByID(ctx, id)
    if err != nil { return nil, err }

    // 3. Populate cache (async — ristretto buffers writes)
    if !r.cache.Set(id, u, r.ttl) {
        slog.WarnContext(ctx, "ristretto: item dropped (cost too high)", "id", id)
    }
    // Wait for Set to propagate (ristretto processes sets asynchronously)
    r.cache.inner.Wait()

    return u, nil
}

// Invalidate on write-through
func (r *CachedUserRepo) Save(ctx context.Context, u *User) error {
    if err := r.repo.Save(ctx, u); err != nil { return err }
    r.cache.Delete(u.ID) // invalidate stale entry
    return nil
}
```

## 3. Cost-Based Eviction (Size-Aware Cache)

```go
// When caching variable-size objects (byte slices, large structs)
// set MaxCost = total bytes budget and Cost = size of each item

var jsonCache, _ = ristretto.NewCache[string, []byte](&ristretto.Config[string, []byte]{
    NumCounters: 1e7,       // 10M counters for 1M items
    MaxCost:     100 << 20, // 100 MB total cache size
    BufferItems: 64,
    Cost: func(value []byte) int64 {
        return int64(len(value)) // cost = byte size
    },
})

// Set with cost derived from value automatically via Cost func
jsonCache.Set("key", largeJSON, 0) // 0 = use Cost function
```

## 4. Metrics

```go
// After enabling Metrics: true
m := cache.Metrics
fmt.Printf("hits: %d, misses: %d, ratio: %.2f\n",
    m.Hits(), m.Misses(), m.Ratio())
fmt.Printf("cost added: %d, evicted: %d\n",
    m.CostAdded(), m.CostEvicted())

// Expose as Prometheus gauges
prometheus.NewGaugeFunc(prometheus.GaugeOpts{
    Name: "cache_hit_ratio",
}, func() float64 { return cache.Metrics.Ratio() })
```

## 5. Important Behaviors

```go
// SET IS ASYNCHRONOUS — item may not be immediately visible after Set()
// For tests or when you need immediate reads after Set():
cache.Set("key", value, 1, ttl)
cache.Wait()  // blocks until all pending sets are processed
v, ok := cache.Get("key")  // now safe

// DROPPED SETS — Set returns false if item dropped (cost > max or admission control)
if !cache.Set("key", value, cost, ttl) {
    slog.Warn("cache item dropped — consider reducing cost or increasing MaxCost")
}

// TTL = 0 means no expiration (item lives until evicted by cost pressure)
cache.Set("permanent", value, cost, 0)
```

## ristretto Checklist
- [ ] `NumCounters` = 10× expected max items (frequency sketch accuracy)
- [ ] `MaxCost` set to total memory budget (not item count unless cost=1)
- [ ] `Cost` function defined for variable-size values (returns byte size)
- [ ] `cache.Wait()` called in tests after `Set` — not in production hot path
- [ ] `Set` return value checked — false means item dropped by admission control
- [ ] Cache invalidated on every write (`Delete` in Save/Update/Delete)
- [ ] TTL set on all items — never cache without expiry in production
- [ ] `Metrics: true` enabled, metrics exported to Prometheus
- [ ] Thread-safe by design — no external locking needed
- [ ] `cache.Close()` called on shutdown to release background goroutines
