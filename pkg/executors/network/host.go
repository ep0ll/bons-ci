//go:build !windows

package network

import (
	"context"

	resourcestypes "github.com/bons/bons-ci/pkg/executors/resources/types"
	"github.com/containerd/containerd/v2/pkg/oci"
	specs "github.com/opencontainers/runtime-spec/specs-go"
)

// NewHostProvider returns a Provider that grants every build container access
// to the daemon's own network namespace (i.e. no network isolation).
//
// Use cases:
//   - Builds that must access localhost services running on the host.
//   - Environments where CNI is unavailable and host networking is acceptable.
//
// Security note: host networking disables network isolation entirely.  Any
// container using this provider can reach all network interfaces on the host.
// Use only when the build inputs are trusted.
func NewHostProvider() Provider {
	return hostProvider{}
}

// hostProvider is a stateless Provider; it holds no resources and requires
// no cleanup.
type hostProvider struct{}

// New returns a new hostNS.  The hostname argument is accepted but ignored:
// the container inherits the host's network stack including its hostname.
func (hostProvider) New(_ context.Context, _ string) (Namespace, error) {
	return hostNS{}, nil
}

// Close is a no-op; the host provider holds no resources.
func (hostProvider) Close() error { return nil }

// hostNS implements Namespace for host networking mode.
type hostNS struct{}

// Set adds a network namespace entry of type "network" with an empty path,
// which instructs the OCI runtime to place the container in the host's
// network namespace.
//
// oci.WithHostNamespace is a containerd helper that performs this mutation;
// the nil arguments are the unused snapshot/task/container context parameters
// that the OCI hook API requires but that are irrelevant for pure spec mutation.
func (hostNS) Set(s *specs.Spec) error {
	return oci.WithHostNamespace(specs.NetworkNamespace)(nil, nil, nil, s)
}

// Close is a no-op; hostNS holds no resources.
func (hostNS) Close() error { return nil }

// Sample returns nil because host-mode containers share the host network
// stack and are not associated with any isolated veth interface.
func (hostNS) Sample() (*resourcestypes.NetworkSample, error) { return nil, nil }
