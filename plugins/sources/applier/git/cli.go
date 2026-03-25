package gitapply

import (
	"bytes"
	"context"
	"io"
	"os"
	"os/exec"
	"strings"
)

// gitBaseConfig is applied to every git invocation via -c flags.
// These settings are security-critical and must not be overridden by the
// caller's environment or user configuration.
var gitBaseConfig = []string{
	// Suppress interactive password prompts.  If git would normally ask the
	// user for credentials (e.g. HTTPS without an auth header) it exits with
	// a non-zero status instead, which surfaces a clear error rather than
	// hanging.
	"-c", "core.askPass=true",

	// Git protocol v2 is more efficient and exposes less server metadata.
	"-c", "protocol.version=2",

	// Prevent git from applying fsck restrictions from user config that
	// might reject valid objects we intentionally create or fetch.
	"-c", "fetch.fsckObjects=false",
}

// gitCLI holds the configuration for running git sub-commands.
// Every field is safe to copy; use [gitCLI.with] to create a derived instance.
type gitCLI struct {
	runner          ProcessRunner
	gitDir          string   // value for --git-dir; empty = omit
	workDir         string   // value for --work-tree; empty = omit
	dir             string   // process working directory (cmd.Dir); empty = inherit
	extraConfigArgs []string // pre-built "-c key=value" args (e.g. auth headers)
	sshSocketPath   string   // path to SSH agent socket
	knownHostsPath  string   // path to known_hosts file

	// Optional writers for stdout/stderr.  When nil the output is captured
	// in memory and returned / included in error messages.
	stdout io.Writer
	stderr io.Writer
}

// newGitCLI creates a gitCLI with the given runner and optional options.
func newGitCLI(runner ProcessRunner, opts ...cliOpt) *gitCLI {
	c := &gitCLI{runner: runner}
	for _, o := range opts {
		o(c)
	}
	return c
}

// cliOpt is a functional option for [gitCLI].
type cliOpt func(*gitCLI)

func withGitDir(dir string) cliOpt     { return func(c *gitCLI) { c.gitDir = dir } }
func withWorkTree(dir string) cliOpt   { return func(c *gitCLI) { c.workDir = dir } }
// withDir sets the process working directory for git sub-commands.
// This is distinct from withWorkTree (which sets the --work-tree flag):
// withDir controls cmd.Dir and is needed for git-submodule, which is a shell
// script that detects the working tree from the process CWD rather than from
// --work-tree / --git-dir flags passed to the parent git process.
func withDir(dir string) cliOpt        { return func(c *gitCLI) { c.dir = dir } }
func withSSHSocket(p string) cliOpt    { return func(c *gitCLI) { c.sshSocketPath = p } }
func withKnownHosts(p string) cliOpt   { return func(c *gitCLI) { c.knownHostsPath = p } }
func withStdout(w io.Writer) cliOpt    { return func(c *gitCLI) { c.stdout = w } }
func withStderr(w io.Writer) cliOpt    { return func(c *gitCLI) { c.stderr = w } }

// withExtraConfigArgs appends git -c config arguments (e.g. auth headers).
// Each call appends; it does not replace the existing slice.
func withExtraConfigArgs(args ...string) cliOpt {
	return func(c *gitCLI) {
		c.extraConfigArgs = append(c.extraConfigArgs, args...)
	}
}

// with returns a shallow copy of c with additional options applied.
// The extraConfigArgs slice is copied so that independent instances do not
// share the same underlying array.
func (c *gitCLI) with(opts ...cliOpt) *gitCLI {
	dup := *c
	dup.extraConfigArgs = append([]string(nil), c.extraConfigArgs...)
	for _, o := range opts {
		o(&dup)
	}
	return &dup
}

// run executes the git sub-command given by subcmdArgs under the configured
// git-dir / work-tree and returns stdout bytes.
//
// stderr is captured internally and included in the returned error; it is
// never written to the parent process's stderr unless an explicit stderr
// writer was provided via [withStderr].
func (c *gitCLI) run(ctx context.Context, subcmdArgs ...string) ([]byte, error) {
	// Build the full argument list:
	//   [--git-dir=…] [--work-tree=…] <base-config> <extra-config> <subcmd-args>
	args := make([]string, 0, 4+len(gitBaseConfig)+len(c.extraConfigArgs)+len(subcmdArgs))
	if c.gitDir != "" {
		args = append(args, "--git-dir="+c.gitDir)
	}
	if c.workDir != "" {
		args = append(args, "--work-tree="+c.workDir)
	}
	args = append(args, gitBaseConfig...)
	args = append(args, c.extraConfigArgs...)
	args = append(args, subcmdArgs...)

	var stdoutBuf, stderrBuf bytes.Buffer

	cmd := exec.CommandContext(ctx, "git", args...)
	cmd.Env = c.buildEnv()
	// Set the process working directory when configured.  An empty string means
	// "inherit the parent's CWD", which is the correct default for all git
	// sub-commands except git-submodule (see withDir).
	if c.dir != "" {
		cmd.Dir = c.dir
	}

	if c.stdout != nil {
		cmd.Stdout = c.stdout
	} else {
		cmd.Stdout = &stdoutBuf
	}
	if c.stderr != nil {
		cmd.Stderr = c.stderr
	} else {
		cmd.Stderr = &stderrBuf
	}

	if err := c.runner(ctx, cmd); err != nil {
		return nil, &gitExecError{
			args:   scrubAuthArgs(args),
			stderr: stderrBuf.String(),
			cause:  err,
		}
	}
	return stdoutBuf.Bytes(), nil
}

// buildEnv constructs the minimal environment for a git subprocess.
//
// We do NOT inherit the parent environment wholesale because:
//   - GIT_DIR / GIT_WORK_TREE in the parent could interfere with our explicit
//     --git-dir / --work-tree flags.
//   - User-level git configuration (~/.gitconfig) might alter behaviour or
//     log sensitive information.
//   - Unexpectedly set credential helpers could capture auth tokens.
//
// We explicitly allow through only the variables git actually needs.
func (c *gitCLI) buildEnv() []string {
	env := []string{
		// Mandatory for git to locate system components and SSH.
		"HOME=" + os.Getenv("HOME"),
		"PATH=" + os.Getenv("PATH"),

		// Prevent interactive credential prompts from blocking indefinitely.
		"GIT_TERMINAL_PROMPT=0",
		"GIT_ASKPASS=true",

		// Suppress system and global git configuration so that the operator's
		// ~/.gitconfig and /etc/gitconfig cannot influence behaviour.
		"GIT_CONFIG_NOSYSTEM=1",
		"GIT_CONFIG_GLOBAL=/dev/null",

		// Disable git's built-in credential helper — we inject auth via -c.
		"GIT_CREDENTIAL_HELPER=",
	}

	// SSH configuration: always enforce batch mode and strict host checking.
	if sshCmd := c.buildSSHCommand(); sshCmd != "" {
		env = append(env, "GIT_SSH_COMMAND="+sshCmd)
	} else {
		// Even without a socket, disable interactive prompts for SSH.
		env = append(env,
			"GIT_SSH_COMMAND=ssh -oBatchMode=yes -oStrictHostKeyChecking=yes",
		)
	}

	// Propagate TLS CA bundle locations so HTTPS verification works.
	for _, key := range []string{"SSL_CERT_FILE", "SSL_CERT_DIR", "GIT_SSL_CAINFO"} {
		if v := os.Getenv(key); v != "" {
			env = append(env, key+"="+v)
		}
	}

	// Propagate locale so that git output encoding is predictable and
	// our string-based parsing of ls-remote / rev-parse output is safe.
	if lang := os.Getenv("LANG"); lang != "" {
		env = append(env, "LANG="+lang)
	}
	if lc := os.Getenv("LC_ALL"); lc != "" {
		env = append(env, "LC_ALL="+lc)
	}

	// Propagate TMPDIR so that git can write temporary files.
	if tmp := os.Getenv("TMPDIR"); tmp != "" {
		env = append(env, "TMPDIR="+tmp)
	}

	return env
}

// buildSSHCommand returns the value for GIT_SSH_COMMAND when the CLI has
// an SSH socket or known_hosts file configured.  Returns "" when neither
// is set (the caller should still set a baseline GIT_SSH_COMMAND).
func (c *gitCLI) buildSSHCommand() string {
	if c.sshSocketPath == "" && c.knownHostsPath == "" {
		return ""
	}
	parts := []string{"ssh", "-oBatchMode=yes"}

	if c.sshSocketPath != "" {
		// IdentityAgent points to the forwarded SSH agent socket rather
		// than ~/.ssh/id_* key files, which we never touch.
		parts = append(parts, "-oIdentityAgent="+c.sshSocketPath)
		// Don't use any local identity files; the agent is the only source.
		parts = append(parts, "-oIdentitiesOnly=yes")
	}

	if c.knownHostsPath != "" {
		// Pin the server key to the supplied known_hosts file.
		parts = append(parts,
			"-oUserKnownHostsFile="+c.knownHostsPath,
			"-oStrictHostKeyChecking=yes",
			// Disable global and user known_hosts so only our pinned file is used.
			"-oGlobalKnownHostsFile=/dev/null",
		)
	} else {
		// No pinned hosts: still refuse unknown hosts (no TOFU).
		parts = append(parts, "-oStrictHostKeyChecking=yes")
	}

	return strings.Join(parts, " ")
}

// scrubAuthArgs replaces the value argument that follows "-c" with
// "<redacted>" when the value contains "extraheader" (i.e. auth headers).
// This prevents auth tokens from appearing in error messages.
func scrubAuthArgs(args []string) []string {
	out := make([]string, len(args))
	for i, a := range args {
		if i > 0 && args[i-1] == "-c" && strings.Contains(a, "extraheader") {
			out[i] = "<redacted>"
		} else {
			out[i] = a
		}
	}
	return out
}
