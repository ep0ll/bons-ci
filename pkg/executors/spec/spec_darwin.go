//go:build darwin

package oci

import (
	"github.com/containerd/containerd/v2/core/mount"
	"github.com/containerd/containerd/v2/pkg/oci"
	"github.com/containerd/continuity/fs"
	"github.com/moby/buildkit/solver/llbsolver/cdidevices"
	"github.com/moby/buildkit/solver/pb"
	"github.com/moby/sys/user"
	"github.com/opencontainers/runtime-spec/specs-go"
	"github.com/pkg/errors"
)

func withProcessArgs(args ...string) oci.SpecOpts {
	return oci.WithProcessArgs(args...)
}

// generateMountOpts is a no-op on Darwin; network config files are not
// bind-mounted because Darwin does not use the Linux container runtime model.
func generateMountOpts(_, _ string) []oci.SpecOpts {
	return nil
}

// generateSecurityOpts is a no-op on Darwin; security modes (INSECURE,
// seccomp, AppArmor, SELinux) are Linux-only.
func generateSecurityOpts(_ pb.SecurityMode, _ string, _ bool) ([]oci.SpecOpts, error) {
	return nil, nil
}

// generateProcessModeOpts is a no-op on Darwin; PID namespaces are Linux-only.
func generateProcessModeOpts(_ ProcessMode) ([]oci.SpecOpts, error) {
	return nil, nil
}

func generateIDmapOpts(idmap *user.IdentityMapping) ([]oci.SpecOpts, error) {
	if idmap == nil {
		return nil, nil
	}
	return nil, errors.New("IdentityMapping is not supported on Darwin")
}

func generateRlimitOpts(ulimits []*pb.Ulimit) ([]oci.SpecOpts, error) {
	if len(ulimits) == 0 {
		return nil, nil
	}
	return nil, errors.New("POSIXRlimit is not supported on Darwin")
}

// getTracingSocketMount is not implemented on Darwin.
func getTracingSocketMount(_ string) *specs.Mount {
	return nil
}

// getTracingSocket is not implemented on Darwin.
func getTracingSocket() string {
	return ""
}

func cgroupV2NamespaceSupported() bool {
	return false
}

// sub resolves a sub-path within a mounted filesystem on Darwin.  Unlike the
// Linux implementation it does not use the /proc/self/fd TOCTOU-resistance
// technique because Darwin does not have /proc.  The path is resolved with
// fs.RootPath (preventing traversal outside the root) and returned directly.
func sub(m mount.Mount, subPath string) (mount.Mount, func() error, error) {
	src, err := fs.RootPath(m.Source, subPath)
	if err != nil {
		return mount.Mount{}, nil, err
	}
	m.Source = src
	return m, func() error { return nil }, nil
}

func generateCDIOpts(_ *cdidevices.Manager, devices []*pb.CDIDevice) ([]oci.SpecOpts, error) {
	if len(devices) == 0 {
		return nil, nil
	}
	return nil, errors.New("CDI is not supported on Darwin")
}
