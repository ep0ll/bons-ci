package transform_test

import (
	"context"
	"errors"
	"sync"
	"testing"

	polOtel "github.com/bons/bons-ci/pkg/policy/opa/internal/otel"
	"github.com/bons/bons-ci/pkg/policy/opa/transform"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func init() { polOtel.UseNoop() }

func newRegistry(t *testing.T) *transform.Registry {
	t.Helper()
	r, err := transform.NewRegistry()
	require.NoError(t, err)
	return r
}

func makeTransformer(name string, tag string, out *[]string) transform.Transformer {
	return transform.Func(name, func(_ context.Context, _ any, d transform.Decision) (transform.Decision, error) {
		*out = append(*out, tag)
		return d, nil
	})
}

func errTransformer(name string) transform.Transformer {
	return transform.Func(name, func(_ context.Context, _ any, d transform.Decision) (transform.Decision, error) {
		return d, errors.New("forced error from " + name)
	})
}

// ─── Key.String ───────────────────────────────────────────────────────────────

func TestKey_String(t *testing.T) {
	assert.Equal(t, "source/DENY", transform.Key{Kind: "source", Action: "DENY"}.String())
	assert.Equal(t, "dag/*", transform.Key{Kind: "dag"}.String())
	assert.Equal(t, "matrix/EXPAND", transform.Key{Kind: "matrix", Action: "EXPAND"}.String())
}

// ─── Register / Get ───────────────────────────────────────────────────────────

func TestRegistry_Get_EmptyReturnsNil(t *testing.T) {
	r := newRegistry(t)
	ts := r.Get(transform.Key{Kind: "source", Action: "DENY"})
	assert.Nil(t, ts)
}

func TestRegistry_Register_ThenGet(t *testing.T) {
	r := newRegistry(t)
	var order []string
	r.Register(transform.Key{Kind: "source", Action: "CONVERT"},
		makeTransformer("a", "a", &order),
		makeTransformer("b", "b", &order),
	)
	ts := r.Get(transform.Key{Kind: "source", Action: "CONVERT"})
	require.Len(t, ts, 2)
}

func TestRegistry_SpecificBeforeWildcard(t *testing.T) {
	r := newRegistry(t)
	var order []string
	r.Register(transform.Key{Kind: "source", Action: ""},
		makeTransformer("wild", "wild", &order),
	)
	r.Register(transform.Key{Kind: "source", Action: "CONVERT"},
		makeTransformer("specific", "specific", &order),
	)
	ts := r.Get(transform.Key{Kind: "source", Action: "CONVERT"})
	require.Len(t, ts, 2)

	dec := transform.Decision{Action: "CONVERT"}
	for _, t2 := range ts {
		dec, _ = t2.Apply(context.Background(), nil, dec)
	}
	assert.Equal(t, []string{"specific", "wild"}, order)
}

func TestRegistry_WildcardOnlyMatchesKind(t *testing.T) {
	r := newRegistry(t)
	var order []string
	r.Register(transform.Key{Kind: "source", Action: ""},
		makeTransformer("wild", "wild", &order),
	)
	ts := r.Get(transform.Key{Kind: "source", Action: "DENY"})
	require.Len(t, ts, 1)
	assert.Equal(t, "wild", ts[0].Name())
}

func TestRegistry_DifferentKinds_NotMixed(t *testing.T) {
	r := newRegistry(t)
	r.Register(transform.Key{Kind: "source", Action: "DENY"},
		makeTransformer("source-deny", "sd", nil),
	)
	r.Register(transform.Key{Kind: "dag", Action: "EXPAND"},
		makeTransformer("dag-expand", "de", nil),
	)
	sourceTs := r.Get(transform.Key{Kind: "source", Action: "DENY"})
	dagTs := r.Get(transform.Key{Kind: "dag", Action: "EXPAND"})
	require.Len(t, sourceTs, 1)
	require.Len(t, dagTs, 1)
	assert.Equal(t, "source-deny", sourceTs[0].Name())
	assert.Equal(t, "dag-expand", dagTs[0].Name())
}

// ─── ApplyAll ─────────────────────────────────────────────────────────────────

func TestApplyAll_ExecutesInOrder(t *testing.T) {
	r := newRegistry(t)
	var order []string
	r.Register(transform.Key{Kind: "k", Action: "A"},
		makeTransformer("t1", "1", &order),
		makeTransformer("t2", "2", &order),
		makeTransformer("t3", "3", &order),
	)
	dec, err := r.ApplyAll(context.Background(), transform.Key{Kind: "k", Action: "A"}, nil,
		transform.Decision{Action: "A"})
	require.NoError(t, err)
	assert.Equal(t, []string{"1", "2", "3"}, order)
	assert.Equal(t, "A", dec.Action)
}

func TestApplyAll_StopsOnFirstError(t *testing.T) {
	r := newRegistry(t)
	var order []string
	r.Register(transform.Key{Kind: "k", Action: "A"},
		makeTransformer("t1", "1", &order),
		errTransformer("t2"),
		makeTransformer("t3", "3", &order),
	)
	_, err := r.ApplyAll(context.Background(), transform.Key{Kind: "k", Action: "A"}, nil,
		transform.Decision{Action: "A"})
	require.Error(t, err)
	assert.Contains(t, err.Error(), "t2")
	assert.Equal(t, []string{"1"}, order, "t3 must not run after error")
}

func TestApplyAll_NoTransformers_ReturnsSameDec(t *testing.T) {
	r := newRegistry(t)
	dec := transform.Decision{Action: "ALLOW", Mutated: false}
	out, err := r.ApplyAll(context.Background(), transform.Key{Kind: "x", Action: "A"}, nil, dec)
	require.NoError(t, err)
	assert.Equal(t, dec.Action, out.Action)
}

func TestApplyAll_TransformerMutatesDecision(t *testing.T) {
	r := newRegistry(t)
	r.Register(transform.Key{Kind: "k", Action: "C"},
		transform.Func("mutator", func(_ context.Context, _ any, d transform.Decision) (transform.Decision, error) {
			d.Mutated = true
			d.Messages = append(d.Messages, "mutated")
			return d, nil
		}),
	)
	dec, err := r.ApplyAll(context.Background(), transform.Key{Kind: "k", Action: "C"}, nil,
		transform.Decision{Action: "C"})
	require.NoError(t, err)
	assert.True(t, dec.Mutated)
	assert.Contains(t, dec.Messages, "mutated")
}

// ─── Decision.Clone ───────────────────────────────────────────────────────────

func TestDecision_Clone_DeepCopy(t *testing.T) {
	orig := transform.Decision{
		Action:   "CONVERT",
		Mutated:  true,
		Messages: []string{"a", "b"},
		Updates:  map[string]any{"id": "old"},
		Expansions: []map[string]any{
			{"node": "x"},
		},
	}
	clone := orig.Clone()

	// Mutate the clone — original must be unchanged.
	clone.Messages = append(clone.Messages, "c")
	clone.Updates["extra"] = "extra"
	clone.Expansions[0]["extra"] = "e"

	assert.Equal(t, []string{"a", "b"}, orig.Messages)
	assert.NotContains(t, orig.Updates, "extra")
	assert.NotContains(t, orig.Expansions[0], "extra")
}

// ─── Functional helpers ───────────────────────────────────────────────────────

func TestChain_ExecutesAll(t *testing.T) {
	var order []string
	t1 := makeTransformer("t1", "1", &order)
	t2 := makeTransformer("t2", "2", &order)
	chained := transform.Chain("chained", t1, t2)

	dec, err := chained.Apply(context.Background(), nil, transform.Decision{Action: "A"})
	require.NoError(t, err)
	assert.Equal(t, []string{"1", "2"}, order)
	_ = dec
}

func TestChain_StopsOnError(t *testing.T) {
	var order []string
	t1 := makeTransformer("t1", "1", &order)
	t2 := errTransformer("t2")
	t3 := makeTransformer("t3", "3", &order)
	chained := transform.Chain("c", t1, t2, t3)

	_, err := chained.Apply(context.Background(), nil, transform.Decision{Action: "A"})
	require.Error(t, err)
	assert.Equal(t, []string{"1"}, order)
}

func TestGuard_PredicateFalse_DoesNotRun(t *testing.T) {
	var ran bool
	inner := transform.Func("inner", func(_ context.Context, _ any, d transform.Decision) (transform.Decision, error) {
		ran = true
		return d, nil
	})
	guarded := transform.Guard("guarded", func(d transform.Decision) bool { return false }, inner)
	_, err := guarded.Apply(context.Background(), nil, transform.Decision{Action: "X"})
	require.NoError(t, err)
	assert.False(t, ran)
}

func TestGuard_PredicateTrue_Runs(t *testing.T) {
	var ran bool
	inner := transform.Func("inner", func(_ context.Context, _ any, d transform.Decision) (transform.Decision, error) {
		ran = true
		return d, nil
	})
	guarded := transform.Guard("guarded", func(d transform.Decision) bool { return true }, inner)
	_, err := guarded.Apply(context.Background(), nil, transform.Decision{Action: "X"})
	require.NoError(t, err)
	assert.True(t, ran)
}

func TestActionGuard_MatchingAction_Runs(t *testing.T) {
	var ran bool
	inner := transform.Func("inner", func(_ context.Context, _ any, d transform.Decision) (transform.Decision, error) {
		ran = true
		return d, nil
	})
	guarded := transform.ActionGuard("DENY", inner)
	_, _ = guarded.Apply(context.Background(), nil, transform.Decision{Action: "DENY"})
	assert.True(t, ran)
}

func TestActionGuard_NonMatchingAction_DoesNotRun(t *testing.T) {
	var ran bool
	inner := transform.Func("inner", func(_ context.Context, _ any, d transform.Decision) (transform.Decision, error) {
		ran = true
		return d, nil
	})
	guarded := transform.ActionGuard("DENY", inner)
	_, _ = guarded.Apply(context.Background(), nil, transform.Decision{Action: "ALLOW"})
	assert.False(t, ran)
}

// ─── Concurrency ─────────────────────────────────────────────────────────────

func TestRegistry_ConcurrentRegisterAndGet(t *testing.T) {
	r := newRegistry(t)
	var wg sync.WaitGroup

	for i := 0; i < 20; i++ {
		wg.Add(2)
		go func(idx int) {
			defer wg.Done()
			r.Register(transform.Key{Kind: "src", Action: "CONVERT"},
				makeTransformer("t", "x", nil),
			)
		}(i)
		go func() {
			defer wg.Done()
			_ = r.Get(transform.Key{Kind: "src", Action: "CONVERT"})
		}()
	}
	wg.Wait()
}
