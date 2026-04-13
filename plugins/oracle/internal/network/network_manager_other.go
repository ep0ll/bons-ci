//go:build !linux

// network_manager_other.go: portable network capture for non-Linux platforms.
// Used during local development on macOS / Windows.
// Production deployments run on OCI Linux instances where
// network_manager_linux.go provides the full netlink implementation.
package network

import (
	"encoding/json"
	"fmt"
	"net"
	"os"

	"go.uber.org/zap"
)

// capturePlatform captures basic interface/address information using the
// portable stdlib net package. iptables, advanced routing, and sysctl
// capture are Linux-only and are omitted here.
func (m *Manager) capturePlatform(snapshotPath string) (*Snapshot, error) {
	m.log.Info("capturing network state (portable mode)", zap.String("path", snapshotPath))

	snap := &Snapshot{
		Sysctls: make(map[string]string),
	}

	ifaces, err := net.Interfaces()
	if err != nil {
		return nil, fmt.Errorf("listing interfaces: %w", err)
	}

	for _, iface := range ifaces {
		// Skip loopback — always present on any successor.
		if iface.Flags&net.FlagLoopback != 0 {
			continue
		}
		state := InterfaceState{
			Name:   iface.Name,
			HWAddr: iface.HardwareAddr.String(),
			MTU:    iface.MTU,
			Flags:  uint32(iface.Flags),
		}
		addrs, err := iface.Addrs()
		if err == nil {
			for _, addr := range addrs {
				state.Addrs = append(state.Addrs, addr.String())
			}
		}
		snap.Interfaces = append(snap.Interfaces, state)
	}

	data, err := json.MarshalIndent(snap, "", "  ")
	if err != nil {
		return nil, fmt.Errorf("marshalling network snapshot: %w", err)
	}
	if err := os.WriteFile(snapshotPath, data, 0o600); err != nil {
		return nil, fmt.Errorf("writing network snapshot: %w", err)
	}

	m.log.Info("network state captured",
		zap.Int("interfaces", len(snap.Interfaces)),
		zap.Int("routes", len(snap.Routes)),
	)
	return snap, nil
}

// restorePlatform is a no-op on non-Linux platforms.
// Full network restore (iptables, routes, sysctls) is only available on Linux.
func (m *Manager) restorePlatform(snapshotPath string) error {
	m.log.Warn("network restore not supported on this platform — skipping")
	return nil
}
