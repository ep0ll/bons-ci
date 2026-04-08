package exporter

import (
	"context"
	"io"

	"github.com/anchore/syft/syft/format"
	"github.com/anchore/syft/syft/format/spdxjson"
	"github.com/anchore/syft/syft/format/spdxtagvalue"
	"go.uber.org/zap"

	"github.com/bons/bons-ci/pkg/sbomkit/core/domain"
	"github.com/bons/bons-ci/pkg/sbomkit/core/ports"
)

// SPDXJSONExporter serialises to SPDX JSON.
type SPDXJSONExporter struct {
	logger *zap.Logger
}

// NewSPDXJSONExporter constructs an SPDXJSONExporter.
func NewSPDXJSONExporter(logger *zap.Logger) *SPDXJSONExporter {
	return &SPDXJSONExporter{logger: logger}
}

// Format implements ports.Exporter.
func (e *SPDXJSONExporter) Format() domain.Format { return domain.FormatSPDXJSON }

// Export implements ports.Exporter.
func (e *SPDXJSONExporter) Export(ctx context.Context, sbom *domain.SBOM, w io.Writer) error {
	enc, err := spdxjson.NewFormatEncoderWithConfig(spdxjson.DefaultEncoderConfig())
	if err != nil {
		return domain.Newf(domain.ErrKindExporting, err, "creating spdx-json encoder")
	}

	raw, ok := asSyftSBOM(sbom)
	if !ok {
		e.logger.Warn("no native syft SBOM; exporting spdx-json from domain types (reduced fidelity)")
		raw = domainToSyft(sbom)
	}

	encoded, err := format.Encode(*raw, enc)
	if err != nil {
		return domain.Newf(domain.ErrKindExporting, err, "encoding spdx-json")
	}

	if _, err := w.Write(encoded); err != nil {
		return domain.Newf(domain.ErrKindExporting, err, "writing spdx-json output")
	}
	return nil
}

var _ ports.Exporter = (*SPDXJSONExporter)(nil)

// ── SPDX tag-value ───────────────────────────────────────────────────────────

// SPDXTagValueExporter serialises to SPDX tag-value format.
type SPDXTagValueExporter struct {
	logger *zap.Logger
}

// NewSPDXTagValueExporter constructs an SPDXTagValueExporter.
func NewSPDXTagValueExporter(logger *zap.Logger) *SPDXTagValueExporter {
	return &SPDXTagValueExporter{logger: logger}
}

// Format implements ports.Exporter.
func (e *SPDXTagValueExporter) Format() domain.Format { return domain.FormatSPDXTagValue }

// Export implements ports.Exporter.
func (e *SPDXTagValueExporter) Export(ctx context.Context, sbom *domain.SBOM, w io.Writer) error {
	enc, err := spdxtagvalue.NewFormatEncoderWithConfig(spdxtagvalue.DefaultEncoderConfig())
	if err != nil {
		return domain.Newf(domain.ErrKindExporting, err, "creating spdx-tag-value encoder")
	}

	raw, ok := asSyftSBOM(sbom)
	if !ok {
		e.logger.Warn("no native syft SBOM; exporting spdx-tag-value from domain types (reduced fidelity)")
		raw = domainToSyft(sbom)
	}

	encoded, err := format.Encode(*raw, enc)
	if err != nil {
		return domain.Newf(domain.ErrKindExporting, err, "encoding spdx-tag-value")
	}

	if _, err := w.Write(encoded); err != nil {
		return domain.Newf(domain.ErrKindExporting, err, "writing spdx-tag-value output")
	}
	return nil
}

var _ ports.Exporter = (*SPDXTagValueExporter)(nil)
