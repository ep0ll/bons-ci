package cniprovider

import (
	"os"

	"github.com/pkg/errors"
)

// Opt holds the configuration for both CNI-file-based and bridge-based network
// providers.  All fields that are not relevant to a particular constructor are
// ignored (e.g. BridgeName / BridgeSubnet are unused by New).
type Opt struct {
	// Root is the BuildKit state directory.  Network namespaces are stored
	// under Root/net/cni/<id>.
	Root string

	// ConfigPath is the path to a CNI config file (.conf) or config-list
	// (.conflist).  Required by New; unused by NewBridge.
	ConfigPath string

	// BinaryDir is the directory containing CNI plugin executables.
	// Required by both New and NewBridge as the fallback binary location.
	BinaryDir string

	// PoolSize is the target number of pre-created network namespaces kept
	// warm in the pool.  0 disables pre-creation (namespaces are created on
	// demand).
	PoolSize int

	// BridgeName is the name of the Linux bridge interface managed by
	// NewBridge (e.g. "buildkit0").  Unused by New.
	BridgeName string

	// BridgeSubnet is the IPv4 CIDR subnet assigned to the bridge
	// (e.g. "10.0.0.0/22").  Unused by New.
	BridgeSubnet string
}

// Validate checks that the fields required for New (CNI file mode) are present
// and accessible.
//
// NewBridge performs its own additional validation because its requirements
// differ from New's.
func (o *Opt) Validate() error {
	if o.Root == "" {
		return errors.New("cniprovider: Root must not be empty")
	}
	if o.ConfigPath == "" {
		return errors.New("cniprovider: ConfigPath must not be empty")
	}
	if o.BinaryDir == "" {
		return errors.New("cniprovider: BinaryDir must not be empty")
	}
	if _, err := os.Stat(o.ConfigPath); err != nil {
		return errors.Wrapf(err, "cniprovider: cannot access CNI config %q", o.ConfigPath)
	}
	if _, err := os.Stat(o.BinaryDir); err != nil {
		return errors.Wrapf(err, "cniprovider: cannot access CNI binary dir %q", o.BinaryDir)
	}
	return nil
}

// ValidateBridge checks that the fields required for NewBridge are present.
func (o *Opt) ValidateBridge() error {
	if o.Root == "" {
		return errors.New("cniprovider: Root must not be empty")
	}
	if o.BridgeName == "" {
		return errors.New("cniprovider: BridgeName must not be empty")
	}
	if o.BridgeSubnet == "" {
		return errors.New("cniprovider: BridgeSubnet must not be empty")
	}
	return nil
}
