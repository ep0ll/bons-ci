//go:build linux

package oci

import (
	"context"
	"fmt"
	"os"
	"strconv"
	"strings"
	"sync"

	"github.com/containerd/containerd/v2/core/containers"
	"github.com/containerd/containerd/v2/core/mount"
	"github.com/containerd/containerd/v2/pkg/apparmor"
	"github.com/containerd/containerd/v2/pkg/oci"
	cdseccomp "github.com/containerd/containerd/v2/pkg/seccomp"
	"github.com/containerd/continuity/fs"
	"github.com/moby/buildkit/snapshot"
	"github.com/moby/buildkit/solver/llbsolver/cdidevices"
	"github.com/moby/buildkit/solver/pb"
	"github.com/moby/buildkit/util/bklog"
	"github.com/moby/buildkit/util/entitlements/security"
	"github.com/moby/profiles/seccomp"
	"github.com/moby/sys/user"
	specs "github.com/opencontainers/runtime-spec/specs-go"
	selinux "github.com/opencontainers/selinux/go-selinux"
	"github.com/opencontainers/selinux/go-selinux/label"
	"github.com/pkg/errors"
	"golang.org/x/sys/unix"
)

const (
	// tracingSocketPath is the well-known path inside the container where the
	// OTel collector socket is bind-mounted.
	tracingSocketPath = "/dev/otel-grpc.sock"
)

var (
	cgroupNSOnce     sync.Once
	supportsCgroupNS bool
)

func withProcessArgs(args ...string) oci.SpecOpts {
	return oci.WithProcessArgs(args...)
}

// generateMountOpts returns the Linux-specific SpecOpts that set up the
// standard network config files and cgroup filesystem.
//
// The /run tmpfs that containerd's default spec includes is removed first
// (withRemovedMount) because buildkit needs to mount its own state there.
// See https://github.com/moby/buildkit/issues/429
func generateMountOpts(resolvConf, hostsFile string) []oci.SpecOpts {
	return []oci.SpecOpts{
		withRemovedMount("/run"),
		withROBind(resolvConf, "/etc/resolv.conf"),
		withROBind(hostsFile, "/etc/hosts"),
		withCGroup(),
	}
}

// generateSecurityOpts builds the security-related SpecOpts for the given
// security mode, AppArmor profile, and SELinux flag.
//
// Must be called AFTER generateMountOpts because INSECURE mode adds writable
// cgroup/sysfs mounts that would be overridden if mount opts ran after.
func generateSecurityOpts(mode pb.SecurityMode, apparmorProfile string, selinuxB bool) ([]oci.SpecOpts, error) {
	if selinuxB && !selinux.GetEnabled() {
		return nil, errors.New("selinux is not available")
	}

	switch mode {
	case pb.SecurityMode_INSECURE:
		// INSECURE mode gives the container nearly-root privileges:
		//  - All capabilities added.
		//  - Writable cgroup and sysfs (needed for nested containers).
		//  - SELinux label disabled if SELinux is active.
		// This should only be enabled when the build requires privileged
		// operations (e.g. nested docker build) AND the operator has
		// explicitly granted the insecure entitlement.
		return []oci.SpecOpts{
			security.WithInsecureSpec(),
			oci.WithWriteableCgroupfs,
			oci.WithWriteableSysfs,
			func(_ context.Context, _ oci.Client, _ *containers.Container, s *oci.Spec) error {
				if !selinuxB {
					return nil
				}
				var err error
				s.Process.SelinuxLabel, s.Linux.MountLabel, err = label.InitLabels([]string{"disable"})
				return err
			},
		}, nil

	case pb.SecurityMode_SANDBOX:
		var opts []oci.SpecOpts

		// Apply the Moby seccomp profile, which blocks the most dangerous
		// syscalls while allowing everything a typical containerised workload
		// needs.  Skipped when seccomp is not compiled in or not supported by
		// the kernel.
		if cdseccomp.IsEnabled() {
			opts = append(opts, withDefaultProfile())
		}

		if apparmorProfile != "" {
			if !apparmor.HostSupports() {
				return nil, errors.Errorf(
					"AppArmor is not supported on this host, but profile %q was specified",
					apparmorProfile,
				)
			}
			opts = append(opts, oci.WithApparmorProfile(apparmorProfile))
		}

		// Initialise SELinux labels.  For sandbox mode we request a random
		// label pair (nil options) so that each container is isolated from
		// others via MCS category enforcement.
		opts = append(opts, func(_ context.Context, _ oci.Client, _ *containers.Container, s *oci.Spec) error {
			if !selinuxB {
				return nil
			}
			var err error
			s.Process.SelinuxLabel, s.Linux.MountLabel, err = label.InitLabels(nil)
			return err
		})
		return opts, nil
	}

	return nil, nil
}

// generateProcessModeOpts returns SpecOpts that implement the requested
// process-isolation mode.
//
// Must be called AFTER generateMountOpts because NoProcessSandbox replaces
// the /proc mount with a bind of the host /proc.
func generateProcessModeOpts(mode ProcessMode) ([]oci.SpecOpts, error) {
	if mode == NoProcessSandbox {
		return []oci.SpecOpts{
			// Share the host PID namespace so the process can see (and
			// signal) all processes on the host.  Required for rootless
			// buildkit running inside a container where the "host" PID ns
			// is already isolated.
			oci.WithHostNamespace(specs.PIDNamespace),
			// Replace the private procfs with a bind of the host /proc so
			// that utilities that read /proc/N can see all host processes.
			// "rbind" is used without "ro" so that tools like strace work,
			// but maskedPaths and readonlyPaths from the default spec are
			// preserved (see withBoundProc) to limit exposure.
			withBoundProc(),
			// Specifically block ptrace so the container cannot modify host processes
			withExplicitPtraceBlock(),
		}, nil
	}
	return nil, nil
}

func generateIDmapOpts(idmap *user.IdentityMapping) ([]oci.SpecOpts, error) {
	if idmap == nil {
		return nil, nil
	}
	return []oci.SpecOpts{
		oci.WithUserNamespace(specMapping(idmap.UIDMaps), specMapping(idmap.GIDMaps)),
	}, nil
}

func specMapping(s []user.IDMap) []specs.LinuxIDMapping {
	ids := make([]specs.LinuxIDMapping, len(s))
	for i, item := range s {
		ids[i] = specs.LinuxIDMapping{
			HostID:      uint32(item.ParentID),
			ContainerID: uint32(item.ID),
			Size:        uint32(item.Count),
		}
	}
	return ids
}

func generateRlimitOpts(ulimits []*pb.Ulimit) ([]oci.SpecOpts, error) {
	if len(ulimits) == 0 {
		return nil, nil
	}
	rlimits := make([]specs.POSIXRlimit, 0, len(ulimits))
	for _, u := range ulimits {
		if u == nil {
			continue
		}
		rlimits = append(rlimits, specs.POSIXRlimit{
			Type: fmt.Sprintf("RLIMIT_%s", strings.ToUpper(u.Name)),
			Hard: uint64(u.Hard),
			Soft: uint64(u.Soft),
		})
	}
	return []oci.SpecOpts{
		func(_ context.Context, _ oci.Client, _ *containers.Container, s *specs.Spec) error {
			s.Process.Rlimits = rlimits
			return nil
		},
	}, nil
}

// generateCDIOpts injects CDI devices into the spec.
//
// CDI (Container Device Interface) is a standard for making host devices
// available inside containers.  The CDI manager refreshes its registry and
// annotates the spec with the device's mounts, env-vars, and hooks.
//
// Important: CDI injection may add mounts, environment variables, and OCI
// hooks.  None of those fields should be reset by the caller after this opt
// is applied.
func generateCDIOpts(manager *cdidevices.Manager, devs []*pb.CDIDevice) ([]oci.SpecOpts, error) {
	if len(devs) == 0 {
		return nil, nil
	}
	return []oci.SpecOpts{
		func(ctx context.Context, _ oci.Client, _ *containers.Container, s *specs.Spec) error {
			if err := manager.Refresh(); err != nil {
				bklog.G(ctx).Warnf("CDI registry refresh failed: %v", err)
			}
			if err := manager.InjectDevices(s, devs...); err != nil {
				return errors.Wrap(err, "CDI device injection failed")
			}
			return nil
		},
	}, nil
}

// withDefaultProfile applies the Moby seccomp profile.
//
// Must be called AFTER the process capabilities have been set, because the
// seccomp profile's allowed-syscall list is calibrated to the effective
// capability set.
func withDefaultProfile() oci.SpecOpts {
	return func(_ context.Context, _ oci.Client, _ *containers.Container, s *specs.Spec) error {
		var err error
		s.Linux.Seccomp, err = seccomp.GetDefaultProfile(s)
		return err
	}
}

// withROBind appends a read-only bind mount of src to dest inside the container.
func withROBind(src, dest string) oci.SpecOpts {
	return func(_ context.Context, _ oci.Client, _ *containers.Container, s *specs.Spec) error {
		s.Mounts = append(s.Mounts, specs.Mount{
			Destination: dest,
			Type:        "bind",
			Source:      src,
			Options:     []string{"nosuid", "noexec", "nodev", "rbind", "ro"},
		})
		return nil
	}
}

// withCGroup appends the cgroup v1/v2 filesystem mount.
// The mount is read-only so that build steps cannot escape their cgroup slice.
func withCGroup() oci.SpecOpts {
	return func(_ context.Context, _ oci.Client, _ *containers.Container, s *specs.Spec) error {
		s.Mounts = append(s.Mounts, specs.Mount{
			Destination: "/sys/fs/cgroup",
			Type:        "cgroup",
			Source:      "cgroup",
			Options:     []string{"ro", "nosuid", "noexec", "nodev"},
		})
		return nil
	}
}

// withBoundProc replaces the private procfs with a bind mount of the host /proc.
//
// It removes all existing /proc mounts (including recursive tmpfs or devpts
// entries that containerd may have placed under /proc), prepends a bind mount
// of the host /proc, and strips /proc prefixes from maskedPaths and
// readonlyPaths.  The masking/readonly lists are preserved for non-/proc paths
// because they limit kernel exposure for paths like /sys.
//
// Note: "rbind"+"ro" does NOT make /proc read-only recursively on Linux;
// kernel restrictions mean individual entries (like /proc/sysrq-trigger) must
// be masked explicitly.  The spec's maskedPaths list handles this.
func withBoundProc() oci.SpecOpts {
	return func(_ context.Context, _ oci.Client, _ *containers.Container, s *specs.Spec) error {
		s.Mounts = removeMountsWithPrefix(s.Mounts, "/proc")
		s.Mounts = append([]specs.Mount{{
			Destination: "/proc",
			Type:        "bind",
			Source:      "/proc",
			Options:     []string{"rbind"},
		}}, s.Mounts...)

		// Strip /proc-prefixed entries; they refer to the private procfs that
		// no longer exists in this mode.
		s.Linux.MaskedPaths = filterOutPrefix(s.Linux.MaskedPaths, "/proc")
		s.Linux.ReadonlyPaths = filterOutPrefix(s.Linux.ReadonlyPaths, "/proc")

		return nil
	}
}

// withExplicitPtraceBlock creates exactly one seccomp rule covering the
// ptrace syscall and marks it as ERRNO, effectively blocking it, while
// preserving the surrounding profile (or adding a default one if absent).
func withExplicitPtraceBlock() oci.SpecOpts {
	return func(_ context.Context, _ oci.Client, _ *containers.Container, s *specs.Spec) error {
		if s.Linux == nil {
			s.Linux = &specs.Linux{}
		}
		if s.Linux.Seccomp == nil {
			// If there's no seccomp profile yet, fetch the default Moby one.
			var err error
			s.Linux.Seccomp, err = seccomp.GetDefaultProfile(s)
			if err != nil {
				return err
			}
		}

		// Prepend a rule strictly preventing ptrace
		s.Linux.Seccomp.Syscalls = append([]specs.LinuxSyscall{
			{
				Names:  []string{"ptrace"},
				Action: specs.ActErrno,
			},
		}, s.Linux.Seccomp.Syscalls...)

		return nil
	}
}

// filterOutPrefix returns a copy of paths with all entries that have the
// given prefix removed.
func filterOutPrefix(paths []string, prefix string) []string {
	var out []string
	for _, p := range paths {
		if !hasPrefix(p, prefix) {
			out = append(out, p)
		}
	}
	return out
}

// removeMountsWithPrefix removes any mount whose Destination is lexically
// under prefixDir (inclusive).
func removeMountsWithPrefix(mounts []specs.Mount, prefixDir string) []specs.Mount {
	var ret []specs.Mount
	for _, m := range mounts {
		if !hasPrefix(m.Destination, prefixDir) {
			ret = append(ret, m)
		}
	}
	return ret
}

func getTracingSocketMount(socket string) *specs.Mount {
	return &specs.Mount{
		Destination: tracingSocketPath,
		Type:        "bind",
		Source:      socket,
		Options:     []string{"ro", "rbind"},
	}
}

func getTracingSocket() string {
	return fmt.Sprintf("unix://%s", tracingSocketPath)
}

// cgroupV2NamespaceSupported detects whether the running kernel supports
// cgroup v2 namespaces.
//
// Attempting to create a cgroup namespace on a system with a non-standard
// cgroup v1 hierarchy results in EINVAL.  We detect cgroup v2 by checking for
// the existence of /proc/self/ns/cgroup (namespace file, Linux 4.6+) AND
// /sys/fs/cgroup/cgroup.subtree_control (v2 unified hierarchy).
//
// See https://github.com/moby/buildkit/issues/4108
func cgroupV2NamespaceSupported() bool {
	cgroupNSOnce.Do(func() {
		if _, err := os.Stat("/proc/self/ns/cgroup"); os.IsNotExist(err) {
			return
		}
		if _, err := os.Stat("/sys/fs/cgroup/cgroup.subtree_control"); os.IsNotExist(err) {
			return
		}
		supportsCgroupNS = true
	})
	return supportsCgroupNS
}

// sub opens subPath within the already-mounted mount m and returns a new
// mount.Mount whose Source points directly at the sub-path.
//
// Security: the function uses the /proc/self/fd trick (similar to runc's
// WithProcfd) to prevent TOCTOU races caused by symlink substitution between
// the stat and the open.  The algorithm:
//
//  1. Resolve the absolute path of the sub-directory (fs.RootPath prevents
//     escape outside the root).
//  2. Open it with O_PATH|O_CLOEXEC to get an fd without following symlinks
//     beyond the final component.
//  3. Read /proc/self/fd/N to find the real path the fd resolves to.
//  4. If the real path differs from what we expected (symlink race), close the
//     fd and retry (up to 10 times).
//  5. Mount the fd path so the returned mount.Mount cannot be redirected by
//     later symlink changes.
func sub(m mount.Mount, subPath string) (mount.Mount, func() error, error) {
	const maxRetries = 10
	root := m.Source

	for i := 0; i < maxRetries; i++ {
		src, err := fs.RootPath(root, subPath)
		if err != nil {
			return mount.Mount{}, nil, err
		}

		fh, err := os.OpenFile(src, unix.O_PATH|unix.O_CLOEXEC, 0)
		if err != nil {
			return mount.Mount{}, nil, errors.WithStack(err)
		}

		fdPath := "/proc/self/fd/" + strconv.Itoa(int(fh.Fd()))
		resolved, err := os.Readlink(fdPath)
		if err != nil {
			fh.Close()
			return mount.Mount{}, nil, errors.WithStack(err)
		}
		if resolved != src {
			// Symlink was swapped between RootPath and OpenFile; retry.
			fh.Close()
			continue
		}

		// The fd is stable.  Mount using the fd path so the mount isn't
		// affected by future symlink changes in the source tree.
		m.Source = fdPath
		lm := snapshot.LocalMounterWithMounts([]mount.Mount{m}, snapshot.ForceRemount())
		mp, err := lm.Mount()
		if err != nil {
			fh.Close()
			return mount.Mount{}, nil, err
		}
		m.Source = mp
		fh.Close()

		return m, lm.Unmount, nil
	}

	return mount.Mount{}, nil, errors.Errorf("unable to safely resolve subpath %s after %d retries", subPath, maxRetries)
}
