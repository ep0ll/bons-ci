package dirsync

import (
	"context"
	"errors"
	"sync"
)

// ─────────────────────────────────────────────────────────────────────────────
// noCopier — go vet copy-protection
// ─────────────────────────────────────────────────────────────────────────────

// noCopier prevents accidental value-copy of types that embed it.
// go vet warns when a struct containing sync.Locker is copied by value.
// Embed noCopier (not sync.Mutex) to get the warning while keeping the actual
// mutex as a separately named, clearly documented field.
type noCopier struct{ sync.Mutex }

// ─────────────────────────────────────────────────────────────────────────────
// Error aggregation
// ─────────────────────────────────────────────────────────────────────────────

// joinErrors combines a slice of errors using [errors.Join].
// Returns nil when errs is empty or contains only nil values.
// Every individual error is preserved and queryable via errors.Is / errors.As.
func joinErrors(errs []error) error {
	if len(errs) == 0 {
		return nil
	}
	return errors.Join(errs...)
}

// ─────────────────────────────────────────────────────────────────────────────
// Context helpers
// ─────────────────────────────────────────────────────────────────────────────

// isContextErr reports whether err is context cancellation or deadline exceeded.
// Used throughout the pipeline to distinguish expected shutdown from real failures.
func isContextErr(err error) bool {
	return errors.Is(err, context.Canceled) || errors.Is(err, context.DeadlineExceeded)
}

// sendErr delivers err to errCh without blocking the caller.
// When ctx is already done the error is dropped — the pipeline is shutting down.
func sendErr(ctx context.Context, errCh chan<- error, err error) {
	select {
	case errCh <- err:
	case <-ctx.Done():
	}
}
