package resources

// parse.go – low-level cgroup file parsing primitives.
//
// All functions in this file are pure (no side effects beyond file I/O) and
// exported only within the package.  They form the single source of truth for
// reading cgroup pseudo-files so that the controller implementations
// (cpu.go, memory.go, io.go, pids.go) never duplicate parsing logic.
//
// Error handling policy:
//   - os.ErrNotExist → return (zero, nil): the controller/file is not enabled.
//   - syscall.ENOTSUP / EOPNOTSUPP → return (zero, nil): kernel lacks feature
//     (e.g. CONFIG_PSI not set).
//   - All other errors → return wrapped error with the offending file path.

import (
	"bufio"
	"errors"
	"fmt"
	"os"
	"strconv"
	"strings"
	"syscall"

	resourcestypes "github.com/bons/bons-ci/pkg/executors/resources/types"
)

// ─── Key-value file parsing ───────────────────────────────────────────────────

// parseKVFile reads a cgroup flat-key-value file (one "key value\n" per line)
// and invokes callback for every successfully parsed entry.
//
// Lines with fewer than two whitespace-separated fields, or whose second field
// cannot be parsed as a uint64, are silently skipped — cgroup files
// occasionally contain future fields the current parser does not understand.
//
// Returns nil when the file does not exist (controller not enabled).
func parseKVFile(filePath string, callback func(key string, value uint64)) error {
	f, err := os.Open(filePath)
	if err != nil {
		if errors.Is(err, os.ErrNotExist) {
			return nil
		}
		return fmt.Errorf("open %q: %w", filePath, err)
	}
	defer f.Close()

	scanner := bufio.NewScanner(f)
	for scanner.Scan() {
		line := scanner.Text()
		key, value, ok := splitKVLine(line)
		if !ok {
			continue
		}
		callback(key, value)
	}
	if err := scanner.Err(); err != nil {
		return fmt.Errorf("scan %q: %w", filePath, err)
	}
	return nil
}

// splitKVLine splits a "key value" line into its components.
// Returns ok=false when the line is malformed or the value is not a uint64.
func splitKVLine(line string) (key string, value uint64, ok bool) {
	fields := strings.Fields(line)
	if len(fields) < 2 {
		return "", 0, false
	}
	v, err := strconv.ParseUint(fields[1], 10, 64)
	if err != nil {
		return "", 0, false
	}
	return fields[0], v, true
}

// parseKVPair splits a "key=value" token as used in io.stat and pressure files.
// Returns ("", 0) when the token is malformed or not parseable as uint64.
func parseKVPair(token string) (key string, value uint64) {
	idx := strings.IndexByte(token, '=')
	if idx < 0 {
		return "", 0
	}
	k := token[:idx]
	v, err := strconv.ParseUint(token[idx+1:], 10, 64)
	if err != nil {
		return "", 0
	}
	return k, v
}

// parseFloatKVPair is like parseKVPair but parses the value as a float64.
// Used for PSI avg10/avg60/avg300 fields.
func parseFloatKVPair(token string) (key string, value float64, ok bool) {
	idx := strings.IndexByte(token, '=')
	if idx < 0 {
		return "", 0, false
	}
	k := token[:idx]
	v, err := strconv.ParseFloat(token[idx+1:], 64)
	if err != nil {
		return "", 0, false
	}
	return k, v, true
}

// ─── Single-value file parsing ────────────────────────────────────────────────

// parseSingleUint64File reads a cgroup file that contains exactly one uint64
// value (e.g. memory.peak, pids.current, memory.swap.current).
//
// Trailing whitespace and newlines are stripped before parsing.
// Returns (0, nil) when the file does not exist.
func parseSingleUint64File(filePath string) (uint64, bool, error) {
	data, err := os.ReadFile(filePath)
	if err != nil {
		if errors.Is(err, os.ErrNotExist) {
			return 0, false, nil
		}
		return 0, false, fmt.Errorf("read %q: %w", filePath, err)
	}
	valueStr := strings.TrimSpace(string(data))
	// cgroup files may contain "max" instead of a numeric limit.
	if valueStr == "max" {
		return 0, false, nil // treat unlimited as absent
	}
	v, err := strconv.ParseUint(valueStr, 10, 64)
	if err != nil {
		return 0, false, fmt.Errorf("parse %q value %q: %w", filePath, valueStr, err)
	}
	return v, true, nil
}

// ─── PSI pressure file parsing ────────────────────────────────────────────────

// parsePressureFile parses a Linux PSI pressure file.
//
// Format (one line per stall category):
//
//	some avg10=N avg60=N avg300=N total=N
//	full avg10=N avg60=N avg300=N total=N
//
// Returns (nil, nil) when:
//   - The file does not exist (controller not mounted).
//   - The kernel returns ENOTSUP / EOPNOTSUPP (CONFIG_PSI not enabled).
func parsePressureFile(filePath string) (*resourcestypes.Pressure, error) {
	data, err := os.ReadFile(filePath)
	if err != nil {
		switch {
		case errors.Is(err, os.ErrNotExist),
			errors.Is(err, syscall.ENOTSUP),
			errors.Is(err, syscall.EOPNOTSUPP):
			return nil, nil
		}
		return nil, fmt.Errorf("read pressure %q: %w", filePath, err)
	}

	pressure := &resourcestypes.Pressure{}
	populated := false

	for line := range strings.SplitSeq(string(data), "\n") {
		line = strings.TrimSpace(line)
		if line == "" {
			continue
		}
		fields := strings.Fields(line)
		if len(fields) < 2 {
			continue
		}
		prefix := fields[0]
		pv := parsePressureValues(fields[1:])
		if pv == nil {
			continue
		}
		populated = true
		switch prefix {
		case "some":
			pressure.Some = pv
		case "full":
			pressure.Full = pv
		}
	}

	if !populated {
		return nil, nil
	}
	return pressure, nil
}

// parsePressureValues converts the key=value tokens from a single pressure line
// into a PressureValues struct. Returns nil when no recognisable fields are found.
func parsePressureValues(tokens []string) *resourcestypes.PressureValues {
	pv := &resourcestypes.PressureValues{}
	found := false

	for _, token := range tokens {
		if k, v, ok := parseFloatKVPair(token); ok {
			found = true
			switch k {
			case "avg10":
				pv.Avg10 = float64Ptr(v)
			case "avg60":
				pv.Avg60 = float64Ptr(v)
			case "avg300":
				pv.Avg300 = float64Ptr(v)
			}
			continue
		}
		// "total" is a uint64, not float64.
		if k, v := parseKVPair(token); k == "total" {
			found = true
			pv.Total = uint64Ptr(v)
		}
	}

	if !found {
		return nil
	}
	return pv
}

// ─── Pointer helpers ──────────────────────────────────────────────────────────

// uint64Ptr returns a pointer to a copy of v.
func uint64Ptr(v uint64) *uint64 { return &v }

// float64Ptr returns a pointer to a copy of v.
func float64Ptr(v float64) *float64 { return &v }

// uint32Ptr returns a pointer to a copy of v.
func uint32Ptr(v uint32) *uint32 { return &v }
