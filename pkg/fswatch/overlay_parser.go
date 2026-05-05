//go:build !linux

package fswatch

import (
	"bufio"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strings"
)

// OverlayInfoFromMount is not available on non-Linux platforms.
// It always returns [ErrNotSupported].
func OverlayInfoFromMount(_ string) (*OverlayInfo, error) {
	return nil, ErrNotSupported
}

// overlayInfoFromMountFile parses a mountinfo file using the built-in pure-Go
// parser on non-Linux platforms (where moby/sys/mountinfo is unavailable).
// This enables tests to run on macOS and Windows with synthetic mountinfo data.
func overlayInfoFromMountFile(mountinfoPath, mergedDir string) (*OverlayInfo, error) {
	mergedDir = filepath.Clean(mergedDir)

	f, err := os.Open(mountinfoPath)
	if err != nil {
		return nil, fmt.Errorf("overlay: open %s: %w", mountinfoPath, err)
	}
	defer f.Close()

	return parseMountinfoReader(f, mergedDir)
}

// parseMountinfoReader scans r line-by-line and returns the OverlayInfo for
// the first overlay mount whose Mountpoint matches mergedDir.
// This pure-Go parser handles the standard /proc/self/mountinfo format.
func parseMountinfoReader(r io.Reader, mergedDir string) (*OverlayInfo, error) {
	scanner := bufio.NewScanner(r)
	for scanner.Scan() {
		line := scanner.Text()
		if line == "" {
			continue
		}
		info, ok := parseMountinfoLine(line)
		if !ok {
			continue
		}
		if filepath.Clean(info.mountpoint) != mergedDir {
			continue
		}
		return overlayInfoFromVFSOptions(info.vfsOptions, mergedDir)
	}
	if err := scanner.Err(); err != nil {
		return nil, fmt.Errorf("overlay: scan mountinfo: %w", err)
	}
	return nil, fmt.Errorf("overlay: %w: %q", ErrMountNotFound, mergedDir)
}

// mountinfoEntry holds the fields we need from one parsed mountinfo line.
type mountinfoEntry struct {
	mountpoint string
	fsType     string
	vfsOptions string // superblock options (column after the " - " separator)
}

// parseMountinfoLine parses one line of /proc/self/mountinfo.
// Returns (entry, true) for overlay mounts, (zero, false) for anything else.
//
// mountinfo line format:
//
//	mountID parentID major:minor root mountPoint mountOptions [optFields] - fsType source superOptions
func parseMountinfoLine(line string) (mountinfoEntry, bool) {
	// Split on " - " to separate the variable-length optional fields section.
	halves := strings.SplitN(line, " - ", 2)
	if len(halves) != 2 {
		return mountinfoEntry{}, false
	}

	left := strings.Fields(halves[0])
	right := strings.Fields(halves[1])

	// left:  [mountID parentID major:minor root mountPoint mountOptions ...optFields]
	// right: [fsType source superOptions]
	if len(left) < 6 || len(right) < 3 {
		return mountinfoEntry{}, false
	}
	if right[0] != "overlay" {
		return mountinfoEntry{}, false
	}

	return mountinfoEntry{
		mountpoint: unescapeMountinfoPath(left[4]),
		fsType:     right[0],
		vfsOptions: right[2],
	}, true
}

// unescapeMountinfoPath converts octal-escaped characters in a mountinfo path
// back to their ASCII values. The kernel escapes spaces, tabs, newlines and
// backslashes as \040, \011, \012, and \134 respectively.
func unescapeMountinfoPath(s string) string {
	if !strings.ContainsRune(s, '\\') {
		return s
	}
	var b strings.Builder
	for i := 0; i < len(s); {
		if s[i] != '\\' || i+4 > len(s) {
			b.WriteByte(s[i])
			i++
			continue
		}
		// Parse a 3-digit octal sequence: \NNN
		var v byte
		valid := true
		for j := 1; j <= 3; j++ {
			c := s[i+j]
			if c < '0' || c > '7' {
				valid = false
				break
			}
			v = (v << 3) | (c - '0')
		}
		if !valid {
			b.WriteByte(s[i])
			i++
			continue
		}
		b.WriteByte(v)
		i += 4
	}
	return b.String()
}
