//go:build linux

// Package runcexecutor provides a BuildKit Executor backed by runc.
//
// # Architecture
//
// See errors.go for the package-level doc comment with the full data-flow
// overview.
//
// # Concurrency model
//
//   - Executor is safe for concurrent use.  The only shared mutable state is
//     the ContainerRegistry, which is internally synchronised.
//   - Each Run / Exec call is entirely self-contained: it creates its own
//     ProcessHandle, errgroup, and Killer; no state leaks between calls.
//
// # Error handling
//
//   - Internal errors (failed to mount rootfs, bad spec, etc.) are returned
//     directly.
//   - Process exits (non-zero code, OOM) are wrapped in *ExitError and annotated
//     by buildExitError before being returned.
//   - Context cancellation causes the in-container process to be SIGKILL'd;
//     the resulting ExitError carries Cause == ctx.Err().
package runcexecutor

import (
	"context"
	"encoding/json"
	"io"
	"os"
	"path/filepath"
	"slices"
	"sync"
	"time"

	"go.opentelemetry.io/otel/attribute"
	"go.opentelemetry.io/otel/trace"

	"github.com/containerd/containerd/v2/core/mount"
	containerdoci "github.com/containerd/containerd/v2/pkg/oci"
	"github.com/containerd/continuity/fs"
	runc "github.com/containerd/go-runc"
	"github.com/bons/bons-ci/pkg/executors"
	oci "github.com/bons/bons-ci/pkg/executors/spec"
	"github.com/bons/bons-ci/pkg/executors/resources"
	resourcestypes "github.com/bons/bons-ci/pkg/executors/resources/types"
	gatewayapi "github.com/moby/buildkit/frontend/gateway/pb"
	"github.com/moby/buildkit/identity"
	"github.com/moby/buildkit/solver/pb"
	"github.com/moby/buildkit/util/bklog"
	"github.com/bons/bons-ci/pkg/executors/network"
	rootlessspecconv "github.com/moby/buildkit/util/rootless/specconv"
	"github.com/moby/buildkit/util/stack"
	"github.com/moby/sys/user"
	specs "github.com/opencontainers/runtime-spec/specs-go"
	"github.com/pkg/errors"
)

// ─── Executor ────────────────────────────────────────────────────────────────

// Executor is the runc-backed BuildKit executor.
// Construct it with New; the zero value is not valid.
type Executor struct {
	// runtime is the configured go-runc client.
	runtime *runc.Runc

	// config holds all tunable parameters.
	config Config

	// networkProviders maps NetMode → Provider for namespace allocation.
	networkProviders map[pb.NetMode]network.Provider

	// registry tracks containers that are currently running (or being started),
	// so that Exec() can wait for them and detect early exits.
	registry *ContainerRegistry

	// oomDetector reads cgroup memory events after container exit.
	oomDetector *OOMDetector
}

// New creates a new Executor from cfg.
//
// New validates the configuration (resolves the runc binary, prepares the
// root directory) and returns an error if anything is mis-configured.
func New(cfg Config, networkProviders map[pb.NetMode]network.Provider) (*Executor, error) {
	binaryPath, err := cfg.resolveRuncBinary()
	if err != nil {
		return nil, err
	}

	root, err := prepareRoot(cfg.Root)
	if err != nil {
		return nil, err
	}
	cfg.Root = root // store canonical path

	rt := &runc.Runc{
		Command:   binaryPath,
		Log:       filepath.Join(root, "runc-log.json"),
		LogFormat: runc.JSON,
		Setpgid:   true,
		// We intentionally do NOT pass --rootless=(true|false) so that
		// non-runc OCI runtimes with the same interface also work.
	}
	applyHostOSRuncDefaults(rt)

	return &Executor{
		runtime:          rt,
		config:           cfg,
		networkProviders: networkProviders,
		registry:         NewContainerRegistry(),
		oomDetector:      DefaultOOMDetector,
	}, nil
}

// ─── Run ─────────────────────────────────────────────────────────────────────

// Run implements executor.Executor.
//
// It creates a new container, starts the given process inside it, and blocks
// until the process exits.  The container is always deleted on return.
//
// started is closed (once) when the container has started; it may be nil.
// The returned Recorder captures cgroup resource usage; nil is returned when
// resource monitoring is not enabled.
func (e *Executor) Run(
	ctx context.Context,
	id string,
	rootMount executor.Mount,
	mounts []executor.Mount,
	process executor.ProcessInfo,
	started chan<- struct{},
) (rec resourcestypes.Recorder, err error) {
	// ── container identity ─────────────────────────────────────────────────
	if id == "" {
		id = identity.NewID()
	}

	// ── register in the container registry ────────────────────────────────
	// Other goroutines (Exec) may poll the registry while this Run is in
	// progress; notifyDone unblocks them when we exit.
	notifyDone, regErr := e.registry.Register(id)
	if regErr != nil {
		return nil, regErr
	}
	var startedOnce sync.Once
	defer func() {
		notifyDone(err)
		if started != nil {
			startedOnce.Do(func() { close(started) })
		}
	}()

	meta := process.Meta
	if meta.NetMode == pb.NetMode_HOST {
		bklog.G(ctx).Info("Run: enabling host networking")
	}

	// ── network namespace ──────────────────────────────────────────────────
	provider, ok := e.networkProviders[meta.NetMode]
	if !ok {
		return nil, errors.Errorf("unknown network mode %s", meta.NetMode)
	}
	ns, err := provider.New(ctx, meta.Hostname)
	if err != nil {
		return nil, errors.Wrap(err, "failed to create network namespace")
	}
	releaseNS := true
	defer func() {
		if releaseNS {
			ns.Close()
		}
	}()

	// ── DNS / hosts files ──────────────────────────────────────────────────
	resolvConf, err := oci.GetResolvConf(ctx, e.config.Root, e.config.IdentityMapping, e.config.DNS, meta.NetMode)
	if err != nil {
		return nil, errors.Wrap(err, "failed to prepare resolv.conf")
	}

	hostsFile, cleanHosts, err := oci.GetHostsFile(ctx, e.config.Root, meta.ExtraHosts, e.config.IdentityMapping, meta.Hostname)
	if err != nil {
		return nil, errors.Wrap(err, "failed to prepare hosts file")
	}
	if cleanHosts != nil {
		defer cleanHosts()
	}

	// ── rootfs mount ───────────────────────────────────────────────────────
	mountable, err := rootMount.Src.Mount(ctx, false)
	if err != nil {
		return nil, errors.Wrap(err, "failed to mount rootfs source")
	}
	rootMounts, releaseRootMount, err := mountable.Mount()
	if err != nil {
		return nil, errors.Wrap(err, "failed to acquire rootfs mounts")
	}
	if releaseRootMount != nil {
		defer releaseRootMount()
	}

	// ── OCI bundle directory ───────────────────────────────────────────────
	bundle := filepath.Join(e.config.Root, id)
	if err := os.Mkdir(bundle, 0o711); err != nil {
		return nil, errors.WithStack(err)
	}
	defer os.RemoveAll(bundle) // always clean up, even on success

	// ── rootfs ─────────────────────────────────────────────────────────────
	var rootUID, rootGID int
	if e.config.IdentityMapping != nil {
		rootUID, rootGID = e.config.IdentityMapping.RootPair()
	}

	rootFSPath := filepath.Join(bundle, "rootfs")
	if err := user.MkdirAllAndChown(rootFSPath, 0o700, rootUID, rootGID); err != nil {
		return nil, errors.WithStack(err)
	}
	if err := mount.All(rootMounts, rootFSPath); err != nil {
		return nil, errors.Wrapf(err, "failed to bind-mount rootfs to %s", rootFSPath)
	}
	defer mount.Unmount(rootFSPath, 0)
	defer executor.MountStubsCleaner(context.WithoutCancel(ctx), rootFSPath, mounts, meta.RemoveMountStubsRecursive)()

	// ── OCI spec ───────────────────────────────────────────────────────────
	uid, gid, sgids, err := oci.GetUser(rootFSPath, meta.User)
	if err != nil {
		return nil, errors.Wrap(err, "failed to resolve container user")
	}

	specOpts := []containerdoci.SpecOpts{oci.WithUIDGID(uid, gid, sgids)}
	if meta.ReadonlyRootFS {
		specOpts = append(specOpts, containerdoci.WithRootFSReadonly())
	}

	// Re-derive rootUID/rootGID after possible uid-map translation.
	rootUID, rootGID = int(uid), int(gid)
	if e.config.IdentityMapping != nil {
		if rootUID, rootGID, err = e.config.IdentityMapping.ToHost(rootUID, rootGID); err != nil {
			return nil, errors.Wrap(err, "UID/GID mapping failed")
		}
	}

	spec, cleanSpec, err := oci.GenerateSpec(
		ctx, meta, mounts, id,
		resolvConf, hostsFile, ns,
		e.config.DefaultCgroupParent,
		e.config.ProcessMode,
		e.config.IdentityMapping,
		e.config.ApparmorProfile,
		e.config.SELinux,
		e.config.TracingSocket,
		e.config.CDIManager,
		specOpts...,
	)
	if err != nil {
		return nil, errors.Wrap(err, "failed to generate OCI spec")
	}
	defer cleanSpec()

	spec.Root.Path = rootFSPath
	if rootMount.Readonly {
		spec.Root.Readonly = true
	}

	// Ensure the working directory exists inside the rootfs.
	if err := e.ensureCwd(spec, rootFSPath, rootUID, rootGID); err != nil {
		return nil, err
	}

	spec.Process.Terminal = meta.Tty
	spec.Process.OOMScoreAdj = e.config.OOMScoreAdj

	if e.config.Rootless {
		if err := rootlessspecconv.ToRootless(spec); err != nil {
			return nil, errors.Wrap(err, "failed to convert spec to rootless")
		}
	}

	// Write config.json.
	configFile, err := os.Create(filepath.Join(bundle, "config.json"))
	if err != nil {
		return nil, errors.WithStack(err)
	}
	if err := json.NewEncoder(configFile).Encode(spec); err != nil {
		configFile.Close()
		return nil, errors.Wrap(err, "failed to write config.json")
	}
	configFile.Close()

	bklog.G(ctx).Debugf("Run: creating container %s args=%v", id, meta.Args)

	// ── resource recording ────────────────────────────────────────────────
	cgroupPath := ""
	if spec.Linux != nil {
		cgroupPath = spec.Linux.CgroupsPath
	}
	if cgroupPath != "" && e.config.ResourceMonitor != nil {
		rec, err = e.config.ResourceMonitor.RecordNamespace(cgroupPath, resources.RecordNamespaceOptions{
			NetworkSampler: ns,
		})
		if err != nil {
			return nil, errors.Wrap(err, "failed to start resource recording")
		}
	}

	// ── start container ────────────────────────────────────────────────────
	trace.SpanFromContext(ctx).AddEvent("Container created")

	onStarted := func() {
		startedOnce.Do(func() {
			trace.SpanFromContext(ctx).AddEvent("Container started")
			if started != nil {
				close(started)
			}
			if rec != nil {
				rec.Start()
			}
		})
	}

	runErr := e.runContainer(ctx, id, bundle, process, onStarted)

	// The container has exited; take ownership of network namespace teardown
	// (deferred releaseNS would double-close otherwise).
	releaseNS = false
	releaseContainer := func(ctx context.Context) error {
		deleteErr := e.runtime.Delete(ctx, id, &runc.DeleteOpts{})
		nsErr := ns.Close()
		if deleteErr != nil {
			return deleteErr
		}
		return nsErr
	}

	runErr = e.buildExitError(ctx, cgroupPath, runErr, meta.ValidExitCodes)
	if runErr != nil {
		if rec != nil {
			rec.Close()
		}
		_ = releaseContainer(context.TODO())
		return nil, runErr
	}

	// Success path: if there is no recorder we release synchronously;
	// otherwise the recorder closes asynchronously after its own cleanup.
	if rec == nil {
		return nil, releaseContainer(context.TODO())
	}
	return rec, rec.CloseAsync(releaseContainer)
}

// ensureCwd creates the working directory inside rootFSPath if it does not
// exist, with the correct ownership for rootless mode.
func (e *Executor) ensureCwd(spec *specs.Spec, rootFSPath string, uid, gid int) error {
	absPath, err := fs.RootPath(rootFSPath, spec.Process.Cwd)
	if err != nil {
		return errors.Wrapf(err, "invalid working directory %q", spec.Process.Cwd)
	}
	if _, statErr := os.Stat(absPath); statErr == nil {
		return nil // already exists
	}
	if err := user.MkdirAllAndChown(absPath, 0o755, uid, gid); err != nil {
		return errors.Wrapf(err, "failed to create working directory %q", spec.Process.Cwd)
	}
	return nil
}

// ─── Exec ────────────────────────────────────────────────────────────────────

// Exec implements executor.Executor.
//
// It starts a new process inside an already-running container identified by id.
// Exec blocks (with 100 ms polling) until the container's status is "running",
// returning an error if the container exits or the context is cancelled first.
func (e *Executor) Exec(ctx context.Context, id string, process executor.ProcessInfo) error {
	containerState, err := e.waitForRunning(ctx, id)
	if err != nil {
		return err
	}

	// Load the container spec to inherit Env, Cwd, and other defaults.
	spec, err := e.loadContainerSpec(containerState.Bundle)
	if err != nil {
		return err
	}

	// Override fields from the Exec request.
	if err := e.applyExecOverrides(spec, containerState.Rootfs, process); err != nil {
		return err
	}

	execErr := e.execProcess(ctx, id, spec.Process, process)
	return e.buildExitError(ctx, "", execErr, process.Meta.ValidExitCodes)
}

// waitForRunning polls the container registry and runc state until the container
// is in the "running" state or the context is cancelled.
func (e *Executor) waitForRunning(ctx context.Context, id string) (*runc.Container, error) {
	const pollInterval = 100 * time.Millisecond

	for {
		doneCh, ok := e.registry.Get(id)
		if !ok {
			return nil, errors.Wrapf(ErrContainerNotFound, "container %s", id)
		}

		state, _ := e.runtime.State(ctx, id)
		if state != nil && state.Status == "running" {
			return state, nil
		}

		select {
		case <-ctx.Done():
			return nil, context.Cause(ctx)

		case exitErr, open := <-doneCh:
			// Container exited before we could attach.
			if !open || exitErr == nil {
				return nil, errors.Wrapf(ErrContainerStopped, "container %s", id)
			}
			return nil, errors.Wrapf(exitErr, "container %s exited with error", id)

		case <-time.After(pollInterval):
			// Not yet running; poll again.
		}
	}
}

// loadContainerSpec reads and decodes config.json from a container bundle.
func (e *Executor) loadContainerSpec(bundlePath string) (*specs.Spec, error) {
	f, err := os.Open(filepath.Join(bundlePath, "config.json"))
	if err != nil {
		return nil, errors.Wrap(err, "failed to open container spec")
	}
	defer f.Close()

	spec := &specs.Spec{}
	dec := json.NewDecoder(f)
	if err := dec.Decode(spec); err != nil {
		return nil, errors.Wrap(err, "failed to decode container spec")
	}
	// Guard against JSON files with trailing content.
	if _, err := dec.Token(); !errors.Is(err, io.EOF) {
		return nil, errors.New("unexpected data following the JSON spec object")
	}
	return spec, nil
}

// applyExecOverrides merges the Exec request metadata into a copy of the
// container's base spec.
func (e *Executor) applyExecOverrides(
	spec *specs.Spec,
	rootfs string,
	process executor.ProcessInfo,
) error {
	meta := process.Meta

	if meta.User != "" {
		uid, gid, sgids, err := oci.GetUser(rootfs, meta.User)
		if err != nil {
			return errors.Wrapf(err, "failed to resolve user %q for exec", meta.User)
		}
		spec.Process.User = specs.User{
			UID:            uid,
			GID:            gid,
			AdditionalGids: sgids,
		}
	}

	spec.Process.Terminal = meta.Tty
	spec.Process.Args = meta.Args

	if meta.Cwd != "" {
		spec.Process.Cwd = meta.Cwd
	}
	if len(meta.Env) > 0 {
		spec.Process.Env = meta.Env
	}
	return nil
}

// ─── runContainer / execProcess ───────────────────────────────────────────────

// runContainer starts a new container via `runc run` and blocks until it exits.
// --keep is always passed so that the container's state directory is preserved
// after exit; the caller is responsible for issuing `runc delete`.
func (e *Executor) runContainer(
	ctx context.Context,
	id, bundle string,
	process executor.ProcessInfo,
	onStarted func(),
) error {
	killer := NewContainerKiller(e.runtime, id)
	provider := SelectIOProvider(process)

	return executeWithIO(ctx, process, killer, provider, onStarted, func(
		runcCtx context.Context,
		startedCh chan<- int,
		io runc.IO,
		_ string, // no pidfile for `runc run`
	) error {
		_, err := e.runtime.Run(runcCtx, id, bundle, &runc.CreateOpts{
			NoPivot:   e.config.NoPivot,
			Started:   startedCh,
			IO:        io,
			ExtraArgs: []string{"--keep"},
		})
		return err
	})
}

// execProcess runs an additional process inside an already-running container
// via `runc exec`.
func (e *Executor) execProcess(
	ctx context.Context,
	id string,
	specsProcess *specs.Process,
	process executor.ProcessInfo,
) error {
	killer, err := NewExecKiller(e.runtime, id)
	if err != nil {
		return errors.Wrap(err, "failed to allocate exec killer")
	}
	defer killer.Close()

	provider := SelectIOProvider(process)

	return executeWithIO(ctx, process, killer, provider, nil /* no start callback for exec */, func(
		runcCtx context.Context,
		startedCh chan<- int,
		io runc.IO,
		pidfile string,
	) error {
		return e.runtime.Exec(runcCtx, id, *specsProcess, &runc.ExecOpts{
			Started: startedCh,
			IO:      io,
			PidFile: pidfile,
		})
	})
}

// ─── exit error analysis ─────────────────────────────────────────────────────

// buildExitError translates a raw runc error into a structured *ExitError,
// checks for OOM kills, applies valid-exit-code filtering, and attaches
// tracing events.
//
// Returns nil if the exit is considered successful (exit code 0, or code is
// in validExitCodes).
func (e *Executor) buildExitError(
	ctx context.Context,
	cgroupPath string,
	err error,
	validExitCodes []int,
) error {
	// ── decode the runc exit status ────────────────────────────────────────
	exitErr := &ExitError{}

	if err == nil {
		exitErr.ExitCode = 0
	} else {
		var runcExit *runc.ExitError
		if errors.As(err, &runcExit) {
			exitErr.ExitCode = runcExit.Status
		} else {
			// Unexpected error (e.g. runc binary crash); carry it as Cause.
			exitErr.ExitCode = int(gatewayapi.UnknownExitStatus)
			exitErr.Cause = err
		}
		// Check if the kernel OOM killer was responsible.
		e.oomDetector.CheckAndAnnotate(ctx, cgroupPath, exitErr)
	}

	// ── tracing ───────────────────────────────────────────────────────────
	trace.SpanFromContext(ctx).AddEvent(
		"Container exited",
		trace.WithAttributes(attribute.Int("exit.code", exitErr.ExitCode)),
	)

	// ── valid-exit-code filter ─────────────────────────────────────────────
	if validExitCodes == nil {
		if exitErr.ExitCode == 0 {
			return nil
		}
	} else {
		if slices.Contains(validExitCodes, exitErr.ExitCode) {
			return nil
		}
	}

	// ── annotate with context cancellation cause ──────────────────────────
	select {
	case <-ctx.Done():
		exitErr.Cause = errors.Wrap(context.Cause(ctx), exitErr.Error())
		return exitErr
	default:
		return stack.Enable(exitErr)
	}
}
