//go:build linux

package cniprovider

import (
	"path/filepath"
	"strconv"
	"strings"
	"syscall"

	resourcestypes "github.com/bons/bons-ci/pkg/executors/resources/types"
	"github.com/pkg/errors"
)

// statCounters enumerates the /sys/class/net/<iface>/statistics file names in
// the order they are read.  Using a slice preserves a deterministic read order
// and makes it trivial to add new counters.
var statCounters = []struct {
	name string
	set  func(*resourcestypes.NetworkSample, int64)
}{
	{"tx_bytes", func(s *resourcestypes.NetworkSample, v int64) { s.TxBytes = v }},
	{"rx_bytes", func(s *resourcestypes.NetworkSample, v int64) { s.RxBytes = v }},
	{"tx_packets", func(s *resourcestypes.NetworkSample, v int64) { s.TxPackets = v }},
	{"rx_packets", func(s *resourcestypes.NetworkSample, v int64) { s.RxPackets = v }},
	{"tx_errors", func(s *resourcestypes.NetworkSample, v int64) { s.TxErrors = v }},
	{"rx_errors", func(s *resourcestypes.NetworkSample, v int64) { s.RxErrors = v }},
	{"tx_dropped", func(s *resourcestypes.NetworkSample, v int64) { s.TxDropped = v }},
	{"rx_dropped", func(s *resourcestypes.NetworkSample, v int64) { s.RxDropped = v }},
}

// sample reads raw traffic counters from the kernel's sysfs statistics
// directory for ns.vethName.
//
// Implementation notes:
//   - A single syscall.Open on the statistics directory gives us a directory fd
//     (dirfd) which we then use with syscall.Openat for each counter file.
//     This saves one path-resolution syscall per counter vs. using os.ReadFile.
//   - The read buffer (32 bytes) is stack-allocated (escape analysis keeps it
//     on-stack for small sizes); no heap allocation per sample call.
//   - ENOENT / ENOTDIR on the directory open is treated as "not yet available"
//     (the veth may not exist yet if CNI setup is still running) and returns
//     (nil, nil) rather than an error.
//
// The result is stored in ns.prevSample so that Close() can advance the
// sampling offset when the namespace is returned to the pool.
func (ns *cniNS) sample() (*resourcestypes.NetworkSample, error) {
	statsDir := filepath.Join("/sys/class/net", ns.vethName, "statistics")

	dirfd, err := syscall.Open(statsDir, syscall.O_RDONLY, 0)
	if err != nil {
		if errors.Is(err, syscall.ENOENT) || errors.Is(err, syscall.ENOTDIR) {
			return nil, nil // interface not yet present; not an error
		}
		return nil, errors.Wrapf(err, "cniprovider: open sysfs dir %s", statsDir)
	}
	defer syscall.Close(dirfd) //nolint:errcheck

	var buf [32]byte // 32 bytes is enough for any uint64 decimal representation
	stat := &resourcestypes.NetworkSample{}

	for _, c := range statCounters {
		v, err := readInt64At(dirfd, c.name, buf[:])
		if err != nil {
			return nil, errors.Wrapf(err, "cniprovider: read counter %s for %s", c.name, ns.vethName)
		}
		c.set(stat, v)
	}

	ns.prevSample = stat
	return stat, nil
}

// readInt64At reads the decimal integer in the sysfs file named filename
// relative to dirfd and parses it as int64.
//
// We use Openat (not Open) to avoid a second path lookup on the directory;
// all files share the same parent fd.
func readInt64At(dirfd int, filename string, buf []byte) (int64, error) {
	fd, err := syscall.Openat(dirfd, filename, syscall.O_RDONLY, 0)
	if err != nil {
		return 0, errors.Wrapf(err, "openat %s", filename)
	}
	defer syscall.Close(fd) //nolint:errcheck

	n, err := syscall.Read(fd, buf)
	if err != nil {
		return 0, errors.Wrapf(err, "read %s", filename)
	}

	v, err := strconv.ParseInt(strings.TrimSpace(string(buf[:n])), 10, 64)
	if err != nil {
		return 0, errors.Wrapf(err, "parse %s", filename)
	}
	return v, nil
}
