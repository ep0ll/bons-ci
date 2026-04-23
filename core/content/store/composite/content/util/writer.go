package util

import (
	"github.com/containerd/containerd/content"
	v2content "github.com/containerd/containerd/v2/core/content"
)

func V2ContentWriterOpt(opt ...content.WriterOpt) []v2content.WriterOpt {
	v2Opt := make([]v2content.WriterOpt, len(opt))
	for i := range opt {
		v2Opt[i] = func(wo *v2content.WriterOpts) error {
			var v1Wo content.WriterOpts
			if wo != nil {
				v1Wo = content.WriterOpts(*wo)
			}
			return opt[i](&v1Wo)
		}
	}

	return v2Opt
}

func V1ContentWriterOpt(opt ...v2content.WriterOpt) []content.WriterOpt {
	v1Opt := make([]content.WriterOpt, len(opt))
	for i := range opt {
		v1Opt[i] = func(wo *content.WriterOpts) error {
			var v2Wo v2content.WriterOpts
			if wo != nil {
				v2Wo = v2content.WriterOpts(*wo)
			}
			return opt[i](&v2Wo)
		}
	}

	return v1Opt
}
