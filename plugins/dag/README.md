# reactdag

A production-ready, reactive, event-driven DAG build engine in pure Go (stdlib only).

Designed after BuildKit LLB: each vertex in the DAG represents a unit of work whose inputs are
the filesystem outputs of its parent vertices. The engine tracks which files each vertex actually
reads (via an external `FileTracker` such as fanotify + blake3), stores results in a two-tier
cache, and computes the minimal set of vertices that must re-execute when files change.

---

## Architecture

```
┌────────────────────────────────────────────────────────────┐
│                        DAGBuilder                          │  ← fluent construction
├────────────────────────────────────────────────────────────┤
│  DAG  (vertices + edges + fine-grained FileDependency)     │  ← sealed graph
├──────────────────┬─────────────────────────────────────────┤
│  InvalidationEngine                                        │  ← O(V+E) file-aware reset
├──────────────────┴─────────────────────────────────────────┤
│                      Scheduler                             │  ← reactive build engine
│  ┌────────────┐  ┌──────────┐  ┌──────────────────────┐   │
│  │ HookRegistry│  │ EventBus │  │ CacheKeyComputer      │   │
│  └────────────┘  └──────────┘  └──────────────────────┘   │
│  ┌──────────────────────────────────────────────────────┐  │
│  │  Executor  (default / RetryExecutor / ResourceLimiter│  │
│  └──────────────────────────────────────────────────────┘  │
│  ┌──────────────────┐  ┌─────────────────────────────────┐ │
│  │ Fast CacheStore  │  │ Slow CacheStore                  │ │
│  │ (MemoryCacheStore│  │ (DiskCacheStore / custom remote) │ │
│  └──────────────────┘  └─────────────────────────────────┘ │
├────────────────────────────────────────────────────────────┤
│  Planner (dry-run)  │  DOT exporter  │  MetricsReporter    │
└────────────────────────────────────────────────────────────┘
```

---

## Core concepts

### Vertex states

```
initial → fast_cache ─┐
        → slow_cache ─┼→ completed
        → completed   │→ failed
        → failed      │
```

`Reset()` returns any vertex to `initial`, allowing incremental rebuilds.

### Fine-grained file dependencies

Declare which exact paths a vertex reads from each parent:

```go
builder.Add("link", linkOp,
    dag.DependsOn("compile"),
    dag.ConsumesFiles("compile", "/out/foo.o", "/out/bar.o"),
)
```

If `baz.o` changes in `compile`'s output but `link` only consumes `foo.o` and `bar.o`,
`link` is **not** invalidated — it does not need to re-execute.

### Cached error replay

When a vertex fails, its error is stored in the cache alongside the input hash. On the next
build, if the inputs are unchanged:

1. The cache key resolves to the failed entry.
2. The error is returned immediately — **no recompute of any ancestor**.
3. Downstream vertices are failed instantly.

This produces the fastest-possible failure response for repeated builds with unchanged inputs.

---

## Quick start

```go
package main

import (
    "context"
    "log"
    "os"

    dag "github.com/example/reactdag"
)

func main() {
    // 1. Build the DAG.
    d, err := dag.NewBuilder().
        Add("fetch",   fetchOp{}).
        Add("compile", compileOp{}, dag.DependsOn("fetch"),
            dag.ConsumesFiles("fetch", "/src/main.go", "/src/lib.go")).
        Add("test",    testOp{},    dag.DependsOn("compile")).
        Add("package", packageOp{}, dag.DependsOn("compile")).
        Add("publish", publishOp{},
            dag.DependsOn("test", "package"),
            dag.WithTimeout(2 * time.Minute),
            dag.WithRetry(dag.RetryPolicy{MaxAttempts: 3})).
        Build()
    if err != nil {
        log.Fatal(err)
    }

    // 2. Wire the scheduler.
    fastCache := dag.NewMemoryCacheStore(4096)
    diskCache, _ := dag.NewDiskCacheStore("/var/cache/reactdag")

    sched := dag.NewScheduler(d,
        dag.WithWorkerCount(8),
        dag.WithFastCache(fastCache),
        dag.WithSlowCache(diskCache),
        dag.WithExecutor(dag.NewRetryExecutor(
            dag.NewDefaultExecutorForTest(),
            30*time.Second,
            dag.RetryPolicy{MaxAttempts: 2},
        )),
    )

    // 3. Subscribe to events.
    sched.EventBus().Subscribe(dag.EventStateChanged, func(ctx context.Context, e dag.Event) {
        log.Printf("[%s] %s → %s", e.VertexID, e.Payload["from"], e.Payload["to"])
    })

    // 4. Register a hook.
    sched.Hooks().Register(dag.HookBeforeExecute, 0, func(ctx context.Context, v *dag.Vertex, _ dag.HookPayload) error {
        log.Printf("starting %s", v.ID())
        return nil
    })

    // 5. Dry-run first.
    planner := dag.NewPlanner(d, fastCache, diskCache, nil)
    plan, _ := planner.Plan(context.Background(), "publish", nil)
    log.Print(dag.RenderPlan(plan))

    // 6. Build.
    changedFiles := []dag.FileRef{{Path: "/src/main.go"}} // from fanotify+blake3
    metrics, err := sched.Build(context.Background(), "publish", changedFiles)
    if err != nil {
        log.Printf("build failed: %v", err)
    }

    // 7. Report.
    dag.WriteReport(os.Stdout, d, metrics, dag.DefaultReportOptions())

    // 8. Visualise.
    dot := dag.ExportDOT(d, dag.DOTOptions{
        Title:       "CI Pipeline",
        ShowState:   true,
        ShowMetrics: true,
        ShowFileDeps: true,
    })
    os.WriteFile("pipeline.dot", []byte(dot), 0o644)
    // dot -Tsvg pipeline.dot -o pipeline.svg
}
```

---

## File inventory

| File | Responsibility |
|---|---|
| `types.go` | `State`, `FileRef`, `FileDependency`, `CacheKey`, `BuildMetrics`, `Event` |
| `interfaces.go` | `Operation`, `Executor`, `CacheStore`, `FileTracker`, `CacheKeyComputer`, `HookFn`, `EventHandler` |
| `vertex.go` | Thread-safe `Vertex` state machine + metrics + file I/O |
| `dag.go` | `DAG`: construction, Kahn toposort, cycle detection, ancestor/descendant queries, critical path |
| `event.go` | `EventBus`: typed, concurrent publish-subscribe |
| `hooks.go` | `HookRegistry`: priority-ordered, deregisterable lifecycle hooks |
| `cache.go` | `CacheEntry`, `MemoryCacheStore` (LRU-evictable), `NoopCacheStore` |
| `store.go` | `ChainedCacheStore` (two-tier), `DiskCacheStore` (JSON, atomic writes, Prune) |
| `keycomputer.go` | `DefaultKeyComputer` (SHA-256; slot for blake3) |
| `invalidation.go` | `InvalidationEngine`: O(V+E) file-change-aware cache invalidation |
| `executor.go` | `defaultExecutor` + `NewDefaultExecutorForTest` |
| `retry.go` | `RetryExecutor` (retry+backoff+timeout), `ResourceLimiter` (semaphore per class) |
| `builder.go` | `DAGBuilder` fluent API, `RetryPolicy`, `VertexOption` |
| `scheduler.go` | `Scheduler`: reactive build engine, full-control API |
| `telemetry.go` | OTel-shaped `Tracer`/`Counter`/`Histogram` interfaces + no-op defaults |
| `plan.go` | `Planner` (dry-run), `BuildPlan`, `RenderPlan` |
| `dot.go` | `ExportDOT`: Graphviz visualisation with state colours |
| `report.go` | `WriteReport`: structured ASCII build report |
| `workgroup.go` | Stdlib `errgroup` equivalent (zero external deps) |
| `options.go` | Functional options: `WithWorkerCount`, `WithFastCache`, `WithSlowCache`, … |

---

## Scheduler optimisations

| Optimisation | Mechanism |
|---|---|
| Fine-grained invalidation | `FileDependency` per (child, parent) pair; only vertices consuming changed paths are reset |
| Two-tier cache | Fast (memory) → slow (disk/remote) lookup before execution; back-fill on slow hit |
| Cached-error replay | Failed result cached with input hash; error returned instantly on unchanged inputs |
| Target short-circuit | Terminal target → ancestors marked completed, zero executions |
| Reactive dispatch | Workers notified the moment all their parents are terminal (`allParentsTerminal`) |
| Failure propagation | Pending descendants failed synchronously; in-flight results drained correctly |
| Critical path DP | O(V+E) DP over topo-sorted graph gives the longest dependency chain |

---

## Extension points

### Custom FileTracker (fanotify + blake3)

```go
type myTracker struct{}
func (t *myTracker) StartTracking(pid int) error     { ... }
func (t *myTracker) StopTracking() ([]dag.FileRef, error) { ... }

sched := dag.NewScheduler(d,
    dag.WithFileTracker(&myTracker{}),
)
```

### Custom CacheStore (Redis, S3, …)

```go
type redisCache struct{ client *redis.Client }
func (r *redisCache) Get(ctx context.Context, key dag.CacheKey) (*dag.CacheEntry, error) { ... }
func (r *redisCache) Set(ctx context.Context, key dag.CacheKey, e *dag.CacheEntry) error { ... }
func (r *redisCache) Delete(ctx context.Context, key dag.CacheKey) error                 { ... }
func (r *redisCache) Exists(ctx context.Context, key dag.CacheKey) (bool, error)         { ... }

sched := dag.NewScheduler(d, dag.WithSlowCache(&redisCache{...}))
```

### OTel instrumentation

```go
// Satisfy dag.Tracer / dag.Counter / dag.Histogram with your real OTel instruments:
tel := dag.NewTelemetry(myTracer, myVertexHist, myBuildHist,
    myCacheHitCounter, myCacheMissCounter,
    myFilesReadCounter, myFilesWrittenCounter, myInvalidationCounter)

// Attach to the event bus:
sched.EventBus().SubscribeAll(func(ctx context.Context, e dag.Event) {
    if e.Type == dag.EventExecutionStart {
        ctx, span = tel.StartVertexSpan(ctx, e.VertexID...)
    }
})
```

### Resource-limited executor (GPU, network, …)

```go
exec := dag.NewResourceLimiter(
    dag.NewDefaultExecutorForTest(),
    map[string]int{
        "gpu":     2,   // max 2 GPU-bound vertices at once
        "network": 10,  // max 10 network-bound vertices at once
    },
)
sched := dag.NewScheduler(d, dag.WithExecutor(exec))

// Tag vertices at build time:
builder.Add("train", trainOp{}, dag.WithLabel("resource_class", "gpu"))
```

---

## Running tests

```bash
go test ./... -race -timeout 60s
go test ./... -bench=. -benchmem -benchtime=2s
```

---

## Zero external dependencies

The package uses only the Go standard library. The OTel-shaped interfaces
(`Tracer`, `Counter`, `Histogram`) are defined in-package so production code
can satisfy them with real `go.opentelemetry.io/otel` types, which implement
identical signatures.
