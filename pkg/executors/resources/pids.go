package resources

// pids.go – cgroupv2 PIDs controller reader.
//
// Reads:
//   - pids.current  live process count in this cgroup
//   - pids.max      configured limit ("max" → unlimited → nil pointer)
//
// The PIDs controller is lightweight (two single-integer files) and does not
// have a pressure file.

import (
	"context"
	"path/filepath"

	resourcestypes "github.com/bons/bons-ci/pkg/executors/resources/types"
)

// ─── cgroup file names ────────────────────────────────────────────────────────

const (
	pidsCurrentFile = "pids.current"
	pidsMaxFile     = "pids.max"
)

// ─── pidsController ──────────────────────────────────────────────────────────

// pidsController implements Controller for the cgroupv2 pids subsystem.
type pidsController struct{}

// Name returns the cgroupv2 controller name.
func (pidsController) Name() string { return "pids" }

// Collect reads pids.current and pids.max, populates dst.PIDsStat.
func (pidsController) Collect(_ context.Context, cgroupPath string, dst *resourcestypes.Sample) error {
	stat, err := collectPIDsStat(cgroupPath)
	if err != nil {
		return err
	}
	dst.PIDsStat = stat
	return nil
}

// ─── Collection logic ─────────────────────────────────────────────────────────

// collectPIDsStat reads pids.current and pids.max for cgroupPath.
// Returns (nil, nil) when pids.current is absent (pids controller not active).
func collectPIDsStat(cgroupPath string) (*resourcestypes.PIDsStat, error) {
	current, ok, err := parseSingleUint64File(filepath.Join(cgroupPath, pidsCurrentFile))
	if err != nil {
		return nil, err
	}
	if !ok {
		return nil, nil // pids controller not active for this cgroup
	}

	stat := &resourcestypes.PIDsStat{
		Current: uint64Ptr(current),
	}

	// pids.max contains a numeric limit or the literal string "max" (= unlimited).
	// parseSingleUint64File returns ok=false for "max", so a nil Limit correctly
	// means "unlimited".
	if limit, ok, err := parseSingleUint64File(filepath.Join(cgroupPath, pidsMaxFile)); err != nil {
		return nil, err
	} else if ok {
		stat.Limit = uint64Ptr(limit)
	}

	return stat, nil
}
