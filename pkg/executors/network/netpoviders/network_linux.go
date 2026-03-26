//go:build linux

package netproviders

import (
	"github.com/bons/bons-ci/pkg/executors/network"
	"github.com/bons/bons-ci/pkg/executors/network/cniprovider"
)

// getBridgeProvider returns a bridge-based CNI provider.  Bridge mode is
// available on Linux only because it requires in-kernel bridge, netfilter, and
// Linux network namespaces.
func getBridgeProvider(opt cniprovider.Opt) (network.Provider, error) {
	return cniprovider.NewBridge(opt)
}
