// Package opapolicy is the high-level public API for OPA-driven build policy.
//
// It wires together the low-level primitives (eval, events, bundle) with the
// mid-level engine and transform registry, then exposes simple, domain-typed
// Evaluate* methods and event subscription helpers.
//
// Callers should import only this package. Sub-packages are for extension
// authors who need to register custom transforms or Rego modules.
//
// Quickstart:
//
//	pol, err := opapolicy.New(ctx, opapolicy.Config{
//	    PolicyDir: "/etc/buildkit/policies",
//	    WatchInterval: 30 * time.Second,
//	})
//	go pol.Watch(ctx) // hot-reload in background
//
//	result, err := pol.EvaluateSource(ctx, "docker-image://example.com/app:v1",
//	    map[string]string{"http.checksum": ""})
//
//	result, err := pol.EvaluateMatrix(ctx, matrix.MatrixInput{...})
//	result, err := pol.EvaluateDAG(ctx, dag.DAGInput{...})
package opapolicy

import (
	"context"
	"errors"
	"fmt"
	"time"

	"github.com/bons/bons-ci/pkg/policy/opa/engine"
	"github.com/bons/bons-ci/pkg/policy/opa/internal/bundle"
	"github.com/bons/bons-ci/pkg/policy/opa/internal/eval"
	"github.com/bons/bons-ci/pkg/policy/opa/internal/events"
	polOtel "github.com/bons/bons-ci/pkg/policy/opa/internal/otel"
	"github.com/bons/bons-ci/pkg/policy/opa/transform"
	dagxform "github.com/bons/bons-ci/pkg/policy/opa/transform/dag"
	matrixxform "github.com/bons/bons-ci/pkg/policy/opa/transform/matrix"
	sourcexform "github.com/bons/bons-ci/pkg/policy/opa/transform/source"
)

// ─── Config ───────────────────────────────────────────────────────────────────

// Config controls the Policy runtime.
type Config struct {
	// PolicyDir loads all *.rego files from this directory recursively.
	// Mutually exclusive with InlineModules; both may be combined via
	// the ComposedSource (InlineModules are merged after PolicyDir, so
	// they can override specific rules for testing).
	PolicyDir string

	// InlineModules maps logical filename → Rego source. Useful for
	// embedding policies in the binary or injecting overrides in tests.
	InlineModules map[string]string

	// WatchInterval enables hot-reload when > 0. Call Watch in a goroutine.
	WatchInterval time.Duration

	// OnBusError is called when an event subscriber returns an error.
	// Defaults to silent drop.
	OnBusError func(err error, event events.RawEvent)

	// ExtraTransforms registers additional Transformers before startup.
	// Key format: transform.Key{Kind: "source", Action: "CONVERT"}.
	ExtraTransforms map[transform.Key][]transform.Transformer

	// CompilerOpts are forwarded to eval.NewCompiler.
	CompilerOpts []eval.CompilerOption
}

// ─── Result types ─────────────────────────────────────────────────────────────

// SourceResult is returned by EvaluateSource.
type SourceResult struct {
	// Action is the final policy decision.
	Action engine.Action
	// Mutated is true when the identifier or attrs were changed by a transformer.
	Mutated bool
	// NewIdentifier is the rewritten identifier when Mutated is true.
	NewIdentifier string
	// NewAttrs contains any attribute updates applied by the transformer.
	NewAttrs map[string]string
	// Messages contains explanatory strings from the policy rules.
	Messages []string
	// Denied is true when Action == ActionDeny.
	Denied bool
}

// DAGResult is returned by EvaluateDAG.
type DAGResult struct {
	Expanded bool
	Nodes    []dagxform.ExpandedNode
	Messages []string
}

// MatrixResult is returned by EvaluateMatrix.
type MatrixResult struct {
	Configs     []matrixxform.BuildConfig
	MaxParallel int
	FailFast    bool
	Messages    []string
}

// ─── Policy ───────────────────────────────────────────────────────────────────

// Policy is the root object. Safe for concurrent use.
// Construct with New; call Watch in a goroutine if hot-reload is desired.
type Policy struct {
	eng    *engine.Engine
	bus    *events.Bus
	loader *bundle.Loader
	reg    *transform.Registry
	cfg    Config
}

// New constructs a Policy from Config.
// The context is used only for the initial bundle build. Returns an error if
// the initial compilation fails (so callers can fail-fast at startup).
func New(ctx context.Context, cfg Config) (*Policy, error) {
	if cfg.PolicyDir == "" && len(cfg.InlineModules) == 0 {
		return nil, fmt.Errorf("opapolicy: Config must set PolicyDir or InlineModules")
	}

	// ── Bundle source ─────────────────────────────────────────────────────────
	src, err := buildSource(cfg)
	if err != nil {
		return nil, fmt.Errorf("opapolicy: build source: %w", err)
	}

	loader, err := bundle.NewLoader(src, cfg.CompilerOpts...)
	if err != nil {
		return nil, fmt.Errorf("opapolicy: new loader: %w", err)
	}
	if err := loader.Build(ctx); err != nil {
		return nil, fmt.Errorf("opapolicy: initial compile: %w", err)
	}

	// ── Evaluator ─────────────────────────────────────────────────────────────
	ev, err := eval.NewEvaluator(loader.Compiler())
	if err != nil {
		return nil, fmt.Errorf("opapolicy: new evaluator: %w", err)
	}

	// ── Event bus ─────────────────────────────────────────────────────────────
	onErr := cfg.OnBusError
	if onErr == nil {
		onErr = func(error, events.RawEvent) {}
	}
	bus, err := events.NewBus(onErr)
	if err != nil {
		return nil, fmt.Errorf("opapolicy: new bus: %w", err)
	}

	// ── Transform registry ────────────────────────────────────────────────────
	reg, err := transform.NewRegistry()
	if err != nil {
		return nil, fmt.Errorf("opapolicy: new registry: %w", err)
	}
	sourcexform.RegisterAll(reg)
	dagxform.RegisterAll(reg)
	matrixxform.RegisterAll(reg)

	for key, ts := range cfg.ExtraTransforms {
		reg.Register(key, ts...)
	}

	// ── Engine ────────────────────────────────────────────────────────────────
	eng, err := engine.NewEngine(ev, bus, reg)
	if err != nil {
		return nil, fmt.Errorf("opapolicy: new engine: %w", err)
	}

	return &Policy{eng: eng, bus: bus, loader: loader, reg: reg, cfg: cfg}, nil
}

// Watch starts hot-reload polling on cfg.WatchInterval.
// It blocks until ctx is cancelled. Call in a goroutine.
// Non-fatal reload errors keep the previous compiler active.
func (p *Policy) Watch(ctx context.Context) error {
	if p.cfg.WatchInterval <= 0 {
		<-ctx.Done()
		return ctx.Err()
	}
	return p.loader.Watch(ctx, p.cfg.WatchInterval)
}

// Bus returns the event bus for custom subscriptions.
func (p *Policy) Bus() *events.Bus { return p.bus }

// Registry returns the transform registry for adding custom transformers.
func (p *Policy) Registry() *transform.Registry { return p.reg }

// ─── EvaluateSource ───────────────────────────────────────────────────────────

// EvaluateSource evaluates source policy for a single image/URL reference.
// It mutates identifier and attrs in place via the transform pipeline, then
// returns a SourceResult describing what happened.
//
// A DENY decision causes a wrapped ErrSourceDenied; callers should use
// errors.Is(err, opapolicy.ErrSourceDenied).
func (p *Policy) EvaluateSource(
	ctx context.Context,
	identifier string,
	attrs map[string]string,
) (SourceResult, error) {
	ctx, end := polOtel.StartSpan(ctx, polOtel.Tracer("policy"),
		"evaluate_source",
		polOtel.AttrIdentifier.String(identifier),
	)
	var retErr error
	defer end(&retErr)

	if attrs == nil {
		attrs = make(map[string]string)
	}

	// The mutable fields that transforms write to.
	mutableID := identifier
	mutableAttrs := cloneAttrs(attrs)

	inp := sourcexform.NewOpInput(identifier, attrs, &mutableID, &mutableAttrs)

	dec, err := p.eng.Process(ctx, engine.PolicyRequest{
		Kind:     "source",
		Input:    inp,
		OPAInput: sourcexform.Input{Identifier: identifier, Attrs: attrs},
	})

	result := SourceResult{
		Action:        dec.Action,
		Mutated:       dec.Mutated,
		NewIdentifier: mutableID,
		NewAttrs:      mutableAttrs,
		Messages:      dec.Messages,
		Denied:        dec.Action == engine.ActionDeny,
	}

	if err != nil {
		retErr = err
		return result, err
	}
	return result, nil
}

// ErrSourceDenied is the sentinel wrapped in errors returned by EvaluateSource
// when a DENY transform fires. Use errors.Is to check.
var ErrSourceDenied = sourcexform.ErrSourceDenied

// ─── EvaluateDAG ─────────────────────────────────────────────────────────────

// EvaluateDAG evaluates DAG expansion policy for a single build graph operation.
// Returns a DAGResult; when Expanded is true, Nodes contains the generated ops
// that the caller must integrate into the build graph.
func (p *Policy) EvaluateDAG(ctx context.Context, input dagxform.DAGInput) (DAGResult, error) {
	ctx, end := polOtel.StartSpan(ctx, polOtel.Tracer("policy"),
		"evaluate_dag",
		polOtel.AttrOpID.String(safeOpID(input.Op)),
		polOtel.AttrOpType.String(safeOpType(input.Op)),
	)
	var retErr error
	defer end(&retErr)

	dec, err := p.eng.Process(ctx, engine.PolicyRequest{
		Kind:  "dag",
		Input: input,
	})
	if err != nil {
		retErr = err
		return DAGResult{Messages: dec.Messages}, err
	}

	var nodes []dagxform.ExpandedNode
	if raw, ok := dec.Updates[dagxform.ExpansionKey]; ok {
		nodes, _ = raw.([]dagxform.ExpandedNode)
	}

	return DAGResult{
		Expanded: dec.Mutated,
		Nodes:    nodes,
		Messages: dec.Messages,
	}, nil
}

// ─── EvaluateMatrix ──────────────────────────────────────────────────────────

// EvaluateMatrix evaluates matrix expansion policy for a build strategy.
// Returns a MatrixResult with all computed BuildConfigs.
// A DENY result (e.g. all combinations excluded) is returned as an error.
func (p *Policy) EvaluateMatrix(ctx context.Context, input matrixxform.MatrixInput) (MatrixResult, error) {
	ctx, end := polOtel.StartSpan(ctx, polOtel.Tracer("policy"),
		"evaluate_matrix",
	)
	var retErr error
	defer end(&retErr)

	dec, err := p.eng.Process(ctx, engine.PolicyRequest{
		Kind:  "matrix",
		Input: input,
	})
	if err != nil {
		retErr = err
		return MatrixResult{Messages: dec.Messages}, err
	}

	if dec.Action == engine.ActionDeny {
		retErr = fmt.Errorf("matrix expansion denied: %v", dec.Messages)
		return MatrixResult{Messages: dec.Messages}, retErr
	}

	var exp matrixxform.Expansion
	if raw, ok := dec.Updates[matrixxform.ExpansionKey]; ok {
		exp, _ = raw.(matrixxform.Expansion)
	}

	return MatrixResult{
		Configs:     exp.Configs,
		MaxParallel: exp.MaxParallel,
		FailFast:    exp.FailFast,
		Messages:    dec.Messages,
	}, nil
}

// ─── Event subscription helpers ───────────────────────────────────────────────

// OnSourceDenied registers fn to be called when any source is denied.
func (p *Policy) OnSourceDenied(fn func(ctx context.Context, payload engine.ActionEventPayload) error) *events.Subscription {
	return events.On[engine.ActionEventPayload](p.bus, engine.KindSourceDenied, fn)
}

// OnSourceConverted registers fn to be called when any source is redirected.
func (p *Policy) OnSourceConverted(fn func(ctx context.Context, payload engine.ActionEventPayload) error) *events.Subscription {
	return events.On[engine.ActionEventPayload](p.bus, engine.KindSourceConverted, fn)
}

// OnSourceAllowed registers fn to be called when a source passes through unchanged.
func (p *Policy) OnSourceAllowed(fn func(ctx context.Context, payload engine.ActionEventPayload) error) *events.Subscription {
	return events.On[engine.ActionEventPayload](p.bus, engine.KindSourceAllowed, fn)
}

// OnDAGExpanded registers fn to be called when a DAG op is expanded.
func (p *Policy) OnDAGExpanded(fn func(ctx context.Context, payload engine.ActionEventPayload) error) *events.Subscription {
	return events.On[engine.ActionEventPayload](p.bus, engine.KindDAGExpanded, fn)
}

// OnMatrixExpanded registers fn to be called when a matrix strategy is expanded.
func (p *Policy) OnMatrixExpanded(fn func(ctx context.Context, payload engine.ActionEventPayload) error) *events.Subscription {
	return events.On[engine.ActionEventPayload](p.bus, engine.KindMatrixExpanded, fn)
}

// OnPolicyEvaluated registers fn to be called on every policy evaluation.
func (p *Policy) OnPolicyEvaluated(fn func(ctx context.Context, payload engine.PolicyEvaluatedPayload) error) *events.Subscription {
	return events.On[engine.PolicyEvaluatedPayload](p.bus, engine.KindPolicyEvaluated, fn)
}

// ─── internal helpers ─────────────────────────────────────────────────────────

func buildSource(cfg Config) (bundle.Source, error) {
	var sources []bundle.Source
	if cfg.PolicyDir != "" {
		sources = append(sources, &bundle.DirSource{Root: cfg.PolicyDir})
	}
	if len(cfg.InlineModules) > 0 {
		sources = append(sources, &bundle.StaticSource{Modules: cfg.InlineModules})
	}
	switch len(sources) {
	case 0:
		return nil, errors.New("no source configured")
	case 1:
		return sources[0], nil
	default:
		return bundle.ComposedSource(sources), nil
	}
}

func cloneAttrs(a map[string]string) map[string]string {
	out := make(map[string]string, len(a))
	for k, v := range a {
		out[k] = v
	}
	return out
}

func safeOpID(op *dagxform.OpDescriptor) string {
	if op == nil {
		return ""
	}
	return op.ID
}

func safeOpType(op *dagxform.OpDescriptor) string {
	if op == nil {
		return ""
	}
	return op.Type
}
