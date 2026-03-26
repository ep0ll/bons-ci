//go:build !linux

package cniprovider

import resourcestypes "github.com/bons/bons-ci/pkg/executors/resources/types"

// sample is a no-op on non-Linux platforms.  sysfs /sys/class/net is a Linux
// kernel interface; Windows and macOS expose network counters via different
// (and not yet integrated) mechanisms.
func (ns *cniNS) sample() (*resourcestypes.NetworkSample, error) {
	return nil, nil
}
