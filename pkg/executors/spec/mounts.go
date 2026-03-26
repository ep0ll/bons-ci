package oci

import (
	"context"
	"path/filepath"
	"strings"

	"github.com/containerd/containerd/v2/core/containers"
	"github.com/containerd/containerd/v2/pkg/oci"
	specs "github.com/opencontainers/runtime-spec/specs-go"
)

// withRemovedMount returns a SpecOpt that removes any mount whose Destination
// matches the given path.  It is used, for example, to drop the /run tmpfs
// that containerd's default spec includes, so that buildkit can mount its own
// run directory there.
//
// See https://github.com/moby/buildkit/issues/429
func withRemovedMount(destination string) oci.SpecOpts {
	return func(_ context.Context, _ oci.Client, _ *containers.Container, s *specs.Spec) error {
		filtered := s.Mounts[:0] // reuse backing array; avoids an extra allocation
		for _, m := range s.Mounts {
			if m.Destination != destination {
				filtered = append(filtered, m)
			}
		}
		s.Mounts = filtered
		return nil
	}
}

// hasPrefix reports whether p is equal to prefixDir or is lexically nested
// under it.  Both paths are cleaned with filepath.Clean before comparison so
// trailing slashes, double slashes, etc. are normalised.
//
// This is the correct way to test path containment on both POSIX and Windows:
//   - It avoids the "/foobar" ⊃ "/foo" false-positive by requiring either
//     exact equality or a separator after the prefix.
//   - It handles the root ("/") case by checking whether Clean(prefixDir)
//     is the separator character.
func hasPrefix(p, prefixDir string) bool {
	prefixDir = filepath.Clean(prefixDir)
	// filepath.Base returns the last element of a path; for the root "/" that
	// equals string(filepath.Separator), making every path a descendant.
	if filepath.Base(prefixDir) == string(filepath.Separator) {
		return true
	}
	p = filepath.Clean(p)
	return p == prefixDir || strings.HasPrefix(p, prefixDir+string(filepath.Separator))
}

// dedupMounts removes duplicate mounts, keeping the last definition for each
// Destination.  This mirrors Docker and OCI-runtime semantics: when the same
// destination is mounted twice the later entry wins.
//
// The function is O(n) in time and space.  A visited map records the index in
// the output slice for each Destination so that an in-place update (ret[j] =
// mnt) is used for subsequent occurrences rather than a linear scan.
//
// Ordering guarantee: the first occurrence of each Destination remains in its
// original position; later occurrences overwrite its slot in-place.  Mounts
// that are unique remain in their original relative order.
func dedupMounts(mnts []specs.Mount) []specs.Mount {
	ret := make([]specs.Mount, 0, len(mnts))
	// visited maps Destination → index in ret.
	visited := make(map[string]int, len(mnts))
	for _, mnt := range mnts {
		if j, ok := visited[mnt.Destination]; ok {
			// Overwrite the earlier entry in place; the mount at index j is
			// completely replaced — its Source, Type, and Options all change.
			ret[j] = mnt
		} else {
			visited[mnt.Destination] = len(ret)
			ret = append(ret, mnt)
		}
	}
	return ret
}
