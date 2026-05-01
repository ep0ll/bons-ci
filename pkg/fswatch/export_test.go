package fanwatch

// OverlayInfoFromMountFile is a test-only export of the private
// overlayInfoFromMountFile function. It lives in export_test.go so it is only
// compiled into the test binary and never included in the production package.
func OverlayInfoFromMountFile(mountFile, mergedDir string) (*OverlayInfo, error) {
	return overlayInfoFromMountFile(mountFile, mergedDir)
}
