package fswatch

// OverlayInfoFromMountFile is a test-only export that allows overlay_test.go
// to exercise the mountinfo parsing logic with a custom mountinfo file path.
// It writes a temporary /proc/self/mountinfo substitute using the moby-backed
// GetMountsFromReader path exposed via mountinfo.GetMountsFromReader.
//
// Because moby/sys/mountinfo.GetMounts reads /proc/self/mountinfo directly and
// does not accept a custom file path, we expose overlayInfoFromMountReader
// which accepts an io.Reader — allowing tests to inject arbitrary mountinfo content.
func OverlayInfoFromMountFile(mountinfoPath, mergedDir string) (*OverlayInfo, error) {
	return overlayInfoFromMountFile(mountinfoPath, mergedDir)
}
