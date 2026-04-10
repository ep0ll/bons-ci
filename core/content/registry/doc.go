// Package registry implements a [content.Store] backed by an OCI-compliant
// container registry with local caching, OpenTelemetry instrumentation, and
// lifecycle hooks.
//
// # Architecture
//
// The package follows hexagonal architecture (ports & adapters):
//
//   - [RegistryBackend] is the port interface abstracting resolve, fetch, and
//     push operations against any OCI registry.
//   - [ociBackend] is the adapter implementing RegistryBackend via containerd's
//     transfer/registry package.
//   - [Store] is the high-level orchestrator implementing [content.Store],
//     combining a remote RegistryBackend with a local content.Store cache.
//   - [TracedStore] is a decorator adding OpenTelemetry spans to every operation.
//   - [Hook] is the observer interface for reactive event-driven extensions.
//
// # Usage
//
//	backend := registry.NewOCIBackend(registryOpts...)
//	store, err := registry.New(backend, localStore,
//	    registry.WithTracer(tp),
//	    registry.WithHooks(myHook),
//	)
package registry
