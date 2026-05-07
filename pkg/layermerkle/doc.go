// Package layermerkle provides a deduplication-aware, event-driven engine for
// building Merkle trees of file accesses across stacked overlay filesystem layers.
//
// # Problem Statement
//
// Container runtimes (Docker, BuildKit, containerd) compose images from a stack
// of read-only layers (lowerdirs) plus a writable layer (upperdir). An ExecOp
// (container execution) reads files from this merged view. When the same file
// exists unchanged in a lower layer and multiple ExecOps access it, naively
// hashing it for every ExecOp wastes CPU and I/O.
//
// layermerkle solves this by maintaining a content-addressed cache keyed by
// (layer_id, relative_path). Once a file's hash is computed and attributed to
// its owner layer, every subsequent ExecOp that reads the same file from the
// same layer reuses the cached hash — zero redundant I/O.
//
// # Architecture
//
//	AccessEvent channel (from fanotify / fanwatch)
//	         │
//	         ▼
//	   EventPipeline
//	         │
//	    ┌────┴─────────────┐
//	    ▼                  ▼
//	 Filter            Normalize
//	(drop noise)      (relpath, mask)
//	         │
//	         ▼
//	  DeduplicationEngine
//	    │         │
//	    ▼         ▼
//	 HashCache  LayerFileResolver
//	(check)     (which layer owns file?)
//	    │
//	    ▼
//	 FileHasher ← only called on cache miss
//	    │
//	    ▼
//	 MerkleTreeBuilder
//	    │
//	    ▼
//	 MerkleForest  (one tree per vertex / ExecOp)
//
// # Deduplication
//
// For each access event (vertexID, layerStack, relPath):
//
//  1. Check HashCache for (topLayer, relPath) → O(1) fast path.
//  2. Walk layerStack top-to-bottom via LayerFileResolver to find the owner layer.
//  3. Check HashCache for (ownerLayer, relPath).
//  4. On cache miss: call FileHasher, store in cache for both ownerLayer and topLayer.
//  5. Feed (relPath, hash, ownerLayer) into the MerkleTreeBuilder for this vertex.
//
// # Merkle Tree
//
// Each vertex (ExecOp) produces one [MerkleTree] whose leaves are (relPath, fileHash)
// pairs, interior nodes are directories (hash of sorted children), and the root
// represents the entire access pattern for that execution. Identical file sets
// across ExecOps produce identical Merkle roots, enabling cache key reuse at the
// solver level.
//
// # Quick Start
//
//	engine, err := layermerkle.NewEngine(
//	    layermerkle.WithFileHasher(layermerkle.NewSHA256Hasher()),
//	    layermerkle.WithResolver(layermerkle.NewOverlayResolver(snapshotRoot)),
//	    layermerkle.WithCacheCapacity(64_000),
//	    layermerkle.WithWorkers(runtime.NumCPU()),
//	)
//
//	// Feed events from the fanwatch pipeline.
//	engine.Feed(ctx, eventCh, func(tree *layermerkle.MerkleTree) {
//	    log.Printf("vertex %s root=%s", tree.VertexID, tree.Root)
//	})
//
// # OTEL
//
// Wrap the engine with [NewOTELEngine] for per-vertex span tracing, cache-hit
// rate histograms, and hash-latency counters. No core package imports otel.
//
// # Build Requirements
//
// No cgo, no kernel privileges, no platform restrictions.
// Runs wherever the fanwatch events originate (client side).
package layermerkle
