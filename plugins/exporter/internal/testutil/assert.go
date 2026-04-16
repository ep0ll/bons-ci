package testutil

import "testing"

// Equal fails if got != want.
func Equal[T comparable](t *testing.T, got, want T, msg ...string) {
	t.Helper()
	if got != want {
		label := ""
		if len(msg) > 0 { label = msg[0] + ": " }
		t.Errorf("%sgot %v, want %v", label, got, want)
	}
}

// NotEqual fails if got == want.
func NotEqual[T comparable](t *testing.T, got, notWant T, msg ...string) {
	t.Helper()
	if got == notWant {
		label := ""
		if len(msg) > 0 { label = msg[0] + ": " }
		t.Errorf("%sunexpected equal value: %v", label, got)
	}
}

// True fails if cond is false.
func True(t *testing.T, cond bool, msg string) {
	t.Helper()
	if !cond { t.Error(msg) }
}

// False fails if cond is true.
func False(t *testing.T, cond bool, msg string) {
	t.Helper()
	if cond { t.Error(msg) }
}

// Nil fails if v is not nil.
func Nil(t *testing.T, v any, msg string) {
	t.Helper()
	if v != nil { t.Errorf("%s: expected nil, got %v", msg, v) }
}

// NotNil fails if v is nil.
func NotNil(t *testing.T, v any, msg string) {
	t.Helper()
	if v == nil { t.Errorf("%s: expected non-nil", msg) }
}

// NoError fails if err != nil.
func NoError(t *testing.T, err error, msg ...string) {
	t.Helper()
	if err != nil {
		label := "unexpected error"
		if len(msg) > 0 { label = msg[0] }
		t.Fatalf("%s: %v", label, err)
	}
}

// Error fails if err == nil.
func Error(t *testing.T, err error, msg string) {
	t.Helper()
	if err == nil { t.Errorf("%s: expected non-nil error", msg) }
}

// ErrorIs fails if !errors.Is(err, target).
func ErrorIs(t *testing.T, err, target error, msg string) {
	t.Helper()
	if !isErr(err, target) { t.Errorf("%s: error chain does not contain %v; got %v", msg, target, err) }
}

func isErr(err, target error) bool {
	if err == nil { return false }
	type unwrapper interface{ Unwrap() error }
	for {
		if err == target { return true }
		if u, ok := err.(unwrapper); ok { err = u.Unwrap() } else { return false }
	}
}

// Contains checks if a string contains a substring.
func Contains(t *testing.T, s, substr string, msg string) {
	t.Helper()
	if len(s) < len(substr) || !containsStr(s, substr) {
		t.Errorf("%s: %q does not contain %q", msg, s, substr)
	}
}

func containsStr(s, sub string) bool {
	for i := 0; i <= len(s)-len(sub); i++ {
		if s[i:i+len(sub)] == sub { return true }
	}
	return false
}

// Len checks slice/map length.
func Len[T any](t *testing.T, s []T, want int, msg string) {
	t.Helper()
	if len(s) != want { t.Errorf("%s: len=%d, want %d", msg, len(s), want) }
}
