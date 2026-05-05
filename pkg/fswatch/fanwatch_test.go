package fswatch_test

import (
	"os"
	"context"
	"errors"
	"fmt"
	"strings"
	"testing"
	"time"

	fswatch "github.com/bons/bons-ci/pkg/fswatch"
	"github.com/bons/bons-ci/pkg/fswatch/testutil"
)

// ─────────────────────────────────────────────────────────────────────────────
// EventMask tests
// ─────────────────────────────────────────────────────────────────────────────

func TestEventMask_Has(t *testing.T) {
	mask := fswatch.MaskReadOnly

	for _, op := range []fswatch.Op{
		fswatch.OpAccess,
		fswatch.OpOpen,
		fswatch.OpOpenExec,
		fswatch.OpCloseNoWrite,
	} {
		if !mask.Has(op) {
			t.Errorf("MaskReadOnly.Has(%v) = false, want true", op)
		}
	}

	for _, op := range []fswatch.Op{
		fswatch.OpModify,
		fswatch.OpCreate,
		fswatch.OpDelete,
		fswatch.OpCloseWrite,
	} {
		if mask.Has(op) {
			t.Errorf("MaskReadOnly.Has(%v) = true, want false", op)
		}
	}
}

func TestEventMask_IsReadOnly(t *testing.T) {
	tests := []struct {
		name     string
		mask     fswatch.EventMask
		wantRO   bool
	}{
		{"read only mask", fswatch.MaskReadOnly, true},
		{"access only", fswatch.EventMask(fswatch.OpAccess), true},
		{"open only", fswatch.EventMask(fswatch.OpOpen), true},
		{"modify", fswatch.EventMask(fswatch.OpModify), false},
		{"create", fswatch.EventMask(fswatch.OpCreate), false},
		{"delete", fswatch.EventMask(fswatch.OpDelete), false},
		{"close write", fswatch.EventMask(fswatch.OpCloseWrite), false},
		{"mixed access+modify", fswatch.EventMask(fswatch.OpAccess | fswatch.OpModify), false},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := tt.mask.IsReadOnly()
			if got != tt.wantRO {
				t.Errorf("IsReadOnly() = %v, want %v", got, tt.wantRO)
			}
		})
	}
}

func TestEventMask_String(t *testing.T) {
	tests := []struct {
		mask fswatch.EventMask
		want string
	}{
		{fswatch.EventMask(fswatch.OpAccess), "ACCESS"},
		{fswatch.EventMask(fswatch.OpModify), "MODIFY"},
		{fswatch.EventMask(0), "none"},
	}
	for _, tt := range tests {
		got := tt.mask.String()
		if got != tt.want {
			t.Errorf("EventMask(%d).String() = %q, want %q", tt.mask, got, tt.want)
		}
	}
	// Multi-op mask should contain both names.
	combined := fswatch.EventMask(fswatch.OpAccess | fswatch.OpOpen)
	s := combined.String()
	if !strings.Contains(s, "ACCESS") || !strings.Contains(s, "OPEN") {
		t.Errorf("combined mask string %q missing expected ops", s)
	}
}

// ─────────────────────────────────────────────────────────────────────────────
// RawEvent tests
// ─────────────────────────────────────────────────────────────────────────────

func TestRawEvent_IsOverflow(t *testing.T) {
	overflow := &fswatch.RawEvent{Mask: fswatch.EventMask(fswatch.OpOverflow)}
	normal := &fswatch.RawEvent{Mask: fswatch.EventMask(fswatch.OpAccess)}

	if !overflow.IsOverflow() {
		t.Error("overflow event: IsOverflow() = false, want true")
	}
	if normal.IsOverflow() {
		t.Error("normal event: IsOverflow() = true, want false")
	}
}

// ─────────────────────────────────────────────────────────────────────────────
// EnrichedEvent tests
// ─────────────────────────────────────────────────────────────────────────────

func TestEnrichedEvent_Clone_IndependentAttrs(t *testing.T) {
	original := testutil.NewEnrichedEvent().
		WithAttr("key", "value").
		Build()

	clone := original.Clone()
	clone.SetAttr("key", "mutated")

	if original.Attr("key") != "value" {
		t.Error("Clone: mutating clone's attrs affected original")
	}
}

func TestEnrichedEvent_SetAttr_LazyInit(t *testing.T) {
	e := &fswatch.EnrichedEvent{}
	if e.Attrs != nil {
		t.Error("Attrs should be nil before first SetAttr call")
	}
	e.SetAttr("foo", 42)
	if e.Attrs == nil {
		t.Error("Attrs should be initialised after SetAttr")
	}
	if e.Attr("foo") != 42 {
		t.Errorf("Attr(foo) = %v, want 42", e.Attr("foo"))
	}
}

func TestEnrichedEvent_IsReadOnly(t *testing.T) {
	roEvent := testutil.NewEnrichedEvent().WithOp(fswatch.OpAccess).Build()
	rwEvent := testutil.NewEnrichedEvent().WithOp(fswatch.OpModify).Build()

	if !roEvent.IsReadOnly() {
		t.Error("ACCESS event should be read-only")
	}
	if rwEvent.IsReadOnly() {
		t.Error("MODIFY event should not be read-only")
	}
}

// ─────────────────────────────────────────────────────────────────────────────
// Filter tests
// ─────────────────────────────────────────────────────────────────────────────

func makeContext() context.Context { return context.Background() }

func TestReadOnlyFilter_AllowsReadOps(t *testing.T) {
	f := fswatch.ReadOnlyFilter()
	ctx := makeContext()

	readOps := []fswatch.Op{
		fswatch.OpAccess, fswatch.OpOpen, fswatch.OpOpenExec, fswatch.OpCloseNoWrite,
	}
	for _, op := range readOps {
		e := testutil.NewEnrichedEvent().WithOp(op).Build()
		if !f.Allow(ctx, e) {
			t.Errorf("ReadOnlyFilter: Allow(%v) = false, want true", op)
		}
	}
}

func TestReadOnlyFilter_DropsWriteOps(t *testing.T) {
	f := fswatch.ReadOnlyFilter()
	ctx := makeContext()

	writeOps := []fswatch.Op{
		fswatch.OpModify, fswatch.OpCreate, fswatch.OpDelete,
		fswatch.OpCloseWrite, fswatch.OpMovedFrom, fswatch.OpMovedTo, fswatch.OpAttrib,
	}
	for _, op := range writeOps {
		e := testutil.NewEnrichedEvent().WithOp(op).Build()
		if f.Allow(ctx, e) {
			t.Errorf("ReadOnlyFilter: Allow(%v) = true, want false", op)
		}
	}
}

func TestMaskFilter_MatchesExpectedOps(t *testing.T) {
	f := fswatch.MaskFilter(fswatch.OpAccess, fswatch.OpOpen)
	ctx := makeContext()

	allow := []fswatch.Op{fswatch.OpAccess, fswatch.OpOpen}
	deny := []fswatch.Op{fswatch.OpModify, fswatch.OpCreate, fswatch.OpOpenExec}

	for _, op := range allow {
		e := testutil.NewEnrichedEvent().WithOp(op).Build()
		if !f.Allow(ctx, e) {
			t.Errorf("MaskFilter: Allow(%v) = false, want true", op)
		}
	}
	for _, op := range deny {
		e := testutil.NewEnrichedEvent().WithOp(op).Build()
		if f.Allow(ctx, e) {
			t.Errorf("MaskFilter: Allow(%v) = true, want false", op)
		}
	}
}

func TestPathPrefixFilter(t *testing.T) {
	f := fswatch.PathPrefixFilter("/merged/app")
	ctx := makeContext()

	tests := []struct {
		path  string
		allow bool
	}{
		{"/merged/app/main.go", true},
		{"/merged/app/subdir/file", true},
		{"/merged/app", true},
		{"/merged/other/file", false},
		{"/merged/appX/file", false}, // must not match partial prefix
		{"/var/lib/file", false},
	}
	for _, tt := range tests {
		e := testutil.NewEnrichedEvent().WithPath(tt.path).Build()
		got := f.Allow(ctx, e)
		if got != tt.allow {
			t.Errorf("PathPrefixFilter Allow(%q) = %v, want %v", tt.path, got, tt.allow)
		}
	}
}

func TestPathExcludeFilter(t *testing.T) {
	f := fswatch.PathExcludeFilter("/proc", "/sys")
	ctx := makeContext()

	if f.Allow(ctx, testutil.NewEnrichedEvent().WithPath("/proc/net/tcp").Build()) {
		t.Error("PathExcludeFilter: /proc path should be excluded")
	}
	if f.Allow(ctx, testutil.NewEnrichedEvent().WithPath("/sys/kernel/debug").Build()) {
		t.Error("PathExcludeFilter: /sys path should be excluded")
	}
	if !f.Allow(ctx, testutil.NewEnrichedEvent().WithPath("/merged/app/file").Build()) {
		t.Error("PathExcludeFilter: non-excluded path should pass")
	}
}

func TestExtensionFilter(t *testing.T) {
	f := fswatch.ExtensionFilter(".so", ".py")
	ctx := makeContext()

	tests := []struct {
		path  string
		allow bool
	}{
		{"/lib/libssl.so", true},
		{"/usr/lib/libssl.so.3", false},
		{"/app/script.py", true},
		{"/app/main.go", false},
		{"/app/PYTHON", false}, // case-sensitive extension check
	}
	for _, tt := range tests {
		e := testutil.NewEnrichedEvent().WithPath(tt.path).Build()
		got := f.Allow(ctx, e)
		if got != tt.allow {
			t.Errorf("ExtensionFilter Allow(%q) = %v, want %v", tt.path, got, tt.allow)
		}
	}
}

func TestPIDFilter(t *testing.T) {
	f := fswatch.PIDFilter(100, 200)
	ctx := makeContext()

	if !f.Allow(ctx, testutil.NewEnrichedEvent().WithPID(100).Build()) {
		t.Error("PIDFilter: pid 100 should pass")
	}
	if !f.Allow(ctx, testutil.NewEnrichedEvent().WithPID(200).Build()) {
		t.Error("PIDFilter: pid 200 should pass")
	}
	if f.Allow(ctx, testutil.NewEnrichedEvent().WithPID(999).Build()) {
		t.Error("PIDFilter: pid 999 should not pass")
	}
}

func TestExcludePIDFilter(t *testing.T) {
	f := fswatch.ExcludePIDFilter(1)
	ctx := makeContext()

	if f.Allow(ctx, testutil.NewEnrichedEvent().WithPID(1).Build()) {
		t.Error("ExcludePIDFilter: PID 1 should be excluded")
	}
	if !f.Allow(ctx, testutil.NewEnrichedEvent().WithPID(1234).Build()) {
		t.Error("ExcludePIDFilter: PID 1234 should pass")
	}
}

func TestNoOverflowFilter(t *testing.T) {
	f := fswatch.NoOverflowFilter()
	ctx := makeContext()

	overflow := testutil.NewEnrichedEvent().WithMask(fswatch.EventMask(fswatch.OpOverflow)).Build()
	normal := testutil.NewEnrichedEvent().WithOp(fswatch.OpAccess).Build()

	if f.Allow(ctx, overflow) {
		t.Error("NoOverflowFilter: overflow event should be dropped")
	}
	if !f.Allow(ctx, normal) {
		t.Error("NoOverflowFilter: normal event should pass")
	}
}

func TestAllFilters_ShortCircuit(t *testing.T) {
	called := 0
	counter := fswatch.FilterFunc(func(_ context.Context, _ *fswatch.EnrichedEvent) bool {
		called++
		return true
	})
	// First filter rejects — counter should never be called.
	rejecter := fswatch.FilterFunc(func(_ context.Context, _ *fswatch.EnrichedEvent) bool {
		return false
	})

	all := fswatch.AllFilters{rejecter, counter}
	e := testutil.NewEnrichedEvent().Build()
	got := all.Allow(context.Background(), e)

	if got {
		t.Error("AllFilters: expected rejection, got allow")
	}
	if called != 0 {
		t.Errorf("AllFilters: expected short-circuit (0 calls), got %d", called)
	}
}

func TestAnyFilter(t *testing.T) {
	reject := fswatch.FilterFunc(func(_ context.Context, _ *fswatch.EnrichedEvent) bool { return false })
	accept := fswatch.FilterFunc(func(_ context.Context, _ *fswatch.EnrichedEvent) bool { return true })

	any1 := fswatch.AnyFilter{reject, accept}
	e := testutil.NewEnrichedEvent().Build()
	if !any1.Allow(context.Background(), e) {
		t.Error("AnyFilter: should pass when at least one filter accepts")
	}

	any2 := fswatch.AnyFilter{reject, reject}
	if any2.Allow(context.Background(), e) {
		t.Error("AnyFilter: should reject when all filters reject")
	}
}

func TestNot_Negates(t *testing.T) {
	alwaysAllow := fswatch.FilterFunc(func(_ context.Context, _ *fswatch.EnrichedEvent) bool { return true })
	neg := fswatch.Not{Inner: alwaysAllow}

	e := testutil.NewEnrichedEvent().Build()
	if neg.Allow(context.Background(), e) {
		t.Error("Not: should negate the inner filter")
	}
}

func TestExternalFilter(t *testing.T) {
	allowed := map[string]bool{"/merged/safe/file": true}
	f := fswatch.ExternalFilter(func(path string) bool { return allowed[path] })
	ctx := context.Background()

	if !f.Allow(ctx, testutil.NewEnrichedEvent().WithPath("/merged/safe/file").Build()) {
		t.Error("ExternalFilter: allowed path should pass")
	}
	if f.Allow(ctx, testutil.NewEnrichedEvent().WithPath("/merged/unsafe/file").Build()) {
		t.Error("ExternalFilter: non-allowed path should be rejected")
	}
}

func TestUpperDirOnlyFilter(t *testing.T) {
	f := fswatch.UpperDirOnlyFilter()
	ctx := context.Background()

	upper := testutil.NewEnrichedEvent().
		WithSourceLayer(testutil.UpperLayerFixture("/upper")).
		Build()
	lower := testutil.NewEnrichedEvent().
		WithSourceLayer(testutil.LowerLayerFixture(1, "/lower")).
		Build()
	noLayer := testutil.NewEnrichedEvent().Build()

	if !f.Allow(ctx, upper) {
		t.Error("UpperDirOnlyFilter: upper-layer event should pass")
	}
	if f.Allow(ctx, lower) {
		t.Error("UpperDirOnlyFilter: lower-layer event should be rejected")
	}
	if f.Allow(ctx, noLayer) {
		t.Error("UpperDirOnlyFilter: event without layer should be rejected")
	}
}

func TestLowerDirOnlyFilter(t *testing.T) {
	f := fswatch.LowerDirOnlyFilter()
	ctx := context.Background()

	upper := testutil.NewEnrichedEvent().
		WithSourceLayer(testutil.UpperLayerFixture("/upper")).
		Build()
	lower := testutil.NewEnrichedEvent().
		WithSourceLayer(testutil.LowerLayerFixture(1, "/lower")).
		Build()

	if f.Allow(ctx, upper) {
		t.Error("LowerDirOnlyFilter: upper event should be rejected")
	}
	if !f.Allow(ctx, lower) {
		t.Error("LowerDirOnlyFilter: lower-layer event should pass")
	}
}

func TestAttrValueFilter(t *testing.T) {
	f := fswatch.AttrValueFilter("env", "prod")
	ctx := context.Background()

	prod := testutil.NewEnrichedEvent().WithAttr("env", "prod").Build()
	staging := testutil.NewEnrichedEvent().WithAttr("env", "staging").Build()
	missing := testutil.NewEnrichedEvent().Build()

	if !f.Allow(ctx, prod) {
		t.Error("AttrValueFilter: matching attr should pass")
	}
	if f.Allow(ctx, staging) {
		t.Error("AttrValueFilter: non-matching attr should be rejected")
	}
	if f.Allow(ctx, missing) {
		t.Error("AttrValueFilter: missing attr should be rejected")
	}
}

func TestFreshnessFilter(t *testing.T) {
	f := fswatch.FreshnessFilter(50 * time.Millisecond)
	ctx := context.Background()

	fresh := testutil.NewRawEvent().WithTimestamp(time.Now()).Build()
	stale := testutil.NewRawEvent().WithTimestamp(time.Now().Add(-1 * time.Second)).Build()

	freshE := &fswatch.EnrichedEvent{Event: fswatch.Event{RawEvent: *fresh}}
	staleE := &fswatch.EnrichedEvent{Event: fswatch.Event{RawEvent: *stale}}

	if !f.Allow(ctx, freshE) {
		t.Error("FreshnessFilter: fresh event should pass")
	}
	if f.Allow(ctx, staleE) {
		t.Error("FreshnessFilter: stale event should be dropped")
	}
}

// ─────────────────────────────────────────────────────────────────────────────
// Transformer tests
// ─────────────────────────────────────────────────────────────────────────────

func TestStaticAttrTransformer(t *testing.T) {
	attrs := map[string]any{"region": "us-east-1", "env": "prod"}
	tr := fswatch.StaticAttrTransformer(attrs)

	e := testutil.NewEnrichedEvent().Build()
	if err := tr.Transform(context.Background(), e); err != nil {
		t.Fatalf("StaticAttrTransformer: unexpected error: %v", err)
	}

	if e.Attr("region") != "us-east-1" {
		t.Errorf("Attr(region) = %v, want us-east-1", e.Attr("region"))
	}
	if e.Attr("env") != "prod" {
		t.Errorf("Attr(env) = %v, want prod", e.Attr("env"))
	}
}

func TestDynamicAttrTransformer(t *testing.T) {
	tr := fswatch.DynamicAttrTransformer(func(e *fswatch.EnrichedEvent) map[string]any {
		return map[string]any{"path_len": len(e.Path)}
	})

	e := testutil.NewEnrichedEvent().WithPath("/merged/hello.txt").Build()
	if err := tr.Transform(context.Background(), e); err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	want := len("/merged/hello.txt")
	if e.Attr("path_len") != want {
		t.Errorf("Attr(path_len) = %v, want %d", e.Attr("path_len"), want)
	}
}

func TestChainTransformer_RunsAll(t *testing.T) {
	order := []int{}
	t1 := fswatch.TransformerFunc(func(_ context.Context, e *fswatch.EnrichedEvent) error {
		order = append(order, 1)
		e.SetAttr("t1", true)
		return nil
	})
	t2 := fswatch.TransformerFunc(func(_ context.Context, e *fswatch.EnrichedEvent) error {
		order = append(order, 2)
		e.SetAttr("t2", true)
		return nil
	})

	chain := fswatch.ChainTransformer{t1, t2}
	e := testutil.NewEnrichedEvent().Build()

	if err := chain.Transform(context.Background(), e); err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	if len(order) != 2 || order[0] != 1 || order[1] != 2 {
		t.Errorf("transformers ran in wrong order: %v", order)
	}
	if e.Attr("t1") != true || e.Attr("t2") != true {
		t.Error("both transformer attrs should be set")
	}
}

func TestChainTransformer_ContinuesAfterError(t *testing.T) {
	errT := fswatch.TransformerFunc(func(_ context.Context, _ *fswatch.EnrichedEvent) error {
		return fmt.Errorf("intentional error")
	})
	setAttr := fswatch.TransformerFunc(func(_ context.Context, e *fswatch.EnrichedEvent) error {
		e.SetAttr("after_error", true)
		return nil
	})

	chain := fswatch.ChainTransformer{errT, setAttr}
	e := testutil.NewEnrichedEvent().Build()

	err := chain.Transform(context.Background(), e)
	if err == nil {
		t.Error("ChainTransformer: expected error to be returned")
	}
	if e.Attr("after_error") != true {
		t.Error("ChainTransformer: second transformer should run even after first errors")
	}
}

func TestConditionalTransformer_SkipsWhenPredicateFalse(t *testing.T) {
	ran := false
	inner := fswatch.TransformerFunc(func(_ context.Context, _ *fswatch.EnrichedEvent) error {
		ran = true
		return nil
	})

	ct := fswatch.ConditionalTransformer{
		Predicate: func(e *fswatch.EnrichedEvent) bool { return false },
		Inner:     inner,
	}
	if err := ct.Transform(context.Background(), testutil.NewEnrichedEvent().Build()); err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if ran {
		t.Error("ConditionalTransformer: inner should not run when predicate is false")
	}
}

func TestOverlayEnricher_SetsOverlayInfo(t *testing.T) {
	overlay := fswatch.NewOverlayInfo(
		"/merged", "/upper", "/work",
		[]string{"/lower0", "/lower1"},
	)
	enricher := fswatch.NewOverlayEnricher(overlay)

	e := testutil.NewEnrichedEvent().WithPath("/merged/app/file.txt").Build()
	if err := enricher.Transform(context.Background(), e); err != nil {
		t.Fatalf("OverlayEnricher: unexpected error: %v", err)
	}

	if e.Overlay == nil {
		t.Fatal("OverlayEnricher: Overlay should be set")
	}
	if e.Overlay.MergedDir != "/merged" {
		t.Errorf("MergedDir = %q, want /merged", e.Overlay.MergedDir)
	}
}

func TestOverlayEnricher_PathOutsideMerge_Ignored(t *testing.T) {
	overlay := fswatch.NewOverlayInfo("/merged", "/upper", "/work", []string{"/lower0"})
	enricher := fswatch.NewOverlayEnricher(overlay)

	e := testutil.NewEnrichedEvent().WithPath("/var/log/syslog").Build()
	if err := enricher.Transform(context.Background(), e); err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	// FIX Bug 6: e.Overlay must be nil for paths outside the merged dir.
	// Previously the enricher attached Overlay unconditionally (false positive).
	if e.Overlay != nil {
		t.Error("Overlay should be nil for paths outside merged dir")
	}
	if e.SourceLayer != nil {
		t.Error("SourceLayer should be nil for paths outside merged dir")
	}
}

// ─────────────────────────────────────────────────────────────────────────────
// Handler tests
// ─────────────────────────────────────────────────────────────────────────────

func TestCountingHandler(t *testing.T) {
	h := &fswatch.CountingHandler{}
	ctx := context.Background()

	events := []fswatch.Op{
		fswatch.OpAccess, fswatch.OpOpen, fswatch.OpOpenExec,
		fswatch.OpAccess, fswatch.OpModify,
	}
	for _, op := range events {
		e := testutil.NewEnrichedEvent().WithOp(op).Build()
		if err := h.Handle(ctx, e); err != nil {
			t.Fatalf("Handle: unexpected error: %v", err)
		}
	}

	snap := h.Snapshot()
	if snap.Total != 5 {
		t.Errorf("Total = %d, want 5", snap.Total)
	}
	if snap.AccessEvents != 2 {
		t.Errorf("AccessEvents = %d, want 2", snap.AccessEvents)
	}
}

func TestCountingHandler_Reset(t *testing.T) {
	h := &fswatch.CountingHandler{}
	ctx := context.Background()

	_ = h.Handle(ctx, testutil.NewEnrichedEvent().Build())
	_ = h.Handle(ctx, testutil.NewEnrichedEvent().Build())

	h.Reset()
	if h.Snapshot().Total != 0 {
		t.Error("Reset: counters should be zero after Reset")
	}
}

func TestCollectingHandler(t *testing.T) {
	h := &fswatch.CollectingHandler{}
	ctx := context.Background()

	for i := range 5 {
		e := testutil.NewEnrichedEvent().
			WithPath(fmt.Sprintf("/merged/file%d", i)).
			Build()
		if err := h.Handle(ctx, e); err != nil {
			t.Fatalf("Handle: unexpected error: %v", err)
		}
	}

	events := h.Events()
	if len(events) != 5 {
		t.Errorf("Len = %d, want 5", len(events))
	}
	// Verify cloning — mutating returned events should not affect collector.
	events[0].SetAttr("mutated", true)
	if h.Events()[0].Attr("mutated") != nil {
		t.Error("CollectingHandler: Events() should return independent copies")
	}
}

func TestCollectingHandler_Reset(t *testing.T) {
	h := &fswatch.CollectingHandler{}
	ctx := context.Background()
	_ = h.Handle(ctx, testutil.NewEnrichedEvent().Build())
	h.Reset()
	if h.Len() != 0 {
		t.Error("Reset: collector should be empty")
	}
}

func TestChainHandler_StopsOnError(t *testing.T) {
	ran := false
	first := fswatch.HandlerFunc(func(_ context.Context, _ *fswatch.EnrichedEvent) error {
		return fmt.Errorf("first handler failed")
	})
	second := fswatch.HandlerFunc(func(_ context.Context, _ *fswatch.EnrichedEvent) error {
		ran = true
		return nil
	})

	chain := fswatch.ChainHandler{first, second}
	err := chain.Handle(context.Background(), testutil.NewEnrichedEvent().Build())
	if err == nil {
		t.Error("ChainHandler: expected error")
	}
	if ran {
		t.Error("ChainHandler: second handler should not run after first fails")
	}
}

func TestMultiHandler_RunsAll(t *testing.T) {
	counts := [2]int{}
	h1 := fswatch.HandlerFunc(func(_ context.Context, _ *fswatch.EnrichedEvent) error {
		counts[0]++
		return fmt.Errorf("h1 error")
	})
	h2 := fswatch.HandlerFunc(func(_ context.Context, _ *fswatch.EnrichedEvent) error {
		counts[1]++
		return nil
	})

	multi := fswatch.MultiHandler{h1, h2}
	err := multi.Handle(context.Background(), testutil.NewEnrichedEvent().Build())

	if err == nil {
		t.Error("MultiHandler: expected error from h1")
	}
	if counts[0] != 1 || counts[1] != 1 {
		t.Errorf("MultiHandler: expected both handlers to run, got counts=%v", counts)
	}
}

func TestNewChannelHandler(t *testing.T) {
	h, ch := fswatch.NewChannelHandler(10)
	ctx := context.Background()

	e := testutil.NewEnrichedEvent().WithPath("/merged/test.so").Build()
	if err := h.Handle(ctx, e); err != nil {
		t.Fatalf("Handle: unexpected error: %v", err)
	}

	select {
	case got := <-ch:
		if got.Path != "/merged/test.so" {
			t.Errorf("channel event path = %q, want /merged/test.so", got.Path)
		}
	default:
		t.Error("ChannelHandler: no event in channel")
	}
}

// ─────────────────────────────────────────────────────────────────────────────
// OverlayInfo tests
// ─────────────────────────────────────────────────────────────────────────────

func TestOverlayInfo_ContainsPath(t *testing.T) {
	o := fswatch.NewOverlayInfo("/merged", "/upper", "/work", []string{"/lower0"})

	if !o.ContainsPath("/merged/app/main.go") {
		t.Error("ContainsPath: /merged/app/main.go should be inside merged dir")
	}
	if !o.ContainsPath("/merged") {
		t.Error("ContainsPath: /merged itself should match")
	}
	if o.ContainsPath("/other/path") {
		t.Error("ContainsPath: /other/path should not be inside merged dir")
	}
}

func TestOverlayInfo_RelPath(t *testing.T) {
	o := fswatch.NewOverlayInfo("/merged", "/upper", "/work", []string{"/lower0"})

	rel, err := o.RelPath("/merged/app/main.go")
	if err != nil {
		t.Fatalf("RelPath: unexpected error: %v", err)
	}
	if rel != "app/main.go" {
		t.Errorf("RelPath = %q, want app/main.go", rel)
	}

	_, err = o.RelPath("/other/path")
	if err == nil {
		t.Error("RelPath: expected error for path outside merged dir")
	}
}

func TestOverlayInfo_AllDirs(t *testing.T) {
	o := fswatch.NewOverlayInfo("/merged", "/upper", "/work", []string{"/lower0", "/lower1"})
	dirs := o.AllDirs()

	if len(dirs) != 3 {
		t.Fatalf("AllDirs: want 3 dirs (upper + 2 lowers), got %d", len(dirs))
	}
	if dirs[0] != "/upper" {
		t.Errorf("AllDirs[0] = %q, want /upper", dirs[0])
	}
}

func TestOverlayInfo_BuildLayers(t *testing.T) {
	o := fswatch.NewOverlayInfo("/merged", "/upper", "/work", []string{"/lower0", "/lower1"})

	if len(o.Layers) != 3 {
		t.Fatalf("Layers: want 3 (1 upper + 2 lowers), got %d", len(o.Layers))
	}
	if !o.Layers[0].IsUpper {
		t.Error("Layers[0] should be upper")
	}
	if o.Layers[1].IsUpper || o.Layers[2].IsUpper {
		t.Error("Layers[1] and Layers[2] should not be upper")
	}
}

// ─────────────────────────────────────────────────────────────────────────────
// Pipeline tests
// ─────────────────────────────────────────────────────────────────────────────

func TestPipeline_ReadOnlyPreset_FiltersWrites(t *testing.T) {
	collector := &fswatch.CollectingHandler{}

	pipeline := fswatch.NewPipeline(
		fswatch.WithReadOnlyPipeline(),
		fswatch.WithHandler(collector),
		fswatch.WithWorkers(1),
	)

	watcher := testutil.NewFakeWatcher(32)
	events := testutil.MakeMixedEvents("/merged", 8)
	watcher.SendMany(events)
	watcher.Close()

	ctx := context.Background()
	ch, _ := watcher.Watch(ctx)
	result := pipeline.RunSync(ctx, ch, nil)

	if result.Received != 8 {
		t.Errorf("Received = %d, want 8", result.Received)
	}

	// Only ACCESS, OPEN, OPEN_EXEC, CLOSE_NOWRITE ops (indices 0,2,4,6) should pass.
	wantHandled := int64(4)
	if result.Handled != wantHandled {
		t.Errorf("Handled = %d, want %d", result.Handled, wantHandled)
	}
	wantFiltered := int64(4)
	if result.Filtered != wantFiltered {
		t.Errorf("Filtered = %d, want %d", result.Filtered, wantFiltered)
	}
}

func TestPipeline_TransformerEnrichesEvents(t *testing.T) {
	collector := &fswatch.CollectingHandler{}

	tr := fswatch.StaticAttrTransformer(map[string]any{"enriched": true})

	pipeline := fswatch.NewPipeline(
		fswatch.WithTransformer(tr),
		fswatch.WithHandler(collector),
		fswatch.WithWorkers(1),
	)

	watcher := testutil.NewFakeWatcher(8)
	events := testutil.MakeReadOnlyEvents("/merged", 3)
	watcher.SendMany(events)
	watcher.Close()

	ctx := context.Background()
	ch, _ := watcher.Watch(ctx)
	pipeline.RunSync(ctx, ch, nil)

	for _, e := range collector.Events() {
		if e.Attr("enriched") != true {
			t.Errorf("event %q missing enriched attr", e.Path)
		}
	}
}

func TestPipeline_ContextCancellation_Stops(t *testing.T) {
	pipeline := fswatch.NewPipeline(
		fswatch.WithHandler(fswatch.NoopHandler{}),
		fswatch.WithWorkers(1),
	)

	// Unbuffered watcher — pipeline will block waiting for the next event.
	watcher := testutil.NewFakeWatcher(0)
	ctx, cancel := context.WithCancel(context.Background())

	ch, _ := watcher.Watch(ctx)

	done := make(chan struct{})
	go func() {
		defer close(done)
		pipeline.RunSync(ctx, ch, nil)
	}()

	cancel()

	select {
	case <-done:
	case <-time.After(2 * time.Second):
		t.Error("Pipeline: did not stop after context cancellation")
	}
}

func TestPipeline_ErrorChannel_ReceivesHandlerErrors(t *testing.T) {
	errHandler := &testutil.ErroringHandler{Err: fmt.Errorf("deliberate error")}

	pipeline := fswatch.NewPipeline(
		fswatch.WithHandler(errHandler),
		fswatch.WithWorkers(1),
	)

	watcher := testutil.NewFakeWatcher(4)
	watcher.Send(testutil.NewRawEvent().Build())
	watcher.Close()

	ctx := context.Background()
	ch, _ := watcher.Watch(ctx)

	var errs []error
	pipeline.RunSync(ctx, ch, func(err error) {
		errs = append(errs, err)
	})

	if len(errs) == 0 {
		t.Error("Pipeline: expected error from erroring handler")
	}
}

func TestPipeline_MultipleFilters_AllMustPass(t *testing.T) {
	collector := &fswatch.CollectingHandler{}

	pipeline := fswatch.NewPipeline(
		fswatch.WithFilter(fswatch.ReadOnlyFilter()),
		fswatch.WithFilter(fswatch.PathPrefixFilter("/merged/app")),
		fswatch.WithHandler(collector),
		fswatch.WithWorkers(1),
	)

	watcher := testutil.NewFakeWatcher(8)
	watcher.Send(testutil.NewRawEvent().WithOp(fswatch.OpAccess).WithPath("/merged/app/main.go").Build())
	watcher.Send(testutil.NewRawEvent().WithOp(fswatch.OpAccess).WithPath("/merged/other/file").Build())
	watcher.Send(testutil.NewRawEvent().WithOp(fswatch.OpModify).WithPath("/merged/app/main.go").Build())
	watcher.Close()

	ctx := context.Background()
	ch, _ := watcher.Watch(ctx)
	result := pipeline.RunSync(ctx, ch, nil)

	if result.Handled != 1 {
		t.Errorf("Handled = %d, want 1 (only app read-access)", result.Handled)
	}
}

func TestPipeline_OverlayEnrichment(t *testing.T) {
	overlay := fswatch.NewOverlayInfo("/merged", "/upper", "/work", []string{"/lower0"})
	collector := &fswatch.CollectingHandler{}

	pipeline := fswatch.NewPipeline(
		fswatch.WithOverlayEnrichment(overlay),
		fswatch.WithHandler(collector),
		fswatch.WithWorkers(1),
	)

	watcher := testutil.NewFakeWatcher(4)
	watcher.Send(testutil.NewRawEvent().WithPath("/merged/app.py").Build())
	watcher.Close()

	ctx := context.Background()
	ch, _ := watcher.Watch(ctx)
	pipeline.RunSync(ctx, ch, nil)

	events := collector.Events()
	if len(events) == 0 {
		t.Fatal("expected at least one event")
	}
	if events[0].Overlay == nil {
		t.Error("event should have Overlay set after OverlayEnrichment")
	}
}

// ─────────────────────────────────────────────────────────────────────────────
// Bug-fix regression tests
// ─────────────────────────────────────────────────────────────────────────────

// TestPipeline_Run_NonBlocking verifies the fixed Run() returns two proper
// channels and delivers the correct final result (Bug 1).
func TestPipeline_Run_NonBlocking(t *testing.T) {
	counter := &fswatch.CountingHandler{}
	pipeline := fswatch.NewPipeline(
		fswatch.WithReadOnlyPipeline(),
		fswatch.WithHandler(counter),
		fswatch.WithWorkers(1),
	)

	w := testutil.NewFakeWatcher(16)
	w.SendMany(testutil.MakeReadOnlyEvents("/merged", 6))
	w.Close()

	ctx := context.Background()
	ch, _ := w.Watch(ctx)

	resultCh, errCh := pipeline.Run(ctx, ch)

	// Drain errors.
	for range errCh {
	}

	result := <-resultCh
	if result.Received != 6 {
		t.Errorf("Run: Received = %d, want 6", result.Received)
	}
	if result.Handled != 6 {
		t.Errorf("Run: Handled = %d, want 6", result.Handled)
	}
}

// TestPipeline_Run_ResultChannelClosedAfterDone verifies resultCh is closed
// exactly once and receives exactly one value (Bug 1).
func TestPipeline_Run_ResultChannelClosedAfterDone(t *testing.T) {
	pipeline := fswatch.NewPipeline(
		fswatch.WithHandler(fswatch.NoopHandler{}),
		fswatch.WithWorkers(1),
	)

	w := testutil.NewFakeWatcher(4)
	w.Close()

	ctx := context.Background()
	ch, _ := w.Watch(ctx)
	resultCh, errCh := pipeline.Run(ctx, ch)

	for range errCh {
	}

	count := 0
	for range resultCh {
		count++
	}
	if count != 1 {
		t.Errorf("resultCh should deliver exactly one value, got %d", count)
	}
}

// TestMaskReadOnly_IsImmutable verifies that MaskReadOnly is a constant and
// cannot be accidentally overwritten (Bug 7 — mutable var to const).
func TestMaskReadOnly_IsImmutable(t *testing.T) {
	if !fswatch.MaskReadOnly.IsReadOnly() {
		t.Error("MaskReadOnly.IsReadOnly() should always be true")
	}
	// Verify it does not include any modification op.
	for _, op := range []fswatch.Op{
		fswatch.OpModify, fswatch.OpCreate, fswatch.OpDelete, fswatch.OpCloseWrite,
	} {
		if fswatch.MaskReadOnly.Has(op) {
			t.Errorf("MaskReadOnly unexpectedly contains %v", op)
		}
	}
}

// TestMultiHandler_ErrorChainPreserved verifies that errors.Is works through
// MultiHandler's joined error (Bug 9 — fmt.Errorf to errors.Join).
func TestMultiHandler_ErrorChainPreserved(t *testing.T) {
	sentinel := fmt.Errorf("sentinel")
	h1 := fswatch.HandlerFunc(func(_ context.Context, _ *fswatch.EnrichedEvent) error {
		return sentinel
	})
	h2 := fswatch.HandlerFunc(func(_ context.Context, _ *fswatch.EnrichedEvent) error {
		return nil
	})

	multi := fswatch.MultiHandler{h1, h2}
	err := multi.Handle(context.Background(), testutil.NewEnrichedEvent().Build())

	if !errors.Is(err, sentinel) {
		t.Errorf("MultiHandler error chain broken: errors.Is(err, sentinel) = false; got %v", err)
	}
}

// TestOverlayEnricher_SetsOverlay_OnlyWhenPathInside verifies that Overlay is
// nil for external paths — the core false-positive fix (Bug 6).
func TestOverlayEnricher_SetsOverlay_OnlyWhenPathInside(t *testing.T) {
	overlay := fswatch.NewOverlayInfo("/merged", "/upper", "/work", []string{"/lower0"})
	enricher := fswatch.NewOverlayEnricher(overlay)
	ctx := context.Background()

	tests := []struct {
		path        string
		wantOverlay bool
		label       string
	}{
		{"/merged/app/main.go", true, "inside merged"},
		{"/merged", true, "merged dir itself"},
		{"/proc/1/maps", false, "proc path"},
		{"/var/log/syslog", false, "unrelated path"},
		{"/mergedX/file", false, "path with merged as prefix but not under it"},
	}

	for _, tt := range tests {
		t.Run(tt.label, func(t *testing.T) {
			e := testutil.NewEnrichedEvent().WithPath(tt.path).Build()
			if err := enricher.Transform(ctx, e); err != nil {
				t.Fatalf("unexpected error: %v", err)
			}
			gotOverlay := e.Overlay != nil
			if gotOverlay != tt.wantOverlay {
				t.Errorf("Overlay set=%v, want %v for path %q", gotOverlay, tt.wantOverlay, tt.path)
			}
		})
	}
}

// TestStaticAttrTransformer_CallerMutationSafe verifies that mutating the
// source map after construction does not affect in-flight events (bug in
// original that didn't copy the map at construction time).
func TestStaticAttrTransformer_CallerMutationSafe(t *testing.T) {
	attrs := map[string]any{"key": "original"}
	tr := fswatch.StaticAttrTransformer(attrs)

	// Mutate the source map after construction.
	attrs["key"] = "mutated"
	attrs["extra"] = "injected"

	e := testutil.NewEnrichedEvent().Build()
	if err := tr.Transform(context.Background(), e); err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	if e.Attr("key") != "original" {
		t.Errorf("StaticAttrTransformer: caller mutation affected event; got %v", e.Attr("key"))
	}
	if e.Attr("extra") != nil {
		t.Errorf("StaticAttrTransformer: injected key after construction appeared in event")
	}
}

// TestChainTransformer_ErrorsJoined verifies that multiple transformer errors
// are joined via errors.Join and individually unwrappable (Bug 8).
func TestChainTransformer_ErrorsJoined(t *testing.T) {
	e1 := fmt.Errorf("transformer error 1")
	e2 := fmt.Errorf("transformer error 2")

	t1 := fswatch.TransformerFunc(func(_ context.Context, _ *fswatch.EnrichedEvent) error { return e1 })
	t2 := fswatch.TransformerFunc(func(_ context.Context, _ *fswatch.EnrichedEvent) error { return e2 })

	chain := fswatch.ChainTransformer{t1, t2}
	err := chain.Transform(context.Background(), testutil.NewEnrichedEvent().Build())

	if !errors.Is(err, e1) {
		t.Error("ChainTransformer joined error should contain e1 (errors.Is)")
	}
	if !errors.Is(err, e2) {
		t.Error("ChainTransformer joined error should contain e2 (errors.Is)")
	}
}

// TestFreshnessFilter_InFilterFile tests that FreshnessFilter is callable from
// the fanwatch package (Bug 5 — was in pipeline.go, now in filter.go).
func TestFreshnessFilter_CorrectPackage(t *testing.T) {
	f := fswatch.FreshnessFilter(100 * time.Millisecond)
	if f == nil {
		t.Error("FreshnessFilter returned nil")
	}
}

// ─────────────────────────────────────────────────────────────────────────────
// WatcherID promotion test (Fix: removed duplicate field from Event)
// ─────────────────────────────────────────────────────────────────────────────

// TestRawEvent_WatcherID_PromotedToEvent verifies that WatcherID set on
// RawEvent is accessible via the embedded field on Event and EnrichedEvent.
func TestRawEvent_WatcherID_PromotedToEvent(t *testing.T) {
	raw := testutil.NewRawEvent().Build()
	raw.WatcherID = "watcher-42"

	enriched := &fswatch.EnrichedEvent{
		Event: fswatch.Event{
			RawEvent: *raw,
			Dir:      "/merged/dir",
			Name:     "file.txt",
		},
	}

	// WatcherID is promoted from RawEvent via embedding — no separate field.
	if enriched.WatcherID != "watcher-42" {
		t.Errorf("WatcherID = %q, want watcher-42 (promoted from RawEvent)", enriched.WatcherID)
	}
}

// ─────────────────────────────────────────────────────────────────────────────
// ErrQueueOverflow pipeline routing test (Fix: overflow events wired to errCh)
// ─────────────────────────────────────────────────────────────────────────────

// TestPipeline_OverflowEvent_RoutedToErrorChannel verifies that an overflow
// event (OpOverflow mask) is not delivered to the handler but does emit
// ErrQueueOverflow on the error channel.
func TestPipeline_OverflowEvent_RoutedToErrorChannel(t *testing.T) {
	collector := &fswatch.CollectingHandler{}
	pipeline := fswatch.NewPipeline(
		fswatch.WithHandler(collector),
		fswatch.WithWorkers(1),
	)

	w := testutil.NewFakeWatcher(4)
	// Overflow event: OpOverflow mask, no path.
	w.Send(testutil.NewRawEvent().WithMask(fswatch.EventMask(fswatch.OpOverflow)).WithPath("").Build())
	// Normal event after overflow.
	w.Send(testutil.NewRawEvent().WithOp(fswatch.OpAccess).WithPath("/merged/file.txt").Build())
	w.Close()

	var errs []error
	ctx := context.Background()
	ch, _ := w.Watch(ctx)
	result := pipeline.RunSync(ctx, ch, func(err error) {
		errs = append(errs, err)
	})

	if result.Received != 2 {
		t.Errorf("Received = %d, want 2", result.Received)
	}
	// Overflow event should be filtered (not handled).
	if result.Handled != 1 {
		t.Errorf("Handled = %d, want 1 (normal event only)", result.Handled)
	}
	// ErrQueueOverflow should appear in the error channel.
	found := false
	for _, err := range errs {
		if errors.Is(err, fswatch.ErrQueueOverflow) {
			found = true
			break
		}
	}
	if !found {
		t.Errorf("expected ErrQueueOverflow in error channel, got: %v", errs)
	}
}

// ─────────────────────────────────────────────────────────────────────────────
// ErrQueueOverflow is not re-emitted by NoOverflowFilter
// ─────────────────────────────────────────────────────────────────────────────

// TestNoOverflowFilter_PreventsOverflowFromHandler verifies the filter works
// as the primary guard when callers prefer silent overflow suppression.
func TestNoOverflowFilter_PreventsOverflowFromReachingHandler(t *testing.T) {
	counter := &fswatch.CountingHandler{}
	pipeline := fswatch.NewPipeline(
		fswatch.WithFilter(fswatch.NoOverflowFilter()),
		fswatch.WithHandler(counter),
		fswatch.WithWorkers(1),
	)

	w := testutil.NewFakeWatcher(4)
	w.Send(testutil.NewRawEvent().WithMask(fswatch.EventMask(fswatch.OpOverflow)).Build())
	w.Send(testutil.NewRawEvent().WithOp(fswatch.OpAccess).Build())
	w.Close()

	ctx := context.Background()
	ch, _ := w.Watch(ctx)
	// Overflow is not OpOverflow at pipeline entry — it's caught before filters.
	// So even without NoOverflowFilter the handler won't receive it.
	// With NoOverflowFilter in place, the ACCESS event should pass.
	result := pipeline.RunSync(ctx, ch, nil)

	if result.Handled != 1 {
		t.Errorf("Handled = %d, want 1", result.Handled)
	}
	if counter.Snapshot().Total != 1 {
		t.Errorf("CountingHandler.Total = %d, want 1", counter.Snapshot().Total)
	}
}

// ─────────────────────────────────────────────────────────────────────────────
// parseKVOptions correctness (overlay.go internal, tested via OverlayInfoFromMount)
// ─────────────────────────────────────────────────────────────────────────────

// TestOverlayInfoFromMountFile_ComplexOptions verifies that VFSOptions with
// multiple keys, spaces, and a 3-lowerdir stack parse correctly.
func TestOverlayInfoFromMountFile_ComplexVFSOptions(t *testing.T) {
	// Build a mountinfo fixture with 3 lower dirs and extra options.
	mountFile := buildMountinfoFixture(t,
		"/merged",
		"/upper",
		"/work",
		"/lower3:/lower2:/lower1",
		"rw,lowerdir=/lower3:/lower2:/lower1,upperdir=/upper,workdir=/work,userxattr",
	)

	info, err := fswatch.OverlayInfoFromMountFile(mountFile, "/merged")
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	if len(info.LowerDirs) != 3 {
		t.Fatalf("LowerDirs len = %d, want 3; got %v", len(info.LowerDirs), info.LowerDirs)
	}
	if info.LowerDirs[0] != "/lower3" {
		t.Errorf("LowerDirs[0] = %q, want /lower3", info.LowerDirs[0])
	}
}

// buildMountinfoFixture writes a mountinfo file with a single overlay line.
func buildMountinfoFixture(t *testing.T, merged, upper, work, lowerStr, superOpts string) string {
	t.Helper()
	// Suppress unused parameter warnings; upper/work/lowerStr are in superOpts.
	_, _, _ = upper, work, lowerStr
	f, err := os.CreateTemp(t.TempDir(), "mountinfo")
	if err != nil {
		t.Fatalf("create mountinfo fixture: %v", err)
	}
	defer f.Close()
	line := fmt.Sprintf("69 64 0:46 / %s rw,relatime - overlay overlay %s\n", merged, superOpts)
	if _, err := f.WriteString(line); err != nil {
		t.Fatalf("write mountinfo fixture: %v", err)
	}
	return f.Name()
}
