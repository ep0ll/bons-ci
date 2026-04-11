package dag_test

import (
	"context"
	"testing"

	polOtel "github.com/bons/bons-ci/pkg/policy/opa/internal/otel"
	"github.com/bons/bons-ci/pkg/policy/opa/transform"
	"github.com/bons/bons-ci/pkg/policy/opa/transform/dag"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func init() { polOtel.UseNoop() }

func makeExpansion(id, typ, identifier string, deps []string) map[string]any {
	m := map[string]any{"id": id, "type": typ}
	if identifier != "" {
		m["identifier"] = identifier
	}
	if len(deps) > 0 {
		deps2 := make([]any, len(deps))
		for i, d := range deps {
			deps2[i] = d
		}
		m["depends_on"] = deps2
	}
	return m
}

// ─── ExpanderTransformer ──────────────────────────────────────────────────────

func TestExpander_NoOp_WhenActionNotExpand(t *testing.T) {
	tr := dag.NewExpanderTransformer()
	inp := dag.DAGInput{Op: &dag.OpDescriptor{ID: "op1", Type: "source"}}
	dec, err := tr.Apply(context.Background(), inp, transform.Decision{Action: "ALLOW"})
	require.NoError(t, err)
	assert.False(t, dec.Mutated)
	assert.Empty(t, dec.Updates)
}

func TestExpander_NoExpansions_NoMutation(t *testing.T) {
	tr := dag.NewExpanderTransformer()
	inp := dag.DAGInput{Op: &dag.OpDescriptor{ID: "op1", Type: "source"}}
	dec, err := tr.Apply(context.Background(), inp, transform.Decision{
		Action:     "EXPAND",
		Expansions: nil,
	})
	require.NoError(t, err)
	assert.False(t, dec.Mutated)
}

func TestExpander_ParsesExpandedNodes(t *testing.T) {
	tr := dag.NewExpanderTransformer()
	inp := dag.DAGInput{Op: &dag.OpDescriptor{ID: "op1", Type: "source",
		Identifier: "docker-image://example.com/app:v1"}}

	dec, err := tr.Apply(context.Background(), inp, transform.Decision{
		Action: "EXPAND",
		Expansions: []map[string]any{
			makeExpansion("op1-scan", "exec", "security-scanner", []string{"op1"}),
		},
	})
	require.NoError(t, err)
	assert.True(t, dec.Mutated)
	require.NotNil(t, dec.Updates)

	nodes, ok := dec.Updates[dag.ExpansionKey].([]dag.ExpandedNode)
	require.True(t, ok)
	require.Len(t, nodes, 1)
	assert.Equal(t, "op1-scan", nodes[0].ID)
	assert.Equal(t, "exec", nodes[0].Type)
	assert.Equal(t, "security-scanner", nodes[0].Identifier)
	assert.Equal(t, []string{"op1"}, nodes[0].DependsOn)
}

func TestExpander_MultipleNodes(t *testing.T) {
	tr := dag.NewExpanderTransformer()
	inp := dag.DAGInput{Op: &dag.OpDescriptor{ID: "op1", Type: "source"}}

	dec, err := tr.Apply(context.Background(), inp, transform.Decision{
		Action: "EXPAND",
		Expansions: []map[string]any{
			makeExpansion("op1-scan", "exec", "scanner", []string{"op1"}),
			makeExpansion("op1-sign", "exec", "signer", []string{"op1-scan"}),
		},
	})
	require.NoError(t, err)
	assert.True(t, dec.Mutated)

	nodes := dec.Updates[dag.ExpansionKey].([]dag.ExpandedNode)
	require.Len(t, nodes, 2)
	assert.Equal(t, "op1-scan", nodes[0].ID)
	assert.Equal(t, "op1-sign", nodes[1].ID)
}

func TestExpander_NodeWithAttrs(t *testing.T) {
	tr := dag.NewExpanderTransformer()
	inp := dag.DAGInput{Op: &dag.OpDescriptor{ID: "op1", Type: "source"}}

	expansion := makeExpansion("op1-scan", "exec", "scanner", nil)
	expansion["attrs"] = map[string]any{"scan.level": "high", "scan.format": "sarif"}

	dec, err := tr.Apply(context.Background(), inp, transform.Decision{
		Action:     "EXPAND",
		Expansions: []map[string]any{expansion},
	})
	require.NoError(t, err)
	nodes := dec.Updates[dag.ExpansionKey].([]dag.ExpandedNode)
	assert.Equal(t, "high", nodes[0].Attrs["scan.level"])
	assert.Equal(t, "sarif", nodes[0].Attrs["scan.format"])
}

func TestExpander_MissingID_ReturnsError(t *testing.T) {
	tr := dag.NewExpanderTransformer()
	inp := dag.DAGInput{Op: &dag.OpDescriptor{ID: "op1", Type: "source"}}

	_, err := tr.Apply(context.Background(), inp, transform.Decision{
		Action: "EXPAND",
		Expansions: []map[string]any{
			{"type": "exec"}, // no id
		},
	})
	require.Error(t, err)
	assert.Contains(t, err.Error(), "'id'")
}

func TestExpander_MissingType_ReturnsError(t *testing.T) {
	tr := dag.NewExpanderTransformer()
	inp := dag.DAGInput{Op: &dag.OpDescriptor{ID: "op1", Type: "source"}}

	_, err := tr.Apply(context.Background(), inp, transform.Decision{
		Action: "EXPAND",
		Expansions: []map[string]any{
			{"id": "op1-scan"}, // no type
		},
	})
	require.Error(t, err)
	assert.Contains(t, err.Error(), "'type'")
}

func TestExpander_NonStringAttr_ReturnsError(t *testing.T) {
	tr := dag.NewExpanderTransformer()
	inp := dag.DAGInput{Op: &dag.OpDescriptor{ID: "op1", Type: "source"}}

	expansion := makeExpansion("op1-scan", "exec", "", nil)
	expansion["attrs"] = map[string]any{"level": 5} // non-string

	_, err := tr.Apply(context.Background(), inp, transform.Decision{
		Action:     "EXPAND",
		Expansions: []map[string]any{expansion},
	})
	require.Error(t, err)
	assert.Contains(t, err.Error(), "must be string")
}

func TestExpander_WrongInputType_ReturnsError(t *testing.T) {
	tr := dag.NewExpanderTransformer()
	_, err := tr.Apply(context.Background(), "wrong type", transform.Decision{
		Action:     "EXPAND",
		Expansions: []map[string]any{makeExpansion("n1", "exec", "", nil)},
	})
	require.Error(t, err)
	assert.Contains(t, err.Error(), "DAGInput")
}

// ─── Walker ───────────────────────────────────────────────────────────────────

func TestWalker_Empty(t *testing.T) {
	w := dag.NewWalker(nil)
	var visited []string
	w.Walk(func(id string, _ *dag.OpDescriptor, _ []string) bool {
		visited = append(visited, id)
		return true
	})
	assert.Empty(t, visited)
}

func TestWalker_SingleNode(t *testing.T) {
	ops := []*dag.OpDescriptor{
		{ID: "op1", Type: "source"},
	}
	w := dag.NewWalker(ops)
	var visited []string
	w.Walk(func(id string, _ *dag.OpDescriptor, _ []string) bool {
		visited = append(visited, id)
		return true
	})
	assert.Equal(t, []string{"op1"}, visited)
}

func TestWalker_LinearChain(t *testing.T) {
	// op3 → op2 → op1 (op3 is terminal)
	ops := []*dag.OpDescriptor{
		{ID: "op1", Type: "source"},
		{ID: "op2", Type: "exec", Inputs: []string{"op1"}},
		{ID: "op3", Type: "exec", Inputs: []string{"op2"}},
	}
	w := dag.NewWalker(ops)
	var visited []string
	w.Walk(func(id string, _ *dag.OpDescriptor, _ []string) bool {
		visited = append(visited, id)
		return true
	})
	assert.Equal(t, []string{"op3", "op2", "op1"}, visited)
}

func TestWalker_AncestorsPopulated(t *testing.T) {
	ops := []*dag.OpDescriptor{
		{ID: "op1", Type: "source"},
		{ID: "op2", Type: "exec", Inputs: []string{"op1"}},
		{ID: "op3", Type: "exec", Inputs: []string{"op2"}},
	}
	w := dag.NewWalker(ops)
	ancestorMap := make(map[string][]string)
	w.Walk(func(id string, _ *dag.OpDescriptor, ancestors []string) bool {
		cp := make([]string, len(ancestors))
		copy(cp, ancestors)
		ancestorMap[id] = cp
		return true
	})
	assert.Empty(t, ancestorMap["op3"])
	assert.Equal(t, []string{"op3"}, ancestorMap["op2"])
	assert.Equal(t, []string{"op3", "op2"}, ancestorMap["op1"])
}

func TestWalker_PruneReturnFalse(t *testing.T) {
	ops := []*dag.OpDescriptor{
		{ID: "op1", Type: "source"},
		{ID: "op2", Type: "exec", Inputs: []string{"op1"}},
		{ID: "op3", Type: "exec", Inputs: []string{"op2"}},
	}
	w := dag.NewWalker(ops)
	var visited []string
	w.Walk(func(id string, _ *dag.OpDescriptor, _ []string) bool {
		visited = append(visited, id)
		return id != "op2" // prune op2's subtree (op1 should not be visited)
	})
	assert.Equal(t, []string{"op3", "op2"}, visited)
}

func TestWalker_NoCycleVisit(t *testing.T) {
	// op1 and op2 both feed into op3; op2 also feeds into op4.
	// op4 is terminal. op1 appears in two chains — should be visited once.
	ops := []*dag.OpDescriptor{
		{ID: "op1", Type: "source"},
		{ID: "op2", Type: "exec", Inputs: []string{"op1"}},
		{ID: "op3", Type: "exec", Inputs: []string{"op1", "op2"}},
		{ID: "op4", Type: "merge", Inputs: []string{"op3", "op2"}},
	}
	w := dag.NewWalker(ops)
	counts := make(map[string]int)
	w.Walk(func(id string, _ *dag.OpDescriptor, _ []string) bool {
		counts[id]++
		return true
	})
	for id, n := range counts {
		assert.Equal(t, 1, n, "op %q visited more than once", id)
	}
}

// ─── RegisterAll ─────────────────────────────────────────────────────────────

func TestRegisterAll(t *testing.T) {
	reg, err := transform.NewRegistry()
	require.NoError(t, err)
	dag.RegisterAll(reg)

	ts := reg.Get(transform.Key{Kind: "dag", Action: "EXPAND"})
	require.Len(t, ts, 1)
	assert.Equal(t, "dag.expander", ts[0].Name())
}
