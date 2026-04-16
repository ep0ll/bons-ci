// Package logger provides a minimal structured logger built on log/slog,
// exposing a zap-compatible API subset (Info, Warn, Error, Debug, With, Check).
// This avoids the go.uber.org/zap external dependency while keeping the call
// sites identical.
package logger

import (
	"context"
	"log/slog"
	"os"
)

// Field is a key-value log field (replaces zap.Field).
type Field struct {
	Key   string
	Value any
}

// Logger wraps slog.Logger with a zap-style API.
type Logger struct {
	inner  *slog.Logger
	fields []Field
}

// NewNop returns a logger that discards all output.
func NewNop() *Logger {
	return &Logger{inner: slog.New(slog.NewTextHandler(os.Stderr, &slog.HandlerOptions{Level: slog.LevelError + 100}))}
}

// NewProduction returns a JSON logger writing to stderr at Info level.
func NewProduction() *Logger {
	return &Logger{inner: slog.New(slog.NewJSONHandler(os.Stderr, &slog.HandlerOptions{Level: slog.LevelInfo}))}
}

// Sync flushes buffered log entries (no-op for slog).
func (l *Logger) Sync() error { return nil }

// With returns a child logger with the given fields pre-attached.
func (l *Logger) With(fields ...Field) *Logger {
	args := fieldsToArgs(fields)
	return &Logger{inner: l.inner.With(args...)}
}

func (l *Logger) Info(msg string, fields ...Field)  { l.inner.Info(msg, fieldsToArgs(fields)...) }
func (l *Logger) Debug(msg string, fields ...Field) { l.inner.Debug(msg, fieldsToArgs(fields)...) }
func (l *Logger) Warn(msg string, fields ...Field)  { l.inner.Warn(msg, fieldsToArgs(fields)...) }
func (l *Logger) Error(msg string, fields ...Field) { l.inner.Error(msg, fieldsToArgs(fields)...) }
func (l *Logger) Fatal(msg string, fields ...Field) {
	l.inner.Error("FATAL: "+msg, fieldsToArgs(fields)...)
	os.Exit(1)
}

// Check returns a *CheckedEntry that mirrors zap.Logger.Check (always non-nil).
func (l *Logger) Check(level Level, msg string) *CheckedEntry {
	return &CheckedEntry{logger: l, level: level, msg: msg}
}

// CheckedEntry mirrors zap.CheckedEntry.
type CheckedEntry struct {
	logger *Logger
	level  Level
	msg    string
}

func (c *CheckedEntry) Write(fields ...Field) {
	switch c.level {
	case DebugLevel:
		c.logger.Debug(c.msg, fields...)
	case InfoLevel:
		c.logger.Info(c.msg, fields...)
	case WarnLevel:
		c.logger.Warn(c.msg, fields...)
	default:
		c.logger.Error(c.msg, fields...)
	}
}

// Level mirrors zapcore.Level.
type Level int8

const (
	DebugLevel Level = iota - 1
	InfoLevel
	WarnLevel
	ErrorLevel
)

// ── Field constructors matching zap.String, zap.Error, etc. ──────────────

func String(key, val string) Field          { return Field{key, val} }
func Error(err error) Field                 { return Field{"error", err} }
func Int(key string, val int) Field         { return Field{key, val} }
func Int64(key string, val int64) Field     { return Field{key, val} }
func Float64(key string, val float64) Field { return Field{key, val} }
func Bool(key string, val bool) Field       { return Field{key, val} }
func Any(key string, val any) Field         { return Field{key, val} }
func ByteString(key string, val []byte) Field { return Field{key, string(val)} }

// Duration formats a duration as a string field.
func Duration(key string, val interface{ String() string }) Field {
	return Field{key, val.String()}
}

// NewAtomicLevelAt is a no-op stub for zap.NewAtomicLevelAt compatibility.
func NewAtomicLevelAt(_ Level) Level { return InfoLevel }

// ── Build helpers ─────────────────────────────────────────────────────────

// Build returns a production Logger; ignores cfg (compatibility stub).
func Build(_ any, _ ...interface{}) (*Logger, error) { return NewProduction(), nil }

// ── Context helpers ───────────────────────────────────────────────────────

func FromContext(_ context.Context) *Logger { return NewProduction() }

// ── Internals ─────────────────────────────────────────────────────────────

func fieldsToArgs(fields []Field) []any {
	if len(fields) == 0 {
		return nil
	}
	args := make([]any, 0, len(fields)*2)
	for _, f := range fields {
		args = append(args, f.Key, f.Value)
	}
	return args
}
