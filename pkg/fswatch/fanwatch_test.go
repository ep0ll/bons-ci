package fanwatch_test

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"testing"
	"time"

	fanwatch "github.com/bons/bons-ci/pkg/fswatch"
	"github.com/bons/bons-ci/pkg/fswatch/testutil"
)

// ─────────────────────────────────────────────────────────────────────────────
// EventMask tests
// ─────────────────────────────────────────────────────────────────────────────

func TestEventMask_Has(t *testing.T) {
	mask := fanwatch.MaskReadOnly

	for _, op := range []fanwatch.Op{
		fanwatch.OpAccess,
		fanwatch.OpOpen,
		fanwatch.OpOpenExec,
		fanwatch.OpCloseNoWrite,
	} {
		if !mask.Has(op) {
			t.Errorf("MaskReadOnly.Has(%v) = false, want true", op)
		}
	}

	for _, op := range []fanwatch.Op{
		fanwatch.OpModify,
		fanwatch.OpCreate,
		fanwatch.OpDelete,
		fanwatch.OpCloseWrite,
	} {
		if mask.Has(op) {
			t.Errorf("MaskReadOnly.Has(%v) = true, want false", op)
		}
	}
}

func TestEventMask_IsReadOnly(t *testing.T) {
	tests := []struct {
		name   string
		mask   fanwatch.EventMask
		wantRO bool
	}{
		{"read only mask", fanwatch.MaskReadOnly, true},
		{"access only", fanwatch.EventMask(fanwatch.OpAccess), true},
		{"open only", fanwatch.EventMask(fanwatch.OpOpen), true},
		{"modify", fanwatch.EventMask(fanwatch.OpModify), false},
		{"create", fanwatch.EventMask(fanwatch.OpCreate), false},
		{"delete", fanwatch.EventMask(fanwatch.OpDelete), false},
		{"close write", fanwatch.EventMask(fanwatch.OpCloseWrite), false},
		{"mixed access+modify", fanwatch.EventMask(fanwatch.OpAccess | fanwatch.OpModify), false},
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
		mask fanwatch.EventMask
		want string
	}{
		{fanwatch.EventMask(fanwatch.OpAccess), "ACCESS"},
		{fanwatch.EventMask(fanwatch.OpModify), "MODIFY"},
		{fanwatch.EventMask(0), "none"},
	}
	for _, tt := range tests {
		got := tt.mask.String()
		if got != tt.want {
			t.Errorf("EventMask(%d).String() = %q, want %q", tt.mask, got, tt.want)
		}
	}
	// Multi-op mask should contain both names.
	combined := fanwatch.EventMask(fanwatch.OpAccess | fanwatch.OpOpen)
	s := combined.String()
	if !strings.Contains(s, "ACCESS") || !strings.Contains(s, "OPEN") {
		t.Errorf("combined mask string %q missing expected ops", s)
	}
}

// ─────────────────────────────────────────────────────────────────────────────
// RawEvent tests
// ─────────────────────────────────────────────────────────────────────────────

func TestRawEvent_IsOverflow(t *testing.T) {
	overflow := &fanwatch.RawEvent{Mask: fanwatch.EventMask(fanwatch.OpOverflow)}
	normal := &fanwatch.RawEvent{Mask: fanwatch.EventMask(fanwatch.OpAccess)}

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
	e := &fanwatch.EnrichedEvent{}
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
	roEvent := testutil.NewEnrichedEvent().WithOp(fanwatch.OpAccess).Build()
	rwEvent := testutil.NewEnrichedEvent().WithOp(fanwatch.OpModify).Build()

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
	f := fanwatch.ReadOnlyFilter()
	ctx := makeContext()

	readOps := []fanwatch.Op{
		fanwatch.OpAccess, fanwatch.OpOpen, fanwatch.OpOpenExec, fanwatch.OpCloseNoWrite,
	}
	for _, op := range readOps {
		e := testutil.NewEnrichedEvent().WithOp(op).Build()
		if !f.Allow(ctx, e) {
			t.Errorf("ReadOnlyFilter: Allow(%v) = false, want true", op)
		}
	}
}

func TestReadOnlyFilter_DropsWriteOps(t *testing.T) {
	f := fanwatch.ReadOnlyFilter()
	ctx := makeContext()

	writeOps := []fanwatch.Op{
		fanwatch.OpModify, fanwatch.OpCreate, fanwatch.OpDelete,
		fanwatch.OpCloseWrite, fanwatch.OpMovedFrom, fanwatch.OpMovedTo, fanwatch.OpAttrib,
	}
	for _, op := range writeOps {
		e := testutil.NewEnrichedEvent().WithOp(op).Build()
		if f.Allow(ctx, e) {
			t.Errorf("ReadOnlyFilter: Allow(%v) = true, want false", op)
		}
	}
}

func TestMaskFilter_MatchesExpectedOps(t *testing.T) {
	f := fanwatch.MaskFilter(fanwatch.OpAccess, fanwatch.OpOpen)
	ctx := makeContext()

	allow := []fanwatch.Op{fanwatch.OpAccess, fanwatch.OpOpen}
	deny := []fanwatch.Op{fanwatch.OpModify, fanwatch.OpCreate, fanwatch.OpOpenExec}

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
	f := fanwatch.PathPrefixFilter("/merged/app")
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
	f := fanwatch.PathExcludeFilter("/proc", "/sys")
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
	f := fanwatch.ExtensionFilter(".so", ".py")
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
	f := fanwatch.PIDFilter(100, 200)
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
	f := fanwatch.ExcludePIDFilter(1)
	ctx := makeContext()

	if f.Allow(ctx, testutil.NewEnrichedEvent().WithPID(1).Build()) {
		t.Error("ExcludePIDFilter: PID 1 should be excluded")
	}
	if !f.Allow(ctx, testutil.NewEnrichedEvent().WithPID(1234).Build()) {
		t.Error("ExcludePIDFilter: PID 1234 should pass")
	}
}

func TestNoOverflowFilter(t *testing.T) {
	f := fanwatch.NoOverflowFilter()
	ctx := makeContext()

	overflow := testutil.NewEnrichedEvent().WithMask(fanwatch.EventMask(fanwatch.OpOverflow)).Build()
	normal := testutil.NewEnrichedEvent().WithOp(fanwatch.OpAccess).Build()

	if f.Allow(ctx, overflow) {
		t.Error("NoOverflowFilter: overflow event should be dropped")
	}
	if !f.Allow(ctx, normal) {
		t.Error("NoOverflowFilter: normal event should pass")
	}
}

func TestAllFilters_ShortCircuit(t *testing.T) {
	called := 0
	counter := fanwatch.FilterFunc(func(_ context.Context, _ *fanwatch.EnrichedEvent) bool {
		called++
		return true
	})
	// First filter rejects — counter should never be called.
	rejecter := fanwatch.FilterFunc(func(_ context.Context, _ *fanwatch.EnrichedEvent) bool {
		return false
	})

	all := fanwatch.AllFilters{rejecter, counter}
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
	reject := fanwatch.FilterFunc(func(_ context.Context, _ *fanwatch.EnrichedEvent) bool { return false })
	accept := fanwatch.FilterFunc(func(_ context.Context, _ *fanwatch.EnrichedEvent) bool { return true })

	any1 := fanwatch.AnyFilter{reject, accept}
	e := testutil.NewEnrichedEvent().Build()
	if !any1.Allow(context.Background(), e) {
		t.Error("AnyFilter: should pass when at least one filter accepts")
	}

	any2 := fanwatch.AnyFilter{reject, reject}
	if any2.Allow(context.Background(), e) {
		t.Error("AnyFilter: should reject when all filters reject")
	}
}

func TestNot_Negates(t *testing.T) {
	alwaysAllow := fanwatch.FilterFunc(func(_ context.Context, _ *fanwatch.EnrichedEvent) bool { return true })
	neg := fanwatch.Not{Inner: alwaysAllow}

	e := testutil.NewEnrichedEvent().Build()
	if neg.Allow(context.Background(), e) {
		t.Error("Not: should negate the inner filter")
	}
}

func TestExternalFilter(t *testing.T) {
	allowed := map[string]bool{"/merged/safe/file": true}
	f := fanwatch.ExternalFilter(func(path string) bool { return allowed[path] })
	ctx := context.Background()

	if !f.Allow(ctx, testutil.NewEnrichedEvent().WithPath("/merged/safe/file").Build()) {
		t.Error("ExternalFilter: allowed path should pass")
	}
	if f.Allow(ctx, testutil.NewEnrichedEvent().WithPath("/merged/unsafe/file").Build()) {
		t.Error("ExternalFilter: non-allowed path should be rejected")
	}
}

func TestUpperDirOnlyFilter(t *testing.T) {
	f := fanwatch.UpperDirOnlyFilter()
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
	f := fanwatch.LowerDirOnlyFilter()
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
	f := fanwatch.AttrValueFilter("env", "prod")
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
	f := fanwatch.FreshnessFilter(50 * time.Millisecond)
	ctx := context.Background()

	fresh := testutil.NewRawEvent().WithTimestamp(time.Now()).Build()
	stale := testutil.NewRawEvent().WithTimestamp(time.Now().Add(-1 * time.Second)).Build()

	freshE := &fanwatch.EnrichedEvent{Event: fanwatch.Event{RawEvent: *fresh}}
	staleE := &fanwatch.EnrichedEvent{Event: fanwatch.Event{RawEvent: *stale}}

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
	tr := fanwatch.StaticAttrTransformer(attrs)

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
	tr := fanwatch.DynamicAttrTransformer(func(e *fanwatch.EnrichedEvent) map[string]any {
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
	t1 := fanwatch.TransformerFunc(func(_ context.Context, e *fanwatch.EnrichedEvent) error {
		order = append(order, 1)
		e.SetAttr("t1", true)
		return nil
	})
	t2 := fanwatch.TransformerFunc(func(_ context.Context, e *fanwatch.EnrichedEvent) error {
		order = append(order, 2)
		e.SetAttr("t2", true)
		return nil
	})

	chain := fanwatch.ChainTransformer{t1, t2}
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
	errT := fanwatch.TransformerFunc(func(_ context.Context, _ *fanwatch.EnrichedEvent) error {
		return fmt.Errorf("intentional error")
	})
	setAttr := fanwatch.TransformerFunc(func(_ context.Context, e *fanwatch.EnrichedEvent) error {
		e.SetAttr("after_error", true)
		return nil
	})

	chain := fanwatch.ChainTransformer{errT, setAttr}
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
	inner := fanwatch.TransformerFunc(func(_ context.Context, _ *fanwatch.EnrichedEvent) error {
		ran = true
		return nil
	})

	ct := fanwatch.ConditionalTransformer{
		Predicate: func(e *fanwatch.EnrichedEvent) bool { return false },
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
	overlay := fanwatch.NewOverlayInfo(
		"/merged", "/upper", "/work",
		[]string{"/lower0", "/lower1"},
	)
	enricher := fanwatch.NewOverlayEnricher(overlay)

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
	overlay := fanwatch.NewOverlayInfo("/merged", "/upper", "/work", []string{"/lower0"})
	enricher := fanwatch.NewOverlayEnricher(overlay)

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
	h := &fanwatch.CountingHandler{}
	ctx := context.Background()

	events := []fanwatch.Op{
		fanwatch.OpAccess, fanwatch.OpOpen, fanwatch.OpOpenExec,
		fanwatch.OpAccess, fanwatch.OpModify,
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
	h := &fanwatch.CountingHandler{}
	ctx := context.Background()

	_ = h.Handle(ctx, testutil.NewEnrichedEvent().Build())
	_ = h.Handle(ctx, testutil.NewEnrichedEvent().Build())

	h.Reset()
	if h.Snapshot().Total != 0 {
		t.Error("Reset: counters should be zero after Reset")
	}
}

func TestCollectingHandler(t *testing.T) {
	h := &fanwatch.CollectingHandler{}
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
	h := &fanwatch.CollectingHandler{}
	ctx := context.Background()
	_ = h.Handle(ctx, testutil.NewEnrichedEvent().Build())
	h.Reset()
	if h.Len() != 0 {
		t.Error("Reset: collector should be empty")
	}
}

func TestChainHandler_StopsOnError(t *testing.T) {
	ran := false
	first := fanwatch.HandlerFunc(func(_ context.Context, _ *fanwatch.EnrichedEvent) error {
		return fmt.Errorf("first handler failed")
	})
	second := fanwatch.HandlerFunc(func(_ context.Context, _ *fanwatch.EnrichedEvent) error {
		ran = true
		return nil
	})

	chain := fanwatch.ChainHandler{first, second}
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
	h1 := fanwatch.HandlerFunc(func(_ context.Context, _ *fanwatch.EnrichedEvent) error {
		counts[0]++
		return fmt.Errorf("h1 error")
	})
	h2 := fanwatch.HandlerFunc(func(_ context.Context, _ *fanwatch.EnrichedEvent) error {
		counts[1]++
		return nil
	})

	multi := fanwatch.MultiHandler{h1, h2}
	err := multi.Handle(context.Background(), testutil.NewEnrichedEvent().Build())

	if err == nil {
		t.Error("MultiHandler: expected error from h1")
	}
	if counts[0] != 1 || counts[1] != 1 {
		t.Errorf("MultiHandler: expected both handlers to run, got counts=%v", counts)
	}
}

func TestNewChannelHandler(t *testing.T) {
	h, ch := fanwatch.NewChannelHandler(10)
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
	o := fanwatch.NewOverlayInfo("/merged", "/upper", "/work", []string{"/lower0"})

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
	o := fanwatch.NewOverlayInfo("/merged", "/upper", "/work", []string{"/lower0"})

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
	o := fanwatch.NewOverlayInfo("/merged", "/upper", "/work", []string{"/lower0", "/lower1"})
	dirs := o.AllDirs()

	if len(dirs) != 3 {
		t.Fatalf("AllDirs: want 3 dirs (upper + 2 lowers), got %d", len(dirs))
	}
	if dirs[0] != "/upper" {
		t.Errorf("AllDirs[0] = %q, want /upper", dirs[0])
	}
}

func TestOverlayInfo_BuildLayers(t *testing.T) {
	o := fanwatch.NewOverlayInfo("/merged", "/upper", "/work", []string{"/lower0", "/lower1"})

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
	collector := &fanwatch.CollectingHandler{}

	pipeline := fanwatch.NewPipeline(
		fanwatch.WithReadOnlyPipeline(),
		fanwatch.WithHandler(collector),
		fanwatch.WithWorkers(1),
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
	collector := &fanwatch.CollectingHandler{}

	tr := fanwatch.StaticAttrTransformer(map[string]any{"enriched": true})

	pipeline := fanwatch.NewPipeline(
		fanwatch.WithTransformer(tr),
		fanwatch.WithHandler(collector),
		fanwatch.WithWorkers(1),
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
	pipeline := fanwatch.NewPipeline(
		fanwatch.WithHandler(fanwatch.NoopHandler{}),
		fanwatch.WithWorkers(1),
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

	pipeline := fanwatch.NewPipeline(
		fanwatch.WithHandler(errHandler),
		fanwatch.WithWorkers(1),
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
	collector := &fanwatch.CollectingHandler{}

	pipeline := fanwatch.NewPipeline(
		fanwatch.WithFilter(fanwatch.ReadOnlyFilter()),
		fanwatch.WithFilter(fanwatch.PathPrefixFilter("/merged/app")),
		fanwatch.WithHandler(collector),
		fanwatch.WithWorkers(1),
	)

	watcher := testutil.NewFakeWatcher(8)
	watcher.Send(testutil.NewRawEvent().WithOp(fanwatch.OpAccess).WithPath("/merged/app/main.go").Build())
	watcher.Send(testutil.NewRawEvent().WithOp(fanwatch.OpAccess).WithPath("/merged/other/file").Build())
	watcher.Send(testutil.NewRawEvent().WithOp(fanwatch.OpModify).WithPath("/merged/app/main.go").Build())
	watcher.Close()

	ctx := context.Background()
	ch, _ := watcher.Watch(ctx)
	result := pipeline.RunSync(ctx, ch, nil)

	if result.Handled != 1 {
		t.Errorf("Handled = %d, want 1 (only app read-access)", result.Handled)
	}
}

func TestPipeline_OverlayEnrichment(t *testing.T) {
	overlay := fanwatch.NewOverlayInfo("/merged", "/upper", "/work", []string{"/lower0"})
	collector := &fanwatch.CollectingHandler{}

	pipeline := fanwatch.NewPipeline(
		fanwatch.WithOverlayEnrichment(overlay),
		fanwatch.WithHandler(collector),
		fanwatch.WithWorkers(1),
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
	counter := &fanwatch.CountingHandler{}
	pipeline := fanwatch.NewPipeline(
		fanwatch.WithReadOnlyPipeline(),
		fanwatch.WithHandler(counter),
		fanwatch.WithWorkers(1),
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
	pipeline := fanwatch.NewPipeline(
		fanwatch.WithHandler(fanwatch.NoopHandler{}),
		fanwatch.WithWorkers(1),
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
	if !fanwatch.MaskReadOnly.IsReadOnly() {
		t.Error("MaskReadOnly.IsReadOnly() should always be true")
	}
	// Verify it does not include any modification op.
	for _, op := range []fanwatch.Op{
		fanwatch.OpModify, fanwatch.OpCreate, fanwatch.OpDelete, fanwatch.OpCloseWrite,
	} {
		if fanwatch.MaskReadOnly.Has(op) {
			t.Errorf("MaskReadOnly unexpectedly contains %v", op)
		}
	}
}

// TestMultiHandler_ErrorChainPreserved verifies that errors.Is works through
// MultiHandler's joined error (Bug 9 — fmt.Errorf to errors.Join).
func TestMultiHandler_ErrorChainPreserved(t *testing.T) {
	sentinel := fmt.Errorf("sentinel")
	h1 := fanwatch.HandlerFunc(func(_ context.Context, _ *fanwatch.EnrichedEvent) error {
		return sentinel
	})
	h2 := fanwatch.HandlerFunc(func(_ context.Context, _ *fanwatch.EnrichedEvent) error {
		return nil
	})

	multi := fanwatch.MultiHandler{h1, h2}
	err := multi.Handle(context.Background(), testutil.NewEnrichedEvent().Build())

	if !errors.Is(err, sentinel) {
		t.Errorf("MultiHandler error chain broken: errors.Is(err, sentinel) = false; got %v", err)
	}
}

// TestOverlayEnricher_SetsOverlay_OnlyWhenPathInside verifies that Overlay is
// nil for external paths — the core false-positive fix (Bug 6).
func TestOverlayEnricher_SetsOverlay_OnlyWhenPathInside(t *testing.T) {
	overlay := fanwatch.NewOverlayInfo("/merged", "/upper", "/work", []string{"/lower0"})
	enricher := fanwatch.NewOverlayEnricher(overlay)
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
	tr := fanwatch.StaticAttrTransformer(attrs)

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

	t1 := fanwatch.TransformerFunc(func(_ context.Context, _ *fanwatch.EnrichedEvent) error { return e1 })
	t2 := fanwatch.TransformerFunc(func(_ context.Context, _ *fanwatch.EnrichedEvent) error { return e2 })

	chain := fanwatch.ChainTransformer{t1, t2}
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
	f := fanwatch.FreshnessFilter(100 * time.Millisecond)
	if f == nil {
		t.Error("FreshnessFilter returned nil")
	}
}
