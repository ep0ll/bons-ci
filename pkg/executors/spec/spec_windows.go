//go:build windows

package oci

import (
	"context"
	"fmt"
	"os"
	"path/filepath"
	"strings"

	"github.com/containerd/containerd/v2/core/containers"
	"github.com/containerd/containerd/v2/core/mount"
	"github.com/containerd/containerd/v2/pkg/oci"
	"github.com/containerd/continuity/fs"
	"github.com/moby/buildkit/solver/llbsolver/cdidevices"
	"github.com/moby/buildkit/solver/pb"
	"github.com/moby/sys/user"
	specs "github.com/opencontainers/runtime-spec/specs-go"
	"github.com/pkg/errors"
)

const (
	// tracingSocketPath is the Windows named-pipe path inside the container
	// where the OTel collector socket is surfaced.
	tracingSocketPath = "//./pipe/otel-grpc"

	// getUserInfoExe is the container-side path where the buildkit binary is
	// mounted for re-exec as the get-user-info helper.
	getUserInfoExe = `C:\Windows\System32\get-user-info.exe`
)

// withProcessArgs translates a slice of argument strings to a Windows
// CommandLine string.  On Windows the OCI spec carries a CommandLine field
// rather than an Args slice, because CreateProcess accepts a single string and
// the argument-splitting rules differ from POSIX.
func withProcessArgs(args ...string) oci.SpecOpts {
	return oci.WithProcessCommandLine(strings.Join(args, " "))
}

// withGetUserInfoMount injects the running buildkit binary into the container
// as a read-only "get-user-info" executable.
//
// Rationale: resolving user/group IDs from /etc/passwd and /etc/group inside
// a Windows container requires running a helper binary in the container's
// context (because the host and container filesystems are separate).  Rather
// than shipping a separate binary, buildkit registers a re-exec handler under
// the name "get-user-info" that implements the lookup.  The binary is
// bind-mounted into the container at the well-known path so the OCI runtime
// can invoke it.
func withGetUserInfoMount() oci.SpecOpts {
	return func(_ context.Context, _ oci.Client, _ *containers.Container, s *specs.Spec) error {
		execPath, err := os.Executable()
		if err != nil {
			return errors.Wrap(err, "getting executable path for get-user-info mount")
		}
		s.Mounts = append(s.Mounts, specs.Mount{
			Destination: getUserInfoExe,
			Source:      execPath,
			Options:     []string{"ro"},
		})
		return nil
	}
}

// generateMountOpts returns the Windows-specific SpecOpts.  Unlike Linux there
// are no network config bind-mounts; the get-user-info binary is the only
// extra mount.
func generateMountOpts(_, _ string) []oci.SpecOpts {
	return []oci.SpecOpts{withGetUserInfoMount()}
}

// generateSecurityOpts validates security mode on Windows.  INSECURE mode is
// not supported because the Linux capability model does not exist on Windows.
func generateSecurityOpts(mode pb.SecurityMode, _ string, _ bool) ([]oci.SpecOpts, error) {
	if mode == pb.SecurityMode_INSECURE {
		return nil, errors.New("insecure mode is not supported on Windows")
	}
	return nil, nil
}

// generateProcessModeOpts validates process mode on Windows.  NoProcessSandbox
// requires Linux /proc bind-mount semantics.
func generateProcessModeOpts(mode ProcessMode) ([]oci.SpecOpts, error) {
	if mode == NoProcessSandbox {
		return nil, errors.New("NoProcessSandbox is not supported on Windows")
	}
	return nil, nil
}

func generateIDmapOpts(idmap *user.IdentityMapping) ([]oci.SpecOpts, error) {
	if idmap == nil {
		return nil, nil
	}
	return nil, errors.New("IdentityMapping is not supported on Windows")
}

func generateRlimitOpts(ulimits []*pb.Ulimit) ([]oci.SpecOpts, error) {
	if len(ulimits) == 0 {
		return nil, nil
	}
	return nil, errors.New("POSIXRlimit is not supported on Windows")
}

// getTracingSocketMount surfaces the OTel collector socket as a named pipe
// inside the container.
func getTracingSocketMount(socket string) *specs.Mount {
	return &specs.Mount{
		Destination: filepath.FromSlash(tracingSocketPath),
		Source:      socket,
		Options:     []string{"ro"},
	}
}

// getTracingSocket returns the in-container endpoint URL for the OTel
// collector named pipe.
func getTracingSocket() string {
	return fmt.Sprintf("npipe://%s", filepath.ToSlash(tracingSocketPath))
}

func cgroupV2NamespaceSupported() bool {
	return false
}

// sub resolves a sub-path within a mounted filesystem on Windows using
// fs.RootPath to prevent path traversal.
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
	// CDI on Windows is tracked at:
	// https://github.com/cncf-tags/container-device-interface/issues/28
	return nil, errors.New("CDI is not supported on Windows")
}

// normalizeMountType returns an empty string on Windows because the HCS shim
// does not accept named mount types and automatically selects the correct
// mechanism (analogous to a bind mount) based on the source path.
func normalizeMountType(_ string) string {
	return ""
}
