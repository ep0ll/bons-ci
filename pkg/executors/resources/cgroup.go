package resources

// cgroup.go – cgroupv2 lifecycle and utility functions.
//
// Responsibilities:
//   - Detecting whether the system uses cgroupv2 (unified hierarchy).
//   - One-time controller setup for the BuildKit cgroup root.
//   - Shared file I/O helpers with a sentinel error type so callers can
//     distinguish "file not found" from "real I/O error" without importing
//     os.ErrNotExist everywhere.
//   - Path helpers for the cgroup virtual filesystem.
//
// cgroupv2 background
//
// In cgroupv2 (unified hierarchy), all controllers are mounted under a single
// tree at /sys/fs/cgroup.  A process is placed in exactly one cgroup.  To
// enable a controller for a group, its name must appear in the parent's
// cgroup.subtree_control file (e.g. "+cpu +memory +io +pids").
//
// BuildKit needs the following sequence on first run when
// BUILDKIT_SETUP_CGROUPV2_ROOT=1:
//   1. Move the current process into a leaf "init" cgroup so the root cgroup
//      is not a leaf (the kernel forbids enabling controllers on a non-leaf
//      that has processes directly in it).
//   2. Enable all available controllers in the root cgroup's subtree_control.
//
// See also: https://docs.kernel.org/admin-guide/cgroup-v2.html

import (
	"bufio"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"sync"

	"github.com/moby/buildkit/util/bklog"
)

// ─── Constants ────────────────────────────────────────────────────────────────

const (
	// defaultMountpoint is the standard cgroupv2 unified hierarchy mountpoint.
	defaultMountpoint = "/sys/fs/cgroup"

	// initGroup is the name of the leaf cgroup that the BuildKit daemon process
	// is moved into so the root cgroup's subtree_control can be written.
	initGroupName = "init"

	// cgroupv2 virtual files used for controller management.
	cgroupProcsFile       = "cgroup.procs"
	cgroupControllersFile = "cgroup.controllers"
	cgroupSubtreeFile     = "cgroup.subtree_control"
)

// ─── cgroupv2 detection ───────────────────────────────────────────────────────

var (
	cgroupV2Once sync.Once
	cgroupV2     bool
)

// IsCgroupV2 returns true when the system uses the cgroupv2 unified hierarchy.
// The result is computed once and cached; subsequent calls are lock-free reads.
func IsCgroupV2() bool {
	cgroupV2Once.Do(func() {
		cgroupV2 = isCgroup2()
	})
	return cgroupV2
}

// ─── Controller setup ─────────────────────────────────────────────────────────

// PrepareCgroupControllers moves the calling process to the "init" leaf cgroup
// and enables all available controllers in the cgroupv2 root subtree_control.
//
// This is a one-time setup step required when BuildKit is bootstrapped inside
// a cgroup that has no controllers enabled.  It is triggered by setting the
// BUILDKIT_SETUP_CGROUPV2_ROOT=1 environment variable.
//
// No-op when BUILDKIT_SETUP_CGROUPV2_ROOT is unset or false.
func PrepareCgroupControllers() error {
	return prepareCgroupControllers()
}

// prepareCgroupControllers is the internal implementation, shared with the
// existing NewMonitor bootstrap path.
func prepareCgroupControllers() error {
	v, ok := os.LookupEnv("BUILDKIT_SETUP_CGROUPV2_ROOT")
	if !ok || !parseBool(v) {
		return nil
	}

	// Step 1: create the "init" leaf and migrate the current process into it.
	initPath := filepath.Join(defaultMountpoint, initGroupName)
	if err := os.MkdirAll(initPath, 0o755); err != nil {
		return fmt.Errorf("create init cgroup %q: %w", initPath, err)
	}
	if err := moveSelfToInitCgroup(initPath); err != nil {
		return fmt.Errorf("move process to init cgroup: %w", err)
	}

	// Step 2: read available controllers from root's cgroup.controllers.
	controllers, err := readCgroupControllers(defaultMountpoint)
	if err != nil {
		return fmt.Errorf("read cgroup controllers: %w", err)
	}

	// Step 3: enable each controller in the root subtree_control.
	subtreePath := filepath.Join(defaultMountpoint, cgroupSubtreeFile)
	for _, c := range controllers {
		if c == "" {
			continue
		}
		if err := os.WriteFile(subtreePath, []byte("+"+c), 0); err != nil {
			// Non-fatal: some controllers may already be enabled or restricted.
			bklog.L.Warnf("resources: failed to enable cgroup controller %q: %v", c, err)
		}
	}
	return nil
}

// moveSelfToInitCgroup migrates all processes currently in the cgroupv2 root
// cgroup into initPath.
func moveSelfToInitCgroup(initPath string) error {
	rootProcs := filepath.Join(defaultMountpoint, cgroupProcsFile)
	f, err := os.Open(rootProcs)
	if err != nil {
		return fmt.Errorf("open %q: %w", rootProcs, err)
	}
	defer f.Close()

	initProcs := filepath.Join(initPath, cgroupProcsFile)
	scanner := bufio.NewScanner(f)
	for scanner.Scan() {
		pid := scanner.Bytes()
		if len(pid) == 0 {
			continue
		}
		if err := os.WriteFile(initProcs, pid, 0); err != nil {
			return fmt.Errorf("write pid %q to %q: %w", pid, initProcs, err)
		}
	}
	return scanner.Err()
}

// readCgroupControllers returns the space-separated controller names from
// the cgroup.controllers file at cgroupPath.
func readCgroupControllers(cgroupPath string) ([]string, error) {
	data, err := os.ReadFile(filepath.Join(cgroupPath, cgroupControllersFile))
	if err != nil {
		return nil, err
	}
	return strings.Fields(string(data)), nil
}

// ─── Low-level file I/O ───────────────────────────────────────────────────────

// errNotExist is a package-private sentinel that readFile uses to signal
// a missing file without forcing callers to import os.
var errNotExist = os.ErrNotExist

// readFile reads path and returns its content.
// Returns (nil, errNotExist) when the file does not exist so callers can
// use errors.Is(err, errNotExist) without importing the os package.
func readFile(path string) ([]byte, error) {
	data, err := os.ReadFile(path)
	if os.IsNotExist(err) {
		return nil, errNotExist
	}
	if err != nil {
		return nil, fmt.Errorf("read %q: %w", path, err)
	}
	return data, nil
}

// ─── Misc helpers ─────────────────────────────────────────────────────────────

// parseBool returns true for "1", "t", "T", "true", "TRUE", "True".
func parseBool(s string) bool {
	switch strings.ToLower(strings.TrimSpace(s)) {
	case "1", "t", "true":
		return true
	}
	return false
}
