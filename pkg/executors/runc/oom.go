//go:build linux

package runcexecutor

import (
	"bufio"
	"context"
	"os"
	"path/filepath"
	"strconv"
	"strings"

	"github.com/moby/buildkit/util/bklog"
	"github.com/pkg/errors"
)

// ─── OOMDetector ─────────────────────────────────────────────────────────────

// OOMDetector inspects a cgroup v2 memory.events file after a container exits
// to determine whether the kernel's OOM killer terminated it.
//
// If OOM is detected the provided ExitError's OOMKilled field is set to true.
// The detector is intentionally a value type with no shared state so it is safe
// to call from multiple goroutines concurrently.
type OOMDetector struct {
	// mountpoint is the cgroup v2 filesystem root.
	// Defaults to "/sys/fs/cgroup" in production.
	mountpoint string
}

// DefaultOOMDetector uses the standard cgroup v2 mountpoint.
var DefaultOOMDetector = &OOMDetector{mountpoint: defaultCgroupMountpoint}

const defaultCgroupMountpoint = "/sys/fs/cgroup"

// NewOOMDetector returns an OOMDetector that reads from mountpoint.
// Use DefaultOOMDetector for production; this constructor is for testing.
func NewOOMDetector(mountpoint string) *OOMDetector {
	return &OOMDetector{mountpoint: mountpoint}
}

// CheckAndAnnotate reads the cgroup memory.events file for cgroupNS and, if
// oom_kill is non-zero, marks exitErr as an OOM kill.
//
// cgroupNS is the relative path component appended to mountpoint — it is
// the value of spec.Linux.CgroupsPath.  An empty cgroupNS is a no-op.
//
// This function never returns an error: OOM detection is best-effort and
// failures are logged at warning level so they do not mask the original exit.
func (d *OOMDetector) CheckAndAnnotate(ctx context.Context, cgroupNS string, exitErr *ExitError) {
	if cgroupNS == "" || exitErr == nil {
		return
	}

	cgroupDir := filepath.Join(d.mountpoint, cgroupNS)
	count, err := readMemoryEvent(cgroupDir, "oom_kill")
	if err != nil {
		bklog.G(ctx).WithError(err).Warnf("OOMDetector: failed to read oom_kill event for %s", cgroupNS)
		return
	}
	if count > 0 {
		exitErr.OOMKilled = true
	}
}

// ─── low-level reader ────────────────────────────────────────────────────────

// readMemoryEvent opens <cgroupPath>/memory.events and returns the integer value
// associated with the named event key (e.g. "oom_kill").
// Returns (0, nil) if the event key is absent but the file exists.
func readMemoryEvent(cgroupPath, event string) (uint64, error) {
	filePath := filepath.Join(cgroupPath, "memory.events")
	f, err := os.Open(filePath)
	if err != nil {
		return 0, errors.Wrapf(err, "failed to open %s", filePath)
	}
	defer f.Close()

	s := bufio.NewScanner(f)
	for s.Scan() {
		fields := strings.Fields(s.Text())
		if len(fields) != 2 {
			continue
		}
		if fields[0] != event {
			continue
		}
		v, err := strconv.ParseUint(fields[1], 10, 64)
		if err != nil {
			return 0, errors.Wrapf(err, "failed to parse value for event %q", event)
		}
		return v, nil
	}
	if err := s.Err(); err != nil {
		return 0, errors.Wrap(err, "scanner error reading memory.events")
	}
	return 0, nil
}
