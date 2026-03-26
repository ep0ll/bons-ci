package oci

import (
	"os"

	"github.com/moby/sys/user"
	"github.com/pkg/errors"
)

// atomicWriteFile writes data to path via a temp-file rename, guaranteeing
// that concurrent readers never observe a partially-written file.
//
// Write order is intentionally: write → chown → rename, rather than
// write → rename → chown. Chowning after rename would leave a window where
// a container could read a root-owned file before the ownership is fixed.
// The downside is that the rename is not truly atomic across the chown, but
// for buildkit's use-case (generating config files before container start)
// this is acceptable.
//
// If idmap is non-nil the temp file is chowned to the idmap root pair so that
// rootless containers can read the generated file.
func atomicWriteFile(path string, data []byte, perm os.FileMode, idmap *user.IdentityMapping) error {
	tmp := path + ".tmp"

	if err := os.WriteFile(tmp, data, perm); err != nil {
		return errors.WithStack(err)
	}

	if idmap != nil {
		uid, gid := idmap.RootPair()
		if err := os.Chown(tmp, uid, gid); err != nil {
			// Clean up the orphaned temp file so repeated calls don't
			// accumulate stale files.
			_ = os.Remove(tmp)
			return errors.WithStack(err)
		}
	}

	return errors.WithStack(os.Rename(tmp, path))
}
