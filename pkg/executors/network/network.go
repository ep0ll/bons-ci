// Package network defines the Provider and Namespace interfaces that decouple
// BuildKit's executor layer from any concrete networking implementation.
//
// # Design contract
//
// A Provider is a long-lived factory that owns lifecycle resources (pools,
// bridge interfaces, CNI handles).  It is created once at daemon startup and
// closed at shutdown.
//
// A Namespace is a short-lived handle representing a single network namespace
// assignment for one build container.  It is acquired via Provider.New,
// applied to an OCI spec via Set, and released via Close.
//
// The two-interface design follows the Interface Segregation Principle: code
// that only needs to apply a namespace to a spec depends only on Namespace,
// not on the full Provider surface.
//
// # Implementations
//
//   - host:        shares the daemon's netns with containers (non-Windows).
//   - none:        installs no network namespace (loopback only, all platforms).
//   - cniprovider: CNI-file-based isolated networking (all platforms).
//   - cniprovider (bridge): auto-configured bridge networking (Linux only).
package network

import (
	"context"
	"io"

	resourcestypes "github.com/bons/bons-ci/pkg/executors/resources/types"
	specs "github.com/opencontainers/runtime-spec/specs-go"
)

// Provider is a long-lived factory for network namespaces.
//
// Implementations must be goroutine-safe: New may be called from multiple
// goroutines simultaneously (one per parallel build job).
//
// Close must be called exactly once when the provider is no longer needed.
// It releases all pooled namespaces and any associated system resources.
// After Close returns, no further calls to New are permitted.
type Provider interface {
	io.Closer

	// New returns a Namespace for a single build container.
	//
	// hostname is the desired container hostname.  Implementations that use
	// a pre-warmed pool may bypass it for hostname=="" and create a fresh
	// namespace otherwise.  Callers that do not require a specific hostname
	// should pass "".
	//
	// The returned Namespace must be closed by the caller via Close() when
	// the container exits.
	New(ctx context.Context, hostname string) (Namespace, error)
}

// Namespace is a handle to a single configured network namespace.
//
// Each instance is owned by exactly one consumer at a time; it is not safe
// for concurrent use after it has been passed to Set.
//
// Close must be called exactly once.  For pooled implementations it returns
// the namespace to the pool; for non-pooled implementations it releases all
// associated kernel resources.
type Namespace interface {
	io.Closer

	// Set applies this namespace to the OCI runtime spec.
	// For Linux: adds a network namespace entry pointing at the netns bind-mount.
	// For Windows: sets Windows.Network.NetworkNamespace to the HCN GUID.
	// For host mode: sets the spec to share the host network namespace.
	// For none mode: no-op (the runtime defaults to a new, unconfigured netns).
	Set(*specs.Spec) error

	// Sample returns a snapshot of traffic statistics for this namespace,
	// expressed as deltas relative to the time the namespace was acquired.
	//
	// Returns (nil, nil) when sampling is not available (non-Linux, no veth,
	// or host/none provider which do not have an isolated interface).
	Sample() (*resourcestypes.NetworkSample, error)
}
