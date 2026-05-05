# SKILL: fanwatch — reactive fanotify pipeline for overlay filesystems

## Identity

**Package path:** `github.com/bons/fanwatch`
**Language:** Go 1.22+
**Build target:** Linux (fanotify); stubs for macOS/Windows
**Privilege:** CAP_SYS_ADMIN (or `--privileged` container)

---

## What this skill does

Generates and modifies code for the `fanwatch` package. After reading this
file the AI assistant knows:

1. The **full type vocabulary** (RawEvent → Event → EnrichedEvent pipeline)
2. The **extension points** (Filter, Transformer, Handler, Middleware interfaces)
3. The **correct patterns** for each extension type
4. The **test patterns** to use (FakeWatcher, builders, CapturingMiddleware)
5. The **naming rules** and SOLID constraints

---

## Type vocabulary quick reference

```
RawEvent        — from kernel: Mask, PID, Path, Timestamp
Event           — + Dir, Name, FileInfo, WatcherID
EnrichedEvent   — + Overlay *OverlayInfo, SourceLayer *SnapshotLayer,
                      Process *ProcessInfo, Attrs map[string]any

OverlayInfo     — MergedDir, UpperDir, WorkDir, LowerDirs []string,
                      Layers []SnapshotLayer, ID, Labels
SnapshotLayer   — Index, Path, IsUpper, ChainID
ProcessInfo     — PID, Comm, Exe, Cmdline, ContainerID

EventMask       — bitmask of Op values (OpAccess, OpOpen, OpOpenExec, …)
MaskReadOnly    — OpAccess | OpOpen | OpOpenExec | OpCloseNoWrite
MaskAll         — all ops including modifications
```

---

## Interface signatures (memorise these)

```go
// Filter — drop or pass events
type Filter interface {
    Allow(ctx context.Context, e *EnrichedEvent) bool
}

// Transformer — enrich events in place
type Transformer interface {
    Transform(ctx context.Context, e *EnrichedEvent) error
}

// Handler — terminal side effects
type Handler interface {
    Handle(ctx context.Context, e *EnrichedEvent) error
}

// Middleware — wrap Handler for cross-cutting concerns
type Middleware interface {
    Wrap(next Handler) Handler
}

// Watcher — produce raw events
type Watcher interface {
    Watch(ctx context.Context) (<-chan *RawEvent, error)
    Close() error
}
```

---

## Canonical pipeline construction

```go
overlay, _ := fanwatch.OverlayInfoFromMount("/var/lib/docker/overlay2/abc/merged")

w, err := fanwatch.NewWatcher(
    fanwatch.WithMergedDir(overlay.MergedDir),
    fanwatch.WithOverlay(overlay),
    fanwatch.WithMask(fanwatch.MaskReadOnly),
)

pipeline := fanwatch.NewPipeline(
    // Filters (drop events — evaluated in order, short-circuit AND)
    fanwatch.WithFilter(fanwatch.ReadOnlyFilter()),
    fanwatch.WithFilter(fanwatch.NoOverflowFilter()),
    fanwatch.WithFilter(fanwatch.PathExcludeFilter("/proc", "/sys")),

    // Transformers (enrich events — all run in order)
    fanwatch.WithTransformer(fanwatch.NewOverlayEnricher(overlay)),
    fanwatch.WithTransformer(fanwatch.ProcessEnricher()),
    fanwatch.WithTransformer(fanwatch.FileStatTransformer()),
    fanwatch.WithTransformer(fanwatch.StaticAttrTransformer(map[string]any{
        "env": "prod",
    })),

    // Handler (terminal)
    fanwatch.WithHandler(fanwatch.LogHandler(slog.Default(), slog.LevelInfo)),

    // Middleware (wraps handler — OTEL, recovery, etc.)
    fanwatch.WithMiddleware(otelMiddleware),

    // Tuning
    fanwatch.WithWorkers(runtime.NumCPU()),
    fanwatch.WithErrorBufferSize(128),
)

ctx, cancel := context.WithCancel(context.Background())
defer cancel()

rawCh, err := w.Watch(ctx)
result := pipeline.RunSync(ctx, rawCh, func(err error) {
    slog.Error("pipeline error", "err", err)
})
```

---

## Writing a new Filter (template)

```go
// <Name>Filter passes events where <condition>.
// [Precondition: requires <Transformer> to have run — if any]
func <Name>Filter(<params>) Filter {
    // pre-compute / validate here (called once at construction)
    return FilterFunc(func(_ context.Context, e *EnrichedEvent) bool {
        // return true = keep event, false = drop event
        // MUST NOT mutate e
        // MUST be goroutine-safe
    })
}
```

Test template:

```go
func Test<Name>Filter(t *testing.T) {
    f := fanwatch.<Name>Filter(...)
    ctx := context.Background()
    tests := []struct {
        name  string
        event *fanwatch.EnrichedEvent
        want  bool
    }{
        {"passes when <condition>", testutil.NewEnrichedEvent()....Build(), true},
        {"drops when <other>",     testutil.NewEnrichedEvent()....Build(), false},
    }
    for _, tt := range tests {
        t.Run(tt.name, func(t *testing.T) {
            got := f.Allow(ctx, tt.event)
            if got != tt.want {
                t.Errorf("Allow() = %v, want %v", got, tt.want)
            }
        })
    }
}
```

---

## Writing a new Transformer (template)

```go
// <Name>Enricher populates e.<Field> with <description>.
// Returns nil on success; returns an error when <failure-condition> — the
// pipeline continues with a partially-enriched event.
func <Name>Enricher(<params>) Transformer {
    // pre-compute here
    return TransformerFunc(func(ctx context.Context, e *EnrichedEvent) error {
        // mutate e in place
        // MUST be goroutine-safe
        // return nil or a descriptive error
    })
}
```

---

## Writing a new Handler (template)

```go
// <Name>Handler <description>.
type <Name>Handler struct {
    // state — must be goroutine-safe (use sync.Mutex, atomic, or channels)
}

func (h *<Name>Handler) Handle(ctx context.Context, e *EnrichedEvent) error {
    // perform side effect
    // MUST NOT mutate e
    // return nil or a descriptive, non-fatal error
}
```

---

## Writing a new Middleware (template)

```go
// <Name>Middleware <description>.
type <Name>Middleware struct{ /* config */ }

func New<Name>(<params>) *<Name>Middleware { return &<Name>Middleware{...} }

func (m *<Name>Middleware) Wrap(next fanwatch.Handler) fanwatch.Handler {
    return fanwatch.HandlerFunc(func(ctx context.Context, e *fanwatch.EnrichedEvent) error {
        // before
        err := next.Handle(ctx, e)
        // after
        return err
    })
}
```

---

## Test patterns

### Inject events without fanotify

```go
w := testutil.NewFakeWatcher(32)
w.SendMany(testutil.MakeReadOnlyEvents("/merged", 100))
w.Close()

ch, _ := w.Watch(context.Background())
result := pipeline.RunSync(ctx, ch, nil)
```

### Assert enriched events

```go
cap := &testutil.CapturingMiddleware{}

pipeline := fanwatch.NewPipeline(
    fanwatch.WithMiddleware(cap),
    fanwatch.WithHandler(fanwatch.NoopHandler{}),
)
// run pipeline...

events := cap.Events()
// assert on events[N].Overlay, events[N].SourceLayer, events[N].Attr("key")
```

### Wait for async delivery

```go
ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
defer cancel()

if !cap.WaitForN(ctx, 10) {
    t.Error("timeout waiting for 10 events")
}
```

### Build specific events

```go
e := testutil.NewEnrichedEvent().
    WithOp(fanwatch.OpOpenExec).
    WithPath("/merged/usr/bin/python3").
    WithPID(9999).
    WithOverlay(overlay).
    WithSourceLayer(testutil.LowerLayerFixture(1, "/lower1")).
    WithProcess(testutil.ProcessInfoFixture(9999, "python3", "/usr/bin/python3")).
    WithAttr("custom.key", "value").
    Build()
```

---

## Naming conventions

| Thing | Pattern | Example |
|-------|---------|---------|
| Filter returning a Filter | `<Adjective>Filter` | `ReadOnlyFilter`, `UpperDirOnlyFilter` |
| Filter excluding something | `Exclude<Noun>Filter` | `ExcludePIDFilter`, `PathExcludeFilter` |
| Transformer adding fields | `<Noun>Enricher` | `OverlayEnricher`, `ProcessEnricher` |
| Transformer setting attrs | `<Scope>AttrTransformer` | `StaticAttrTransformer`, `DynamicAttrTransformer` |
| Handler writing output | `<Dest>Handler` | `LogHandler`, `WriterHandler`, `ChannelHandler` |
| Handler accumulating | `<Verb>ingHandler` | `CollectingHandler`, `CountingHandler` |
| Middleware | `<Concern>Middleware` | `OTELMiddleware`, `RecoveryMiddleware` |
| Option functions | `With<Thing>` | `WithFilter`, `WithMergedDir`, `WithWorkers` |

---

## Common mistakes to avoid

| Mistake | Correction |
|---------|------------|
| Calling `os.Lstat` in a Filter | Move the stat to `FileStatTransformer` |
| Using `log.Printf` in library code | Accept `*slog.Logger` as parameter |
| Adding fields to `RawEvent` | Add to `EnrichedEvent.Attrs` via Transformer |
| Holding `*EnrichedEvent` without Clone | Call `e.Clone()` before storing |
| Importing `unix` outside sys/ or watcher_linux.go | Use `Watcher` interface |
| Blocking in Filter/Transformer | These run synchronously — keep them fast |
| Writing multiple filters in one function | One filter per function |
| Using `panic` in error paths | Return the error |
| Checking `e.Overlay == nil` in OverlayEnricher | Guard in the caller's filter |

---

## OTEL attribute namespace

All OTEL spans and metrics use the `fanwatch.*` attribute namespace:

```
fanwatch.event.mask       string   "ACCESS|OPEN"
fanwatch.event.pid        int      1234
fanwatch.event.path       string   "/merged/app/main.go"
fanwatch.layer.index      int      0
fanwatch.layer.upper      bool     true
fanwatch.process.comm     string   "python3"
fanwatch.watcher.id       string   "container-abc123"
```

Do not invent new attribute names — extend this list in `middleware/otel.go`
and document here.
