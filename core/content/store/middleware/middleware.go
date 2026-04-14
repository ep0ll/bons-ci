// Package middleware provides composable decorators for content.Store.
//
// Middlewares follow the standard decorator pattern: each wraps a store and
// returns a new store. Multiple middlewares are composed via Chain.
//
// Example:
//
//	store := middleware.Chain(
//	    base,
//	    middleware.Observable(bus, "my-store"),
//	    middleware.ReadOnly(),
//	)
package middleware

import "github.com/containerd/containerd/v2/core/content"

// Middleware is a function that wraps a content.Store with additional behaviour.
// Middlewares are applied right-to-left by Chain (last middleware wraps first).
type Middleware func(next content.Store) content.Store

// Chain applies each middleware to base in order (first middleware is outermost).
// That is, Chain(base, A, B, C) produces A(B(C(base))).
//
// If no middlewares are provided, base is returned unchanged.
func Chain(base content.Store, middlewares ...Middleware) content.Store {
	// Apply in reverse so the first middleware in the slice ends up outermost.
	for i := len(middlewares) - 1; i >= 0; i-- {
		base = middlewares[i](base)
	}
	return base
}
