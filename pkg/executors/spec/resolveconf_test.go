package oci

import (
	"fmt"
	"os"
	"path"
	"testing"
	"time"

	"github.com/moby/buildkit/solver/pb"
	"github.com/stretchr/testify/require"
)

// Canonical resolv.conf content used as the built-in default when no source
// file exists or when it contains only loopback nameservers.
const wantDefaultResolvConf = `nameserver 8.8.8.8
nameserver 8.8.4.4
nameserver 2001:4860:4860::8888
nameserver 2001:4860:4860::8844
`

// Options appended when a local DNS is replaced by the built-in default.
const wantDNSOption = `options ndots:0
`

// localDNSResolvConf represents a resolv.conf that delegates to a loopback
// resolver (e.g. systemd-resolved or Docker's embedded DNS).
const localDNSResolvConf = `nameserver 127.0.0.11
options ndots:0
`

// regularResolvConf represents a non-loopback resolv.conf (e.g. the one
// written by DHCP on a desktop machine).
const regularResolvConf = `nameserver 192.168.65.5
`

// TestResolvConf exercises GetResolvConf through the per-test ResolvConfManager
// so that tests can run in parallel without sharing global state.
//
// Why this is different from the original:
//   - The original patched the global `resolvconfPath` variable and documented
//     "must not run in parallel".  Each test now gets its own manager and its
//     own injected sourcePath function, so there is no shared state to race on.
//   - The bug where lastNotEmpty was never set is also covered: see the
//     "TestRegenerateResolvconfToRemoveLocalDNS" case which verifies that
//     switching from HOST→UNSET replaces the local DNS with defaults.
func TestResolvConf(t *testing.T) {
	t.Parallel()

	cases := []struct {
		name        string
		dt          []byte
		executions  int
		networkMode []pb.NetMode
		expected    []string
	}{
		{
			// Source file does not exist → built-in defaults are used.
			name:        "ResolvConfNotExist",
			dt:          nil,
			executions:  1,
			networkMode: []pb.NetMode{pb.NetMode_UNSET},
			expected:    []string{wantDefaultResolvConf},
		},
		{
			// HOST mode, source missing → built-in defaults.
			name:        "NetModeHostResolvConfNotExist",
			dt:          nil,
			executions:  1,
			networkMode: []pb.NetMode{pb.NetMode_HOST},
			expected:    []string{wantDefaultResolvConf},
		},
		{
			// HOST mode, regular upstream nameserver → pass through as-is.
			name:        "NetModeHostWithoutLocalDNS",
			dt:          []byte(regularResolvConf),
			executions:  1,
			networkMode: []pb.NetMode{pb.NetMode_HOST},
			expected:    []string{regularResolvConf},
		},
		{
			// HOST mode, loopback nameserver → keep it because the container
			// shares the host's loopback interface.
			name:        "NetModeHostWithLocalDNS",
			dt:          []byte(localDNSResolvConf),
			executions:  1,
			networkMode: []pb.NetMode{pb.NetMode_HOST},
			expected:    []string{localDNSResolvConf},
		},
		{
			// Non-host mode, regular upstream → pass through as-is.
			name:        "NetModeNotHostWithoutLocalDNS",
			dt:          []byte(regularResolvConf),
			executions:  1,
			networkMode: []pb.NetMode{pb.NetMode_UNSET},
			expected:    []string{regularResolvConf},
		},
		{
			// Non-host mode, loopback nameserver → replace with built-in
			// defaults because 127.0.0.11 is not reachable from a network ns.
			name:        "NetModeNotHostWithLocalDNS",
			dt:          []byte(localDNSResolvConf),
			executions:  1,
			networkMode: []pb.NetMode{pb.NetMode_UNSET},
			expected:    []string{fmt.Sprintf("%s%s", wantDefaultResolvConf, wantDNSOption)},
		},
		{
			// Two-phase: HOST first (keeps local DNS), then UNSET (replaces it).
			// This verifies that switching network mode forces regeneration.
			name:        "RegenerateResolvconfToRemoveLocalDNS",
			dt:          []byte(localDNSResolvConf),
			executions:  2,
			networkMode: []pb.NetMode{pb.NetMode_HOST, pb.NetMode_UNSET},
			expected: []string{
				localDNSResolvConf,
				fmt.Sprintf("%s%s", wantDefaultResolvConf, wantDNSOption),
			},
		},
		{
			// Two-phase: UNSET first (replaces local DNS), then HOST (keeps it).
			name:        "RegenerateResolvconfToAddLocalDNS",
			dt:          []byte(localDNSResolvConf),
			executions:  2,
			networkMode: []pb.NetMode{pb.NetMode_UNSET, pb.NetMode_HOST},
			expected: []string{
				fmt.Sprintf("%s%s", wantDefaultResolvConf, wantDNSOption),
				localDNSResolvConf,
			},
		},
	}

	for _, tt := range cases {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()

			ctx := t.Context()
			stateDir := t.TempDir()

			// Each sub-test gets its own manager with an injected sourcePath
			// that writes tt.dt to a fresh temp file on each call.  This lets
			// us control whether the source "changes" between executions (by
			// varying the mtime) without touching any global state.
			mgr := newResolvConfManagerWithSource(func(netMode pb.NetMode) string {
				if tt.dt == nil {
					return "no-such-file"
				}
				p := path.Join(t.TempDir(), "resolv.conf")
				require.NoError(t, os.WriteFile(p, tt.dt, 0o600))
				return p
			})

			for i := 0; i < tt.executions; i++ {
				if i > 0 {
					// Ensure the mtime of the source file written on this
					// iteration is strictly after the output written on the
					// previous iteration, so needsRegeneration fires.
					time.Sleep(100 * time.Millisecond)
				}

				p, err := mgr.Get(ctx, stateDir, nil, nil, tt.networkMode[i])
				require.NoError(t, err)

				b, err := os.ReadFile(p)
				require.NoError(t, err)
				require.Equal(t, tt.expected[i], string(b),
					"execution %d: unexpected resolv.conf content", i)
			}
		})
	}
}
