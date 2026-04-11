// Package matrix implements matrix-build expansion transforms.
// When OPA returns action=EXPAND for kind="matrix", the transformer interprets
// the decision (or defers to the pure Expand() function) to produce a
// slice of BuildConfig values — one per axis combination.
//
// The Expand() function is intentionally pure (no context, no OPA dependency)
// so it can be unit-tested and called independently from any layer.
package matrix

import (
	"context"
	"fmt"
	"sort"
	"strings"

	"go.opentelemetry.io/otel/attribute"
	"go.opentelemetry.io/otel/trace"

	polOtel "github.com/bons/bons-ci/pkg/policy/opa/internal/otel"
	"github.com/bons/bons-ci/pkg/policy/opa/transform"
)

// ─── Input / Output shapes ────────────────────────────────────────────────────

// Strategy mirrors a CI-style matrix strategy.
type Strategy struct {
	// Matrix maps axis name → ordered list of values.
	// e.g. {"os": ["linux","windows"], "arch": ["amd64","arm64"]}
	Matrix map[string][]string `json:"matrix"`
	// Include adds extra key-value pairs to specific matching combinations,
	// or introduces entirely new combinations if no existing combo is a subset.
	Include []map[string]string `json:"include,omitempty"`
	// Exclude removes combinations where all key-value pairs match.
	Exclude []map[string]string `json:"exclude,omitempty"`
	// MaxParallel caps concurrent jobs (advisory; engine does not enforce).
	MaxParallel int `json:"max_parallel,omitempty"`
	// FailFast stops the matrix on first failure (advisory).
	FailFast bool `json:"fail_fast,omitempty"`
}

// MatrixInput is the typed OPA input for kind="matrix".
type MatrixInput struct {
	Strategy Strategy          `json:"strategy"`
	BaseOp   map[string]any    `json:"base_op,omitempty"`
	Labels   map[string]string `json:"labels,omitempty"`
}

// BuildConfig is a single concrete build configuration.
type BuildConfig struct {
	// ID is a deterministic slug for this combination.
	ID string `json:"id"`
	// Vars holds the axis values.
	Vars map[string]string `json:"vars"`
	// Extra holds fields added by Include entries that extend existing combos.
	Extra map[string]string `json:"extra,omitempty"`
}

// Expansion is the full result of a matrix expansion.
type Expansion struct {
	Configs     []BuildConfig `json:"configs"`
	MaxParallel int           `json:"max_parallel,omitempty"`
	FailFast    bool          `json:"fail_fast,omitempty"`
}

// ExpansionKey is used to store the Expansion in Decision.Updates.
const ExpansionKey = "matrix_expansion"

// ─── Pure expansion logic ─────────────────────────────────────────────────────

// Expand computes the full set of BuildConfigs from a Strategy.
// It is a pure function: no side effects, no context, safe to call concurrently.
//
// Algorithm:
//  1. Compute the cartesian product of all axes in deterministic order.
//  2. Remove combinations that match any Exclude entry (subset match).
//  3. For each remaining combination, merge any Include entries that are subsets.
//  4. Include entries not subsumed by any combo are added as standalone configs.
func Expand(s Strategy) (Expansion, error) {
	if len(s.Matrix) == 0 {
		return Expansion{MaxParallel: s.MaxParallel, FailFast: s.FailFast}, nil
	}

	// Step 1: sorted axes for determinism.
	axes := sortedKeys(s.Matrix)
	product := cartesian(axes, s.Matrix)

	// Step 2: apply exclusions.
	filtered := make([]map[string]string, 0, len(product))
	for _, combo := range product {
		if !isExcluded(combo, s.Exclude) {
			filtered = append(filtered, combo)
		}
	}

	// Step 3: build configs with merged includes.
	configs := make([]BuildConfig, 0, len(filtered)+len(s.Include))
	for _, combo := range filtered {
		extra := mergeIncludes(combo, s.Include)
		configs = append(configs, BuildConfig{
			ID:    comboID(axes, combo),
			Vars:  combo,
			Extra: extra,
		})
	}

	// Step 4: standalone includes (no existing combo is a subset).
	for _, inc := range s.Include {
		if !isSubsumedByAnyCombo(inc, filtered) {
			// Sort keys for deterministic ID.
			incAxes := sortedStrKeys(inc)
			configs = append(configs, BuildConfig{
				ID:   includeID(incAxes, inc),
				Vars: inc,
			})
		}
	}

	return Expansion{
		Configs:     configs,
		MaxParallel: s.MaxParallel,
		FailFast:    s.FailFast,
	}, nil
}

// ─── ExpanderTransformer ──────────────────────────────────────────────────────

// ExpanderTransformer converts OPA EXPAND decisions into a typed Expansion.
// If OPA pre-computed the expansion (Expansions non-empty), those are parsed.
// Otherwise, the transformer calls Expand() from the input strategy.
type ExpanderTransformer struct {
	tracer trace.Tracer
}

// NewExpanderTransformer creates an ExpanderTransformer.
func NewExpanderTransformer() *ExpanderTransformer {
	return &ExpanderTransformer{tracer: polOtel.Tracer("matrix.expander")}
}

func (t *ExpanderTransformer) Name() string { return "matrix.expander" }

func (t *ExpanderTransformer) Apply(ctx context.Context, input any, dec transform.Decision) (transform.Decision, error) {
	if dec.Action != "EXPAND" {
		return dec, nil
	}

	ctx, span := t.tracer.Start(ctx, polOtel.Namespace+".matrix.expand")
	defer span.End()

	var exp Expansion

	if len(dec.Expansions) > 0 {
		// OPA pre-computed the expansion.
		configs, err := parseOPAExpansions(dec.Expansions)
		if err != nil {
			polOtel.RecordError(ctx, err)
			return dec, fmt.Errorf("matrix.expander: parse OPA expansions: %w", err)
		}
		exp.Configs = configs

		// Carry forward MaxParallel / FailFast from Updates if OPA set them.
		if v, ok := dec.Updates["max_parallel"].(float64); ok {
			exp.MaxParallel = int(v)
		}
		if v, ok := dec.Updates["fail_fast"].(bool); ok {
			exp.FailFast = v
		}
	} else {
		// Compute expansion from input strategy (Go-side computation).
		mi, ok := input.(MatrixInput)
		if !ok {
			return dec, fmt.Errorf("matrix.expander: expected MatrixInput, got %T", input)
		}
		var err error
		exp, err = Expand(mi.Strategy)
		if err != nil {
			polOtel.RecordError(ctx, err)
			return dec, fmt.Errorf("matrix.expander: expand: %w", err)
		}
	}

	span.SetAttributes(
		polOtel.AttrMatrixSize.Int(len(exp.Configs)),
	)
	polOtel.AddEvent(ctx, "matrix.expanded",
		attribute.Int("config_count", len(exp.Configs)),
	)

	if dec.Updates == nil {
		dec.Updates = make(map[string]any)
	}
	dec.Updates[ExpansionKey] = exp
	dec.Mutated = len(exp.Configs) > 0
	return dec, nil
}

// ─── Registration helper ──────────────────────────────────────────────────────

// RegisterAll registers matrix transforms into reg.
func RegisterAll(reg *transform.Registry) {
	reg.Register(transform.Key{Kind: "matrix", Action: "EXPAND"},
		NewExpanderTransformer(),
	)
}

// ─── Pure helpers ─────────────────────────────────────────────────────────────

func sortedKeys(m map[string][]string) []string {
	keys := make([]string, 0, len(m))
	for k := range m {
		keys = append(keys, k)
	}
	sort.Strings(keys)
	return keys
}

func sortedStrKeys(m map[string]string) []string {
	keys := make([]string, 0, len(m))
	for k := range m {
		keys = append(keys, k)
	}
	sort.Strings(keys)
	return keys
}

// cartesian computes the cartesian product of the values for each axis.
// Axes are processed in the provided (sorted) order for determinism.
func cartesian(axes []string, matrix map[string][]string) []map[string]string {
	result := []map[string]string{{}}
	for _, axis := range axes {
		vals := matrix[axis]
		var next []map[string]string
		for _, combo := range result {
			for _, val := range vals {
				c := make(map[string]string, len(combo)+1)
				for k, v := range combo {
					c[k] = v
				}
				c[axis] = val
				next = append(next, c)
			}
		}
		result = next
	}
	return result
}

// isExcluded returns true when combo matches any exclusion (all k-v pairs present).
func isExcluded(combo map[string]string, excludes []map[string]string) bool {
	for _, ex := range excludes {
		if subsetMatch(ex, combo) {
			return true
		}
	}
	return false
}

// subsetMatch returns true when every k-v in sub appears in super.
func subsetMatch(sub, super map[string]string) bool {
	for k, v := range sub {
		if super[k] != v {
			return false
		}
	}
	return true
}

// mergeIncludes returns the extra keys from includes that partially match combo.
// Only keys not already in combo are returned.
func mergeIncludes(combo map[string]string, includes []map[string]string) map[string]string {
	extra := make(map[string]string)
	for _, inc := range includes {
		if !subsetMatch(combo, inc) {
			continue
		}
		for k, v := range inc {
			if _, inCombo := combo[k]; !inCombo {
				extra[k] = v
			}
		}
	}
	if len(extra) == 0 {
		return nil
	}
	return extra
}

// isSubsumedByAnyCombo returns true when any combo is a subset of inc.
func isSubsumedByAnyCombo(inc map[string]string, combos []map[string]string) bool {
	for _, c := range combos {
		if subsetMatch(c, inc) {
			return true
		}
	}
	return false
}

// comboID builds a deterministic slug from the combo using sorted axes.
func comboID(axes []string, combo map[string]string) string {
	parts := make([]string, 0, len(axes))
	for _, a := range axes {
		if v, ok := combo[a]; ok {
			parts = append(parts, v)
		}
	}
	return strings.Join(parts, "-")
}

// includeID builds a deterministic slug for a standalone include.
func includeID(axes []string, inc map[string]string) string {
	parts := make([]string, 0, len(axes))
	for _, a := range axes {
		if v, ok := inc[a]; ok {
			parts = append(parts, v)
		}
	}
	return strings.Join(parts, "-")
}

// parseOPAExpansions converts the raw OPA expansions slice into BuildConfigs.
func parseOPAExpansions(raw []map[string]any) ([]BuildConfig, error) {
	configs := make([]BuildConfig, 0, len(raw))
	for i, item := range raw {
		cfg := BuildConfig{}

		if v, ok := item["id"].(string); ok {
			cfg.ID = v
		} else {
			return nil, fmt.Errorf("expansion[%d]: missing 'id'", i)
		}

		if vars, ok := item["vars"].(map[string]any); ok {
			cfg.Vars = make(map[string]string, len(vars))
			for k, v := range vars {
				s, ok := v.(string)
				if !ok {
					return nil, fmt.Errorf("expansion[%d].vars[%q]: must be string, got %T", i, k, v)
				}
				cfg.Vars[k] = s
			}
		}

		configs = append(configs, cfg)
	}
	return configs, nil
}

// ─── internal use only ────────────────────────────────────────────────────────

// sortedStrKeys is only used internally; exposed for tests via package-level
// access through the pure functions.
var _ = sortedStrKeys
