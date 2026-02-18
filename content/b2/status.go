package b2

import (
	"strings"

	"github.com/containerd/containerd/v2/core/content"
	"github.com/containerd/containerd/v2/pkg/filters"
	"github.com/minio/minio-go/v7"
)

func adaptStatus(status content.Status) filters.Adaptor {
	return filters.AdapterFunc(func(fieldpath []string) (string, bool) {
		if len(fieldpath) == 0 {
			return "", false
		}
		switch fieldpath[0] {
		case "ref":
			return status.Ref, true
		}

		return "", false
	})
}

var adaptUpdate = adaptWalk

func adaptWalk(info minio.ObjectInfo) filters.Adaptor {
	return filters.AdapterFunc(func(fieldpath []string) (string, bool) {
		if len(fieldpath) == 0 {
			return "", false
		}

		switch strings.ToLower(fieldpath[0]) {
		case "ref", "object", "key":
			return info.Key, true
		case "labels":
			return checkMap(fieldpath[1:], info.UserMetadata)
		}

		return "", false
	})
}

func checkMap(fieldpath []string, m map[string]string) (string, bool) {
	if len(m) == 0 {
		return "", false
	}

	value, ok := m[strings.Join(fieldpath, ".")]
	return value, ok
}
