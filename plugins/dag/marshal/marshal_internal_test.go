// Package marshal internal tests — exercises type-mismatch error branches in
// serializer functions that are unreachable via the public Marshal API
// (which always passes the correct vertex type to each serializer).
package marshal

import (
	"context"
	"testing"

	"github.com/bons/bons-ci/plugins/dag/ops"
	"github.com/bons/bons-ci/plugins/dag/vertex"
)

// wrongVertex is a vertex that does NOT implement any of the concrete op types.
type wrongVertex struct{ id string }

func (v *wrongVertex) ID() string                       { return v.id }
func (v *wrongVertex) Kind() vertex.Kind                { return vertex.KindSource }
func (v *wrongVertex) Inputs() []vertex.Vertex          { return nil }
func (v *wrongVertex) Validate(_ context.Context) error { return nil }

var testPosMap = map[string]int{"dummy": 0}

func TestSerializeSourceTypeMismatch(t *testing.T) {
	m := New()
	_, err := m.serializeSource(&wrongVertex{id: "x"})
	if err == nil {
		t.Error("serializeSource with wrong type must return error")
	}
}

func TestSerializeExecTypeMismatch(t *testing.T) {
	m := New()
	_, err := m.serializeExec(&wrongVertex{id: "x"}, testPosMap)
	if err == nil {
		t.Error("serializeExec with wrong type must return error")
	}
}

func TestSerializeFileTypeMismatch(t *testing.T) {
	m := New()
	_, err := m.serializeFile(&wrongVertex{id: "x"}, testPosMap)
	if err == nil {
		t.Error("serializeFile with wrong type must return error")
	}
}

func TestSerializeMergeTypeMismatch(t *testing.T) {
	m := New()
	_, err := m.serializeMerge(&wrongVertex{id: "x"}, testPosMap)
	if err == nil {
		t.Error("serializeMerge with wrong type must return error")
	}
}

func TestSerializeDiffTypeMismatch(t *testing.T) {
	m := New()
	_, err := m.serializeDiff(&wrongVertex{id: "x"}, testPosMap)
	if err == nil {
		t.Error("serializeDiff with wrong type must return error")
	}
}

func TestSerializeBuildTypeMismatch(t *testing.T) {
	_, err := serializeBuild(&wrongVertex{id: "x"}, testPosMap)
	if err == nil {
		t.Error("serializeBuild with wrong type must return error")
	}
}

// ─── serializeExec — mount source not in posMap ───────────────────────────────

func TestSerializeExecMountSourceNotInPosMap(t *testing.T) {
	img := ops.Image("alpine:3.18")
	extra := ops.Image("busybox:latest")
	e := ops.Exec(img.Ref(),
		ops.WithArgs("ls"),
		ops.WithCwd("/"),
		ops.WithMount(ops.Mount{Target: "/mnt", Source: extra.Ref()}),
	)
	m := New()
	// posMap does NOT contain extra's ID → triggers "not in position map" error.
	posMap := map[string]int{img.ID(): 0} // extra missing
	_, err := m.serializeExec(e, posMap)
	if err == nil {
		t.Error("serializeExec with mount source not in posMap must error")
	}
}

// ─── serializeDiff — lower/upper not in posMap ────────────────────────────────

func TestSerializeDiffLowerNotInPosMap(t *testing.T) {
	lower := ops.Image("alpine:3.18")
	upper := ops.Image("busybox:latest")
	d := ops.NewDiffOp(lower.Ref(), upper.Ref(), ops.Constraints{})

	m := New()
	// posMap is empty → lower.Vertex.ID() not found.
	_, err := m.serializeDiff(d, map[string]int{})
	if err == nil {
		t.Error("serializeDiff with lower not in posMap must error")
	}
}

func TestSerializeDiffUpperNotInPosMap(t *testing.T) {
	lower := ops.Image("alpine:3.18")
	upper := ops.Image("busybox:latest")
	d := ops.NewDiffOp(lower.Ref(), upper.Ref(), ops.Constraints{})

	m := New()
	// posMap has lower but not upper → upper.Vertex.ID() not found.
	posMap := map[string]int{lower.ID(): 0}
	_, err := m.serializeDiff(d, posMap)
	if err == nil {
		t.Error("serializeDiff with upper not in posMap must error")
	}
}

// ─── serializeMerge — input not in posMap ─────────────────────────────────────

func TestSerializeMergeInputNotInPosMap(t *testing.T) {
	a := ops.Image("alpine:3.18")
	b := ops.Image("busybox:latest")
	mo := ops.NewMergeOp([]vertex.Ref{a.Ref(), b.Ref()}, ops.Constraints{})

	m := New()
	// posMap is empty → a.Vertex.ID() not found.
	_, err := m.serializeMerge(mo, map[string]int{})
	if err == nil {
		t.Error("serializeMerge with input not in posMap must error")
	}
}

// ─── serializeBuild — source not in posMap ────────────────────────────────────

func TestSerializeBuildSourceNotInPosMap(t *testing.T) {
	src := ops.Image("alpine:3.18")
	b := ops.NewBuildOp(src.Ref(), ops.WithDefinitionFile("plan.llb"))

	// posMap missing src.ID() → triggers "not in position map" error.
	_, err := serializeBuild(b, map[string]int{})
	if err == nil {
		t.Error("serializeBuild with source not in posMap must error")
	}
}
