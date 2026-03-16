# dirsync

A high-performance, fault-tolerant Go library for comparing two directory trees
(`lower` vs `upper`) and streaming typed results over channels.

## Features

| Feature | Detail |
|---|---|
| **Merge-sort walk** | O(N) comparisons using pre-sorted `os.ReadDir` output; one `getdents64` per directory |
| **Pruned exclusive set** | Directory subtrees exclusive to lower are emitted as a single pruned root — one `os.RemoveAll` deletes the whole tree |
| **Two-tier equality** | Fast path (inode identity → size+mtime), slow path (incremental SHA-256 via 64 KiB buffered pool) |
| **Hash worker pool** | Fixed-size goroutine pool (`sync.Pool` buffer recycling) prevents GC churn on large trees |
| **Context-aware** | All blocking sends select on `ctx.Done()`; cancellation is not treated as an error |
| **Special-file safe** | Devices, FIFOs, and sockets never opened for hashing — compared by `mode+rdev` |
| **Symlink modes** | No-follow: compare `readlink(2)` targets. Follow: resolve then compare as regular files |

---

## Architecture

```
┌─────────────────────────────────────────────────────────────────────────────┐
│  Diff(ctx, lowerRoot, upperRoot, opts)  →  Result{Exclusive, Common, Err}  │
└────────────────────────────┬────────────────────────────────────────────────┘
                             │  starts one goroutine
                             ▼
                    ┌────────────────┐
                    │   walker       │  single-threaded recursive merge-sort
                    │  compareDir()  │  ← reads both dirs once (os.ReadDir)
                    └───────┬────────┘
               ┌────────────┴──────────────┐
               ▼                           ▼
      ┌─────────────────┐        ┌──────────────────────┐
      │  excCh chan      │        │  hashPool (N workers) │
      │  ExclusivePath  │        │  ├── worker goroutine  │
      │                 │        │  ├── worker goroutine  │
      │  Pruned dirs:   │        │  └── worker goroutine  │
      │  one entry =    │        │                        │
      │  entire subtree │        │  comCh chan CommonPath  │
      └─────────────────┘        └──────────────────────┘
```

### DSA: Pruned Prefix Forest

The exclusive-lower paths form a **minimal cover** of the exclusive sub-forest
of the lower tree.  The invariant is:

> If a directory `D` is exclusive to lower (no counterpart in upper), then `D`
> is emitted as a single `ExclusivePath{Pruned: true}` and **none** of its
> descendants are emitted.

This means a caller performing deletions needs at most **k** `os.RemoveAll`
calls where `k` is the number of pruned roots — not `n` calls for every file.

```
lower/               upper/
├── shared/    ←──── ├── shared/    common  (recurse)
│   ├── a.txt  ←──── │   └── a.txt  common  (meta check)
│   └── b.txt  ────╳       b.txt   EXCLUSIVE (leaf)
├── only/      ────╳               EXCLUSIVE_DIR (pruned)
│   └── deep/                       ↑ children NOT emitted
│       └── f.txt                   ↑
└── c.txt      ←──── └── c.txt     common
```

### Two-Tier Equality

```
pair of stat results
       │
       ▼
┌──────────────────────────────────┐
│  Tier 1 (0 extra I/O)            │
│  ① same device + inode?          │  → MetaEqual=true   (hard link)
│  ② same size AND same mtime?     │  → MetaEqual=true   (heuristic)
└──────────────┬───────────────────┘
               │ no
               ▼
┌──────────────────────────────────┐
│  Tier 2 (file I/O)               │
│  incremental SHA-256             │
│  • pooled 64 KiB read buffers    │
│  • O(1) memory regardless of     │
│    file size                     │
│  • N parallel hash workers       │
└──────────────────────────────────┘
```

---

## Quick Start

```go
import "github.com/example/dirsync"

res := dirsync.Diff(ctx, "/lower", "/upper", dirsync.Options{
    FollowSymlinks: false,
    HashWorkers:    0, // 0 → GOMAXPROCS
})

var wg sync.WaitGroup
wg.Add(2)

// Consumer 1: exclusive lower paths
go func() {
    defer wg.Done()
    for ep := range res.Exclusive {
        if ep.Pruned {
            // One call removes entire subtree.
            os.RemoveAll(ep.AbsPath)
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

// ⚠ Must drain both channels BEFORE reading Err.
wg.Wait()
if err := <-res.Err; err != nil {
    log.Fatal(err)
}
```

---

## CLI

```bash
go build ./cmd/dirsync

# What is only in lower?
./dirsync -lower ./a -upper ./b

# Dry-run: show what would be deleted
./dirsync -lower ./a -upper ./b -dry-run

# Delete all exclusive-lower entries (pruned dirs = one syscall each)
./dirsync -lower ./a -upper ./b -delete-exclusive

# Show common paths with content diff status
./dirsync -lower ./a -upper ./b -common -hash-diff

# Follow symlinks
./dirsync -lower ./a -upper ./b -follow-symlinks

# Tune workers and buffer sizes
./dirsync -lower ./a -upper ./b -hash-workers 8 -exclusive-buf 1024 -common-buf 1024
```

### Output Format

```
EXCLUSIVE_FILE  orphan.txt
EXCLUSIVE_DIR   only_lower_subtree/         ← single entry, prune entire tree
COMMON [META_EQ  ] shared/unchanged.txt     lower=-         upper=-
COMMON [HASH_EQ  ] shared/same_content.txt  lower=a1b2c3…   upper=a1b2c3…
COMMON [HASH_DIFF] shared/changed.txt       lower=deadbeef… upper=cafebabe…
```

---

## Options Reference

| Option | Default | Description |
|---|---|---|
| `FollowSymlinks` | `false` | Stat symlink targets; compare as regular files |
| `HashWorkers` | `GOMAXPROCS` | Goroutines for SHA-256 hashing |
| `ExclusiveBuf` | `512` | `ExclusivePath` channel buffer depth |
| `CommonBuf` | `512` | `CommonPath` channel buffer depth |

---

## Result Fields

### ExclusivePath

| Field | Description |
|---|---|
| `RelPath` | Path relative to lower root |
| `AbsPath` | Absolute path in lower (pass directly to `os.RemoveAll`) |
| `IsDir` | True if directory |
| `Pruned` | True if entire subtree is exclusive; use `os.RemoveAll` |

### CommonPath

| Field | Description |
|---|---|
| `RelPath` | Path relative to both roots |
| `LowerAbs / UpperAbs` | Absolute paths |
| `LowerInfo / UpperInfo` | `fs.FileInfo` from stat |
| `MetaEqual` | Fast-path passed; content assumed identical |
| `HashChecked` | SHA-256 (or readlink) comparison was performed |
| `HashEqual` | Content equal (valid only when `HashChecked=true`) |
| `LowerHash / UpperHash` | Hex SHA-256 digest or readlink target string |
| `Err` | Per-file hash error; does not abort the walk |

---

## Testing

```bash
# Unit + integration tests
go test ./...

# With race detector
go test -race ./...

# Benchmarks
go test -run=^$ -bench=. -benchmem -benchtime=5s ./...
```

### Benchmark Guide

| Benchmark | Measures |
|---|---|
| `BenchmarkWalk_MetaEqualOnly` | Pure walk + metadata check cost (best case) |
| `BenchmarkWalk_HashSlow` | Walk + hash pool throughput (worst case) |
| `BenchmarkWalk_ExclusiveOnly` | Pruning emission throughput |
| `BenchmarkHashPool_Scaling/workers=N` | Hash pool scalability |
| `BenchmarkWalk_LargeFlat` | Merge-sort on wide flat directory (2 500 entries) |

---

## Relation to BuildKit merge-diff

The algorithm follows the same principles as the [BuildKit merge-diff spec][1]:

- The walk produces a **diff** between lower (base layer) and upper (applied layer).
- `ExclusivePath` entries correspond to **whiteout** candidates.
- `CommonPath{HashChecked: true, HashEqual: false}` entries correspond to
  **modified** files in the diff.
- Pruned directory roots are the equivalent of **opaque whiteouts** (`.wh..wh..opq`).

[1]: https://raw.githubusercontent.com/moby/buildkit/refs/heads/master/docs/dev/merge-diff.md

---

## Syscall Budget

For a tree with `D` directories, `F` files, `k` exclusive subtree roots,
and `H` files requiring hashing:

| Operation | Syscall count |
|---|---|
| Directory listing | `2D` (`getdents64`: one per dir in lower, one per dir in upper) |
| Metadata comparison | `0` (stat results reused from `ReadDir`) |
| Symlink comparison | `2 × symlinks` (`readlink` per side) |
| Content hashing | `2H` file `open` + streaming `read` calls |
| Deletion (pruned) | `k` (`RemoveAll` per exclusive root, not per file) |