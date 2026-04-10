package b2

import (
	"testing"

	"github.com/containerd/containerd/v2/core/content"
	"github.com/stretchr/testify/assert"
)

// ---------------------------------------------------------------------------
// statusAdaptor
// ---------------------------------------------------------------------------

func TestStatusAdaptor_Ref(t *testing.T) {
	a := statusAdaptor(content.Status{Ref: "my-ref"})
	v, ok := a.Field([]string{"ref"})
	assert.True(t, ok)
	assert.Equal(t, "my-ref", v)
}

func TestStatusAdaptor_Empty(t *testing.T) {
	a := statusAdaptor(content.Status{Ref: "r"})
	_, ok := a.Field(nil)
	assert.False(t, ok)
}

func TestStatusAdaptor_Unknown(t *testing.T) {
	a := statusAdaptor(content.Status{})
	_, ok := a.Field([]string{"unknown"})
	assert.False(t, ok)
}

// ---------------------------------------------------------------------------
// objectEntryAdaptor
// ---------------------------------------------------------------------------

func TestObjectEntryAdaptor_Key(t *testing.T) {
	for _, field := range []string{"ref", "object", "key", "Key", "OBJECT"} {
		t.Run(field, func(t *testing.T) {
			a := objectEntryAdaptor("tenant/blobs/sha256/abc", nil)
			v, ok := a.Field([]string{field})
			assert.True(t, ok)
			assert.Equal(t, "tenant/blobs/sha256/abc", v)
		})
	}
}

func TestObjectEntryAdaptor_Labels(t *testing.T) {
	meta := map[string]string{"foo": "bar", "baz.qux": "val"}
	a := objectEntryAdaptor("k", meta)

	v, ok := a.Field([]string{"labels", "foo"})
	assert.True(t, ok)
	assert.Equal(t, "bar", v)

	v, ok = a.Field([]string{"labels", "baz", "qux"})
	assert.True(t, ok)
	assert.Equal(t, "val", v)
}

func TestObjectEntryAdaptor_Labels_NoSubfield(t *testing.T) {
	a := objectEntryAdaptor("k", map[string]string{"a": "b"})
	_, ok := a.Field([]string{"labels"})
	assert.False(t, ok)
}

func TestObjectEntryAdaptor_NilMetadata(t *testing.T) {
	a := objectEntryAdaptor("k", nil)
	_, ok := a.Field([]string{"labels", "any"})
	assert.False(t, ok)
}

func TestObjectEntryAdaptor_Empty(t *testing.T) {
	a := objectEntryAdaptor("k", nil)
	_, ok := a.Field(nil)
	assert.False(t, ok)
}

// ---------------------------------------------------------------------------
// mapLookup
// ---------------------------------------------------------------------------

func TestMapLookup(t *testing.T) {
	tests := []struct {
		name     string
		fp       []string
		m        map[string]string
		wantVal  string
		wantOK   bool
	}{
		{"nil fieldpath", nil, map[string]string{"a": "b"}, "", false},
		{"nil map", []string{"a"}, nil, "", false},
		{"found", []string{"x"}, map[string]string{"x": "y"}, "y", true},
		{"joined", []string{"a", "b"}, map[string]string{"a.b": "c"}, "c", true},
		{"missing", []string{"z"}, map[string]string{"a": "b"}, "", false},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			v, ok := mapLookup(tc.fp, tc.m)
			assert.Equal(t, tc.wantOK, ok)
			assert.Equal(t, tc.wantVal, v)
		})
	}
}
