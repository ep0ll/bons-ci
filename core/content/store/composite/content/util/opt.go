package util

import (
	"github.com/containerd/containerd/content"
	v2content "github.com/containerd/containerd/v2/core/content"
)

func V1Opt(opt ...v2content.Opt) []content.Opt {
	v1Opt := make([]content.Opt, len(opt))

	for i := range opt {
		v1Opt[i] = func(v1Info *content.Info) error {
			info := v2content.Info(*v1Info)
			return opt[i](&info)
		}
	}

	return v1Opt
}

func V2Opt(opt ...content.Opt) []v2content.Opt {
	v2Opt := make([]v2content.Opt, len(opt))

	for i := range opt {
		v2Opt[i] = func(v2Info *v2content.Info) error {
			info := content.Info(*v2Info)
			return opt[i](&info)
		}
	}

	return v2Opt
}
