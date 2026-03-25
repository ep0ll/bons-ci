package resources

// cpu.go – cgroupv2 CPU controller reader.
//
// Reads:
//   - cpu.stat   (usage_usec, user_usec, system_usec, nr_periods,
//                 nr_throttled, throttled_usec)
//   - cpu.pressure (PSI, requires CONFIG_PSI=y)
//
// All time fields in cpu.stat are in microseconds; they are converted to
// nanoseconds (* 1000) so the output type is directly comparable to
// Go's time.Duration.

import (
	"context"
	"path/filepath"

	resourcestypes "github.com/bons/bons-ci/pkg/executors/resources/types"
)

// ─── cpu.stat field names ─────────────────────────────────────────────────────

const (
	cpuKeyUsageUsec     = "usage_usec"
	cpuKeyUserUsec      = "user_usec"
	cpuKeySystemUsec    = "system_usec"
	cpuKeyNrPeriods     = "nr_periods"
	cpuKeyNrThrottled   = "nr_throttled"
	cpuKeyThrottledUsec = "throttled_usec"
)

// ─── cpuController ────────────────────────────────────────────────────────────

// cpuController implements Controller for the cgroupv2 cpu subsystem.
type cpuController struct{}

// Name returns the cgroupv2 controller name.
func (cpuController) Name() string { return "cpu" }

// Collect reads cpu.stat and cpu.pressure and populates dst.CPUStat.
// Returns (nil) when either file does not exist (cpu controller not enabled).
func (cpuController) Collect(_ context.Context, cgroupPath string, dst *resourcestypes.Sample) error {
	stat, err := collectCPUStat(cgroupPath)
	if err != nil {
		return err
	}
	dst.CPUStat = stat
	return nil
}

// ─── Collection logic ─────────────────────────────────────────────────────────

// collectCPUStat reads cpu.stat and cpu.pressure from cgroupPath and
// returns a populated *CPUStat.  Returns (nil, nil) if cpu.stat is absent.
func collectCPUStat(cgroupPath string) (*resourcestypes.CPUStat, error) {
	stat := &resourcestypes.CPUStat{}
	found := false // becomes true when at least one known field is read

	err := parseKVFile(filepath.Join(cgroupPath, "cpu.stat"), func(key string, value uint64) {
		found = true
		switch key {
		case cpuKeyUsageUsec:
			stat.UsageNanos = uint64Ptr(value * 1000)
		case cpuKeyUserUsec:
			stat.UserNanos = uint64Ptr(value * 1000)
		case cpuKeySystemUsec:
			stat.SystemNanos = uint64Ptr(value * 1000)
		case cpuKeyNrPeriods:
			stat.NrPeriods = uint32Ptr(uint32(value))
		case cpuKeyNrThrottled:
			stat.NrThrottled = uint32Ptr(uint32(value))
		case cpuKeyThrottledUsec:
			stat.ThrottledNanos = uint64Ptr(value * 1000)
		}
	})
	if err != nil {
		return nil, err
	}
	if !found {
		return nil, nil // cpu controller not active for this cgroup
	}

	pressure, err := parsePressureFile(filepath.Join(cgroupPath, "cpu.pressure"))
	if err != nil {
		return nil, err
	}
	stat.Pressure = pressure

	return stat, nil
}
