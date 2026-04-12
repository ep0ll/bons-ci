package httpapplier

// adaptor.go – ContainerdAdaptor bridges HTTPApplier to the containerd
// diff.Applier interface.
//
// Containerd's diff.Applier is:
//
//	type Applier interface {
//	    Apply(ctx context.Context, desc ocispec.Descriptor, mounts []mount.Mount, opts ...ApplyOpt) (ocispec.Descriptor, error)
//	}
//
// Because our HTTPApplier carries the identical signature we only need to
// handle the option type conversion.  The bridge is intentionally thin so that
// the two interfaces can evolve independently.
//
// Usage:
//
//	httpApp := httpapplier.New(httpapplier.Options{...})
//	ctdApp  := httpapplier.NewContainerdAdaptor(httpApp)
//	// ctdApp.Apply(ctx, desc, mounts) now satisfies containerd/diff.Applier

import (
	"context"

	"github.com/containerd/containerd/v2/core/mount"
	ocispec "github.com/opencontainers/image-spec/specs-go/v1"
)

// ContainerdApplyOpt is the function type used by containerd's diff package.
// It is an alias so callers can use this package's options directly in
// containerd contexts without import-cycle issues.
type ContainerdApplyOpt = ApplyOpt

// containerdAdaptor wraps an HTTPApplier and exposes the containerd Apply
// method signature, bridging any option conversion needed.
type containerdAdaptor struct {
	inner HTTPApplier
}

// NewContainerdAdaptor returns an adaptor that wraps inner and satisfies
// both HTTPApplier and containerd's diff.Applier (structurally, via duck typing).
//
// The returned value also implements ContainerdApplierAdaptor so callers can
// type-assert to verify the adaptor is in place.
func NewContainerdAdaptor(inner HTTPApplier) ContainerdApplierAdaptor {
	return &containerdAdaptor{inner: inner}
}

// Apply delegates to the wrapped HTTPApplier, converting ContainerdApplyOpt
// values (which are type-identical to ApplyOpt) transparently.
func (a *containerdAdaptor) Apply(
	ctx context.Context,
	desc ocispec.Descriptor,
	mounts []mount.Mount,
	opts ...ApplyOpt,
) (ocispec.Descriptor, error) {
	return a.inner.Apply(ctx, desc, mounts, opts...)
}

// ─── Multi-applier chain ──────────────────────────────────────────────────────

// ChainApplier tries each applier in order and returns the first success.
// This enables a fallback strategy: e.g. try HTTP mirror, fall back to S3,
// fall back to a local cache.
type ChainApplier struct {
	appliers []HTTPApplier
}

// NewChainApplier wraps multiple HTTPAppliers into a fallback chain.
func NewChainApplier(appliers ...HTTPApplier) *ChainApplier {
	return &ChainApplier{appliers: appliers}
}

// Apply tries each inner applier in registration order.  It returns the first
// successful result.  If all fail, it returns the last error.
func (c *ChainApplier) Apply(
	ctx context.Context,
	desc ocispec.Descriptor,
	mounts []mount.Mount,
	opts ...ApplyOpt,
) (ocispec.Descriptor, error) {
	var lastErr error
	for _, a := range c.appliers {
		out, err := a.Apply(ctx, desc, mounts, opts...)
		if err == nil {
			return out, nil
		}
		lastErr = err
		// Continue to next applier; check context cancellation between tries.
		if ctx.Err() != nil {
			return ocispec.Descriptor{}, ctx.Err()
		}
	}
	return ocispec.Descriptor{}, lastErr
}

// ─── No-op / passthrough appliers (testing) ──────────────────────────────────

// NoopApplier is an HTTPApplier that does nothing and returns the input
// descriptor unchanged.  Useful in tests and dry-run mode.
type NoopApplier struct{}

func (NoopApplier) Apply(
	ctx context.Context,
	desc ocispec.Descriptor,
	_ []mount.Mount,
	_ ...ApplyOpt,
) (ocispec.Descriptor, error) {
	return desc, nil
}

// RecordingApplier records each Apply call for inspection in tests.
type RecordingApplier struct {
	Calls []ApplyCall
	inner HTTPApplier
}

// ApplyCall captures one invocation of Apply.
type ApplyCall struct {
	Desc   ocispec.Descriptor
	Mounts []mount.Mount
}

// NewRecordingApplier wraps inner with a recording layer.
// Pass NoopApplier{} as inner to capture calls without side effects.
func NewRecordingApplier(inner HTTPApplier) *RecordingApplier {
	return &RecordingApplier{inner: inner}
}

func (r *RecordingApplier) Apply(
	ctx context.Context,
	desc ocispec.Descriptor,
	mounts []mount.Mount,
	opts ...ApplyOpt,
) (ocispec.Descriptor, error) {
	r.Calls = append(r.Calls, ApplyCall{Desc: desc, Mounts: mounts})
	return r.inner.Apply(ctx, desc, mounts, opts...)
}
