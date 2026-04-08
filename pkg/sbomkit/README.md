# sbomkit

Production-grade, event-driven SBOM generation for Go.

Built on [Syft v1](https://github.com/anchore/syft), with a clean hexagonal
architecture, typed event bus, composable pipeline middleware, and a one-call
client API.

---

## Architecture

```
┌─────────────────────────────────────────────────────────────────┐
│  client package  (high-level, source-typed API)                 │
│  GenerateFromImage · GenerateFromSnapshot · GenerateFromArchive │
│  GenerateFromDirectory · GenerateFromOCILayout                  │
└────────────────────┬────────────────────────────────────────────┘
                     │ composes
┌────────────────────▼────────────────────────────────────────────┐
│  core.Engine  (composition root)                                │
│  • Resolver registry   (routes by SourceKind)                   │
│  • Pipeline            (ordered processor middleware)           │
│  • event.Bus           (topic-scoped, sync + async delivery)    │
│  • Exporter registry   (routes by Format)                       │
└──┬──────────┬─────────┬──────────────────────────┬─────────────┘
   │          │         │                          │
   ▼          ▼         ▼                          ▼
Resolvers  Pipeline  Scanner port            Exporters
 Image      processors  │                    CycloneDX JSON/XML
 Filesystem  Logging    ▼                    SPDX JSON/TV
             Events   syft v1 adapter        (pluggable)
             Cache
             Retry
```

### Packages

| Package | Role |
|---|---|
| `core/domain` | Pure domain types — SBOM, Source, Component, Error |
| `core/ports` | Outbound port interfaces — Scanner, Resolver, Exporter, Cache |
| `core/event` | Typed event bus with topic routing, predicates, async delivery |
| `core/pipeline` | Composable middleware chain — Processor, Handler, Chain |
| `core/scanner` | Syft v1 scanner adapter |
| `core/resolver` | Image and filesystem resolvers |
| `core/exporter` | CycloneDX JSON/XML and SPDX JSON/TV exporters |
| `core/cache` | Sharded in-memory cache with TTL |
| `core` | Engine — the composition root |
| `client` | Ergonomic one-call API wrapping the Engine |

---

## Quick start

```go
import (
    "context"
    "os"

    "github.com/bons/bons-ci/pkg/sbomkit/client"
    "github.com/bons/bons-ci/pkg/sbomkit/core/domain"
)

func main() {
    c, err := client.New()
    if err != nil { panic(err) }
    defer c.Close()

    // Container image → CycloneDX JSON (default)
    result, err := c.GenerateFromImage(context.Background(), "ubuntu:22.04")
    if err != nil { panic(err) }
    os.Stdout.Write(result.Encoded)
}
```

---

## All Generate APIs

```go
// Public OCI/Docker image
result, err := c.GenerateFromImage(ctx, "docker.io/ubuntu:22.04")

// Private registry image (with auth)
result, err := c.GenerateFromImage(ctx, "corp.registry/app:v1.0",
    client.WithCredentials(domain.Credentials{Token: os.Getenv("REG_TOKEN")}),
)

// Image pinned to a specific platform
result, err := c.GenerateFromImage(ctx, "ubuntu:22.04",
    client.WithPlatform(domain.Platform{OS: "linux", Arch: "arm64"}),
)

// Container rootfs snapshot on local disk (BuildKit snapshot, overlayfs merge dir)
result, err := c.GenerateFromSnapshot(ctx, "/var/lib/buildkit/snapshots/abc123")

// Arbitrary local directory
result, err := c.GenerateFromDirectory(ctx, "/path/to/project")

// Tarball (supports .tar, .tar.gz, .tgz, .tar.bz2, .tar.xz, .tar.zst)
result, err := c.GenerateFromArchive(ctx, "/tmp/image.tar")

// OCI image layout directory (must contain oci-layout + index.json)
result, err := c.GenerateFromOCILayout(ctx, "/tmp/oci-image-dir")
```

### Per-request options

```go
client.WithFormat(domain.FormatSPDXJSON)          // output format
client.WithCredentials(domain.Credentials{...})    // registry auth
client.WithPlatform(domain.Platform{...})          // platform pin
client.WithCatalogers("go-module", "python")       // cataloger allowlist
client.WithExcludePatterns("**/*.test", "vendor/") // glob exclusions
client.WithScanLayers()                            // layer attribution
client.WithLabels(map[string]string{"env":"prod"}) // arbitrary metadata
```

---

## Client construction options

```go
c, err := client.New(
    client.WithLogger(zapLogger),
    client.WithCacheTTL(12 * time.Hour),
    client.WithCacheDisabled(),
    client.WithMaxRetries(3),
    client.WithDefaultFormat(domain.FormatSPDXJSON),
    client.WithImagePullSource("registry"),    // or "docker"
    client.WithScanParallelism(4),
    client.WithRegistryMirror("docker.io", "mirror.corp:5000"),
    client.WithInsecureRegistry(),
    client.WithAllowedScanRoots("/var/lib/snapshots"), // multi-tenant safety
    client.WithExtraExporter(myCustomExporter),        // pluggable exporter
    client.WithExtraResolver(myCustomResolver),        // pluggable resolver
)
```

---

## Event bus

```go
c, _ := client.New(
    client.WithEventHandler(event.TopicScanCompleted, func(e event.Event) error {
        p := e.Payload.(event.ScanCompletedPayload)
        fmt.Printf("scan %s: %d components in %dms (cache=%v)\n",
            p.RequestID, p.ComponentCount, p.DurationMs, p.CacheHit)
        return nil
    }),
    client.WithEventHandler(event.TopicScanFailed, func(e event.Event) error {
        p := e.Payload.(event.ScanFailedPayload)
        log.Printf("scan %s failed at %s: %v", p.RequestID, p.Stage, p.Err)
        return nil
    }),
)

// Or register/unregister dynamically:
id := c.SubscribeEvents(event.TopicCacheHit, myHandler)
defer c.UnsubscribeEvents(id)
```

### All event topics

| Topic | Payload type | When |
|---|---|---|
| `sbom.scan.requested` | `ScanRequestedPayload` | Before resolver |
| `sbom.scan.started` | `ScanProgressPayload` | Pipeline entry |
| `sbom.scan.progress` | `ScanProgressPayload` | During scan |
| `sbom.scan.completed` | `ScanCompletedPayload` | After scan |
| `sbom.scan.failed` | `ScanFailedPayload` | On any pipeline error |
| `sbom.resolve.started` | `ScanProgressPayload` | Resolver start |
| `sbom.resolve.completed` | `ResolveCompletedPayload` | Resolver done |
| `sbom.resolve.failed` | `ScanFailedPayload` | Resolver error |
| `sbom.export.started` | `ScanProgressPayload` | Exporter start |
| `sbom.export.completed` | `ExportCompletedPayload` | Exporter done |
| `sbom.export.failed` | `ScanFailedPayload` | Exporter error |
| `sbom.cache.hit` | `CacheHitPayload` | Cache served result |
| `sbom.cache.miss` | `CacheMissPayload` | Cache miss |

---

## Plugging in custom components

### Custom scanner backend

```go
type MyScanner struct{}

func (s *MyScanner) Name() string { return "my-scanner" }
func (s *MyScanner) Close() error { return nil }
func (s *MyScanner) Scan(ctx context.Context, src domain.Source, opts ports.ScanOptions) (*domain.SBOM, error) {
    // invoke your scanner; return domain.SBOM
}
```

Wire directly into `core.New()` or inject via a custom `client.New()` with
`client.WithExtraResolver` / `client.WithExtraExporter`.

### Custom exporter

```go
type MyExporter struct{}

func (e *MyExporter) Format() domain.Format { return "application/x-my-format" }
func (e *MyExporter) Export(ctx context.Context, sbom *domain.SBOM, w io.Writer) error {
    // serialise sbom to w
}

c, _ := client.New(client.WithExtraExporter(&MyExporter{}))
result, _ := c.GenerateFromImage(ctx, "ubuntu:22.04",
    client.WithFormat("application/x-my-format"),
)
```

### Custom pipeline middleware

Build the Engine directly and wrap the pipeline:

```go
metricsProcessor := pipeline.Processor(func(ctx context.Context, req pipeline.Request, next pipeline.Handler) (pipeline.Response, error) {
    start := time.Now()
    resp, err := next(ctx, req)
    metrics.RecordScanDuration(time.Since(start), err == nil)
    return resp, err
})

eng := core.New(scanner, resolvers, exporters, cache, logger, cfg)
// The pipeline is built inside New; for full control, construct pipeline.Pipeline directly.
```

---

## Supported formats

| Constant | Wire format |
|---|---|
| `domain.FormatCycloneDXJSON` | CycloneDX 1.5 JSON |
| `domain.FormatCycloneDXXML` | CycloneDX 1.5 XML |
| `domain.FormatSPDXJSON` | SPDX 2.3 JSON |
| `domain.FormatSPDXTagValue` | SPDX 2.3 Tag-Value |
| `domain.FormatSyftJSON` | Syft native JSON |

---

## Error handling

All errors are `*domain.Error` with a typed `Kind` field:

```go
result, err := c.GenerateFromImage(ctx, ref)
if err != nil {
    switch {
    case domain.IsKind(err, domain.ErrKindAuth):
        // credentials missing or rejected
    case domain.IsKind(err, domain.ErrKindNotFound):
        // image or path does not exist
    case domain.IsKind(err, domain.ErrKindValidation):
        // bad input (empty reference, unsupported format, …)
    case domain.IsKind(err, domain.ErrKindScanning):
        // scanner-level failure (all retries exhausted)
    case domain.IsKind(err, domain.ErrKindExporting):
        // serialisation failure
    default:
        // ErrKindInternal, ErrKindTimeout
    }
}
```

---

## Running tests

```bash
go test ./...                        # all tests
go test -race ./...                  # with data-race detector
go test -run TestEngine ./core/...   # engine tests only
go test -run TestClient ./client/... # client tests only
```
