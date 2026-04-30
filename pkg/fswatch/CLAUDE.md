# CLAUDE.md — fanwatch codegen guidance

> This file is read by AI code-generation tools (Claude, Copilot, etc.) to
> understand the expectations, constraints, and architecture of the `fanwatch`
> package before generating or modifying code. Reading this file **first** is
> mandatory before any code change.

---

## Package purpose

`fanwatch` is a **production-ready, reactive, event-driven fanotify observer**
for Linux overlay filesystems. It watches the merged view of a container
snapshot and delivers enriched, filtered filesystem events over Go channels.

It is **not** a generic inotify/fanotify wrapper — it is opinionated about
overlay structure and ships first-class support for:

- Overlay layer awareness (lowerdir stack + upperdir + merged view)
- Read-only vs modification event classification
- Composable filter → transformer → handler pipelines
- OpenTelemetry tracing and metrics via optional middleware
- Full test doubles (fake watcher, fixture builders) in `testutil/`

---

## Architecture — the pipeline model

```
Watcher (fanotify kernel events)
    │
    ▼  chan *RawEvent
Pipeline
    │
    ├── Filter stage   — drop unwanted events (AND chain, short-circuit)
    │
    ├── Transform stage — enrich events in place (sequential chain)
    │       OverlayEnricher   → sets Event.Overlay, Event.SourceLayer
    │       ProcessEnricher   → sets Event.Process
    │       FileStatTransformer → sets Event.FileInfo
    │       StaticAttrTransformer / DynamicAttrTransformer → sets Event.Attrs
    │
    └── Handler stage  — side effects (log, alert, push to channel, count)
            wrapped by Middleware (OTEL, recovery, logging)
```

**All stages communicate via typed channels or function calls — no shared
mutable state between stages.**

---

## Key types (never break these contracts)

| Type | File | Role |
|------|------|------|
| `RawEvent` | `event.go` | Kernel event, path-resolved, timestamp-stamped |
| `Event` | `event.go` | RawEvent + Dir/Name split + FileInfo |
| `EnrichedEvent` | `event.go` | Event + Overlay + Process + Attrs |
| `OverlayInfo` | `overlay.go` | Describes one overlay mount (lowerdirs, upper, merged) |
| `SnapshotLayer` | `overlay.go` | One layer in the stack |
| `ProcessInfo` | `event.go` | /proc-derived process metadata |
| `Filter` | `filter.go` | `Allow(ctx, *EnrichedEvent) bool` |
| `Transformer` | `transform.go` | `Transform(ctx, *EnrichedEvent) error` |
| `Handler` | `handler.go` | `Handle(ctx, *EnrichedEvent) error` |
| `Middleware` | `pipeline.go` | `Wrap(Handler) Handler` |
| `Pipeline` | `pipeline.go` | Connects watcher → filters → transformers → handler |
| `Watcher` | `watcher.go` | Interface; Linux impl in `watcher_linux.go` |

---

## Coding conventions

### SOLID
- **S** — each type/file has a single responsibility (filter.go = only filtering)
- **O** — extend via new Filter/Transformer/Handler implementations, not by
          modifying existing ones
- **L** — all Filter/Transformer/Handler implementations are substitutable
- **I** — small focused interfaces (Filter, Transformer, Handler are 1-method each)
- **D** — pipeline depends on interfaces, not concrete types

### Clean Code rules enforced here
1. **Functions do one thing** — if a function is doing two things, split it.
2. **Names describe intent** — `ReadOnlyFilter`, not `Filter1`; `OverlayEnricher`,
   not `Enricher`.
3. **No boolean parameters** — use functional options or named types.
4. **No magic numbers** — constants in `event.go` match `<linux/fanotify.h>` exactly.
5. **Errors are values** — every error is either sentinel (`errors.go`) or wrapped
   with context. Never `panic` in production paths.
6. **Tests are first-class** — every public function has a test. Use `testutil/`
   helpers; never reach into `/proc` in unit tests.

### File layout — one concern per file

```
fanwatch/
├── doc.go               package-level godoc
├── event.go             RawEvent, Event, EnrichedEvent, ProcessInfo, EventMask, Op
├── errors.go            sentinel errors
├── overlay.go           OverlayInfo, SnapshotLayer, mountinfo parsing
├── filter.go            Filter interface + all built-in filters
├── transform.go         Transformer interface + all built-in transformers
├── handler.go           Handler interface + all built-in handlers
├── pipeline.go          Pipeline, Middleware, PipelineResult
├── options.go           PipelineOption, WatcherOption (functional options)
├── watcher.go           Watcher interface + NewWatcher factory
├── watcher_linux.go     fanotifyWatcher (build tag: linux)
├── watcher_other.go     stubWatcher (build tag: !linux)
├── export_test.go       test-only exports (overlayInfoFromMountFile)
├── fanwatch_test.go     unit + pipeline integration tests
├── overlay_test.go      mountinfo parsing tests
├── example_test.go      godoc example functions
├── internal/
│   └── sys/
│       ├── fanotify_linux.go   raw syscall layer (linux)
│       └── fanotify_other.go   stubs (non-linux)
├── middleware/
│   ├── otel.go          OTELMiddleware, LoggingMiddleware, RecoveryMiddleware
│   └── middleware_test.go
└── testutil/
    └── testutil.go      FakeWatcher, builders, OverlayFixture, CapturingMiddleware
```

---

## Adding a new Filter

1. Add a function returning `Filter` (or `FilterFunc`) in `filter.go`.
2. Name it `<What>Filter` (e.g. `ContainerIDFilter`).
3. It must not mutate the event.
4. It must be safe for concurrent use.
5. Add a `Test<What>Filter` in `fanwatch_test.go`.
6. Document it with a godoc comment explaining what it passes and what it drops.

```go
// ContainerIDFilter passes only events from the given container.
// Requires that [ProcessEnricher] has run (e.Process.ContainerID must be set).
func ContainerIDFilter(id string) Filter {
    return FilterFunc(func(_ context.Context, e *EnrichedEvent) bool {
        return e.Process != nil && e.Process.ContainerID == id
    })
}
```

---

## Adding a new Transformer

1. Add a function returning `Transformer` (or `TransformerFunc`) in `transform.go`.
2. Name it `<What>Enricher` or `<What>Transformer`.
3. Mutate `e` in place — never replace the pointer.
4. Return an error on partial failure; the pipeline continues regardless.
5. Must be safe for concurrent use.
6. Add a `Test<What>Transformer` in `fanwatch_test.go`.

```go
// ImageNameTransformer adds the container image name to e.Attrs["image.name"]
// by looking up the overlay ID in the provided registry.
func ImageNameTransformer(registry map[string]string) Transformer {
    return TransformerFunc(func(_ context.Context, e *EnrichedEvent) error {
        if e.Overlay == nil {
            return nil
        }
        if name, ok := registry[e.Overlay.ID]; ok {
            e.SetAttr("image.name", name)
        }
        return nil
    })
}
```

---

## Adding a new Handler

1. Add in `handler.go` (simple) or a new file (complex).
2. Name it `<What>Handler`.
3. Must not mutate the event.
4. Must be safe for concurrent use.
5. Returning an error is non-fatal — the pipeline records it and continues.

---

## Adding a new Middleware

1. Implement `Middleware` (single method: `Wrap(Handler) Handler`) in `middleware/`.
2. The wrapper must call `next.Handle(ctx, e)` — no event skipping in middleware.
3. Middleware is for **cross-cutting concerns** (tracing, metrics, recovery).
   Dropping events belongs in a **Filter**, not a middleware.

---

## Platform constraints

- `watcher_linux.go` and `internal/sys/fanotify_linux.go` use build tag `//go:build linux`.
- `watcher_other.go` and `internal/sys/fanotify_other.go` use `//go:build !linux`.
- **Never** put `unix.*` calls outside of `internal/sys/` or `watcher_linux.go`.
- The package must compile on macOS and Windows (tests skip fanotify, not the package).

---

## Test rules

- **Unit tests** in `fanwatch_test.go`: use `testutil.FakeWatcher`, builders, and
  `testutil.MakeReadOnlyEvents`. Never touch `/proc`, real files, or fanotify.
- **Integration tests** (tagged `//go:build integration`): use `OverlayFixture` and
  a real `Watcher`. Must run as root in a Docker container.
- Every new public symbol needs at least one test.
- Table-driven tests preferred for filters (one row per op or path pattern).
- `testutil.CapturingMiddleware` + `WaitForN` for async pipeline assertions.

---

## OTEL integration

- Tracing and metrics live exclusively in `middleware/otel.go`.
- Core pipeline never imports `go.opentelemetry.io/otel` directly.
- The `OTELMiddleware` wraps the terminal `Handler` — it does not wrap filters
  or transformers (those are synchronous and fast).
- Attribute names follow the `fanwatch.*` namespace:
  `fanwatch.event.mask`, `fanwatch.event.pid`, `fanwatch.event.path`,
  `fanwatch.layer.index`, `fanwatch.layer.upper`, `fanwatch.process.comm`.

---

## Do not do these things

| ❌ Bad | ✅ Good |
|--------|---------|
| `if err != nil { panic(err) }` in production paths | Return the error |
| `log.Printf(...)` in the library | Accept a `*slog.Logger` or use `Handler` |
| Storing `*EnrichedEvent` without calling `.Clone()` | Call `.Clone()` first |
| Calling `os.Lstat` inside a `Filter` | Do it in a `Transformer` |
| Adding fields to `RawEvent` | Add them in `EnrichedEvent` via a `Transformer` |
| Importing `internal/sys` from outside the package | Use the `Watcher` interface |
| Sharing mutable state between concurrent handlers | Each handler owns its state |
| Blocking in a `Filter` or `Transformer` | Filters/transformers must be fast |

---

## go.mod dependencies

Direct runtime dependencies only (keep the list short):

| Module | Reason |
|--------|--------|
| `golang.org/x/sys` | fanotify syscall bindings (`unix.FanotifyInit`, etc.) |
| `go.opentelemetry.io/otel` | OTEL API types (middleware package only) |

Test dependencies (`testutil/`, `*_test.go`):

| Module | Reason |
|--------|--------|
| `go.opentelemetry.io/otel/sdk` | noopTracerProvider in middleware tests |

---

## Versioning and stability

- All types in the top-level `fanwatch` package are **stable** (semver 1.x).
- Types in `internal/sys` are **unstable** — not part of the public API.
- Types in `testutil/` are stable for test code but must not be imported from
  non-test files.
- `middleware/` is stable but optional (separate import path).
