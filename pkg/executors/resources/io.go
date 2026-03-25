package resources

// io.go – cgroupv2 I/O controller reader.
//
// Reads:
//   - io.stat      per-device counters, summed across all devices
//   - io.pressure  PSI stall metrics (requires CONFIG_PSI=y)
//
// io.stat format (one device per line):
//
//	MAJ:MIN rbytes=N wbytes=N dbytes=N rios=N wios=N dios=N
//
// Values are summed across all device lines.  Per-device breakdowns are
// discarded because BuildKit workloads are single-root-device by design and
// presenting per-device data would add complexity with little benefit.
//
// The first field on each line is the MAJ:MIN device number, which is skipped.

import (
	"context"
	"errors"
	"path/filepath"
	"strings"

	resourcestypes "github.com/bons/bons-ci/pkg/executors/resources/types"
)

// ─── io.stat field names ─────────────────────────────────────────────────────

const (
	ioKeyReadBytes    = "rbytes"
	ioKeyWriteBytes   = "wbytes"
	ioKeyDiscardBytes = "dbytes"
	ioKeyReadIOs      = "rios"
	ioKeyWriteIOs     = "wios"
	ioKeyDiscardIOs   = "dios"
)

// ─── cgroup file names ────────────────────────────────────────────────────────

const (
	ioStatFile     = "io.stat"
	ioPressureFile = "io.pressure"
)

// ─── ioController ────────────────────────────────────────────────────────────

// ioController implements Controller for the cgroupv2 I/O subsystem.
type ioController struct{}

// Name returns the cgroupv2 controller name.
func (ioController) Name() string { return "io" }

// Collect reads io.stat and io.pressure, populates dst.IOStat.
func (ioController) Collect(_ context.Context, cgroupPath string, dst *resourcestypes.Sample) error {
	stat, err := collectIOStat(cgroupPath)
	if err != nil {
		return err
	}
	dst.IOStat = stat
	return nil
}

// ─── Collection logic ─────────────────────────────────────────────────────────

// collectIOStat reads and aggregates the io.stat and io.pressure files.
// Returns (nil, nil) when io.stat is absent (io controller not enabled).
func collectIOStat(cgroupPath string) (*resourcestypes.IOStat, error) {
	data, err := readFileIfExists(filepath.Join(cgroupPath, ioStatFile))
	if err != nil {
		return nil, err
	}
	if data == nil {
		return nil, nil // io controller not active
	}

	stat := &resourcestypes.IOStat{}
	if err := parseIOStatData(string(data), stat); err != nil {
		return nil, err
	}

	// ── io.pressure ──────────────────────────────────────────────────────────
	pressure, err := parsePressureFile(filepath.Join(cgroupPath, ioPressureFile))
	if err != nil {
		return nil, err
	}
	stat.Pressure = pressure

	return stat, nil
}

// parseIOStatData parses the content of an io.stat file into stat,
// accumulating values across all device lines.
func parseIOStatData(content string, stat *resourcestypes.IOStat) error {
	for line := range strings.SplitSeq(content, "\n") {
		fields := strings.Fields(line)
		if len(fields) < 2 {
			continue
		}
		// fields[0] is MAJ:MIN — skip it.
		for _, token := range fields[1:] {
			key, value := parseKVPair(token)
			if key == "" {
				continue
			}
			accumulateIOField(stat, key, value)
		}
	}
	return nil
}

// accumulateIOField adds value to the appropriate IOStat counter identified by key.
// Fields not recognised are silently ignored (forward-compatibility).
func accumulateIOField(stat *resourcestypes.IOStat, key string, value uint64) {
	switch key {
	case ioKeyReadBytes:
		stat.ReadBytes = addUint64Ptr(stat.ReadBytes, value)
	case ioKeyWriteBytes:
		stat.WriteBytes = addUint64Ptr(stat.WriteBytes, value)
	case ioKeyDiscardBytes:
		stat.DiscardBytes = addUint64Ptr(stat.DiscardBytes, value)
	case ioKeyReadIOs:
		stat.ReadIOs = addUint64Ptr(stat.ReadIOs, value)
	case ioKeyWriteIOs:
		stat.WriteIOs = addUint64Ptr(stat.WriteIOs, value)
	case ioKeyDiscardIOs:
		stat.DiscardIOs = addUint64Ptr(stat.DiscardIOs, value)
	}
}

// addUint64Ptr returns a pointer to (existing + delta).
// If existing is nil, returns uint64Ptr(delta).
func addUint64Ptr(existing *uint64, delta uint64) *uint64 {
	if existing != nil {
		*existing += delta
		return existing
	}
	return uint64Ptr(delta)
}

// readFileIfExists reads a file and returns its content.
// Returns (nil, nil) if the file does not exist.
func readFileIfExists(path string) ([]byte, error) {
	data, err := readFile(path)
	if errors.Is(err, errNotExist) {
		return nil, nil
	}
	return data, err
}
