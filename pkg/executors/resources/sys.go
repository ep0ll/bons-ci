package resources

import resourcestypes "github.com/bons/bons-ci/pkg/executors/resources/types"

// SysSampler is the concrete type alias for a subscription into the
// system-wide sampler.  Callers that need to record system stats for an
// interval can obtain one from the global sampler exposed here.
type SysSampler = Sub[*resourcestypes.SysSample]

// NewSysSampler creates a standalone system-wide Sampler that is independent
// of any Monitor instance.  This is useful for components that need system
// stats but do not participate in the Monitor lifecycle (e.g. the CLI).
//
// Returns (nil, nil) on non-Linux platforms.
func NewSysSampler() (*Sampler[*resourcestypes.SysSample], error) {
	return newSysSampler()
}
