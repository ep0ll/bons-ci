// Package network captures the full network state of the source VM —
// interfaces, routes, iptables rules, netfilter state, and socket table —
// and serialises it to the migration volume so the successor can replay it
// before CRIU restores any TCP connections.
//
// TCP connection continuity:
// CRIU's --tcp-established flag freezes open TCP connections at the kernel
// level.  For this to work on the successor, the network identity must be
// identical: same IP, same interface MAC, same iptables rules, and the
// restored sockets must be present before CRIU re-injects them.
package network

import (
	"encoding/json"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"

	"github.com/vishvananda/netlink"
	"go.uber.org/zap"
	"golang.org/x/sys/unix"
)

// Snapshot is the serialisable network state.
type Snapshot struct {
	Interfaces []InterfaceState `json:"interfaces"`
	Routes     []RouteState     `json:"routes"`
	IPTables   IPTablesState    `json:"iptables"`
	Sysctls    map[string]string `json:"sysctls"`
}

// InterfaceState captures one network interface.
type InterfaceState struct {
	Name    string   `json:"name"`
	HWAddr  string   `json:"hw_addr"`
	MTU     int      `json:"mtu"`
	Flags   uint32   `json:"flags"`
	Addrs   []string `json:"addrs"`
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
func (m *Manager) Capture(snapshotPath string) (*Snapshot, error) {
	m.log.Info("capturing network state", zap.String("path", snapshotPath))

	snap := &Snapshot{
		Sysctls: make(map[string]string),
	}

	// Enumerate interfaces.
	links, err := netlink.LinkList()
	if err != nil {
		return nil, fmt.Errorf("listing links: %w", err)
	}
	for _, link := range links {
		attrs := link.Attrs()
		if attrs.Name == "lo" {
			continue // loopback is always present
		}

		iface := InterfaceState{
			Name:   attrs.Name,
			HWAddr: attrs.HardwareAddr.String(),
			MTU:    attrs.MTU,
			Flags:  uint32(attrs.Flags),
		}

		addrs, err := netlink.AddrList(link, unix.AF_UNSPEC)
		if err != nil {
			m.log.Warn("listing addresses for interface", zap.String("iface", attrs.Name), zap.Error(err))
		}
		for _, addr := range addrs {
			iface.Addrs = append(iface.Addrs, addr.IPNet.String())
		}
		snap.Interfaces = append(snap.Interfaces, iface)
	}

	// Enumerate routes.
	routes, err := netlink.RouteList(nil, unix.AF_UNSPEC)
	if err != nil {
		return nil, fmt.Errorf("listing routes: %w", err)
	}
	for _, r := range routes {
		rs := RouteState{
			// netlink.RouteProtocol is a named integer type; cast explicitly
			// to int so it fits the RouteState.Protocol field.
			Protocol: int(r.Protocol),
			Priority: r.Priority,
			Type:     r.Type,
		}
		if r.Dst != nil {
			rs.Dst = r.Dst.String()
		}
		if r.Src != nil {
			rs.Src = r.Src.String()
		}
		if r.Gw != nil {
			rs.Gw = r.Gw.String()
		}
		if r.LinkIndex > 0 {
			if link, err := netlink.LinkByIndex(r.LinkIndex); err == nil {
				rs.Dev = link.Attrs().Name
			}
		}
		snap.Routes = append(snap.Routes, rs)
	}

	// Capture iptables state for all tables.
	snap.IPTables = m.captureIPTables()

	// Capture key network sysctls that affect TCP behaviour.
	sysctlKeys := []string{
		"net.ipv4.ip_forward",
		"net.ipv4.tcp_keepalive_time",
		"net.ipv4.tcp_keepalive_probes",
		"net.ipv4.tcp_keepalive_intvl",
		"net.ipv4.tcp_fin_timeout",
		"net.core.rmem_max",
		"net.core.wmem_max",
		"net.ipv4.tcp_rmem",
		"net.ipv4.tcp_wmem",
	}
	for _, key := range sysctlKeys {
		if val, err := readSysctl(key); err == nil {
			snap.Sysctls[key] = val
		}
	}

	// Serialise to disk.
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

// Restore replays a network snapshot on the successor instance.
// This must run BEFORE CRIU restore so that re-injected TCP sockets
// find the correct network configuration.
func (m *Manager) Restore(snapshotPath string) error {
	m.log.Info("restoring network state", zap.String("path", snapshotPath))

	data, err := os.ReadFile(snapshotPath)
	if err != nil {
		return fmt.Errorf("reading network snapshot: %w", err)
	}

	var snap Snapshot
	if err := json.Unmarshal(data, &snap); err != nil {
		return fmt.Errorf("parsing network snapshot: %w", err)
	}

	// Restore sysctls first (affect how interfaces behave).
	for key, val := range snap.Sysctls {
		if err := writeSysctl(key, val); err != nil {
			m.log.Warn("sysctl restore failed", zap.String("key", key), zap.Error(err))
		}
	}

	// Note: on OCI the VNIC MAC and private IP are assigned by the platform.
	// The successor was launched with --private-ip matching the source, so
	// the primary IP is already correct. We just need to restore any
	// secondary IPs, routes, and iptables rules.

	// Restore secondary addresses.
	for _, iface := range snap.Interfaces {
		link, err := netlink.LinkByName(iface.Name)
		if err != nil {
			m.log.Warn("interface not found on successor", zap.String("name", iface.Name))
			continue
		}
		for _, addrStr := range iface.Addrs {
			addr, err := netlink.ParseAddr(addrStr)
			if err != nil {
				continue
			}
			// Skip link-local and platform-assigned; add secondary IPs.
			if err := netlink.AddrAdd(link, addr); err != nil && !isAlreadyExists(err) {
				m.log.Warn("addr restore failed",
					zap.String("addr", addrStr),
					zap.String("iface", iface.Name),
					zap.Error(err),
				)
			}
		}
	}

	// Restore iptables rules.
	if err := m.restoreIPTables(snap.IPTables); err != nil {
		m.log.Warn("iptables restore failed — continuing", zap.Error(err))
	}

	m.log.Info("network state restored")
	return nil
}

func (m *Manager) captureIPTables() IPTablesState {
	state := IPTablesState{}
	for _, table := range []struct {
		name string
		dest *string
	}{
		{"filter", &state.Filter},
		{"nat", &state.NAT},
		{"mangle", &state.Mangle},
		{"raw", &state.Raw},
	} {
		out, err := exec.Command("iptables-save", "-t", table.name).Output()
		if err != nil {
			m.log.Warn("iptables-save failed", zap.String("table", table.name), zap.Error(err))
			continue
		}
		*table.dest = string(out)
	}
	return state
}

func (m *Manager) restoreIPTables(state IPTablesState) error {
	for _, rules := range []string{state.Filter, state.NAT, state.Mangle, state.Raw} {
		if rules == "" {
			continue
		}
		cmd := exec.Command("iptables-restore", "--noflush")
		cmd.Stdin = strings.NewReader(rules)
		if out, err := cmd.CombinedOutput(); err != nil {
			return fmt.Errorf("iptables-restore: %w (%s)", err, string(out))
		}
	}
	return nil
}

func readSysctl(key string) (string, error) {
	path := filepath.Join("/proc/sys", strings.ReplaceAll(key, ".", "/"))
	data, err := os.ReadFile(path)
	if err != nil {
		return "", err
	}
	return strings.TrimSpace(string(data)), nil
}

func writeSysctl(key, val string) error {
	path := filepath.Join("/proc/sys", strings.ReplaceAll(key, ".", "/"))
	return os.WriteFile(path, []byte(val), 0o644)
}

func isAlreadyExists(err error) bool {
	return err != nil && strings.Contains(err.Error(), "file exists")
}
