package resolver

import "github.com/bons/bons-ci/pkg/sbomkit/core/event"

// BusInjector is the exported interface that allows the client package to
// back-fill the engine's event bus into a resolver after the engine is built.
// This breaks the construction cycle: resolvers need a bus reference, but the
// bus is owned by the engine which is built after the resolvers.
//
// The interface must be exported because Go's interface satisfaction for
// unexported methods is restricted to the defining package — a type in package
// resolver cannot satisfy an interface with an unexported method defined in
// package client.
type BusInjector interface {
	SetBus(b *event.Bus)
}

// SetBus implements BusInjector for ImageResolver.
func (r *ImageResolver) SetBus(b *event.Bus) {
	if b == nil {
		return
	}
	r.bus = b
}

// SetBus implements BusInjector for FilesystemResolver.
func (r *FilesystemResolver) SetBus(b *event.Bus) {
	if b == nil {
		return
	}
	r.bus = b
}

// Compile-time interface satisfaction checks.
var _ BusInjector = (*ImageResolver)(nil)
var _ BusInjector = (*FilesystemResolver)(nil)
