// Package engine is the policy orchestrator that wires OPA evaluation, the
// event bus, and the transform registry into a single cohesive pipeline.
//
// Processing pipeline for each PolicyRequest:
//
//  1. Serialise input → OPA-compatible JSON map
//  2. Evaluate data.buildkit.policy.<kind> query via Evaluator
//  3. Decode OPA result → Decision
//  4. Publish KindPolicyEvaluated on the bus
//  5. Run transform chain via Registry.ApplyAll
//  6. Publish specific action event (KindSourceDenied, KindSourceConverted, etc.)
//  7. Return final Decision to caller
//
// All steps are wrapped in an OTEL span; errors record on the span and return
// to the caller without suppression.
package engine

import (
	"context"
	"encoding/json"
	"fmt"
	"time"

	"go.opentelemetry.io/otel/attribute"
	"go.opentelemetry.io/otel/metric"
	"go.opentelemetry.io/otel/trace"

	"github.com/bons/bons-ci/pkg/policy/opa/internal/eval"
	"github.com/bons/bons-ci/pkg/policy/opa/internal/events"
	polOtel "github.com/bons/bons-ci/pkg/policy/opa/internal/otel"
	"github.com/bons/bons-ci/pkg/policy/opa/transform"
)

// ─── Event kinds ─────────────────────────────────────────────────────────────

const (
	KindPolicyEvaluated events.Kind = "policy.evaluated"
	KindSourceAllowed   events.Kind = "policy.source.allowed"
	KindSourceDenied    events.Kind = "policy.source.denied"
	KindSourceConverted events.Kind = "policy.source.converted"
	KindDAGExpanded     events.Kind = "policy.dag.expanded"
	KindMatrixExpanded  events.Kind = "policy.matrix.expanded"
)

// ─── Action constants ─────────────────────────────────────────────────────────

type Action string

const (
	ActionAllow   Action = "ALLOW"
	ActionDeny    Action = "DENY"
	ActionConvert Action = "CONVERT"
	ActionExpand  Action = "EXPAND"
)

// ─── Request / Decision ───────────────────────────────────────────────────────

// PolicyRequest is the input to Engine.Process.
type PolicyRequest struct {
	// Kind selects the OPA query suffix: "source", "dag", "matrix", …
	Kind string
	// Input is passed to OPA (serialised via JSON) AND to transforms (typed).
	// For source policies, pass the live OpInput; OPA receives the JSON form.
	Input any
	// OPAInput overrides the JSON sent to OPA when Input cannot be serialised
	// as the correct JSON shape. If nil, Input is used.
	OPAInput any
	// Meta propagates arbitrary key-value pairs through events.
	Meta map[string]string
}

// Decision is the engine's output.
type Decision struct {
	Action     Action
	Mutated    bool
	Messages   []string
	Updates    map[string]any
	Expansions []map[string]any
}

// ─── Event payloads ───────────────────────────────────────────────────────────

// PolicyEvaluatedPayload is emitted after every OPA evaluation.
type PolicyEvaluatedPayload struct {
	Request  PolicyRequest
	Decision Decision
	Duration time.Duration
}

// ActionEventPayload is emitted for specific action outcomes.
type ActionEventPayload struct {
	Request  PolicyRequest
	Decision Decision
}

// ─── Engine ───────────────────────────────────────────────────────────────────

// Engine orchestrates OPA evaluation, event delivery, and transforms.
// It is safe for concurrent use.
type Engine struct {
	evaluator *eval.Evaluator
	bus       *events.Bus
	registry  *transform.Registry

	tracer    trace.Tracer
	total     metric.Int64Counter
	errors    metric.Int64Counter
	latencyMs metric.Float64Histogram
}

// NewEngine assembles an Engine from its dependencies.
func NewEngine(ev *eval.Evaluator, bus *events.Bus, reg *transform.Registry) (*Engine, error) {
	m := polOtel.Meter("engine")
	prefix := polOtel.Namespace + ".engine"

	total, err := m.Int64Counter(prefix+".requests_total",
		metric.WithDescription("Total policy engine Process calls"))
	if err != nil {
		return nil, fmt.Errorf("engine: metric requests: %w", err)
	}
	errs, err := m.Int64Counter(prefix+".errors_total",
		metric.WithDescription("Process calls that returned an error"))
	if err != nil {
		return nil, fmt.Errorf("engine: metric errors: %w", err)
	}
	lat, err := m.Float64Histogram(prefix+".latency_ms",
		metric.WithDescription("End-to-end Process latency in ms"),
		metric.WithUnit("ms"),
		metric.WithExplicitBucketBoundaries(0.1, 0.5, 1, 5, 10, 25, 50, 100, 250),
	)
	if err != nil {
		return nil, fmt.Errorf("engine: metric latency: %w", err)
	}

	return &Engine{
		evaluator: ev,
		bus:       bus,
		registry:  reg,
		tracer:    polOtel.Tracer("engine"),
		total:     total,
		errors:    errs,
		latencyMs: lat,
	}, nil
}

// Process evaluates req through the full pipeline and returns a Decision.
// It never swallows errors: evaluation errors, transform errors, and denial
// errors are all returned to the caller.
func (e *Engine) Process(ctx context.Context, req PolicyRequest) (Decision, error) {
	start := time.Now()

	ctx, span := e.tracer.Start(ctx, polOtel.Namespace+".engine.process",
		trace.WithAttributes(
			polOtel.AttrPolicyKind.String(req.Kind),
		),
	)
	var retErr error
	defer func() {
		elapsed := float64(time.Since(start).Microseconds()) / 1000.0
		attrs := metric.WithAttributes(
			polOtel.AttrPolicyKind.String(req.Kind),
		)
		e.total.Add(ctx, 1, attrs)
		e.latencyMs.Record(ctx, elapsed, attrs)
		if retErr != nil {
			e.errors.Add(ctx, 1, attrs)
			polOtel.RecordError(ctx, retErr)
		}
		span.End()
	}()

	// 1. Build OPA input.
	opaInput, err := e.buildOPAInput(req)
	if err != nil {
		retErr = fmt.Errorf("engine: build input: %w", err)
		return Decision{}, retErr
	}

	// 2. Evaluate OPA policy.
	query := fmt.Sprintf("data.buildkit.policy.%s.result", req.Kind)
	raw, err := e.evaluator.EvalObject(ctx, query, opaInput)
	if err != nil {
		retErr = fmt.Errorf("engine: eval %q: %w", query, err)
		return Decision{}, retErr
	}

	// 3. Decode result → Decision.
	dec := decodeDecision(raw)
	span.SetAttributes(
		polOtel.AttrAction.String(string(dec.Action)),
	)

	// 4. Publish evaluation event.
	events.PublishTyped(ctx, e.bus, events.Event[PolicyEvaluatedPayload]{
		Kind: KindPolicyEvaluated,
		Payload: PolicyEvaluatedPayload{
			Request:  req,
			Decision: dec,
			Duration: time.Since(start),
		},
		Meta: req.Meta,
	})

	// 5. Run transform pipeline.
	key := transform.Key{Kind: req.Kind, Action: string(dec.Action)}
	transformed, tErr := e.registry.ApplyAll(ctx, key, req.Input, toTransformDecision(dec))
	if tErr != nil {
		// Update dec with whatever partial mutations occurred before the error.
		dec = fromTransformDecision(transformed)
		dec.Mutated = transformed.Mutated
		retErr = tErr
		e.publishActionEvent(ctx, req, dec)
		return dec, retErr
	}
	dec = fromTransformDecision(transformed)

	span.SetAttributes(polOtel.AttrMutated.Bool(dec.Mutated))

	// 6. Publish fine-grained action event.
	e.publishActionEvent(ctx, req, dec)

	return dec, nil
}

// ─── OPA input construction ───────────────────────────────────────────────────

func (e *Engine) buildOPAInput(req PolicyRequest) (map[string]any, error) {
	// Use OPAInput override if provided, otherwise serialise Input.
	src := req.Input
	if req.OPAInput != nil {
		src = req.OPAInput
	}

	if src == nil {
		return map[string]any{}, nil
	}

	b, err := json.Marshal(src)
	if err != nil {
		return nil, fmt.Errorf("json.Marshal input: %w", err)
	}
	var m map[string]any
	if err := json.Unmarshal(b, &m); err != nil {
		// Input might be a non-object (slice, scalar). Wrap it.
		return map[string]any{"value": src}, nil
	}
	if req.Meta != nil {
		m["_meta"] = req.Meta
	}
	return m, nil
}

// ─── Decision codec ───────────────────────────────────────────────────────────

func decodeDecision(raw map[string]any) Decision {
	if raw == nil {
		return Decision{Action: ActionAllow}
	}

	action := Action(stringField(raw, "action"))
	if action == "" {
		action = ActionAllow
	}

	var msgs []string
	if rawMsgs, ok := raw["messages"].([]any); ok {
		for _, m := range rawMsgs {
			if s, ok := m.(string); ok {
				msgs = append(msgs, s)
			}
		}
	}

	var updates map[string]any
	if u, ok := raw["updates"].(map[string]any); ok {
		updates = u
	}

	var expansions []map[string]any
	if rawExp, ok := raw["expansions"].([]any); ok {
		for _, item := range rawExp {
			if m, ok := item.(map[string]any); ok {
				expansions = append(expansions, m)
			}
		}
	}

	return Decision{
		Action:     action,
		Messages:   msgs,
		Updates:    updates,
		Expansions: expansions,
	}
}

func stringField(m map[string]any, key string) string {
	v, _ := m[key].(string)
	return v
}

// ─── Decision / transform.Decision bridging ───────────────────────────────────

func toTransformDecision(d Decision) transform.Decision {
	return transform.Decision{
		Action:     string(d.Action),
		Mutated:    d.Mutated,
		Messages:   d.Messages,
		Updates:    d.Updates,
		Expansions: d.Expansions,
	}
}

func fromTransformDecision(d transform.Decision) Decision {
	return Decision{
		Action:     Action(d.Action),
		Mutated:    d.Mutated,
		Messages:   d.Messages,
		Updates:    d.Updates,
		Expansions: d.Expansions,
	}
}

// ─── Action event publishing ──────────────────────────────────────────────────

func (e *Engine) publishActionEvent(ctx context.Context, req PolicyRequest, dec Decision) {
	p := ActionEventPayload{Request: req, Decision: dec}

	var kind events.Kind
	switch dec.Action {
	case ActionAllow:
		kind = KindSourceAllowed
	case ActionDeny:
		kind = KindSourceDenied
	case ActionConvert:
		kind = KindSourceConverted
	case ActionExpand:
		if req.Kind == "dag" {
			kind = KindDAGExpanded
		} else {
			kind = KindMatrixExpanded
		}
	default:
		return
	}

	events.PublishTyped(ctx, e.bus, events.Event[ActionEventPayload]{
		Kind:    kind,
		Payload: p,
		Meta:    req.Meta,
	})

	polOtel.AddEvent(ctx, string(kind),
		attribute.String("kind", req.Kind),
		attribute.String("action", string(dec.Action)),
		attribute.Bool("mutated", dec.Mutated),
	)
}
