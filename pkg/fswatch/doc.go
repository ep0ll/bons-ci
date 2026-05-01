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
//   - A [PathResolver] can determine which layer a path originates from.
//
// # Pipeline Model
//
//	Watcher ──► raw events ──► Filter ──► Transformer ──► Handler
//	                                          │
//	                                    (adds OverlayInfo,
//	                                     ProcessInfo, attrs)
//
// Each stage is independently replaceable and composable. Stages communicate
// through typed Go channels; no stage holds a reference to another.
//
// # Quick Start
//
//	overlay, _ := fanwatch.OverlayInfoFromMount("/var/lib/docker/overlay2/abc/merged")
//
//	w, err := fanwatch.NewWatcher(
//	    fanwatch.WithMergedDir("/var/lib/docker/overlay2/abc/merged"),
//	    fanwatch.WithOverlay(overlay),
//	    fanwatch.WithMask(fanwatch.MaskReadOnly),
//	)
//
//	pipeline := fanwatch.NewPipeline(
//	    fanwatch.WithFilter(filter.ReadOnly()),
//	    fanwatch.WithTransformer(transform.OverlayEnricher(overlay)),
//	    fanwatch.WithTransformer(transform.ProcessEnricher()),
//	    fanwatch.WithHandler(fanwatch.LogHandler(slog.Default())),
//	)
//
//	ctx, cancel := context.WithCancel(context.Background())
//	defer cancel()
//
//	rawCh := w.Watch(ctx)
//	pipeline.Run(ctx, rawCh)
//
// # Build Requirements
//
// fanwatch requires Linux 5.1+ for fanotify filesystem-level marking, and
// CAP_SYS_ADMIN capability (or equivalent in a privileged container). On
// non-Linux platforms the Watcher compiles but Watch() returns an error.
//
// # OTEL Integration
//
// Full OpenTelemetry tracing and metrics are available via [middleware.OTEL]:
//
//	pipeline := fanwatch.NewPipeline(
//	    fanwatch.WithMiddleware(middleware.NewOTEL(tracer, meter)),
//	    ...
//	)
package fanwatch
