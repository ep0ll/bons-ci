package hook_test

import (
	"context"
	"errors"
	"testing"

	"github.com/user/layermerkle/hook"
)

func makeEvent(t hook.HookType) hook.HookEvent {
	return hook.HookEvent{Type: t}
}

// ─── HookChain strict mode ────────────────────────────────────────────────────

func TestHookChain_AllFire(t *testing.T) {
	var fired []string
	mkHook := func(name string) hook.Hook {
		return hook.HookFunc(func(_ context.Context, _ hook.HookEvent) error {
			fired = append(fired, name)
			return nil
		})
	}
	hc := hook.NewHookChain(false, mkHook("a"), mkHook("b"), mkHook("c"))
	if err := hc.Fire(context.Background(), makeEvent(hook.HookCacheHit)); err != nil {
		t.Fatalf("Fire: %v", err)
	}
	if len(fired) != 3 {
		t.Fatalf("expected 3 hooks fired, got %d: %v", len(fired), fired)
	}
	if fired[0] != "a" || fired[1] != "b" || fired[2] != "c" {
		t.Fatalf("wrong order: %v", fired)
	}
}

func TestHookChain_Strict_StopsOnError(t *testing.T) {
	sentinel := errors.New("boom")
	var count int
	hc := hook.NewHookChain(false,
		hook.HookFunc(func(_ context.Context, _ hook.HookEvent) error { count++; return sentinel }),
		hook.HookFunc(func(_ context.Context, _ hook.HookEvent) error { count++; return nil }),
	)
	err := hc.Fire(context.Background(), makeEvent(hook.HookCacheHit))
	if !errors.Is(err, sentinel) {
		t.Fatalf("expected sentinel error, got %v", err)
	}
	if count != 1 {
		t.Fatalf("strict mode: second hook must not fire, count=%d", count)
	}
}

func TestHookChain_Lenient_AllFire(t *testing.T) {
	var count int
	hc := hook.NewHookChain(true,
		hook.HookFunc(func(_ context.Context, _ hook.HookEvent) error { count++; return errors.New("err1") }),
		hook.HookFunc(func(_ context.Context, _ hook.HookEvent) error { count++; return nil }),
	)
	_ = hc.Fire(context.Background(), makeEvent(hook.HookCacheHit))
	if count != 2 {
		t.Fatalf("lenient mode: both hooks must fire, count=%d", count)
	}
}

func TestHookChain_Add(t *testing.T) {
	hc := hook.NewHookChain(false)
	if hc.Len() != 0 {
		t.Fatal("expected empty chain")
	}
	hc.Add(hook.NoopHook{})
	if hc.Len() != 1 {
		t.Fatal("expected 1 hook after Add")
	}
}

// ─── TypedHook ───────────────────────────────────────────────────────────────

func TestTypedHook_FiltersCorrectly(t *testing.T) {
	var fired int
	inner := hook.HookFunc(func(_ context.Context, _ hook.HookEvent) error { fired++; return nil })
	th := hook.NewTypedHook(inner, hook.HookCacheHit, hook.HookTombstone)

	th.OnHook(context.Background(), makeEvent(hook.HookCacheHit))   // should fire
	th.OnHook(context.Background(), makeEvent(hook.HookTombstone))  // should fire
	th.OnHook(context.Background(), makeEvent(hook.HookHashComputed)) // must not fire

	if fired != 2 {
		t.Fatalf("TypedHook: expected 2 invocations, got %d", fired)
	}
}

// ─── RecordingHook ────────────────────────────────────────────────────────────

func TestRecordingHook_RecordsAll(t *testing.T) {
	rec := hook.NewRecordingHook()
	ctx := context.Background()

	types := []hook.HookType{hook.HookCacheHit, hook.HookHashComputed, hook.HookTombstone}
	for _, ht := range types {
		rec.OnHook(ctx, makeEvent(ht))
	}

	events := rec.Events()
	if len(events) != 3 {
		t.Fatalf("expected 3 events, got %d", len(events))
	}
}

func TestRecordingHook_CountByType(t *testing.T) {
	rec := hook.NewRecordingHook()
	ctx := context.Background()

	for i := 0; i < 5; i++ {
		rec.OnHook(ctx, makeEvent(hook.HookCacheHit))
	}
	rec.OnHook(ctx, makeEvent(hook.HookTombstone))

	if n := rec.CountByType(hook.HookCacheHit); n != 5 {
		t.Fatalf("expected 5 cache hits, got %d", n)
	}
	if n := rec.CountByType(hook.HookTombstone); n != 1 {
		t.Fatalf("expected 1 tombstone, got %d", n)
	}
}

func TestRecordingHook_Reset(t *testing.T) {
	rec := hook.NewRecordingHook()
	ctx := context.Background()
	rec.OnHook(ctx, makeEvent(hook.HookCacheHit))
	rec.Reset()
	if len(rec.Events()) != 0 {
		t.Fatal("Reset must clear all events")
	}
}

// ─── HookType String ─────────────────────────────────────────────────────────

func TestHookTypeString(t *testing.T) {
	cases := map[hook.HookType]string{
		hook.HookCacheHit:          "cache_hit",
		hook.HookHashComputed:      "hash_computed",
		hook.HookTombstone:         "tombstone",
		hook.HookLayerSealed:       "layer_sealed",
		hook.HookPipelineStarted:   "pipeline_started",
		hook.HookPipelineStopped:   "pipeline_stopped",
		hook.HookMerkleLeafAdded:   "merkle_leaf_added",
		hook.HookError:             "error",
	}
	for ht, want := range cases {
		if got := ht.String(); got != want {
			t.Errorf("HookType(%d).String() = %q, want %q", ht, got, want)
		}
	}
}
