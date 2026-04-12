//go:build linux

package cniprovider

import (
	"context"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"

	"github.com/bons/bons-ci/pkg/executors/network"
	cni "github.com/containerd/go-cni"
	"github.com/moby/buildkit/util/bklog"
	"github.com/pkg/errors"
	"github.com/vishvananda/netlink"
)

// NewBridge constructs a bridge-based CNI network provider using the bundled
// buildkit-cni-* plugin binaries.
//
// Bridge mode synthesises its own CNI conflist at runtime rather than reading
// one from disk.  The conflist wires up:
//
//   - loopback        (lo inside the container)
//   - bridge + IPAM   (isolated L2 network with NAT/masquerade)
//   - firewall        (iptables/nftables ingress policy)
//
// The bridge interface (opt.BridgeName) is created on first use and removed by
// Close().  If the bridge already exists when NewBridge is called, it is reused
// and NOT removed on Close() (to avoid disrupting other users).
//
// Rootless: when running under RootlessKit with --detach-netns, the bridge
// existence check and all CNI operations run inside the detached network
// namespace via withDetachedNetNSIfAny.
func NewBridge(opt Opt) (network.Provider, error) {
	if err := opt.ValidateBridge(); err != nil {
		return nil, err
	}

	bins, err := resolveBridgeBinaries(opt.BinaryDir)
	if err != nil {
		return nil, err
	}

	firewallBackend := resolveFirewallBackend()
	confList := buildBridgeConfList(bins, opt.BridgeName, opt.BridgeSubnet, firewallBackend)

	cniOpts := append(bins.pluginDirOpts(),
		cni.WithInterfacePrefix("eth"),
		cni.WithConfListBytes(confList),
	)

	unlock, err := acquireInitLock()
	if err != nil {
		return nil, err
	}
	defer unlock() //nolint:errcheck

	// Determine whether we are responsible for creating (and later removing)
	// the bridge.  If it already exists, we leave teardown to whoever owns it.
	needsCreate := !bridgeExists(opt.BridgeName)

	cniHandle, err := cni.New(cniOpts...)
	if err != nil {
		return nil, errors.Wrap(err, "cniprovider: failed to create CNI handle for bridge")
	}

	cp := &cniProvider{
		handle: cniHandle,
		root:   opt.Root,
	}

	if needsCreate {
		cp.release = func() error {
			// CNI initialisation blocks on background context because it must
			// not be interrupted mid-flight.
			if err := withDetachedNetNSIfAny(context.Background(), func(_ context.Context) error {
				return removeBridge(opt.BridgeName)
			}); err != nil {
				bklog.L.WithError(err).Errorf("cniprovider: failed to remove bridge %q", opt.BridgeName)
				// Don't propagate — bridge removal is best-effort on shutdown.
			}
			return nil
		}
	}

	cleanOldNamespaces(cp)

	cp.nsPool = newNSPool(opt.PoolSize, cp)
	if err := cp.probeNetwork(); err != nil {
		return nil, errors.Wrap(err, "cniprovider: bridge network probe failed")
	}
	cp.nsPool.startFill(context.Background())
	return cp, nil
}

// ─── Binary discovery ─────────────────────────────────────────────────────────

// bridgeBinaries holds the resolved binary names and the de-duplicated set of
// directories they live in.  The directories are passed to cni.WithPluginDir
// so the CNI library can exec them during Setup/Remove.
type bridgeBinaries struct {
	loopback  string
	bridge    string
	hostLocal string
	firewall  string
	dirs      []string // de-duplicated, ordered
}

// pluginDirOpts returns a single cni.WithPluginDir option populated with the
// de-duplicated binary directories.
func (b *bridgeBinaries) pluginDirOpts() []cni.Opt {
	if len(b.dirs) == 0 {
		return nil
	}
	return []cni.Opt{cni.WithPluginDir(b.dirs)}
}

// resolveBridgeBinaries discovers the four CNI plugin binaries that
// NewBridge requires.
//
// Discovery strategy (in priority order):
//  1. Look for buildkit-prefixed binaries (buildkit-cni-bridge, etc.) on PATH.
//     These ship with BuildKit and take precedence.
//  2. Fall back to the generic binary names in opt.BinaryDir.
//
// The original code used a `for { ... break }` idiom as a structured
// early-exit mechanism.  That pattern is idiomatic but obscures intent;
// this version uses a named helper with explicit returns.
func resolveBridgeBinaries(binaryDir string) (*bridgeBinaries, error) {
	if bins := lookupBundledBinaries(); bins != nil {
		return bins, nil
	}
	return fallbackBinaryDir(binaryDir)
}

// lookupBundledBinaries searches PATH for the buildkit-cni-* binaries that
// ship alongside the BuildKit daemon.  Returns nil if any binary is missing.
func lookupBundledBinaries() *bridgeBinaries {
	type entry struct {
		lookupName string
		dest       *string
	}

	bins := &bridgeBinaries{
		bridge:    "bridge",
		loopback:  "loopback",
		hostLocal: "host-local",
		firewall:  "firewall",
	}

	lookups := []entry{
		{"buildkit-cni-bridge", &bins.bridge},
		{"buildkit-cni-loopback", &bins.loopback},
		{"buildkit-cni-host-local", &bins.hostLocal},
		{"buildkit-cni-firewall", &bins.firewall},
	}

	dirSet := map[string]struct{}{}

	for _, e := range lookups {
		p, err := exec.LookPath(e.lookupName)
		if err != nil {
			// Any missing binary aborts the bundled-binary path.
			return nil
		}
		dir, name := filepath.Split(p)
		*e.dest = name
		if _, seen := dirSet[dir]; !seen {
			dirSet[dir] = struct{}{}
			bins.dirs = append(bins.dirs, dir)
		}
	}

	return bins
}

// fallbackBinaryDir uses the generic binary names (bridge, loopback, …) from
// opt.BinaryDir, verifying that at least the bridge binary is present.
func fallbackBinaryDir(binaryDir string) (*bridgeBinaries, error) {
	bridgePath := filepath.Join(binaryDir, "bridge")
	if _, err := os.Stat(bridgePath); err != nil {
		return nil, errors.Wrapf(err,
			"cniprovider: bridge binary not found at %q and buildkit-cni-bridge not on PATH",
			bridgePath)
	}
	return &bridgeBinaries{
		bridge:    "bridge",
		loopback:  "loopback",
		hostLocal: "host-local",
		firewall:  "firewall",
		dirs:      []string{binaryDir},
	}, nil
}

// ─── Firewall backend ─────────────────────────────────────────────────────────

// resolveFirewallBackend returns the firewall backend string for the CNI
// firewall plugin.  An empty string instructs the plugin to auto-detect
// (firewalld or iptables).
//
// When running under RootlessKit, firewalld is incompatible (see
// https://github.com/containerd/nerdctl/issues/2818) so we force iptables.
func resolveFirewallBackend() string {
	if os.Getenv("ROOTLESSKIT_STATE_DIR") != "" {
		return "iptables"
	}
	return "" // auto-detect
}

// ─── CNI conflist builder ─────────────────────────────────────────────────────

// buildBridgeConfList generates the CNI conflist JSON that wires up loopback,
// bridge, and firewall plugins.
//
// Using fmt.Appendf with %s substitutions is the same approach as the upstream
// BuildKit code.  A struct-based JSON marshal would add complexity without
// benefit here because the schema is fixed and tested end-to-end.
func buildBridgeConfList(bins *bridgeBinaries, bridgeName, subnet, firewallBackend string) []byte {
	return fmt.Appendf(nil, `{
	"cniVersion": "1.0.0",
	"name": "buildkit",
	"plugins": [
		{
			"type": %q
		},
		{
			"type": %q,
			"bridge": %q,
			"isDefaultGateway": true,
			"ipMasq": true,
			"ipam": {
				"type": %q,
				"ranges": [[{"subnet": %q}]]
			}
		},
		{
			"type": %q,
			"backend": %q,
			"ingressPolicy": "same-bridge"
		}
	]
}`, bins.loopback, bins.bridge, bridgeName, bins.hostLocal, subnet, bins.firewall, firewallBackend)
}

// ─── Bridge interface helpers ─────────────────────────────────────────────────

// bridgeExists reports whether a Linux bridge named name already exists.
// It runs inside the detached netns when applicable.
func bridgeExists(name string) bool {
	// Ensure device teardown evaluates securely against uncancelled contexts.
	err := withDetachedNetNSIfAny(context.Background(), func(_ context.Context) error {
		_, err := bridgeByName(name)
		return err
	})
	return err == nil
}

// bridgeByName retrieves the netlink.Bridge for name, returning an error if
// the link does not exist or is not a bridge.
func bridgeByName(name string) (*netlink.Bridge, error) {
	l, err := netlink.LinkByName(name)
	if err != nil {
		return nil, errors.Wrapf(err, "cniprovider: link %q not found", name)
	}
	br, ok := l.(*netlink.Bridge)
	if !ok {
		return nil, errors.Errorf("cniprovider: %q exists but is not a bridge (type %T)", name, l)
	}
	return br, nil
}

// removeBridge deletes the named Linux bridge interface.
func removeBridge(name string) error {
	br, err := bridgeByName(name)
	if err != nil {
		return err
	}
	return netlink.LinkDel(br)
}
