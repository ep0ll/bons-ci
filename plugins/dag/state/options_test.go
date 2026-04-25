package state_test

import (
	"testing"

	"github.com/bons/bons-ci/plugins/dag/ops"
	"github.com/bons/bons-ci/plugins/dag/state"
	"github.com/bons/bons-ci/plugins/dag/vertex"
)

// ─── WithLabel / Labels / Label ───────────────────────────────────────────────

func TestWithLabel(t *testing.T) {
	s := state.Image("alpine:3.18").
		With(state.WithLabel("llb.customname", "Build step"))

	v, ok := s.Label("llb.customname")
	if !ok || v != "Build step" {
		t.Errorf("Label: want (Build step, true), got (%q, %v)", v, ok)
	}
}

func TestWithCustomName(t *testing.T) {
	s := state.Image("alpine:3.18").With(state.WithCustomName("Compile Go"))
	v, ok := s.Label("llb.customname")
	if !ok || v != "Compile Go" {
		t.Errorf("WithCustomName: got (%q, %v)", v, ok)
	}
}

func TestWithDescription(t *testing.T) {
	s := state.Image("alpine:3.18").With(state.WithDescription("Runs unit tests"))
	v, ok := s.Label("llb.description")
	if !ok || v != "Runs unit tests" {
		t.Errorf("WithDescription: got (%q, %v)", v, ok)
	}
}

func TestLabelsReturnsAll(t *testing.T) {
	s := state.Scratch().
		With(state.WithLabel("a", "1")).
		With(state.WithLabel("b", "2")).
		With(state.WithLabel("c", "3"))

	labels := s.Labels()
	if len(labels) != 3 {
		t.Errorf("Labels: want 3, got %d: %v", len(labels), labels)
	}
	for k, want := range map[string]string{"a": "1", "b": "2", "c": "3"} {
		if got := labels[k]; got != want {
			t.Errorf("labels[%q]: want %q, got %q", k, want, got)
		}
	}
}

func TestLabelsNilWhenEmpty(t *testing.T) {
	s := state.Scratch()
	if s.Labels() != nil {
		t.Error("Labels() must be nil for a state with no labels")
	}
}

func TestLabelMissing(t *testing.T) {
	s := state.Scratch()
	_, ok := s.Label("nothere")
	if ok {
		t.Error("Label(missing) must return ok=false")
	}
}

func TestLabelImmutability(t *testing.T) {
	base := state.Image("alpine:3.18").With(state.WithLabel("x", "1"))
	_ = base.With(state.WithLabel("y", "2"))
	// base must not have "y".
	_, ok := base.Label("y")
	if ok {
		t.Error("adding a label to a derived state must not mutate base")
	}
}

func TestLabelMutatedMapDoesNotAffectState(t *testing.T) {
	s := state.Image("alpine:3.18").With(state.WithLabel("k", "v"))
	labels := s.Labels()
	labels["k"] = "mutated"
	// State must be unchanged.
	v, _ := s.Label("k")
	if v != "v" {
		t.Errorf("mutating returned labels map must not affect state: got %q", v)
	}
}

func TestLabelOverride(t *testing.T) {
	s := state.Image("alpine:3.18").
		With(state.WithLabel("env", "dev")).
		With(state.WithLabel("env", "prod"))
	v, ok := s.Label("env")
	if !ok || v != "prod" {
		t.Errorf("label override: want (prod, true), got (%q, %v)", v, ok)
	}
}

// ─── With() composition ────────────────────────────────────────────────────────

func TestWithComposesInOrder(t *testing.T) {
	var calls []string
	a := state.StateOption(func(s state.State) state.State {
		calls = append(calls, "a")
		return s.AddEnv("FROM_A", "yes")
	})
	b := state.StateOption(func(s state.State) state.State {
		calls = append(calls, "b")
		return s.AddEnv("FROM_B", "yes")
	})

	s := state.Scratch().With(a, b)
	if len(calls) != 2 || calls[0] != "a" || calls[1] != "b" {
		t.Errorf("With must apply options in order: got %v", calls)
	}
	_, okA := s.GetEnv("FROM_A")
	_, okB := s.GetEnv("FROM_B")
	if !okA || !okB {
		t.Error("both options must take effect")
	}
}

func TestWithEmpty(t *testing.T) {
	s := state.Image("alpine:3.18")
	s2 := s.With()
	// With no options is identity.
	if s.Ref() != s2.Ref() {
		t.Error("With() with no options must return equivalent state")
	}
}

// ─── Compose ──────────────────────────────────────────────────────────────────

func TestCompose(t *testing.T) {
	goEnv := state.Compose(
		state.WithLabel("lang", "go"),
		state.WithEnvMap(map[string]string{"CGO_ENABLED": "0", "GOFLAGS": "-mod=vendor"}),
		state.WithPlatformStr("linux", "amd64", ""),
	)

	s := state.Image("golang:1.22").With(goEnv)

	v, ok := s.Label("lang")
	if !ok || v != "go" {
		t.Errorf("compose label: got (%q, %v)", v, ok)
	}
	cgo, _ := s.GetEnv("CGO_ENABLED")
	if cgo != "0" {
		t.Errorf("CGO_ENABLED: want 0, got %q", cgo)
	}
	if s.GetPlatform() == nil || s.GetPlatform().Architecture != "amd64" {
		t.Errorf("platform: want amd64, got %v", s.GetPlatform())
	}
}

func TestComposeEmpty(t *testing.T) {
	noop := state.Compose()
	s := state.Scratch().With(noop)
	if !s.Ref().IsZero() {
		t.Error("Compose() with no options applied to Scratch must remain Scratch")
	}
}

// ─── Conditional ──────────────────────────────────────────────────────────────

func TestConditionalTrue(t *testing.T) {
	opt := state.Conditional(true, state.WithLabel("enabled", "yes"))
	s := state.Scratch().With(opt)
	_, ok := s.Label("enabled")
	if !ok {
		t.Error("Conditional(true): label must be set")
	}
}

func TestConditionalFalse(t *testing.T) {
	opt := state.Conditional(false, state.WithLabel("enabled", "yes"))
	s := state.Scratch().With(opt)
	_, ok := s.Label("enabled")
	if ok {
		t.Error("Conditional(false): label must NOT be set")
	}
}

// ─── Platform options ─────────────────────────────────────────────────────────

func TestPlatformVars(t *testing.T) {
	cases := []struct {
		opt  state.StateOption
		os   string
		arch string
	}{
		{state.LinuxAmd64, "linux", "amd64"},
		{state.LinuxArm64, "linux", "arm64"},
		{state.LinuxArm, "linux", "arm"},
		{state.Windows, "windows", "amd64"},
		{state.Darwin, "darwin", "amd64"},
	}
	for _, tc := range cases {
		s := state.Scratch().With(tc.opt)
		p := s.GetPlatform()
		if p == nil {
			t.Errorf("platform opt %s/%s: platform not set", tc.os, tc.arch)
			continue
		}
		if p.OS != tc.os || p.Architecture != tc.arch {
			t.Errorf("platform: want %s/%s, got %s/%s", tc.os, tc.arch, p.OS, p.Architecture)
		}
	}
}

func TestWithPlatformStr(t *testing.T) {
	opt := state.WithPlatformStr("linux", "riscv64", "")
	s := state.Scratch().With(opt)
	p := s.GetPlatform()
	if p == nil || p.Architecture != "riscv64" {
		t.Errorf("WithPlatformStr: want riscv64, got %v", p)
	}
}

// ─── WithEnvMap / WithEnvSlice ────────────────────────────────────────────────

func TestWithEnvMap(t *testing.T) {
	s := state.Scratch().With(state.WithEnvMap(map[string]string{
		"A": "1",
		"B": "2",
	}))
	for k, want := range map[string]string{"A": "1", "B": "2"} {
		got, ok := s.GetEnv(k)
		if !ok || got != want {
			t.Errorf("WithEnvMap[%s]: want (%q, true), got (%q, %v)", k, want, got, ok)
		}
	}
}

func TestWithEnvMapEmpty(t *testing.T) {
	s := state.Scratch().With(state.WithEnvMap(nil))
	if len(s.Env()) != 0 {
		t.Error("WithEnvMap(nil) must not add any variables")
	}
}

func TestWithEnvSlice(t *testing.T) {
	s := state.Scratch().With(state.WithEnvSlice([]string{
		"GOOS=linux",
		"GOARCH=amd64",
		"invalid_no_equals",
		"GOFLAGS=-mod=vendor", // value contains =
	}))
	for k, want := range map[string]string{
		"GOOS":    "linux",
		"GOARCH":  "amd64",
		"GOFLAGS": "-mod=vendor",
	} {
		got, ok := s.GetEnv(k)
		if !ok || got != want {
			t.Errorf("WithEnvSlice[%s]: want (%q, true), got (%q, %v)", k, want, got, ok)
		}
	}
	// "invalid_no_equals" must be silently skipped.
	_, ok := s.GetEnv("invalid_no_equals")
	if ok {
		t.Error("entries without '=' must be skipped")
	}
}

func TestWithEnvSliceEmpty(t *testing.T) {
	s := state.Scratch().With(state.WithEnvSlice(nil))
	if len(s.Env()) != 0 {
		t.Error("WithEnvSlice(nil) must not add variables")
	}
}

// ─── Network options ──────────────────────────────────────────────────────────

func TestNetworkOptions(t *testing.T) {
	cases := []struct {
		opt  state.StateOption
		want ops.NetMode
	}{
		{state.WithNetworkHost, ops.NetModeHost},
		{state.WithNetworkNone, ops.NetModeNone},
		{state.WithNetworkSandbox, ops.NetModeSandbox},
	}
	for _, tc := range cases {
		s := state.Scratch().With(tc.opt)
		if got := s.GetNetwork(); got != tc.want {
			t.Errorf("network option: want %d, got %d", tc.want, got)
		}
	}
}

// ─── Mount helpers ─────────────────────────────────────────────────────────────

func TestReadonlyMount(t *testing.T) {
	src := state.Image("alpine:3.18")
	m := state.ReadonlyMount("/mnt", src.Ref())
	if m.Target != "/mnt" {
		t.Errorf("target: want /mnt, got %q", m.Target)
	}
	if !m.Readonly {
		t.Error("ReadonlyMount must be readonly")
	}
	if m.Source.Vertex == nil {
		t.Error("source must not be nil")
	}
}

func TestWritableMount(t *testing.T) {
	src := state.Image("alpine:3.18")
	m := state.WritableMount("/out", src.Ref())
	if m.Readonly {
		t.Error("WritableMount must not be readonly")
	}
	if m.Target != "/out" {
		t.Errorf("target: want /out, got %q", m.Target)
	}
}

func TestScratchMount(t *testing.T) {
	m := state.ScratchMount("/work")
	if !m.Source.IsZero() {
		t.Error("ScratchMount source must be zero (scratch)")
	}
	if m.Readonly {
		t.Error("ScratchMount must not be readonly")
	}
	if m.Target != "/work" {
		t.Errorf("target: want /work, got %q", m.Target)
	}
}

func TestCacheMount(t *testing.T) {
	m := state.CacheMount("/cache", "go-mod", ops.CacheSharingShared)
	if m.Type != ops.MountTypeCache {
		t.Errorf("type: want cache, got %d", m.Type)
	}
	if m.CacheID != "go-mod" {
		t.Errorf("cache ID: want go-mod, got %q", m.CacheID)
	}
	if m.CacheSharing != ops.CacheSharingShared {
		t.Errorf("sharing: want shared, got %d", m.CacheSharing)
	}
}

func TestTmpfsMount(t *testing.T) {
	m := state.TmpfsMount("/tmp", 64*1024*1024)
	if m.Type != ops.MountTypeTmpfs {
		t.Errorf("type: want tmpfs, got %d", m.Type)
	}
	if m.TmpfsSize != 64*1024*1024 {
		t.Errorf("size: want 64MB, got %d", m.TmpfsSize)
	}
	if m.Target != "/tmp" {
		t.Errorf("target: want /tmp, got %q", m.Target)
	}
}

func TestTmpfsMountUnlimited(t *testing.T) {
	m := state.TmpfsMount("/tmp", 0)
	if m.TmpfsSize != 0 {
		t.Errorf("unlimited tmpfs: size must be 0, got %d", m.TmpfsSize)
	}
}

// ─── LinuxArm64V8 ─────────────────────────────────────────────────────────────

func TestLinuxArm64V8(t *testing.T) {
	s := state.Scratch().With(state.LinuxArm64V8)
	p := s.GetPlatform()
	if p == nil {
		t.Fatal("platform must be set")
	}
	if p.Architecture != "arm64" {
		t.Errorf("arch: want arm64, got %s", p.Architecture)
	}
	if p.Variant != "v8" {
		t.Errorf("variant: want v8, got %s", p.Variant)
	}
}

// ─── StateOption integration with File / Merge ────────────────────────────────

func TestStateOptionWithFile(t *testing.T) {
	// StateOption should work across all State constructors.
	tag := state.WithLabel("op", "file")
	s := state.Image("alpine:3.18").
		File(ops.Mkdir("/app", 0755)).
		With(tag)

	v, ok := s.Label("op")
	if !ok || v != "file" {
		t.Errorf("label after File: got (%q, %v)", v, ok)
	}
}

func TestStateOptionWithMerge(t *testing.T) {
	a := state.Image("alpine:3.18")
	b := state.Image("busybox:latest")
	merged := state.Merge([]state.State{a, b}, ops.Constraints{}).
		With(state.WithCustomName("merged images"))

	v, ok := merged.Label("llb.customname")
	if !ok || v != "merged images" {
		t.Errorf("label after Merge: got (%q, %v)", v, ok)
	}
}

// ─── State.Ref() IsZero coverage ─────────────────────────────────────────────

func TestScratchRefIsZero(t *testing.T) {
	if !state.Scratch().Ref().IsZero() {
		t.Error("Scratch().Ref() must be zero")
	}
}

func TestScratchOutputNil(t *testing.T) {
	if state.Scratch().Output() != nil {
		t.Error("Scratch().Output() must be nil")
	}
}

// ─── vertex.Ref.String exhaustive ─────────────────────────────────────────────

func TestVertexRefStringZeroValue(t *testing.T) {
	r := vertex.Ref{}
	if r.String() != "<scratch>" {
		t.Errorf("zero Ref.String: want <scratch>, got %q", r.String())
	}
}

func TestVertexRefStringIsZeroFalse(t *testing.T) {
	src := state.Image("alpine:3.18")
	r := src.Ref()
	if r.IsZero() {
		t.Error("image ref must not be zero")
	}
	s := r.String()
	if s == "" || s == "<scratch>" {
		t.Errorf("non-zero Ref.String must not be empty or <scratch>, got %q", s)
	}
}
