---
name: pkg-errgroup
description: >
  Exhaustive reference for golang.org/x/sync/errgroup: parallel execution with error collection,
  bounded concurrency, context propagation, and comparison with sync.WaitGroup. Primary pattern
  for all parallel Go work. Cross-references: concurrency/SKILL.md, packages/context/SKILL.md.
---

# Package: golang.org/x/sync/errgroup — Complete Reference

## Import
```go
import "golang.org/x/sync/errgroup"
```

## 1. Basic Usage

```go
// Run N goroutines; return first non-nil error; cancel all on first error
g, ctx := errgroup.WithContext(parentCtx)

g.Go(func() error {
    return fetchUserData(ctx, userID)
})
g.Go(func() error {
    return fetchOrderHistory(ctx, userID)
})
g.Go(func() error {
    return fetchPaymentMethods(ctx, userID)
})

// Wait blocks until all goroutines complete
// Returns first non-nil error; ctx is cancelled on first error
if err := g.Wait(); err != nil {
    return nil, fmt.Errorf("parallel fetch: %w", err)
}
```

## 2. Collecting Results Safely

```go
// Pattern: pre-allocate results slice; each goroutine writes to its own index
func FetchBatch(ctx context.Context, ids []string) ([]*User, error) {
    users := make([]*User, len(ids))  // pre-allocated — no mutex needed

    g, ctx := errgroup.WithContext(ctx)
    for i, id := range ids {
        i, id := i, id  // capture (required for Go < 1.22)
        g.Go(func() error {
            u, err := fetchUser(ctx, id)
            if err != nil {
                return fmt.Errorf("fetchUser(%s): %w", id, err)
            }
            users[i] = u  // safe: each goroutine writes unique index
            return nil
        })
    }

    if err := g.Wait(); err != nil {
        return nil, err
    }
    return users, nil
}
```

## 3. Bounded Concurrency (SetLimit)

```go
// SetLimit: cap concurrent goroutines — prevents resource exhaustion
func ProcessAllItems(ctx context.Context, items []Item) error {
    g, ctx := errgroup.WithContext(ctx)
    g.SetLimit(10)  // max 10 concurrent goroutines

    for _, item := range items {
        item := item
        g.Go(func() error {  // blocks if 10 already running — automatic backpressure
            return processItem(ctx, item)
        })
    }
    return g.Wait()
}
```

## 4. TryGo (Non-blocking Submit)

```go
// TryGo: submit only if under limit; returns false if at limit
g.SetLimit(5)
for _, item := range items {
    item := item
    if !g.TryGo(func() error { return processItem(ctx, item) }) {
        // At capacity — handle: skip, queue, or wait
        pendingQueue = append(pendingQueue, item)
    }
}
```

## 5. errgroup vs WaitGroup

```go
// USE errgroup when:
//   - Goroutines return errors
//   - You want cancellation on first error
//   - You want bounded concurrency (SetLimit)

// USE sync.WaitGroup when:
//   - Goroutines don't return errors
//   - You need to wait for goroutines you don't control
//   - You need fine-grained Done() calls (not just g.Go)

// errgroup DOES NOT collect ALL errors — only first
// For ALL errors: use custom solution
type multiErr struct {
    mu   sync.Mutex
    errs []error
}
func (m *multiErr) Add(err error) {
    if err == nil { return }
    m.mu.Lock(); m.errs = append(m.errs, err); m.mu.Unlock()
}
func (m *multiErr) Err() error { return errors.Join(m.errs...) }

func ProcessAllWithAllErrors(ctx context.Context, items []Item) error {
    var me multiErr
    var wg sync.WaitGroup
    for _, item := range items {
        item := item
        wg.Add(1)
        go func() {
            defer wg.Done()
            me.Add(processItem(ctx, item))
        }()
    }
    wg.Wait()
    return me.Err()
}
```

## 6. Pipeline with errgroup

```go
// Stage 1 → Stage 2 pipeline; error in either stage cancels both
func runPipeline(ctx context.Context, input []string) ([]Result, error) {
    // Buffer between stages
    stage1Out := make(chan Intermediate, len(input))
    results := make([]Result, 0, len(input))
    var mu sync.Mutex

    g, ctx := errgroup.WithContext(ctx)

    // Stage 1: parse + transform
    g.Go(func() error {
        defer close(stage1Out)
        for _, s := range input {
            v, err := parse(s)
            if err != nil { return fmt.Errorf("parse(%q): %w", s, err) }
            select {
            case stage1Out <- v:
            case <-ctx.Done(): return ctx.Err()
            }
        }
        return nil
    })

    // Stage 2: enrich + collect
    g.Go(func() error {
        for v := range stage1Out {
            r, err := enrich(ctx, v)
            if err != nil { return fmt.Errorf("enrich: %w", err) }
            mu.Lock(); results = append(results, r); mu.Unlock()
        }
        return nil
    })

    if err := g.Wait(); err != nil { return nil, err }
    return results, nil
}
```

## errgroup Checklist
- [ ] `errgroup.WithContext` used (not `errgroup.Group{}` directly) for cancellation
- [ ] Loop variable captured (`i, id := i, id`) for Go < 1.22
- [ ] `SetLimit` used for large-scale parallel work (prevent goroutine explosion)
- [ ] Result collection uses pre-indexed slice (no mutex) or channel
- [ ] `g.Wait()` always called — even if no goroutines launched
- [ ] Error from `g.Wait()` wrapped with context before returning
