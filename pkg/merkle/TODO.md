# TODO — layermerkle

Priority: P0 (blocker) → P1 (important) → P2 (nice-to-have) → P3 (future)

---

## P0 — ✅ COMPLETE

- [x] **Proof edge case: 2-leaf tree** — covered in TestProofMultiLeaf
- [x] **Tombstone + concurrent write race** — TestTombstoneAfterSetIfAbsent, TestComputePhaseGetsTombstoneRace
- [x] **Stack walk direction regression test** — TestPhase2_AncestorPromotion, TestWriteAfterReadPromotion
- [x] **All test packages** — 142 tests across 9 packages, race-detector clean

## P1 — ✅ COMPLETE

- [x] **LRU cap** — BoundedCache with per-shard LRU list (`cache/bounded.go`)
- [x] **TTL eviction** — BoundedCache WithTTL + background GC goroutine
- [x] **Metrics hook** — MetricsHook with atomic counters + Snapshot (`hook/metrics.go`)
- [x] **Batch event API** — Pipeline.Submit(ctx, []*event.FileAccessEvent)
- [x] **Persist/restore cache** — ShardedCache.Snapshot(w) / Restore(r) (`cache/snapshot.go`)
- [x] **Parallel SealAll** — Registry.SealAll() runs Seal() per-layer in goroutines
- [x] **Benchmarks** — cache throughput, Merkle seal/proof, pipeline submit/run

## P2 — Nice-to-Have

- [ ] **Cross-layer proof** ✅ done — Pipeline.CrossLayerProof(stack, path)
- [ ] **Multi-algorithm support** — allow SHA512 leaves alongside SHA256
- [ ] **Streaming Merkle** — stream-hash large files via io.Reader
- [ ] **CLI tool** — `layermerkle-inspect` binary to dump cache/Merkle state
- [ ] **Protobuf serialisation** — Proof.ToProto() / Proof.FromProto()
- [ ] **OpenTelemetry hook** — MetricsHook extension for OTEL trace spans

## P3 — Future

- [ ] **Persistent cache backend** — BadgerDB / bbolt Cache implementation
- [ ] **Distributed cache** — Redis-backed Cache for multi-host builds
- [ ] **WASM target** — ensure GOARCH=wasm compiles (no fanotify dependency)
