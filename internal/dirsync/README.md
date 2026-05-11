# dirsync

High-performance directory synchronisation for Go, purpose-built for
BuildKit / containerd overlay-filesystem workflows.

## What it does

Compares **lower** (base) and **upper** (diff) directory trees and removes
entries from a **merged** overlay directory that are identical to lower —
because the kernel overlay driver already serves them from lower.

## Architecture

```
Classifier ──exclusive──▶ ExclusiveHandler ──▶ Batcher ──▶ MergedView
           ──common───▶ HashPipeline ──▶ CommonHandler ──▶ Batcher
           ──errs────▶ error collector
```

Every component is an interface. Swap implementations at the composition root.

## Performance: four-tier comparison

| Tier | Condition | Cost |
|------|-----------|------|
| 1 – Inode identity | Hard-linked paths (same dev+ino) | O(1), zero I/O |
| 2 – Size mismatch | Different sizes | O(1), zero I/O |
| 3 – Mtime equality | Same size + mtime | O(1), zero I/O |
| 4S – Sequential | Files < 2 MiB | 64 KiB chunks, early-exit |
| 4P – Parallel | Files ≥ 2 MiB | pread64 segments + ctx cancel |

### Benchmark results (Intel Xeon 2.80 GHz)

| Benchmark | Throughput |
|-----------|-----------|
| Sequential equal 1 MiB | **10.9 GB/s** |
| Parallel equal 8 MiB (4 workers) | **13.3 GB/s** |
| Parallel early-exit 8 MiB (differ at byte 0) | **53.3 GB/s** |

## External libraries

| Library | Purpose |
|---------|---------|
| `github.com/moby/patternmatcher` | Docker/BuildKit gitignore-style path filtering |
| `github.com/cyphar/filepath-securejoin` | Path-traversal-safe `SecureJoin` |
| `github.com/zeebo/xxh3` | xxHash3 128-bit fingerprinting (~25 GB/s) |

## Test coverage

- **171 tests**, 0 failures
- **85.9% statement coverage** (main package)
- All tests run with `-race` — **zero data races**
- `go vet` — **zero warnings**

## Quickstart

```go
eng, err := dirsync.NewDeleteEngine(lowerDir, upperDir, mergedDir, nil, nil)
if err != nil { return err }
result := eng.Run(ctx)
if !result.OK() { return result.Err }
```

## Pattern filtering (gitignore syntax)

```go
// Use moby/patternmatcher compatible patterns
c := dirsync.NewClassifier(lower, upper,
    dirsync.WithIncludePatterns("*.go", "**/*.go"),
    dirsync.WithExcludePatterns("vendor", "testdata"),
    dirsync.WithRequiredPaths("go.mod"),
)
```

## Running tests

```bash
go test -race -count=1 ./...
go test -bench=. -benchmem -benchtime=3s ./...
```
