// Internal ops package tests — exercises the nil-action branch in FileOp.Name
// which is unreachable via NewFileOp (always sets action).
package ops

import (
	"testing"
)

// TestFileOpNameNilActionInternal directly exercises the `f.action == nil`
// branch of Name() by constructing a FileOp with a nil action field.
// This branch is defensive dead-code in the public API but must be tested
// for full statement coverage.
func TestFileOpNameNilActionInternal(t *testing.T) {
	f := &FileOp{action: nil} // action is nil → Name() returns "file"
	name := f.Name()
	if name != "file" {
		t.Errorf("FileOp.Name with nil action: want \"file\", got %q", name)
	}
}

func TestFileOpNameWithActionInternal(t *testing.T) {
	fa := &FileAction{kind: FileActionMkdir}
	f := &FileOp{action: fa}
	name := f.Name()
	if name != "file:mkdir" {
		t.Errorf("FileOp.Name with mkdir action: want file:mkdir, got %q", name)
	}
}

func TestFileOpValidateNilActionInternal(t *testing.T) {
	// Validate() with nil action should return an error.
	f := &FileOp{action: nil}
	if err := f.Validate(nil); err == nil { //nolint:staticcheck
		t.Error("FileOp.Validate with nil action must return an error")
	}
}
