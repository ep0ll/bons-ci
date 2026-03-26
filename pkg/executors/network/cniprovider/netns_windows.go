//go:build windows

package cniprovider

import (
	"os"
	"path/filepath"

	"github.com/Microsoft/hcsshim/hcn"
	"github.com/moby/buildkit/util/bklog"
	specs "github.com/opencontainers/runtime-spec/specs-go"
	"github.com/pkg/errors"
)

func (c *cniProvider) nsDir() string {
	return filepath.Join(c.root, "net", "cni")
}

// cleanOldNamespaces is not yet implemented on Windows.  HCN namespaces
// created by a previous daemon instance are not accessible via a filesystem
// directory listing, so the Linux approach of iterating nsDir() does not
// apply.  A future implementation could query hcn.ListNamespaces() to find
// leaked namespaces by tag/name prefix.
func cleanOldNamespaces(_ *cniProvider) {
	bklog.L.Debug("cniprovider: stale namespace cleanup not implemented on Windows")
}

// createNetNS creates an HCN (Host Compute Network) guest namespace and
// returns its GUID as the nativeID.
//
// On Windows, network namespaces are managed by the HCS (Host Compute Service)
// rather than by kernel netns files.  The GUID returned here is passed back to
// setNetNS to be written into the OCI spec's Windows.Network.NetworkNamespace.
func createNetNS(_ *cniProvider, _ string) (string, error) {
	tmpl := hcn.NewNamespace(hcn.NamespaceTypeGuest)
	ns, err := tmpl.Create()
	if err != nil {
		return "", errors.Wrapf(err, "cniprovider: HCN namespace create (template id=%s)", tmpl.Id)
	}
	return ns.Id, nil
}

// setNetNS writes the HCN namespace GUID into the OCI spec so the Windows
// container runtime can join the pre-created namespace.
//
// Note: containerd does not yet provide a helper for this; the implementation
// mirrors the pattern from the runtime-tools generator:
// https://github.com/opencontainers/runtime-tools/blob/07406c5/generate/generate.go#L1810
func setNetNS(s *specs.Spec, nativeID string) error {
	if s.Windows == nil {
		s.Windows = &specs.Windows{}
	}
	if s.Windows.Network == nil {
		s.Windows.Network = &specs.WindowsNetwork{}
	}
	s.Windows.Network.NetworkNamespace = nativeID
	return nil
}

// unmountNetNS is a no-op on Windows.  HCN namespaces are not bind-mounted.
func unmountNetNS(_ string) error {
	return nil
}

// deleteNetNS deletes the HCN namespace identified by nativeID (a GUID).
func deleteNetNS(nativeID string) error {
	ns, err := hcn.GetNamespaceByID(nativeID)
	if err != nil {
		// HCN returns an error when the namespace does not exist; treat that
		// as success so deleteNetNS is idempotent.
		return errors.Wrapf(err, "cniprovider: get HCN namespace %s", nativeID)
	}
	return ns.Delete()
}

// ─── Unused on Windows ────────────────────────────────────────────────────────

// The following symbols exist only to satisfy references in shared code that
// is compiled on all platforms.  They are never called on Windows.
var _ = os.DevNull // suppress "os imported and not used"
