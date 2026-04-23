package util

import (
	"github.com/containerd/containerd/content"
	v2content "github.com/containerd/containerd/v2/core/content"
)

func V1Statuses(statuses ...v2content.Status) []content.Status {
	out := make([]content.Status, len(statuses))
	for i := range statuses {
		out[i] = content.Status(statuses[i])
	}

	return out
}

func V2Statuses(statuses ...content.Status) []v2content.Status {
	out := make([]v2content.Status, len(statuses))
	for i := range statuses {
		out[i] = v2content.Status(statuses[i])
	}

	return out
}
