// Package cniprovider implements BuildKit's network.Provider using the
// Container Network Interface (CNI) specification.
//
// Two constructors are exported:
//
//   - [New]       – file-based CNI: reads an existing .conf/.conflist and an
//                   external CNI binary directory.
//   - [NewBridge] – built-in bridge: synthesises a CNI config from the bundled
//                   buildkit-cni-* binaries (bridge_linux.go, Linux only).
//
// # Architecture
//
//	Opt ──► New / NewBridge
//	            │
//	            ▼
//	       cniProvider (implements network.Provider)
//	            │  contains
//	            ├── cni.CNI handle   (unexported — not embedded)
//	            └── nsPool
//	                    │ manages
//	                    └── []*cniNS  (implements network.Namespace)
//
// # Why cni.CNI is NOT embedded
//
// Embedding an interface on a concrete struct promotes all interface methods
// to the outer type, violating the Interface Segregation Principle.  Callers
// who obtain a network.Provider would gain access to cni.Setup, cni.Remove,
// etc., which are internal CNI concerns.  Using an unexported field keeps the
// public API minimal and the coupling explicit.
//
// # Namespace pool
//
// Both constructors pre-warm a fixed-size pool (Opt.PoolSize) of network
// namespaces so containers can acquire one without paying CNI-setup latency on
// the critical path.  Namespaces requiring a custom hostname bypass the pool.
//
// # Rootless support
//
// Under RootlessKit ≥ 2.0 with --detach-netns, CNI operations must execute
// inside the detached network namespace.  [withDetachedNetNSIfAny] handles
// this transparently; the presence of [contextKeyDetachedNetNS] in the context
// also switches CNI setup from parallel to serial (setns(2) + goroutines race).
package cniprovider

import (
	"context"
	"os"
	"runtime"
	"strings"

	cni "github.com/containerd/go-cni"
	"github.com/gofrs/flock"
	"github.com/moby/buildkit/identity"
	"github.com/moby/buildkit/util/bklog"
	"github.com/bons/bons-ci/pkg/executors/network"
	"github.com/pkg/errors"
	"go.opentelemetry.io/otel/trace"
)

// ─── Context key ──────────────────────────────────────────────────────────────

// detachedNetNSKeyT is an unexported zero-size struct used as the context-value
// key for the RootlessKit detached-netns path.
//
// Using a package-private struct type (not a string alias or string constant)
// prevents any other package from producing the same key by coincidence, which
// is the canonical Go pattern for context keys (see context package docs).
type detachedNetNSKeyT struct{}

// contextKeyDetachedNetNS is stored in the context by withDetachedNetNSIfAny
// when the caller is running inside RootlessKit's detached network namespace.
// Its presence signals that CNI setup must use SetupSerially.
var contextKeyDetachedNetNS = detachedNetNSKeyT{}

// ─── Provider ─────────────────────────────────────────────────────────────────

// cniProvider implements network.Provider using a CNI handle.
type cniProvider struct {
	// handle is the CNI library instance used for Setup/Remove/SetupSerially.
	// Unexported and not embedded; see package doc for rationale.
	handle cni.CNI

	// root is the BuildKit state directory (e.g. /var/lib/buildkit).
	// Namespace bind-mount files are stored under root/net/cni/<id>.
	root string

	// nsPool is the pre-warmed namespace pool.  Never nil after construction.
	nsPool *nsPool

	// release is called by Close to tear down resources created during
	// construction (e.g. the bridge interface in NewBridge).  May be nil.
	release func() error
}

// New constructs a CNI file-based network provider.
//
// Steps:
//  1. Validate Opt (config and binary paths accessible).
//  2. Build CNI option list.
//  3. Create the CNI handle (inside the detached netns when under RootlessKit).
//  4. Clean up any stale namespaces from a previous daemon run.
//  5. Probe the network (create+close one namespace) to surface mis-config.
//  6. Start the namespace pool and its background filler.
func New(opt Opt) (network.Provider, error) {
	if err := opt.Validate(); err != nil {
		return nil, err
	}

	cniOpts := buildCNIOptions(opt.BinaryDir, opt.ConfigPath)

	var handle cni.CNI
	if err := withDetachedNetNSIfAny(context.Background(), func(_ context.Context) error {
		var err error
		handle, err = cni.New(cniOpts...)
		return err
	}); err != nil {
		return nil, errors.Wrap(err, "cniprovider: failed to initialise CNI handle")
	}

	cp := &cniProvider{handle: handle, root: opt.Root}
	cleanOldNamespaces(cp)

	if err := cp.probeNetwork(); err != nil {
		return nil, errors.Wrap(err, "cniprovider: network probe")
	}

	cp.nsPool = newNSPool(opt.PoolSize, cp)
	cp.nsPool.startFill(context.Background())
	return cp, nil
}

// buildCNIOptions assembles the cni.Opt slice.
//
// Windows does not use CNI for loopback (handled natively), so we skip
// WithMinNetworkCount and WithLoNetwork on that platform.
func buildCNIOptions(binaryDir, configPath string) []cni.Opt {
	opts := make([]cni.Opt, 0, 5)

	if runtime.GOOS != "windows" {
		// WithMinNetworkCount(2): require at least loopback + one data network.
		// This catches misconfigured conflist files at provider creation time.
		opts = append(opts, cni.WithMinNetworkCount(2), cni.WithLoNetwork)
	}

	opts = append(opts,
		cni.WithPluginDir([]string{binaryDir}),
		cni.WithInterfacePrefix("eth"),
	)

	if strings.HasSuffix(configPath, ".conflist") {
		opts = append(opts, cni.WithConfListFile(configPath))
	} else {
		opts = append(opts, cni.WithConfFile(configPath))
	}

	return opts
}

// probeNetwork verifies that the CNI configuration is functional by creating
// and immediately releasing one namespace under the init lock.
//
// BUILDKIT_CNI_INIT_LOCK_PATH serialises concurrent daemon startups sharing a
// CNI binary directory (e.g. multiple BuildKit workers on the same host).
func (c *cniProvider) probeNetwork() error {
	unlock, err := acquireInitLock()
	if err != nil {
		return err
	}
	defer unlock() //nolint:errcheck

	ns, err := c.New(context.Background(), "")
	if err != nil {
		return err
	}
	return ns.Close()
}

// acquireInitLock acquires the file lock at BUILDKIT_CNI_INIT_LOCK_PATH.
// Returns a no-op release func when the env var is unset.
func acquireInitLock() (func() error, error) {
	path := os.Getenv("BUILDKIT_CNI_INIT_LOCK_PATH")
	if path == "" {
		return func() error { return nil }, nil
	}
	l := flock.New(path)
	if err := l.Lock(); err != nil {
		return nil, errors.Wrapf(err, "cniprovider: acquire init lock %q", path)
	}
	return l.Unlock, nil
}

// ─── network.Provider implementation ─────────────────────────────────────────

// New implements network.Provider.
//
// Routing logic:
//   - hostname == "" && not Windows → pool (LIFO reuse, lowest latency).
//   - hostname != ""                → direct newNS (custom CNI args required).
//   - Windows                       → direct newNS (pool teardown not yet impl).
func (c *cniProvider) New(ctx context.Context, hostname string) (network.Namespace, error) {
	if hostname == "" && runtime.GOOS != "windows" {
		return c.nsPool.get(ctx)
	}
	return c.newNSWithHostname(ctx, hostname)
}

// Close implements network.Provider.
func (c *cniProvider) Close() error {
	c.nsPool.close()
	if c.release != nil {
		return c.release()
	}
	return nil
}

// ─── Namespace factory ────────────────────────────────────────────────────────

// newNSWithHostname wraps newNS with detached-netns awareness for the direct
// (non-pool) allocation path.
func (c *cniProvider) newNSWithHostname(ctx context.Context, hostname string) (network.Namespace, error) {
	var ns *cniNS
	if err := withDetachedNetNSIfAny(ctx, func(ctx context.Context) error {
		var err error
		ns, err = c.newNS(ctx, hostname)
		return err
	}); err != nil {
		return nil, err
	}
	return ns, nil
}

// newNS is the core namespace creation path.  Called from both the public New
// path and nsPool.allocNew; must be safe for concurrent use.
//
// Steps:
//  1. createNetNS    – allocate an OS network namespace (platform-specific).
//  2. setupCNI       – run CNI plugins to configure the namespace.
//  3. resolveVethName – find the veth interface for traffic sampling.
//  4. initSampling   – record baseline counters so Sample() returns deltas.
func (c *cniProvider) newNS(ctx context.Context, hostname string) (*cniNS, error) {
	id := identity.NewID()
	span := trace.SpanFromContext(ctx)

	bklog.G(ctx).Debugf("cniprovider: creating namespace %s", id)
	span.AddEvent("creating network namespace")

	nativeID, err := createNetNS(c, id)
	if err != nil {
		return nil, errors.Wrapf(err, "cniprovider: createNetNS %s", id)
	}
	span.AddEvent("network namespace created")

	nsOpts := buildNSOpts(hostname)

	cniRes, err := c.setupCNI(ctx, id, nativeID, nsOpts)
	if err != nil {
		// Best-effort cleanup: namespace was created but CNI setup failed.
		if delErr := deleteNetNS(nativeID); delErr != nil {
			bklog.G(ctx).WithError(delErr).Warnf(
				"cniprovider: failed to delete netns %s after CNI setup failure", nativeID)
		}
		return nil, errors.Wrap(err, "cniprovider: CNI setup")
	}
	span.AddEvent("network namespace ready")
	bklog.G(ctx).Debugf("cniprovider: namespace %s ready", id)

	ns := &cniNS{
		nativeID: nativeID,
		id:       id,
		handle:   c.handle,
		opts:     nsOpts,
		vethName: resolveVethName(cniRes),
	}
	ns.initSampling()
	return ns, nil
}

// buildNSOpts constructs the CNI namespace options for the given hostname.
//
// K8S_POD_NAME is leveraged by the dnsname CNI plugin (and historically by k8s
// and podman) to configure per-container DNS entries.  IgnoreUnknown prevents
// plugins that don't recognise K8S_POD_NAME from failing the setup chain.
func buildNSOpts(hostname string) []cni.NamespaceOpts {
	if hostname == "" {
		return nil
	}
	return []cni.NamespaceOpts{
		cni.WithArgs("K8S_POD_NAME", hostname),
		cni.WithArgs("IgnoreUnknown", "1"),
	}
}

// setupCNI calls Setup or SetupSerially depending on whether we are inside a
// detached netns.
//
// Parallel Setup is unsafe in the detached-netns context: the goroutines it
// spawns internally may be scheduled onto OS threads that have not had setns(2)
// applied, causing them to operate in the wrong namespace.
// SetupSerially executes all plugin invocations sequentially on the calling
// goroutine, which is pinned to the correct netns by WithNetNSPath.
func (c *cniProvider) setupCNI(ctx context.Context, id, nativeID string, opts []cni.NamespaceOpts) (*cni.Result, error) {
	// Use context.Background() for the inner CNI calls: the CNI library does not
	// propagate context to plugin processes (they communicate via stdin/stdout),
	// so passing the caller's context would give a false impression of
	// cancellation support.
	if ctx.Value(contextKeyDetachedNetNS) != nil {
		return c.handle.SetupSerially(context.Background(), id, nativeID, opts...)
	}
	return c.handle.Setup(context.Background(), id, nativeID, opts...)
}

// resolveVethName scans the CNI result for the host-side veth interface used to
// read traffic statistics.  Returns "" if zero or more than one veth interface
// is present (ambiguous or misconfigured CNI chain).
func resolveVethName(res *cni.Result) string {
	found := ""
	for k := range res.Interfaces {
		if !strings.HasPrefix(k, "veth") {
			continue
		}
		if found != "" {
			// More than one veth — ambiguous; disable sampling.
			return ""
		}
		found = k
	}
	return found
}
