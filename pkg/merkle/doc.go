// Package layermerkle provides efficient, deduplicated Merkle tree construction
// over layered filesystems (e.g., OCI image layers, BuildKit LLB ExecOp outputs).
//
// # Problem
//
// When executing operations over a stacked overlay filesystem (analogous to
// BuildKit's ExecOp), file accesses span multiple layers. Computing a Merkle
// tree of accessed files naively results in redundant hash computations: a file
// unchanged across layer N and N+1 will be hashed twice.
//
// This package solves that by deduplicating hash work across the entire layer
// stack. Its single responsibility is:
//
//   - Accept file-access events (from fanotify or any source) with associated
//     layer-stack metadata (ordered layer digests, vertex digest, filepath, …)
//   - Maintain a sharded, concurrent hash cache keyed by (filepath, layerDigest)
//   - For each new event, walk the layer stack top-down and reuse any already-
//     cached hash before falling back to fresh computation
//   - Feed deduplicated hashes into a per-layer incremental Merkle builder
//   - Expose the resulting per-layer Merkle trees and proofs
//
// # Architecture
//
//	EventSource ──► Pipeline ──► DedupEngine ──► MerkleRegistry
//	                                │
//	                           ShardedCache
//	                                │
//	                          HashProvider
//	                                │
//	                           HookChain
//
// All components are interface-driven, composable, and replaceable. The pipeline
// is event-driven and processes events concurrently via a configurable worker pool.
//
// # Quick start
//
//	eng, err := pipeline.New(
//	    pipeline.WithHashProvider(hash.SHA256FileHasher()),
//	    pipeline.WithWorkers(runtime.NumCPU()),
//	    pipeline.WithHook(myHook),
//	)
//	if err != nil { ... }
//
//	go eng.Run(ctx, eventCh)
//
//	// Later, seal a layer's Merkle tree:
//	tree, err := eng.Seal(ctx, layerDigest)
package layermerkle
