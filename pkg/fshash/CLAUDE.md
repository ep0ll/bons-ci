# CLAUDE.md — fshash Package

## What This Package Does

`pkg/fshash` is a **Merkle tree deduplication engine** for layer-scoped file access tracking. It eliminates redundant hash computations across stacked container filesystem layers (overlay-fs style) by maintaining a content-addressed Merkle tree with copy-on-write deduplication semantics.

## Package Structure

```
pkg/fshash/
├── internal/core/   # Shared domain types (LayerID, AccessEvent, FileHash, errors)
├── access/          # Deduplication engine (bloom filter, tracker, deduplicator)
├── cache/           # Sharded LRU hash cache
├── chunk/           # Pluggable hashers (SHA256, BLAKE3, XXH3) + buffer pool
├── layer/           # Layer store, chain, and ownership resolver
├── merkle/          # Merkle tree builder, node types, and proof generation
├── overlay/         # OverlayFS metadata interpreter (whiteouts, opaques)
├── types.go         # Re-exports from internal/core (public API)
├── errors.go        # Package declaration
├── options.go       # Functional options for Processor
├── hooks.go         # Lifecycle event hooks
└── processor.go     # Top-level orchestrator (public entry point)
```

## Key Concepts

- **LayerID**: Opaque layer identifier wrapping a digest string
- **AccessEvent**: Input from fanotify — contains layerID, path, op, optional data
- **Overlay Interpreter**: Converts raw events into logically-aware mutations (e.g. `.wh.foo` → ActionExclude)
- **Deduplicator**: 5-step hot path: overlay visibility → bloom filter → cache → ownership resolution → compute/reuse
- **LayerChain**: Ordered sequence of stacked layers (bottom→top)
- **Resolver.NeedsRehash**: Walks chain above cached layer checking for modifications

## Architecture Rules

1. **No import cycles**: Sub-packages import `internal/core` for shared types. Root `fshash` re-exports via type aliases.
2. **Concurrency**: All public types are thread-safe. Cache uses sharded LRU. Bloom filter uses mutex.
3. **Zero-alloc hot path**: Bloom filter uses FNV-1a dual hashing. Cache uses FNV-1a for shard routing.
4. **Functional options**: All configuration via `Option` functions passed to `NewProcessor`.

## Testing

```bash
go test ./pkg/fshash/... -v -race       # Full suite with race detector
go test ./pkg/fshash/... -bench=. -benchmem  # Benchmarks
```

## Common Patterns

```go
proc := fshash.NewProcessor(fshash.WithHashAlgorithm(chunk.BLAKE3))
proc.RegisterLayer(ctx, baseID, fshash.LayerID{})
proc.RegisterLayer(ctx, upperID, baseID)
proc.MarkModified(upperID, "/modified/file")
proc.Submit(ctx, fshash.AccessEvent{LayerID: upperID, Path: "/foo", Op: fshash.OpRead, Data: data})
root, _ := proc.Finalize(ctx, upperID)
proc.Close()
```
