package state_test

import (
	"context"
	"testing"

	"github.com/bons/bons-ci/plugins/dag/ops"
	"github.com/bons/bons-ci/plugins/dag/state"
	"github.com/bons/bons-ci/plugins/dag/vertex"
)

func TestScratch(t *testing.T) {
	s := state.Scratch()
	if s.Ref() != (vertex.Ref{}) {
		t.Error("Scratch state must have a zero ref")
	}
	if s.Output() != nil {
		t.Error("Scratch state output must be nil")
	}
}

func TestImageState(t *testing.T) {
	s := state.Image("alpine:3.18")
	if s.Output() == nil {
		t.Error("Image state must have a non-nil output")
	}
	if s.Output().Kind() != vertex.KindSource {
		t.Errorf("image output kind: want source, got %s", s.Output().Kind())
	}
}

func TestDirAbsolute(t *testing.T) {
	s := state.Image("alpine").Dir("/workspace")
	dir, err := s.GetDir(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if dir != "/workspace" {
		t.Errorf("dir: want /workspace, got %q", dir)
	}
}

func TestDirRelative(t *testing.T) {
	s := state.Image("alpine").Dir("/base").Dir("sub")
	dir, _ := s.GetDir(context.Background())
	if dir != "/base/sub" {
		t.Errorf("relative dir: want /base/sub, got %q", dir)
	}
}

func TestDirChaining(t *testing.T) {
	s := state.Image("alpine").Dir("/a").Dir("b").Dir("../c")
	dir, _ := s.GetDir(context.Background())
	if dir != "/a/c" {
		t.Errorf("dir chaining: want /a/c, got %q", dir)
	}
}

func TestEnvAddAndGet(t *testing.T) {
	s := state.Image("alpine").AddEnv("FOO", "bar").AddEnv("BAZ", "qux")
	v, ok := s.GetEnv("FOO")
	if !ok || v != "bar" {
		t.Errorf("FOO: want bar, got %q (found=%v)", v, ok)
	}
	v, ok = s.GetEnv("BAZ")
	if !ok || v != "qux" {
		t.Errorf("BAZ: want qux, got %q (found=%v)", v, ok)
	}
}

func TestEnvOverride(t *testing.T) {
	s := state.Image("alpine").AddEnv("KEY", "first").AddEnv("KEY", "second")
	v, _ := s.GetEnv("KEY")
	if v != "second" {
		t.Errorf("env override: want second, got %q", v)
	}
}

func TestEnvMissing(t *testing.T) {
	s := state.Image("alpine")
	_, ok := s.GetEnv("NOTSET")
	if ok {
		t.Error("missing env should return ok=false")
	}
}

func TestEnvToSlice(t *testing.T) {
	s := state.Image("alpine").AddEnv("A", "1").AddEnv("B", "2")
	env := s.Env()
	if len(env) != 2 {
		t.Errorf("expected 2 env vars, got %v", env)
	}
}

func TestUserSetting(t *testing.T) {
	s := state.Image("alpine").User("nobody")
	if s.GetUser() != "nobody" {
		t.Errorf("user: want nobody, got %q", s.GetUser())
	}
}

func TestNetworkSetting(t *testing.T) {
	s := state.Image("alpine").Network(ops.NetModeHost)
	// Just verify it doesn't panic and the state is valid.
	if s.Output() == nil {
		t.Error("network state must still have output")
	}
}

func TestPlatformSetting(t *testing.T) {
	p := ops.Platform{OS: "linux", Architecture: "arm64"}
	s := state.Image("alpine").Platform(p)
	got := s.GetPlatform()
	if got == nil || got.Architecture != "arm64" {
		t.Errorf("platform: want arm64, got %v", got)
	}
}

func TestStateImmutability(t *testing.T) {
	// Operations on a State must not mutate it.
	base := state.Image("alpine")
	_ = base.AddEnv("X", "1")
	_ = base.Dir("/work")

	// base must be unchanged.
	_, ok := base.GetEnv("X")
	if ok {
		t.Error("AddEnv must not mutate the receiver")
	}
	dir, _ := base.GetDir(context.Background())
	if dir != "/" {
		t.Errorf("Dir must not mutate the receiver, got %q", dir)
	}
}

func TestRunProducesNewState(t *testing.T) {
	s := state.Image("alpine").Dir("/app")
	rs := s.Run(
		ops.WithArgs("echo", "hello"),
		ops.WithCwd("/app"),
	)
	root := rs.Root()
	if root.Output() == nil {
		t.Error("exec root must have output")
	}
	if root.Output().Kind() != vertex.KindExec {
		t.Errorf("exec output kind: want exec, got %s", root.Output().Kind())
	}
}

func TestRunInheritsWorkingDir(t *testing.T) {
	// When state has a working dir, Run should use it unless overridden.
	s := state.Image("alpine").Dir("/workspace")
	rs := s.Run(ops.WithArgs("ls"))
	// The exec op must have cwd = /workspace (propagated from state).
	e, ok := rs.Root().Output().(*ops.ExecOp)
	if !ok {
		t.Fatalf("expected *ops.ExecOp, got %T", rs.Root().Output())
	}
	if e.Meta().Cwd != "/workspace" {
		t.Errorf("exec cwd: want /workspace, got %q", e.Meta().Cwd)
	}
}

func TestRunInheritsEnv(t *testing.T) {
	s := state.Image("alpine").AddEnv("MY_VAR", "42")
	rs := s.Run(ops.WithArgs("printenv", "MY_VAR"))
	e, ok := rs.Root().Output().(*ops.ExecOp)
	if !ok {
		t.Fatalf("expected *ops.ExecOp, got %T", rs.Root().Output())
	}
	var found bool
	for _, kv := range e.Meta().Env {
		if kv == "MY_VAR=42" {
			found = true
		}
	}
	if !found {
		t.Errorf("env var MY_VAR=42 not propagated to exec: %v", e.Meta().Env)
	}
}

func TestFileOp(t *testing.T) {
	s := state.Image("alpine").Dir("/").
		File(ops.Mkdir("/data", 0755))
	if s.Output() == nil {
		t.Error("file op must produce output")
	}
	if s.Output().Kind() != vertex.KindFile {
		t.Errorf("file op kind: want file, got %s", s.Output().Kind())
	}
}

func TestMergeState(t *testing.T) {
	a := state.Image("alpine:3.18")
	b := state.Image("busybox:latest")
	merged := state.Merge([]state.State{a, b}, ops.Constraints{})
	if merged.Output() == nil {
		t.Error("merged state must have output")
	}
	if merged.Output().Kind() != vertex.KindMerge {
		t.Errorf("merge output kind: want merge, got %s", merged.Output().Kind())
	}
}

func TestMergeWithScratch(t *testing.T) {
	// Merge(scratch, image) = image.
	img := state.Image("alpine")
	merged := state.Merge([]state.State{state.Scratch(), img}, ops.Constraints{})
	if merged.Output() != img.Output() {
		t.Error("Merge with one scratch should return the non-scratch input")
	}
}

func TestDiffState(t *testing.T) {
	lower := state.Image("alpine:3.18")
	upper := state.Image("alpine:edge")
	diff := lower.Diff(upper, ops.Constraints{})
	if diff.Output() == nil {
		t.Error("diff must produce output")
	}
	if diff.Output().Kind() != vertex.KindDiff {
		t.Errorf("diff kind: want diff, got %s", diff.Output().Kind())
	}
}

func TestGitState(t *testing.T) {
	s := state.Git("https://github.com/foo/bar.git", ops.WithGitRef("main"))
	if s.Output() == nil || s.Output().Kind() != vertex.KindSource {
		t.Error("git state must have a source vertex")
	}
}

func TestHTTPState(t *testing.T) {
	s := state.HTTP("https://example.com/file.tar.gz")
	if s.Output() == nil || s.Output().Kind() != vertex.KindSource {
		t.Error("http state must have a source vertex")
	}
}

func TestLocalState(t *testing.T) {
	s := state.Local("my-context")
	if s.Output() == nil || s.Output().Kind() != vertex.KindSource {
		t.Error("local state must have a source vertex")
	}
}

func TestWithOutput(t *testing.T) {
	img := state.Image("alpine:3.18")
	extra := state.Image("busybox:latest")
	replaced := img.WithOutput(extra.Ref())
	if replaced.Output() != extra.Output() {
		t.Error("WithOutput must replace the underlying vertex")
	}
}
