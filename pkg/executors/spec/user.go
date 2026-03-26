package oci

import (
	"context"
	"os"
	"slices"
	"strconv"
	"strings"

	"github.com/containerd/containerd/v2/core/containers"
	containerdoci "github.com/containerd/containerd/v2/pkg/oci"
	"github.com/containerd/continuity/fs"
	"github.com/moby/sys/user"
	specs "github.com/opencontainers/runtime-spec/specs-go"
	"github.com/pkg/errors"
)

// GetUser resolves a username/UID string to a (uid, gid, supplementaryGIDs)
// triple by consulting the container's /etc/passwd and /etc/group files rooted
// at root.
//
// username may be any of the forms accepted by user.GetExecUser:
//   - ""           → uid=0, gid=0 (root)
//   - "1000"       → uid=1000 with the gid from /etc/passwd
//   - "1000:1000"  → uid=1000, gid=1000 (fast path; no file I/O)
//   - "alice"      → resolved via /etc/passwd
//   - "alice:sudo" → user from /etc/passwd, group from /etc/group
func GetUser(root, username string) (uid, gid uint32, sgids []uint32, err error) {
	// Empty string means root; we handle it explicitly so that a container
	// that doesn't have /etc/passwd still works.
	isDefault := username == ""
	if isDefault {
		username = "0"
	}

	// Fast path: if username is already "uid:gid" we can skip all file I/O.
	if uid, gid, err := ParseUIDGID(username); err == nil {
		return uid, gid, nil, nil
	}

	// Slow path: resolve via /etc/passwd and /etc/group.
	passwdFile, err := openUserFile(root, "/etc/passwd")
	if err == nil {
		defer passwdFile.Close()
	}
	groupFile, err := openUserFile(root, "/etc/group")
	if err == nil {
		defer groupFile.Close()
	}

	execUser, err := user.GetExecUser(username, nil, passwdFile, groupFile)
	if err != nil {
		if isDefault {
			// A missing /etc/passwd in a scratch container is expected; fall
			// back to root rather than propagating the error.
			return 0, 0, nil, nil
		}
		return 0, 0, nil, err
	}

	sgids = make([]uint32, len(execUser.Sgids))
	for i, g := range execUser.Sgids {
		sgids[i] = uint32(g)
	}
	return uint32(execUser.Uid), uint32(execUser.Gid), sgids, nil
}

// ParseUIDGID parses a "uid:gid" string into numeric UID and GID values.
//
// Both components must be present; a bare UID such as "1000" is rejected
// (use GetUser for that).  Either component may be the literal string "root"
// (treated as 0).
//
// This is the fast path used by GetUser when the caller has already expressed
// the identity numerically, avoiding all /etc/passwd file I/O.
func ParseUIDGID(str string) (uid, gid uint32, err error) {
	if str == "" {
		return 0, 0, nil
	}
	parts := strings.SplitN(str, ":", 2)
	if len(parts) != 2 {
		return 0, 0, errors.New("expected uid:gid format")
	}
	if uid, err = parseUID(parts[0]); err != nil {
		return 0, 0, err
	}
	if gid, err = parseUID(parts[1]); err != nil {
		return 0, 0, err
	}
	return uid, gid, nil
}

// openUserFile opens a path within the container rootfs safely using
// fs.RootPath to prevent path-traversal attacks.
func openUserFile(root, p string) (*os.File, error) {
	resolved, err := fs.RootPath(root, p)
	if err != nil {
		return nil, err
	}
	return os.Open(resolved)
}

// parseUID converts a UID string to a uint32.  The special string "root" is
// treated as 0.
func parseUID(str string) (uint32, error) {
	if str == "root" {
		return 0, nil
	}
	v, err := strconv.ParseUint(str, 10, 32)
	if err != nil {
		return 0, err
	}
	return uint32(v), nil
}

// WithUIDGID sets the process UID, GID, and supplementary GIDs on the spec.
//
// This is a temporary shim for the missing supplementary-GID support in
// containerd's own WithUserID / WithUsername opts.  Once the upstream fix is
// merged and adopted this function should be removed.
//
// FIXME: Remove once https://github.com/containerd/containerd is updated.
func WithUIDGID(uid, gid uint32, sgids []uint32) containerdoci.SpecOpts {
	return func(_ context.Context, _ containerdoci.Client, _ *containers.Container, s *containerdoci.Spec) error {
		setProcess(s)
		s.Process.User.UID = uid
		s.Process.User.GID = gid
		s.Process.User.AdditionalGids = sgids
		ensureAdditionalGids(s)
		return nil
	}
}

// setProcess initialises s.Process to a zero value if it is nil.
// Containerd's SpecOpts sometimes assume Process is non-nil.
//
// FIXME: Remove once containerd initialises Process unconditionally.
func setProcess(s *containerdoci.Spec) {
	if s.Process == nil {
		s.Process = &specs.Process{}
	}
}

// ensureAdditionalGids guarantees that the primary GID is included in
// AdditionalGids.  This matches the behaviour of newuidmap/newgidmap and
// avoids a class of "permission denied" errors when a process checks its own
// supplementary groups.
//
// Reference: https://github.com/containerd/containerd/blob/v1.7.0-beta.4/oci/spec_opts.go#L124-L133
func ensureAdditionalGids(s *containerdoci.Spec) {
	setProcess(s)
	if slices.Contains(s.Process.User.AdditionalGids, s.Process.User.GID) {
		return
	}
	// Prepend so that the primary GID is always the first supplementary GID,
	// matching the Linux kernel's expectation.
	s.Process.User.AdditionalGids = append(
		[]uint32{s.Process.User.GID},
		s.Process.User.AdditionalGids...,
	)
}
