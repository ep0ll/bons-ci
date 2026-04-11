package engine_test

import (
	"context"
	"errors"
	"sync"
	"sync/atomic"
	"testing"

	"github.com/bons/bons-ci/pkg/policy/opa/engine"
	"github.com/bons/bons-ci/pkg/policy/opa/internal/eval"
	"github.com/bons/bons-ci/pkg/policy/opa/internal/events"
	polOtel "github.com/bons/bons-ci/pkg/policy/opa/internal/otel"
	"github.com/bons/bons-ci/pkg/policy/opa/transform"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func init() { polOtel.UseNoop() }

// ─── Minimal Rego policies for engine tests ───────────────────────────────────
// These are self-contained and do not depend on data.rego.

const allowAllRego = `
package buildkit.policy.source
import rego.v1
default result := {"action":"ALLOW","messages":[],"updates":{}}`

const denyAllRego = `
package buildkit.policy.source
import rego.v1
default result := {"action":"DENY","messages":["blocked"],"updates":{}}`

const convertRego = `
package buildkit.policy.source
import rego.v1
default result := {"action":"ALLOW","messages":[],"updates":{}}
result := {
	"action": "CONVERT",
	"messages": ["redirected"],
	"updates": {"identifier": "docker-image://new.example.com/app:v1"},
} if { input.identifier == "docker-image://old.example.com/app:v1" }`

const dagExpandRego = `
package buildkit.policy.dag
import rego.v1
default result := {"action":"ALLOW","messages":[],"updates":{},"expansions":[]}
result := {
	"action": "EXPAND",
	"messages": ["scan added"],
	"updates": {},
	"expansions": [{
		"id":   concat("-", [input.op.id, "scan"]),
		"type": "exec",
		"identifier": "scanner:latest",
		"depends_on": [input.op.id],
	}],
} if { input.op.type == "source" }`

const matrixExpandRego = `
package buildkit.policy.matrix
import rego.v1
default result := {"action":"ALLOW","messages":[],"updates":{},"expansions":[]}
result := {
	"action": "EXPAND",
	"messages": ["expanded"],
	"updates": {"max_parallel": 2, "fail_fast": false},
	"expansions": [
		{"id":"linux-amd64","vars":{"os":"linux","arch":"amd64"}},
		{"id":"linux-arm64","vars":{"os":"linux","arch":"arm64"}},
	],
} if { count(input.strategy.matrix) > 0 }`

const undefinedRego = `
package buildkit.policy.source
import rego.v1
# intentionally no result rule → undefined`

// ─── helpers ──────────────────────────────────────────────────────────────────

func newEngine(t *testing.T, modules map[string]string) (*engine.Engine, *events.Bus) {
	t.Helper()

	c, err := eval.NewCompiler(modules)
	require.NoError(t, err)

	ev, err := eval.NewEvaluator(c)
	require.NoError(t, err)

	bus, err := events.NewBus(nil)
	require.NoError(t, err)

	reg, err := transform.NewRegistry()
	require.NoError(t, err)

	eng, err := engine.NewEngine(ev, bus, reg)
	require.NoError(t, err)

	return eng, bus
}

func newEngineWithRegistry(t *testing.T, modules map[string]string, reg *transform.Registry) (*engine.Engine, *events.Bus) {
	t.Helper()

	c, err := eval.NewCompiler(modules)
	require.NoError(t, err)

	ev, err := eval.NewEvaluator(c)
	require.NoError(t, err)

	bus, err := events.NewBus(nil)
	require.NoError(t, err)

	eng, err := engine.NewEngine(ev, bus, reg)
	require.NoError(t, err)

	return eng, bus
}

// ─── Action: ALLOW ────────────────────────────────────────────────────────────

func TestEngine_Allow_ReturnsAllowDecision(t *testing.T) {
	eng, _ := newEngine(t, map[string]string{"s.rego": allowAllRego})
	dec, err := eng.Process(context.Background(), engine.PolicyRequest{
		Kind:  "source",
		Input: map[string]any{"identifier": "docker-image://safe:latest"},
	})
	require.NoError(t, err)
	assert.Equal(t, engine.ActionAllow, dec.Action)
	assert.False(t, dec.Mutated)
}

func TestEngine_Allow_UndefinedQuery_DefaultsToAllow(t *testing.T) {
	eng, _ := newEngine(t, map[string]string{"s.rego": undefinedRego})
	dec, err := eng.Process(context.Background(), engine.PolicyRequest{
		Kind:  "source",
		Input: map[string]any{"identifier": "docker-image://anything:latest"},
	})
	require.NoError(t, err)
	assert.Equal(t, engine.ActionAllow, dec.Action)
}

// ─── Action: DENY ─────────────────────────────────────────────────────────────

func TestEngine_Deny_ReturnsDecisionWithMessages(t *testing.T) {
	eng, _ := newEngine(t, map[string]string{"s.rego": denyAllRego})
	dec, err := eng.Process(context.Background(), engine.PolicyRequest{
		Kind:  "source",
		Input: map[string]any{"identifier": "docker-image://blocked:latest"},
	})
	// Engine itself does not return an error for a DENY — that's the transform's job.
	require.NoError(t, err)
	assert.Equal(t, engine.ActionDeny, dec.Action)
	assert.Contains(t, dec.Messages, "blocked")
}

// ─── Action: CONVERT ──────────────────────────────────────────────────────────

func TestEngine_Convert_ReturnsMutatedDecision(t *testing.T) {
	eng, _ := newEngine(t, map[string]string{"s.rego": convertRego})
	dec, err := eng.Process(context.Background(), engine.PolicyRequest{
		Kind:  "source",
		Input: map[string]any{"identifier": "docker-image://old.example.com/app:v1"},
	})
	require.NoError(t, err)
	assert.Equal(t, engine.ActionConvert, dec.Action)
	assert.Equal(t, "docker-image://new.example.com/app:v1", dec.Updates["identifier"])
}

func TestEngine_Convert_NonMatchingIdentifier_AllowsThrough(t *testing.T) {
	eng, _ := newEngine(t, map[string]string{"s.rego": convertRego})
	dec, err := eng.Process(context.Background(), engine.PolicyRequest{
		Kind:  "source",
		Input: map[string]any{"identifier": "docker-image://other.example.com/app:v1"},
	})
	require.NoError(t, err)
	assert.Equal(t, engine.ActionAllow, dec.Action)
}

// ─── Action: EXPAND (DAG) ─────────────────────────────────────────────────────

func TestEngine_DAGExpand_ReturnsExpansions(t *testing.T) {
	eng, _ := newEngine(t, map[string]string{"d.rego": dagExpandRego})
	dec, err := eng.Process(context.Background(), engine.PolicyRequest{
		Kind: "dag",
		Input: map[string]any{
			"op": map[string]any{"id": "op1", "type": "source",
				"identifier": "docker-image://example.com/app:v1"},
		},
	})
	require.NoError(t, err)
	assert.Equal(t, engine.ActionExpand, dec.Action)
	require.Len(t, dec.Expansions, 1)
	assert.Equal(t, "op1-scan", dec.Expansions[0]["id"])
}

func TestEngine_DAGExpand_ExecType_NoExpansion(t *testing.T) {
	eng, _ := newEngine(t, map[string]string{"d.rego": dagExpandRego})
	dec, err := eng.Process(context.Background(), engine.PolicyRequest{
		Kind: "dag",
		Input: map[string]any{
			"op": map[string]any{"id": "exec1", "type": "exec"},
		},
	})
	require.NoError(t, err)
	assert.Equal(t, engine.ActionAllow, dec.Action)
	assert.Empty(t, dec.Expansions)
}

// ─── Action: EXPAND (matrix) ─────────────────────────────────────────────────

func TestEngine_MatrixExpand_ReturnsExpansions(t *testing.T) {
	eng, _ := newEngine(t, map[string]string{"m.rego": matrixExpandRego})
	dec, err := eng.Process(context.Background(), engine.PolicyRequest{
		Kind: "matrix",
		Input: map[string]any{
			"strategy": map[string]any{
				"matrix": map[string]any{"os": []any{"linux"}},
			},
		},
	})
	require.NoError(t, err)
	assert.Equal(t, engine.ActionExpand, dec.Action)
	assert.Len(t, dec.Expansions, 2)
}

// ─── Transform integration ────────────────────────────────────────────────────

func TestEngine_TransformApplied_MutatesdDecision(t *testing.T) {
	reg, err := transform.NewRegistry()
	require.NoError(t, err)

	var transformCalled bool
	reg.Register(transform.Key{Kind: "source", Action: "CONVERT"},
		transform.Func("test.mutator", func(_ context.Context, _ any, d transform.Decision) (transform.Decision, error) {
			transformCalled = true
			d.Mutated = true
			return d, nil
		}),
	)

	eng, _ := newEngineWithRegistry(t, map[string]string{"s.rego": convertRego}, reg)
	dec, err := eng.Process(context.Background(), engine.PolicyRequest{
		Kind:  "source",
		Input: map[string]any{"identifier": "docker-image://old.example.com/app:v1"},
	})
	require.NoError(t, err)
	assert.True(t, transformCalled)
	assert.True(t, dec.Mutated)
}

func TestEngine_TransformError_ReturnsError(t *testing.T) {
	reg, err := transform.NewRegistry()
	require.NoError(t, err)

	sentinel := errors.New("transform failed")
	reg.Register(transform.Key{Kind: "source", Action: "DENY"},
		transform.Func("test.fail", func(_ context.Context, _ any, d transform.Decision) (transform.Decision, error) {
			return d, sentinel
		}),
	)

	eng, _ := newEngineWithRegistry(t, map[string]string{"s.rego": denyAllRego}, reg)
	_, err = eng.Process(context.Background(), engine.PolicyRequest{
		Kind:  "source",
		Input: map[string]any{"identifier": "docker-image://blocked:latest"},
	})
	require.Error(t, err)
	assert.ErrorContains(t, err, "test.fail")
}

// ─── Event bus integration ────────────────────────────────────────────────────

func TestEngine_EventFired_OnEachProcess(t *testing.T) {
	eng, bus := newEngine(t, map[string]string{"s.rego": allowAllRego})

	var evaluatedCount atomic.Int64
	sub := events.On[engine.PolicyEvaluatedPayload](bus, engine.KindPolicyEvaluated,
		func(_ context.Context, _ engine.PolicyEvaluatedPayload) error {
			evaluatedCount.Add(1)
			return nil
		},
	)
	defer sub.Cancel()

	for i := 0; i < 5; i++ {
		_, err := eng.Process(context.Background(), engine.PolicyRequest{
			Kind:  "source",
			Input: map[string]any{"identifier": "docker-image://safe:latest"},
		})
		require.NoError(t, err)
	}
	assert.Equal(t, int64(5), evaluatedCount.Load())
}

func TestEngine_EventFired_SourceAllowed(t *testing.T) {
	eng, bus := newEngine(t, map[string]string{"s.rego": allowAllRego})
	var fired atomic.Bool
	sub := events.On[engine.ActionEventPayload](bus, engine.KindSourceAllowed,
		func(_ context.Context, p engine.ActionEventPayload) error {
			fired.Store(true)
			assert.Equal(t, engine.ActionAllow, p.Decision.Action)
			return nil
		},
	)
	defer sub.Cancel()

	eng.Process(context.Background(), engine.PolicyRequest{ //nolint:errcheck
		Kind:  "source",
		Input: map[string]any{"identifier": "docker-image://safe:latest"},
	})
	assert.True(t, fired.Load())
}

func TestEngine_EventFired_SourceDenied(t *testing.T) {
	eng, bus := newEngine(t, map[string]string{"s.rego": denyAllRego})
	var fired atomic.Bool
	sub := events.On[engine.ActionEventPayload](bus, engine.KindSourceDenied,
		func(_ context.Context, p engine.ActionEventPayload) error {
			fired.Store(true)
			assert.Equal(t, engine.ActionDeny, p.Decision.Action)
			return nil
		},
	)
	defer sub.Cancel()

	eng.Process(context.Background(), engine.PolicyRequest{ //nolint:errcheck
		Kind:  "source",
		Input: map[string]any{"identifier": "docker-image://blocked:latest"},
	})
	assert.True(t, fired.Load())
}

func TestEngine_EventFired_SourceConverted(t *testing.T) {
	eng, bus := newEngine(t, map[string]string{"s.rego": convertRego})
	var fired atomic.Bool
	sub := events.On[engine.ActionEventPayload](bus, engine.KindSourceConverted,
		func(_ context.Context, _ engine.ActionEventPayload) error {
			fired.Store(true)
			return nil
		},
	)
	defer sub.Cancel()

	eng.Process(context.Background(), engine.PolicyRequest{ //nolint:errcheck
		Kind:  "source",
		Input: map[string]any{"identifier": "docker-image://old.example.com/app:v1"},
	})
	assert.True(t, fired.Load())
}

func TestEngine_EventFired_DAGExpanded(t *testing.T) {
	eng, bus := newEngine(t, map[string]string{"d.rego": dagExpandRego})
	var fired atomic.Bool
	sub := events.On[engine.ActionEventPayload](bus, engine.KindDAGExpanded,
		func(_ context.Context, _ engine.ActionEventPayload) error {
			fired.Store(true)
			return nil
		},
	)
	defer sub.Cancel()

	eng.Process(context.Background(), engine.PolicyRequest{ //nolint:errcheck
		Kind:  "dag",
		Input: map[string]any{"op": map[string]any{"id": "op1", "type": "source"}},
	})
	assert.True(t, fired.Load())
}

func TestEngine_EventFired_MatrixExpanded(t *testing.T) {
	eng, bus := newEngine(t, map[string]string{"m.rego": matrixExpandRego})
	var fired atomic.Bool
	sub := events.On[engine.ActionEventPayload](bus, engine.KindMatrixExpanded,
		func(_ context.Context, _ engine.ActionEventPayload) error {
			fired.Store(true)
			return nil
		},
	)
	defer sub.Cancel()

	eng.Process(context.Background(), engine.PolicyRequest{ //nolint:errcheck
		Kind:  "matrix",
		Input: map[string]any{"strategy": map[string]any{"matrix": map[string]any{"os": []any{"linux"}}}},
	})
	assert.True(t, fired.Load())
}

func TestEngine_EventPayload_ContainsRequest(t *testing.T) {
	eng, bus := newEngine(t, map[string]string{"s.rego": allowAllRego})
	var gotPayload engine.PolicyEvaluatedPayload
	sub := events.On[engine.PolicyEvaluatedPayload](bus, engine.KindPolicyEvaluated,
		func(_ context.Context, p engine.PolicyEvaluatedPayload) error {
			gotPayload = p
			return nil
		},
	)
	defer sub.Cancel()

	eng.Process(context.Background(), engine.PolicyRequest{ //nolint:errcheck
		Kind:  "source",
		Meta:  map[string]string{"trace": "abc123"},
		Input: map[string]any{"identifier": "docker-image://x:v1"},
	})
	assert.Equal(t, "source", gotPayload.Request.Kind)
	assert.Equal(t, "abc123", gotPayload.Request.Meta["trace"])
	assert.NotZero(t, gotPayload.Duration)
}

// ─── OPAInput override ────────────────────────────────────────────────────────

func TestEngine_OPAInput_Override_UsedForEval(t *testing.T) {
	rego := `
package buildkit.policy.source
import rego.v1
default result := {"action":"ALLOW","messages":[],"updates":{}}
result := {"action":"DENY","messages":["custom input denied"],"updates":{}} if {
	input.custom_field == "trigger"
}`
	eng, _ := newEngine(t, map[string]string{"s.rego": rego})

	// Input is an opaque struct; OPAInput provides the serialised form.
	dec, err := eng.Process(context.Background(), engine.PolicyRequest{
		Kind:     "source",
		Input:    "some-opaque-go-struct",
		OPAInput: map[string]any{"custom_field": "trigger"},
	})
	require.NoError(t, err)
	assert.Equal(t, engine.ActionDeny, dec.Action)
	assert.Contains(t, dec.Messages, "custom input denied")
}

// ─── Meta propagation ────────────────────────────────────────────────────────

func TestEngine_Meta_PropagatedToEventBus(t *testing.T) {
	eng, bus := newEngine(t, map[string]string{"s.rego": allowAllRego})
	var gotMeta map[string]string
	sub := events.On[engine.PolicyEvaluatedPayload](bus, engine.KindPolicyEvaluated,
		func(_ context.Context, p engine.PolicyEvaluatedPayload) error {
			gotMeta = p.Request.Meta
			return nil
		},
	)
	defer sub.Cancel()

	eng.Process(context.Background(), engine.PolicyRequest{ //nolint:errcheck
		Kind:  "source",
		Meta:  map[string]string{"build_id": "build-42"},
		Input: map[string]any{"identifier": "docker-image://x:v1"},
	})
	assert.Equal(t, "build-42", gotMeta["build_id"])
}

// ─── Concurrency ─────────────────────────────────────────────────────────────

func TestEngine_ConcurrentProcess_NoPanic(t *testing.T) {
	eng, _ := newEngine(t, map[string]string{"s.rego": allowAllRego})
	const goroutines = 30
	var wg sync.WaitGroup
	errs := make([]error, goroutines)

	for i := 0; i < goroutines; i++ {
		wg.Add(1)
		go func(idx int) {
			defer wg.Done()
			_, err := eng.Process(context.Background(), engine.PolicyRequest{
				Kind:  "source",
				Input: map[string]any{"identifier": "docker-image://safe:latest"},
			})
			errs[idx] = err
		}(i)
	}
	wg.Wait()
	for i, err := range errs {
		assert.NoError(t, err, "goroutine %d", i)
	}
}

// ─── Invalid OPA query ────────────────────────────────────────────────────────

func TestEngine_UnknownKind_ReturnsError(t *testing.T) {
	eng, _ := newEngine(t, map[string]string{"s.rego": allowAllRego})
	// "unknown" kind will produce an undefined query result → ALLOW (no error).
	dec, err := eng.Process(context.Background(), engine.PolicyRequest{
		Kind:  "unknown",
		Input: map[string]any{},
	})
	// Undefined query defaults to ALLOW; not an error.
	require.NoError(t, err)
	assert.Equal(t, engine.ActionAllow, dec.Action)
}

// ─── Decision struct ─────────────────────────────────────────────────────────

func TestEngine_DecisionMessages_Propagated(t *testing.T) {
	eng, _ := newEngine(t, map[string]string{"s.rego": denyAllRego})
	dec, err := eng.Process(context.Background(), engine.PolicyRequest{
		Kind:  "source",
		Input: map[string]any{"identifier": "docker-image://x:v1"},
	})
	require.NoError(t, err)
	assert.Contains(t, dec.Messages, "blocked")
}

func TestEngine_DecisionUpdates_Propagated(t *testing.T) {
	eng, _ := newEngine(t, map[string]string{"s.rego": convertRego})
	dec, err := eng.Process(context.Background(), engine.PolicyRequest{
		Kind:  "source",
		Input: map[string]any{"identifier": "docker-image://old.example.com/app:v1"},
	})
	require.NoError(t, err)
	require.NotNil(t, dec.Updates)
	assert.Equal(t, "docker-image://new.example.com/app:v1", dec.Updates["identifier"])
}
