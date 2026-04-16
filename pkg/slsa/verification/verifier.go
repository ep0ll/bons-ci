// Package verification provides the complete attestation verification pipeline:
// DSSE signature → in-toto statement → SLSA policy evaluation.
package verification

import (
	"encoding/json"
	"errors"
	"fmt"

	"github.com/bons/bons-ci/pkg/slsa/attestation"
	"github.com/bons/bons-ci/pkg/slsa/policy"
	"github.com/bons/bons-ci/pkg/slsa/provenance"
	"github.com/bons/bons-ci/pkg/slsa/signing"
	"github.com/bons/bons-ci/pkg/slsa/types"
)

// ─── Result ───────────────────────────────────────────────────────────────────

// Result is the output of a successful verification run.
type Result struct {
	Statement       *attestation.Statement
	ComplianceLevel types.Level
	SignatureIndex  int
	Violations      policy.Violations
	Passed          bool
}

// ─── Verifier ─────────────────────────────────────────────────────────────────

// Verifier orchestrates DSSE signature verification followed by SLSA policy evaluation.
type Verifier struct {
	Verifiers     []signing.Verifier
	Policy        *policy.Policy
	MaxCheckLevel types.Level
}

// NewVerifier creates a Verifier with the given policy and accepted verifiers.
func NewVerifier(pol *policy.Policy, verifiers ...signing.Verifier) *Verifier {
	return &Verifier{
		Verifiers:     verifiers,
		Policy:        pol,
		MaxCheckLevel: types.Level4,
	}
}

// ─── VerifyEnvelope ───────────────────────────────────────────────────────────

// VerifyEnvelope verifies a DSSE envelope containing a SLSA v1 provenance statement.
//
// Steps:
//  1. Verify DSSE signature.
//  2. Decode in-toto statement.
//  3. Decode SLSA v1 predicate.
//  4. Evaluate policy.
//  5. Compute compliance level.
func (v *Verifier) VerifyEnvelope(env *signing.Envelope) (*Result, error) {
	if env == nil {
		return nil, errors.New("verifier: envelope must not be nil")
	}
	if len(v.Verifiers) == 0 {
		return nil, errors.New("verifier: no signature verifiers configured")
	}
	if v.Policy == nil {
		return nil, errors.New("verifier: no policy configured")
	}

	// Step 1: signature.
	sigIdx, err := v.verifySignature(env)
	if err != nil {
		return nil, fmt.Errorf("verifier: signature invalid: %w", err)
	}

	// Step 2: statement.
	payload, err := env.DecodePayload()
	if err != nil {
		return nil, fmt.Errorf("verifier: decode payload: %w", err)
	}
	stmt, err := attestation.UnmarshalStatement(payload)
	if err != nil {
		return nil, fmt.Errorf("verifier: parse statement: %w", err)
	}

	// Step 3: predicate.
	pred, err := decodeSLSAv1(stmt)
	if err != nil {
		return nil, fmt.Errorf("verifier: decode predicate: %w", err)
	}

	// Step 4: policy.
	var violations policy.Violations
	if polErr := v.Policy.EvaluateV1(pred); polErr != nil {
		vs, ok := polErr.(policy.Violations)
		if !ok {
			vs = policy.Violations{{Requirement: "unknown", Detail: polErr.Error()}}
		}
		violations = vs
	}

	// Step 5: compliance level.
	maxLevel := v.MaxCheckLevel
	if maxLevel == 0 {
		maxLevel = types.Level4
	}
	report, err := policy.CheckLevel(pred, maxLevel)
	if err != nil {
		return nil, fmt.Errorf("verifier: check level: %w", err)
	}

	return &Result{
		Statement:       stmt,
		ComplianceLevel: report.Level,
		SignatureIndex:  sigIdx,
		Violations:      violations,
		Passed:          len(violations) == 0,
	}, nil
}

// ─── VerifyEnvelopeV02 ────────────────────────────────────────────────────────

// VerifyEnvelopeV02 verifies a DSSE envelope containing a SLSA v0.2 predicate.
func (v *Verifier) VerifyEnvelopeV02(env *signing.Envelope) (*Result, error) {
	if env == nil {
		return nil, errors.New("verifier: envelope must not be nil")
	}
	if len(v.Verifiers) == 0 {
		return nil, errors.New("verifier: no signature verifiers configured")
	}
	if v.Policy == nil {
		return nil, errors.New("verifier: no policy configured")
	}

	sigIdx, err := v.verifySignature(env)
	if err != nil {
		return nil, fmt.Errorf("verifier: signature invalid: %w", err)
	}

	payload, err := env.DecodePayload()
	if err != nil {
		return nil, fmt.Errorf("verifier: decode payload: %w", err)
	}
	stmt, err := attestation.UnmarshalStatement(payload)
	if err != nil {
		return nil, fmt.Errorf("verifier: parse statement: %w", err)
	}

	pred, err := decodeSLSAv02(stmt)
	if err != nil {
		return nil, fmt.Errorf("verifier: decode v0.2 predicate: %w", err)
	}

	var violations policy.Violations
	if polErr := v.Policy.EvaluateV02(pred); polErr != nil {
		vs, ok := polErr.(policy.Violations)
		if !ok {
			vs = policy.Violations{{Requirement: "unknown", Detail: polErr.Error()}}
		}
		violations = vs
	}

	return &Result{
		Statement:      stmt,
		SignatureIndex: sigIdx,
		Violations:     violations,
		Passed:         len(violations) == 0,
	}, nil
}

// ─── helpers ──────────────────────────────────────────────────────────────────

func (v *Verifier) verifySignature(env *signing.Envelope) (int, error) {
	av := signing.NewAnyVerifier(v.Verifiers...)
	return env.Verify(av)
}

func decodeSLSAv1(stmt *attestation.Statement) (*provenance.PredicateV1, error) {
	var pred provenance.PredicateV1
	if err := json.Unmarshal(stmt.Predicate, &pred); err != nil {
		return nil, fmt.Errorf("decode SLSA v1 predicate: %w", err)
	}
	return &pred, nil
}

func decodeSLSAv02(stmt *attestation.Statement) (*provenance.PredicateV02, error) {
	var pred provenance.PredicateV02
	if err := json.Unmarshal(stmt.Predicate, &pred); err != nil {
		return nil, fmt.Errorf("decode SLSA v0.2 predicate: %w", err)
	}
	return &pred, nil
}

// EnvelopeFromJSON parses a DSSE envelope from JSON.
func EnvelopeFromJSON(data []byte) (*signing.Envelope, error) {
	var env signing.Envelope
	if err := json.Unmarshal(data, &env); err != nil {
		return nil, fmt.Errorf("parse DSSE envelope: %w", err)
	}
	return &env, nil
}
