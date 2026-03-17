# dirsync

A high-performance, fault-tolerant Go library for comparing two directory trees
(`lower` vs `upper`) and streaming typed results over channels.

---

## Features

| Feature | Detail |
|---|---|
| **Merge-sort walk** | O(N) comparisons using pre-sorted `os.ReadDir` output; one `getdents64` per directory |
| **Pruned exclusive set** | Directory subtrees exclusive to lower emit as a single pruned root — one `os.RemoveAll` deletes the whole tree |
| **Two-tier equality** | Fast path (inode identity → size+mtime), slow path (incremental SHA-256 via 64 KiB buffer pool) |
| **Hash worker pool** | Fixed-size goroutine pool with `sync.Pool` buffer recycling; no GC churn on large trees |
| **Filter pipeline** | ExcludeFilter prunes dirs; IncludeFilter skips non-matching files but still traverses dirs |
| **AllowWildcards** | Toggle between literal base-name/prefix matching and `filepath.Match` glob syntax |
| **RequiredPaths** | Asserts that listed paths appear in output; returns `*MissingRequiredPathsError` if absent |
| **Context-aware** | All blocking sends select on `ctx.Done()`; cancellation is not treated as an error |
| **Special-file safe** | Devices, FIFOs, and sockets never opened for hashing — compared by `mode+rdev` |
| **Symlink modes** | No-follow: compare `readlink(2)` targets. Follow: resolve then compare as regular files |

---

## Architecture

```
┌────────────────────────────────────────────────────────────────────────────────┐
│  Diff(ctx, lowerRoot, upperRoot, opts)  →  (Result, error)                    │
│                                                                                │
│  Synchronous:  BuildFilter(opts) → PathFilter     ← validates globs eagerly   │
│                newRequiredTracker(RequiredPaths)   ← nil when list is empty    │
└───────────────────────────────┬────────────────────────────────────────────────┘
                                │  starts one goroutine
                                ▼
                       ┌────────────────┐
                       │   walker       │  single-threaded recursive merge-sort
                       │  compareDir()  │  reads both dirs once (os.ReadDir)
                       │                │
                       │  Injected:     │
                       │  PathFilter    │ ← decides Allow/Skip/Prune per entry
                       │  *tracker      │ ← marks required paths as seen
                       │  *hashPool     │ ← offloads SHA-256 to workers
                       └───────┬────────┘
              ┌────────────────┴──────────────┐
              ▼                               ▼
     ┌─────────────────┐           ┌──────────────────────┐
     │  excCh chan      │           │  hashPool (N workers) │
     │  ExclusivePath  │           │                        │
     │                 │           │  comCh chan CommonPath  │
     │  Pruned dirs:   │           │  (meta-equal: sync)    │
     │  one entry =    │           │  (hash-checked: async) │
     │  entire subtree │           └──────────────────────  ┘
     └─────────────────┘
```

### Module structure

```
dirsync/
├── dirsync.go          Public API: Options, Result, ExclusivePath, CommonPath, Diff()
├── filter.go           PathFilter interface + NopFilter, IncludeFilter, ExcludeFilter,
│                       CompositeFilter, BuildFilter() factory
├── pattern.go          patternSet: literal vs glob matching; zero filesystem I/O
├── required.go         requiredTracker + MissingRequiredPathsError
├── walk.go             walker: merge-sort algorithm, filter/tracker integration
├── entry.go            readDirEntries(), sameMetadata(), isSymlink()
├── hash.go             hashPool, hashJob, hashFile() (incremental SHA-256)
│
├── pattern_test.go     White-box: patternSet literal + glob rules
├── filter_test.go      White-box: all filter types + BuildFilter factory
├── required_test.go    White-box: requiredTracker lifecycle + MissingRequiredPathsError
├── testutil_test.go    Black-box: shared test helpers (runDiff, writeFile, …)
├── exclusive_test.go   Black-box: ExclusivePath pruning DSA invariants
├── common_test.go      Black-box: CommonPath meta-equal + hash slow-path
├── integration_test.go Black-box: cancellation, concurrency, RequiredPaths end-to-end
├── filter_integration_test.go  Black-box: filter end-to-end through Diff API
├── bench_test.go       Benchmarks: walk throughput, hash pool scaling
│
└── cmd/dirsync/main.go CLI: -include/-exclude/-wildcard/-require/-delete-exclusive flags
```

### Dependency graph (no cycles)

```
patternSet  ←──  IncludeFilter  ┐
patternSet  ←──  ExcludeFilter  ├──  CompositeFilter
                                │
Options  ──►  BuildFilter()  ───┘ → PathFilter
Options  ──►  newRequiredTracker() → *requiredTracker

walker depends on PathFilter (interface) and *requiredTracker (nil-safe pointer)
— never on any concrete filter type.
```

---

## Quick Start

```go
import "github.com/example/dirsync"

res, err := dirsync.Diff(ctx, "/lower", "/upper", dirsync.Options{
    FollowSymlinks:  false,
    AllowWildcards:  true,
    IncludePatterns: []string{"*.go"},          // only .go files
    ExcludePatterns: []string{"vendor", ".*"},  // drop vendor/ and dot-files
    RequiredPaths:   []string{"go.mod"},        // must be present
    HashWorkers:     0,                         // 0 → GOMAXPROCS
})
if err != nil {
    // Synchronous: invalid glob pattern in options
    log.Fatal(err)
}

var wg sync.WaitGroup
wg.Add(2)

// Consumer 1: exclusive lower paths
go func() {
    defer wg.Done()
    for ep := range res.Exclusive {
        if ep.Pruned {
            os.RemoveAll(ep.AbsPath) // one call removes entire subtree
        } else {
            os.Remove(ep.AbsPath)
        }
    }
}()

// Consumer 2: common paths
go func() {
    defer wg.Done()
    for cp := range res.Common {
        if cp.Err != nil {
            log.Printf("hash error: %v", cp.Err)
            continue
        }
        if cp.HashChecked && !cp.HashEqual {
            fmt.Printf("DIFF %s\n", cp.RelPath)
        }
    }
}()

// ⚠  MUST drain both channels before reading Err
wg.Wait()
if err := <-res.Err; err != nil {
    var mErr *dirsync.MissingRequiredPathsError
    if errors.As(err, &mErr) {
        fmt.Println("missing required paths:", mErr.Paths)
    } else {
        log.Fatal(err)
    }
}
```

---

## Options Reference

| Field | Default | Description |
|---|---|---|
| `FollowSymlinks` | `false` | Stat symlink targets; compare as regular files |
| `AllowWildcards` | `false` | Enable `filepath.Match` glob syntax in patterns |
| `IncludePatterns` | `nil` | Restrict output to matching entries (empty = include all) |
| `ExcludePatterns` | `nil` | Suppress matching entries; prune matching dirs (empty = exclude nothing) |
| `RequiredPaths` | `nil` | Paths that must appear in output; absent → `*MissingRequiredPathsError` |
| `HashWorkers` | `GOMAXPROCS` | Goroutines for SHA-256 hashing |
| `ExclusiveBuf` | `512` | `ExclusivePath` channel buffer depth |
| `CommonBuf` | `512` | `CommonPath` channel buffer depth |

### Pattern matching modes

#### `AllowWildcards = false` (literal)

Three matching rules applied per pattern, short-circuit OR:

| Rule | Example pattern | Matches |
|---|---|---|
| Exact full-path | `"cmd/main.go"` | only `cmd/main.go` |
| Base-name | `"Makefile"` | `Makefile`, `src/Makefile`, `a/b/Makefile` |
| Directory prefix | `"vendor"` | `vendor`, `vendor/pkg/x.go`, `vendor/a/b/c` |

The prefix rule requires a path separator — `"vendor"` does **not** match `"vendor_extra"`.

#### `AllowWildcards = true` (glob via `filepath.Match`)

| Rule | Example pattern | Matches |
|---|---|---|
| Full-path glob | `"cmd/*.go"` | `cmd/main.go` (not `cmd/sub/x.go`) |
| Base-name glob | `"*.go"` | `main.go`, `cmd/main.go`, `a/b/c/x.go` |

Invalid glob patterns return a synchronous error from `Diff` before any goroutine starts.

### Filter evaluation order

```
per entry
    │
    ▼
ExcludePatterns?  ── matches ──► Prune (dir) or Skip (file) — FINAL, no include check
    │ no match
    ▼
IncludePatterns?  ── matches ──► Allow
    │ no match
    ▼
    Skip (file) / Skip+recurse (dir)
```

**Exclude wins over include.** A path matching both an exclude and an include pattern is excluded.

**Directories that don't match an include pattern** are still traversed (FilterSkip, not FilterPrune) so their children can be evaluated individually.

---

## Filter Interface

Implement `PathFilter` to add custom filtering logic:

```go
type PathFilter interface {
    Decide(relPath string, isDir bool) FilterDecision
}

// FilterDecision values:
//   FilterAllow  – emit entry; recurse if directory
//   FilterSkip   – skip entry; still recurse if directory
//   FilterPrune  – skip entry; do NOT recurse (directories only)
```

Pass your filter directly to `BuildFilter` by replacing the factory call — or add it to `CompositeFilter` alongside the built-in include/exclude filters.

---

## CLI

```bash
go build ./cmd/dirsync

# What is only in lower?
./dirsync -lower ./a -upper ./b

# Filter: only *.go files, excluding vendor/
./dirsync -lower ./a -upper ./b -wildcard -include '*.go' -exclude vendor

# Multiple include/exclude patterns (flag is repeatable)
./dirsync -lower ./a -upper ./b -exclude vendor -exclude .git -exclude '*.tmp'

# Require go.mod and go.sum to be present in output
./dirsync -lower ./a -upper ./b -require go.mod -require go.sum

# Show common paths with content diff status
./dirsync -lower ./a -upper ./b -common -hash-diff

# Dry-run deletion
./dirsync -lower ./a -upper ./b -dry-run

# Delete all exclusive-lower entries (pruned dirs = one RemoveAll each)
./dirsync -lower ./a -upper ./b -delete-exclusive

# Tune workers and buffer sizes
./dirsync -lower ./a -upper ./b -hash-workers 8 -exclusive-buf 1024
```

### Output format

```
EXCLUSIVE_FILE  orphan.txt
EXCLUSIVE_DIR   old_subdir/            ← single entry, prunes entire tree
COMMON [META_EQ  ] shared/same.txt     lower=-          upper=-
COMMON [HASH_EQ  ] shared/copy.txt     lower=a1b2c3…    upper=a1b2c3…
COMMON [HASH_DIFF] shared/changed.txt  lower=deadbeef…  upper=cafebabe…
```

---

## Testing

```bash
# All tests
go test ./...

# With race detector (recommended)
go test -race ./...

# Only the new filter/pattern/required unit tests
go test -run 'TestPattern|TestFilter|TestRequired' ./...

# Benchmarks
go test -run=^$ -bench=. -benchmem -benchtime=5s ./...
```

### Test organisation

| File | Package | Tests |
|---|---|---|
| `pattern_test.go` | `dirsync` | `patternSet` literal rules, glob mode, construction errors |
| `filter_test.go` | `dirsync` | All filter types, `BuildFilter` factory |
| `required_test.go` | `dirsync` | `requiredTracker` lifecycle, nil-safety, error type |
| `exclusive_test.go` | `dirsync_test` | Pruning DSA invariants, AbsPath, type mismatch |
| `common_test.go` | `dirsync_test` | Meta-equal fast-path, hash slow-path, large files, symlinks |
| `integration_test.go` | `dirsync_test` | Cancellation, concurrency, `RequiredPaths` end-to-end |
| `filter_integration_test.go` | `dirsync_test` | Include/exclude/wildcard via public `Diff` API |
| `bench_test.go` | `dirsync_test` | Walk throughput, hash pool scaling, large flat directories |

---

## Syscall Budget

For a tree with `D` directories, `F` files, `k` exclusive subtree roots,
and `H` files requiring hashing:

| Operation | Syscall count |
|---|---|
| Directory listing | `2D` (`getdents64`: one per dir in lower, one in upper) |
| Metadata comparison | `0` extra (stat results reused from `ReadDir`) |
| Symlink comparison | `2 × symlinks` (`readlink` per side) |
| Content hashing | `2H` file opens + streaming reads |
| Deletion (pruned) | `k` (`RemoveAll` per exclusive root, not per file) |
| Filter evaluation | `0` filesystem I/O (pure in-memory string matching) |
