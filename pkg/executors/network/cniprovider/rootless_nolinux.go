//go:build !linux

package cniprovider

import "context"

// withDetachedNetNSIfAny is a no-op on non-Linux platforms.
// RootlessKit's detached-netns mechanism is Linux-specific.
func withDetachedNetNSIfAny(ctx context.Context, fn func(context.Context) error) error {
	return fn(ctx)
}
