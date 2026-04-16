// Package policy implements SLSA compliance checking against provenance predicates.
package policy

import (
	"errors"
	"fmt"
	"strings"
	"time"

	"github.com/bons/bons-ci/pkg/slsa/provenance"
	"github.com/bons/bons-ci/pkg/slsa/types"
)

// ─────────────────────────────────────────────────────────────────────────────
// Violation
// ─────────────────────────────────────────────────────────────────────────────

// Violation is a single SLSA compliance failure.
type Violation struct {
	Requirement string
	Detail      string
}

// Error implements the error interface.
func (v *Violation) Error() string {
	if v.Detail == "" {
		return "slsa: " + v.Requirement
	}
	return "slsa: " + v.Requirement + ": " + v.Detail
}

// Violations is a slice of Violation that also satisfies error.
type Violations []Violation

// Error returns a combined human-readable description.
func (vs Violations) Error() string {
	if len(vs) == 0 {
		return ""
	}
	if len(vs) == 1 {
		return vs[0].Error()
	}
	parts := make([]string, len(vs))
	for i, v := range vs {
		if v.Detail != "" {
			parts[i] = "  - " + v.Requirement + ": " + v.Detail
		} else {
			parts[i] = "  - " + v.Requirement
		}
	}
	return "slsa compliance failures:\n" + strings.Join(parts, "\n")
}

// ─────────────────────────────────────────────────────────────────────────────
// Policy
// ─────────────────────────────────────────────────────────────────────────────

// Policy specifies the minimum SLSA level and optional additional constraints.
type Policy struct {
	// MinLevel is the minimum required SLSA level.
	MinLevel types.Level
	// RequiredBuilderIDs is an allow-list of builder URIs. Empty means any builder.
	RequiredBuilderIDs []string
	// MaxAge limits how old the provenance StartedOn timestamp may be.
	MaxAge time.Duration
	// RequireReproducible mandates the reproducible flag.
	RequireReproducible bool
}

// ─── EvaluateV1 ───────────────────────────────────────────────────────────────

// EvaluateV1 checks whether the SLSA v1 predicate satisfies the policy.
// Returns nil when compliant; otherwise returns a Violations value.
func (p *Policy) EvaluateV1(pred *provenance.PredicateV1) error {
	if pred == nil {
		return &Violation{Requirement: "non-nil-predicate", Detail: "predicate must not be nil"}
	}

	var vs Violations
	reqs := types.RequirementsFor(p.MinLevel)

	// Build type must always be set.
	if pred.BuildDefinition.BuildType == "" {
		vs = append(vs, Violation{Requirement: "build-type", Detail: "buildDefinition.buildType must not be empty"})
	}

	// Builder ID (L2+).
	if reqs.NonFalsifiable || p.MinLevel >= types.Level2 {
		if pred.RunDetails.Builder.ID == "" {
			vs = append(vs, Violation{Requirement: "builder-id", Detail: "builder ID is required for SLSA L2+"})
		}
	}

	// Builder allow-list.
	if len(p.RequiredBuilderIDs) > 0 {
		found := false
		for _, allowed := range p.RequiredBuilderIDs {
			if pred.RunDetails.Builder.ID == allowed {
				found = true
				break
			}
		}
		if !found {
			vs = append(vs, Violation{
				Requirement: "builder-allowlist",
				Detail:      fmt.Sprintf("builder %q is not in the required builders list", pred.RunDetails.Builder.ID),
			})
		}
	}

	meta := pred.RunDetails.Metadata
	if meta == nil {
		meta = &provenance.MetadataV1{}
	}

	// Invocation ID (L2+).
	if p.MinLevel >= types.Level2 && meta.InvocationID == "" {
		vs = append(vs, Violation{Requirement: "invocation-id", Detail: "invocation ID is required for SLSA L2+"})
	}

	// Resolved dependencies (L3+).
	if p.MinLevel >= types.Level3 && len(pred.BuildDefinition.ResolvedDependencies) == 0 {
		vs = append(vs, Violation{
			Requirement: "resolved-dependencies",
			Detail:      "at least one resolved dependency is required for SLSA L3+",
		})
	}

	// Hermetic (L4).
	if reqs.Hermetic && !meta.Hermetic {
		vs = append(vs, Violation{Requirement: "hermetic", Detail: "build must be hermetic for SLSA L4"})
	}

	// Dependencies complete (L4).
	if reqs.DependenciesComplete && !meta.Completeness.Materials {
		vs = append(vs, Violation{Requirement: "dependencies-complete", Detail: "all materials must be resolved"})
	}

	// Reproducible.
	if (reqs.Reproducible || p.RequireReproducible) && !meta.Reproducible {
		vs = append(vs, Violation{Requirement: "reproducible", Detail: "build must be marked as reproducible"})
	}

	// Max age.
	if p.MaxAge > 0 && meta.StartedOn != nil {
		if time.Since(*meta.StartedOn) > p.MaxAge {
			vs = append(vs, Violation{Requirement: "max-age", Detail: "provenance exceeds the configured max age"})
		}
	}

	if len(vs) > 0 {
		return vs
	}
	return nil
}

// ─── EvaluateV02 ──────────────────────────────────────────────────────────────

// EvaluateV02 checks whether the SLSA v0.2 predicate satisfies the policy.
func (p *Policy) EvaluateV02(pred *provenance.PredicateV02) error {
	if pred == nil {
		return &Violation{Requirement: "non-nil-predicate", Detail: "predicate must not be nil"}
	}

	var vs Violations
	reqs := types.RequirementsFor(p.MinLevel)

	if pred.BuildType == "" {
		vs = append(vs, Violation{Requirement: "build-type", Detail: "buildType must not be empty"})
	}

	if reqs.NonFalsifiable || p.MinLevel >= types.Level2 {
		if pred.Builder.ID == "" {
			vs = append(vs, Violation{Requirement: "builder-id", Detail: "builder ID is required for SLSA L2+"})
		}
	}

	if len(p.RequiredBuilderIDs) > 0 {
		found := false
		for _, allowed := range p.RequiredBuilderIDs {
			if pred.Builder.ID == allowed {
				found = true
				break
			}
		}
		if !found {
			vs = append(vs, Violation{
				Requirement: "builder-allowlist",
				Detail:      fmt.Sprintf("builder %q is not in the required builders list", pred.Builder.ID),
			})
		}
	}

	if pred.Metadata != nil {
		if reqs.Hermetic && !pred.Metadata.Hermetic {
			vs = append(vs, Violation{Requirement: "hermetic", Detail: "build must be hermetic"})
		}
		if reqs.DependenciesComplete && !pred.Metadata.Completeness.Materials {
			vs = append(vs, Violation{Requirement: "dependencies-complete"})
		}
		if p.MinLevel >= types.Level2 && pred.Metadata.BuildInvocationID == "" {
			vs = append(vs, Violation{Requirement: "invocation-id", Detail: "invocation ID required for L2+"})
		}
		if p.MaxAge > 0 && pred.Metadata.BuildStartedOn != nil {
			if time.Since(*pred.Metadata.BuildStartedOn) > p.MaxAge {
				vs = append(vs, Violation{Requirement: "max-age"})
			}
		}
	}

	if len(vs) > 0 {
		return vs
	}
	return nil
}

// ─── Preset policies ──────────────────────────────────────────────────────────

func L1() *Policy { return &Policy{MinLevel: types.Level1} }
func L2() *Policy { return &Policy{MinLevel: types.Level2} }
func L3() *Policy { return &Policy{MinLevel: types.Level3} }
func L4() *Policy { return &Policy{MinLevel: types.Level4, RequireReproducible: true} }

// ─────────────────────────────────────────────────────────────────────────────
// ComplianceReport / CheckLevel
// ─────────────────────────────────────────────────────────────────────────────

// ComplianceReport summarises the highest SLSA level achieved.
type ComplianceReport struct {
	Level      types.Level
	Violations Violations
	Passed     bool
}

// CheckLevel determines the highest SLSA level (up to maxLevel) that pred satisfies.
func CheckLevel(pred *provenance.PredicateV1, maxLevel types.Level) (*ComplianceReport, error) {
	if pred == nil {
		return nil, errors.New("policy: predicate must not be nil")
	}
	if maxLevel < types.LevelNone || maxLevel > types.Level4 {
		return nil, fmt.Errorf("policy: invalid maxLevel %d", maxLevel)
	}

	report := &ComplianceReport{}
	for l := types.Level1; l <= maxLevel; l++ {
		pol := &Policy{MinLevel: l}
		if l == types.Level4 {
			pol.RequireReproducible = true
		}
		if err := pol.EvaluateV1(pred); err != nil {
			vs, ok := err.(Violations)
			if !ok {
				vs = Violations{{Requirement: "unknown", Detail: err.Error()}}
			}
			report.Violations = vs
			break
		}
		report.Level = l
	}
	report.Passed = len(report.Violations) == 0
	return report, nil
}
