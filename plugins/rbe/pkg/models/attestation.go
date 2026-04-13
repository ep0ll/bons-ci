package models

import "time"

type AttestationType string

const (
	AttestationTypeSBOMSPDX       AttestationType = "sbom-spdx"
	AttestationTypeSBOMCycloneDX  AttestationType = "sbom-cyclonedx"
	AttestationTypeSLSA           AttestationType = "slsa-provenance"
	AttestationTypeCosign         AttestationType = "cosign"
	AttestationTypeCustom         AttestationType = "custom"
)

type Attestation struct {
	ID               string            `json:"id"`
	SubjectDigest    string            `json:"subject_digest"`
	SubjectRepo      string            `json:"subject_repo"`
	Type             AttestationType   `json:"type"`
	PredicateType    string            `json:"predicate_type"`
	Payload          []byte            `json:"payload"`   // DSSE envelope
	Signature        []byte            `json:"signature,omitempty"`
	PublicKey        []byte            `json:"public_key,omitempty"`
	KeyID            string            `json:"key_id,omitempty"`
	SigningCert      string            `json:"signing_cert,omitempty"` // PEM
	TransparencyLogID string           `json:"transparency_log_id,omitempty"` // Rekor
	SignedAt         time.Time         `json:"signed_at"`
	Metadata         map[string]string `json:"metadata,omitempty"`
}

type SLSAProvenance struct {
	BuildType       string            `json:"buildType"`
	BuilderID       string            `json:"builder_id"`
	Materials       []string          `json:"materials"`
	BuildConfig     map[string]string `json:"build_config,omitempty"`
	StartedAt       time.Time         `json:"started_at"`
	FinishedAt      time.Time         `json:"finished_at"`
	Reproducible    bool              `json:"reproducible"`
	DAGID           string            `json:"dag_id,omitempty"`
}
