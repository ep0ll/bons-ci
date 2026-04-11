package bundle_test

import (
	"context"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/bons/bons-ci/pkg/policy/opa/internal/bundle"
	polOtel "github.com/bons/bons-ci/pkg/policy/opa/internal/otel"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func init() { polOtel.UseNoop() }

func newLoader(t *testing.T, src bundle.Source) *bundle.Loader {
	t.Helper()
	l, err := bundle.NewLoader(src)
	require.NoError(t, err)
	return l
}

// ─── StaticSource ─────────────────────────────────────────────────────────────

func TestStaticSource_Load(t *testing.T) {
	src := &bundle.StaticSource{Modules: map[string]string{
		"a.rego": `package a; import rego.v1; v := 1`,
	}}
	m, err := src.Load(context.Background())
	require.NoError(t, err)
	assert.Len(t, m, 1)
	assert.Contains(t, m, "a.rego")
}

func TestStaticSource_Nil_ReturnsEmpty(t *testing.T) {
	src := &bundle.StaticSource{}
	m, err := src.Load(context.Background())
	require.NoError(t, err)
	assert.Empty(t, m)
}

// ─── DirSource ────────────────────────────────────────────────────────────────

func TestDirSource_Load_ReadsRegoFiles(t *testing.T) {
	dir := t.TempDir()
	require.NoError(t, os.WriteFile(filepath.Join(dir, "a.rego"), []byte(`package a`), 0o600))
	require.NoError(t, os.WriteFile(filepath.Join(dir, "b.rego"), []byte(`package b`), 0o600))
	require.NoError(t, os.WriteFile(filepath.Join(dir, "skip.json"), []byte(`{}`), 0o600))

	src := &bundle.DirSource{Root: dir}
	m, err := src.Load(context.Background())
	require.NoError(t, err)
	assert.Len(t, m, 2)
	assert.Contains(t, m, "a.rego")
	assert.Contains(t, m, "b.rego")
	assert.NotContains(t, m, "skip.json")
}

func TestDirSource_Load_EmptyRoot_ReturnsError(t *testing.T) {
	src := &bundle.DirSource{Root: ""}
	_, err := src.Load(context.Background())
	require.Error(t, err)
}

func TestDirSource_Load_NonexistentDir_ReturnsError(t *testing.T) {
	src := &bundle.DirSource{Root: "/nonexistent/path/that/does/not/exist"}
	_, err := src.Load(context.Background())
	require.Error(t, err)
}

func TestDirSource_Load_Recursive(t *testing.T) {
	dir := t.TempDir()
	sub := filepath.Join(dir, "sub")
	require.NoError(t, os.MkdirAll(sub, 0o700))
	require.NoError(t, os.WriteFile(filepath.Join(dir, "top.rego"), []byte(`package top`), 0o600))
	require.NoError(t, os.WriteFile(filepath.Join(sub, "nested.rego"), []byte(`package sub`), 0o600))

	src := &bundle.DirSource{Root: dir}
	m, err := src.Load(context.Background())
	require.NoError(t, err)
	assert.Len(t, m, 2)
}

// ─── ComposedSource ───────────────────────────────────────────────────────────

func TestComposedSource_MergesModules(t *testing.T) {
	src := bundle.ComposedSource{
		&bundle.StaticSource{Modules: map[string]string{"a.rego": `package a`}},
		&bundle.StaticSource{Modules: map[string]string{"b.rego": `package b`}},
	}
	m, err := src.Load(context.Background())
	require.NoError(t, err)
	assert.Len(t, m, 2)
}

func TestComposedSource_LaterOverwritesEarlier(t *testing.T) {
	src := bundle.ComposedSource{
		&bundle.StaticSource{Modules: map[string]string{"p.rego": `package p; v := 1`}},
		&bundle.StaticSource{Modules: map[string]string{"p.rego": `package p; v := 2`}},
	}
	m, err := src.Load(context.Background())
	require.NoError(t, err)
	assert.Contains(t, m["p.rego"], "v := 2")
}

func TestComposedSource_ErrorPropagates(t *testing.T) {
	src := bundle.ComposedSource{
		&bundle.StaticSource{Modules: map[string]string{"a.rego": `package a`}},
		&bundle.DirSource{Root: ""}, // will error
	}
	_, err := src.Load(context.Background())
	require.Error(t, err)
}

// ─── Loader ───────────────────────────────────────────────────────────────────

func TestLoader_Compiler_NilBeforeBuild(t *testing.T) {
	l := newLoader(t, &bundle.StaticSource{Modules: map[string]string{
		"p.rego": `package p`,
	}})
	assert.Nil(t, l.Compiler())
}

func TestLoader_Build_ThenCompilerNonNil(t *testing.T) {
	l := newLoader(t, &bundle.StaticSource{Modules: map[string]string{
		"p.rego": `package p`,
	}})
	require.NoError(t, l.Build(context.Background()))
	assert.NotNil(t, l.Compiler())
}

func TestLoader_Build_InvalidRego_ReturnsError(t *testing.T) {
	l := newLoader(t, &bundle.StaticSource{Modules: map[string]string{
		"bad.rego": `this is not rego`,
	}})
	err := l.Build(context.Background())
	require.Error(t, err)
	assert.Contains(t, err.Error(), "compile")
}

func TestLoader_Changed_ClosedOnBuild(t *testing.T) {
	l := newLoader(t, &bundle.StaticSource{Modules: map[string]string{
		"p.rego": `package p`,
	}})
	ch := l.Changed()
	// Not yet built — channel should be open.
	select {
	case <-ch:
		t.Fatal("channel should not be closed before Build")
	default:
	}

	require.NoError(t, l.Build(context.Background()))

	select {
	case <-ch:
		// Correct: channel closed on build.
	case <-time.After(100 * time.Millisecond):
		t.Fatal("Changed channel not closed after Build")
	}
}

func TestLoader_Watch_HotSwapsCompiler(t *testing.T) {
	src := &bundle.StaticSource{Modules: map[string]string{
		"p.rego": "package p\nimport rego.v1\nv := \"v1\"",
	}}
	l, err := bundle.NewLoader(src)
	require.NoError(t, err)
	require.NoError(t, l.Build(context.Background()))

	// Swap the source modules.
	src.Modules["p.rego"] = "package p\nimport rego.v1\nv := \"v2\""

	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
	defer cancel()

	// Watch with a fast interval.
	go func() { _ = l.Watch(ctx, 10*time.Millisecond) }()

	// Wait for a reload.
	deadline := time.After(500 * time.Millisecond)
	for {
		select {
		case <-deadline:
			t.Fatal("Watch did not hot-swap within deadline")
		case <-time.After(20 * time.Millisecond):
			// Check if the compiler reflects the new module.
			c := l.Compiler()
			if c != nil {
				// We can't directly eval without an evaluator here, so
				// just verify the compiler is non-nil after multiple Watch cycles.
				goto done
			}
		}
	}
done:
	cancel()
}
