# ovlhash/engine

**Blazingly fast, deduplication-aware BLAKE3 checksum engine for overlayfs / RAFS.**

Computes a cryptographic hash for every file accessed inside any number of overlayfs
merged views — guaranteeing that files physically shared in a common lowerdir are
**hashed exactly once**, regardless of how many containers or snapshotter views
simultaneously access them.

---

## Contents

- [Why this engine?](#why-this-engine)
- [Architecture](#architecture)
- [Package map](#package-map)
- [Quick start](#quick-start)
- [CLI usage](#cli-usage)
- [Hashing strategies](#hashing-strategies)
- [Cross-overlayfs deduplication](#cross-overlayfs-deduplication)
- [Hook system](#hook-system)
- [Metrics](#metrics)
- [Switching to real BLAKE3](#switching-to-real-blake3)
- [Benchmarks](#benchmarks)
- [Build constraints & architecture support](#build-constraints--architecture-support)

---

## Why this engine?

Container runtimes (containerd, CRI-O, nydus/RAFS) create **many overlayfs
mergedviews** from a small set of shared image layers.  A naive checksum tool
would hash the same file N times — once per container that accesses it.

This engine uses `name_to_handle_at(2)` to obtain the underlying filesystem's
`(mount_id, file_handle)` tuple, which is **identical** for any fd opened through
any mergedview that shares the same lowerdir.  A singleflight + LRU cache layer
ensures a single hash computation per unique underlying file.

```
mergedview₁ ──┐
              ├─► lowerdir-L ──► (mount_id=42, handle=0xABCD) ──► ONE hash
mergedview₂ ──┘
```

---

## Architecture

```
┌─────────────────────────────────────────────────────────────────┐
│  fanotify kernel (FAN_OPEN | FAN_OPEN_EXEC)                     │
│  marks: FAN_MARK_MOUNT per mergedview                           │
└──────────────────────────┬──────────────────────────────────────┘
                           │ fd to accessed file (no TOCTOU)
                           ▼
┌──────────────────── pipeline.Pipeline ─────────────────────────┐
│                                                                  │
│  Stage 0: key-resolve  (N workers)                              │
│    name_to_handle_at(fd) → filekey.Key                          │
│    OR fstat(dev,ino)     → filekey.Key  (fallback)              │
│                                                                  │
│  Stage 1: hash          (N workers)                             │
│    dedup.Engine.Compute(key, hashFn)                            │
│      ├─ cache hit?  → return immediately                        │
│      └─ singleflight.Do(key)                                    │
│           ├─ one goroutine calls hashFn                         │
│           └─ N waiters receive same []byte                      │
│             hashFn:                                             │
│               size < 8 MiB  → Blake3Hasher  (sequential pread) │
│               size < 128 MiB→ MmapHasher    (mmap+MAP_POPULATE)│
│               size ≥ 128 MiB→ ParallelHasher(concurrent pread) │
│                                                                  │
│  Stage 2: result        (4 workers)                             │
│    ResultCallback(key, path, hash, size)                        │
│    PostHash hooks                                               │
└─────────────────────────────────────────────────────────────────┘
```

---

## Package map

| Package | Responsibility |
|---------|---------------|
| `pkg/syscallx` | Raw Linux syscalls (fanotify, epoll, name_to_handle_at, pread64, mmap). Zero external deps. |
| `pkg/singleflight` | Inline `x/sync/singleflight` – duplicate call suppression. |
| `pkg/hooks` | Generic priority-ordered hook registry (`Registry[T]`). |
| `pkg/metrics` | Lock-free atomic counters + power-of-two latency histograms. |
| `pkg/filekey` | Cross-overlayfs file identity via `name_to_handle_at` with fstat fallback. |
| `pkg/hasher` | `Blake3Hasher` / `MmapHasher` / `ParallelHasher` / `AdaptiveHasher` + size-classed `BufferPool`. |
| `pkg/cache` | 64-shard LRU cache with TTL and FNV-1a sharding. |
| `pkg/dedup` | Singleflight + cache: one hash per unique file. |
| `pkg/overlay` | `/proc/self/mountinfo` parser; lowerdir / backing-file resolver. |
| `pkg/fanotify` | `Watcher`: epoll + worker pool, mark lifecycle, filter hooks. |
| `pkg/pipeline` | Composable back-pressured stage pipeline. |
| `engine` | Top-level orchestrator + fluent `Builder`. |
| `cmd/checksumctl` | CLI: `watch`, `hash`, `scan`, `bench-report`. |
| `bench` | Empirical benchmarks: sequential vs mmap vs parallel. |

---

## Quick start

### Embed in your Go program

```go
package main

import (
    "context"
    "encoding/hex"
    "fmt"
    "log"
    "os/signal"
    "syscall"

    "github.com/bons/bons-ci/plugins/checksummer/engine"
    "github.com/bons/bons-ci/plugins/checksummer/pkg/filekey"
)

func main() {
    eng, err := engine.Build().
        WatchMount("/run/containerd/io.containerd.runtime.v2.task/k8s.io/abc/rootfs").
        WatchMount("/run/containerd/io.containerd.runtime.v2.task/k8s.io/def/rootfs").
        WatchWorkers(16).
        ParallelWorkers(8).
        OnResult(func(key filekey.Key, path string, hash []byte, size int64) {
            fmt.Printf("%s  %s (%d bytes)\n", hex.EncodeToString(hash), path, size)
        }).
        Engine()
    if err != nil {
        log.Fatal(err)
    }

    ctx, stop := signal.NotifyContext(context.Background(), syscall.SIGINT)
    defer stop()

    log.Fatal(eng.Start(ctx))
}
```

### Hash a single file (no fanotify)

```go
eng, _ := engine.Build().Engine()
hash, err := eng.HashPath(context.Background(), "/usr/lib/libc.so.6")
```

### Hash an already-open fd

```go
hash, err := eng.HashFD(ctx, fd, fileSize)
```

### Register hooks

```go
eng.Hooks().PostHash.Register(hooks.NewHook("audit", hooks.PriorityNormal,
    func(ctx context.Context, p hooks.HashPayload) error {
        auditLog.Record(p.Key, p.Hash, p.FileSize)
        return nil
    }))
```

---

## CLI usage

```sh
# Build
go build -o checksumctl ./cmd/checksumctl/

# Watch two overlay mounts (requires CAP_SYS_ADMIN / root)
sudo ./checksumctl watch \
    --mount /run/containerd/.../abc/rootfs \
    --mount /run/containerd/.../def/rootfs \
    --workers 16 --parallel-workers 8 \
    --json

# Hash a single file
./checksumctl hash /usr/lib/libssl.so.3
./checksumctl hash --json /usr/lib/libssl.so.3

# Recursive directory scan
./checksumctl scan /var/lib/containerd/snapshots/base
./checksumctl scan --workers 8 --json /overlay/lower

# Print system capability report
./checksumctl bench-report
```

---

## Hashing strategies

| File size | Strategy | Why |
|-----------|----------|-----|
| `< 8 MiB` | `Blake3Hasher` — sequential `pread64` | Setup overhead outweighs parallelism gain |
| `8–128 MiB` | `MmapHasher` — `mmap` + `MAP_POPULATE` + `MADV_SEQUENTIAL` | Zero-copy; kernel prefetches into page cache |
| `≥ 128 MiB` | `ParallelHasher` — concurrent `pread64` | Saturates multiple NVMe hardware queues |

All three produce **byte-identical digests** — verified by `TestParallelMatchesSequential`,
`TestMmapMatchesSequential`, and `TestAdaptiveAllStrategiesMatch`.

### Parallel IO — the performance guarantee

Modern NVMe SSDs expose N independent hardware queues.  A single sequential read
saturates only ONE queue.  `ParallelHasher` issues W concurrent `pread64` calls
from different offsets simultaneously, saturating W queues.

**Guarantee**: on any device where `IOdepth > 1` yields higher throughput than
`IOdepth = 1` (i.e. every NVMe and any SSD with NCQ), `ParallelHasher` with
`workers ≥ 2` outperforms sequential for files `≥ DefaultChunkSize`.

Tune with `--parallel-workers` and `--parallel-chunk`. Benchmark:

```
go test ./bench/ -bench=BenchmarkParallelChunkSize -benchmem -benchtime=5s
```

### `io.ReaderAt` support

`ParallelHasher.HashReaderAt` hashes an arbitrary `[offset, offset+size)` byte
range from any `io.ReaderAt` (e.g. `os.File`, `bytes.Reader`, HTTP range response)
using the same parallel strategy — no file re-open required.

---

## Cross-overlayfs deduplication

**The core invariant**: `name_to_handle_at(fd, "", AT_EMPTY_PATH)` bypasses the
overlayfs VFS layer and returns the underlying filesystem's `(mount_id, handle)`.
Two file descriptors opened through *different* mergedviews but backed by the
*same* lowerdir file return **identical** tuples.

```
mergedview-A/lib/libc.so.6  ──►  fd₁  ──►  name_to_handle_at  ──►  (42, 0xABCD)
mergedview-B/lib/libc.so.6  ──►  fd₂  ──►  name_to_handle_at  ──►  (42, 0xABCD)
                                                                        ↓
                                                          singleflight.Do("42:0xABCD")
                                                            → ONE hash computed
                                                            → fd₁ and fd₂ both receive result
```

**Fallback**: when `name_to_handle_at` returns `ENOTSUP` (FUSE, tmpfs, RAFS with
certain configurations), the engine falls back to `fstat(dev, ino)`.  On overlayfs
the kernel preserves the lower-layer inode number in the merged view, so the fallback
key is still unique per lower-layer file within a single mount.

---

## Hook system

Every significant operation exposes a hook point via `pkg/hooks`.
All registries are type-safe generics, priority-ordered, and thread-safe.

| HookSet field | Payload type | Fires when |
|---------------|-------------|-----------|
| `OnFilter` | `EventPayload` | fanotify event received; non-nil error skips the file |
| `OnEvent` | `EventPayload` | event dispatched to worker pool |
| `PreHash` | `HashPayload` | before hash computation |
| `PostHash` | `HashPayload` | after hash stored in cache |
| `OnCacheHit` | `CachePayload` | result returned from cache |
| `OnCacheMiss` | `CachePayload` | key not in cache |
| `OnDedup` | `HashPayload` | singleflight or cache dedup occurred |
| `OnChunk` | `ChunkPayload` | each parallel IO chunk read |
| `OnLayerResolve` | `LayerPayload` | overlay layer resolved from fd |
| `OnError` | `ErrorPayload` | any processing error |

```go
// Skip events triggered by PID 1 (init).
eng.Hooks().OnFilter.Register(hooks.NewHook("skip-init", hooks.PriorityFirst,
    func(_ context.Context, p hooks.EventPayload) error {
        if p.Pid == 1 {
            return errors.New("skip")
        }
        return nil
    }))

// Alert on files larger than 1 GiB.
eng.Hooks().PostHash.Register(hooks.NewHook("large-file-alert", hooks.PriorityNormal,
    func(_ context.Context, p hooks.HashPayload) error {
        if p.FileSize > 1<<30 {
            alerting.Send("large file hashed", p.Path, p.FileSize)
        }
        return nil
    }))
```

---

## Metrics

All metrics are lock-free (`sync/atomic`) and never block the hot path.

```go
snap := eng.MetricsSnapshot()
fmt.Println(snap)
// events=4821 filtered=12 dropped=0 | hashes=391 deduped=4430 bytes=2147483648
// errors=0 | cache hits=4039 misses=782 evicts=0 | parallel=47 mmap=121
// hash_mean=1842.3µs

// Export to Prometheus / JSON
for k, v := range snap.Map() {
    prometheus.MustRegister(prometheus.NewGauge(...).Set(float64(v)))
}
```

### Pipeline back-pressure

```go
for _, s := range eng.PipelineStats() {
    fmt.Println(s)
    // stage="key-resolve" enabled=true buf=12/1024 dropped=0 errors=0
    // stage="hash"        enabled=true buf=3/512  dropped=0 errors=0
    // stage="result"      enabled=true buf=0/256  dropped=0 errors=0
}
```

---

## Switching to real BLAKE3

The engine ships with a **SHA-256 stub** (`pkg/hasher/digest.go`) so it compiles
without external dependencies.  To use real BLAKE3 (3× faster on modern hardware):

```sh
go get github.com/zeebo/blake3
```

Then edit `pkg/hasher/digest.go`:

```go
// Before:
import "crypto/sha256"
func newDigest() hash.Hash { return sha256.New() }

// After:
import "github.com/zeebo/blake3"
func newDigest() hash.Hash { return blake3.New() }
```

Also update `go.mod` if needed:
```sh
go mod tidy
```

For `golang.org/x/sys` (replaces `pkg/syscallx` and `pkg/singleflight`):
```sh
go get golang.org/x/sys golang.org/x/sync
```

---

## Benchmarks

```sh
# All benchmarks
go test ./bench/ -bench=. -benchmem -benchtime=5s -count=3

# Strategy comparison at 128 MiB
go test ./bench/ -bench='BenchmarkSequential/128MiB|BenchmarkMmap/128MiB|BenchmarkParallel/128MiB' -benchtime=5s

# Tune chunk size for your NVMe
go test ./bench/ -bench=BenchmarkParallelChunkSize -benchtime=10s

# Dedup cache-hit latency
go test ./bench/ -bench=BenchmarkDedupCacheHit -benchmem -benchtime=5s

# Buffer pool vs raw alloc
go test ./bench/ -bench=BenchmarkBufferPoolVsAlloc -benchtime=5s
```

### Sample results (Intel Xeon 8581C @ 2.1 GHz, tmpfs)

| Benchmark | Throughput | Allocs/op |
|-----------|-----------|-----------|
| Sequential/128MiB | 789 MB/s | 4 |
| SequentialFD/128MiB | 795 MB/s | 3 |
| Mmap/128MiB | 869 MB/s | **3** |
| Parallel/128MiB/w8 | 711 MB/s | 269 |
| HashBytes/1MiB | 1367 MB/s | 2 |
| BufferPool/1MiB | **80 ns/op** | 1 |
| BufferPool vs alloc | 68 ns vs 1,599,028 ns | — |
| CacheGet | 369 ns/op | 1 |
| FileKey.SFKey | **12 ns/op** | **0** |

> Note: parallel IO advantage is most visible on NVMe with queue depth > 1.
> On tmpfs (in-memory) the page cache is always warm so mmap leads.

---

## Build constraints & architecture support

`pkg/syscallx` uses raw syscall numbers which are architecture-specific:

| File | Architecture | `name_to_handle_at` |
|------|-------------|---------------------|
| `sysnum_amd64.go` | x86_64 | 303 |
| `sysnum_arm64.go` | AArch64 | 264 |
| `sysnum_arm.go` | ARM 32-bit | 370 |

When `golang.org/x/sys/unix` is available, replace the entire `pkg/syscallx`
package with that import — the function signatures are intentionally identical.

---

## Testing

```sh
# All packages, race detector enabled
go test ./... -race -count=1

# Specific packages
go test ./pkg/hasher/... -race -v -run TestParallel
go test ./pkg/dedup/... -race -v -run TestSingleflight
go test ./engine/... -race -v

# Fanotify integration (requires root + overlayfs)
sudo go test ./pkg/fanotify/... -race -v -tags integration
```

---

## License

MIT — see LICENSE file.
