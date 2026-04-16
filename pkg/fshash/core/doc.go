// Package core provides low-level, reusable primitives for content hashing.
// It has no filesystem dependencies and can be imported by any project.
//
// # Key types
//
//   - [Registry] / [Hasher] — pluggable algorithm registry backed by
//     high-performance external libraries (zeebo/blake3, cespare/xxhash,
//     zeebo/xxh3, stdlib crc32/Castagnoli).
//   - [TieredPool] — four-tier buffer pool (4 KiB / 64 KiB / 1 MiB / 4 MiB)
//     tuned to storage bandwidth; zero allocations on reuse.
//   - [WorkerPool] — fixed goroutine pool with cache-line padding between
//     the producer channel and the WaitGroup to eliminate false sharing.
//   - [Stream[T]] — generic, context-aware, race-safe push channel.
//   - [EventBus[T]] — typed broadcast pub/sub bus for multi-consumer fan-out.
//   - [HashSharded] / [CombineShards] — deterministic parallel pread(2)-based
//     file hashing: mode depends on file SIZE, never on worker count.
//   - [DigestSink] — stack-allocated digest buffer; avoids one heap allocation
//     per file.
//   - [WriteMetaHeader] — single-Write fixed-width metadata encoder.
//
// # External libraries (no reinvented wheels)
//
//   - github.com/zeebo/blake3    — BLAKE3-256; ~2.5 GB/s (scalar Go)
//   - github.com/cespare/xxhash/v2 — XXHash64; ~10 GB/s
//   - github.com/zeebo/xxh3       — XXHash3-64; ~40 GB/s
//   - hash/crc32 (stdlib)          — CRC32C; ~19 GB/s (SSE4.2 hardware)
//
// # Extension points
//
// Register a custom algorithm:
//
//	core.DefaultRegistry.Register("my-algo", myHasher{})
//
// Or create an isolated registry:
//
//	reg := core.NewRegistry()
//	reg.Register("my-algo", myHasher{})
//	h, _ := reg.Get("my-algo")
package core
