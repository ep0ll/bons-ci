package fswatch_test

import (
	"context"
	"fmt"
	"os"
	"time"

	fswatch "github.com/bons/bons-ci/pkg/fswatch"
	"github.com/bons/bons-ci/pkg/fswatch/testutil"
)

// ExampleNewPipeline_readOnlyObserver shows the minimal setup to observe
// read-only filesystem events on a Docker overlay mount.
func ExampleNewPipeline_readOnlyObserver() {
	// In production, replace with:
	//   overlay, err := fswatch.OverlayInfoFromMount("/var/lib/docker/overlay2/abc/merged")
	overlay := fswatch.NewOverlayInfo(
		"/merged", "/upper", "/work",
		[]string{"/lower1", "/lower0"},
	)
	overlay.ID = "my-container-snapshot"

	// Use a counting handler so the example output is deterministic.
	counter := &fswatch.CountingHandler{}

	pipeline := fswatch.NewPipeline(
		fswatch.WithReadOnlyPipeline(),         // filter: only ACCESS/OPEN/EXEC
		fswatch.WithOverlayEnrichment(overlay), // transform: add layer metadata
		fswatch.WithHandler(counter),
		fswatch.WithWorkers(1),
	)

	// Use FakeWatcher in this example (real code uses fswatch.NewWatcher).
	w := testutil.NewFakeWatcher(16)
	w.Send(testutil.NewRawEvent().
		WithOp(fswatch.OpAccess).
		WithPath("/merged/usr/bin/python3").
		WithPID(1234).
		Build())
	w.Close()

	ctx := context.Background()
	ch, _ := w.Watch(ctx)
	result := pipeline.RunSync(ctx, ch, func(err error) {
		fmt.Fprintln(os.Stderr, "pipeline error:", err)
	})

	fmt.Printf("received=%d handled=%d filtered=%d\n",
		result.Received, result.Handled, result.Filtered)
	// Output:
	// received=1 handled=1 filtered=0
}

// ExampleNewPipeline_customFiltersAndTransformers demonstrates composing
// custom filters, external path-allow-lists, and attribute transformers.
func ExampleNewPipeline_customFiltersAndTransformers() {
	// External allow-list (could call an external package).
	allowedPaths := map[string]bool{
		"/merged/usr/lib/libssl.so": true,
		"/merged/app/main":          true,
	}
	externalAllow := fswatch.ExternalFilter(func(path string) bool {
		return allowedPaths[path]
	})

	// Add deployment metadata to every event.
	deploymentAttrs := fswatch.StaticAttrTransformer(map[string]any{
		"datacenter": "us-east-1a",
		"pod":        "worker-7",
	})

	collector := &fswatch.CollectingHandler{}

	pipeline := fswatch.NewPipeline(
		fswatch.WithFilter(fswatch.ReadOnlyFilter()),
		fswatch.WithFilter(externalAllow),
		fswatch.WithTransformer(deploymentAttrs),
		fswatch.WithHandler(collector),
		fswatch.WithWorkers(2),
	)

	w := testutil.NewFakeWatcher(16)
	w.Send(testutil.NewRawEvent().WithPath("/merged/usr/lib/libssl.so").WithOp(fswatch.OpOpen).Build())
	w.Send(testutil.NewRawEvent().WithPath("/merged/proc/kcore").WithOp(fswatch.OpOpen).Build()) // blocked
	w.Send(testutil.NewRawEvent().WithPath("/merged/app/main").WithOp(fswatch.OpOpenExec).Build())
	w.Close()

	ctx := context.Background()
	ch, _ := w.Watch(ctx)
	result := pipeline.RunSync(ctx, ch, nil)

	fmt.Printf("received=%d handled=%d filtered=%d\n",
		result.Received, result.Handled, result.Filtered)
	// Output:
	// received=3 handled=2 filtered=1
}

// ExampleNewPipeline_multipleHandlers shows fanning events out to both a
// log handler and an audit channel simultaneously.
func ExampleNewPipeline_multipleHandlers() {
	channelH, auditCh := fswatch.NewChannelHandler(64)
	counter := &fswatch.CountingHandler{}

	pipeline := fswatch.NewPipeline(
		fswatch.WithReadOnlyPipeline(),
		fswatch.WithHandler(fswatch.MultiHandler{channelH, counter}),
		fswatch.WithWorkers(1),
	)

	w := testutil.NewFakeWatcher(8)
	w.SendMany(testutil.MakeReadOnlyEvents("/merged", 4))
	w.Close()

	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
	defer cancel()

	ch, _ := w.Watch(ctx)
	pipeline.RunSync(ctx, ch, nil)

	snap := counter.Snapshot()
	fmt.Printf("counter.Total=%d channel.Len=%d\n", snap.Total, len(auditCh))
	// Output:
	// counter.Total=4 channel.Len=4
}

// ExampleConditionalTransformer demonstrates applying a transformer only
// to events for files with a specific extension.
func ExampleConditionalTransformer() {
	isSharedLib := func(e *fswatch.EnrichedEvent) bool {
		name := e.Name
		return len(name) > 3 && name[len(name)-3:] == ".so"
	}

	markSharedLib := fswatch.StaticAttrTransformer(map[string]any{
		"file_type": "shared_library",
	})

	conditional := fswatch.ConditionalTransformer{
		Predicate: isSharedLib,
		Inner:     markSharedLib,
	}

	events := []*fswatch.EnrichedEvent{
		testutil.NewEnrichedEvent().WithPath("/merged/lib/libssl.so").Build(),
		testutil.NewEnrichedEvent().WithPath("/merged/app/main").Build(),
	}

	ctx := context.Background()
	for _, e := range events {
		_ = conditional.Transform(ctx, e)
		fmt.Printf("path=%s file_type=%v\n", e.Name, e.Attr("file_type"))
	}
	// Output:
	// path=libssl.so file_type=shared_library
	// path=main file_type=<nil>
}
