package gitapply

import (
	"strings"
	"testing"
)

// ─── buildEnv ─────────────────────────────────────────────────────────────────

func TestGitCLI_buildEnv_containsMandatoryVars(t *testing.T) {
	t.Parallel()
	cli := newGitCLI(DefaultProcessRunner)
	env := cli.buildEnv()
	env = append(env) // shallow copy to avoid any mutation concerns

	mandatory := []string{
		"GIT_TERMINAL_PROMPT=0",
		"GIT_ASKPASS=true",
		"GIT_CONFIG_NOSYSTEM=1",
		"GIT_CONFIG_GLOBAL=/dev/null",
		"GIT_CREDENTIAL_HELPER=",
	}
	for _, want := range mandatory {
		found := false
		for _, e := range env {
			if e == want {
				found = true
				break
			}
		}
		if !found {
			t.Errorf("env should contain %q; full env: %v", want, env)
		}
	}
}

func TestGitCLI_buildEnv_noParentGitVars(t *testing.T) {
	t.Parallel()
	// Variables that must NOT leak from the parent environment.
	forbidden := []string{"GIT_DIR=", "GIT_WORK_TREE=", "GIT_AUTHOR_", "GIT_COMMITTER_"}
	cli := newGitCLI(DefaultProcessRunner)
	for _, e := range cli.buildEnv() {
		for _, f := range forbidden {
			if strings.HasPrefix(e, f) {
				t.Errorf("forbidden env var leaked into child env: %q", e)
			}
		}
	}
}

func TestGitCLI_buildEnv_sshFallback(t *testing.T) {
	t.Parallel()
	// Without a socket or known_hosts, GIT_SSH_COMMAND should still enforce
	// batch mode and strict host checking.
	cli := newGitCLI(DefaultProcessRunner)
	env := cli.buildEnv()

	var sshCmd string
	for _, e := range env {
		if strings.HasPrefix(e, "GIT_SSH_COMMAND=") {
			sshCmd = strings.TrimPrefix(e, "GIT_SSH_COMMAND=")
			break
		}
	}
	if sshCmd == "" {
		t.Fatal("GIT_SSH_COMMAND must always be set")
	}
	if !strings.Contains(sshCmd, "-oBatchMode=yes") {
		t.Errorf("GIT_SSH_COMMAND must include -oBatchMode=yes; got %q", sshCmd)
	}
	if !strings.Contains(sshCmd, "-oStrictHostKeyChecking=yes") {
		t.Errorf("GIT_SSH_COMMAND must include -oStrictHostKeyChecking=yes; got %q", sshCmd)
	}
}

// ─── buildSSHCommand ──────────────────────────────────────────────────────────

func TestGitCLI_buildSSHCommand_withSocket(t *testing.T) {
	t.Parallel()
	cli := newGitCLI(DefaultProcessRunner, withSSHSocket("/run/ssh-agent.sock"))
	cmd := cli.buildSSHCommand()

	if cmd == "" {
		t.Fatal("expected non-empty SSH command when socket is set")
	}
	if !strings.Contains(cmd, "-oIdentityAgent=/run/ssh-agent.sock") {
		t.Errorf("SSH command should include IdentityAgent; got %q", cmd)
	}
	if !strings.Contains(cmd, "-oIdentitiesOnly=yes") {
		t.Errorf("SSH command should include IdentitiesOnly; got %q", cmd)
	}
	if !strings.Contains(cmd, "-oBatchMode=yes") {
		t.Errorf("SSH command should include BatchMode; got %q", cmd)
	}
}

func TestGitCLI_buildSSHCommand_withKnownHosts(t *testing.T) {
	t.Parallel()
	cli := newGitCLI(DefaultProcessRunner, withKnownHosts("/tmp/known_hosts-abc123"))
	cmd := cli.buildSSHCommand()

	if !strings.Contains(cmd, "-oUserKnownHostsFile=/tmp/known_hosts-abc123") {
		t.Errorf("SSH command should pin known_hosts; got %q", cmd)
	}
	if !strings.Contains(cmd, "-oStrictHostKeyChecking=yes") {
		t.Errorf("SSH command must enforce StrictHostChecking; got %q", cmd)
	}
	if !strings.Contains(cmd, "-oGlobalKnownHostsFile=/dev/null") {
		t.Errorf("SSH command must null out global known_hosts; got %q", cmd)
	}
}

func TestGitCLI_buildSSHCommand_withBoth(t *testing.T) {
	t.Parallel()
	cli := newGitCLI(DefaultProcessRunner,
		withSSHSocket("/run/ssh-agent.sock"),
		withKnownHosts("/tmp/kh"),
	)
	cmd := cli.buildSSHCommand()
	if !strings.Contains(cmd, "IdentityAgent") {
		t.Error("expected IdentityAgent when socket set")
	}
	if !strings.Contains(cmd, "UserKnownHostsFile") {
		t.Error("expected UserKnownHostsFile when known_hosts set")
	}
}

func TestGitCLI_buildSSHCommand_empty(t *testing.T) {
	t.Parallel()
	cli := newGitCLI(DefaultProcessRunner)
	if cli.buildSSHCommand() != "" {
		t.Error("buildSSHCommand should return empty when neither socket nor known_hosts is set")
	}
}

// ─── with (copy semantics) ────────────────────────────────────────────────────

func TestGitCLI_with_doesNotMutateParent(t *testing.T) {
	t.Parallel()
	parent := newGitCLI(DefaultProcessRunner,
		withExtraConfigArgs("-c", "protocol.version=2"),
	)
	original := make([]string, len(parent.extraConfigArgs))
	copy(original, parent.extraConfigArgs)

	// Adding extra config to a derived instance must not change parent.
	_ = parent.with(withExtraConfigArgs("-c", "http.sslVerify=false"))

	if len(parent.extraConfigArgs) != len(original) {
		t.Errorf("parent.extraConfigArgs was mutated: before %v, after %v",
			original, parent.extraConfigArgs)
	}
}

// ─── gitBaseConfig ────────────────────────────────────────────────────────────

func TestGitBaseConfig_containsSecurityDefaults(t *testing.T) {
	t.Parallel()
	raw := strings.Join(gitBaseConfig, " ")
	if !strings.Contains(raw, "core.askPass=true") {
		t.Error("gitBaseConfig must disable interactive credential prompts")
	}
	if !strings.Contains(raw, "protocol.version=2") {
		t.Error("gitBaseConfig must request protocol v2")
	}
}
