package util

import (
	"github.com/containerd/containerd/content"
	v2content "github.com/containerd/containerd/v2/core/content"
)

func V2WalkFunc(v1Walk content.WalkFunc) v2content.WalkFunc {
	return func(i v2content.Info) error {
		return v1Walk(content.Info(i))
	}
}

func V1WalkFunc(v1Walk v2content.WalkFunc) content.WalkFunc {
	return func(i content.Info) error {
		return v1Walk(v2content.Info(i))
	}
}
