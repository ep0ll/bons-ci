package state_test

import (
	"testing"

	"github.com/bons/bons-ci/plugins/dag/state"
)

// EnvList is exposed via State's AddEnv / GetEnv / Env surface.
// These tests exercise all corner cases through the State API.

func TestEnvListEmpty(t *testing.T) {
	s := state.Scratch()
	env := s.Env()
	if len(env) != 0 {
		t.Errorf("fresh state: expected no env vars, got %v", env)
	}
}

func TestEnvListSet(t *testing.T) {
	s := state.Scratch().AddEnv("A", "1")
	v, ok := s.GetEnv("A")
	if !ok || v != "1" {
		t.Errorf("Set: want A=1, got %q ok=%v", v, ok)
	}
}

func TestEnvListMultiple(t *testing.T) {
	s := state.Scratch().
		AddEnv("A", "1").
		AddEnv("B", "2").
		AddEnv("C", "3")

	for _, want := range [][2]string{{"A", "1"}, {"B", "2"}, {"C", "3"}} {
		v, ok := s.GetEnv(want[0])
		if !ok || v != want[1] {
			t.Errorf("env %s: want %s, got %q ok=%v", want[0], want[1], v, ok)
		}
	}
}

func TestEnvListOverride(t *testing.T) {
	s := state.Scratch().
		AddEnv("KEY", "first").
		AddEnv("KEY", "second").
		AddEnv("KEY", "third")

	v, _ := s.GetEnv("KEY")
	if v != "third" {
		t.Errorf("override: want third, got %q", v)
	}
}

func TestEnvListToSliceFormat(t *testing.T) {
	s := state.Scratch().
		AddEnv("HELLO", "world").
		AddEnv("FOO", "bar")

	env := s.Env()
	found := map[string]bool{}
	for _, kv := range env {
		found[kv] = true
	}
	if !found["HELLO=world"] {
		t.Errorf("Env() missing HELLO=world: %v", env)
	}
	if !found["FOO=bar"] {
		t.Errorf("Env() missing FOO=bar: %v", env)
	}
}

func TestEnvListMissing(t *testing.T) {
	s := state.Scratch().AddEnv("A", "1")
	_, ok := s.GetEnv("MISSING")
	if ok {
		t.Error("missing key: expected ok=false")
	}
}

func TestEnvListEmptyValue(t *testing.T) {
	s := state.Scratch().AddEnv("EMPTY", "")
	v, ok := s.GetEnv("EMPTY")
	if !ok {
		t.Error("empty-string value should still be findable")
	}
	if v != "" {
		t.Errorf("empty-string value: want empty, got %q", v)
	}
}

func TestEnvListImmutability(t *testing.T) {
	// Intermediate states should not see later additions.
	base := state.Scratch().AddEnv("A", "1")
	later := base.AddEnv("B", "2")

	// base should NOT see B.
	_, ok := base.GetEnv("B")
	if ok {
		t.Error("base state should not see env vars added in derived state")
	}
	// later should see both.
	_, ok = later.GetEnv("A")
	if !ok {
		t.Error("derived state should see parent env vars")
	}
	_, ok = later.GetEnv("B")
	if !ok {
		t.Error("derived state should see its own env vars")
	}
}

func TestEnvListLargeNumber(t *testing.T) {
	s := state.Scratch()
	for i := 0; i < 200; i++ {
		s = s.AddEnv("VAR_"+itoa(i), itoa(i))
	}
	env := s.Env()
	if len(env) != 200 {
		t.Errorf("large env list: want 200, got %d", len(env))
	}
	for i := 0; i < 200; i++ {
		v, ok := s.GetEnv("VAR_" + itoa(i))
		if !ok || v != itoa(i) {
			t.Errorf("VAR_%d: want %d, got %q ok=%v", i, i, v, ok)
		}
	}
}

func TestEnvListValueContainsEquals(t *testing.T) {
	// Values that contain "=" must be preserved intact.
	s := state.Scratch().AddEnv("PATH", "/usr/bin:/bin")
	v, ok := s.GetEnv("PATH")
	if !ok || v != "/usr/bin:/bin" {
		t.Errorf("value with colon: want /usr/bin:/bin, got %q", v)
	}
}

func TestEnvInheritedAcrossRun(t *testing.T) {
	// Env set on a State must be visible in derived States and Run calls.
	s := state.Image("alpine").AddEnv("BUILD_ENV", "production")
	derived := s.AddEnv("EXTRA", "yes")

	v, ok := derived.GetEnv("BUILD_ENV")
	if !ok || v != "production" {
		t.Errorf("inherited env: want production, got %q ok=%v", v, ok)
	}
}

// itoa converts int to string without importing strconv.
func itoa(n int) string {
	if n == 0 {
		return "0"
	}
	buf := make([]byte, 0, 10)
	for n > 0 {
		buf = append(buf, byte('0'+n%10))
		n /= 10
	}
	// reverse
	for i, j := 0, len(buf)-1; i < j; i, j = i+1, j-1 {
		buf[i], buf[j] = buf[j], buf[i]
	}
	return string(buf)
}
