// Package b2 implements a [content.Store] backed by Backblaze B2 (S3-compatible
// object storage) with OpenTelemetry instrumentation and lifecycle hooks.
//
// # Architecture
//
// The package follows hexagonal architecture (ports & adapters):
//
//   - [ObjectStorage] is the port interface abstracting all object operations.
//   - [minioBackend] is the adapter implementing ObjectStorage via minio-go.
//   - [Store] is the high-level orchestrator implementing [content.Store].
//   - [TracedStore] is a decorator adding OpenTelemetry spans to every operation.
//   - [Hook] is the observer interface for reactive event-driven extensions.
//
// # Usage
//
//	store, err := b2.NewWithMinio(cfg, creds,
//	    b2.WithTracer(tp),
//	    b2.WithHooks(myAuditHook),
//	)
//
// All object keys are scoped under a tenant prefix for multi-tenancy:
//
//	{tenant}/{blobsPrefix}{algorithm}/{encoded}
package b2
