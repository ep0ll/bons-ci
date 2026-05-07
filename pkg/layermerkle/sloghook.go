package layermerkle

import (
	"context"
	"log/slog"
)

// ─────────────────────────────────────────────────────────────────────────────
// SlogHook — structured logging via log/slog
// ─────────────────────────────────────────────────────────────────────────────

// SlogHook is an EngineHook that writes structured log entries via a
// *slog.Logger. Each engine event is logged at a configurable level.
//
// Recommended levels:
//   - Cache hits: slog.LevelDebug (very high frequency)
//   - Hash starts: slog.LevelDebug
//   - Hash errors: slog.LevelWarn
//   - Event drops: slog.LevelWarn
//   - Vertex finalized: slog.LevelInfo
type SlogHook struct {
	NoopHook
	logger        *slog.Logger
	hitLevel      slog.Level
	hashLevel     slog.Level
	errorLevel    slog.Level
	dropLevel     slog.Level
	finalizeLevel slog.Level
}

// SlogHookConfig configures a SlogHook.
type SlogHookConfig struct {
	// Logger is the slog.Logger to write to. Defaults to slog.Default().
	Logger *slog.Logger

	// HitLevel is the level for cache hit events. Default: Debug.
	HitLevel slog.Level

	// HashLevel is the level for hash computation events. Default: Debug.
	HashLevel slog.Level

	// ErrorLevel is the level for hash errors. Default: Warn.
	ErrorLevel slog.Level

	// DropLevel is the level for dropped events. Default: Warn.
	DropLevel slog.Level

	// FinalizeLevel is the level for vertex finalization events. Default: Info.
	FinalizeLevel slog.Level
}

// NewSlogHook constructs a SlogHook with sensible default levels.
func NewSlogHook(logger *slog.Logger) *SlogHook {
	if logger == nil {
		logger = slog.Default()
	}
	return &SlogHook{
		logger:        logger,
		hitLevel:      slog.LevelDebug,
		hashLevel:     slog.LevelDebug,
		errorLevel:    slog.LevelWarn,
		dropLevel:     slog.LevelWarn,
		finalizeLevel: slog.LevelInfo,
	}
}

// NewSlogHookWithConfig constructs a SlogHook with explicit level configuration.
func NewSlogHookWithConfig(cfg SlogHookConfig) *SlogHook {
	h := NewSlogHook(cfg.Logger)
	if cfg.HitLevel != 0 {
		h.hitLevel = cfg.HitLevel
	}
	if cfg.HashLevel != 0 {
		h.hashLevel = cfg.HashLevel
	}
	if cfg.ErrorLevel != 0 {
		h.errorLevel = cfg.ErrorLevel
	}
	if cfg.DropLevel != 0 {
		h.dropLevel = cfg.DropLevel
	}
	if cfg.FinalizeLevel != 0 {
		h.finalizeLevel = cfg.FinalizeLevel
	}
	return h
}

// OnCacheHit implements EngineHook.
func (h *SlogHook) OnCacheHit(ctx context.Context, req HashRequest, _ *HashResult) {
	h.logger.Log(ctx, h.hitLevel, "layermerkle: cache hit",
		slog.String("vertex", shortDigest(req.VertexID)),
		slog.String("path", req.RelPath),
	)
}

// OnHashStart implements EngineHook.
func (h *SlogHook) OnHashStart(ctx context.Context, req HashRequest) {
	h.logger.Log(ctx, h.hashLevel, "layermerkle: hashing file",
		slog.String("vertex", shortDigest(req.VertexID)),
		slog.String("path", req.RelPath),
		slog.String("top_layer", shortDigest(req.LayerStack.Top())),
	)
}

// OnHashComplete implements EngineHook.
func (h *SlogHook) OnHashComplete(ctx context.Context, req HashRequest, hash FileHash) {
	h.logger.Log(ctx, h.hashLevel, "layermerkle: hash computed",
		slog.String("vertex", shortDigest(req.VertexID)),
		slog.String("path", req.RelPath),
		slog.String("hash", shortDigest(hash)),
	)
}

// OnHashError implements EngineHook.
func (h *SlogHook) OnHashError(ctx context.Context, req HashRequest, err error) {
	h.logger.Log(ctx, h.errorLevel, "layermerkle: hash error",
		slog.String("vertex", shortDigest(req.VertexID)),
		slog.String("path", req.RelPath),
		slog.Any("error", err),
	)
}

// OnEventDropped implements EngineHook.
func (h *SlogHook) OnEventDropped(ctx context.Context, ev *AccessEvent, reason error) {
	h.logger.Log(ctx, h.dropLevel, "layermerkle: event dropped",
		slog.String("vertex", shortDigest(ev.VertexID)),
		slog.String("path", ev.RelPath),
		slog.Any("reason", reason),
	)
}

// OnVertexFinalized implements EngineHook.
func (h *SlogHook) OnVertexFinalized(ctx context.Context, tree *MerkleTree) {
	h.logger.Log(ctx, h.finalizeLevel, "layermerkle: vertex finalized",
		slog.String("vertex", shortDigest(tree.VertexID)),
		slog.String("root", string(tree.Root)),
		slog.Int("leaves", tree.LeafCount),
		slog.Int("cache_hits", tree.CacheHitCount),
		slog.Float64("cache_rate", tree.CacheHitRate()),
	)
}
