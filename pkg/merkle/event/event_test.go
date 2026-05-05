package event_test

import (
	"testing"
	"time"

	"github.com/user/layermerkle/event"
	"github.com/user/layermerkle/layer"
)

func TestValidate_OK(t *testing.T) {
	ev := &event.FileAccessEvent{
		FilePath:   "/bin/sh",
		LayerStack: layer.MustNew("l0"),
		AccessType: event.AccessRead,
		Timestamp:  time.Now(),
	}
	if err := ev.Validate(); err != nil {
		t.Fatalf("valid event: %v", err)
	}
}

func TestValidate_EmptyPath(t *testing.T) {
	ev := &event.FileAccessEvent{
		FilePath:   "",
		LayerStack: layer.MustNew("l0"),
		AccessType: event.AccessRead,
	}
	if err := ev.Validate(); err == nil {
		t.Fatal("expected error for empty FilePath")
	}
}

func TestValidate_EmptyStack(t *testing.T) {
	ev := &event.FileAccessEvent{
		FilePath:   "/bin/sh",
		LayerStack: nil,
		AccessType: event.AccessRead,
	}
	if err := ev.Validate(); err == nil {
		t.Fatal("expected error for nil LayerStack")
	}
}

func TestValidate_UnknownAccessType(t *testing.T) {
	ev := &event.FileAccessEvent{
		FilePath:   "/bin/sh",
		LayerStack: layer.MustNew("l0"),
		AccessType: event.AccessUnknown,
	}
	if err := ev.Validate(); err == nil {
		t.Fatal("expected error for AccessUnknown")
	}
}

func TestIsMutating(t *testing.T) {
	nonMutating := []event.AccessType{event.AccessRead}
	mutating := []event.AccessType{
		event.AccessWrite, event.AccessCreate,
		event.AccessDelete, event.AccessRename, event.AccessChmod,
	}
	for _, at := range nonMutating {
		if at.IsMutating() {
			t.Errorf("AccessType %s should not be mutating", at)
		}
	}
	for _, at := range mutating {
		if !at.IsMutating() {
			t.Errorf("AccessType %s should be mutating", at)
		}
	}
}

func TestOutputLayer(t *testing.T) {
	ev := &event.FileAccessEvent{
		FilePath:   "/f",
		LayerStack: layer.MustNew("base", "exec1", "exec2"),
		AccessType: event.AccessRead,
	}
	if ev.OutputLayer() != "exec2" {
		t.Fatalf("OutputLayer: expected exec2, got %s", ev.OutputLayer())
	}
}

func TestClone_Isolation(t *testing.T) {
	orig := &event.FileAccessEvent{
		FilePath:     "/f",
		LayerStack:   layer.MustNew("a", "b"),
		AccessType:   event.AccessRead,
		VertexDigest: "vtx",
		Metadata:     map[string]string{"key": "val"},
	}
	clone := orig.Clone()
	clone.FilePath = "mutated"
	clone.LayerStack[0] = "MUTATED"
	clone.Metadata["key"] = "changed"

	if orig.FilePath == "mutated" {
		t.Fatal("Clone must not share FilePath")
	}
	if orig.LayerStack[0] == "MUTATED" {
		t.Fatal("Clone must not share LayerStack backing array")
	}
	if orig.Metadata["key"] == "changed" {
		t.Fatal("Clone must not share Metadata map")
	}
}

func TestAccessTypeString(t *testing.T) {
	cases := map[event.AccessType]string{
		event.AccessRead:   "read",
		event.AccessWrite:  "write",
		event.AccessCreate: "create",
		event.AccessDelete: "delete",
		event.AccessRename: "rename",
		event.AccessChmod:  "chmod",
	}
	for at, want := range cases {
		if got := at.String(); got != want {
			t.Errorf("AccessType(%d).String(): got %q, want %q", at, got, want)
		}
	}
}
