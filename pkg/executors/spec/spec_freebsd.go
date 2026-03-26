//go:build freebsd

package oci

import (
	"github.com/containerd/containerd/v2/core/mount"
	"github.com/containerd/containerd/v2/pkg/oci"
	"github.com/containerd/continuity/fs"
	"github.com/moby/buildkit/solver/llbsolver/cdidevices"
	"github.com/moby/buildkit/solver/pb"
	"github.com/moby/sys/user"
	specs "github.com/opencontainers/runtime-spec/specs-go"
	"github.com/pkg/errors"
)

func withProcessArgs(args ...string) oci.SpecOpts {
	return oci.WithProcessArgs(args...)
}

// generateMountOpts is a no-op on FreeBSD; network config bind-mounts are
// Linux-specific.
func generateMountOpts(_, _ string) []oci.SpecOpts {
	return nil
}

// generateSecurityOpts validates security mode on FreeBSD.  INSECURE mode is
// not supported because the Linux-specific syscall capability model does not
// apply.
func generateSecurityOpts(mode pb.SecurityMode, _ string, _ bool) ([]oci.SpecOpts, error) {
	if mode == pb.SecurityMode_INSECURE {
		return nil, errors.New("insecure mode is not supported on FreeBSD")
	}
	return nil, nil
}

// generateProcessModeOpts validates process mode on FreeBSD.  NoProcessSandbox
// requires Linux-specific /proc bind-mount semantics.
func generateProcessModeOpts(mode ProcessMode) ([]oci.SpecOpts, error) {
	if mode == NoProcessSandbox {
		return nil, errors.New("NoProcessSandbox is not supported on FreeBSD")
	}
	return nil, nil
}

func generateIDmapOpts(idmap *user.IdentityMapping) ([]oci.SpecOpts, error) {
	if idmap == nil {
		return nil, nil
	}
	return nil, errors.New("IdentityMapping is not supported on FreeBSD")
}

func generateRlimitOpts(ulimits []*pb.Ulimit) ([]oci.SpecOpts, error) {
	if len(ulimits) == 0 {
		return nil, nil
	}
	return nil, errors.New("POSIXRlimit is not supported on FreeBSD")
}

// getTracingSocketMount is not implemented on FreeBSD.
func getTracingSocketMount(_ string) *specs.Mount {
	return nil
}

// getTracingSocket is not implemented on FreeBSD.
func getTracingSocket() string {
	return ""
}

func cgroupV2NamespaceSupported() bool {
	return false
}

// sub resolves a sub-path within a mounted filesystem on FreeBSD using
// fs.RootPath to prevent path traversal outside the root.
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
	return nil, errors.New("CDI is not supported on FreeBSD")
}
