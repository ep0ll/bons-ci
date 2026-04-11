// Package source implements source-policy transformers that apply OPA decisions
// to live *pb.SourceOp values. This is a domain package — it knows about
// buildkit protobuf types. It must NOT import the eval or events packages.
package source

import (
	"context"
	"fmt"
	"regexp"
	"strings"

	"go.opentelemetry.io/otel/attribute"
	"go.opentelemetry.io/otel/trace"

	polOtel "github.com/bons/bons-ci/pkg/policy/opa/internal/otel"
	"github.com/bons/bons-ci/pkg/policy/opa/transform"
)

// ─── Sentinel errors ──────────────────────────────────────────────────────────

// ErrSourceDenied is returned by DenyTransformer. Callers can use errors.Is.
var ErrSourceDenied = fmt.Errorf("source denied by policy")

// ─── Input shape ─────────────────────────────────────────────────────────────

// Input is the JSON-serialisable representation of a source operation sent
// to OPA. Build from a *pb.SourceOp; the *pb.SourceOp itself is threaded
// through as the opaque input so transforms can mutate it in place.
type Input struct {
	Identifier string            `json:"identifier"`
	Attrs      map[string]string `json:"attrs,omitempty"`
}

// OpInput wraps the live *pb.SourceOp for transform-side mutations.
// OPA sees Input; transforms receive OpInput.
type OpInput struct {
	// Identifier and Attrs mirror Input for transforms that don't have pb.
	Identifier string
	Attrs      map[string]string
	// MutableIdentifier and MutableAttrs are the fields transforms write to.
	// Separated so transforms can be tested without a real pb.SourceOp.
	MutableIdentifier *string
	MutableAttrs      *map[string]string
}

// NewOpInput creates an OpInput backed by live fields.
func NewOpInput(identifier string, attrs map[string]string,
	mutableID *string, mutableAttrs *map[string]string) OpInput {
	return OpInput{
		Identifier:        identifier,
		Attrs:             attrs,
		MutableIdentifier: mutableID,
		MutableAttrs:      mutableAttrs,
	}
}

// ─── MutateOpTransformer ──────────────────────────────────────────────────────

// MutateOpTransformer applies the OPA decision's "updates.identifier" and
// "updates.attrs" fields to the live op. It only fires on action=CONVERT.
type MutateOpTransformer struct {
	tracer trace.Tracer
}

// NewMutateOpTransformer creates a MutateOpTransformer.
func NewMutateOpTransformer() *MutateOpTransformer {
	return &MutateOpTransformer{tracer: polOtel.Tracer("source.mutate_op")}
}

func (t *MutateOpTransformer) Name() string { return "source.mutate_op" }

func (t *MutateOpTransformer) Apply(ctx context.Context, input any, dec transform.Decision) (transform.Decision, error) {
	if dec.Action != "CONVERT" || len(dec.Updates) == 0 {
		return dec, nil
	}

	op, ok := input.(OpInput)
	if !ok {
		return dec, fmt.Errorf("source.mutate_op: expected OpInput, got %T", input)
	}

	ctx, span := t.tracer.Start(ctx, polOtel.Namespace+".source.mutate_op",
		trace.WithAttributes(
			attribute.String("identifier", op.Identifier),
		),
	)
	defer span.End()

	// Apply identifier update.
	if newID, _ := dec.Updates["identifier"].(string); newID != "" && newID != op.Identifier {
		polOtel.AddEvent(ctx, "source.identifier_updated",
			polOtel.AttrIdentifier.String(newID),
		)
		*op.MutableIdentifier = newID
		dec.Mutated = true
	}

	// Apply attribute updates.
	if rawAttrs, ok := dec.Updates["attrs"].(map[string]any); ok && len(rawAttrs) > 0 {
		if *op.MutableAttrs == nil {
			m := make(map[string]string, len(rawAttrs))
			*op.MutableAttrs = m
		}
		for k, v := range rawAttrs {
			s, ok := v.(string)
			if !ok {
				return dec, fmt.Errorf("source.mutate_op: attrs[%q] must be string, got %T", k, v)
			}
			if (*op.MutableAttrs)[k] != s {
				(*op.MutableAttrs)[k] = s
				dec.Mutated = true
			}
		}
	}

	return dec, nil
}

// ─── RegexRewriteTransformer ──────────────────────────────────────────────────

// RegexRewriteTransformer applies regex capture-group substitution when the
// OPA decision includes "updates.pattern" and "updates.replacement".
// The pattern is compiled once and cached per Apply call (the pattern comes from
// OPA and may vary — caching per-transformer is left to callers if needed).
type RegexRewriteTransformer struct {
	tracer trace.Tracer
}

// NewRegexRewriteTransformer creates a RegexRewriteTransformer.
func NewRegexRewriteTransformer() *RegexRewriteTransformer {
	return &RegexRewriteTransformer{tracer: polOtel.Tracer("source.regex_rewrite")}
}

func (t *RegexRewriteTransformer) Name() string { return "source.regex_rewrite" }

func (t *RegexRewriteTransformer) Apply(ctx context.Context, input any, dec transform.Decision) (transform.Decision, error) {
	if dec.Action != "CONVERT" {
		return dec, nil
	}

	pattern, _ := dec.Updates["pattern"].(string)
	replacement, _ := dec.Updates["replacement"].(string)
	if pattern == "" {
		return dec, nil
	}

	op, ok := input.(OpInput)
	if !ok {
		return dec, fmt.Errorf("source.regex_rewrite: expected OpInput, got %T", input)
	}

	ctx, span := t.tracer.Start(ctx, polOtel.Namespace+".source.regex_rewrite",
		trace.WithAttributes(attribute.String("pattern", pattern)),
	)
	defer span.End()

	re, err := regexp.Compile(pattern)
	if err != nil {
		return dec, fmt.Errorf("source.regex_rewrite: invalid pattern %q: %w", pattern, err)
	}

	newID := re.ReplaceAllString(op.Identifier, replacement)
	if newID != op.Identifier {
		polOtel.AddEvent(ctx, "source.regex_rewritten",
			polOtel.AttrIdentifier.String(newID),
		)
		*op.MutableIdentifier = newID
		dec.Mutated = true
	}
	return dec, nil
}

// ─── WildcardRewriteTransformer ───────────────────────────────────────────────

// WildcardRewriteTransformer handles shell-glob wildcard rewrites.
// The OPA decision must include "updates.glob_pattern" and "updates.glob_replacement".
// Supports single-level (*) and multi-level (**) wildcards.
// Capture groups are referenced as ${1}, ${2}, … in the replacement template.
type WildcardRewriteTransformer struct {
	tracer trace.Tracer
}

// NewWildcardRewriteTransformer creates a WildcardRewriteTransformer.
func NewWildcardRewriteTransformer() *WildcardRewriteTransformer {
	return &WildcardRewriteTransformer{tracer: polOtel.Tracer("source.wildcard_rewrite")}
}

func (t *WildcardRewriteTransformer) Name() string { return "source.wildcard_rewrite" }

func (t *WildcardRewriteTransformer) Apply(ctx context.Context, input any, dec transform.Decision) (transform.Decision, error) {
	if dec.Action != "CONVERT" {
		return dec, nil
	}

	glob, _ := dec.Updates["glob_pattern"].(string)
	tmpl, _ := dec.Updates["glob_replacement"].(string)
	if glob == "" {
		return dec, nil
	}

	op, ok := input.(OpInput)
	if !ok {
		return dec, fmt.Errorf("source.wildcard_rewrite: expected OpInput, got %T", input)
	}

	ctx, span := t.tracer.Start(ctx, polOtel.Namespace+".source.wildcard_rewrite",
		trace.WithAttributes(attribute.String("glob", glob)),
	)
	defer span.End()

	newID, matched, err := applyGlob(op.Identifier, glob, tmpl)
	if err != nil {
		return dec, fmt.Errorf("source.wildcard_rewrite: %w", err)
	}
	if matched && newID != op.Identifier {
		polOtel.AddEvent(ctx, "source.wildcard_rewritten",
			polOtel.AttrIdentifier.String(newID),
		)
		*op.MutableIdentifier = newID
		dec.Mutated = true
	}
	return dec, nil
}

// ─── DenyTransformer ──────────────────────────────────────────────────────────

// DenyTransformer returns ErrSourceDenied (wrapping Messages) for action=DENY.
// Register this last in the chain so other transforms can still observe the denial.
type DenyTransformer struct{}

func NewDenyTransformer() *DenyTransformer { return &DenyTransformer{} }
func (DenyTransformer) Name() string       { return "source.deny" }

func (DenyTransformer) Apply(_ context.Context, _ any, dec transform.Decision) (transform.Decision, error) {
	if dec.Action != "DENY" {
		return dec, nil
	}
	msg := strings.Join(dec.Messages, "; ")
	if msg == "" {
		msg = "source denied by policy"
	}
	return dec, fmt.Errorf("%w: %s", ErrSourceDenied, msg)
}

// ─── Registration helper ──────────────────────────────────────────────────────

// RegisterAll registers the standard source transforms into reg.
// Call this once during application bootstrap.
//
// Transform order for action=CONVERT:
//  1. MutateOpTransformer  — apply exact identifier / attr updates
//  2. RegexRewriteTransformer — apply regex capture-group rewrite
//  3. WildcardRewriteTransformer — apply glob wildcard rewrite
//
// Transform order for action=DENY:
//  1. DenyTransformer — return ErrSourceDenied
func RegisterAll(reg *transform.Registry) {
	reg.Register(transform.Key{Kind: "source", Action: "CONVERT"},
		NewMutateOpTransformer(),
		NewRegexRewriteTransformer(),
		NewWildcardRewriteTransformer(),
	)
	reg.Register(transform.Key{Kind: "source", Action: "DENY"},
		NewDenyTransformer(),
	)
}

// ─── Pure glob implementation ─────────────────────────────────────────────────

// applyGlob converts a shell-glob pattern to a regex, matches subject,
// and interpolates ${N} references in tmpl. Returns (result, matched, error).
func applyGlob(subject, glob, tmpl string) (string, bool, error) {
	reStr, err := globToRegex(glob)
	if err != nil {
		return subject, false, fmt.Errorf("glob_to_regex(%q): %w", glob, err)
	}

	re, err := regexp.Compile("^" + reStr + "$")
	if err != nil {
		return subject, false, fmt.Errorf("compile glob regex: %w", err)
	}

	m := re.FindStringSubmatch(subject)
	if m == nil {
		return subject, false, nil
	}

	result := tmpl
	for i, cap := range m[1:] {
		result = strings.ReplaceAll(result, fmt.Sprintf("${%d}", i+1), cap)
	}
	return result, true, nil
}

// globToRegex converts a glob pattern into a regex string.
// * matches any sequence that doesn't contain "/".
// ** matches any sequence including "/".
func globToRegex(glob string) (string, error) {
	var sb strings.Builder
	i := 0
	for i < len(glob) {
		switch glob[i] {
		case '*':
			if i+1 < len(glob) && glob[i+1] == '*' {
				sb.WriteString("(.+)")
				i += 2
			} else {
				sb.WriteString("([^/]+)")
				i++
			}
		case '?':
			sb.WriteString("([^/])")
			i++
		default:
			sb.WriteString(regexp.QuoteMeta(string(glob[i])))
			i++
		}
	}
	return sb.String(), nil
}
