package gitapply

import (
	"context"
	"os"
	"strings"
	"testing"
)

var testCtx = context.Background()

// ─── NoAuthProvider ───────────────────────────────────────────────────────────

func TestNoAuthProvider_HTTPAuthArgs(t *testing.T) {
	t.Parallel()
	args, err := NoAuthProvider{}.HTTPAuthArgs(testCtx, "https://github.com/a/b.git")
	if err != nil {
		t.Fatal(err)
	}
	if len(args) != 0 {
		t.Errorf("expected no args; got %v", args)
	}
}

func TestNoAuthProvider_SSHSocket(t *testing.T) {
	t.Parallel()
	sock, cleanup, err := NoAuthProvider{}.SSHSocket(testCtx, "default")
	if err != nil {
		t.Fatal(err)
	}
	defer cleanup()
	if sock != "" {
		t.Errorf("expected empty socket; got %q", sock)
	}
}

// ─── StaticTokenAuthProvider ─────────────────────────────────────────────────

func TestStaticTokenAuthProvider_HTTPAuthArgs(t *testing.T) {
	t.Parallel()
	p := &StaticTokenAuthProvider{
		RemotePrefix: "https://github.com/",
		Token:        "ghp_MyPersonalAccessToken",
	}
	args, err := p.HTTPAuthArgs(testCtx, "https://github.com/org/repo.git")
	if err != nil {
		t.Fatal(err)
	}
	// Must produce exactly ["-c", "http.<scope>.extraheader=Authorization: basic <b64>"]
	if len(args) != 2 {
		t.Fatalf("expected 2 args; got %d: %v", len(args), args)
	}
	if args[0] != "-c" {
		t.Errorf("first arg must be -c; got %q", args[0])
	}
	if !strings.Contains(args[1], "extraheader") {
		t.Errorf("second arg must contain 'extraheader'; got %q", args[1])
	}
	if !strings.HasPrefix(args[1], "http.https://github.com/") {
		t.Errorf("scope prefix should match RemotePrefix; got %q", args[1])
	}
	if !strings.Contains(args[1], "basic ") {
		t.Errorf("value should use 'basic ' auth scheme; got %q", args[1])
	}
	// The raw token must NOT appear in plaintext — it should be Base64-encoded.
	if strings.Contains(args[1], "ghp_MyPersonalAccessToken") {
		t.Errorf("token must be Base64-encoded, not plaintext; got %q", args[1])
	}
}

func TestStaticTokenAuthProvider_EmptyToken(t *testing.T) {
	t.Parallel()
	p := &StaticTokenAuthProvider{}
	args, err := p.HTTPAuthArgs(testCtx, "https://github.com/a/b.git")
	if err != nil {
		t.Fatal(err)
	}
	if len(args) != 0 {
		t.Errorf("empty token should produce no args; got %v", args)
	}
}

// ─── StaticHeaderAuthProvider ────────────────────────────────────────────────

func TestStaticHeaderAuthProvider_HTTPAuthArgs(t *testing.T) {
	t.Parallel()
	p := &StaticHeaderAuthProvider{
		Header: "bearer eyJhbGciOiJSUzI1NiJ9.payload.sig",
	}
	args, err := p.HTTPAuthArgs(testCtx, "https://example.com/repo.git")
	if err != nil {
		t.Fatal(err)
	}
	if len(args) != 2 {
		t.Fatalf("expected 2 args; got %d", len(args))
	}
	if !strings.Contains(args[1], "bearer ") {
		t.Errorf("header should be verbatim; got %q", args[1])
	}
}

// ─── writeKnownHostsTemp ─────────────────────────────────────────────────────

func TestWriteKnownHostsTemp_empty(t *testing.T) {
	t.Parallel()
	path, cleanup, err := writeKnownHostsTemp("")
	if err != nil {
		t.Fatal(err)
	}
	defer cleanup()
	if path != "" {
		t.Errorf("empty data should return empty path; got %q", path)
	}
}

func TestWriteKnownHostsTemp_nonEmpty(t *testing.T) {
	t.Parallel()
	const data = "github.com ssh-rsa AAAAB3NzaC1yc2EAAAA"
	path, cleanup, err := writeKnownHostsTemp(data)
	if err != nil {
		t.Fatal(err)
	}
	defer cleanup()

	if path == "" {
		t.Fatal("expected a non-empty file path")
	}
	stat, err := os.Stat(path)
	if err != nil {
		t.Fatalf("temp file should exist: %v", err)
	}
	// Verify 0600 permissions.
	if perm := stat.Mode().Perm(); perm != 0o600 {
		t.Errorf("known_hosts should be 0600; got %04o", perm)
	}
	// Verify content.
	content, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	if string(content) != data {
		t.Errorf("content mismatch: want %q, got %q", data, string(content))
	}
	// After cleanup the file must be gone.
	if err := cleanup(); err != nil {
		t.Fatalf("cleanup failed: %v", err)
	}
	if _, err := os.Stat(path); !os.IsNotExist(err) {
		t.Errorf("file should be removed after cleanup; stat returned: %v", err)
	}
}
