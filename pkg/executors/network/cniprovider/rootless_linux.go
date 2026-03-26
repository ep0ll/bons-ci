//go:build linux

package cniprovider

import (
	"context"
	"os"
	"path/filepath"

	cnins "github.com/containernetworking/plugins/pkg/ns"
	"github.com/moby/buildkit/util/bklog"
	"github.com/pkg/errors"
)

// withDetachedNetNSIfAny executes fn inside RootlessKit's "detached" network
// namespace when the daemon is running in rootless mode with --detach-netns.
// Otherwise, fn is executed in the current network namespace.
//
// # Background
//
// RootlessKit ≥ 2.0 with --detach-netns keeps the daemon in the host network
// namespace but creates a separate "slirp" namespace for container networking.
// That namespace is bind-mounted at $ROOTLESSKIT_STATE_DIR/netns.  Any CNI
// operations (Setup, Remove, bridge creation) must run inside it.
//
// The detached-netns path is stored in the context so that downstream code
// (setupCNI) can switch from parallel to serial CNI execution.  Parallel
// goroutines inside netns.Do cause setns(2) to be applied to an unpredictable
// OS thread; SetupSerially avoids this by executing plugins one at a time on
// the calling goroutine.
//
// References:
//   - https://github.com/rootless-containers/rootlesskit/pull/379
//   - https://github.com/containerd/nerdctl/pull/2723
func withDetachedNetNSIfAny(ctx context.Context, fn func(context.Context) error) error {
	stateDir := os.Getenv("ROOTLESSKIT_STATE_DIR")
	if stateDir == "" {
		return fn(ctx)
	}

	detachedNS := filepath.Join(stateDir, "netns")
	if _, err := os.Lstat(detachedNS); errors.Is(err, os.ErrNotExist) {
		// Env var set but netns file absent — older RootlessKit without
		// --detach-netns; run in current namespace.
		return fn(ctx)
	}

	return cnins.WithNetNSPath(detachedNS, func(_ cnins.NetNS) error {
		// Annotate the context so callers know we are inside the detached netns.
		ctx = context.WithValue(ctx, contextKeyDetachedNetNS, detachedNS)
		bklog.G(ctx).Debugf("cniprovider: entering detached netns %q", detachedNS)
		err := fn(ctx)
		bklog.G(ctx).WithError(err).Debugf("cniprovider: leaving detached netns %q", detachedNS)
		return err
	})
}
