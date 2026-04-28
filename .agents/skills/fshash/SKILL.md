---
name: fshash
description: Merkle tree deduplication engine for layer-scoped file access tracking across stacked container filesystem layers
---

# fshash Skill

## Overview

The `pkg/fshash` package provides a production-ready, event-driven Merkle tree deduplication engine. It eliminates redundant hash computations across stacked overlay filesystem layers using copy-on-write ownership resolution.

## When to Use

- Processing fanotify file access events from container ExecOps
- Building content-addressed Merkle trees scoped to filesystem layers
- Deduplicating hash computation across stacked layer chains
- Verifying file integrity via Merkle inclusion proofs

## Quick Start

```go
import (
    "github.com/bons/bons-ci/pkg/fshash"
    "github.com/bons/bons-ci/pkg/fshash/chunk"
    "github.com/bons/bons-ci/pkg/fshash/internal/core"
)

// 1. Create processor with options
proc := fshash.NewProcessor(
    fshash.WithHashAlgorithm(chunk.BLAKE3),
    fshash.WithWorkerCount(4),
    fshash.WithCacheSize(65536),
)
defer proc.Close()

// 2. Register layer stack
proc.RegisterLayer(ctx, baseID, fshash.LayerID{})
proc.RegisterLayer(ctx, upperID, baseID)

// 3. Mark modified files (from ExecOp results)
proc.MarkModified(upperID, "/etc/hosts")

// 4. Submit access events from fanotify
proc.Submit(ctx, core.AccessEvent{
    LayerID: upperID,
    Path:    "/etc/passwd",
    Op:      core.OpRead,
    Data:    fileData,
})

// 5. Finalize → Merkle root
root, err := proc.Finalize(ctx, upperID)
```

## Deduplication Algorithm

The 4-step hot path in `access/dedup.go`:

1. **Bloom Filter**: O(1) rejection of duplicate events within same ExecOp session
2. **Cache Lookup**: Search sharded LRU for cached hash from any ancestor layer
3. **Ownership Resolution**: Check if any layer above cached layer modified the file
4. **Decision**: `ActionReuse` (cache valid) or `ActionCompute` (unique work)

## Testing

```bash
go test ./pkg/fshash/... -v -race -count=1
go test ./pkg/fshash/... -bench=. -benchmem
```

## Architecture

```
Processor → Deduplicator → BloomFilter
                         → Cache (ShardedLRU)
                         → Resolver → LayerStore → Chain
            Hasher → Pool
            Tracker → MerkleTree → Proof
```
