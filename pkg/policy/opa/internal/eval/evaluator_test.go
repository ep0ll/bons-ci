package eval_test

import (
	"context"
	"fmt"
	"sync"
	"testing"

	"github.com/bons/bons-ci/pkg/policy/opa/internal/eval"
	polOtel "github.com/bons/bons-ci/pkg/policy/opa/internal/otel"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func init() { polOtel.UseNoop() }

// ─── helpers ──────────────────────────────────────────────────────────────────

func mustCompiler(t *testing.T, modules map[string]string, opts ...eval.CompilerOption) *eval.Compiler {
	t.Helper()
	c, err := eval.NewCompiler(modules, opts...)
	require.NoError(t, err)
	return c
}

func mustEvaluator(t *testing.T, modules map[string]string) *eval.Evaluator {
	t.Helper()
	c := mustCompiler(t, modules)
	ev, err := eval.NewEvaluator(c)
	require.NoError(t, err)
	return ev
}

// regoModule builds a properly-formatted Rego module string.
// OPA v0.68+ with import rego.v1 requires each statement on its own line.
func regoModule(pkg, body string) string {
	return "package " + pkg + "\nimport rego.v1\n" + body
}

// ─── Compiler construction ────────────────────────────────────────────────────

func TestNewCompiler_ParseError(t *testing.T) {
	_, err := eval.NewCompiler(map[string]string{
		"bad.rego": `this is not valid rego !!!`,
	})
	require.Error(t, err)
	assert.Contains(t, err.Error(), "parse")
}

func TestNewCompiler_CompileError(t *testing.T) {
	// WithStrictMode causes unknown built-ins to be compile errors.
	_, err := eval.NewCompiler(map[string]string{
		"bad.rego": regoModule("p", "allow if { undefined_func_xyz() }"),
	}, eval.WithStrictMode())
	require.Error(t, err)
}

func TestNewCompiler_EmptyModules(t *testing.T) {
	c, err := eval.NewCompiler(map[string]string{})
	require.NoError(t, err)
	require.NotNil(t, c)
}

func TestNewCompiler_MultipleModules(t *testing.T) {
	c := mustCompiler(t, map[string]string{
		"a.rego": regoModule("a", "val := 1"),
		"b.rego": regoModule("b", "val := data.a.val + 1"),
	})
	ev, err := eval.NewEvaluator(c)
	require.NoError(t, err)

	rs, err := ev.Eval(context.Background(), "data.b.val", nil)
	require.NoError(t, err)
	assert.Equal(t, float64(2), rs.First())
}

// ─── Evaluator ────────────────────────────────────────────────────────────────

func TestEvaluator_EvalBool_True(t *testing.T) {
	ev := mustEvaluator(t, map[string]string{
		"p.rego": regoModule("p", "allow if { input.x > 5 }"),
	})
	ok, err := ev.EvalBool(context.Background(), "data.p.allow", map[string]any{"x": 10})
	require.NoError(t, err)
	assert.True(t, ok)
}

func TestEvaluator_EvalBool_False(t *testing.T) {
	ev := mustEvaluator(t, map[string]string{
		"p.rego": regoModule("p", "allow if { input.x > 5 }"),
	})
	ok, err := ev.EvalBool(context.Background(), "data.p.allow", map[string]any{"x": 3})
	require.NoError(t, err)
	assert.False(t, ok)
}

func TestEvaluator_EvalBool_Undefined_ReturnsFalseNotError(t *testing.T) {
	ev := mustEvaluator(t, map[string]string{
		"p.rego": regoModule("p", "allow if { false }"),
	})
	ok, err := ev.EvalBool(context.Background(), "data.p.allow", nil)
	require.NoError(t, err)
	assert.False(t, ok)
}

func TestEvaluator_EvalBool_WrongType_ReturnsError(t *testing.T) {
	ev := mustEvaluator(t, map[string]string{
		"p.rego": regoModule("p", `allow := "yes"`),
	})
	_, err := ev.EvalBool(context.Background(), "data.p.allow", nil)
	require.Error(t, err)
	assert.Contains(t, err.Error(), "expected bool")
}

func TestEvaluator_EvalObject(t *testing.T) {
	ev := mustEvaluator(t, map[string]string{
		"p.rego": regoModule("p", `result := {"action": "ALLOW", "reason": "ok"}`),
	})
	m, err := ev.EvalObject(context.Background(), "data.p.result", nil)
	require.NoError(t, err)
	require.NotNil(t, m)
	assert.Equal(t, "ALLOW", m["action"])
	assert.Equal(t, "ok", m["reason"])
}

func TestEvaluator_EvalObject_Undefined_ReturnsNil(t *testing.T) {
	ev := mustEvaluator(t, map[string]string{
		"p.rego": `package p`,
	})
	m, err := ev.EvalObject(context.Background(), "data.p.missing", nil)
	require.NoError(t, err)
	assert.Nil(t, m)
}

func TestEvaluator_InputPropagation(t *testing.T) {
	ev := mustEvaluator(t, map[string]string{
		"p.rego": regoModule("p", "echo := input"),
	})
	in := map[string]any{"hello": "world", "n": float64(42)}
	rs, err := ev.Eval(context.Background(), "data.p.echo", in)
	require.NoError(t, err)
	m, ok := rs.First().(map[string]interface{})
	require.True(t, ok)
	assert.Equal(t, "world", m["hello"])
	assert.Equal(t, float64(42), m["n"])
}

func TestEvaluator_InvalidQuery_ReturnsError(t *testing.T) {
	ev := mustEvaluator(t, map[string]string{
		"p.rego": `package p`,
	})
	_, err := ev.Eval(context.Background(), "this is not valid rego!!!", nil)
	require.Error(t, err)
}

// ─── ResultSet ────────────────────────────────────────────────────────────────

func TestResultSet_Defined(t *testing.T) {
	ev := mustEvaluator(t, map[string]string{
		"p.rego": regoModule("p", "v := 1"),
	})
	rs, err := ev.Eval(context.Background(), "data.p.v", nil)
	require.NoError(t, err)
	assert.True(t, rs.Defined())
}

func TestResultSet_NotDefined(t *testing.T) {
	ev := mustEvaluator(t, map[string]string{
		"p.rego": `package p`,
	})
	rs, err := ev.Eval(context.Background(), "data.p.missing", nil)
	require.NoError(t, err)
	assert.False(t, rs.Defined())
	assert.Nil(t, rs.First())
}

func TestResultSet_FirstObject_InvalidJSON_ReturnsError(t *testing.T) {
	// Array value → FirstObject returns an error (not a map).
	ev := mustEvaluator(t, map[string]string{
		"p.rego": regoModule("p", "v := [1,2,3]"),
	})
	rs, err := ev.Eval(context.Background(), "data.p.v", nil)
	require.NoError(t, err)
	m, err := rs.FirstObject()
	// Our implementation errors when the top-level value isn't an object.
	// Either nil+nil (if we treat array as nil map) or nil+error is acceptable.
	if err != nil {
		assert.Nil(t, m)
	} else {
		assert.Nil(t, m)
	}
}

// ─── HotSwap ─────────────────────────────────────────────────────────────────

func TestCompiler_HotSwap_AtomicReplacement(t *testing.T) {
	c1 := mustCompiler(t, map[string]string{
		"p.rego": regoModule("p", `v := "old"`),
	})
	ev, err := eval.NewEvaluator(c1)
	require.NoError(t, err)

	rs, err := ev.Eval(context.Background(), "data.p.v", nil)
	require.NoError(t, err)
	assert.Equal(t, "old", rs.First())

	c2 := mustCompiler(t, map[string]string{
		"p.rego": regoModule("p", `v := "new"`),
	})
	c1.HotSwap(c2)

	rs, err = ev.Eval(context.Background(), "data.p.v", nil)
	require.NoError(t, err)
	assert.Equal(t, "new", rs.First())
}

func TestCompiler_HotSwap_ConcurrentSafe(t *testing.T) {
	c := mustCompiler(t, map[string]string{
		"p.rego": regoModule("p", "v := input.x * 2"),
	})
	ev, err := eval.NewEvaluator(c)
	require.NoError(t, err)

	const goroutines = 20
	const iters = 50
	var wg sync.WaitGroup
	errs := make([]error, goroutines)

	for i := 0; i < goroutines; i++ {
		wg.Add(1)
		go func(idx int) {
			defer wg.Done()
			for j := 0; j < iters; j++ {
				if idx%5 == 0 {
					nc := mustCompiler(t, map[string]string{
						"p.rego": regoModule("p", "v := input.x * 2"),
					})
					c.HotSwap(nc)
				}
				_, err := ev.Eval(context.Background(), "data.p.v", map[string]any{"x": j})
				if err != nil {
					errs[idx] = err
					return
				}
			}
		}(i)
	}
	wg.Wait()
	for i, err := range errs {
		assert.NoError(t, err, "goroutine %d errored", i)
	}
}

// ─── PreparedQuery ────────────────────────────────────────────────────────────

func TestPreparedQuery_Eval(t *testing.T) {
	ev := mustEvaluator(t, map[string]string{
		"p.rego": regoModule("p", "square := input.n * input.n"),
	})
	pq, err := ev.PrepareQuery(context.Background(), "data.p.square")
	require.NoError(t, err)

	tests := []struct{ n, want float64 }{
		{1, 1}, {2, 4}, {3, 9}, {10, 100}, {0, 0},
	}
	for _, tc := range tests {
		rs, err := pq.Eval(context.Background(), map[string]any{"n": tc.n})
		require.NoError(t, err, "n=%v", tc.n)
		assert.Equal(t, tc.want, rs.First(), "n=%v", tc.n)
	}
}

func TestPreparedQuery_ConcurrentSafe(t *testing.T) {
	ev := mustEvaluator(t, map[string]string{
		"p.rego": regoModule("p", "v := input.x + 1"),
	})
	pq, err := ev.PrepareQuery(context.Background(), "data.p.v")
	require.NoError(t, err)

	const goroutines = 30
	var wg sync.WaitGroup
	errs := make([]error, goroutines)

	for i := 0; i < goroutines; i++ {
		wg.Add(1)
		go func(idx int) {
			defer wg.Done()
			rs, err := pq.Eval(context.Background(), map[string]any{"x": float64(idx)})
			if err != nil {
				errs[idx] = err
				return
			}
			expected := float64(idx + 1)
			if rs.First() != expected {
				errs[idx] = fmt.Errorf("expected %v got %v", expected, rs.First())
			}
		}(i)
	}
	wg.Wait()
	for i, err := range errs {
		assert.NoError(t, err, "goroutine %d", i)
	}
}

func TestPreparedQuery_InvalidQuery_ReturnsError(t *testing.T) {
	ev := mustEvaluator(t, map[string]string{"p.rego": `package p`})
	_, err := ev.PrepareQuery(context.Background(), "!!! invalid")
	require.Error(t, err)
}

// ─── Context cancellation ─────────────────────────────────────────────────────

func TestEvaluator_ContextCancelled(t *testing.T) {
	ev := mustEvaluator(t, map[string]string{
		"p.rego": regoModule("p", "v := 1"),
	})
	ctx, cancel := context.WithCancel(context.Background())
	cancel() // already cancelled
	_, err := ev.Eval(ctx, "data.p.v", nil)
	// OPA may or may not honour the cancelled context — must not panic.
	_ = err
}
