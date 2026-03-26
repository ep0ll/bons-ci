package oci

// Design notes
// ============
//
// Problem with the original implementation
// -----------------------------------------
// The original used three package-level variables (g, notFirstRun, lastNotEmpty)
// to track singleflight deduplication and regeneration state.  This caused two
// concrete problems:
//
//  1. Tests had to be serialised ("must not run in parallel") because any two
//     concurrent invocations of GetResolvConf raced on those variables.
//
//  2. notFirstRun was shared across ALL stateDirs, so the first call for
//     stateDir-A silenced the always-generate safeguard for stateDir-B.
//
//  3. lastNotEmpty was NEVER set to true anywhere in the original code, making
//     the "source file disappeared but previously had nameservers → regenerate"
//     branch dead code.  This rewrite fixes the bug.
//
// Solution
// --------
// All mutable state is moved into ResolvConfManager, keyed per output path.
// Tests construct their own manager with an injected sourcePath function;
// the package-level GetResolvConf delegates to a default manager.

import (
	"context"
	"net/netip"
	"os"
	"path/filepath"
	"sync"

	"github.com/moby/buildkit/solver/pb"
	"github.com/moby/buildkit/util/bklog"
	"github.com/moby/buildkit/util/flightcontrol"
	"github.com/moby/buildkit/util/resolvconf"
	"github.com/moby/sys/user"
	"github.com/pkg/errors"
)

// Host-side resolv.conf paths.
const (
	// hostResolvConfDefault is the standard resolv.conf location.
	hostResolvConfDefault = "/etc/resolv.conf"

	// hostResolvConfSystemd is the upstream resolv.conf that systemd-resolved
	// generates.  It contains the real upstream nameservers, unlike the stub
	// listener at 127.0.0.53 that /etc/resolv.conf may point to.
	hostResolvConfSystemd = "/run/systemd/resolve/resolv.conf"

	// systemdResolvedStub is the loopback address that systemd-resolved
	// listens on.  It is NOT reachable from inside a non-host network
	// namespace, so we must substitute it with hostResolvConfSystemd.
	systemdResolvedStub = "127.0.0.53"

	// Filenames written inside stateDir for each network mode.
	resolvConfFile     = "resolv.conf"
	resolvConfHostFile = "resolv-host.conf"
)

// DNSConfig carries per-container DNS overrides that are merged onto the
// host resolv.conf before the file is written into the container's stateDir.
type DNSConfig struct {
	Nameservers   []string
	Options       []string
	SearchDomains []string
}

// pathGenState tracks per-output-path regeneration state inside
// ResolvConfManager.  The zero value is safe to use (initialized == false
// causes unconditional generation on the first call for that path).
type pathGenState struct {
	initialized    bool // has this path been generated at least once?
	hadNameservers bool // did the source resolv.conf have nameservers last time?
}

// ResolvConfManager manages generation and caching of resolv.conf files for
// container sandbox environments.
//
// For each output path it tracks:
//   - Whether the path has been generated at all (always generate on first use).
//   - Whether the source resolv.conf had nameservers last time (needed to
//     detect the case where the source file disappears and we must regenerate
//     with the built-in defaults so the container doesn't lose DNS).
//
// Concurrent calls for the same output path are coalesced via singleflight.
// Each executor instance should hold one ResolvConfManager.
type ResolvConfManager struct {
	// sourcePath resolves the host-side resolv.conf path for a given network
	// mode.  It defaults to selectHostResolvConf but can be replaced in tests.
	sourcePath func(netMode pb.NetMode) string

	mu    sync.Mutex
	state map[string]*pathGenState // keyed by absolute output path

	// group deduplicates concurrent generation requests for the same output
	// path.  Using flightcontrol (not sync.Map) because callers hold contexts
	// that can be cancelled; flightcontrol propagates cancellation correctly.
	group flightcontrol.Group[struct{}]
}

// NewResolvConfManager returns a manager that uses the standard host-side
// resolv.conf selection logic (systemd-resolved detection, etc.).
func NewResolvConfManager() *ResolvConfManager {
	return newResolvConfManagerWithSource(selectHostResolvConf)
}

// newResolvConfManagerWithSource creates a manager with a custom source-path
// function.  Only intended for tests.
func newResolvConfManagerWithSource(src func(pb.NetMode) string) *ResolvConfManager {
	return &ResolvConfManager{
		sourcePath: src,
		state:      make(map[string]*pathGenState),
	}
}

// defaultManager is the package-level singleton used by GetResolvConf.
// It is created lazily so that its sourcePath function can call bklog at
// runtime without a nil logger.
var (
	defaultManagerOnce sync.Once
	defaultManager     *ResolvConfManager
)

func getDefaultManager() *ResolvConfManager {
	defaultManagerOnce.Do(func() {
		defaultManager = NewResolvConfManager()
	})
	return defaultManager
}

// GetResolvConf is the package-level entry point retained for backward
// compatibility.  New code should prefer holding a ResolvConfManager directly.
func GetResolvConf(
	ctx context.Context,
	stateDir string,
	idmap *user.IdentityMapping,
	dns *DNSConfig,
	netMode pb.NetMode,
) (string, error) {
	return getDefaultManager().Get(ctx, stateDir, idmap, dns, netMode)
}

// selectHostResolvConf picks the host resolv.conf to read for the given
// network mode.
//
// HOST mode: always /etc/resolv.conf.
//   The container shares the host network namespace so loopback addresses
//   (127.0.0.53) are reachable.  See:
//   https://github.com/moby/buildkit/pull/5207#discussion_r1705362230
//
// Non-HOST mode: if /etc/resolv.conf lists *only* 127.0.0.53, assume that
//   systemd-resolved is managing DNS via its stub listener.  That listener is
//   NOT reachable from inside a private network namespace, so use the upstream
//   resolv.conf that systemd-resolved itself generates.
func selectHostResolvConf(netMode pb.NetMode) string {
	if netMode == pb.NetMode_HOST {
		return hostResolvConfDefault
	}
	rc, err := resolvconf.Load(hostResolvConfDefault)
	if err != nil {
		return hostResolvConfDefault
	}
	ns := rc.NameServers()
	if len(ns) == 1 && ns[0] == netip.MustParseAddr(systemdResolvedStub) {
		bklog.G(context.TODO()).Infof(
			"detected %s nameserver, assuming systemd-resolved; using %s",
			systemdResolvedStub, hostResolvConfSystemd,
		)
		return hostResolvConfSystemd
	}
	return hostResolvConfDefault
}

// Get returns the path to a resolv.conf file ready for bind-mounting into
// a container.  The file lives in stateDir and is regenerated automatically
// when the host resolv.conf changes.  Concurrent callers for the same
// (stateDir, netMode) pair are coalesced so the file is only written once.
func (m *ResolvConfManager) Get(
	ctx context.Context,
	stateDir string,
	idmap *user.IdentityMapping,
	dns *DNSConfig,
	netMode pb.NetMode,
) (string, error) {
	outputPath := outputResolvConfPath(stateDir, netMode)

	_, err := m.group.Do(ctx, outputPath, func(ctx context.Context) (struct{}, error) {
		return struct{}{}, m.generateIfNeeded(ctx, outputPath, idmap, dns, netMode)
	})
	if err != nil {
		return "", err
	}
	return outputPath, nil
}

// outputResolvConfPath returns the path where the container resolv.conf will
// be written for the given network mode.  HOST mode gets a separate file so
// that the two variants can coexist in the same stateDir without conflicts.
func outputResolvConfPath(stateDir string, netMode pb.NetMode) string {
	name := resolvConfFile
	if netMode == pb.NetMode_HOST {
		name = resolvConfHostFile
	}
	return filepath.Join(stateDir, name)
}

// generateIfNeeded writes outputPath if and only if the source has changed
// since the last write, or this is the first call for outputPath.
//
// The function is called within a flightcontrol singleflight group, so at
// most one goroutine executes it for any given outputPath at a time.
func (m *ResolvConfManager) generateIfNeeded(
	_ context.Context,
	outputPath string,
	idmap *user.IdentityMapping,
	dns *DNSConfig,
	netMode pb.NetMode,
) error {
	// Snapshot per-path state under the lock.
	m.mu.Lock()
	ps, ok := m.state[outputPath]
	if !ok {
		ps = &pathGenState{}
		m.state[outputPath] = ps
	}
	initialized := ps.initialized
	hadNameservers := ps.hadNameservers
	m.mu.Unlock()

	// Resolve source path once per call so we don't load /etc/resolv.conf
	// twice (the original code called resolvconfPath twice: once for the mtime
	// stat and once for the actual load).
	srcPath := m.sourcePath(netMode)

	need, err := m.needsRegeneration(outputPath, srcPath, initialized, hadNameservers)
	if err != nil {
		return err
	}
	if !need {
		return nil
	}

	nowHasNameservers, err := m.write(outputPath, srcPath, idmap, dns, netMode)
	if err != nil {
		return err
	}

	// Update state after a successful write.
	m.mu.Lock()
	ps.initialized = true
	ps.hadNameservers = nowHasNameservers
	m.mu.Unlock()

	return nil
}

// needsRegeneration determines whether outputPath should be (re)written.
//
// Generation is needed when:
//  1. The path has never been generated (first call).
//  2. The output file no longer exists.
//  3. The source resolv.conf is newer than the output file.
//  4. The source resolv.conf has disappeared but previously had nameservers
//     (we must regenerate with built-in defaults so the container isn't left
//     without DNS).
//
// Note: case 4 was always dead code in the original because lastNotEmpty was
// never set to true.  This rewrite fixes the bug.
func (m *ResolvConfManager) needsRegeneration(
	outputPath, srcPath string,
	initialized, hadNameservers bool,
) (bool, error) {
	if !initialized {
		return true, nil
	}

	outInfo, err := os.Stat(outputPath)
	if err != nil {
		if !errors.Is(err, os.ErrNotExist) {
			return false, errors.WithStack(err)
		}
		// The output file vanished; regenerate unconditionally.
		return true, nil
	}

	srcInfo, err := os.Stat(srcPath)
	if err != nil {
		if !errors.Is(err, os.ErrNotExist) {
			return false, errors.WithStack(err)
		}
		// Source has disappeared.  Only regenerate if it previously carried
		// nameservers; otherwise the container already has the defaults and
		// nothing useful would change.
		return hadNameservers, nil
	}

	// The common case: regenerate if the source is newer than our cached copy.
	return outInfo.ModTime().Before(srcInfo.ModTime()), nil
}

// write generates the resolv.conf content for outputPath and atomically
// writes it.  It returns whether the source resolv.conf contained any
// nameservers (used to populate hadNameservers for subsequent calls).
func (m *ResolvConfManager) write(
	outputPath, srcPath string,
	idmap *user.IdentityMapping,
	dns *DNSConfig,
	netMode pb.NetMode,
) (hadNameservers bool, err error) {
	rc, err := resolvconf.Load(srcPath)
	if err != nil && !errors.Is(err, os.ErrNotExist) {
		return false, errors.WithStack(err)
	}

	// Record whether the source had nameservers BEFORE any overrides, so that
	// subsequent calls can detect the "source disappeared" case correctly.
	hadNameservers = len(rc.NameServers()) > 0

	if dns != nil {
		if err := applyDNSConfig(rc, dns); err != nil {
			return false, err
		}
	}

	// Replace loopback/link-local nameservers unless we are in host network
	// mode and the resolv.conf is non-empty.  In host mode the container
	// shares the host's loopback interface, so 127.x addresses work fine.
	if netMode != pb.NetMode_HOST || len(rc.NameServers()) == 0 {
		rc.TransformForLegacyNw(true)
	}

	dt, err := rc.Generate(false)
	if err != nil {
		return false, errors.WithStack(err)
	}

	if err := atomicWriteFile(outputPath, dt, 0o644, idmap); err != nil {
		return false, err
	}

	return hadNameservers, nil
}

// applyDNSConfig merges the user-supplied DNS overrides into the parsed
// resolv.conf.  Each field is applied only when the slice is non-empty so
// that an absent field in DNSConfig means "inherit from host".
func applyDNSConfig(rc resolvconf.ResolvConf, dns *DNSConfig) error {
	if len(dns.Nameservers) > 0 {
		addrs := make([]netip.Addr, 0, len(dns.Nameservers))
		for _, s := range dns.Nameservers {
			a, err := netip.ParseAddr(s)
			if err != nil {
				return errors.Wrap(err, "invalid nameserver address")
			}
			addrs = append(addrs, a)
		}
		rc.OverrideNameServers(addrs)
	}
	if len(dns.SearchDomains) > 0 {
		rc.OverrideSearch(dns.SearchDomains)
	}
	if len(dns.Options) > 0 {
		rc.OverrideOptions(dns.Options)
	}
	return nil
}
