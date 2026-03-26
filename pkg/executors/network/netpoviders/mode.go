package netproviders

import "github.com/pkg/errors"

// NetworkMode is the user-visible network mode string accepted by the daemon.
//
// Using a named type (rather than a bare string) makes the API self-documenting,
// enables exhaustive switch analysis, and centralises validation so callers
// cannot pass an arbitrary string and discover the error deep inside a
// constructor.
type NetworkMode string

const (
	// NetworkModeAuto probes for the best available mode in priority order:
	//   1. bridge (when BUILDKIT_NETWORK_BRIDGE_AUTO=1)
	//   2. cni    (when a CNI config file exists)
	//   3. host   (Unix fallback)
	//   4. none   (Windows fallback)
	NetworkModeAuto NetworkMode = "auto"

	// NetworkModeCNI uses an external CNI config file and binary directory.
	NetworkModeCNI NetworkMode = "cni"

	// NetworkModeHost shares the daemon's network namespace with build
	// containers.  Unavailable on Windows.
	NetworkModeHost NetworkMode = "host"

	// NetworkModeBridge creates an isolated Linux bridge network using the
	// bundled buildkit-cni-* binaries.  Linux-only.
	NetworkModeBridge NetworkMode = "bridge"

	// NetworkModeNone disables networking entirely (only loopback is available).
	NetworkModeNone NetworkMode = "none"
)

// resolvedNetworkMode is the concrete mode stored in the Result after
// auto-detection.  Its value is always one of the non-auto constants.
type resolvedNetworkMode = NetworkMode

// Validate returns an error when m is not one of the known mode strings.
// Empty string is treated as NetworkModeAuto.
func (m NetworkMode) Validate() error {
	switch m {
	case NetworkModeAuto, "", NetworkModeCNI, NetworkModeHost, NetworkModeBridge, NetworkModeNone:
		return nil
	default:
		return errors.Errorf(
			"invalid network mode %q: must be one of auto, cni, host, bridge, none", string(m))
	}
}

// normalise converts the empty string to NetworkModeAuto.
func (m NetworkMode) normalise() NetworkMode {
	if m == "" {
		return NetworkModeAuto
	}
	return m
}

// String implements fmt.Stringer.
func (m NetworkMode) String() string { return string(m) }
