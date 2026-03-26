//go:build !linux && !windows

package cniprovider

import (
	specs "github.com/opencontainers/runtime-spec/specs-go"
	"github.com/pkg/errors"
)

func (c *cniProvider) nsDir() string {
	return ""
}

func cleanOldNamespaces(_ *cniProvider) {}

func createNetNS(_ *cniProvider, _ string) (string, error) {
	return "", errors.New("cniprovider: network namespace creation is not supported on this platform")
}

func setNetNS(_ *specs.Spec, _ string) error {
	return errors.New("cniprovider: network namespace is not supported on this platform")
}

func unmountNetNS(_ string) error {
	return errors.New("cniprovider: network namespace unmount is not supported on this platform")
}

func deleteNetNS(_ string) error {
	return errors.New("cniprovider: network namespace deletion is not supported on this platform")
}
