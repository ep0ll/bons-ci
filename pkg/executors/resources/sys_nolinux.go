//go:build !linux

package resources

import resourcestypes "github.com/bons/bons-ci/pkg/executors/resources/types"

// newSysSampler returns nil on non-Linux platforms where /proc is not available.
// The Monitor gracefully handles a nil sysSampler.
func newSysSampler() (*Sampler[*resourcestypes.SysSample], error) {
	return nil, nil
}
