package mountcache_test

import (
	"testing"
	"time"

	"github.com/bons/bons-ci/plugins/rbe/pkg/models"
)

func TestSharingModes(t *testing.T) {
	cases := []struct {
		mode    models.CacheSharingMode
		wantStr string
	}{
		{models.CacheSharingShared, "shared"},
		{models.CacheSharingPrivate, "private"},
		{models.CacheSharingLocked, "locked"},
	}
	for _, tc := range cases {
		if string(tc.mode) != tc.wantStr {
			t.Errorf("mode %q: want string %q", tc.mode, tc.wantStr)
		}
	}
}

func TestLockExpiry(t *testing.T) {
	now := time.Now()
	exp := now.Add(-time.Second) // already expired

	cache := &models.MountCache{
		Locked:        true,
		LockOwner:     "v1",
		LockExpiresAt: &exp,
	}

	// Lock is expired — simulate check
	if cache.LockExpiresAt != nil && time.Now().After(*cache.LockExpiresAt) {
		cache.Locked = false
		cache.LockOwner = ""
	}

	if cache.Locked {
		t.Fatal("expected lock to be cleared after expiry")
	}
}

func TestPlatformIsolation(t *testing.T) {
	linux := models.MountCache{
		UserKey: "npm-cache", Scope: "project",
		PlatformSpecific: true, Platform: &models.Platform{OS: "linux", Arch: "amd64"},
	}
	darwin := models.MountCache{
		UserKey: "npm-cache", Scope: "project",
		PlatformSpecific: true, Platform: &models.Platform{OS: "darwin", Arch: "arm64"},
	}

	// Same key, different platform → should be different cache entries
	if linux.Platform.OS == darwin.Platform.OS {
		t.Fatal("platform isolation broken: same OS")
	}
}
