package runcexecutor

import (
	"sync"

	"github.com/pkg/errors"
)

// ─── ContainerEntry ───────────────────────────────────────────────────────────

// containerEntry is an in-flight container tracked by the registry.
// done is closed exactly once when the container exits; the final error
// (nil for clean exit) is sent to it before it is closed.
type containerEntry struct {
	done chan error
}

// ─── ContainerRegistry ───────────────────────────────────────────────────────

// ContainerRegistry provides thread-safe tracking of containers that are
// currently running (or being started) by this executor.
//
// It serves two roles:
//  1. Exec() polls the registry to verify that its target container is alive
//     before attempting to attach.
//  2. The done channel in each entry lets Exec() block until the container
//     starts instead of returning an immediate "not running" error, and
//     propagates exit errors back to any Exec calls that are still waiting.
type ContainerRegistry struct {
	mu      sync.Mutex
	entries map[string]*containerEntry
}

// NewContainerRegistry allocates an empty registry.
func NewContainerRegistry() *ContainerRegistry {
	return &ContainerRegistry{
		entries: make(map[string]*containerEntry),
	}
}

// Register creates a new entry for id and returns a notify function.
//
// The caller MUST call notify(err) exactly once when the container exits:
//   - err == nil   → clean exit
//   - err != nil   → the container exited with an error
//
// An error is returned if id is already registered.
func (r *ContainerRegistry) Register(id string) (notify func(error), err error) {
	r.mu.Lock()
	defer r.mu.Unlock()

	if _, exists := r.entries[id]; exists {
		return nil, errors.Errorf("container %s is already registered", id)
	}

	entry := &containerEntry{done: make(chan error, 1)}
	r.entries[id] = entry

	// notify is called by the Run goroutine when the container exits.
	// It sends the final error into the buffered channel, then closes it
	// so that any waiting Exec goroutines unblock, and finally removes
	// the entry from the registry.
	notify = func(exitErr error) {
		entry.done <- exitErr
		close(entry.done)

		r.mu.Lock()
		delete(r.entries, id)
		r.mu.Unlock()
	}
	return notify, nil
}

// Get returns the done channel for a registered container.
// The second return value is false when the container is unknown.
//
// The returned channel is read-only; callers must not send to it.
func (r *ContainerRegistry) Get(id string) (<-chan error, bool) {
	r.mu.Lock()
	defer r.mu.Unlock()

	if e, ok := r.entries[id]; ok {
		return e.done, true
	}
	return nil, false
}
