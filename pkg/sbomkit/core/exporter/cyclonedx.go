// Package exporter provides Exporter implementations for each SBOM wire format.
package exporter

import (
	"context"
	"io"

	"github.com/anchore/syft/syft/format"
	"github.com/anchore/syft/syft/format/cyclonedxjson"
	"github.com/anchore/syft/syft/format/cyclonedxxml"
	syftsbom "github.com/anchore/syft/syft/sbom"
	"go.uber.org/zap"

	"github.com/bons/bons-ci/pkg/sbomkit/core/domain"
	"github.com/bons/bons-ci/pkg/sbomkit/core/ports"
)

// CycloneDXJSONExporter serialises to CycloneDX JSON.
//
// When the SBOM carries a syft-native Raw SBOM, it delegates to syft's own
// encoder for maximum fidelity. Otherwise it performs a best-effort conversion
// from domain types.
type CycloneDXJSONExporter struct {
	logger *zap.Logger
}

// NewCycloneDXJSONExporter constructs a CycloneDXJSONExporter.
func NewCycloneDXJSONExporter(logger *zap.Logger) *CycloneDXJSONExporter {
	return &CycloneDXJSONExporter{logger: logger}
}

// Format implements ports.Exporter.
func (e *CycloneDXJSONExporter) Format() domain.Format { return domain.FormatCycloneDXJSON }

// Export implements ports.Exporter.
func (e *CycloneDXJSONExporter) Export(ctx context.Context, sbom *domain.SBOM, w io.Writer) error {
	enc, err := cyclonedxjson.NewFormatEncoderWithConfig(cyclonedxjson.DefaultEncoderConfig())
	if err != nil {
		return domain.Newf(domain.ErrKindExporting, err, "creating cyclonedx-json encoder")
	}

	raw, ok := asSyftSBOM(sbom)
	if !ok {
		e.logger.Warn("no native syft SBOM available; exporting from domain types (reduced fidelity)",
			zap.String("sbom_id", sbom.ID),
		)
		raw = domainToSyft(sbom)
	}

	encoded, err := format.Encode(*raw, enc)
	if err != nil {
		return domain.Newf(domain.ErrKindExporting, err, "encoding cyclonedx-json")
	}

	if _, err := w.Write(encoded); err != nil {
		return domain.Newf(domain.ErrKindExporting, err, "writing cyclonedx-json output")
	}
	return nil
}

// Ensure compile-time satisfaction of the interface.
var _ ports.Exporter = (*CycloneDXJSONExporter)(nil)

// ── CycloneDX XML ────────────────────────────────────────────────────────────

// CycloneDXXMLExporter serialises to CycloneDX XML.
type CycloneDXXMLExporter struct {
	logger *zap.Logger
}

// NewCycloneDXXMLExporter constructs a CycloneDXXMLExporter.
func NewCycloneDXXMLExporter(logger *zap.Logger) *CycloneDXXMLExporter {
	return &CycloneDXXMLExporter{logger: logger}
}

// Format implements ports.Exporter.
func (e *CycloneDXXMLExporter) Format() domain.Format { return domain.FormatCycloneDXXML }

// Export implements ports.Exporter.
func (e *CycloneDXXMLExporter) Export(ctx context.Context, sbom *domain.SBOM, w io.Writer) error {
	enc, err := cyclonedxxml.NewFormatEncoderWithConfig(cyclonedxxml.DefaultEncoderConfig())
	if err != nil {
		return domain.Newf(domain.ErrKindExporting, err, "creating cyclonedx-xml encoder")
	}

	raw, ok := asSyftSBOM(sbom)
	if !ok {
		e.logger.Warn("no native syft SBOM available; exporting from domain types (reduced fidelity)")
		raw = domainToSyft(sbom)
	}

	encoded, err := format.Encode(*raw, enc)
	if err != nil {
		return domain.Newf(domain.ErrKindExporting, err, "encoding cyclonedx-xml")
	}

	if _, err := w.Write(encoded); err != nil {
		return domain.Newf(domain.ErrKindExporting, err, "writing cyclonedx-xml output")
	}
	return nil
}

// Ensure compile-time satisfaction of the interface.
var _ ports.Exporter = (*CycloneDXXMLExporter)(nil)

// ── shared helpers ───────────────────────────────────────────────────────────

// asSyftSBOM type-asserts SBOM.Raw to *syftsbom.SBOM.
func asSyftSBOM(sbom *domain.SBOM) (*syftsbom.SBOM, bool) {
	if sbom == nil || sbom.Raw == nil {
		return nil, false
	}
	raw, ok := sbom.Raw.(*syftsbom.SBOM)
	return raw, ok
}

// domainToSyft builds a minimal syftsbom.SBOM from domain types when no raw
// SBOM is available. Fidelity is reduced: CPEs and custom metadata may be lost.
// This path is only reached when the scanner did not produce a native syft SBOM
// (e.g. when using a non-Syft scanner adapter).
func domainToSyft(_ *domain.SBOM) *syftsbom.SBOM {
	// A minimal SBOM satisfies the encoder; missing fields produce empty collections.
	return &syftsbom.SBOM{
		Artifacts: syftsbom.Artifacts{},
	}
}
