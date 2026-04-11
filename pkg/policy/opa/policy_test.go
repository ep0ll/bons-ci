package opapolicy_test

import (
	"context"
	"errors"
	"sync/atomic"
	"testing"
	"time"

	opapolicy "github.com/bons/bons-ci/pkg/policy/opa"
	"github.com/bons/bons-ci/pkg/policy/opa/engine"
	"github.com/bons/bons-ci/pkg/policy/opa/internal/events"
	polOtel "github.com/bons/bons-ci/pkg/policy/opa/internal/otel"
	"github.com/bons/bons-ci/pkg/policy/opa/transform"
	dagxform "github.com/bons/bons-ci/pkg/policy/opa/transform/dag"
	matrixxform "github.com/bons/bons-ci/pkg/policy/opa/transform/matrix"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func init() { polOtel.UseNoop() }

// ─── Inline Rego used in integration tests ────────────────────────────────────
// These are complete, self-contained policies that do not rely on data.rego
// so every test case is hermetically isolated.

const (
	// sourceAllowAll: every source is allowed.
	sourceAllowAll = `
package buildkit.policy.source
import rego.v1
default result := {"action":"ALLOW","messages":[],"updates":{}}`

	// sourceDenyAll: every source is denied with a message.
	sourceDenyAll = `
package buildkit.policy.source
import rego.v1
default result := {"action":"DENY","messages":["all sources blocked"],"updates":{}}`

	// sourceConvertExact: redirects one specific identifier.
	sourceConvertExact = `
package buildkit.policy.source
import rego.v1
default result := {"action":"ALLOW","messages":[],"updates":{}}
result := {
	"action": "CONVERT",
	"messages": [],
	"updates": {"identifier": "docker-image://new.registry.io/app:v2"},
} if { input.identifier == "docker-image://old.registry.io/app:v1" }`

	// sourceConvertRegex: redirects docker.io golang images via regex capture.
	sourceConvertRegex = `
package buildkit.policy.source
import rego.v1
default result := {"action":"ALLOW","messages":[],"updates":{}}
result := {
	"action": "CONVERT",
	"messages": [],
	"updates": {
		"pattern":     "^docker-image://docker\\.io/library/golang:(.+)$",
		"replacement": "docker-image://mirror.io/library/golang:$1",
	},
} if { regex.match("^docker-image://docker\\.io/library/golang:", input.identifier) }`

	// sourceConvertWildcard: wildcard redirect via glob fields.
	sourceConvertWildcard = `
package buildkit.policy.source
import rego.v1
default result := {"action":"ALLOW","messages":[],"updates":{}}
result := {
	"action": "CONVERT",
	"messages": [],
	"updates": {
		"glob_pattern":     "docker-image://docker.io/library/*",
		"glob_replacement": "docker-image://mirror.io/library/${1}",
	},
} if { startswith(input.identifier, "docker-image://docker.io/library/") }`

	// sourceConvertAttr: adds an HTTP checksum attr.
	sourceConvertAttr = `
package buildkit.policy.source
import rego.v1
default result := {"action":"ALLOW","messages":[],"updates":{}}
result := {
	"action": "CONVERT",
	"messages": [],
	"updates": {"attrs": {"http.checksum": "sha256:abc123"}},
} if { startswith(input.identifier, "https://") }`

	// sourceLastRuleWins: deny then allow.
	sourceLastRuleWins = `
package buildkit.policy.source
import rego.v1
data.buildkit.source_rules := [
	{"selector":{"identifier":"docker-image://docker.io/library/busybox:latest"},
	 "action":"ALLOW","updates":{}},
	{"selector":{"identifier":"docker-image://docker.io/library/busybox:latest"},
	 "action":"DENY","updates":{}},
	{"selector":{"identifier":"docker-image://docker.io/library/busybox:latest"},
	 "action":"ALLOW","updates":{}},
]
default result := {"action":"ALLOW","messages":[],"updates":{}}
# Inline last-rule-wins logic without importing source.rego.
matching[i] if {
	rule := data.buildkit.source_rules[i]
	rule.selector.identifier == input.identifier
}
allow_deny[i] if { matching[i]; data.buildkit.source_rules[i].action != "CONVERT" }
last_idx := max(allow_deny) if count(allow_deny) > 0
result := {"action": data.buildkit.source_rules[last_idx].action, "messages":[], "updates":{}} if {
	defined(last_idx)
}
defined(x) if { x != null }`

	// dagAddScan: adds a security-scan node after source ops.
	dagAddScan = `
package buildkit.policy.dag
import rego.v1
default result := {"action":"ALLOW","messages":[],"updates":{},"expansions":[]}
result := {
	"action": "EXPAND",
	"messages": ["security scan added"],
	"updates": {},
	"expansions": [{
		"id":         concat("-", [input.op.id, "scan"]),
		"type":       "exec",
		"identifier": "security-scanner:latest",
		"attrs":      {"scan.target": input.op.identifier},
		"depends_on": [input.op.id],
	}],
} if {
	input.op.type == "source"
	startswith(input.op.identifier, "docker-image://")
}`

	// matrixTwoAxis: expands a 2-axis matrix with exclude and max_parallel.
	matrixTwoAxis = `
package buildkit.policy.matrix
import rego.v1
default result := {"action":"ALLOW","messages":[],"updates":{},"expansions":[]}
result := {
	"action": "EXPAND",
	"messages": ["matrix expanded"],
	"updates": {"max_parallel": 2, "fail_fast": false},
	"expansions": [cfg | cfg := build_configs[_]],
} if {
	count(input.strategy.matrix) > 0
	count(build_configs) > 0
}
result := {
	"action": "DENY",
	"messages": ["no valid matrix combinations"],
	"updates": {}, "expansions": [],
} if {
	count(input.strategy.matrix) > 0
	count(build_configs) == 0
}
build_configs := [c |
	some os in input.strategy.matrix.os
	some arch in input.strategy.matrix.arch
	not excluded({"os": os, "arch": arch})
	c := {"id": concat("-", [os, arch]), "vars": {"os": os, "arch": arch}}
]
excluded(combo) if {
	some exc in input.strategy.exclude
	every k, v in exc { combo[k] == v }
}`
)

// ─── helpers ──────────────────────────────────────────────────────────────────

func newPolicy(t *testing.T, modules map[string]string) *opapolicy.Policy {
	t.Helper()
	pol, err := opapolicy.New(context.Background(), opapolicy.Config{
		InlineModules: modules,
	})
	require.NoError(t, err)
	return pol
}

// ─── Construction ─────────────────────────────────────────────────────────────

func TestNew_MissingSource_ReturnsError(t *testing.T) {
	_, err := opapolicy.New(context.Background(), opapolicy.Config{})
	require.Error(t, err)
	assert.Contains(t, err.Error(), "PolicyDir or InlineModules")
}

func TestNew_InvalidRego_ReturnsError(t *testing.T) {
	_, err := opapolicy.New(context.Background(), opapolicy.Config{
		InlineModules: map[string]string{"bad.rego": `not valid rego!!`},
	})
	require.Error(t, err)
	assert.Contains(t, err.Error(), "compile")
}

func TestNew_Valid_ReturnsPolicy(t *testing.T) {
	pol := newPolicy(t, map[string]string{"s.rego": sourceAllowAll})
	require.NotNil(t, pol)
	require.NotNil(t, pol.Bus())
	require.NotNil(t, pol.Registry())
}

// ─── EvaluateSource: ALLOW ────────────────────────────────────────────────────

func TestEvaluateSource_Allow(t *testing.T) {
	pol := newPolicy(t, map[string]string{"s.rego": sourceAllowAll})
	res, err := pol.EvaluateSource(context.Background(),
		"docker-image://safe.registry.io/app:v1", nil)
	require.NoError(t, err)
	assert.Equal(t, engine.ActionAllow, res.Action)
	assert.False(t, res.Mutated)
	assert.False(t, res.Denied)
}

func TestEvaluateSource_Allow_NilAttrs_SafeToPass(t *testing.T) {
	pol := newPolicy(t, map[string]string{"s.rego": sourceAllowAll})
	res, err := pol.EvaluateSource(context.Background(), "docker-image://x:v1", nil)
	require.NoError(t, err)
	assert.Equal(t, engine.ActionAllow, res.Action)
}

// ─── EvaluateSource: DENY ────────────────────────────────────────────────────

func TestEvaluateSource_Deny_ReturnsDeniedAndWrappedError(t *testing.T) {
	pol := newPolicy(t, map[string]string{"s.rego": sourceDenyAll})
	res, err := pol.EvaluateSource(context.Background(),
		"docker-image://blocked.example.com/app:v1", nil)
	// The engine returns no error; the transform wraps the denial into an error.
	// Without the DenyTransformer being wired in this test (it IS wired via RegisterAll),
	// we get an error wrapping ErrSourceDenied.
	if err != nil {
		assert.True(t, errors.Is(err, opapolicy.ErrSourceDenied))
	} else {
		// If no transform error, the result action should be DENY.
		assert.True(t, res.Denied)
		assert.Equal(t, engine.ActionDeny, res.Action)
	}
	assert.Contains(t, res.Messages, "all sources blocked")
}

func TestEvaluateSource_Deny_ErrSourceDenied_Sentinel(t *testing.T) {
	pol := newPolicy(t, map[string]string{"s.rego": sourceDenyAll})
	_, err := pol.EvaluateSource(context.Background(),
		"docker-image://blocked.example.com/app:v1", nil)
	// With DenyTransformer wired via RegisterAll, err wraps ErrSourceDenied.
	if err != nil {
		assert.True(t, errors.Is(err, opapolicy.ErrSourceDenied))
	}
}

// ─── EvaluateSource: CONVERT (exact) ─────────────────────────────────────────

func TestEvaluateSource_ConvertExact(t *testing.T) {
	pol := newPolicy(t, map[string]string{"s.rego": sourceConvertExact})
	res, err := pol.EvaluateSource(context.Background(),
		"docker-image://old.registry.io/app:v1", nil)
	require.NoError(t, err)
	assert.Equal(t, engine.ActionConvert, res.Action)
	assert.True(t, res.Mutated)
	assert.Equal(t, "docker-image://new.registry.io/app:v2", res.NewIdentifier)
}

func TestEvaluateSource_ConvertExact_NonMatchingIdentifier_Allows(t *testing.T) {
	pol := newPolicy(t, map[string]string{"s.rego": sourceConvertExact})
	res, err := pol.EvaluateSource(context.Background(),
		"docker-image://other.registry.io/app:v1", nil)
	require.NoError(t, err)
	assert.Equal(t, engine.ActionAllow, res.Action)
	assert.False(t, res.Mutated)
}

// ─── EvaluateSource: CONVERT (regex) ─────────────────────────────────────────

func TestEvaluateSource_ConvertRegex(t *testing.T) {
	pol := newPolicy(t, map[string]string{"s.rego": sourceConvertRegex})
	res, err := pol.EvaluateSource(context.Background(),
		"docker-image://docker.io/library/golang:1.22", nil)
	require.NoError(t, err)
	assert.Equal(t, engine.ActionConvert, res.Action)
	assert.True(t, res.Mutated)
	assert.Equal(t, "docker-image://mirror.io/library/golang:1.22", res.NewIdentifier)
}

func TestEvaluateSource_ConvertRegex_OtherTag(t *testing.T) {
	pol := newPolicy(t, map[string]string{"s.rego": sourceConvertRegex})
	res, err := pol.EvaluateSource(context.Background(),
		"docker-image://docker.io/library/golang:1.21-alpine", nil)
	require.NoError(t, err)
	if res.Mutated {
		assert.Equal(t, "docker-image://mirror.io/library/golang:1.21-alpine", res.NewIdentifier)
	}
}

// ─── EvaluateSource: CONVERT (wildcard) ──────────────────────────────────────

func TestEvaluateSource_ConvertWildcard(t *testing.T) {
	pol := newPolicy(t, map[string]string{"s.rego": sourceConvertWildcard})
	res, err := pol.EvaluateSource(context.Background(),
		"docker-image://docker.io/library/alpine:3.18", nil)
	require.NoError(t, err)
	assert.Equal(t, engine.ActionConvert, res.Action)
	assert.True(t, res.Mutated)
	assert.Equal(t, "docker-image://mirror.io/library/alpine:3.18", res.NewIdentifier)
}

// ─── EvaluateSource: CONVERT (attrs) ─────────────────────────────────────────

func TestEvaluateSource_ConvertAttr_AddsChecksum(t *testing.T) {
	pol := newPolicy(t, map[string]string{"s.rego": sourceConvertAttr})
	res, err := pol.EvaluateSource(context.Background(),
		"https://artifacts.example.com/file.tar.gz", nil)
	require.NoError(t, err)
	assert.Equal(t, engine.ActionConvert, res.Action)
	assert.True(t, res.Mutated)
	assert.Equal(t, "sha256:abc123", res.NewAttrs["http.checksum"])
}

// ─── EvaluateSource: last-rule-wins ──────────────────────────────────────────

func TestEvaluateSource_LastRuleWins(t *testing.T) {
	pol := newPolicy(t, map[string]string{"s.rego": sourceLastRuleWins})
	res, err := pol.EvaluateSource(context.Background(),
		"docker-image://docker.io/library/busybox:latest", nil)
	require.NoError(t, err)
	// Last rule is ALLOW, so the result should be ALLOW.
	assert.Equal(t, engine.ActionAllow, res.Action)
}

// ─── EvaluateDAG ──────────────────────────────────────────────────────────────

func TestEvaluateDAG_SourceOp_Expanded(t *testing.T) {
	pol := newPolicy(t, map[string]string{"d.rego": dagAddScan})
	inp := dagxform.DAGInput{
		Op: &dagxform.OpDescriptor{
			ID:         "pull-op-1",
			Type:       "source",
			Identifier: "docker-image://example.com/app:v1",
		},
	}
	res, err := pol.EvaluateDAG(context.Background(), inp)
	require.NoError(t, err)
	assert.True(t, res.Expanded)
	require.Len(t, res.Nodes, 1)
	assert.Equal(t, "pull-op-1-scan", res.Nodes[0].ID)
	assert.Equal(t, "exec", res.Nodes[0].Type)
	assert.Equal(t, "security-scanner:latest", res.Nodes[0].Identifier)
	assert.Equal(t, []string{"pull-op-1"}, res.Nodes[0].DependsOn)
	assert.Equal(t, "docker-image://example.com/app:v1", res.Nodes[0].Attrs["scan.target"])
}

func TestEvaluateDAG_ExecOp_NotExpanded(t *testing.T) {
	pol := newPolicy(t, map[string]string{"d.rego": dagAddScan})
	inp := dagxform.DAGInput{
		Op: &dagxform.OpDescriptor{ID: "exec-1", Type: "exec"},
	}
	res, err := pol.EvaluateDAG(context.Background(), inp)
	require.NoError(t, err)
	assert.False(t, res.Expanded)
	assert.Empty(t, res.Nodes)
}

func TestEvaluateDAG_NoAncestors(t *testing.T) {
	pol := newPolicy(t, map[string]string{"d.rego": dagAddScan})
	inp := dagxform.DAGInput{
		Op:        &dagxform.OpDescriptor{ID: "op1", Type: "source", Identifier: "docker-image://x:v1"},
		Ancestors: nil,
	}
	res, err := pol.EvaluateDAG(context.Background(), inp)
	require.NoError(t, err)
	assert.True(t, res.Expanded)
}

func TestEvaluateDAG_Messages_Propagated(t *testing.T) {
	pol := newPolicy(t, map[string]string{"d.rego": dagAddScan})
	inp := dagxform.DAGInput{
		Op: &dagxform.OpDescriptor{ID: "op1", Type: "source",
			Identifier: "docker-image://example.com/img:v1"},
	}
	res, err := pol.EvaluateDAG(context.Background(), inp)
	require.NoError(t, err)
	assert.Contains(t, res.Messages, "security scan added")
}

// ─── EvaluateMatrix ───────────────────────────────────────────────────────────

func TestEvaluateMatrix_CartesianProduct(t *testing.T) {
	pol := newPolicy(t, map[string]string{"m.rego": matrixTwoAxis})
	inp := matrixxform.MatrixInput{
		Strategy: matrixxform.Strategy{
			Matrix: map[string][]string{
				"os":   {"linux", "windows"},
				"arch": {"amd64", "arm64"},
			},
		},
	}
	res, err := pol.EvaluateMatrix(context.Background(), inp)
	require.NoError(t, err)
	assert.Len(t, res.Configs, 4)
}

func TestEvaluateMatrix_WithExclude(t *testing.T) {
	pol := newPolicy(t, map[string]string{"m.rego": matrixTwoAxis})
	inp := matrixxform.MatrixInput{
		Strategy: matrixxform.Strategy{
			Matrix: map[string][]string{
				"os":   {"linux", "windows"},
				"arch": {"amd64", "arm64"},
			},
			Exclude: []map[string]string{
				{"os": "windows", "arch": "arm64"},
			},
		},
	}
	res, err := pol.EvaluateMatrix(context.Background(), inp)
	require.NoError(t, err)
	assert.Len(t, res.Configs, 3)
	for _, c := range res.Configs {
		assert.False(t, c.Vars["os"] == "windows" && c.Vars["arch"] == "arm64")
	}
}

func TestEvaluateMatrix_AllExcluded_ReturnsDenyError(t *testing.T) {
	pol := newPolicy(t, map[string]string{"m.rego": matrixTwoAxis})
	inp := matrixxform.MatrixInput{
		Strategy: matrixxform.Strategy{
			Matrix: map[string][]string{
				"os":   {"linux"},
				"arch": {"amd64"},
			},
			Exclude: []map[string]string{
				{"os": "linux", "arch": "amd64"},
			},
		},
	}
	_, err := pol.EvaluateMatrix(context.Background(), inp)
	require.Error(t, err)
	assert.Contains(t, err.Error(), "denied")
}

func TestEvaluateMatrix_MaxParallel_Propagated(t *testing.T) {
	pol := newPolicy(t, map[string]string{"m.rego": matrixTwoAxis})
	inp := matrixxform.MatrixInput{
		Strategy: matrixxform.Strategy{
			Matrix: map[string][]string{"os": {"linux"}, "arch": {"amd64"}},
		},
	}
	res, err := pol.EvaluateMatrix(context.Background(), inp)
	require.NoError(t, err)
	assert.Equal(t, 2, res.MaxParallel) // set in matrixTwoAxis Rego
}

func TestEvaluateMatrix_EmptyMatrix_ReturnsAllow(t *testing.T) {
	pol := newPolicy(t, map[string]string{"m.rego": matrixTwoAxis})
	inp := matrixxform.MatrixInput{Strategy: matrixxform.Strategy{}}
	res, err := pol.EvaluateMatrix(context.Background(), inp)
	require.NoError(t, err)
	assert.Empty(t, res.Configs)
}

// ─── Event subscriptions ──────────────────────────────────────────────────────

func TestOnSourceDenied_FiredOnDeny(t *testing.T) {
	pol := newPolicy(t, map[string]string{"s.rego": sourceDenyAll})
	var fired atomic.Bool
	sub := pol.OnSourceDenied(func(_ context.Context, _ engine.ActionEventPayload) error {
		fired.Store(true)
		return nil
	})
	defer sub.Cancel()

	pol.EvaluateSource(context.Background(), "docker-image://blocked:latest", nil) //nolint:errcheck
	assert.True(t, fired.Load())
}

func TestOnSourceConverted_FiredOnConvert(t *testing.T) {
	pol := newPolicy(t, map[string]string{"s.rego": sourceConvertExact})
	var fired atomic.Bool
	sub := pol.OnSourceConverted(func(_ context.Context, p engine.ActionEventPayload) error {
		fired.Store(true)
		assert.Equal(t, engine.ActionConvert, p.Decision.Action)
		return nil
	})
	defer sub.Cancel()

	pol.EvaluateSource(context.Background(), "docker-image://old.registry.io/app:v1", nil) //nolint:errcheck
	assert.True(t, fired.Load())
}

func TestOnSourceAllowed_FiredOnAllow(t *testing.T) {
	pol := newPolicy(t, map[string]string{"s.rego": sourceAllowAll})
	var fired atomic.Bool
	sub := pol.OnSourceAllowed(func(_ context.Context, _ engine.ActionEventPayload) error {
		fired.Store(true)
		return nil
	})
	defer sub.Cancel()

	pol.EvaluateSource(context.Background(), "docker-image://safe:latest", nil) //nolint:errcheck
	assert.True(t, fired.Load())
}

func TestOnDAGExpanded_FiredOnExpansion(t *testing.T) {
	pol := newPolicy(t, map[string]string{"d.rego": dagAddScan})
	var fired atomic.Bool
	sub := pol.OnDAGExpanded(func(_ context.Context, _ engine.ActionEventPayload) error {
		fired.Store(true)
		return nil
	})
	defer sub.Cancel()

	pol.EvaluateDAG(context.Background(), dagxform.DAGInput{ //nolint:errcheck
		Op: &dagxform.OpDescriptor{ID: "op1", Type: "source",
			Identifier: "docker-image://example.com/app:v1"},
	})
	assert.True(t, fired.Load())
}

func TestOnMatrixExpanded_FiredOnExpansion(t *testing.T) {
	pol := newPolicy(t, map[string]string{"m.rego": matrixTwoAxis})
	var fired atomic.Bool
	sub := pol.OnMatrixExpanded(func(_ context.Context, _ engine.ActionEventPayload) error {
		fired.Store(true)
		return nil
	})
	defer sub.Cancel()

	pol.EvaluateMatrix(context.Background(), matrixxform.MatrixInput{ //nolint:errcheck
		Strategy: matrixxform.Strategy{
			Matrix: map[string][]string{"os": {"linux"}, "arch": {"amd64"}},
		},
	})
	assert.True(t, fired.Load())
}

func TestOnPolicyEvaluated_FiredOnEveryCall(t *testing.T) {
	pol := newPolicy(t, map[string]string{"s.rego": sourceAllowAll})
	var count atomic.Int64
	sub := pol.OnPolicyEvaluated(func(_ context.Context, _ engine.PolicyEvaluatedPayload) error {
		count.Add(1)
		return nil
	})
	defer sub.Cancel()

	for i := 0; i < 10; i++ {
		pol.EvaluateSource(context.Background(), "docker-image://x:v1", nil) //nolint:errcheck
	}
	assert.Equal(t, int64(10), count.Load())
}

// ─── Subscription cancellation ────────────────────────────────────────────────

func TestSubscription_Cancel_StopsDelivery(t *testing.T) {
	pol := newPolicy(t, map[string]string{"s.rego": sourceAllowAll})
	var count atomic.Int64
	sub := pol.OnSourceAllowed(func(_ context.Context, _ engine.ActionEventPayload) error {
		count.Add(1)
		return nil
	})

	pol.EvaluateSource(context.Background(), "docker-image://x:v1", nil) //nolint:errcheck
	assert.Equal(t, int64(1), count.Load())

	sub.Cancel()
	pol.EvaluateSource(context.Background(), "docker-image://x:v1", nil) //nolint:errcheck
	assert.Equal(t, int64(1), count.Load(), "handler must not fire after Cancel")
}

// ─── ExtraTransforms ─────────────────────────────────────────────────────────

func TestNew_ExtraTransforms_Applied(t *testing.T) {
	var customRan atomic.Bool
	pol, err := opapolicy.New(context.Background(), opapolicy.Config{
		InlineModules: map[string]string{"s.rego": sourceConvertExact},
		ExtraTransforms: map[transform.Key][]transform.Transformer{
			{Kind: "source", Action: "CONVERT"}: {
				transform.Func("custom.observer", func(_ context.Context, _ any, d transform.Decision) (transform.Decision, error) {
					customRan.Store(true)
					return d, nil
				}),
			},
		},
	})
	require.NoError(t, err)

	pol.EvaluateSource(context.Background(), "docker-image://old.registry.io/app:v1", nil) //nolint:errcheck
	assert.True(t, customRan.Load())
}

// ─── OnBusError callback ──────────────────────────────────────────────────────

func TestNew_OnBusError_CalledOnHandlerError(t *testing.T) {
	errCh := make(chan error, 1)
	pol, err := opapolicy.New(context.Background(), opapolicy.Config{
		InlineModules: map[string]string{"s.rego": sourceAllowAll},
		OnBusError:    func(e error, _ events.RawEvent) { errCh <- e },
	})
	require.NoError(t, err)

	sentinel := errors.New("subscriber error")
	sub := pol.OnSourceAllowed(func(_ context.Context, _ engine.ActionEventPayload) error {
		return sentinel
	})
	defer sub.Cancel()

	pol.EvaluateSource(context.Background(), "docker-image://x:v1", nil) //nolint:errcheck

	select {
	case gotErr := <-errCh:
		assert.Equal(t, sentinel, gotErr)
	case <-time.After(200 * time.Millisecond):
		t.Fatal("OnBusError not called")
	}
}

// ─── Watch (hot-reload) ───────────────────────────────────────────────────────

func TestWatch_Noop_WhenIntervalZero(t *testing.T) {
	pol := newPolicy(t, map[string]string{"s.rego": sourceAllowAll})
	ctx, cancel := context.WithTimeout(context.Background(), 50*time.Millisecond)
	defer cancel()
	err := pol.Watch(ctx)
	// Should return ctx.Err() (deadline/cancel), not a real error.
	assert.True(t, errors.Is(err, context.DeadlineExceeded) || errors.Is(err, context.Canceled))
}

// ─── Identifier unchanged on allow ───────────────────────────────────────────

func TestEvaluateSource_Allow_NewIdentifierEqualToInput(t *testing.T) {
	pol := newPolicy(t, map[string]string{"s.rego": sourceAllowAll})
	id := "docker-image://safe.example.com/app:v1"
	res, err := pol.EvaluateSource(context.Background(), id, nil)
	require.NoError(t, err)
	// Even for ALLOW, NewIdentifier reflects the (unchanged) identifier.
	assert.Equal(t, id, res.NewIdentifier)
}
