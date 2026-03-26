//go:build !windows

package oci

// normalizeMountType is a no-op on non-Windows platforms; mount types such as
// "bind" and "overlay" are passed through to the kernel as-is.
func normalizeMountType(mType string) string {
	return mType
}
