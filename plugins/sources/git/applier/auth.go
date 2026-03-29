package gitapply

import (
	"context"
	"encoding/base64"
	"fmt"
	"os"
)

// AuthProvider resolves authentication credentials for a git remote on demand.
//
// Every method must be safe for concurrent use.  Implementations may fetch
// secrets from a secrets manager, an environment variable, a BuildKit session,
// or any other source.
//
// Methods that have nothing to return should return ("", nopRelease, nil).
type AuthProvider interface {
	// HTTPAuthArgs returns the git -c arguments needed to authenticate HTTP(S)
	// requests to remote.  A typical return looks like:
	//
	//   ["-c", "http.https://github.com/.extraheader=Authorization: basic <b64>"]
	//
	// Returning nil, nil means no HTTP auth is needed.
	HTTPAuthArgs(ctx context.Context, remote string) (args []string, err error)

	// SSHSocket mounts or locates the SSH agent socket identified by socketID.
	//
	// socketID is the value from [FetchSpec.SSHSocketID].  Implementations
	// may ignore it (e.g. return $SSH_AUTH_SOCK) or use it to select among
	// multiple forwarded agents.
	//
	// The returned cleanup function must be called when the socket is no longer
	// needed.  Returning ("", nopRelease, nil) means no SSH agent auth.
	SSHSocket(ctx context.Context, socketID string) (socketPath string, cleanup func() error, err error)

	// KnownHostsFile writes the supplied SSH known_hosts data to a temp file
	// and returns its path.  hostsData is [FetchSpec.KnownSSHHosts].
	//
	// The cleanup function removes the temp file.  Return ("", nopRelease, nil)
	// when hostsData is empty or the implementation does not need a file.
	KnownHostsFile(ctx context.Context, hostsData string) (path string, cleanup func() error, err error)
}

// nopRelease is a no-op cleanup function returned by AuthProviders that have
// nothing to clean up.
func nopRelease() error { return nil }

// ─── Concrete implementations ────────────────────────────────────────────────

// NoAuthProvider provides no credentials.  Suitable for public repositories
// or when credentials are already embedded in the remote URL (not recommended).
type NoAuthProvider struct{}

var _ AuthProvider = NoAuthProvider{}

func (NoAuthProvider) HTTPAuthArgs(_ context.Context, _ string) ([]string, error) {
	return nil, nil
}
func (NoAuthProvider) SSHSocket(_ context.Context, _ string) (string, func() error, error) {
	return "", nopRelease, nil
}
func (NoAuthProvider) KnownHostsFile(_ context.Context, _ string) (string, func() error, error) {
	return "", nopRelease, nil
}

// StaticTokenAuthProvider provides a static PAT / OAuth token for HTTPS remotes.
//
// The token is Base64-encoded as "x-access-token:<token>" and sent as an
// Authorization: basic header — the convention used by GitHub, GitLab, Gitea,
// Azure DevOps, and Bitbucket.
//
// RemotePrefix scopes the token: if non-empty, only remotes whose URL starts
// with RemotePrefix receive this credential.  A sensible default is the scheme
// + host, e.g. "https://github.com/".
type StaticTokenAuthProvider struct {
	// RemotePrefix scopes the credential.  Empty means all HTTPS remotes.
	RemotePrefix string
	Token        string
	KnownHosts   string
}

var _ AuthProvider = (*StaticTokenAuthProvider)(nil)

func (p *StaticTokenAuthProvider) HTTPAuthArgs(_ context.Context, remote string) ([]string, error) {
	if p.Token == "" {
		return nil, nil
	}
	scope := p.RemotePrefix
	if scope == "" {
		scope = remote
	}
	encoded := base64.StdEncoding.EncodeToString(
		fmt.Appendf(nil, "x-access-token:%s", p.Token),
	)
	header := "basic " + encoded
	return []string{"-c", "http." + scope + ".extraheader=Authorization: " + header}, nil
}

func (p *StaticTokenAuthProvider) SSHSocket(_ context.Context, _ string) (string, func() error, error) {
	return "", nopRelease, nil
}

func (p *StaticTokenAuthProvider) KnownHostsFile(_ context.Context, hosts string) (string, func() error, error) {
	return writeKnownHostsTemp(hosts)
}

// StaticHeaderAuthProvider provides a verbatim Authorization header value for
// HTTPS remotes.  Use when the caller constructs the header itself (e.g. for
// JWT bearer tokens or custom schemes).
type StaticHeaderAuthProvider struct {
	// RemotePrefix scopes the credential.  Empty means all HTTPS remotes.
	RemotePrefix string
	// Header is the verbatim value after "Authorization: ", e.g. "bearer <token>".
	Header     string
	KnownHosts string
}

var _ AuthProvider = (*StaticHeaderAuthProvider)(nil)

func (p *StaticHeaderAuthProvider) HTTPAuthArgs(_ context.Context, remote string) ([]string, error) {
	if p.Header == "" {
		return nil, nil
	}
	scope := p.RemotePrefix
	if scope == "" {
		scope = remote
	}
	return []string{"-c", "http." + scope + ".extraheader=Authorization: " + p.Header}, nil
}

func (p *StaticHeaderAuthProvider) SSHSocket(_ context.Context, _ string) (string, func() error, error) {
	return "", nopRelease, nil
}

func (p *StaticHeaderAuthProvider) KnownHostsFile(_ context.Context, hosts string) (string, func() error, error) {
	return writeKnownHostsTemp(hosts)
}

// EnvSSHAuthProvider forwards the ambient $SSH_AUTH_SOCK to git subprocesses.
// This is convenient in development environments; for production use prefer
// an AuthProvider that mounts an explicit, isolated socket.
type EnvSSHAuthProvider struct {
	KnownHosts string
}

var _ AuthProvider = (*EnvSSHAuthProvider)(nil)

func (p *EnvSSHAuthProvider) HTTPAuthArgs(_ context.Context, _ string) ([]string, error) {
	return nil, nil
}

func (p *EnvSSHAuthProvider) SSHSocket(_ context.Context, _ string) (string, func() error, error) {
	sock := os.Getenv("SSH_AUTH_SOCK")
	return sock, nopRelease, nil
}

func (p *EnvSSHAuthProvider) KnownHostsFile(_ context.Context, hosts string) (string, func() error, error) {
	if hosts == "" {
		hosts = p.KnownHosts
	}
	return writeKnownHostsTemp(hosts)
}

// ─── Internal helpers ─────────────────────────────────────────────────────────

// writeKnownHostsTemp writes data to a 0600 temp file and returns its path.
// The returned cleanup function removes the file.
// Returns ("", nopRelease, nil) when data is empty.
func writeKnownHostsTemp(data string) (string, func() error, error) {
	if data == "" {
		return "", nopRelease, nil
	}
	f, err := os.CreateTemp("", "gitapply-known-hosts-*")
	if err != nil {
		return "", nopRelease, fmt.Errorf("gitapply: create known_hosts temp: %w", err)
	}
	cleanup := func() error { return os.Remove(f.Name()) }

	// 0600: readable only by owner; git requires at most 0644.
	if err := f.Chmod(0o600); err != nil {
		_ = f.Close()
		_ = cleanup()
		return "", nopRelease, fmt.Errorf("gitapply: chmod known_hosts: %w", err)
	}
	if _, err := f.WriteString(data); err != nil {
		_ = f.Close()
		_ = cleanup()
		return "", nopRelease, fmt.Errorf("gitapply: write known_hosts: %w", err)
	}
	if err := f.Close(); err != nil {
		_ = cleanup()
		return "", nopRelease, fmt.Errorf("gitapply: close known_hosts: %w", err)
	}
	return f.Name(), cleanup, nil
}
