# fanwatch

**Reactive, event-driven fanotify observer for Linux overlay filesystems.**

`fanwatch` watches the merged view of a container snapshot (Docker, containerd,
BuildKit) via Linux [fanotify](https://man7.org/linux/man-pages/man7/fanotify.7.html)
and delivers structured, enriched filesystem events over Go channels.

---

## Features

| | |
|--|--|
| 🔍 **Overlay-aware** | Knows about lowerdirs, upperdir, and the merged view; enriches every event with the originating layer |
| 🔇 **Read-only by default** | Ships a `ReadOnlyFilter` that keeps only ACCESS / OPEN / OPEN_EXEC / CLOSE_NOWRITE events and drops all modifications |
| 🧩 **Composable pipeline** | Filter → Transformer → Handler stages, each independently replaceable |
| 🔭 **OTEL-ready** | Plug-in `OTELMiddleware` for spans, counters, and latency histograms |
| 🧪 **Test-first** | `testutil` package ships `FakeWatcher`, fluent builders, and `CapturingMiddleware` |
| 🐳 **Container-native** | Works inside `--privileged` Docker containers; cgroup-based container ID detection |

---

## Requirements

| | |
|--|--|
| Linux kernel | 5.1+ (fanotify filesystem-level marking) |
| Capability | `CAP_SYS_ADMIN` |
| Go | 1.22+ |

---

## Installation

```bash
go get github.com/bons/bons-ci/pkg/fswatch
```

---

## Quick start

```go
package main

import (
    "context"
    "log/slog"
    "os"

    "github.com/bons/bons-ci/pkg/fswatch"
)

func main() {
    overlay, err := fanwatch.OverlayInfoFromMount("/var/lib/docker/overlay2/abc/merged")
    if err != nil {
        slog.Error("find overlay mount", "err", err)
        os.Exit(1)
    }

    w, err := fanwatch.NewWatcher(
        fanwatch.WithOverlay(overlay),
        fanwatch.WithMask(fanwatch.MaskReadOnly),
    )
    if err != nil {
        slog.Error("create watcher", "err", err)
        os.Exit(1)
    }
    defer w.Close()

    pipeline := fanwatch.NewPipeline(
        fanwatch.WithReadOnlyPipeline(),           // filter: ACCESS/OPEN/EXEC only
        fanwatch.WithFullEnrichment(overlay),       // overlay + process + stat
        fanwatch.WithHandler(fanwatch.LogHandler(
            slog.Default(), slog.LevelInfo,
        )),
    )

    ctx, cancel := context.WithCancel(context.Background())
    defer cancel()

    rawCh, err := w.Watch(ctx)
    if err != nil {
        slog.Error("start watch", "err", err)
        os.Exit(1)
    }

    result := pipeline.RunSync(ctx, rawCh, func(err error) {
        slog.Error("pipeline error", "err", err)
    })

    slog.Info("done",
        "received", result.Received,
        "handled",  result.Handled,
        "filtered", result.Filtered,
    )
}
```

---

## Pipeline model

```
Watcher ──► chan *RawEvent ──► Pipeline
                                  │
                    ┌─────────────┼──────────────┐
                    ▼             ▼               ▼
                 Filter      Transformer       Handler
               (drop/pass)   (enrich e)     (side effects)
                    │             │               │
               ReadOnly?    OverlayEnricher   LogHandler
               PathPrefix?  ProcessEnricher   ChannelHandler
               PIDFilter?   StaticAttrs       CountingHandler
               ExternalFn?  FileStatTransformer  ...
                    │             │               │
                    └─────────────┴───────────────┘
                                  │
                             Middleware
                           (OTEL, Recovery, Logging)
```

---

## Built-in filters

| Filter | What it passes |
|--------|---------------|
| `ReadOnlyFilter()` | ACCESS, OPEN, OPEN_EXEC, CLOSE_NOWRITE |
| `MaskFilter(ops...)` | Events matching any of the given ops |
| `PathPrefixFilter(prefixes...)` | Events inside any of the path prefixes |
| `PathExcludeFilter(prefixes...)` | Events **not** inside the given prefixes |
| `ExtensionFilter(exts...)` | Files with any of the given extensions |
| `PIDFilter(pids...)` | Events from specific PIDs |
| `ExcludePIDFilter(pids...)` | Events **not** from the given PIDs |
| `NoOverflowFilter()` | Drops FAN_Q_OVERFLOW events |
| `UpperDirOnlyFilter()` | Events from the writable upperdir layer |
| `LowerDirOnlyFilter()` | Events from read-only lower layers |
| `AttrFilter(key)` | Events where Attrs[key] exists |
| `AttrValueFilter(key, val)` | Events where Attrs[key] == val |
| `FreshnessFilter(maxAge)` | Drops events older than maxAge |
| `ExternalFilter(fn)` | Delegates to `func(path string) bool` |
| `ExternalContextFilter(fn)` | Delegates to `func(ctx, path) bool` |

Compose with `AllFilters{}`, `AnyFilter{}`, `Not{}`.

---

## Built-in transformers

| Transformer | What it adds |
|-------------|-------------|
| `NewOverlayEnricher(overlay)` | `e.Overlay`, `e.SourceLayer` |
| `ProcessEnricher()` | `e.Process` (comm, exe, cmdline, containerID) |
| `FileStatTransformer()` | `e.FileInfo` (Lstat result) |
| `PathNormaliser(root)` | Cleans `e.Path`, sets `e.Dir`, `e.Name` |
| `StaticAttrTransformer(map)` | Fixed key-value attrs on every event |
| `DynamicAttrTransformer(fn)` | Per-event dynamic attrs |

Compose with `ChainTransformer{}` or `ConditionalTransformer{}`.

---

## Built-in handlers

| Handler | What it does |
|---------|-------------|
| `LogHandler(logger, level)` | Structured slog output per event |
| `WriterHandler(w)` | Plain-text line per event to any `io.Writer` |
| `NoopHandler{}` | Silent discard |
| `CountingHandler` | Atomic event counters by op type |
| `CollectingHandler` | Accumulates events in memory (audit / tests) |
| `NewChannelHandler(buf)` | Forwards cloned events to a channel |
| `ChainHandler{}` | Sequential — stops at first error |
| `MultiHandler{}` | Fan-out — all handlers run even after errors |
| `PredicateHandler{}` | Conditionally delegates to an inner handler |

---

## Middleware

```go
import "github.com/bons/bons-ci/pkg/fswatch/middleware"

otel, _ := middleware.NewOTEL(tracer, meter)
recovery := middleware.NewRecovery(func(rec any, e *fanwatch.EnrichedEvent) {
    slog.Error("handler panicked", "recovered", rec, "path", e.Path)
})
logging := middleware.NewLogging(slog.Default().Error)

pipeline := fanwatch.NewPipeline(
    fanwatch.WithMiddleware(recovery),   // outermost — catches panics first
    fanwatch.WithMiddleware(logging),    // logs handler errors
    fanwatch.WithMiddleware(otel),       // innermost — spans the actual handler
    fanwatch.WithHandler(myHandler),
)
```

---

## Running in Docker

```dockerfile
FROM golang:1.22-alpine AS builder
WORKDIR /app
COPY . .
RUN go build -o fanwatch-demo ./cmd/demo

FROM alpine:3.19
COPY --from=builder /app/fanwatch-demo /fanwatch-demo
ENTRYPOINT ["/fanwatch-demo"]
```

```bash
docker run --privileged \
    -v /var/lib/docker:/var/lib/docker:ro \
    fanwatch-demo \
    -merged /var/lib/docker/overlay2/<snapshot-id>/merged
```

---

## Testing

```bash
# Unit tests (no root, no fanotify)
go test ./...

# Integration tests (requires root + Linux)
go test -tags integration -count=1 ./...
```

### Test helpers

```go
import "github.com/bons/bons-ci/pkg/fswatch/testutil"

// Inject events without touching fanotify
w := testutil.NewFakeWatcher(64)
w.SendMany(testutil.MakeReadOnlyEvents("/merged", 100))
w.Close()

// Build specific events
e := testutil.NewEnrichedEvent().
    WithOp(fanwatch.OpOpenExec).
    WithPath("/merged/usr/bin/python3").
    WithPID(1234).
    Build()

// Assert pipeline output
cap := &testutil.CapturingMiddleware{}
pipeline := fanwatch.NewPipeline(
    fanwatch.WithMiddleware(cap),
    fanwatch.WithHandler(fanwatch.NoopHandler{}),
)
```

---

## Architecture decisions

**Why filter before transform?**  
Enrichment (especially ProcessEnricher) makes syscalls into `/proc`. Filtering
first avoids enriching events that will be discarded.

**Why is Transformer error non-fatal?**  
A partially-enriched event (e.g. process exited before `/proc` read) is often
still valuable to a handler. The caller decides via a downstream filter whether
to drop it.

**Why is Handler error non-fatal?**  
A logging failure or full audit buffer should not halt the entire observation
loop. Errors accumulate in the pipeline result and via the error callback.

**Why not inotify?**  
inotify requires watching every directory recursively and misses events for
files accessed through bind mounts or overlay views. fanotify marks the entire
filesystem/mount and catches all access patterns.

---

## License

MIT
