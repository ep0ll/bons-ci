// Package dirty provides real-time dirty-page tracking for the CI/CD build
// cgroup, enabling adaptive pre-dump convergence.
//
// Instead of running a fixed number of pre-dump rounds, we stop when the
// dirty-page ratio drops below a threshold (e.g. <5% of total pages dirtied
// per interval).  This prevents wasted rounds on processes that have already
// slowed their memory write rate.
//
// Mechanism:
//
//	We use the kernel's soft-dirty PTE tracking:
//	1. Reset soft-dirty bits:  echo 4 > /proc/<pid>/clear_refs
//	2. After a sleep interval, count dirty pages by reading /proc/<pid>/pagemap
//	   and checking the soft-dirty bit (bit 55).
//	This is exactly what CRIU's own pre-dump tracking uses internally.
package dirty

import (
	"encoding/binary"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strconv"
	"strings"
)

const (
	// softDirtyBit is bit 55 in a pagemap entry.
	softDirtyBit = uint64(1) << 55
	// presentBit indicates the page is in RAM (not swapped).
	presentBit = uint64(1) << 63
	// pagemapEntrySize is always 8 bytes.
	pagemapEntrySize = 8
)

// Tracker measures dirty page rate for a set of PIDs.
type Tracker struct {
	pids []int
}

// New constructs a Tracker for the given PIDs.
func New(pids []int) *Tracker {
	return &Tracker{pids: pids}
}

// PageStats holds dirty page measurement results.
type PageStats struct {
	TotalPages int64
	DirtyPages int64
	// DirtyRatio is DirtyPages/TotalPages (0.0–1.0).
	DirtyRatio float64
}

// Reset clears soft-dirty bits for all tracked PIDs.
// Must be called at the START of each measurement interval.
func (t *Tracker) Reset() error {
	for _, pid := range t.pids {
		path := fmt.Sprintf("/proc/%d/clear_refs", pid)
		// Write "4" = clear soft-dirty bits only (not referenced bits).
		if err := os.WriteFile(path, []byte("4"), 0o200); err != nil {
			// PID may have exited — not fatal.
			if os.IsNotExist(err) {
				continue
			}
			return fmt.Errorf("clear_refs for pid %d: %w", pid, err)
		}
	}
	return nil
}

// Measure counts dirty pages across all tracked PIDs since the last Reset.
func (t *Tracker) Measure() (*PageStats, error) {
	var total, dirty int64

	for _, pid := range t.pids {
		t, d, err := measurePID(pid)
		if err != nil {
			if os.IsNotExist(err) {
				continue // PID exited
			}
			return nil, err
		}
		total += t
		dirty += d
	}

	ratio := 0.0
	if total > 0 {
		ratio = float64(dirty) / float64(total)
	}
	return &PageStats{
		TotalPages: total,
		DirtyPages: dirty,
		DirtyRatio: ratio,
	}, nil
}

// HasConverged returns true if the dirty ratio is below threshold,
// meaning an additional pre-dump round would capture very little new data.
func (s *PageStats) HasConverged(threshold float64) bool {
	return s.DirtyRatio < threshold
}

// measurePID counts total and soft-dirty pages for one PID.
func measurePID(pid int) (total, dirty int64, err error) {
	// Read memory maps to get all VMA ranges.
	maps, err := readMaps(pid)
	if err != nil {
		return 0, 0, err
	}

	// Open pagemap.
	pmPath := fmt.Sprintf("/proc/%d/pagemap", pid)
	pmFile, err := os.Open(pmPath)
	if err != nil {
		return 0, 0, err
	}
	defer pmFile.Close()

	pageSize := int64(os.Getpagesize())

	for _, m := range maps {
		// Number of pages in this VMA.
		pages := (m.end - m.start) / uint64(pageSize)
		// Seek to the pagemap offset for this VMA.
		offset := int64(m.start/uint64(pageSize)) * pagemapEntrySize
		if _, err := pmFile.Seek(offset, io.SeekStart); err != nil {
			continue
		}

		buf := make([]byte, pages*pagemapEntrySize)
		n, err := io.ReadFull(pmFile, buf)
		if err != nil && n == 0 {
			continue
		}

		for i := 0; i < n; i += pagemapEntrySize {
			entry := binary.LittleEndian.Uint64(buf[i:])
			if entry&presentBit != 0 {
				total++
				if entry&softDirtyBit != 0 {
					dirty++
				}
			}
		}
	}
	return total, dirty, nil
}

type vma struct{ start, end uint64 }

func readMaps(pid int) ([]vma, error) {
	data, err := os.ReadFile(fmt.Sprintf("/proc/%d/maps", pid))
	if err != nil {
		return nil, err
	}

	var vmas []vma
	for _, line := range strings.Split(string(data), "\n") {
		if line == "" {
			continue
		}
		parts := strings.Fields(line)
		if len(parts) == 0 {
			continue
		}
		addrs := strings.Split(parts[0], "-")
		if len(addrs) != 2 {
			continue
		}
		start, err1 := strconv.ParseUint(addrs[0], 16, 64)
		end, err2 := strconv.ParseUint(addrs[1], 16, 64)
		if err1 != nil || err2 != nil {
			continue
		}
		// Skip non-writable VMAs — they can't be dirty.
		if len(parts) > 1 && !strings.Contains(parts[1], "w") {
			continue
		}
		vmas = append(vmas, vma{start, end})
	}
	return vmas, nil
}

// CgroupDirtyStats aggregates dirty stats across all PIDs in a cgroup.
func CgroupDirtyStats(cgroupPath string) (*PageStats, error) {
	pids, err := listCgroupPIDs(cgroupPath)
	if err != nil {
		return nil, err
	}
	t := New(pids)
	return t.Measure()
}

func listCgroupPIDs(cgroupPath string) ([]int, error) {
	procsFile := filepath.Join("/sys/fs/cgroup", cgroupPath, "cgroup.procs")
	if _, err := os.Stat(procsFile); os.IsNotExist(err) {
		procsFile = filepath.Join("/sys/fs/cgroup/freezer", cgroupPath, "cgroup.procs")
	}
	data, err := os.ReadFile(procsFile)
	if err != nil {
		return nil, fmt.Errorf("reading %s: %w", procsFile, err)
	}
	var pids []int
	for _, line := range strings.Split(strings.TrimSpace(string(data)), "\n") {
		if line == "" {
			continue
		}
		pid, err := strconv.Atoi(line)
		if err == nil {
			pids = append(pids, pid)
		}
	}
	return pids, nil
}
