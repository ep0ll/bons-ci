// Package fanwatch provides a reactive, event-driven fanotify observer for
// Linux overlay filesystems.
//
// # Overview
//
// fanwatch watches a merged overlay directory (as used by Docker, BuildKit,
// containerd, and similar container runtimes) and emits structured events for
// every filesystem access, open, or exec operation that occurs on the merged
// view. Modification events (writes, creates, deletes, renames) are filtered
// out by default so callers observe a read-only view of activity.
//
// # Overlay Filesystem Awareness
//
// Container runtimes compose images from a stack of read-only layers
// (lowerdirs) plus a writable layer (upperdir). The kernel presents a unified
// merged view. fanwatch is aware of this structure:
//
//   - It watches only the merged directory via fanotify.
//   - Each event is enriched with the corresponding [OverlayInfo] that records
//     the lowerdir stack, upperdir, workdir, and merged path.
//   - [OverlayInfo.ResolveLayer] determines which layer a path originates from.
//
// # Pipeline Model
//
//	Watcher ──► chan *RawEvent ──► Pipeline
//	                                  │
//	                   ┌──────────────┼───────────────┐
//	                   ▼              ▼                ▼
//	               Filter        Transformer        Handler
//	           (drop/pass)      (enrich event)   (side effects)
//	                   │              │                │
//	             ReadOnly?      OverlayEnricher   LogHandler
//	             PathPrefix?    ProcessEnricher   CountingHandler
//	             PIDFilter?     FileStatTransform ChannelHandler
//	             ExternalFn?    StaticAttrs       ...
//	                   └──────────────┴───────────────┘
//	                                  │
//	                             Middleware
//	                        (OTEL, Recovery, Logging)
//
// Each stage is independently replaceable and composable. Stages communicate
// through typed Go channels; no stage holds a reference to another.
//
// # Quick Start
//
//	overlay, err := fswatch.OverlayInfoFromMount("/var/lib/docker/overlay2/abc/merged")
//	if err != nil {
//	    log.Fatal(err)
//	}
//
//	w, err := fswatch.NewWatcher(
//	    fswatch.WithOverlay(overlay),
//	    fswatch.WithMask(fswatch.MaskReadOnly),
//	)
//	if err != nil {
//	    log.Fatal(err)
//	}
//	defer w.Close()
//
//	pipeline := fswatch.NewPipeline(
//	    fswatch.WithReadOnlyPipeline(),
//	    fswatch.WithFullEnrichment(overlay),
//	    fswatch.WithHandler(fswatch.LogHandler(slog.Default(), slog.LevelInfo)),
//	)
//
//	ctx, cancel := context.WithCancel(context.Background())
//	defer cancel()
//
//	rawCh, err := w.Watch(ctx)
//	if err != nil {
//	    log.Fatal(err)
//	}
//	result := pipeline.RunSync(ctx, rawCh, func(err error) {
//	    slog.Error("pipeline error", "err", err)
//	})
//	slog.Info("done", "received", result.Received, "handled", result.Handled)
//
// # Containerd Integration
//
// The [snapshot] sub-package provides overlay resolution via the official
// containerd libraries:
//
//	import "github.com/bons/bons-ci/pkg/fswatch/snapshot"
//
//	// From a running mount (fastest):
//	info, err := snapshot.OverlayInfoFromMergedDir("/run/containerd/io.containerd.runtime.v2.task/k8s.io/abc/rootfs")
//
//	// From a Snapshotter.Mounts() result:
//	mounts, _ := snapshotter.Mounts(ctx, snapshotKey)
//	info, err := snapshot.MountsToOverlayInfo(mounts, mergedDir)
//
//	// Enrich every event automatically with caching:
//	enricher := snapshot.NewContainerdEnricher(nil) // nil = live-mount only
//	pipeline := fswatch.NewPipeline(
//	    fswatch.WithTransformer(enricher),
//	    fswatch.WithHandler(myHandler),
//	)
//
// # Build Requirements
//
// fanwatch requires Linux 5.1+ for fanotify filesystem-level marking, and
// CAP_SYS_ADMIN capability (or equivalent in a privileged container). On
// non-Linux platforms the Watcher compiles but [Watcher.Watch] returns
// [ErrNotSupported].
//
// Build with -mod=vendor (see Makefile):
//
//	make build
//	make test
//
// # OTEL Integration
//
// Full OpenTelemetry tracing and metrics are available via [middleware.OTELMiddleware]:
//
//	import "github.com/bons/bons-ci/pkg/fswatch/middleware"
//
//	otelMW, _ := middleware.NewOTEL(tracer, meter)
//	pipeline := fswatch.NewPipeline(
//	    fswatch.WithMiddleware(otelMW),
//	    fswatch.WithHandler(myHandler),
//	)
package fswatch
