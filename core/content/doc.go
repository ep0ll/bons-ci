// Package content provides a composable, pluggable content store abstraction.
//
// # Architecture
//
// The package is organized around three concerns:
//
//   - [store/composite] — Composite stores that combine multiple backends:
//
//   - [store/composite/fallback] — Tries a primary store, falls back to secondary
//
//   - [store/composite/fanout]   — Fans out all operations to N stores in parallel
//
//   - [store/composite/split]    — Reads from one store, writes to N others
//
//   - [writer] — Writer implementations:
//
//   - [writer/fanout]    — Broadcasts writes to N underlying writers
//
//   - [writer/resilient] — Best-effort writer that absorbs secondary failures
//
//   - [store/middleware] — Pluggable store decorators:
//
//   - Chain           — Compose multiple middlewares onto any store
//
//   - Observable      — Emit lifecycle events via an [event.Bus]
//
//   - [event] — Reactive event bus for store lifecycle notifications
//
//   - [store/local]  — Local filesystem-backed store adapter
//
//   - [store/noop]   — No-op implementations for testing
//
//   - [testutil]     — Mock store and writer for unit tests
//
// # Composition Example
//
//	primary := local.NewStore("/var/lib/content")
//	secondary := registryStore            // some remote content.Store
//
//	bus := event.NewBus()
//	bus.Subscribe(func(_ context.Context, e event.Event) {
//	    log.Printf("store event: %s err=%v", e.Kind, e.Err)
//	})
//
//	store := middleware.Chain(
//	    fallback.New(primary, secondary),
//	    middleware.Observable(bus, "primary"),
//	)
package content
