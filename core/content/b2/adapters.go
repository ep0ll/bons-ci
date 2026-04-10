package b2

import (
	"strings"

	"github.com/containerd/containerd/v2/core/content"
	"github.com/containerd/containerd/v2/pkg/filters"
)

// statusAdaptor exposes content.Status fields for containerd filter matching.
func statusAdaptor(status content.Status) filters.Adaptor {
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

// objectEntryAdaptor exposes ObjectEntry fields for containerd filter matching.
// Used by Walk and ListStatuses.
func objectEntryAdaptor(key string, metadata map[string]string) filters.Adaptor {
	return filters.AdapterFunc(func(fieldpath []string) (string, bool) {
		if len(fieldpath) == 0 {
			return "", false
		}
		switch strings.ToLower(fieldpath[0]) {
		case "ref", "object", "key":
			return key, true
		case "labels":
			if len(fieldpath) < 2 {
				return "", false
			}
			return mapLookup(fieldpath[1:], metadata)
		}
		return "", false
	})
}

// mapLookup joins remaining fieldpath segments with "." and looks up in m.
func mapLookup(fieldpath []string, m map[string]string) (string, bool) {
	if len(fieldpath) == 0 || len(m) == 0 {
		return "", false
	}
	value, ok := m[strings.Join(fieldpath, ".")]
	return value, ok
}
