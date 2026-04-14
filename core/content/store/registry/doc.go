// Package registry implements a [content.Store] backed by an OCI-compliant
// container registry with transparent local caching, sharded lock-free
// metadata cache, concurrent dual-write pipelines, OpenTelemetry tracing,
// and composable lifecycle hook observers.
//
// # Architecture (Hexagonal / Ports & Adapters)
//
//   - [RegistryBackend] – port interface abstracting resolve / fetch / push.
//   - [ociBackend]      – adapter: containerd transfer/registry + host-keyed
//     connection pool with idle eviction.
//   - [Store]           – orchestrator implementing [content.Store].
//   - [TracedStore]     – decorator: OpenTelemetry spans on every operation.
//   - [Hook]            – observer for reactive, event-driven extensions.
//
// # Performance Highlights
//
//   - 256-shard InfoCache with cache-line padding (false-sharing free).
//   - 64-shard ingestion tracker – no global lock on concurrent Writer calls.
//   - 13-bucket power-of-2 sync.Pool sub-package pool (512 B – 4 MiB).
//   - Async local-cache write goroutine – remote write never blocks on local I/O.
//   - io.ReaderAt fast path in contentReaderAt – zero-copy, no mutex, no seek.
//   - Parallel hook fan-out; single-hook short-circuit avoids goroutine cost.
//   - OCI connection pool keyed on registry host (one TLS session per host).
//   - Exponential-backoff retry (default 3×) for transient network errors.
//
// # Usage
//
//	backend := registry.NewOCIBackend(opts...)
//	s, err := registry.New(backend, localStore, "docker.io/library/nginx",
//	    registry.WithHooks(myHook),
//	    registry.WithCacheTTL(2*time.Minute),
//	)
//	traced := registry.NewTracedStore(s, tracerProvider)
package registry
