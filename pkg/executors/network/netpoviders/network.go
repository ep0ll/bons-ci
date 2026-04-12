// Package netproviders assembles the set of network.Provider implementations
// that the BuildKit worker exposes to the scheduler.
//
// The scheduler maps pb.NetMode values to providers:
//
//	pb.NetMode_UNSET → defaultProvider  (determined by Opt.Mode or auto-detection)
//	pb.NetMode_NONE  → NoneProvider     (always available)
//	pb.NetMode_HOST  → HostProvider     (available on Unix only)
//
// # Auto-detection order (Mode == "auto" or "")
//
//  1. BUILDKIT_NETWORK_BRIDGE_AUTO=1 → bridge
//  2. Opt.CNI.ConfigPath accessible  → cni
//  3. Platform fallback              → host (Unix) or none (Windows)
package netproviders

import (
	"os"
	"strconv"

	"github.com/bons/bons-ci/pkg/executors/network"
	"github.com/bons/bons-ci/pkg/executors/network/cniprovider"
	"github.com/moby/buildkit/solver/pb"
	"github.com/pkg/errors"
)

// Opt is the top-level configuration passed to Providers.
type Opt struct {
	// CNI holds configuration for CNI-based providers (cni and bridge modes).
	CNI cniprovider.Opt

	// Mode selects the network mode.  See NetworkMode constants.
	// Empty string is equivalent to NetworkModeAuto.
	Mode NetworkMode
}

// Result is returned by Providers.
type Result struct {
	// Providers maps pb.NetMode to the corresponding network.Provider.
	// Callers must close all providers when they are no longer needed.
	Providers map[pb.NetMode]network.Provider

	// ResolvedMode is the concrete mode used for pb.NetMode_UNSET after
	// auto-detection.  Always one of: "cni", "host", "none", "bridge".
	ResolvedMode NetworkMode
}

// Providers constructs the full set of network providers for the given options.
//
// It returns a synchronous error only for invalid configurations (bad mode
// string, inaccessible CNI config, etc.).  All returned providers must be
// closed by the caller when the daemon shuts down.
//
// The function validates Opt.Mode eagerly so that a configuration error is
// surfaced at startup rather than when the first build runs.
func Providers(opt Opt) (Result, error) {
	if err := opt.Mode.Validate(); err != nil {
		return Result{}, err
	}

	defaultProvider, resolvedMode, err := buildDefaultProvider(opt)
	if err != nil {
		return Result{}, err
	}

	providers := map[pb.NetMode]network.Provider{
		pb.NetMode_UNSET: defaultProvider,
		pb.NetMode_NONE:  network.NewNoneProvider(),
	}

	// Host provider is platform-conditional; getHostProvider returns (nil,
	// false) on platforms that do not support host networking (Windows).
	if hostProvider, ok := getHostProvider(); ok {
		providers[pb.NetMode_HOST] = hostProvider
	}

	return Result{
		Providers:    providers,
		ResolvedMode: resolvedMode,
	}, nil
}

// buildDefaultProvider resolves the provider for pb.NetMode_UNSET based on
// Opt.Mode.
//
// Separating this from Providers keeps the switch statement focused on a
// single concern (mode→provider mapping) and makes each branch independently
// testable.
func buildDefaultProvider(opt Opt) (network.Provider, resolvedNetworkMode, error) {
	switch opt.Mode.normalise() {
	case NetworkModeCNI:
		return buildCNIProvider(opt)

	case NetworkModeHost:
		return buildHostProvider()

	case NetworkModeBridge:
		return buildBridgeProvider(opt)

	case NetworkModeNone:
		return network.NewNoneProvider(), NetworkModeNone, nil

	case NetworkModeAuto:
		return autoDetectProvider(opt)

	default:
		// Validate() should have caught this; defensive guard.
		return nil, "", errors.Errorf("netproviders: unhandled mode %q", opt.Mode)
	}
}

// buildCNIProvider constructs the CNI file-based provider.
func buildCNIProvider(opt Opt) (network.Provider, resolvedNetworkMode, error) {
	p, err := cniprovider.New(opt.CNI)
	if err != nil {
		return nil, "", errors.Wrap(err, "netproviders: cni mode")
	}
	return p, NetworkModeCNI, nil
}

// buildHostProvider returns the host-network provider or an error if the
// current platform does not support it.
func buildHostProvider() (network.Provider, resolvedNetworkMode, error) {
	p, ok := getHostProvider()
	if !ok {
		return nil, "", errors.New("netproviders: host networking is not supported on this platform")
	}
	return p, NetworkModeHost, nil
}

// buildBridgeProvider constructs the bridge-based CNI provider.
// Bridge mode resolves to "cni" as the reported mode because it uses the CNI
// machinery internally; the distinction is only relevant during construction.
func buildBridgeProvider(opt Opt) (network.Provider, resolvedNetworkMode, error) {
	p, err := getBridgeProvider(opt.CNI)
	if err != nil {
		return nil, "", errors.Wrap(err, "netproviders: bridge mode")
	}
	return p, NetworkModeCNI, nil
}

// autoDetectProvider probes for the best available network provider in the
// following priority order:
//
//  1. BUILDKIT_NETWORK_BRIDGE_AUTO=1  → bridge
//  2. Opt.CNI.ConfigPath accessible   → cni (file-based)
//  3. Platform fallback               → getFallback()
//
// The env-var override is intentional: it lets operators request bridge mode
// without passing an explicit flag through the entire call stack.
func autoDetectProvider(opt Opt) (network.Provider, resolvedNetworkMode, error) {
	// 1. Explicit bridge-auto override.
	if isBridgeAutoEnabled() {
		p, err := getBridgeProvider(opt.CNI)
		if err != nil {
			return nil, "", errors.Wrap(err, "netproviders: BUILDKIT_NETWORK_BRIDGE_AUTO bridge setup")
		}
		return p, NetworkModeCNI, nil
	}

	// 2. CNI config file present → use file-based CNI.
	if _, err := os.Stat(opt.CNI.ConfigPath); err == nil {
		p, err := cniprovider.New(opt.CNI)
		if err != nil {
			return nil, "", errors.Wrap(err, "netproviders: auto cni setup")
		}
		return p, NetworkModeCNI, nil
	}

	// 3. Platform-specific fallback (host on Unix, none on Windows).
	p, mode := getFallback()
	return p, mode, nil
}

// isBridgeAutoEnabled reports whether the BUILDKIT_NETWORK_BRIDGE_AUTO
// environment variable is set to a truthy value.
func isBridgeAutoEnabled() bool {
	v, err := strconv.ParseBool(os.Getenv("BUILDKIT_NETWORK_BRIDGE_AUTO"))
	return err == nil && v
}
