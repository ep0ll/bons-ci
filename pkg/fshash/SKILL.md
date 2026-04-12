# fshash Performance SKILL

## 1. Idempotency contract (NON-NEGOTIABLE)

The digest of a file or directory MUST depend ONLY on:
- Raw byte content
- MetaFlag selection
- Hash Algorithm
- shardThreshold constant (fixed at compile time)

It MUST NEVER depend on:
- Worker / goroutine count
- OS scheduling order
- Run-to-run timing
- Machine identity

**Rule**: File size >= shardThreshold → ALWAYS shard mode (regardless of Workers).
Workers control parallelism WITHIN shard mode, not WHICH mode is chosen.

## 2. Buffer pool tiers

| File size         | Buffer  | Pool     | Reasoning                                   |
|-------------------|---------|----------|---------------------------------------------|
| 0 .. 4 KiB        | 4 KiB   | small    | tiny files; fit in one syscall              |
| 4 KiB .. 64 KiB   | 64 KiB  | medium   | source, config; one–two reads               |
| 64 KiB .. 16 MiB  | 1 MiB   | large    | binaries, data; saturates sequential I/O    |
| > 16 MiB          | 4 MiB   | xlarge   | large blobs; maximise read bandwidth        |
| unknown size      | 1 MiB   | large    | HashReader / streaming; good default        |

- `getBufForSize(size int64)` returns the SMALLEST tier that fits `size`.
- `getBufForSize(0)` MUST return the 4 KiB small pool (empty files).
- `getBuf()` returns a 1 MiB large buffer for streaming where size is unknown.
- `putBuf(b)` returns the buffer to its originating pool keyed by `cap(*b)`.

**Why these sizes?**
NVMe SSDs saturate at ~7 GB/s. A 1 MiB read fills in ~143 µs giving ~7000 reads/s.
HDDs saturate at ~200 MB/s. A 4 MiB read fills in ~20 ms giving 50 reads/s — still
fine; larger buffers only reduce syscall count, not bandwidth. For parallel shard
hashing each goroutine holds one `largeBuf` for the duration of its shard, so peak
pool pressure = Workers × 1 MiB (e.g. 8 workers → 8 MiB). Pools reuse allocations,
so steady-state GC pressure is zero after the first few files.

## 3. Parallel file hashing (sharding)

For files >= shardThreshold (4 MiB):
1. Divide into fixed-size chunks (shardSize = 1 MiB).
2. Issue `os.File.ReadAt(chunk, offset)` in parallel — ReadAt is `pread(2)`
   on Linux, which is SAFE to call concurrently on the same fd with no seek-
   position races.
3. Hash each chunk independently → N shard digests.
4. Combine in ORDER: H(masterHash | 0xFE | nShards_BE32 | shardSize_BE32 | d_0 | d_1 … d_N)
5. The 0xFE sentinel + encoding of chunk count/size makes the combined digest
   distinguishable from any sequential hash of the same bytes.

The combination is DETERMINISTIC across all worker counts (SKILL §1) because:
- shardThreshold/shardSize are compile-time constants.
- Shard digests are stored in an index-keyed slice before combination.
- No goroutine writes to another goroutine's index slot.

**Sentinel encoding**: use `binary.BigEndian.PutUint32(buf, uint32(val))` — NOT
`byte(val >> 8)` / `byte(val)` which are compile-time constant expressions that
overflow `byte` when val > 255. This was a real bug fixed in this version.

## 4. Linux-specific I/O

- `O_NOATIME`: prevents atime inode update on every read → no dirty inode,
  no writeback pressure, no extra latency. Falls back to plain `O_RDONLY` on EPERM.
- `POSIX_FADV_SEQUENTIAL` (=2): triggers aggressive kernel read-ahead
  (doubles the read-ahead window from 128 KiB to 256 KiB on typical kernels).
- `POSIX_FADV_DONTNEED` (=4) after reading: evicts hashed pages from the
  page cache, preventing hash workloads from evicting hot application data.
- `O_CLOEXEC`: prevents fd leaking to child processes.
- On non-Linux: graceful fallback to `os.Open`.

## 5. Zero-allocation fast paths

- `digestSink`: stack-allocated `[maxDigestSize]byte` fed to `h.Sum(buf[:0])`.
  Only `cloneDigest()` allocates one heap copy, stored persistently.
- `writeString`: `unsafe.Slice` over string's memory → no `[]byte` allocation
  per name write in `hashDir`.
- `writeMetaHeader`: single `h.Write` for all fixed-width fields.
- Atomic first-error: `atomic.Pointer[error]` instead of `[]error` slice.
- `maxDigestSize = 64`: covers SHA-512 (64 B), Blake3 (32 B), xxHash3 (8 B),
  CRC32C (4 B). Never reallocate the sink for any supported algorithm.

## 6. Walker contract

`Walker.IsSorted()` → `true` means `ReadDir` output is already lexicographically
sorted. When true, `hashDir` SKIPS the O(n log n) defensive sort.
`OSWalker` and `FSWalker` both return `true` (`os.ReadDir` / `fs.ReadDir` guarantee
order). `SortedWalker` also returns `true` after sorting internally.

## 7. Path joining in hashDir

Fast-path join: `absPath + sep + name`
SPECIAL CASE: when `absPath == "."` (FS root), produce `name` NOT `"./name"`.
The separator character is always `'/'` for Walker relPaths; on Windows,
`os.PathSeparator` is used for real filesystem paths in `OSWalker`.

## 8. ExcludeDir semantics

`ExcludeDir` on a directory means:
- The directory NODE is NOT emitted to `collect` (no entry in `Result.Entries`).
- The directory's combined digest is NOT mixed into the parent hash.
- The directory IS recursed — its children ARE collected and hashed individually.

Implementation: wrap the `collect` closure for `ExcludeDir` nodes to filter out
the top-level `relPath` before passing through to the outer `collect`.

## 9. Hash Algorithms — Performance & Use-case Guide

| Algorithm  | Output  | Throughput   | Security | Use-case                               |
|------------|---------|--------------|----------|----------------------------------------|
| SHA-256    | 32 B    | ~0.5–2 GB/s  | ✅ crypto | Compliance, release signing, content addressing |
| SHA-512    | 64 B    | ~0.8–3 GB/s  | ✅ crypto | 64-bit CPUs (wider SIMD lanes)          |
| Blake3     | 32 B    | ~3–15 GB/s   | ✅ crypto | Best balance: fast + cryptographically strong |
| XXHash3    | 8 B     | ~10–30 GB/s  | ❌ non-crypto | Change detection, dedup on trusted inputs |
| XXHash64   | 8 B     | ~7–15 GB/s   | ❌ non-crypto | Legacy; prefer XXHash3 for new code     |
| CRC32C     | 4 B     | ~20–40 GB/s  | ❌ non-crypto | Hardware-accelerated; storage checksums |
| MD5        | 16 B    | ~1–3 GB/s    | ❌ broken | Legacy only; avoid for new code         |
| SHA-1      | 20 B    | ~1–3 GB/s    | ❌ broken | Legacy only; avoid for new code         |

**Default** is SHA-256 (widest compatibility, cryptographically sound).
**Recommended fast+secure**: Blake3. **Recommended fast non-crypto**: XXHash3.

### CRC32C
Uses the Castagnoli polynomial via `hash/crc32` + `crc32.Castagnoli` table.
Modern x86 CPUs accelerate this with the `SSE4.2` `crc32` instruction via CLMUL;
the Go runtime automatically uses hardware acceleration when available.
4-byte digest — sufficient for storage integrity, not for security.

### XXHash3-64 (self-contained, no external deps)
Self-contained pure-Go implementation following the xxHash specification rev 0.9.
Uses a 192-byte secret (built-in default). Designed for exceptional throughput on
modern CPUs via wide integer operations. Test vectors (seed=0):
  `""`    → `0x2D06800538D394C2`
  `"a"`   → `0xE6C9D24F09C18EA7`

### Blake3-256 (self-contained, no external deps)
Self-contained pure-Go implementation following the official BLAKE3 specification.
Uses a 7-round Merkle tree compression function seeded from the SHA-256 IV.
Security: 128-bit collision resistance. Designed for parallel hashing (can use
SIMD; this impl is scalar but correct). Test vectors:
  `""`    → `AF1349B9F5F9A1A6A0404DEA36DCC9499BCB25C9ADC112B7CC9A87EA5B84C7EC`  (first 32 B of empty-input hash)

## 10. Concurrency model

- `workerPool`: fixed goroutine count, buffered channel (n×8), channel-close
  for clean shutdown. NO context in the pool — callers check `ctx.Err()` in jobs.
- `hashDir`: files dispatched to pool; dirs processed serially on calling goroutine
  (maintains cycle-detection isolation without locking visited maps).
- `SumMany` semaphore: ctx-aware acquire so cancellation unblocks all paths.
- Worker pool per `Sum()` call: created fresh, stopped via `defer`. Bounds
  goroutine lifetime to a single tree walk.
- `atomic.Pointer[error]` for first-error in parallel file hashing — lock-free,
  avoids a mutex per directory.
- `workerPool` struct pads 56 bytes between `jobs chan` and `wg sync.WaitGroup`
  to place them on separate cache lines, eliminating false sharing between
  the producer writing to `jobs` and the WaitGroup decrement in workers.

## 11. Blake3 multi-chunk counter invariant (BUG FIX)

Each Blake3 chunk MUST use its sequential position in the input as its
compression counter. Chunk 0 → counter=0, chunk 1 → counter=1, etc.

**Broken pattern** (wrong after Merkle merges reduce `stackLen`):
```go
// chunkCount() re-derives the count from stackLen using powers of 2.
// After a merge: two entries collapse to one, so chunkCount() returns half
// the real value — chunk 2 gets counter=1 (collision with chunk 1).
func (h *blake3State) chunkCount() int {
    n := 0
    for i := 0; i < h.stackLen; i++ { n += 1 << uint(i) }
    return n
}
h.chunk = newBlake3Chunk(uint64(h.chunkCount())) // WRONG
```

**Fixed pattern**: track `numChunksCompleted` separately as a monotone counter:
```go
type blake3State struct {
    numChunksCompleted uint64  // never decreases; drives new chunk counters
    ...
}
// After committing a completed chunk:
h.pushChunkCV(cv)  // increments numChunksCompleted
h.chunk = newBlake3Chunk(h.numChunksCompleted) // correct sequential counter
```

This bug would produce wrong digests for any input > 2048 bytes (> 2 chunks)
once the first parent merge reduces `stackLen`, making subsequent chunks reuse
earlier counter values and collide in the Merkle tree.

