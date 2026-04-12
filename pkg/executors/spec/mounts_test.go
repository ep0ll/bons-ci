package oci

import (
	"runtime"
	"testing"

	"github.com/containerd/containerd/v2/pkg/oci"
	"github.com/moby/buildkit/util/appcontext"
	specs "github.com/opencontainers/runtime-spec/specs-go"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// containerdDefMounts mirrors the default mount list that containerd generates
// for a Linux container.  It is reproduced here so that tests are not coupled
// to containerd's internal default-spec generation.
//
// Source: https://github.com/containerd/containerd/blob/main/oci/mounts.go
var containerdDefMounts = []specs.Mount{
	{
		Destination: "/proc",
		Type:        "proc",
		Source:      "proc",
		Options:     []string{"nosuid", "noexec", "nodev"},
	},
	{
		Destination: "/dev",
		Type:        "tmpfs",
		Source:      "tmpfs",
		Options:     []string{"nosuid", "strictatime", "mode=755", "size=65536k"},
	},
	{
		Destination: "/dev/pts",
		Type:        "devpts",
		Source:      "devpts",
		Options:     []string{"nosuid", "noexec", "newinstance", "ptmxmode=0666", "mode=0620", "gid=5"},
	},
	{
		Destination: "/dev/shm",
		Type:        "tmpfs",
		Source:      "shm",
		Options:     []string{"nosuid", "noexec", "nodev", "mode=1777", "size=65536k"},
	},
	{
		Destination: "/dev/mqueue",
		Type:        "mqueue",
		Source:      "mqueue",
		Options:     []string{"nosuid", "noexec", "nodev"},
	},
	{
		Destination: "/sys",
		Type:        "sysfs",
		Source:      "sysfs",
		Options:     []string{"nosuid", "noexec", "nodev", "ro"},
	},
	{
		Destination: "/run",
		Type:        "tmpfs",
		Source:      "tmpfs",
		Options:     []string{"nosuid", "strictatime", "mode=755", "size=65536k"},
	},
}

func TestHasPrefix(t *testing.T) {
	t.Parallel()

	cases := []struct {
		path     string
		prefix   string
		expected bool
	}{
		{"/foo/bar", "/foo", true},
		{"/foo/bar", "/foo/", true}, // trailing slash on prefix is normalised
		{"/foo/bar", "/", true},     // root contains everything
		{"/foo", "/foo", true},      // exact match
		{"/foo/bar", "/bar", false}, // sibling directory
		{"/foo/bar", "foo", false},  // relative prefix is never a match
		{"/foobar", "/foo", false},  // prefix must be followed by separator
	}

	if runtime.GOOS == "windows" {
		cases = append(cases,
			struct {
				path     string
				prefix   string
				expected bool
			}{"C:\\foo\\bar", "C:\\foo", true},
			struct {
				path     string
				prefix   string
				expected bool
			}{"C:\\foo\\bar", "C:\\foo\\", true},
			struct {
				path     string
				prefix   string
				expected bool
			}{"C:\\foo\\bar", "C:\\", true},
			struct {
				path     string
				prefix   string
				expected bool
			}{"C:\\foo", "C:\\foo", true},
			struct {
				path     string
				prefix   string
				expected bool
			}{"C:\\foo\\bar", "C:\\bar", false},
			struct {
				path     string
				prefix   string
				expected bool
			}{"C:\\foo\\bar", "foo", false},
			struct {
				path     string
				prefix   string
				expected bool
			}{"C:\\foobar", "C:\\foo", false},
		)
	}

	for i, tc := range cases {
		tc := tc
		t.Run("", func(t *testing.T) {
			t.Parallel()
			actual := hasPrefix(tc.path, tc.prefix)
			assert.Equal(t, tc.expected, actual,
				"case %d: hasPrefix(%q, %q)", i, tc.path, tc.prefix)
		})
	}
}

func TestWithRemovedMounts(t *testing.T) {
	t.Parallel()

	s := oci.Spec{Mounts: containerdDefMounts}
	oldLen := len(s.Mounts)

	err := withRemovedMount("/run")(appcontext.Context(), nil, nil, &s)
	require.NoError(t, err)
	assert.Equal(t, oldLen-1, len(s.Mounts))
	for _, m := range s.Mounts {
		assert.NotEqual(t, "/run", m.Destination, "withRemovedMount should have removed /run")
	}
}

func TestDedupMounts(t *testing.T) {
	t.Parallel()

	// Three extra mounts: two override existing destinations, one is new.
	extra := []specs.Mount{
		{
			// Overrides the /dev/shm entry at index 3.
			Destination: "/dev/shm",
			Type:        "tmpfs",
			Source:      "shm",
			Options:     []string{"nosuid", "size=131072k"},
		},
		{
			// New destination; should appear at the end.
			Destination: "/foo",
			Type:        "bind",
			Source:      "/bar",
			Options:     []string{"nosuid", "noexec", "nodev", "rbind", "ro"},
		},
		{
			// Overrides the /dev/mqueue entry at index 4.
			Destination: "/dev/mqueue",
			Type:        "mqueue",
			Source:      "mqueue",
			Options:     []string{"nosuid"},
		},
	}

	s := oci.Spec{Mounts: append(containerdDefMounts, extra...)}
	beforeLen := len(s.Mounts)

	s.Mounts = dedupMounts(s.Mounts)

	// Two destinations were duplicated; the total should decrease by 2.
	require.Equal(t, beforeLen-2, len(s.Mounts))

	// /dev/shm at its original index (3) should carry the overriding options.
	assert.Equal(t, specs.Mount{
		Destination: "/dev/shm",
		Type:        "tmpfs",
		Source:      "shm",
		Options:     []string{"nosuid", "size=131072k"},
	}, s.Mounts[3], "last /dev/shm definition should win")

	// /foo (new destination) should be the last entry.
	assert.Equal(t, specs.Mount{
		Destination: "/foo",
		Type:        "bind",
		Source:      "/bar",
		Options:     []string{"nosuid", "noexec", "nodev", "rbind", "ro"},
	}, s.Mounts[len(s.Mounts)-1], "/foo should be appended at the end")
}
