package resources

// parse.go – low-level cgroup file parsing primitives.
//
// All functions in this file are pure (no side effects beyond file I/O) and
// package-private. They form the single source of truth for reading cgroup
// pseudo-files so that controller implementations (cpu.go, memory.go, io.go,
// pids.go) never duplicate parsing logic.
//
// Error handling policy
//
//   - os.ErrNotExist  → return (zero, nil): controller/file not enabled.
//   - syscall.ENOTSUP → return (zero, nil): kernel lacks feature (CONFIG_PSI=n).
//   - All other errors → return wrapped error carrying the offending file path.

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
// occasionally expose future fields the current parser does not understand.
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
		key, value, ok := splitKVLine(scanner.Text())
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

// splitKVLine splits a space-separated "key value" line into its components.
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

// parseKVPair splits a "key=value" token as used in io.stat lines.
// Returns ("", 0) when the token is malformed or the value is not parseable
// as a uint64.
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

// parseFloatKVPair splits a "key=value" token and parses the value as a float64.
// Returns ("", 0, false) when the token has no '=' or the value is not a valid float.
//
// NOTE: This function is intentionally NOT used by parsePressureValues — that
// function dispatches on the key name first, then selects the correct parser type
// (float64 vs uint64) to avoid ambiguity on the "total" field.  parseFloatKVPair
// remains here as a package-level utility exercised by TestParseFloatKVPair.
func parseFloatKVPair(token string) (key string, value float64, ok bool) {
	idx := strings.IndexByte(token, '=')
	if idx < 0 {
		return "", 0, false
	}
	v, err := strconv.ParseFloat(token[idx+1:], 64)
	if err != nil {
		return "", 0, false
	}
	return token[:idx], v, true
}

// ─── Single-value file parsing ────────────────────────────────────────────────

// parseSingleUint64File reads a cgroup file that contains exactly one uint64
// value (e.g. memory.peak, pids.current, memory.swap.current).
//
// Trailing whitespace and newlines are stripped before parsing.
// The literal string "max" is returned as (0, false, nil) — callers treat a
// false present flag as "unlimited/absent" rather than an error.
// Returns (0, false, nil) when the file does not exist.
func parseSingleUint64File(filePath string) (value uint64, present bool, err error) {
	data, readErr := os.ReadFile(filePath)
	if readErr != nil {
		if errors.Is(readErr, os.ErrNotExist) {
			return 0, false, nil
		}
		return 0, false, fmt.Errorf("read %q: %w", filePath, readErr)
	}

	s := strings.TrimSpace(string(data))
	if s == "max" {
		// "max" means unlimited/unset; represent as absent.
		return 0, false, nil
	}

	v, parseErr := strconv.ParseUint(s, 10, 64)
	if parseErr != nil {
		return 0, false, fmt.Errorf("parse %q from %q: %w", s, filePath, parseErr)
	}
	return v, true, nil
}

// ─── PSI pressure file parsing ────────────────────────────────────────────────

// parsePressureFile parses a Linux PSI pressure file.
//
// File format (two lines, one per stall category):
//
//	some avg10=N avg60=N avg300=N total=N
//	full avg10=N avg60=N avg300=N total=N
//
// Returns (nil, nil) when:
//   - The file does not exist (cgroup or /proc/pressure not mounted).
//   - The kernel returns ENOTSUP/EOPNOTSUPP (CONFIG_PSI not compiled in).
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

	// strings.Split on a newline-terminated file always yields a trailing
	// empty element; the inner trim+empty check handles it gracefully.
	for _, line := range strings.Split(string(data), "\n") {
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

// parsePressureValues converts the key=value tokens from a single PSI line into
// a PressureValues struct.
//
// PSI token types by key name:
//
//	avg10, avg60, avg300 → float64  (exponential moving averages, 0.0–100.0)
//	total                → uint64   (cumulative stall time in microseconds)
//
// ⚠ Bug avoidance note: the key name is checked FIRST, then the value is
// parsed with the type that matches that specific key.  This is critical because
// "total=3031" would be accepted by a float64 parser (3031 is a valid float64),
// causing the uint64 branch to be silently skipped and leaving pv.Total == nil.
// Dispatching on key name before parsing eliminates this ambiguity entirely.
//
// Returns nil when no recognised PSI fields are found in tokens.
func parsePressureValues(tokens []string) *resourcestypes.PressureValues {
	pv := &resourcestypes.PressureValues{}
	found := false

	for _, token := range tokens {
		idx := strings.IndexByte(token, '=')
		if idx <= 0 || idx == len(token)-1 {
			// Reject tokens with no '=', a leading '=', or no value after '='.
			continue
		}
		key := token[:idx]
		valStr := token[idx+1:]

		// Dispatch on key name FIRST to select the correct parser.
		// Never attempt to infer the type from whether the value parses as float.
		switch key {
		case "total":
			// uint64: cumulative stall duration in microseconds.
			// Cannot be stored as float64 without precision loss on long-running
			// systems where total can exceed 2^53 microseconds (~285 years).
			v, err := strconv.ParseUint(valStr, 10, 64)
			if err == nil {
				pv.Total = uint64Ptr(v)
				found = true
			}
		case "avg10":
			v, err := strconv.ParseFloat(valStr, 64)
			if err == nil {
				pv.Avg10 = float64Ptr(v)
				found = true
			}
		case "avg60":
			v, err := strconv.ParseFloat(valStr, 64)
			if err == nil {
				pv.Avg60 = float64Ptr(v)
				found = true
			}
		case "avg300":
			v, err := strconv.ParseFloat(valStr, 64)
			if err == nil {
				pv.Avg300 = float64Ptr(v)
				found = true
			}
			// Unknown keys are silently ignored for forward-compatibility with
			// future kernel PSI extensions (e.g. per-CPU stall metrics).
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
