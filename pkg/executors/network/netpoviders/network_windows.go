//go:build windows

package netproviders

import (
	"github.com/bons/bons-ci/pkg/executors/network"
	"github.com/moby/buildkit/util/bklog"
)

// getHostProvider returns (nil, false) on Windows.
//
// Sharing the host network namespace on Windows requires HNS support that is
// not yet integrated into BuildKit's network provider abstraction.  Host mode
// is therefore unavailable on Windows; callers should use CNI or none mode.
func getHostProvider() (network.Provider, bool) {
	return nil, false
}

// getFallback returns the none provider on Windows.
//
// None mode is the safest no-CNI fallback on Windows: the container gets a
// new, unconfigured HNS namespace with no external connectivity.  This is
// preferable to silently ignoring networking, which could cause confusing
// runtime errors.
func getFallback() (network.Provider, NetworkMode) {
	bklog.L.Warn("netproviders: no CNI config found; falling back to none network mode")
	return network.NewNoneProvider(), NetworkModeNone
}
