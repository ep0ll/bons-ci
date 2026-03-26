package oci

// Design notes
// ============
//
// The /etc/hosts file has two distinct life-cycles depending on whether the
// caller requested custom extra-hosts or a non-default hostname:
//
//  Shared (standard) file
//    Path: {stateDir}/hosts
//    Life: created once, persists for the lifetime of the executor.
//    Cleanup: no-op; the executor deletes stateDir when it shuts down.
//    Singleflight: yes — concurrent container starts share the write.
//
//  Per-container (custom) file
//    Path: {stateDir}/hosts.{randomID}
//    Life: created before the container starts, deleted after it exits.
//    Cleanup: caller must invoke the returned func() when the container exits.
//    Singleflight: no — each container needs its own copy.
//
// Original bug: makeHostsFile returned ("", func(){}, nil) when the file
// already existed.  The shared path silently compensated by hardcoding the
// expected path after the singleflight.  The per-container path was safe only
// because identity.NewID() makes every path unique.
//
// This rewrite makes makeHostsFile always return a non-empty path on success,
// removing the implicit contract between the two call sites.

import (
	"bytes"
	"context"
	"fmt"
	"os"
	"path/filepath"

	"github.com/bons/bons-ci/pkg/executors"
	"github.com/moby/buildkit/identity"
	"github.com/moby/buildkit/util/flightcontrol"
	"github.com/moby/sys/user"
	"github.com/pkg/errors"
)

const defaultHostname = "buildkitsandbox"

// HostsManager manages the generation and caching of /etc/hosts files for
// container sandbox environments.
//
// It deduplicates concurrent writes for the shared (no extra-hosts,
// default-hostname) case via an internal singleflight group keyed on the
// output path.  Custom (extra-hosts or non-default hostname) files are always
// written fresh with a unique name so concurrent containers don't collide.
//
// Each executor instance should hold one HostsManager.
type HostsManager struct {
	// group is keyed on output path; its return value is the written path,
	// propagated to all coalesced callers so the path is not reconstructed
	// by hand after the group call.
	group flightcontrol.Group[string]
}

// NewHostsManager returns a HostsManager ready to use.
func NewHostsManager() *HostsManager {
	return &HostsManager{}
}

// defaultHostsManager is the package-level singleton used by GetHostsFile.
var defaultHostsManager = &HostsManager{}

// GetHostsFile is the package-level entry point retained for backward
// compatibility.  New code should call (*HostsManager).Get directly.
func GetHostsFile(
	ctx context.Context,
	stateDir string,
	extraHosts []executor.HostIP,
	idmap *user.IdentityMapping,
	hostname string,
) (string, func(), error) {
	return defaultHostsManager.Get(ctx, stateDir, extraHosts, idmap, hostname)
}

// Get returns the path to an /etc/hosts file suitable for the container
// described by the arguments.
//
// For the common case (no extra hosts, default hostname) the file is shared
// across all containers in the executor and the cleanup is a no-op.
//
// For containers that require extra hosts or a custom hostname, an ephemeral
// per-container file is created.  The caller must invoke cleanup() after the
// container exits to remove the file.
func (m *HostsManager) Get(
	ctx context.Context,
	stateDir string,
	extraHosts []executor.HostIP,
	idmap *user.IdentityMapping,
	hostname string,
) (path string, cleanup func(), err error) {
	if len(extraHosts) != 0 || hostname != defaultHostname {
		return makeHostsFile(stateDir, extraHosts, idmap, hostname)
	}
	return m.getShared(ctx, stateDir, idmap, hostname)
}

// getShared returns the shared (non-ephemeral) hosts file for stateDir,
// generating it on first call and coalescing concurrent writers.
func (m *HostsManager) getShared(
	ctx context.Context,
	stateDir string,
	idmap *user.IdentityMapping,
	hostname string,
) (string, func(), error) {
	sharedPath := filepath.Join(stateDir, "hosts")

	// Key on the shared path so that all concurrent calls for the same
	// executor coalesce to a single write.  The winning goroutine's returned
	// path is propagated to all waiters.
	p, err := m.group.Do(ctx, sharedPath, func(_ context.Context) (string, error) {
		p, _, err := makeHostsFile(stateDir, nil, idmap, hostname)
		return p, err
	})
	if err != nil {
		return "", nil, err
	}
	// The shared file persists for the lifetime of the executor; no cleanup.
	return p, func() {}, nil
}

// makeHostsFile writes an /etc/hosts-style file into stateDir and returns its
// path.  If the file already exists it is returned without modification
// (idempotent for the shared case).
//
// Per-container files get a unique suffix so concurrent container starts using
// different extra-host sets don't clobber each other.
func makeHostsFile(
	stateDir string,
	extraHosts []executor.HostIP,
	idmap *user.IdentityMapping,
	hostname string,
) (string, func(), error) {
	p := filepath.Join(stateDir, "hosts")
	if len(extraHosts) != 0 || hostname != defaultHostname {
		p += "." + identity.NewID()
	}

	if _, statErr := os.Stat(p); statErr == nil {
		// File already exists (e.g. second concurrent writer for shared file).
		// Return the path so callers don't have to reconstruct it.
		return p, func() {}, nil
	} else if !errors.Is(statErr, os.ErrNotExist) {
		return "", nil, errors.WithStack(statErr)
	}

	content := buildHostsContent(hostname, extraHosts)

	if err := atomicWriteFile(p, content, 0o644, idmap); err != nil {
		return "", nil, err
	}

	return p, func() { _ = os.RemoveAll(p) }, nil
}

// buildHostsContent assembles the byte content for an /etc/hosts file.
//
// The format follows RFC 952 / RFC 1123:
//
//	<IP>  <FQDN-or-hostname> [<alias>...]
func buildHostsContent(hostname string, extraHosts []executor.HostIP) []byte {
	if hostname == "" {
		hostname = defaultHostname
	}

	var buf bytes.Buffer

	// Standard loopback entries.  The IPv6 aliases are always written because
	// the OCI runtime configures a loopback interface for every container.
	fmt.Fprintf(&buf, "127.0.0.1\tlocalhost %s\n", hostname)
	fmt.Fprintf(&buf, "::1\tlocalhost ip6-localhost ip6-loopback\n")

	// Caller-supplied extra host->IP mappings (e.g. --add-host in docker build).
	for _, h := range extraHosts {
		fmt.Fprintf(&buf, "%s\t%s\n", h.IP.String(), h.Host)
	}

	return buf.Bytes()
}
