//go:build linux

package runcexecutor

import (
	"os"
	"os/exec"
	"path/filepath"

	oci "github.com/bons/bons-ci/pkg/executors/spec"
	"github.com/bons/bons-ci/pkg/executors/resources"
	"github.com/moby/buildkit/solver/llbsolver/cdidevices"
	"github.com/moby/sys/user"
	"github.com/pkg/errors"
)

// ─── defaults ────────────────────────────────────────────────────────────────

var defaultCommandCandidates = []string{"buildkit-runc", "runc"}

// staleStateFiles are leftover files from a previous run that must be removed
// on startup so fresh copies are written with correct content.
var staleStateFiles = []string{"hosts", "resolv.conf"}

// ─── Config ──────────────────────────────────────────────────────────────────

// Config is the set of parameters used to initialise a runc Executor.
//
// All fields are plain assignments — no functional-options wrapper —
// because the caller (typically a BuildKit worker) already constructs the
// equivalent of this struct from its own option types, and an extra layer
// of indirection would add noise without benefit.
//
// Zero values are safe: the executor will use the compiled-in defaults
// (e.g. defaultCommandCandidates, no cgroup parent, no AppArmor, etc.).
type Config struct {
	// Root is the working directory where the executor stores bundle dirs,
	// the runc log, and shared state files (resolv.conf, hosts).
	// Must be an absolute path; it is created if it does not exist.
	Root string

	// CommandCandidates is the ordered list of runc binary names to search
	// for on PATH.  Defaults to ["buildkit-runc", "runc"].
	CommandCandidates []string

	// Rootless enables rootless operation (unprivileged user namespaces).
	// This has nothing to do with the Root directory.
	Rootless bool

	// DefaultCgroupParent is the cgroup parent path for all containers
	// created by this executor.  Per-container overrides take precedence.
	DefaultCgroupParent string

	// ProcessMode controls PID namespace handling (sandbox vs host).
	ProcessMode oci.ProcessMode

	// IdentityMapping enables UID/GID remapping via user namespaces.
	IdentityMapping *user.IdentityMapping

	// NoPivot disables pivot_root (uses MS_MOVE instead).
	// Unrecommended; provided only for environments where pivot_root fails.
	NoPivot bool

	// DNS overrides the DNS configuration written into containers.
	DNS *oci.DNSConfig

	// OOMScoreAdj sets the Linux OOM score adjustment for container init
	// processes.  nil means "inherit from the daemon".
	OOMScoreAdj *int

	// ApparmorProfile is the AppArmor profile name applied to each container.
	// Empty string means no profile is applied.
	ApparmorProfile string

	// SELinux enables SELinux label assignment for containers.
	SELinux bool

	// TracingSocket is the path to an OpenTelemetry gRPC socket that should
	// be mounted into each container for distributed tracing.
	TracingSocket string

	// ResourceMonitor collects cgroup v2 resource samples during container
	// execution.  nil disables resource monitoring.
	ResourceMonitor *resources.Monitor

	// CDIManager handles Container Device Interface device injection.
	CDIManager *cdidevices.Manager
}

// ─── resolution helpers ───────────────────────────────────────────────────────

// resolveRuncBinary searches PATH for the first available binary from
// cfg.CommandCandidates and returns its absolute path.
// Returns ErrRuncBinaryNotFound when no candidate is found.
func (cfg *Config) resolveRuncBinary() (string, error) {
	candidates := cfg.CommandCandidates
	if len(candidates) == 0 {
		candidates = defaultCommandCandidates
	}
	for _, name := range candidates {
		if path, err := exec.LookPath(name); err == nil {
			return path, nil
		}
	}
	return "", errors.Wrapf(ErrRuncBinaryNotFound, "searched %v", candidates)
}

// prepareRoot ensures the root directory exists, resolves symlinks to a
// canonical absolute path, and removes stale per-run state files.
// Returns the canonical root path.
func prepareRoot(root string) (string, error) {
	if err := os.MkdirAll(root, 0o711); err != nil {
		return "", errors.Wrapf(err, "failed to create executor root %s", root)
	}

	abs, err := filepath.Abs(root)
	if err != nil {
		return "", errors.Wrap(err, "failed to resolve absolute path for executor root")
	}

	canonical, err := filepath.EvalSymlinks(abs)
	if err != nil {
		return "", errors.Wrap(err, "failed to eval symlinks for executor root")
	}

	for _, f := range staleStateFiles {
		// Ignore errors — the file simply may not exist yet.
		_ = os.RemoveAll(filepath.Join(canonical, f))
	}

	return canonical, nil
}
