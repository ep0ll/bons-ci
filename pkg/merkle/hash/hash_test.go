package hash_test

import (
	"context"
	"testing"

	"github.com/user/layermerkle/hash"
	"github.com/user/layermerkle/layer"
)

var baseStack = layer.MustNew(layer.Digest("base"))

func req(path string) hash.HashRequest {
	return hash.HashRequest{
		FilePath:    path,
		LayerStack:  baseStack,
		OutputLayer: "base",
	}
}

// ─── SyntheticProvider ────────────────────────────────────────────────────────

func TestSyntheticProvider_Deterministic(t *testing.T) {
	p := hash.NewSyntheticProvider()
	ctx := context.Background()
	r1, _ := p.Hash(ctx, req("/bin/sh"))
	r2, _ := p.Hash(ctx, req("/bin/sh"))
	if string(r1.Hash) != string(r2.Hash) {
		t.Fatal("SyntheticProvider must be deterministic")
	}
}

func TestSyntheticProvider_DifferentPaths(t *testing.T) {
	p := hash.NewSyntheticProvider()
	ctx := context.Background()
	r1, _ := p.Hash(ctx, req("/bin/sh"))
	r2, _ := p.Hash(ctx, req("/etc/passwd"))
	if string(r1.Hash) == string(r2.Hash) {
		t.Fatal("different paths must produce different hashes")
	}
}

func TestSyntheticProvider_DifferentLayers(t *testing.T) {
	p := hash.NewSyntheticProvider()
	ctx := context.Background()
	r1, _ := p.Hash(ctx, hash.HashRequest{
		FilePath:    "/same",
		LayerStack:  layer.MustNew("la"),
		OutputLayer: "la",
	})
	r2, _ := p.Hash(ctx, hash.HashRequest{
		FilePath:    "/same",
		LayerStack:  layer.MustNew("lb"),
		OutputLayer: "lb",
	})
	if string(r1.Hash) == string(r2.Hash) {
		t.Fatal("same path in different output layers must produce different hashes")
	}
}

func TestSyntheticProvider_EmptyPath(t *testing.T) {
	p := hash.NewSyntheticProvider()
	_, err := p.Hash(context.Background(), hash.HashRequest{
		FilePath:    "",
		LayerStack:  baseStack,
		OutputLayer: "base",
	})
	if err == nil {
		t.Fatal("expected error for empty FilePath")
	}
}

func TestSyntheticProvider_Algorithm(t *testing.T) {
	p := hash.NewSyntheticProvider()
	if got := p.Algorithm(); got != hash.AlgorithmSHA256 {
		t.Fatalf("expected sha256, got %s", got)
	}
}

func TestSyntheticProvider_HexFilled(t *testing.T) {
	p := hash.NewSyntheticProvider()
	r, _ := p.Hash(context.Background(), req("/f"))
	if r.Hex == "" {
		t.Fatal("HashResult.Hex must be non-empty")
	}
	if len(r.Hash) != 32 {
		t.Fatalf("SHA256 hash must be 32 bytes, got %d", len(r.Hash))
	}
}

// ─── SHA256Provider ──────────────────────────────────────────────────────────

func TestSHA256Provider_Basic(t *testing.T) {
	p := hash.NewSHA256Provider(func(_ context.Context, _ string, _ layer.Stack) ([]byte, error) {
		return []byte("hello world"), nil
	})
	r, err := p.Hash(context.Background(), req("/f"))
	if err != nil {
		t.Fatalf("Hash: %v", err)
	}
	if len(r.Hash) != 32 {
		t.Fatalf("expected 32-byte hash, got %d", len(r.Hash))
	}
	if r.Hex == "" {
		t.Fatal("Hex must be non-empty")
	}
}

func TestSHA256Provider_Deterministic(t *testing.T) {
	content := []byte("constant content")
	p := hash.NewSHA256Provider(func(_ context.Context, _ string, _ layer.Stack) ([]byte, error) {
		return content, nil
	})
	ctx := context.Background()
	r1, _ := p.Hash(ctx, req("/f"))
	r2, _ := p.Hash(ctx, req("/f"))
	if string(r1.Hash) != string(r2.Hash) {
		t.Fatal("SHA256Provider must be deterministic for same content")
	}
}

func TestSHA256Provider_EmptyPath(t *testing.T) {
	p := hash.NewSHA256Provider(func(_ context.Context, _ string, _ layer.Stack) ([]byte, error) {
		return nil, nil
	})
	_, err := p.Hash(context.Background(), hash.HashRequest{
		FilePath:    "",
		LayerStack:  baseStack,
		OutputLayer: "base",
	})
	if err == nil {
		t.Fatal("expected error for empty FilePath")
	}
}

func TestSHA256Provider_NilPanics(t *testing.T) {
	defer func() {
		if r := recover(); r == nil {
			t.Fatal("NewSHA256Provider(nil) must panic")
		}
	}()
	hash.NewSHA256Provider(nil)
}

// ─── HashResult ───────────────────────────────────────────────────────────────

func TestHashResultEqual(t *testing.T) {
	p := hash.NewSyntheticProvider()
	r1, _ := p.Hash(context.Background(), req("/f"))
	r2, _ := p.Hash(context.Background(), req("/f"))
	if !r1.Equal(r2) {
		t.Fatal("identical hashes must be Equal")
	}
	r3, _ := p.Hash(context.Background(), req("/g"))
	if r1.Equal(r3) {
		t.Fatal("different hashes must not be Equal")
	}
}
