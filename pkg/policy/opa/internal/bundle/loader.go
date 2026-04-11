// Package bundle provides hot-reloadable Rego policy management.
// It isolates all I/O from the evaluation layer, exposing only a compiled
// Compiler that transparently hot-swaps when the source changes.
package bundle

import (
	"context"
	"fmt"
	"io/fs"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"time"

	"go.opentelemetry.io/otel/attribute"
	"go.opentelemetry.io/otel/metric"
	"go.opentelemetry.io/otel/trace"

	"github.com/bons/bons-ci/pkg/policy/opa/internal/eval"
	polOtel "github.com/bons/bons-ci/pkg/policy/opa/internal/otel"
)

// ─── Source interface ─────────────────────────────────────────────────────────

// Source is an abstract origin for Rego module text.
// Implementations are pure (no side effects beyond I/O).
type Source interface {
	// Load returns map[logicalName]regoSource.
	// logicalName is used only in OPA error messages; it must be unique.
	Load(ctx context.Context) (map[string]string, error)
}

// ─── Source implementations ───────────────────────────────────────────────────

// DirSource loads all *.rego files under Root recursively.
type DirSource struct {
	Root string
}

func (d *DirSource) Load(_ context.Context) (map[string]string, error) {
	if d.Root == "" {
		return nil, fmt.Errorf("bundle: DirSource.Root is empty")
	}
	modules := make(map[string]string)
	err := filepath.WalkDir(d.Root, func(path string, e fs.DirEntry, walkErr error) error {
		if walkErr != nil {
			return walkErr
		}
		if e.IsDir() || !strings.HasSuffix(path, ".rego") {
			return nil
		}
		raw, err := os.ReadFile(path)
		if err != nil {
			return fmt.Errorf("bundle: read %q: %w", path, err)
		}
		rel, _ := filepath.Rel(d.Root, path)
		modules[rel] = string(raw)
		return nil
	})
	return modules, err
}

// StaticSource holds pre-defined Rego modules (embedded policies, tests).
type StaticSource struct {
	Modules map[string]string
}

func (s *StaticSource) Load(_ context.Context) (map[string]string, error) {
	if s.Modules == nil {
		return map[string]string{}, nil
	}
	out := make(map[string]string, len(s.Modules))
	for k, v := range s.Modules {
		out[k] = v
	}
	return out, nil
}

// ComposedSource merges multiple sources. Later sources overwrite earlier ones
// for the same logical filename, enabling override patterns.
type ComposedSource []Source

func (c ComposedSource) Load(ctx context.Context) (map[string]string, error) {
	merged := make(map[string]string)
	for i, s := range c {
		mods, err := s.Load(ctx)
		if err != nil {
			return nil, fmt.Errorf("bundle: source[%d]: %w", i, err)
		}
		for k, v := range mods {
			merged[k] = v
		}
	}
	return merged, nil
}

// ─── Loader ───────────────────────────────────────────────────────────────────

// Loader builds and optionally hot-reloads a Compiler from a Source.
// It is goroutine-safe. The zero value is not usable; use NewLoader.
type Loader struct {
	src    Source
	opts   []eval.CompilerOption
	mu     sync.RWMutex
	cur    *eval.Compiler // nil until first successful Build
	notify chan struct{}  // closed-and-replaced on each reload; never nil

	// telemetry
	tracer    trace.Tracer
	reloads   metric.Int64Counter
	reloadErr metric.Int64Counter
}

// NewLoader creates a Loader bound to src. Call Build or Watch before using Compiler().
func NewLoader(src Source, opts ...eval.CompilerOption) (*Loader, error) {
	m := polOtel.Meter("bundle")
	prefix := polOtel.Namespace + ".bundle"

	rel, err := m.Int64Counter(prefix+".reloads_total",
		metric.WithDescription("Successful policy reloads"))
	if err != nil {
		return nil, fmt.Errorf("bundle: metric reloads: %w", err)
	}
	relErr, err := m.Int64Counter(prefix+".reload_errors_total",
		metric.WithDescription("Failed policy reload attempts"))
	if err != nil {
		return nil, fmt.Errorf("bundle: metric reload_errors: %w", err)
	}

	return &Loader{
		src:       src,
		opts:      opts,
		notify:    make(chan struct{}),
		tracer:    polOtel.Tracer("bundle"),
		reloads:   rel,
		reloadErr: relErr,
	}, nil
}

// Build compiles policies from the source. Must be called at least once before
// Compiler() returns a non-nil value. Subsequent calls hot-swap the compiler.
func (l *Loader) Build(ctx context.Context) error {
	ctx, end := polOtel.StartSpan(ctx, l.tracer, "bundle.build")
	var retErr error
	defer end(&retErr)

	c, err := l.compile(ctx)
	if err != nil {
		l.reloadErr.Add(ctx, 1)
		retErr = err
		return err
	}

	l.mu.Lock()
	if l.cur != nil {
		l.cur.HotSwap(c)
	} else {
		l.cur = c
	}
	old := l.notify
	l.notify = make(chan struct{})
	l.mu.Unlock()

	// Signal all waiters that new policies are available.
	close(old)
	l.reloads.Add(ctx, 1)
	return nil
}

// Compiler returns the most recently compiled Compiler.
// Returns nil if Build has not been called yet.
func (l *Loader) Compiler() *eval.Compiler {
	l.mu.RLock()
	defer l.mu.RUnlock()
	return l.cur
}

// Changed returns a channel that is closed whenever a new build completes.
// Callers that want to react to hot-reloads select on this channel.
func (l *Loader) Changed() <-chan struct{} {
	l.mu.RLock()
	defer l.mu.RUnlock()
	return l.notify
}

// Watch calls Build on the given interval, performing hot-swaps on change.
// It blocks until ctx is cancelled. An initial Build is performed immediately.
// Non-fatal reload errors are logged via OTEL but do not stop the watch.
func (l *Loader) Watch(ctx context.Context, interval time.Duration) error {
	if err := l.Build(ctx); err != nil {
		return fmt.Errorf("bundle: initial build: %w", err)
	}
	ticker := time.NewTicker(interval)
	defer ticker.Stop()
	for {
		select {
		case <-ctx.Done():
			return ctx.Err()
		case <-ticker.C:
			_ = l.Build(ctx) // errors are non-fatal; previous compiler stays active
		}
	}
}

// ─── internal ─────────────────────────────────────────────────────────────────

func (l *Loader) compile(ctx context.Context) (*eval.Compiler, error) {
	mods, err := l.src.Load(ctx)
	if err != nil {
		return nil, fmt.Errorf("bundle: load: %w", err)
	}
	c, err := eval.NewCompiler(mods, l.opts...)
	if err != nil {
		return nil, fmt.Errorf("bundle: compile: %w", err)
	}
	polOtel.AddEvent(ctx, "bundle.compiled",
		attribute.Int("module_count", len(mods)),
	)
	return c, nil
}
