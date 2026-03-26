//go:build !windows

package netproviders

import (
	"github.com/moby/buildkit/util/bklog"
	"github.com/bons/bons-ci/pkg/executors/network"
)

// getHostProvider returns the host-network provider on Unix platforms.
//
// Host networking is supported on all Unix-like systems where the daemon can
// place containers into its own network namespace via the OCI spec.
func getHostProvider() (network.Provider, bool) {
	return network.NewHostProvider(), true
}

// getFallback returns the platform-appropriate default provider when neither
// CNI nor bridge networking is available.
//
// On Unix, host networking is the safest fallback: it requires no CNI plugins
// and no extra privileges beyond those already required to run containers.
// A warning is logged because host networking has security implications.
func getFallback() (network.Provider, NetworkMode) {
	bklog.L.Warn("netproviders: falling back to host network mode (no CNI config found)")
	return network.NewHostProvider(), NetworkModeHost
}
