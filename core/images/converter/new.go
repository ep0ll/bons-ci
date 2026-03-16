package converter

import (
	"github.com/containerd/platforms"
)

// IndexConvertFuncWithHook is the convert func used by Convert with hook functions support.
func IndexConvertFuncWithHook(layerConvertFunc ConvertFunc, docker2oci bool, platformMC platforms.MatchComparer, hooks ConvertHooks) ConvertFunc {
	return newConverter(layerConvertFunc, docker2oci, platformMC, hooks).convert
}

// DefaultIndexConvertFunc is the default convert func used by Convert.
func DefaultIndexConvertFunc(layerConvertFunc ConvertFunc, docker2oci bool, platformMC platforms.MatchComparer) ConvertFunc {
	return newConverter(layerConvertFunc, docker2oci, platformMC, ConvertHooks{}).convert
}
