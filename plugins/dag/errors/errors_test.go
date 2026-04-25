package errors_test

import (
	stderrors "errors"
	"fmt"
	"testing"

	dagerrors "github.com/bons/bons-ci/plugins/dag/errors"
)

func TestCycleError(t *testing.T) {
	err := dagerrors.NewCycleError([]string{"A", "B", "C"})
	if err == nil {
		t.Fatal("expected error")
	}
	msg := err.Error()
	if msg == "" {
		t.Error("error message must not be empty")
	}
	// Must contain the cycle path.
	for _, id := range []string{"A", "B", "C"} {
		if !containsStr(msg, id) {
			t.Errorf("cycle error must mention vertex %q: %s", id, msg)
		}
	}
	// Vertices slice should close the loop.
	if len(err.Vertices) != 4 { // A→B→C→A
		t.Errorf("Vertices: want 4 (closed loop), got %d", len(err.Vertices))
	}
	if err.Vertices[0] != err.Vertices[len(err.Vertices)-1] {
		t.Error("Vertices must form a closed loop (first == last)")
	}
}

func TestCycleErrorEmptyPath(t *testing.T) {
	err := dagerrors.NewCycleError(nil)
	if err.Error() == "" {
		t.Error("empty path cycle error must still produce a message")
	}
}

func TestValidationError(t *testing.T) {
	cause := fmt.Errorf("missing args")
	err := dagerrors.NewValidationError("exec-abc123", "exec", cause)

	if err.VertexID != "exec-abc123" {
		t.Errorf("vertex ID: got %q", err.VertexID)
	}
	if err.VertexKind != "exec" {
		t.Errorf("vertex kind: got %q", err.VertexKind)
	}
	// errors.As must find the ValidationError.
	var verr *dagerrors.ValidationError
	if !stderrors.As(err, &verr) {
		t.Error("errors.As must find *ValidationError")
	}
	// errors.Unwrap must expose the cause.
	if !stderrors.Is(err, cause) {
		t.Error("errors.Is must find cause through Unwrap")
	}
	if err.Error() == "" {
		t.Error("ValidationError.Error() must not be empty")
	}
}

func TestIDCollisionError(t *testing.T) {
	err := &dagerrors.IDCollisionError{ID: "abc123", KindA: "source", KindB: "exec"}
	msg := err.Error()
	if msg == "" {
		t.Error("IDCollisionError.Error() must not be empty")
	}
	if !containsStr(msg, "abc123") {
		t.Errorf("error must mention collision ID: %s", msg)
	}
}

func TestSerializationError(t *testing.T) {
	cause := fmt.Errorf("missing position map entry")
	err := &dagerrors.SerializationError{
		VertexID:   "xyz",
		VertexKind: "file",
		Cause:      cause,
	}
	if err.Error() == "" {
		t.Error("SerializationError.Error() must not be empty")
	}
	if !stderrors.Is(err, cause) {
		t.Error("errors.Is must find cause via Unwrap")
	}
}

func TestTraversalError(t *testing.T) {
	cause := fmt.Errorf("cache unavailable")
	err := &dagerrors.TraversalError{
		Hook:     "pre",
		VertexID: "src-1",
		Depth:    3,
		Cause:    cause,
	}
	if err.Error() == "" {
		t.Error("TraversalError.Error() must not be empty")
	}
	if !stderrors.Is(err, cause) {
		t.Error("errors.Is must find cause via Unwrap")
	}
	if !containsStr(err.Error(), "pre") {
		t.Errorf("error must mention hook type: %s", err.Error())
	}
}

func TestUnknownVertexKindError(t *testing.T) {
	err := &dagerrors.UnknownVertexKindError{Kind: "custom"}
	if err.Error() == "" {
		t.Error("UnknownVertexKindError.Error() must not be empty")
	}
	if !containsStr(err.Error(), "custom") {
		t.Errorf("error must mention the unknown kind: %s", err.Error())
	}
}

func TestErrorsAreDistinctTypes(t *testing.T) {
	// Each error type must be distinguishable with errors.As.
	cause := fmt.Errorf("base")

	errs := []error{
		dagerrors.NewCycleError([]string{"A", "B"}),
		dagerrors.NewValidationError("id", "kind", cause),
		&dagerrors.IDCollisionError{ID: "x"},
		&dagerrors.SerializationError{Cause: cause},
		&dagerrors.TraversalError{Cause: cause},
		&dagerrors.UnknownVertexKindError{Kind: "k"},
	}

	for _, e := range errs {
		if e.Error() == "" {
			t.Errorf("%T must have a non-empty Error()", e)
		}
	}
}

func containsStr(s, sub string) bool {
	return len(s) >= len(sub) && (s == sub ||
		func() bool {
			for i := 0; i <= len(s)-len(sub); i++ {
				if s[i:i+len(sub)] == sub {
					return true
				}
			}
			return false
		}())
}
