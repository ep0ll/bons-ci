// Package graph internal tests — covers buildCyclePath branches directly.
package graph

import (
	"context"
	"strings"
	"testing"

	"github.com/bons/bons-ci/plugins/dag/vertex"
)

type graphNamedVertex struct {
	id     string
	name   string
	inputs []vertex.Vertex
}

func (v *graphNamedVertex) ID() string                       { return v.id }
func (v *graphNamedVertex) Kind() vertex.Kind                { return vertex.KindExec }
func (v *graphNamedVertex) Inputs() []vertex.Vertex          { return v.inputs }
func (v *graphNamedVertex) Validate(_ context.Context) error { return nil }
func (v *graphNamedVertex) Name() string                     { return v.name }

type graphUnnamedVertex struct {
	id     string
	inputs []vertex.Vertex
}

func (v *graphUnnamedVertex) ID() string                       { return v.id }
func (v *graphUnnamedVertex) Kind() vertex.Kind                { return vertex.KindSource }
func (v *graphUnnamedVertex) Inputs() []vertex.Vertex          { return v.inputs }
func (v *graphUnnamedVertex) Validate(_ context.Context) error { return nil }

func TestBuildCyclePathStartIdxMinusOne(t *testing.T) {
	result := buildCyclePath([]string{"A", "B", "C"}, "X", nil)
	if !strings.Contains(result, "X") {
		t.Errorf("startIdx==-1: want X in result, got %q", result)
	}
	if !strings.Contains(result, "(cycle)") {
		t.Errorf("startIdx==-1: want '(cycle)' in result, got %q", result)
	}
}

func TestBuildCyclePathVertexMissingFromMap(t *testing.T) {
	verts := map[string]vertex.Vertex{
		"A": &graphUnnamedVertex{id: "A"},
		// "B" absent — hits !ok branch
	}
	result := buildCyclePath([]string{"A", "B"}, "A", verts)
	if !strings.Contains(result, "B") {
		t.Errorf("missing vertex B must appear (truncated) in path: %q", result)
	}
}

func TestBuildCyclePathNamedVertex(t *testing.T) {
	verts := map[string]vertex.Vertex{
		"id-alpha": &graphNamedVertex{id: "id-alpha", name: "AlphaNode"},
		"id-beta":  &graphNamedVertex{id: "id-beta", name: "BetaNode"},
	}
	result := buildCyclePath([]string{"id-alpha", "id-beta"}, "id-alpha", verts)
	if !strings.Contains(result, "AlphaNode") {
		t.Errorf("Named vertex: result must use Name(), got %q", result)
	}
}

func TestBuildCyclePathUnnamedVertex(t *testing.T) {
	verts := map[string]vertex.Vertex{
		"abcdefghijklmnopqr": &graphUnnamedVertex{id: "abcdefghijklmnopqr"},
	}
	result := buildCyclePath([]string{"abcdefghijklmnopqr"}, "abcdefghijklmnopqr", verts)
	if !strings.Contains(result, string(vertex.KindSource)) {
		t.Errorf("unnamed vertex: result must contain kind prefix, got %q", result)
	}
}

func TestBuildCyclePathShortID(t *testing.T) {
	verts := map[string]vertex.Vertex{
		"abc": &graphUnnamedVertex{id: "abc"},
	}
	result := buildCyclePath([]string{"abc"}, "abc", verts)
	if !strings.Contains(result, "abc") {
		t.Errorf("short ID must appear in path: %q", result)
	}
}

func TestDetectCyclesNamedVerticesInternal(t *testing.T) {
	a := &graphNamedVertex{id: "gv-a", name: "SourceStage"}
	b := &graphNamedVertex{id: "gv-b", name: "BuildStage"}
	verts := map[string]vertex.Vertex{"gv-a": a, "gv-b": b}
	upstream := map[string][]string{
		"gv-a": {"gv-b"},
		"gv-b": {"gv-a"},
	}
	if err := detectCycles(verts, upstream); err == nil {
		t.Fatal("detectCycles must return error for named-vertex cycle")
	}
}

func TestDetectCyclesNoCycleInternal(t *testing.T) {
	a := &graphUnnamedVertex{id: "dc-a"}
	b := &graphUnnamedVertex{id: "dc-b"}
	verts := map[string]vertex.Vertex{"dc-a": a, "dc-b": b}
	upstream := map[string][]string{
		"dc-a": {"dc-b"},
		"dc-b": {},
	}
	if err := detectCycles(verts, upstream); err != nil {
		t.Errorf("acyclic graph must pass: %v", err)
	}
}
