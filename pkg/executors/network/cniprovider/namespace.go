package cniprovider

import (
	"context"
	"time"

	cni "github.com/containerd/go-cni"
	resourcestypes "github.com/bons/bons-ci/pkg/executors/resources/types"
	"github.com/moby/buildkit/util/bklog"
	specs "github.com/opencontainers/runtime-spec/specs-go"
	"github.com/pkg/errors"
)

// cniNS is a single CNI-configured network namespace.
//
// Instances are created by cniProvider.newNS and either returned directly to
// callers (custom hostname) or managed by nsPool (generic pool namespaces).
//
// # Sampling
//
// When a veth interface is found in the CNI result, traffic counters are read
// from /sys/class/net/<veth>/statistics at the time of creation (offsetSample).
// Subsequent Sample() calls return delta values relative to that baseline.
//
// When the namespace is returned to the pool via Close(), the last observed
// sample is saved as the new offset so that when the namespace is reused, the
// next consumer sees deltas from their start (not from the namespace's birth).
//
// # Ownership
//
// pool is nil for non-pooled namespaces (custom hostname / Windows).
// Close() checks pool: if nil → release; if non-nil → return to pool.
type cniNS struct {
	// Immutable after construction.
	id       string // BuildKit-assigned identity (used as CNI container ID)
	nativeID string // OS-level namespace path (Linux) or HCN GUID (Windows)
	handle   cni.CNI
	opts     []cni.NamespaceOpts
	vethName string // empty if sampling is not available

	// Mutable; written only by the goroutine that owns the namespace at a
	// given moment (no concurrent access during normal use).
	pool         *nsPool
	lastUsed     time.Time
	canSample    bool
	offsetSample *resourcestypes.NetworkSample // baseline at acquisition time
	prevSample   *resourcestypes.NetworkSample // last observed sample
}

// initSampling reads the initial traffic counters and sets up the offset
// baseline.  Called once at namespace creation, before the namespace enters
// the pool or is returned to a caller.
func (ns *cniNS) initSampling() {
	if ns.vethName == "" {
		return
	}
	s, err := ns.sample()
	if err != nil || s == nil {
		return
	}
	ns.canSample = true
	ns.offsetSample = s
}

// ─── network.Namespace implementation ────────────────────────────────────────

// Set applies the network namespace to the OCI runtime spec.
// Implemented in the platform-specific netns_*.go files.
func (ns *cniNS) Set(s *specs.Spec) error {
	return setNetNS(s, ns.nativeID)
}

// Close returns the namespace to the pool (if pooled) or releases it (if not).
//
// Before returning to the pool, Close records prevSample as the new offset.
// This means each consumer of the namespace sees traffic deltas relative to
// their own start, not relative to the namespace's creation.
func (ns *cniNS) Close() error {
	// Update the sampling baseline for the next consumer.
	if ns.prevSample != nil {
		ns.offsetSample = ns.prevSample
		ns.prevSample = nil
	}

	if ns.pool == nil {
		// Non-pooled (custom hostname or Windows): release immediately.
		return ns.release()
	}
	ns.pool.put(ns)
	return nil
}

// Sample returns traffic statistics for this namespace relative to the
// baseline captured at acquisition time.
//
// Returns (nil, nil) when sampling is not available (no veth, non-Linux, or
// failed initialisation).
func (ns *cniNS) Sample() (*resourcestypes.NetworkSample, error) {
	if !ns.canSample {
		return nil, nil
	}

	var raw *resourcestypes.NetworkSample
	// Teardowns execute asynchronously in the background so they aren't
	// interrupted if the client cancels the master request.
	err := withDetachedNetNSIfAny(context.Background(), func(_ context.Context) error {
		var err error
		raw, err = ns.sample()
		return err
	})
	if err != nil {
		return nil, err
	}
	if raw == nil {
		return nil, nil
	}

	return subtractSample(raw, ns.offsetSample), nil
}

// subtractSample returns the difference (raw − offset) for each counter field.
// If offset is nil (no baseline recorded), raw is returned unchanged.
func subtractSample(raw, offset *resourcestypes.NetworkSample) *resourcestypes.NetworkSample {
	if offset == nil {
		return raw
	}
	return &resourcestypes.NetworkSample{
		TxBytes:   raw.TxBytes - offset.TxBytes,
		RxBytes:   raw.RxBytes - offset.RxBytes,
		TxPackets: raw.TxPackets - offset.TxPackets,
		RxPackets: raw.RxPackets - offset.RxPackets,
		TxErrors:  raw.TxErrors - offset.TxErrors,
		RxErrors:  raw.RxErrors - offset.RxErrors,
		TxDropped: raw.TxDropped - offset.TxDropped,
		RxDropped: raw.RxDropped - offset.RxDropped,
	}
}

// ─── Internal release ─────────────────────────────────────────────────────────

// release tears down the CNI configuration, unmounts the netns bind-mount, and
// removes the netns file.  All three steps are attempted; the first non-nil
// error is returned.
//
// release is called from:
//   - nsPool.close          – bulk drain on provider shutdown.
//   - nsPool.cleanupToTarget – LRU eviction.
//   - nsPool.put            – when pool is already closed.
//   - ns.Close              – for non-pooled namespaces.
func (ns *cniNS) release() error {
	bklog.L.Debugf("cniprovider: releasing namespace %s", ns.id)

	// Step 1: Ask CNI to tear down network plumbing (routes, iptables rules,
	// IPAM lease release, bridge port removal, etc.).
	// Deallocations use background contexts to ensure teardown finishes reliably.
	err := ns.handle.Remove(context.Background(), ns.id, ns.nativeID, ns.opts...)
	if err != nil {
		bklog.L.WithError(err).Warnf("cniprovider: CNI Remove failed for namespace %s", ns.id)
	}

	// Step 2: Detach the bind-mount so the kernel can reclaim the netns.
	// EINVAL / ENOENT are benign — they mean the mount was never set up or was
	// already removed.
	if err2 := unmountNetNS(ns.nativeID); err2 != nil && err == nil {
		err = errors.Wrapf(err2, "cniprovider: unmount netns %s", ns.nativeID)
	}

	// Step 3: Remove the backing file.
	if err3 := deleteNetNS(ns.nativeID); err3 != nil && err == nil {
		err = errors.Wrapf(err3, "cniprovider: delete netns file %s", ns.nativeID)
	}

	return err
}
