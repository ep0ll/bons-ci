package source_test

import (
	"context"
	"errors"
	"testing"

	polOtel "github.com/bons/bons-ci/pkg/policy/opa/internal/otel"
	"github.com/bons/bons-ci/pkg/policy/opa/transform"
	"github.com/bons/bons-ci/pkg/policy/opa/transform/source"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func init() { polOtel.UseNoop() }

// ─── helpers ──────────────────────────────────────────────────────────────────

func makeInput(id string, attrs map[string]string) source.OpInput {
	if attrs == nil {
		attrs = make(map[string]string)
	}
	return source.NewOpInput(id, attrs, &id, &attrs)
}

func makeDec(action string, updates map[string]any) transform.Decision {
	return transform.Decision{Action: action, Updates: updates}
}

// ─── MutateOpTransformer ──────────────────────────────────────────────────────

func TestMutateOp_NoOp_WhenActionNotConvert(t *testing.T) {
	t2 := source.NewMutateOpTransformer()
	id := "docker-image://old:latest"
	inp := makeInput(id, nil)
	dec, err := t2.Apply(context.Background(), inp, makeDec("ALLOW", nil))
	require.NoError(t, err)
	assert.False(t, dec.Mutated)
	assert.Equal(t, "docker-image://old:latest", id) // unchanged
}

func TestMutateOp_UpdatesIdentifier(t *testing.T) {
	t2 := source.NewMutateOpTransformer()
	id := "docker-image://old:latest"
	attrs := map[string]string{}
	inp := source.NewOpInput(id, attrs, &id, &attrs)

	dec, err := t2.Apply(context.Background(), inp, makeDec("CONVERT", map[string]any{
		"identifier": "docker-image://new:latest",
	}))
	require.NoError(t, err)
	assert.True(t, dec.Mutated)
	assert.Equal(t, "docker-image://new:latest", id)
}

func TestMutateOp_SameIdentifier_NotMutated(t *testing.T) {
	t2 := source.NewMutateOpTransformer()
	id := "docker-image://same:latest"
	attrs := map[string]string{}
	inp := source.NewOpInput(id, attrs, &id, &attrs)

	dec, err := t2.Apply(context.Background(), inp, makeDec("CONVERT", map[string]any{
		"identifier": "docker-image://same:latest",
	}))
	require.NoError(t, err)
	assert.False(t, dec.Mutated)
}

func TestMutateOp_UpdatesAttrs(t *testing.T) {
	t2 := source.NewMutateOpTransformer()
	id := "https://example.com/file"
	attrs := map[string]string{}
	inp := source.NewOpInput(id, attrs, &id, &attrs)

	dec, err := t2.Apply(context.Background(), inp, makeDec("CONVERT", map[string]any{
		"attrs": map[string]any{"http.checksum": "sha256:abc123"},
	}))
	require.NoError(t, err)
	assert.True(t, dec.Mutated)
	assert.Equal(t, "sha256:abc123", attrs["http.checksum"])
}

func TestMutateOp_SameAttrValue_NotMutated(t *testing.T) {
	t2 := source.NewMutateOpTransformer()
	id := "https://example.com/file"
	attrs := map[string]string{"http.checksum": "sha256:abc123"}
	inp := source.NewOpInput(id, attrs, &id, &attrs)

	dec, err := t2.Apply(context.Background(), inp, makeDec("CONVERT", map[string]any{
		"attrs": map[string]any{"http.checksum": "sha256:abc123"},
	}))
	require.NoError(t, err)
	assert.False(t, dec.Mutated)
}

func TestMutateOp_WrongInputType_ReturnsError(t *testing.T) {
	t2 := source.NewMutateOpTransformer()
	_, err := t2.Apply(context.Background(), "not an OpInput", makeDec("CONVERT", map[string]any{
		"identifier": "x",
	}))
	require.Error(t, err)
	assert.Contains(t, err.Error(), "OpInput")
}

func TestMutateOp_NonStringAttrValue_ReturnsError(t *testing.T) {
	t2 := source.NewMutateOpTransformer()
	id := "https://example.com/file"
	attrs := map[string]string{}
	inp := source.NewOpInput(id, attrs, &id, &attrs)

	_, err := t2.Apply(context.Background(), inp, makeDec("CONVERT", map[string]any{
		"attrs": map[string]any{"key": 123}, // non-string
	}))
	require.Error(t, err)
	assert.Contains(t, err.Error(), "must be string")
}

func TestMutateOp_NoUpdates_NoMutation(t *testing.T) {
	t2 := source.NewMutateOpTransformer()
	id := "docker-image://old:v1"
	attrs := map[string]string{}
	inp := source.NewOpInput(id, attrs, &id, &attrs)

	dec, err := t2.Apply(context.Background(), inp, makeDec("CONVERT", nil))
	require.NoError(t, err)
	assert.False(t, dec.Mutated)
}

// ─── RegexRewriteTransformer ─────────────────────────────────────────────────

func TestRegexRewrite_NoOp_WhenNoPattern(t *testing.T) {
	tr := source.NewRegexRewriteTransformer()
	id := "docker-image://old:v1"
	inp := makeInput(id, nil)
	dec, err := tr.Apply(context.Background(), inp, makeDec("CONVERT", map[string]any{}))
	require.NoError(t, err)
	assert.False(t, dec.Mutated)
}

func TestRegexRewrite_CaptureGroupSubstitution(t *testing.T) {
	tr := source.NewRegexRewriteTransformer()
	id := "docker-image://docker.io/library/golang:1.22"
	attrs := map[string]string{}
	inp := source.NewOpInput(id, attrs, &id, &attrs)

	dec, err := tr.Apply(context.Background(), inp, makeDec("CONVERT", map[string]any{
		"pattern":     `^docker-image://docker\.io/library/golang:(.*)$`,
		"replacement": "docker-image://mirror.example.com/golang:$1",
	}))
	require.NoError(t, err)
	assert.True(t, dec.Mutated)
	assert.Equal(t, "docker-image://mirror.example.com/golang:1.22", id)
}

func TestRegexRewrite_NoMatch_NoMutation(t *testing.T) {
	tr := source.NewRegexRewriteTransformer()
	id := "docker-image://other.io/app:latest"
	attrs := map[string]string{}
	inp := source.NewOpInput(id, attrs, &id, &attrs)

	dec, err := tr.Apply(context.Background(), inp, makeDec("CONVERT", map[string]any{
		"pattern":     `^docker-image://docker\.io/(.*)$`,
		"replacement": "docker-image://mirror.example.com/$1",
	}))
	require.NoError(t, err)
	assert.False(t, dec.Mutated)
	assert.Equal(t, "docker-image://other.io/app:latest", id)
}

func TestRegexRewrite_InvalidPattern_ReturnsError(t *testing.T) {
	tr := source.NewRegexRewriteTransformer()
	id := "docker-image://old:v1"
	attrs := map[string]string{}
	inp := source.NewOpInput(id, attrs, &id, &attrs)

	_, err := tr.Apply(context.Background(), inp, makeDec("CONVERT", map[string]any{
		"pattern": `[invalid(regex`,
	}))
	require.Error(t, err)
	assert.Contains(t, err.Error(), "invalid pattern")
}

func TestRegexRewrite_ActionNotConvert_NoOp(t *testing.T) {
	tr := source.NewRegexRewriteTransformer()
	id := "docker-image://old:v1"
	inp := makeInput(id, nil)
	dec, err := tr.Apply(context.Background(), inp, makeDec("DENY", map[string]any{
		"pattern": `.*`, "replacement": "new",
	}))
	require.NoError(t, err)
	assert.False(t, dec.Mutated)
}

// ─── WildcardRewriteTransformer ───────────────────────────────────────────────

func TestWildcardRewrite_SingleStar_MatchesNonSlash(t *testing.T) {
	tests := []struct {
		name   string
		id     string
		glob   string
		tmpl   string
		wantID string
	}{
		{
			name:   "tag wildcard",
			id:     "docker-image://docker.io/library/golang:1.22",
			glob:   "docker-image://docker.io/library/golang:*",
			tmpl:   "docker-image://mirror.io/library/golang:${1}",
			wantID: "docker-image://mirror.io/library/golang:1.22",
		},
		{
			name:   "no match",
			id:     "docker-image://other.io/app:v1",
			glob:   "docker-image://docker.io/*",
			tmpl:   "docker-image://mirror.io/${1}",
			wantID: "docker-image://other.io/app:v1",
		},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			tr := source.NewWildcardRewriteTransformer()
			id := tc.id
			attrs := map[string]string{}
			inp := source.NewOpInput(id, attrs, &id, &attrs)

			_, err := tr.Apply(context.Background(), inp, makeDec("CONVERT", map[string]any{
				"glob_pattern":     tc.glob,
				"glob_replacement": tc.tmpl,
			}))
			require.NoError(t, err)
			assert.Equal(t, tc.wantID, id)
		})
	}
}

func TestWildcardRewrite_DoubleStarMatchesSlash(t *testing.T) {
	tr := source.NewWildcardRewriteTransformer()
	id := "docker-image://docker.io/library/golang:1.22"
	attrs := map[string]string{}
	inp := source.NewOpInput(id, attrs, &id, &attrs)

	_, err := tr.Apply(context.Background(), inp, makeDec("CONVERT", map[string]any{
		"glob_pattern":     "docker-image://docker.io/**",
		"glob_replacement": "docker-image://mirror.io/${1}",
	}))
	require.NoError(t, err)
	assert.Equal(t, "docker-image://mirror.io/library/golang:1.22", id)
}

func TestWildcardRewrite_NoPattern_NoOp(t *testing.T) {
	tr := source.NewWildcardRewriteTransformer()
	id := "docker-image://old:v1"
	inp := makeInput(id, nil)
	dec, err := tr.Apply(context.Background(), inp, makeDec("CONVERT", map[string]any{}))
	require.NoError(t, err)
	assert.False(t, dec.Mutated)
}

// ─── DenyTransformer ─────────────────────────────────────────────────────────

func TestDeny_ReturnsSentinelError(t *testing.T) {
	d := source.NewDenyTransformer()
	_, err := d.Apply(context.Background(), nil, transform.Decision{
		Action:   "DENY",
		Messages: []string{"blocked registry"},
	})
	require.Error(t, err)
	assert.True(t, errors.Is(err, source.ErrSourceDenied))
	assert.Contains(t, err.Error(), "blocked registry")
}

func TestDeny_NoOp_WhenActionAllow(t *testing.T) {
	d := source.NewDenyTransformer()
	dec, err := d.Apply(context.Background(), nil, transform.Decision{Action: "ALLOW"})
	require.NoError(t, err)
	assert.Equal(t, "ALLOW", dec.Action)
}

func TestDeny_EmptyMessages_UsesDefault(t *testing.T) {
	d := source.NewDenyTransformer()
	_, err := d.Apply(context.Background(), nil, transform.Decision{Action: "DENY"})
	require.Error(t, err)
	assert.Contains(t, err.Error(), "denied by policy")
}

// ─── RegisterAll integration ──────────────────────────────────────────────────

func TestRegisterAll_TransformerNames(t *testing.T) {
	reg, err := transform.NewRegistry()
	require.NoError(t, err)
	source.RegisterAll(reg)

	convertTs := reg.Get(transform.Key{Kind: "source", Action: "CONVERT"})
	denyTs := reg.Get(transform.Key{Kind: "source", Action: "DENY"})

	require.Len(t, convertTs, 3, "CONVERT should have 3 transformers")
	assert.Equal(t, "source.mutate_op", convertTs[0].Name())
	assert.Equal(t, "source.regex_rewrite", convertTs[1].Name())
	assert.Equal(t, "source.wildcard_rewrite", convertTs[2].Name())

	require.Len(t, denyTs, 1, "DENY should have 1 transformer")
	assert.Equal(t, "source.deny", denyTs[0].Name())
}

// ─── Full pipeline integration test ──────────────────────────────────────────

func TestSourcePipeline_ConvertThenDenyIsCorrectlyOrdered(t *testing.T) {
	// Verify the CONVERT chain does not accidentally call DenyTransformer.
	reg, err := transform.NewRegistry()
	require.NoError(t, err)
	source.RegisterAll(reg)

	id := "docker-image://old.registry.io/app:v1"
	attrs := map[string]string{}
	inp := source.NewOpInput(id, attrs, &id, &attrs)

	dec, err := reg.ApplyAll(context.Background(),
		transform.Key{Kind: "source", Action: "CONVERT"},
		inp,
		transform.Decision{
			Action:  "CONVERT",
			Updates: map[string]any{"identifier": "docker-image://new.registry.io/app:v1"},
		},
	)
	require.NoError(t, err)
	assert.True(t, dec.Mutated)
	assert.Equal(t, "docker-image://new.registry.io/app:v1", id)
}

func TestSourcePipeline_DenyReturnsSentinelError(t *testing.T) {
	reg, err := transform.NewRegistry()
	require.NoError(t, err)
	source.RegisterAll(reg)

	id := "docker-image://blocked.example.com/app:latest"
	attrs := map[string]string{}
	inp := source.NewOpInput(id, attrs, &id, &attrs)

	_, err = reg.ApplyAll(context.Background(),
		transform.Key{Kind: "source", Action: "DENY"},
		inp,
		transform.Decision{
			Action:   "DENY",
			Messages: []string{"blocked by policy"},
		},
	)
	require.Error(t, err)
	assert.True(t, errors.Is(err, source.ErrSourceDenied))
}
