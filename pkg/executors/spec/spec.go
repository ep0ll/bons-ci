package oci

import (
	"context"
	"os"
	"path"
	"path/filepath"
	"runtime"
	"strings"
	"sync"

	"github.com/containerd/containerd/v2/core/containers"
	"github.com/containerd/containerd/v2/core/mount"
	"github.com/containerd/containerd/v2/pkg/namespaces"
	"github.com/containerd/containerd/v2/pkg/oci"
	"github.com/mitchellh/hashstructure/v2"
	"github.com/bons/bons-ci/pkg/executors"
	"github.com/moby/buildkit/snapshot"
	"github.com/moby/buildkit/solver/llbsolver/cdidevices"
	"github.com/bons/bons-ci/pkg/executors/network"
	rootlessmountopts "github.com/moby/buildkit/util/rootless/mountopts"
	"github.com/moby/buildkit/util/system"
	traceexec "github.com/moby/buildkit/util/tracing/exec"
	"github.com/moby/sys/user"
	"github.com/moby/sys/userns"
	specs "github.com/opencontainers/runtime-spec/specs-go"
	"github.com/opencontainers/selinux/go-selinux"
	"github.com/pkg/errors"
)

// ProcessMode configures how PID namespaces are managed for build containers.
type ProcessMode int

const (
	// ProcessSandbox (default) gives each container a private PID namespace
	// and a fresh procfs mount.  This is the safe default.
	ProcessSandbox ProcessMode = iota

	// NoProcessSandbox shares the host PID namespace and bind-mounts the
	// host procfs.  This allows containers to kill and potentially ptrace any
	// process running in the buildkit host namespace.  Enable only when
	// buildkit itself runs in a container as an unprivileged user (rootless
	// mode), where the "host" PID namespace is already isolated.
	NoProcessSandbox
)

func (pm ProcessMode) String() string {
	switch pm {
	case ProcessSandbox:
		return "sandbox"
	case NoProcessSandbox:
		return "no-sandbox"
	default:
		return ""
	}
}

// tracingEnvVars are injected into the container environment when a tracing
// socket is configured.  They tell the OTel SDK inside the build step to
// export spans over the socket that GenerateSpec bind-mounts into the container.
var tracingEnvVars = []string{
	"OTEL_TRACES_EXPORTER=otlp",
	"OTEL_EXPORTER_OTLP_TRACES_ENDPOINT=" + getTracingSocket(),
	"OTEL_EXPORTER_OTLP_TRACES_PROTOCOL=grpc",
}

// overlayMountOptsSizeThreshold is the maximum byte-length of overlay mount
// options before we compact the mount (mount it locally and replace it with
// a bind mount).  Overlay options are passed in a single page; leaving a
// 512-byte safety margin avoids hitting the kernel's page-size limit.
const overlayMountOptsSizeThreshold = 4096 - 512

// GenerateSpec generates an OCI runtime spec for a build container.
//
// The function proceeds in five phases:
//
//  1. Container identity & cgroup placement.
//  2. Option chain assembly (platform mounts, security, process mode, ID
//     mapping, rlimits, tracing, CDI devices).
//  3. Base spec generation via containerd.
//  4. Post-spec fixups (cgroup namespace, rlimit reset, network namespace).
//  5. Mount resolution: each executor.Mount is mounted, optionally compacted,
//     optionally sub-pathed, and appended to the spec.  Tracing socket mount
//     is added here too.  Finally the mount list is deduplicated and, for
//     rootless environments, fixed up.
//
// The returned func() must be called when the container exits; it unmounts any
// temporarily mounted paths and releases SELinux labels.
//
// opts are applied before the process, hostname, and mount settings are set,
// so they cannot override those fields.
func GenerateSpec(
	ctx context.Context,
	meta executor.Meta,
	mounts []executor.Mount,
	id, resolvConf, hostsFile string,
	namespace network.Namespace,
	cgroupParent string,
	processMode ProcessMode,
	idmap *user.IdentityMapping,
	apparmorProfile string,
	selinuxB bool,
	tracingSocket string,
	cdiManager *cdidevices.Manager,
	opts ...oci.SpecOpts,
) (*specs.Spec, func(), error) {
	c := &containers.Container{ID: id}

	// ── Phase 1: cgroup placement ────────────────────────────────────────────
	opts, err := appendCgroupOpt(opts, meta, cgroupParent, id)
	if err != nil {
		return nil, nil, err
	}

	// containerd's GenerateSpec requires a namespace in the context; it is
	// used to namespace specs.Linux.CgroupsPath when generated.
	if _, ok := namespaces.Namespace(ctx); !ok {
		ctx = namespaces.WithNamespace(ctx, "buildkit")
	}

	// ── Phase 2: option chain ────────────────────────────────────────────────
	opts = append(opts, generateMountOpts(resolvConf, hostsFile)...)

	if secOpts, err := generateSecurityOpts(meta.SecurityMode, apparmorProfile, selinuxB); err != nil {
		return nil, nil, err
	} else {
		opts = append(opts, secOpts...)
	}

	if pmOpts, err := generateProcessModeOpts(processMode); err != nil {
		return nil, nil, err
	} else {
		opts = append(opts, pmOpts...)
	}

	if idOpts, err := generateIDmapOpts(idmap); err != nil {
		return nil, nil, err
	} else {
		opts = append(opts, idOpts...)
	}

	if rlOpts, err := generateRlimitOpts(meta.Ulimit); err != nil {
		return nil, nil, err
	} else {
		opts = append(opts, rlOpts...)
	}

	hostname := defaultHostname
	if meta.Hostname != "" {
		hostname = meta.Hostname
	}

	if tracingSocket != "" {
		// Inject OTel environment variables so that build steps that link
		// against an OTel SDK automatically export spans to the collector.
		meta.Env = append(meta.Env, tracingEnvVars...)
		meta.Env = append(meta.Env, traceexec.Environ(ctx)...)
	}

	opts = append(opts,
		withProcessArgs(meta.Args...),
		oci.WithEnv(meta.Env),
		oci.WithProcessCwd(meta.Cwd),
		oci.WithNewPrivileges,
		oci.WithHostname(hostname),
	)

	if cdiManager != nil {
		if cdiOpts, err := generateCDIOpts(cdiManager, meta.CDIDevices); err != nil {
			return nil, nil, err
		} else {
			opts = append(opts, cdiOpts...)
		}
	}

	// ── Phase 3: base spec ───────────────────────────────────────────────────
	s, err := oci.GenerateSpec(ctx, nil, c, opts...)
	if err != nil {
		return nil, nil, errors.WithStack(err)
	}

	// ── Phase 4: post-spec fixups ────────────────────────────────────────────
	if cgroupV2NamespaceSupported() {
		s.Linux.Namespaces = append(s.Linux.Namespaces, specs.LinuxNamespace{
			Type: specs.CgroupNamespace,
		})
	}

	// Containerd sets a default NOFILE rlimit in the generated spec; reset it
	// when the caller has not explicitly requested any rlimits so that the
	// container inherits the daemon's limit rather than an arbitrary default.
	if len(meta.Ulimit) == 0 {
		s.Process.Rlimits = nil
	}

	if err := namespace.Set(s); err != nil {
		return nil, nil, errors.WithStack(err)
	}

	// ── Phase 5: mount resolution ────────────────────────────────────────────
	sm := &submounts{}
	var releasers []func() error

	releaseAll := func() {
		sm.cleanup()
		// Mount teardowns MUST execute in strict LIFO order (Reverse).
		// Unmounting a lower directory while an upper directory is still mounted
		// fails silently with EBUSY, permanently leaking the kernel mount graphs.
		for i := len(releasers) - 1; i >= 0; i-- {
			releasers[i]()
		}
		if s.Process.SelinuxLabel != "" {
			selinux.ReleaseLabel(s.Process.SelinuxLabel)
		}
	}

	for _, m := range mounts {
		if m.Src == nil {
			releaseAll()
			return nil, nil, errors.Errorf("mount %s has no source", m.Dest)
		}
		mountable, err := m.Src.Mount(ctx, m.Readonly)
		if err != nil {
			releaseAll()
			return nil, nil, errors.Wrapf(err, "failed to mount %s", m.Dest)
		}

		mnts, release, err := mountable.Mount()
		if err != nil {
			releaseAll()
			return nil, nil, errors.WithStack(err)
		}
		releasers = append(releasers, release)

		for _, mnt := range mnts {
			mnt, release, err = compactLongOverlayMount(mnt, m.Readonly)
			if err != nil {
				releaseAll()
				return nil, nil, err
			}
			if release != nil {
				releasers = append(releasers, release)
			}

			mnt, err = sm.subMount(mnt, m.Selector)
			if err != nil {
				releaseAll()
				// Improve the error message: if the path error ends with the
				// selector, replace it with just the selector so the user sees
				// a clean relative path rather than an internal absolute path.
				var pathErr *os.PathError
				if errors.As(err, &pathErr) && strings.HasSuffix(pathErr.Path, m.Selector) {
					pathErr.Path = m.Selector
				}
				return nil, nil, err
			}

			s.Mounts = append(s.Mounts, specs.Mount{
				Destination: system.GetAbsolutePath(m.Dest),
				Type:        normalizeMountType(mnt.Type),
				Source:      mnt.Source,
				Options:     mnt.Options,
			})
		}
	}

	// Bind-mount the OTel collector socket into the container so that build
	// steps can export spans without network access.
	// moby/buildkit#4764: stat the socket first to avoid adding a dead bind.
	if tracingSocket != "" {
		if _, err := os.Stat(tracingSocket); err == nil {
			if tmount := getTracingSocketMount(tracingSocket); tmount != nil {
				s.Mounts = append(s.Mounts, *tmount)
			}
		}
	}

	// Last occurrence wins for duplicate destinations (e.g. /dev/shm
	// overridden by a user-supplied bind mount).
	s.Mounts = dedupMounts(s.Mounts)

	// In rootless (user-namespace) mode some mount types are not permitted.
	// FixUpOCI translates them to equivalent user-accessible forms.
	if userns.RunningInUserNS() {
		s.Mounts, err = rootlessmountopts.FixUpOCI(s.Mounts)
		if err != nil {
			releaseAll()
			return nil, nil, err
		}
	}

	return s, releaseAll, nil
}

// appendCgroupOpt resolves the cgroup path for this container and appends a
// WithCgroup option to opts.  meta.CgroupParent takes precedence over the
// executor-level cgroupParent if both are set.
//
// Two cgroup path formats are supported:
//
//  systemd slice:  "system.slice:" → "system.slice:{id}"
//  plain path:     "buildkit"      → "/buildkit/buildkit/{id}"
func appendCgroupOpt(opts []oci.SpecOpts, meta executor.Meta, cgroupParent, id string) ([]oci.SpecOpts, error) {
	if meta.CgroupParent != "" {
		cgroupParent = meta.CgroupParent
	}
	if cgroupParent == "" {
		return opts, nil
	}

	var cgroupsPath string
	lastChar := cgroupParent[len(cgroupParent)-1:]
	if strings.Contains(cgroupParent, ".slice") && lastChar == ":" {
		// systemd transient-unit style: "parent.slice:prefix:" + container-id
		cgroupsPath = cgroupParent + id
	} else {
		cgroupsPath = filepath.Join("/", cgroupParent, "buildkit", id)
	}

	return append(opts, oci.WithCgroup(cgroupsPath)), nil
}

// ── Submount machinery ──────────────────────────────────────────────────────
//
// submounts caches mounted filesystems so that multiple mounts of the same
// source that differ only in subPath can share a single Mount() call.
//
// How it works:
//  1. The first time a mount is seen its content is mounted to a temp dir
//     and recorded in s.m keyed by the hash of the mount descriptor.
//  2. sub() is called with the mounted temp dir and the subPath; it opens
//     the sub-path safely (TOCTOU-resistant on Linux via O_PATH + /proc/fd)
//     and returns a new mount.Mount pointing inside the temp dir.
//  3. On cleanup, sub-mounts are unmounted first, then the parent.

type mountRef struct {
	mount   mount.Mount
	unmount func() error
	subRefs map[string]mountRef
}

type submounts struct {
	m map[uint64]mountRef
}

func (s *submounts) subMount(m mount.Mount, subPath string) (mount.Mount, error) {
	// No sub-path requested; return the mount as-is (except on Windows where
	// the submounting machinery is always required).
	if path.Join("/", subPath) == "/" && runtime.GOOS != "windows" {
		return m, nil
	}

	if s.m == nil {
		s.m = make(map[uint64]mountRef)
	}

	h, err := hashstructure.Hash(m, hashstructure.FormatV2, nil)
	if err != nil {
		return mount.Mount{}, errors.WithStack(err)
	}

	// If the root of this mount has already been established, look up or
	// create the sub-path reference within it.
	if mr, ok := s.m[h]; ok {
		if sm, ok := mr.subRefs[subPath]; ok {
			return sm.mount, nil
		}
		sm, unmount, err := sub(mr.mount, subPath)
		if err != nil {
			return mount.Mount{}, err
		}
		mr.subRefs[subPath] = mountRef{mount: sm, unmount: unmount}
		return sm, nil
	}

	// First time seeing this mount: mount it to a temp dir and record.
	lm := snapshot.LocalMounterWithMounts([]mount.Mount{m})
	mp, err := lm.Mount()
	if err != nil {
		return mount.Mount{}, err
	}

	s.m[h] = mountRef{
		mount:   bind(mp, m.ReadOnly()),
		unmount: lm.Unmount,
		subRefs: make(map[string]mountRef),
	}

	sm, unmount, err := sub(s.m[h].mount, subPath)
	if err != nil {
		return mount.Mount{}, err
	}
	s.m[h].subRefs[subPath] = mountRef{mount: sm, unmount: unmount}
	return sm, nil
}

// cleanup unmounts all sub-paths (in parallel) and then all parents.
func (s *submounts) cleanup() {
	var wg sync.WaitGroup
	wg.Add(len(s.m))
	for _, mr := range s.m {
		mr := mr
		go func() {
			defer wg.Done()
			for _, sm := range mr.subRefs {
				sm.unmount()
			}
			mr.unmount()
		}()
	}
	wg.Wait()
}

// bind constructs a bind-mount descriptor pointing at p.
// On Windows the mount type is left empty; the HCS shim does not accept named
// types and uses a mechanism analogous to bind mounts automatically.
func bind(p string, ro bool) mount.Mount {
	m := mount.Mount{Source: p}
	if runtime.GOOS != "windows" {
		m.Type = "bind"
		m.Options = []string{"rbind"}
	}
	if ro {
		m.Options = append(m.Options, "ro")
	}
	return m
}

// compactLongOverlayMount replaces an overlay mount whose option string
// exceeds a single kernel page with a local bind mount.
//
// Overlay mounts pass all options in a single string to the kernel; if that
// string exceeds the page size the mount(2) syscall fails with EINVAL.  This
// is common when a snapshot has a very deep layer chain.
//
// The compaction mounts the overlay locally (creating a merged view), then
// replaces it with a simple bind mount of the merged dir.  The unmount func
// is returned so the caller can clean up after the container exits.
func compactLongOverlayMount(m mount.Mount, ro bool) (mount.Mount, func() error, error) {
	if m.Type != "overlay" {
		return m, nil, nil
	}

	sz := 0
	for _, opt := range m.Options {
		sz += len(opt) + 1 // +1 for the comma separator
	}
	if sz < overlayMountOptsSizeThreshold {
		return m, nil, nil
	}

	lm := snapshot.LocalMounterWithMounts([]mount.Mount{m})
	mp, err := lm.Mount()
	if err != nil {
		return mount.Mount{}, nil, err
	}
	return bind(mp, ro), lm.Unmount, nil
}
