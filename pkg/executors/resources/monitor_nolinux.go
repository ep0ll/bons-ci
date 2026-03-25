//go:build !linux

package resources

// isCgroup2 always returns false on non-Linux platforms.
// cgroupv2 is a Linux-only kernel feature.
func isCgroup2() bool { return false }
