package llbx_test

import (
	"context"
	"sync"
	"sync/atomic"
	"testing"

	"github.com/bons/bons-ci/client/llb/builder"
	"github.com/bons/bons-ci/client/llb/core"
	"github.com/bons/bons-ci/client/llb/graph"
	fileop "github.com/bons/bons-ci/client/llb/ops/file"
	"github.com/bons/bons-ci/client/llb/ops/source/git"
	"github.com/bons/bons-ci/client/llb/ops/source/http"
	"github.com/bons/bons-ci/client/llb/ops/source/image"
	"github.com/bons/bons-ci/client/llb/ops/source/local"
	"github.com/bons/bons-ci/client/llb/reactive"
	"github.com/bons/bons-ci/client/llb/state"
)

// ─── Source op construction ───────────────────────────────────────────────────

func TestImageVertex_BasicConstruction(t *testing.T) {
	t.Parallel()
	v, err := image.New(image.WithRef("alpine:3.20"))
	if err != nil {
		t.Fatalf("image.New: %v", err)
	}
	if got := v.NormalisedRef(); got != "docker.io/library/alpine:3.20" {
		t.Errorf("NormalisedRef = %q, want docker.io/library/alpine:3.20", got)
	}
	if v.Type() != core.VertexTypeSource {
		t.Errorf("Type = %v, want source", v.Type())
	}
	if len(v.Inputs()) != 0 {
		t.Errorf("image vertex must have 0 inputs, got %d", len(v.Inputs()))
	}
	if len(v.Outputs()) != 1 {
		t.Errorf("image vertex must have 1 output, got %d", len(v.Outputs()))
	}
}

func TestImageVertex_RequiresRef(t *testing.T) {
	t.Parallel()
	_, err := image.New()
	if err == nil {
		t.Fatal("expected error when Ref is empty")
	}
}

func TestImageVertex_Marshal(t *testing.T) {
	t.Parallel()
	ctx := context.Background()
	v, _ := image.New(image.WithRef("busybox:latest"))
	c := core.DefaultConstraints()
	mv, err := v.Marshal(ctx, c)
	if err != nil {
		t.Fatalf("Marshal: %v", err)
	}
	if mv.Digest == "" {
		t.Error("Marshal returned empty digest")
	}
	if len(mv.Bytes) == 0 {
		t.Error("Marshal returned empty bytes")
	}
	// Second call should be served from cache (same pointer).
	mv2, err := v.Marshal(ctx, c)
	if err != nil {
		t.Fatalf("second Marshal: %v", err)
	}
	if mv.Digest != mv2.Digest {
		t.Error("cached Marshal returned different digest")
	}
}

func TestImageVertex_WithOption(t *testing.T) {
	t.Parallel()
	ctx := context.Background()
	v1, _ := image.New(image.WithRef("alpine:3.20"))
	v2, err := v1.WithOption(image.WithResolveMode(image.ResolveModePreferLocal))
	if err != nil {
		t.Fatalf("WithOption: %v", err)
	}
	c := core.DefaultConstraints()
	mv1, _ := v1.Marshal(ctx, c)
	mv2, _ := v2.Marshal(ctx, c)
	if mv1.Digest == mv2.Digest {
		t.Error("WithOption produced identical digest – mutation not reflected")
	}
}

func TestGitVertex_BasicConstruction(t *testing.T) {
	t.Parallel()
	v, err := git.New(
		git.WithRemote("https://github.com/moby/buildkit.git"),
		git.WithRef(git.TagRef("v0.15.0")),
	)
	if err != nil {
		t.Fatalf("git.New: %v", err)
	}
	if v.Type() != core.VertexTypeSource {
		t.Errorf("Type = %v, want source", v.Type())
	}
	if v.Ref().String() != "v0.15.0" {
		t.Errorf("Ref = %q, want v0.15.0", v.Ref())
	}
}

func TestGitVertex_RequiresRemote(t *testing.T) {
	t.Parallel()
	_, err := git.New()
	if err == nil {
		t.Fatal("expected error when Remote is empty")
	}
}

func TestGitVertex_RefTypes(t *testing.T) {
	t.Parallel()
	branch := git.BranchRef("main")
	tag := git.TagRef("v1.0.0")
	commit := git.CommitRef("abc123")

	if branch.String() != "main" {
		t.Errorf("BranchRef.String = %q", branch)
	}
	if tag.String() != "v1.0.0" {
		t.Errorf("TagRef.String = %q", tag)
	}
	if commit.String() != "abc123" {
		t.Errorf("CommitRef.String = %q", commit)
	}
}

func TestHTTPVertex_Marshal(t *testing.T) {
	t.Parallel()
	ctx := context.Background()
	v, err := http.New(http.WithURL("https://example.com/file.tar.gz"))
	if err != nil {
		t.Fatalf("http.New: %v", err)
	}
	mv, err := v.Marshal(ctx, core.DefaultConstraints())
	if err != nil {
		t.Fatalf("Marshal: %v", err)
	}
	if mv.Digest == "" {
		t.Error("empty digest")
	}
}

func TestHTTPVertex_RequiresURL(t *testing.T) {
	t.Parallel()
	_, err := http.New()
	if err == nil {
		t.Fatal("expected error when URL is empty")
	}
}

func TestLocalVertex_Marshal(t *testing.T) {
	t.Parallel()
	ctx := context.Background()
	v, err := local.New(local.WithName("context"))
	if err != nil {
		t.Fatalf("local.New: %v", err)
	}
	mv, err := v.Marshal(ctx, core.DefaultConstraints())
	if err != nil {
		t.Fatalf("Marshal: %v", err)
	}
	if mv.Digest == "" {
		t.Error("empty digest")
	}
}

// ─── File op ──────────────────────────────────────────────────────────────────

func TestFileOp_Mkdir(t *testing.T) {
	t.Parallel()
	ctx := context.Background()

	base, _ := image.New(image.WithRef("alpine:3.20"))
	fv, err := fileop.New(
		fileop.OnState(base.Output()),
		fileop.Do(fileop.Mkdir("/foo", 0755, fileop.WithMkdirParents(true))),
	)
	if err != nil {
		t.Fatalf("file.New: %v", err)
	}

	mv, err := fv.Marshal(ctx, core.DefaultConstraints())
	if err != nil {
		t.Fatalf("Marshal: %v", err)
	}
	if mv.Digest == "" {
		t.Error("empty digest")
	}
}

func TestFileOp_RequiresActions(t *testing.T) {
	t.Parallel()
	_, err := fileop.New()
	if err == nil {
		t.Fatal("expected error when no actions provided")
	}
}

func TestFileOp_MkdirMkfile(t *testing.T) {
	t.Parallel()
	ctx := context.Background()

	fv, err := fileop.New(
		fileop.Do(
			fileop.Mkdir("/out", 0755),
			fileop.Mkfile("/out/hello.txt", 0644, []byte("hello")),
		),
	)
	if err != nil {
		t.Fatalf("file.New: %v", err)
	}

	mv, err := fv.Marshal(ctx, core.DefaultConstraints())
	if err != nil {
		t.Fatalf("Marshal: %v", err)
	}
	if mv.Digest == "" {
		t.Error("empty digest")
	}
}

// ─── State / Builder integration ─────────────────────────────────────────────

func TestBuilder_ImageSerialization(t *testing.T) {
	t.Parallel()
	ctx := context.Background()

	b := builder.New()
	alpine := b.Image("alpine:3.20")
	if alpine.IsScratch() {
		t.Fatal("Image() returned scratch unexpectedly")
	}

	def, err := b.Serialize(ctx, alpine)
	if err != nil {
		t.Fatalf("Serialize: %v", err)
	}
	if len(def.Def) == 0 {
		t.Error("definition has no ops")
	}
}

func TestBuilder_ScratchSerialization(t *testing.T) {
	t.Parallel()
	ctx := context.Background()

	b := builder.New()
	def, err := b.Serialize(ctx, b.Scratch())
	if err != nil {
		t.Fatalf("Serialize scratch: %v", err)
	}
	// Scratch produces an empty definition.
	if len(def.Def) != 0 {
		t.Errorf("scratch definition should be empty, got %d ops", len(def.Def))
	}
}

func TestStateMerge(t *testing.T) {
	t.Parallel()
	ctx := context.Background()
	b := builder.New()

	a := b.Image("alpine:3.20")
	bb := b.Image("busybox:latest")

	merged := a.Merge(bb)
	if merged.IsScratch() {
		t.Fatal("Merge returned scratch unexpectedly")
	}

	def, err := b.Serialize(ctx, merged)
	if err != nil {
		t.Fatalf("Serialize merged: %v", err)
	}
	if len(def.Def) == 0 {
		t.Error("merged definition has no ops")
	}
}

func TestStateDiff(t *testing.T) {
	t.Parallel()
	ctx := context.Background()
	b := builder.New()

	base := b.Image("alpine:3.20")
	upper := b.Image("alpine:3.20")

	diffed := base.Diff(upper)
	def, err := b.Serialize(ctx, diffed)
	if err != nil {
		t.Fatalf("Serialize diff: %v", err)
	}
	if len(def.Def) == 0 {
		t.Error("diff definition has no ops")
	}
}

func TestStateScratchMerge(t *testing.T) {
	t.Parallel()
	// Merging scratch with scratch → scratch.
	merged := state.Scratch().Merge(state.Scratch())
	if !merged.IsScratch() {
		t.Error("scratch.Merge(scratch) should return scratch")
	}

	// Merging single non-scratch with scratch → that state.
	b := builder.New()
	alpine := b.Image("alpine:3.20")
	result := alpine.Merge(state.Scratch())
	// One non-scratch input → returns that input unchanged.
	if result.IsScratch() {
		t.Error("non-scratch.Merge(scratch) should not return scratch")
	}
}

// ─── Graph mutations ─────────────────────────────────────────────────────────

func TestGraphMutator_Replace(t *testing.T) {
	t.Parallel()
	ctx := context.Background()
	b := builder.New()
	c := core.DefaultConstraints()

	alpine, _ := image.New(image.WithRef("alpine:3.20"))
	g, err := graph.New(ctx, alpine, c)
	if err != nil {
		t.Fatalf("graph.New: %v", err)
	}

	busybox, _ := image.New(image.WithRef("busybox:latest"))
	alpineID := g.Roots()[0]

	mut := b.Mutator(g)
	ng, err := mut.Replace(ctx, alpineID, busybox, c)
	if err != nil {
		t.Fatalf("Replace: %v", err)
	}

	// New graph should have a different root (new digest).
	if ng.Size() == 0 {
		t.Error("graph should not be empty after Replace")
	}
	if ng.Roots()[0] == alpineID {
		t.Error("Replace should produce a new root digest")
	}
}

// ─── Reactive event bus ───────────────────────────────────────────────────────

func TestEventBus_PublishSubscribe(t *testing.T) {
	t.Parallel()
	bus := reactive.NewEventBus[string]()
	defer bus.Close()

	var received []string
	var mu sync.Mutex

	sub := bus.Subscribe(func(s string) {
		mu.Lock()
		received = append(received, s)
		mu.Unlock()
	})
	defer sub.Cancel()

	bus.Publish("hello")
	bus.Publish("world")

	// Give the goroutine time to drain.
	for i := 0; i < 100; i++ {
		mu.Lock()
		n := len(received)
		mu.Unlock()
		if n == 2 {
			break
		}
		// tiny busy-wait acceptable in tests
	}

	mu.Lock()
	defer mu.Unlock()
	if len(received) != 2 {
		t.Errorf("received %d events, want 2", len(received))
	}
}

func TestEventBus_Cancel(t *testing.T) {
	t.Parallel()
	bus := reactive.NewEventBus[int]()
	defer bus.Close()

	var count atomic.Int64
	sub := bus.Subscribe(func(n int) { count.Add(1) })

	bus.Publish(1)
	sub.Cancel()
	bus.Publish(2) // should not reach the subscriber

	// Allow drain.
	for i := 0; i < 100; i++ {
		if count.Load() >= 1 {
			break
		}
	}
	if count.Load() > 1 {
		t.Errorf("received %d events after Cancel, want ≤ 1", count.Load())
	}
}

func TestObservable_SetNotifies(t *testing.T) {
	t.Parallel()
	obs := reactive.NewObservable("initial")
	defer obs.Close()

	changes := make(chan reactive.ChangeEvent[string], 4)
	sub := obs.Subscribe(func(e reactive.ChangeEvent[string]) {
		changes <- e
	})
	defer sub.Cancel()

	obs.Set("updated")

	select {
	case e := <-changes:
		if e.Old != "initial" || e.New != "updated" {
			t.Errorf("ChangeEvent = {%q, %q}, want {initial, updated}", e.Old, e.New)
		}
	default:
		t.Error("no change event received")
	}
}

func TestObservable_NoEventOnSameValue(t *testing.T) {
	t.Parallel()
	obs := reactive.NewObservable(42)
	defer obs.Close()

	var count atomic.Int64
	sub := obs.Subscribe(func(_ reactive.ChangeEvent[int]) { count.Add(1) })
	defer sub.Cancel()

	obs.Set(42) // same value – should not fire
	if count.Load() != 0 {
		t.Errorf("got %d events for no-op Set, want 0", count.Load())
	}
}

// ─── Builder subscription events ─────────────────────────────────────────────

func TestBuilder_EmitsEventsOnGraphOperations(t *testing.T) {
	t.Parallel()
	ctx := context.Background()

	b := builder.New()
	var eventCount atomic.Int64
	sub := b.Subscribe(func(_ reactive.GraphEvent) { eventCount.Add(1) })
	defer sub.Cancel()

	_ = b.Image("alpine:3.20")
	_ = b.Local("ctx")

	c := core.DefaultConstraints()
	alpine, _ := image.New(image.WithRef("alpine:3.20"))
	g, _ := graph.New(ctx, alpine, c)
	busybox, _ := image.New(image.WithRef("busybox:latest"))
	alpineID := g.Roots()[0]
	_, _ = b.Mutator(g).Replace(ctx, alpineID, busybox, c)

	// Builder.Image x2 and Replace emit events.
	if eventCount.Load() < 2 {
		t.Errorf("expected ≥ 2 graph events, got %d", eventCount.Load())
	}
}

// ─── Determinism tests ────────────────────────────────────────────────────────

func TestMarshal_Determinism(t *testing.T) {
	t.Parallel()
	ctx := context.Background()
	c := core.DefaultConstraints()

	v, _ := image.New(image.WithRef("alpine:3.20"))
	var prev []byte
	for i := 0; i < 50; i++ {
		mv, err := v.Marshal(ctx, c)
		if err != nil {
			t.Fatalf("iter %d: %v", i, err)
		}
		if prev != nil && string(mv.Bytes) != string(prev) {
			t.Fatalf("iter %d: non-deterministic serialisation", i)
		}
		prev = mv.Bytes
	}
}

func TestMarshal_DifferentOptions_DifferentDigest(t *testing.T) {
	t.Parallel()
	ctx := context.Background()
	c := core.DefaultConstraints()

	v1, _ := image.New(image.WithRef("alpine:3.20"))
	v2, _ := image.New(image.WithRef("busybox:latest"))

	mv1, _ := v1.Marshal(ctx, c)
	mv2, _ := v2.Marshal(ctx, c)
	if mv1.Digest == mv2.Digest {
		t.Error("different images produced the same digest")
	}
}
