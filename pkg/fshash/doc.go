// Package fshash computes reproducible, hermetic checksums of files and
// directory trees.
//
// # Architecture
//
// The package is split into two tiers:
//
//   - [github.com/bons/bons-ci/pkg/fshash/core] — low-level primitives
//     (algorithms, buffer pools, worker pools, reactive streams). No filesystem
//     dependencies; reusable by any external package.
//   - This package — high-level filesystem API built on top of core.
//
// # Quick start
//
//	cs, _ := fshash.New()
//	res, err := cs.Sum(ctx, "/path/to/dir")
//	fmt.Println(res.Hex())
//
// # Design goals
//
//   - Reproducible: identical content always produces identical digests
//     regardless of OS scheduling, traversal order, or worker count.
//   - Hermetic: the digest depends only on content and (optionally) metadata;
//     never on timestamps or inode numbers unless explicitly requested.
//   - Efficient: parallel file hashing via pread(2); tiered buffer pools;
//     O_NOATIME + POSIX_FADV_SEQUENTIAL on Linux.
//   - Reactive: [Checksummer.SumStream] and [Watcher.WatchStream] expose
//     results as [core.Stream[T]] values for event-driven processing.
//   - Extensible: hash algorithm, filesystem walker, entry filter, and metadata
//     inclusion are all narrow interfaces that callers can replace or compose.
//
// # Algorithms
//
// The default algorithm is SHA-256. All algorithms below are available via
// [WithAlgorithm]:
//
//	Algorithm   Output  Throughput    Security  Use case
//	─────────── ──────  ────────────  ────────  ────────────────────────────
//	SHA-256     32 B    ~1.2 GB/s     ✅ crypto  Default; widest compatibility
//	SHA-512     64 B    ~2 GB/s       ✅ crypto  64-bit CPUs
//	Blake3      32 B    ~2.5 GB/s     ✅ crypto  Best crypto throughput
//	XXHash3     8 B     ~40 GB/s      ❌ non     Change detection, dedup
//	XXHash64    8 B     ~10 GB/s      ❌ non     Legacy; prefer XXHash3
//	CRC32C      4 B     ~19 GB/s      ❌ non     Storage checksums (SSE4.2)
//	SHA-1       20 B    ~1.5 GB/s     ❌ broken  Legacy only
//	MD5         16 B    ~1 GB/s       ❌ broken  Legacy only
//
// # Sharding (large files)
//
// Files ≥ 4 MiB are always hashed in parallel using pread(2) shards of 1 MiB
// each regardless of the worker count. The combined digest uses a deterministic
// sentinel so shard and sequential digests are always distinguishable:
//
//	H(master | 0xFE | nShards_BE32 | shardSize_BE32 | d_0 | d_1 … d_N)
//
// # Reactive streaming
//
// [Checksummer.SumStream] pushes [EntryResult] values to a [core.Stream[EntryResult]]
// as each file or directory is hashed — no waiting for the full tree:
//
//	stream := cs.SumStream(ctx, "/data")
//	for e := range stream.Chan() {
//	    fmt.Printf("%s  %s\n", e.Hex(), e.RelPath)
//	}
//
// [Watcher.WatchStream] combines polling with reactive delivery:
//
//	w := fshash.NewWatcher(cs, "/data", fshash.WithWatchInterval(5*time.Second))
//	for evt := range w.WatchStream(ctx).Chan() {
//	    fmt.Println("changed:", evt.Path)
//	}
package fshash
