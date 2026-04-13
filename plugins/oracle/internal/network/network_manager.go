// Package network captures the full network state of the source VM —
// interfaces, routes, iptables rules, netfilter state, and socket table —
// and serialises it to the migration volume so the successor can replay it
// before CRIU restores any TCP connections.
//
// TCP connection continuity:
// CRIU's --tcp-established flag freezes open TCP connections at the kernel
// level. For this to work on the successor, the network identity must be
// identical: same IP, same interface MAC, same iptables rules, and the
// restored sockets must be present before CRIU re-injects them.
//
// NOTE on private IP: OCI does not allow two running instances to share a
// private IP. During migration the source is still alive while the successor
// boots, so the successor receives a new auto-assigned IP. CRIU restores
// process memory state; open TCP connections are dropped and retried by the
// application. For CI/CD build workloads this is acceptable — the build
// cache, compiler state, and downloaded modules are preserved in memory.
package network

import (
	"go.uber.org/zap"
)

// Snapshot is the serialisable network state.
type Snapshot struct {
	Interfaces []InterfaceState  `json:"interfaces"`
	Routes     []RouteState      `json:"routes"`
	IPTables   IPTablesState     `json:"iptables"`
	Sysctls    map[string]string `json:"sysctls"`
}

// InterfaceState captures one network interface.
type InterfaceState struct {
	Name   string   `json:"name"`
	HWAddr string   `json:"hw_addr"`
	MTU    int      `json:"mtu"`
	Flags  uint32   `json:"flags"`
	Addrs  []string `json:"addrs"`
}

// RouteState captures one IP route.
// Protocol is stored as int so we can serialise netlink.RouteProtocol
// (a named type) portably across Go versions and netlink library updates.
type RouteState struct {
	Dst      string `json:"dst"`
	Src      string `json:"src"`
	Gw       string `json:"gw"`
	Dev      string `json:"dev"`
	Protocol int    `json:"protocol"` // cast from netlink.RouteProtocol
	Priority int    `json:"priority"`
	Type     int    `json:"type"`
}

// IPTablesState contains iptables-save output for each table.
type IPTablesState struct {
	Filter string `json:"filter"`
	NAT    string `json:"nat"`
	Mangle string `json:"mangle"`
	Raw    string `json:"raw"`
}

// Manager captures and restores network state.
type Manager struct {
	log *zap.Logger
}

// NewManager constructs a network Manager.
func NewManager(log *zap.Logger) *Manager {
	return &Manager{log: log}
}

// Capture snapshots the current network state and writes it to snapshotPath.
// The implementation is platform-specific: full netlink capture on Linux,
// stdlib-only capture on other platforms (macOS, development).
func (m *Manager) Capture(snapshotPath string) (*Snapshot, error) {
	return m.capturePlatform(snapshotPath)
}

// Restore replays a network snapshot on the successor instance.
// This must run BEFORE CRIU restore so that re-injected TCP sockets
// find the correct network configuration. No-op on non-Linux platforms.
func (m *Manager) Restore(snapshotPath string) error {
	return m.restorePlatform(snapshotPath)
}
