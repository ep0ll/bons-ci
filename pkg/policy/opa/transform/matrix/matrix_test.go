package matrix_test

import (
	"context"
	"testing"

	polOtel "github.com/bons/bons-ci/pkg/policy/opa/internal/otel"
	"github.com/bons/bons-ci/pkg/policy/opa/transform"
	"github.com/bons/bons-ci/pkg/policy/opa/transform/matrix"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func init() { polOtel.UseNoop() }

// ─── Expand (pure) ────────────────────────────────────────────────────────────

func TestExpand_Empty(t *testing.T) {
	exp, err := matrix.Expand(matrix.Strategy{})
	require.NoError(t, err)
	assert.Empty(t, exp.Configs)
}

func TestExpand_SingleAxis(t *testing.T) {
	exp, err := matrix.Expand(matrix.Strategy{
		Matrix: map[string][]string{"os": {"linux", "windows", "darwin"}},
	})
	require.NoError(t, err)
	require.Len(t, exp.Configs, 3)

	ids := configIDs(exp.Configs)
	assert.ElementsMatch(t, []string{"linux", "windows", "darwin"}, ids)
}

func TestExpand_TwoAxes_CartesianProduct(t *testing.T) {
	exp, err := matrix.Expand(matrix.Strategy{
		Matrix: map[string][]string{
			"os":   {"linux", "windows"},
			"arch": {"amd64", "arm64"},
		},
	})
	require.NoError(t, err)
	require.Len(t, exp.Configs, 4)

	ids := configIDs(exp.Configs)
	assert.ElementsMatch(t, []string{
		"amd64-linux", "arm64-linux", "amd64-windows", "arm64-windows",
	}, ids)
}

func TestExpand_ThreeAxes(t *testing.T) {
	exp, err := matrix.Expand(matrix.Strategy{
		Matrix: map[string][]string{
			"os":   {"linux"},
			"arch": {"amd64", "arm64"},
			"go":   {"1.21", "1.22"},
		},
	})
	require.NoError(t, err)
	assert.Len(t, exp.Configs, 4) // 1 × 2 × 2
}

func TestExpand_IDIsDeterministic(t *testing.T) {
	s := matrix.Strategy{
		Matrix: map[string][]string{
			"os":   {"linux"},
			"arch": {"amd64"},
		},
	}
	exp1, _ := matrix.Expand(s)
	exp2, _ := matrix.Expand(s)
	assert.Equal(t, configIDs(exp1.Configs), configIDs(exp2.Configs))
}

func TestExpand_Exclude_SingleEntry(t *testing.T) {
	exp, err := matrix.Expand(matrix.Strategy{
		Matrix: map[string][]string{
			"os":   {"linux", "windows"},
			"arch": {"amd64", "arm64"},
		},
		Exclude: []map[string]string{
			{"os": "windows", "arch": "arm64"},
		},
	})
	require.NoError(t, err)
	require.Len(t, exp.Configs, 3)

	for _, c := range exp.Configs {
		assert.False(t, c.Vars["os"] == "windows" && c.Vars["arch"] == "arm64",
			"excluded combo should not appear")
	}
}

func TestExpand_Exclude_MultipleEntries(t *testing.T) {
	exp, err := matrix.Expand(matrix.Strategy{
		Matrix: map[string][]string{
			"os":   {"linux", "windows"},
			"arch": {"amd64", "arm64"},
		},
		Exclude: []map[string]string{
			{"os": "windows", "arch": "arm64"},
			{"os": "linux", "arch": "arm64"},
		},
	})
	require.NoError(t, err)
	assert.Len(t, exp.Configs, 2) // only linux/amd64 and windows/amd64
}

func TestExpand_Exclude_AllCombos_EmptyResult(t *testing.T) {
	exp, err := matrix.Expand(matrix.Strategy{
		Matrix: map[string][]string{"os": {"linux"}},
		Exclude: []map[string]string{
			{"os": "linux"},
		},
	})
	require.NoError(t, err)
	assert.Empty(t, exp.Configs)
}

func TestExpand_Include_AddsExtraKey(t *testing.T) {
	exp, err := matrix.Expand(matrix.Strategy{
		Matrix: map[string][]string{
			"os":   {"linux"},
			"arch": {"amd64"},
		},
		Include: []map[string]string{
			{"os": "linux", "arch": "amd64", "runner": "ubuntu-latest"},
		},
	})
	require.NoError(t, err)
	require.Len(t, exp.Configs, 1)
	assert.Equal(t, "ubuntu-latest", exp.Configs[0].Extra["runner"])
}

func TestExpand_Include_AddsStandaloneConfig(t *testing.T) {
	exp, err := matrix.Expand(matrix.Strategy{
		Matrix: map[string][]string{
			"os": {"linux"},
		},
		Include: []map[string]string{
			{"os": "macos", "arch": "arm64"}, // new combo not in matrix
		},
	})
	require.NoError(t, err)
	require.Len(t, exp.Configs, 2)
	ids := configIDs(exp.Configs)
	assert.Contains(t, ids, "linux")

	// The standalone include should appear with its own ID.
	found := false
	for _, c := range exp.Configs {
		if c.Vars["os"] == "macos" {
			found = true
			break
		}
	}
	assert.True(t, found, "standalone include not added")
}

func TestExpand_Include_NotAddedWhenComboSubsumes(t *testing.T) {
	// Include that is fully subsumed by existing combos should NOT add a new config.
	exp, err := matrix.Expand(matrix.Strategy{
		Matrix: map[string][]string{
			"os":   {"linux", "windows"},
			"arch": {"amd64"},
		},
		Include: []map[string]string{
			{"os": "linux"}, // linux is already in the matrix
		},
	})
	require.NoError(t, err)
	assert.Len(t, exp.Configs, 2, "include subsumed by existing combo should not add a third config")
}

func TestExpand_MaxParallel_Propagated(t *testing.T) {
	exp, err := matrix.Expand(matrix.Strategy{
		Matrix:      map[string][]string{"os": {"linux"}},
		MaxParallel: 4,
	})
	require.NoError(t, err)
	assert.Equal(t, 4, exp.MaxParallel)
}

func TestExpand_FailFast_Propagated(t *testing.T) {
	exp, err := matrix.Expand(matrix.Strategy{
		Matrix:   map[string][]string{"os": {"linux"}},
		FailFast: true,
	})
	require.NoError(t, err)
	assert.True(t, exp.FailFast)
}

// ─── ExpanderTransformer ──────────────────────────────────────────────────────

func TestExpanderTransformer_NoOp_WhenActionNotExpand(t *testing.T) {
	tr := matrix.NewExpanderTransformer()
	inp := matrix.MatrixInput{Strategy: matrix.Strategy{
		Matrix: map[string][]string{"os": {"linux"}},
	}}
	dec, err := tr.Apply(context.Background(), inp, transform.Decision{Action: "ALLOW"})
	require.NoError(t, err)
	assert.False(t, dec.Mutated)
}

func TestExpanderTransformer_OPAExpansion_Parsed(t *testing.T) {
	tr := matrix.NewExpanderTransformer()
	inp := matrix.MatrixInput{}

	dec, err := tr.Apply(context.Background(), inp, transform.Decision{
		Action: "EXPAND",
		Expansions: []map[string]any{
			{"id": "linux-amd64", "vars": map[string]any{"os": "linux", "arch": "amd64"}},
			{"id": "linux-arm64", "vars": map[string]any{"os": "linux", "arch": "arm64"}},
		},
	})
	require.NoError(t, err)
	assert.True(t, dec.Mutated)

	exp, ok := dec.Updates[matrix.ExpansionKey].(matrix.Expansion)
	require.True(t, ok)
	require.Len(t, exp.Configs, 2)
	assert.Equal(t, "linux-amd64", exp.Configs[0].ID)
	assert.Equal(t, "linux-arm64", exp.Configs[1].ID)
}

func TestExpanderTransformer_GoSideExpansion(t *testing.T) {
	tr := matrix.NewExpanderTransformer()
	inp := matrix.MatrixInput{
		Strategy: matrix.Strategy{
			Matrix: map[string][]string{
				"os":   {"linux", "windows"},
				"arch": {"amd64"},
			},
		},
	}

	dec, err := tr.Apply(context.Background(), inp, transform.Decision{
		Action:     "EXPAND",
		Expansions: nil, // trigger Go-side computation
	})
	require.NoError(t, err)
	assert.True(t, dec.Mutated)

	exp := dec.Updates[matrix.ExpansionKey].(matrix.Expansion)
	assert.Len(t, exp.Configs, 2)
}

func TestExpanderTransformer_WrongInputType_ReturnsError(t *testing.T) {
	tr := matrix.NewExpanderTransformer()
	_, err := tr.Apply(context.Background(), "wrong", transform.Decision{
		Action:     "EXPAND",
		Expansions: nil,
	})
	require.Error(t, err)
	assert.Contains(t, err.Error(), "MatrixInput")
}

func TestExpanderTransformer_OPAExpansion_MissingID_ReturnsError(t *testing.T) {
	tr := matrix.NewExpanderTransformer()
	inp := matrix.MatrixInput{}
	_, err := tr.Apply(context.Background(), inp, transform.Decision{
		Action: "EXPAND",
		Expansions: []map[string]any{
			{"vars": map[string]any{"os": "linux"}}, // no id
		},
	})
	require.Error(t, err)
	assert.Contains(t, err.Error(), "'id'")
}

func TestExpanderTransformer_OPAExpansion_NonStringVar_ReturnsError(t *testing.T) {
	tr := matrix.NewExpanderTransformer()
	inp := matrix.MatrixInput{}
	_, err := tr.Apply(context.Background(), inp, transform.Decision{
		Action: "EXPAND",
		Expansions: []map[string]any{
			{"id": "x", "vars": map[string]any{"os": 123}},
		},
	})
	require.Error(t, err)
	assert.Contains(t, err.Error(), "must be string")
}

func TestExpanderTransformer_EmptyExpansion_NotMutated(t *testing.T) {
	tr := matrix.NewExpanderTransformer()
	inp := matrix.MatrixInput{
		Strategy: matrix.Strategy{}, // empty matrix → Expand returns empty
	}
	dec, err := tr.Apply(context.Background(), inp, transform.Decision{
		Action:     "EXPAND",
		Expansions: nil,
	})
	require.NoError(t, err)
	assert.False(t, dec.Mutated)
}

func TestExpanderTransformer_MaxParallelFromUpdates(t *testing.T) {
	tr := matrix.NewExpanderTransformer()
	inp := matrix.MatrixInput{}
	dec, err := tr.Apply(context.Background(), inp, transform.Decision{
		Action: "EXPAND",
		Updates: map[string]any{
			"max_parallel": float64(8),
			"fail_fast":    true,
		},
		Expansions: []map[string]any{
			{"id": "cfg1", "vars": map[string]any{"os": "linux"}},
		},
	})
	require.NoError(t, err)
	exp := dec.Updates[matrix.ExpansionKey].(matrix.Expansion)
	assert.Equal(t, 8, exp.MaxParallel)
	assert.True(t, exp.FailFast)
}

// ─── RegisterAll ─────────────────────────────────────────────────────────────

func TestRegisterAll(t *testing.T) {
	reg, err := transform.NewRegistry()
	require.NoError(t, err)
	matrix.RegisterAll(reg)

	ts := reg.Get(transform.Key{Kind: "matrix", Action: "EXPAND"})
	require.Len(t, ts, 1)
	assert.Equal(t, "matrix.expander", ts[0].Name())
}

// ─── helpers ──────────────────────────────────────────────────────────────────

func configIDs(cfgs []matrix.BuildConfig) []string {
	ids := make([]string, len(cfgs))
	for i, c := range cfgs {
		ids[i] = c.ID
	}
	return ids
}
