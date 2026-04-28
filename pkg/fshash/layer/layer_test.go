package layer_test

import (
	"context"
	"testing"

	"github.com/bons/bons-ci/pkg/fshash/internal/core"
	"github.com/bons/bons-ci/pkg/fshash/layer"
)

func TestStoreRegisterAndExists(t *testing.T) {
	ctx := context.Background()
	store := layer.NewMemoryStore()

	l := core.NewLayerID("sha256:abc")
	if err := store.Register(ctx, l, core.LayerID{}); err != nil {
		t.Fatalf("Register: %v", err)
	}

	if !store.Exists(l) {
		t.Error("expected layer to exist")
	}

	// Duplicate registration.
	err := store.Register(ctx, l, core.LayerID{})
	if err == nil {
		t.Error("expected error for duplicate registration")
	}
}

func TestStoreParentValidation(t *testing.T) {
	ctx := context.Background()
	store := layer.NewMemoryStore()

	child := core.NewLayerID("sha256:child")
	nonexistent := core.NewLayerID("sha256:ghost")

	err := store.Register(ctx, child, nonexistent)
	if err == nil {
		t.Error("expected error when parent doesn't exist")
	}
}

func TestStoreModifiedPaths(t *testing.T) {
	ctx := context.Background()
	store := layer.NewMemoryStore()

	l := core.NewLayerID("sha256:mod")
	store.Register(ctx, l, core.LayerID{})

	store.MarkModified(l, "/etc/hosts")
	store.MarkModified(l, "/etc/resolv.conf")

	if !store.IsModified(l, "/etc/hosts") {
		t.Error("expected /etc/hosts to be modified")
	}
	if store.IsModified(l, "/etc/passwd") {
		t.Error("expected /etc/passwd to NOT be modified")
	}

	paths := store.ModifiedPaths(l)
	if len(paths) != 2 {
		t.Errorf("ModifiedPaths = %d, want 2", len(paths))
	}
}

func TestStoreOwnerOf(t *testing.T) {
	ctx := context.Background()
	store := layer.NewMemoryStore()

	base := core.NewLayerID("sha256:base")
	mid := core.NewLayerID("sha256:mid")
	top := core.NewLayerID("sha256:top")

	store.Register(ctx, base, core.LayerID{})
	store.Register(ctx, mid, base)
	store.Register(ctx, top, mid)

	// /etc/hosts modified in mid layer.
	store.MarkModified(mid, "/etc/hosts")

	chain := []core.LayerID{base, mid, top}

	owner, explicit := store.OwnerOf(chain, "/etc/hosts")
	if !explicit || owner != mid {
		t.Errorf("OwnerOf(/etc/hosts) = %s (explicit=%v), want mid", owner, explicit)
	}

	// /etc/passwd not modified anywhere — defaults to base.
	owner, explicit = store.OwnerOf(chain, "/etc/passwd")
	if explicit {
		t.Error("expected non-explicit ownership for unmodified file")
	}
	if owner != base {
		t.Errorf("OwnerOf(/etc/passwd) = %s, want base", owner)
	}
}

func TestChainOperations(t *testing.T) {
	l1 := core.NewLayerID("sha256:1")
	l2 := core.NewLayerID("sha256:2")
	l3 := core.NewLayerID("sha256:3")

	chain := layer.NewChain([]core.LayerID{l1, l2, l3})

	if chain.Depth() != 3 {
		t.Errorf("Depth = %d, want 3", chain.Depth())
	}

	if chain.Bottom() != l1 {
		t.Errorf("Bottom = %s, want %s", chain.Bottom(), l1)
	}

	if chain.Top() != l3 {
		t.Errorf("Top = %s, want %s", chain.Top(), l3)
	}

	if !chain.Contains(l2) {
		t.Error("expected chain to contain l2")
	}

	if chain.Position(l2) != 1 {
		t.Errorf("Position(l2) = %d, want 1", chain.Position(l2))
	}

	above := chain.Above(l1)
	if len(above) != 2 {
		t.Errorf("Above(l1) = %d items, want 2", len(above))
	}

	// Freeze produces a deep copy.
	frozen := chain.Freeze()
	if frozen.Depth() != 3 {
		t.Error("frozen chain depth mismatch")
	}
}

func TestResolverNeedsRehash(t *testing.T) {
	ctx := context.Background()
	store := layer.NewMemoryStore()

	base := core.NewLayerID("sha256:base")
	upper := core.NewLayerID("sha256:upper")

	store.Register(ctx, base, core.LayerID{})
	store.Register(ctx, upper, base)

	resolver := layer.NewResolver(store)
	chain := layer.NewChain([]core.LayerID{base, upper})

	// File not modified in upper → no rehash needed.
	if resolver.NeedsRehash(chain, "/foo", base) {
		t.Error("expected no rehash for unmodified file")
	}

	// Mark modified in upper.
	store.MarkModified(upper, "/foo")
	if !resolver.NeedsRehash(chain, "/foo", base) {
		t.Error("expected rehash for modified file")
	}

	// Cached layer not in chain → always rehash.
	unknown := core.NewLayerID("sha256:unknown")
	if !resolver.NeedsRehash(chain, "/bar", unknown) {
		t.Error("expected rehash when cached layer is not in chain")
	}
}

func TestChainBuilder(t *testing.T) {
	builder := layer.NewChainBuilder()

	builder.Push(core.NewLayerID("sha256:a"))
	builder.Push(core.NewLayerID("sha256:b"))

	if builder.Depth() != 2 {
		t.Errorf("Depth = %d, want 2", builder.Depth())
	}

	chain := builder.Build()
	if chain.Depth() != 2 {
		t.Errorf("built chain Depth = %d, want 2", chain.Depth())
	}
}
