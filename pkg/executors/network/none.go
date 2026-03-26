package network

import (
	"context"

	resourcestypes "github.com/bons/bons-ci/pkg/executors/resources/types"
	specs "github.com/opencontainers/runtime-spec/specs-go"
)

// NewNoneProvider returns a Provider that disables networking for build
// containers.
//
// "None" mode does not configure any network namespace on the OCI spec.
// The OCI runtime will create a new, empty network namespace for the container
// automatically (standard behaviour when no network namespace is specified).
// Only the loopback interface (lo) is available inside the container.
//
// Use cases:
//   - Builds that must not have any network access (maximum isolation).
//   - Platforms where host or CNI networking is unavailable.
//   - The default Windows fallback when CNI is not configured.
func NewNoneProvider() Provider {
	return noneProvider{}
}

// noneProvider is a stateless Provider; it holds no resources.
type noneProvider struct{}

// New returns a noneNS.  The hostname is accepted but ignored.
func (noneProvider) New(_ context.Context, _ string) (Namespace, error) {
	return noneNS{}, nil
}

// Close is a no-op; the none provider holds no resources.
func (noneProvider) Close() error { return nil }

// noneNS implements Namespace for none-networking mode.
type noneNS struct{}

// Set is a deliberate no-op.  When no network namespace is written into the
// spec, the OCI runtime (runc/crun) creates a new, unconfigured network
// namespace for the container, leaving only the loopback interface present.
func (noneNS) Set(_ *specs.Spec) error { return nil }

// Close is a no-op; noneNS holds no resources.
func (noneNS) Close() error { return nil }

// Sample returns nil; a none-mode container has no veth interface to sample.
func (noneNS) Sample() (*resourcestypes.NetworkSample, error) { return nil, nil }
