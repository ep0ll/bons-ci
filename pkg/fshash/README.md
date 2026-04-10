# fshash

**Reproducible, hermetic checksums for files and directory trees.**

`fshash` computes stable SHA-256 (or any pluggable algorithm) digests for
files and recursive directory trees.  Identical content always produces an
identical digest regardless of traversal order, OS scheduler timing, inode
numbers, or filesystem creation timestamps.

---

## Design goals

| Goal | How |
|---|---|
| **Reproducible** | Entries sorted by name at every directory level before hashing. |
| **Hermetic** | Only content and selected metadata (mode bits, size) affect the digest by default — never mtimes or inode numbers. |
| **Efficient** | One `ReadDir` call per directory; 64 KiB pooled I/O buffer per file; parallel file hashing bounded by a configurable worker pool. |
| **Extensible** | `Hasher`, `Walker`, `Filter`, and `FileCache` are narrow interfaces — swap any one without touching the others. |
| **Clean API** | Functional options, zero global mutable state, full context support. |

---

## Quick start

```go
import "github.com/example/fshash"

// SHA-256, MetaModeAndSize, NumCPU workers, no filter.
cs := fshash.MustNew()

res, err := cs.Sum(ctx, "/path/to/dir")
fmt.Println(res.Hex()) // e.g. "a3f2..."
```

### Single file

```go
dgst, err := fshash.FileDigest(ctx, "/path/to/file.bin")
```

### Verify against a known digest

```go
err := cs.Verify(ctx, "/path/to/dir", knownDigest)
// Returns *fshash.VerifyError on mismatch.
```

---

## Options

```go
cs, err := fshash.New(
    fshash.WithAlgorithm(fshash.SHA512),
    fshash.WithWorkers(runtime.NumCPU()),
    fshash.WithFilter(fshash.ExcludeNames(".git", "vendor")),
    fshash.WithMetadata(fshash.MetaModeAndSize),
    fshash.WithCollectEntries(true),
    fshash.WithFollowSymlinks(false),
)
```

| Option | Default | Description |
|---|---|---|
| `WithAlgorithm` | `SHA256` | Built-in: `SHA256`, `SHA512`, `SHA1`, `MD5`. |
| `WithHasher` | SHA-256 | Custom `Hasher` (e.g. BLAKE3). |
| `WithWalker` | `OSWalker` | Custom `Walker` (e.g. `FSWalker` for `fs.FS`). |
| `WithFilter` | allow-all | Exclude entries by name or pattern. |
| `WithMetadata` | `MetaModeAndSize` | Which fields mix into the per-file hash. |
| `WithWorkers` | `NumCPU` (max 64) | Parallel file-hashing goroutines. |
| `WithCollectEntries` | `false` | Populate `Result.Entries`. |
| `WithFollowSymlinks` | `false` | Dereference symlinks (cycle-safe). |
| `WithFileCache` | `nil` | Short-circuit file reads on cache hits. |

---

## Metadata flags

```go
fshash.MetaNone         // content only — maximally portable
fshash.MetaMode         // include unix permission bits
fshash.MetaSize         // include file size in header
fshash.MetaMtime        // include modification time (breaks hermeticity)
fshash.MetaModeAndSize  // default — permissions matter; timestamps don't
```

---

## Filters

```go
// Exclude by exact base name.
fshash.ExcludeNames(".git", "vendor", "node_modules")

// Exclude by glob pattern (path.Match semantics).
fshash.ExcludePatterns("*.tmp", "*.log", "testdata/")

// Chain: first non-Include decision wins.
fshash.ChainFilters(
    fshash.ExcludeNames(".git"),
    fshash.ExcludePatterns("*.generated.go"),
)

// Custom filter.
myFilter := fshash.FilterFunc(func(relPath string, fi fs.FileInfo) fshash.FilterDecision {
    if strings.HasSuffix(relPath, "_test.go") {
        return fshash.Exclude
    }
    return fshash.Include
})
```

---

## Caching

Avoid re-reading unchanged files:

```go
cache := &fshash.MemoryCache{}
cs, err := fshash.NewCachingChecksummer(cache)

// First call reads every file from disk and populates the cache.
r1, _ := cs.Sum(ctx, root)

// Subsequent calls serve file digests from the cache.
// Directory digests are always recomputed (cheap: one hash over cached children).
r2, _ := cs.Sum(ctx, root)

// Invalidate a specific entry when you know the file changed.
cache.Invalidate("/abs/path/to/file")
```

### Inspector: measure cache hit rate

```go
ins := fshash.NewInspector(cs, cache)
res, entries, err := ins.Sum(ctx, root)

fmt.Printf("cache hit rate: %.0f%%\n", ins.HitRate()*100)
for _, e := range entries {
    if e.CacheHit {
        fmt.Println("HIT ", e.RelPath)
    }
}
```

---

## Snapshots

Save a digest record and verify later:

```go
// Save.
snap, err := fshash.TakeSnapshot(ctx, root)
var buf bytes.Buffer
snap.WriteTo(&buf)
os.WriteFile("snapshot.json", buf.Bytes(), 0o644)

// Verify later.
data, _ := os.ReadFile("snapshot.json")
snap, _ = fshash.ReadSnapshot(bytes.NewReader(data))
if err := snap.VerifyAgainst(ctx, root); err != nil {
    log.Fatal("tree has changed:", err)
}

// Diff two snapshots.
diff := snapBefore.Diff(snapAfter)
fmt.Println("added:", diff.Added)
fmt.Println("removed:", diff.Removed)
fmt.Println("modified:", diff.Modified)
```

---

## Diff

Compare two live trees without snapshots:

```go
diff, err := cs.Diff(ctx, "/path/to/v1", "/path/to/v2")
fmt.Println(diff.Added, diff.Removed, diff.Modified)
```

---

## Streaming APIs

### HashReader — hash a byte stream directly

```go
f, _ := os.Open("/path/to/file")
defer f.Close()

dgst, err := fshash.HashReader(ctx, f, fshash.WithAlgorithm(fshash.SHA512))
```

### Walk — visit entries as they are computed

```go
_, err := cs.Walk(ctx, root, func(e fshash.EntryResult) error {
    fmt.Printf("%s  %s  %s\n", e.Hex(), e.Kind, e.RelPath)
    return nil
})
```

### Canonicalize — write a sha256sum-style manifest

```go
var buf bytes.Buffer
rootDgst, err := cs.Canonicalize(ctx, root, &buf)
os.WriteFile("MANIFEST.txt", buf.Bytes(), 0o644)

// Parse it back.
entries, err := fshash.ReadCanonical(&buf)
```

Output format (one line per entry, root last):
```
a3f2...  file     src/main.go
8b1c...  dir      src
e9d4...  root     .
```

### ParallelDiff — diff two trees concurrently

```go
// Runs both Sum calls simultaneously, halving wall time.
diff, err := cs.ParallelDiff(ctx, "/path/to/v1", "/path/to/v2")
```

### SumMany — hash many independent paths

```go
paths := []string{"/a", "/b", "/c"}
results, errs := cs.SumMany(ctx, paths)
```

---

## Size limit

Reject files above a threshold to prevent hashing untrusted large files:

```go
cs, err := fshash.New(fshash.WithSizeLimit(100 * 1024 * 1024)) // 100 MiB
_, err = cs.Sum(ctx, root)

var tl *fshash.FileTooLargeError
if errors.As(err, &tl) {
    fmt.Printf("file %s is %d bytes (limit %d)\n", tl.Path, tl.Size, tl.Limit)
}
```

---

## CompareTrees — full per-entry structured comparison

```go
cmp, err := cs.CompareTrees(ctx, "/path/to/v1", "/path/to/v2")
// cmp.Changes is sorted by RelPath, contains ALL entries (unchanged included)

fmt.Println(cmp.Summary()) // "added=2 removed=1 modified=3 unchanged=47"

for _, ch := range cmp.OnlyChanged() {
    fmt.Printf("%s  %s  %s\n", ch.Status, ch.Kind, ch.RelPath)
}

counts := cmp.CountByStatus()
// map[unchanged:47 added:2 removed:1 modified:3]
```

Unlike `Diff` (which returns only changed paths), `CompareTrees` returns
every entry with a `ChangeStatus` and the digests from both trees.

---

## Watcher — polling-based change detection

```go
w := fshash.NewWatcher(cs, "/path/to/dir",
    func(e fshash.ChangeEvent) {
        fmt.Println("changed:", e.Path)
        fmt.Printf("  prev: %x\n  curr: %x\n", e.PrevDigest, e.CurrDigest)
    },
    fshash.WithWatchInterval(5*time.Second),
    fshash.WithWatchCompareTrees(true),
)

ctx, cancel := context.WithCancel(context.Background())
defer cancel()
err := w.Watch(ctx) // blocks; returns ctx.Err() on cancel
```

With `WithWatchCompareTrees(true)`, take a snapshot first for full before/after diffs:

```go
snap, _ := fshash.TakeSnapshot(ctx, "/path/to/dir")
err = w.WatchWithSnapshot(ctx, snap)
// ChangeEvent.Comparison is now populated on each change
```

---

## Custom Hasher (e.g. BLAKE3)

```go
type blake3Hasher struct{}
func (blake3Hasher) New() hash.Hash    { return blake3.New() }
func (blake3Hasher) Algorithm() string { return "blake3" }

cs, err := fshash.New(fshash.WithHasher(blake3Hasher{}))
```

---

## Custom Walker (in-memory FS)

```go
// Walk an embed.FS or testing tree without touching the real filesystem.
cs, err := fshash.New(fshash.WithWalker(fshash.FSWalker{FS: myFS}))
res, err := cs.Sum(ctx, ".")
```

---

## Directory digest algorithm

```
dir_digest = H(
    name₁ || 0x00 || child_digest₁
    name₂ || 0x00 || child_digest₂
    …
)
```

Children are sorted by base name in ascending lexicographic order before
hashing, so the result is invariant to OS-level traversal order.

```
file_digest = H( metadata_header || file_content )
```

The metadata header is a fixed-width binary encoding of the selected
`MetaFlag` fields, prefixed by a 0xFF sentinel byte so that a zero-size file
with `MetaNone` differs from one with metadata.

---

## Package structure

| File | Responsibility |
|---|---|
| `doc.go` | Package documentation |
| `result.go` | Value types: `Result`, `EntryResult`, `EntryKind`, `FileTooLargeError` |
| `hasher.go` | `Hasher` interface, built-in algorithms, `MetaFlag`, `mustWrite` |
| `filter.go` | `Filter` interface, `ExcludeNames`, `ExcludePatterns`, `ChainFilters` |
| `walker.go` | `Walker` interface, `OSWalker`, `FSWalker` |
| `options.go` | `Options` struct, functional `Option` helpers incl. `WithSizeLimit` |
| `pool.go` | I/O buffer pool, `workerPool` |
| `checksum.go` | `Checksummer`, `FileCache`, `MemoryCache`, `VerifyError`, `DiffResult` |
| `mtimecache.go` | `MtimeCache` — auto-invalidating cache by mtime+size |
| `snapshot.go` | `Snapshot`, `Inspector`, `InspectEntry` |
| `stream.go` | `HashReader`, `Walk`, `Canonicalize`, `ParallelDiff`, `SumMany` |
| `compare.go` | `CompareTrees`, `TreeComparison`, `TreeChange`, `ChangeStatus` |
| `watcher.go` | `Watcher`, `ChangeEvent`, polling-based change detection |

---

## Design decisions

### Entry sort order
`Result.Entries` (and therefore `Walk` callbacks) are ordered with all non-root entries in ascending lexicographic order of their relative path, with the root directory `"."` last. This **bottom-up order** is natural for tree operations: a parent directory's digest is always emitted after all its children have been visited.

### The `"."` entry
The root path is always represented as `"."` in `RelPath`. It appears in `Result.Entries` when `CollectEntries` is true. It is:
- **Excluded** from `Diff`, `ParallelDiff`, and `CompareTrees` change sets — the root's overall change is already captured by comparing root digests.
- **Excluded** from `Snapshot.Diff` for the same reason.
- **Excluded** from `Canonicalize` entry lines — represented instead by the final `"root"` summary line.

### Directory digest includes directory metadata
When `MetaMode` is set, the directory's own permission bits are mixed into its digest *before* its children. This means `chmod`-ing a directory (without changing any file inside) changes the directory's digest and therefore all ancestor digests.

### Cache key is absolute path
`FileCache` keys are absolute paths, not content addresses. This means two different files with identical content get separate cache entries. Callers that need content-address deduplication should implement their own `FileCache` on top of the existing interface.

---

## License

MIT
