package gitapply

import (
	"errors"
	"strings"
	"testing"
)

// ─── FetchSpec.Validate ───────────────────────────────────────────────────────

func TestFetchSpec_Validate_valid(t *testing.T) {
	t.Parallel()
	cases := []FetchSpec{
		{Remote: "https://github.com/user/repo.git"},
		{Remote: "https://github.com/user/repo.git", Ref: "main"},
		{Remote: "https://github.com/user/repo.git", Ref: "refs/heads/main"},
		{Remote: "https://github.com/user/repo.git", Checksum: "abc1234"},
		{Remote: "https://github.com/user/repo.git", Checksum: strings.Repeat("a", 40)},
		{Remote: "https://github.com/user/repo.git", Checksum: strings.Repeat("a", 64)},
		{Remote: "git@github.com:user/repo.git"},
		{Remote: "ssh://github.com/user/repo.git"},
		{Remote: "git://github.com/user/repo.git"},
		{Remote: "https://github.com/user/repo.git", Subdir: "subdir"},
		{Remote: "https://github.com/user/repo.git", Subdir: "a/b/c"},
		{Remote: "https://github.com/user/repo.git", KeepGitDir: true},
		// file:// is valid for local repos and for the internal KeepGitDir clone.
		{Remote: "file:///tmp/repo.git"},
		{Remote: "file:///home/user/repo", Ref: "main"},
	}
	for _, spec := range cases {
		spec := spec
		t.Run(spec.Remote+"/"+spec.Ref, func(t *testing.T) {
			t.Parallel()
			if err := spec.Validate(); err != nil {
				t.Errorf("unexpected error for valid spec %+v: %v", spec, err)
			}
		})
	}
}

func TestFetchSpec_Validate_invalid(t *testing.T) {
	t.Parallel()
	cases := []struct {
		name    string
		spec    FetchSpec
		wantErr error
	}{
		{
			name:    "empty remote",
			spec:    FetchSpec{},
			wantErr: ErrInvalidRemote,
		},
		{
			name:    "bare hostname without scheme",
			spec:    FetchSpec{Remote: "github.com/user/repo.git"},
			wantErr: ErrInvalidRemote,
		},
		{
			name:    "ftp scheme",
			spec:    FetchSpec{Remote: "ftp://github.com/user/repo.git"},
			wantErr: ErrInvalidRemote,
		},
		{
			name:    "checksum too short",
			spec:    FetchSpec{Remote: "https://github.com/a/b.git", Checksum: "abc"},
			wantErr: ErrInvalidChecksum,
		},
		{
			name:    "checksum non-hex",
			spec:    FetchSpec{Remote: "https://github.com/a/b.git", Checksum: "gg12345"},
			wantErr: ErrInvalidChecksum,
		},
		{
			name:    "subdir traversal dotdot",
			spec:    FetchSpec{Remote: "https://github.com/a/b.git", Subdir: "a/../../../etc"},
			wantErr: ErrSubdirTraversal,
		},
		{
			name:    "absolute subdir",
			spec:    FetchSpec{Remote: "https://github.com/a/b.git", Subdir: "/etc"},
			wantErr: ErrSubdirTraversal,
		},
	}
	for _, tc := range cases {
		tc := tc
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			err := tc.spec.Validate()
			if err == nil {
				t.Fatalf("expected error %v, got nil", tc.wantErr)
			}
			if !errors.Is(err, tc.wantErr) {
				t.Errorf("expected errors.Is(%v) but got: %v", tc.wantErr, err)
			}
		})
	}
}

// ─── validateSubdir ───────────────────────────────────────────────────────────

func TestValidateSubdir(t *testing.T) {
	t.Parallel()
	validCases := []string{
		"",
		"sub",
		"a/b/c",
		"deeply/nested/path",
		"path.with.dots",
	}
	for _, c := range validCases {
		c := c
		t.Run("valid/"+c, func(t *testing.T) {
			t.Parallel()
			if err := validateSubdir(c); err != nil {
				t.Errorf("unexpected error for valid subdir %q: %v", c, err)
			}
		})
	}

	invalidCases := []struct {
		input string
		want  error
	}{
		{"/etc/passwd", ErrSubdirTraversal},
		{"../sibling", ErrSubdirTraversal},
		{"a/../../etc", ErrSubdirTraversal},
		{"a/b/../../../root", ErrSubdirTraversal},
	}
	for _, c := range invalidCases {
		c := c
		t.Run("invalid/"+c.input, func(t *testing.T) {
			t.Parallel()
			err := validateSubdir(c.input)
			if err == nil {
				t.Fatalf("expected error for %q, got nil", c.input)
			}
			if !errors.Is(err, c.want) {
				t.Errorf("expected %v; got %v", c.want, err)
			}
		})
	}
}

// ─── redactURL ────────────────────────────────────────────────────────────────

func TestRedactURL(t *testing.T) {
	t.Parallel()
	cases := []struct {
		input string
		want  string
	}{
		{
			"https://user:secret@github.com/org/repo.git",
			"https://github.com/org/repo.git",
		},
		{
			"https://token@github.com/org/repo.git",
			"https://github.com/org/repo.git",
		},
		{
			"https://github.com/org/repo.git",
			"https://github.com/org/repo.git",
		},
		{
			// SCP-style: no credentials, should be returned as-is.
			"git@github.com:org/repo.git",
			"git@github.com:org/repo.git",
		},
	}
	for _, tc := range cases {
		tc := tc
		t.Run(tc.input, func(t *testing.T) {
			t.Parallel()
			got := redactURL(tc.input)
			if got != tc.want {
				t.Errorf("redactURL(%q) = %q; want %q", tc.input, got, tc.want)
			}
		})
	}
}

// ─── IsCommitSHA ──────────────────────────────────────────────────────────────

func TestIsCommitSHA(t *testing.T) {
	t.Parallel()
	validSHAs := []string{
		strings.Repeat("a", 40), // SHA-1
		strings.Repeat("f", 40),
		strings.Repeat("0", 40),
		strings.Repeat("a", 64),             // SHA-256
		"abc1234" + strings.Repeat("0", 33), // 40 chars mixed
	}
	for _, sha := range validSHAs {
		sha := sha
		t.Run("valid/"+sha[:8], func(t *testing.T) {
			t.Parallel()
			if !IsCommitSHA(sha) {
				t.Errorf("IsCommitSHA(%q) should be true", sha)
			}
		})
	}

	invalidSHAs := []string{
		"",
		"main",
		"refs/heads/main",
		"abc123",                // too short (abbreviated)
		strings.Repeat("g", 40), // non-hex
		strings.Repeat("a", 39), // one too short
		strings.Repeat("a", 41), // one too long (not 40 or 64)
		strings.Repeat("a", 63), // one too short for SHA-256
	}
	for _, sha := range invalidSHAs {
		sha := sha
		t.Run("invalid/"+func() string {
			if len(sha) > 12 {
				return sha[:12] + "..."
			}
			return sha
		}(), func(t *testing.T) {
			t.Parallel()
			if IsCommitSHA(sha) {
				t.Errorf("IsCommitSHA(%q) should be false", sha)
			}
		})
	}
}

// ─── isGitTransport ───────────────────────────────────────────────────────────

func TestIsGitTransport(t *testing.T) {
	t.Parallel()
	positives := []string{
		"https://github.com/user/repo.git",
		"http://github.com/user/repo.git",
		"ssh://github.com/user/repo.git",
		"git://github.com/user/repo.git",
		"git@github.com:user/repo.git",
		"root@example.com:path/to/repo.git",
		// file:// is a legitimate transport for local repos and is used
		// internally by KeepGitDir checkout.
		"file:///tmp/repo.git",
		"file:///home/user/myrepo",
	}
	for _, u := range positives {
		u := u
		t.Run("positive/"+u, func(t *testing.T) {
			t.Parallel()
			if !isGitTransport(u) {
				t.Errorf("isGitTransport(%q) should be true", u)
			}
		})
	}

	negatives := []string{
		"",
		"github.com/user/repo.git",
		"ftp://github.com/user/repo.git",
		"/absolute/path/repo.git",
		"./relative/path/repo.git",
	}
	for _, u := range negatives {
		u := u
		t.Run("negative/"+u, func(t *testing.T) {
			t.Parallel()
			if isGitTransport(u) {
				t.Errorf("isGitTransport(%q) should be false", u)
			}
		})
	}
}
