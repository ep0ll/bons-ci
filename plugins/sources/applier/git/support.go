package gitapply

import (
	"context"
	"fmt"
	"os/exec"
	"strings"
)

// Supported returns nil if the system satisfies the minimum requirements for
// this package to operate:
//
//   - The git binary is present on PATH.
//   - The git version is at least 2.18 (first version with protocol v2 and
//     reliable --depth=1 tag dereferencing).
//
// Call this once at startup to surface configuration problems early rather
// than discovering them during the first Fetch.
func Supported() error {
	path, err := exec.LookPath("git")
	if err != nil {
		return fmt.Errorf("%w: %v", ErrGitNotFound, err)
	}

	versionOutput, err := exec.Command(path, "version").Output()
	if err != nil {
		return fmt.Errorf("gitapply: git version check failed: %w", err)
	}

	version, ok := parseGitVersion(string(versionOutput))
	if !ok {
		// Non-fatal: we got a git binary but could not parse its version string.
		// Allow the call to succeed; the version warning will appear in the log.
		return nil
	}

	if !meetsMinimumVersion(version, minimumGitVersion) {
		return fmt.Errorf(
			"gitapply: git version %s is below the minimum required %s",
			version, minimumGitVersion,
		)
	}
	return nil
}

// minimumGitVersion is the oldest git release that supports all features used
// by this package: protocol v2 (-c protocol.version=2), reliable shallow
// handling, and annotated-tag dereferencing with --depth=1.
const minimumGitVersion = "2.18"

// gitVersion is a comparable [major, minor] tuple.
type gitVersion [2]int

// parseGitVersion parses "git version 2.43.0" → ("2.43", true).
// Returns ("", false) when the format is unrecognised.
func parseGitVersion(output string) (string, bool) {
	// Expected format: "git version X.Y.Z\n"
	output = strings.TrimSpace(output)
	const prefix = "git version "
	if !strings.HasPrefix(output, prefix) {
		return "", false
	}
	versionStr := strings.TrimPrefix(output, prefix)
	// Strip any trailing OS-specific suffix ("git version 2.43.0 (Apple Git-...)")
	if idx := strings.IndexByte(versionStr, ' '); idx != -1 {
		versionStr = versionStr[:idx]
	}
	return versionStr, true
}

// meetsMinimumVersion returns true when actual is ≥ minimum.
// Both strings must be in "X.Y" or "X.Y.Z" format.
func meetsMinimumVersion(actual, minimum string) bool {
	av := splitVersion(actual)
	mv := splitVersion(minimum)
	if av[0] != mv[0] {
		return av[0] > mv[0]
	}
	return av[1] >= mv[1]
}

func splitVersion(v string) gitVersion {
	var maj, min int
	parts := strings.SplitN(v, ".", 3)
	if len(parts) >= 1 {
		fmt.Sscanf(parts[0], "%d", &maj)
	}
	if len(parts) >= 2 {
		fmt.Sscanf(parts[1], "%d", &min)
	}
	return gitVersion{maj, min}
}

// Probe runs a lightweight end-to-end sanity check against a known-good
// public repository.  It is intended for integration health checks, not for
// regular use.  The probe performs only "git ls-remote" (no checkout) so it
// does not write any files and completes quickly.
//
// Pass a context with an appropriate deadline (e.g. 10 s) to prevent the
// probe from blocking indefinitely on network issues.
func Probe(ctx context.Context) error {
	if err := Supported(); err != nil {
		return err
	}
	// Use the git CLI directly for the probe rather than going through
	// DefaultFetcher so that the probe does not need a temp directory or auth.
	cli := newGitCLI(DefaultProcessRunner)
	if _, err := cli.run(ctx, "ls-remote", "--exit-code", "--quiet",
		"https://github.com/git/git.git", "HEAD",
	); err != nil {
		return fmt.Errorf("gitapply: probe ls-remote failed: %w", err)
	}
	return nil
}
