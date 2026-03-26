//go:build !linux

package netproviders

import (
	"runtime"

	"github.com/bons/bons-ci/pkg/executors/network"
	"github.com/bons/bons-ci/pkg/executors/network/cniprovider"
	"github.com/pkg/errors"
)

// getBridgeProvider is unavailable on non-Linux platforms.
//
// Bridge mode depends on Linux kernel features (in-kernel bridge, iptables/
// nftables, network namespaces with CLONE_NEWNET) that do not exist on macOS
// or Windows.  Future work could expose a similar capability via hypervisor
// networking (e.g. gVisor netstack or Windows HNS bridge networks), at which
// point this stub would be replaced by a platform-specific implementation.
func getBridgeProvider(_ cniprovider.Opt) (network.Provider, error) {
	return nil, errors.Errorf(
		"netproviders: bridge network mode is not supported on %s", runtime.GOOS)
}
