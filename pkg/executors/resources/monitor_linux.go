//go:build linux

package resources

import "golang.org/x/sys/unix"

// isCgroup2 reports whether the cgroupv2 unified hierarchy is mounted at
// defaultMountpoint.  It does so by checking that statfs(2) reports the
// CGROUP2_SUPER_MAGIC filesystem type (0x63677270).
//
// This function is called exactly once per process lifetime (via cgroupV2Once
// in cgroup.go).  The result is cached in the package-level isCgroupV2 bool.
func isCgroup2() bool {
	var st unix.Statfs_t
	if err := unix.Statfs(defaultMountpoint, &st); err != nil {
		return false
	}
	return st.Type == unix.CGROUP2_SUPER_MAGIC
}
