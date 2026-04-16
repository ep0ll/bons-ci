package transform

import (
	"context"
	"strconv"
	"time"

	"github.com/bons/bons-ci/plugins/exporter/core"
)

const (
	// MetaKeySourceDateEpoch is the metadata key carrying SOURCE_DATE_EPOCH
	// as a Unix-second string, injected by the frontend before export.
	MetaKeySourceDateEpoch = "source.date.epoch"

	// MetaKeyEpochApplied is set to "true" by EpochTransformer after it runs,
	// allowing downstream transformers to detect it was applied.
	MetaKeyEpochApplied = "exporter.epoch.applied"
)

// EpochTransformerOptions controls EpochTransformer behaviour.
type EpochTransformerOptions struct {
	// FallbackToNow, when true, uses time.Now() as epoch if none is found.
	// When false (default), layers without an explicit epoch are left unchanged.
	FallbackToNow bool
}

// EpochTransformerOption is a functional option for EpochTransformer.
type EpochTransformerOption func(*EpochTransformerOptions)

// WithFallbackToNow sets FallbackToNow.
func WithFallbackToNow(v bool) EpochTransformerOption {
	return func(o *EpochTransformerOptions) { o.FallbackToNow = v }
}

// EpochTransformer clamps all layer and config timestamps to the
// SOURCE_DATE_EPOCH value found in the artifact metadata, enabling
// fully reproducible builds.
//
// SOURCE_DATE_EPOCH specification: https://reproducible-builds.org/docs/source-date-epoch/
type EpochTransformer struct {
	BaseTransformer
	opts EpochTransformerOptions
}

// NewEpochTransformer creates an EpochTransformer.
func NewEpochTransformer(options ...EpochTransformerOption) *EpochTransformer {
	opts := EpochTransformerOptions{}
	for _, o := range options {
		o(&opts)
	}
	return &EpochTransformer{
		BaseTransformer: NewBase("epoch-normaliser", PriorityPreProcess),
		opts:            opts,
	}
}

// Transform clamps layer history timestamps to the resolved epoch value.
// If no epoch is resolvable and FallbackToNow is false, the artifact is
// returned unmodified (idempotent).
func (t *EpochTransformer) Transform(ctx context.Context, a *core.Artifact) (*core.Artifact, error) {
	epoch, err := t.resolveEpoch(a)
	if err != nil {
		return nil, err
	}
	if epoch == nil {
		if !t.opts.FallbackToNow {
			return a, nil
		}
		now := time.Now().UTC()
		epoch = &now
	}

	clone := a.Clone()

	// Clamp layer history timestamps.
	for i, layer := range clone.Layers {
		if layer.History == nil {
			continue
		}
		if layer.History.CreatedAt == nil || layer.History.CreatedAt.After(*epoch) {
			clamped := *epoch
			clone.Layers[i].History.CreatedAt = &clamped
		}
	}

	// Record that epoch was applied.
	if clone.Metadata == nil {
		clone.Metadata = make(map[string][]byte)
	}
	clone.Metadata[MetaKeyEpochApplied] = []byte("true")
	clone.Metadata[MetaKeySourceDateEpoch] = []byte(strconv.FormatInt(epoch.Unix(), 10))

	return clone, nil
}

// resolveEpoch reads the epoch from artifact metadata (SOURCE_DATE_EPOCH).
// Returns nil (no error) when the key is absent.
func (t *EpochTransformer) resolveEpoch(a *core.Artifact) (*time.Time, error) {
	raw, ok := a.Metadata[MetaKeySourceDateEpoch]
	if !ok || len(raw) == 0 {
		return nil, nil
	}
	secs, err := strconv.ParseInt(string(raw), 10, 64)
	if err != nil {
		return nil, core.NewValidationError(
			MetaKeySourceDateEpoch,
			"must be a Unix second integer, got: "+string(raw),
		)
	}
	tm := time.Unix(secs, 0).UTC()
	return &tm, nil
}
