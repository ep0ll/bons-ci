package dirsync

import "context"

// ─────────────────────────────────────────────────────────────────────────────
// Core handler interfaces
// ─────────────────────────────────────────────────────────────────────────────

// ExclusiveHandler processes entries that exist only in the lower directory.
//
// Implementations receive [ExclusivePath] values from the exclusive stream.
// Collapsed directory entries (Collapsed == true) represent an entire subtree
// that the handler should treat as one atomic unit — e.g. one os.RemoveAll
// call rather than per-file operations.
//
// All implementations must be safe for concurrent use: the [Pipeline] may
// call HandleExclusive from multiple goroutines simultaneously.
type ExclusiveHandler interface {
	HandleExclusive(ctx context.Context, ep ExclusivePath) error
}

// CommonHandler processes entries that exist in both lower and upper.
//
// When called, cp.HashEqual is already populated for regular files and
// symlinks. Handlers inspect the equality result to decide what action to
// take — delete from merged if identical, skip if changed, etc.
//
// All implementations must be safe for concurrent use.
type CommonHandler interface {
	HandleCommon(ctx context.Context, cp CommonPath) error
}

// ─────────────────────────────────────────────────────────────────────────────
// Function adapters — turn plain functions into handler interface values
// ─────────────────────────────────────────────────────────────────────────────

// ExclusiveHandlerFunc is a function that implements [ExclusiveHandler].
// It lets closures and package-level functions satisfy the interface without
// needing a named struct type.
type ExclusiveHandlerFunc func(ctx context.Context, ep ExclusivePath) error

// HandleExclusive implements [ExclusiveHandler].
func (f ExclusiveHandlerFunc) HandleExclusive(ctx context.Context, ep ExclusivePath) error {
	return f(ctx, ep)
}

// CommonHandlerFunc is a function that implements [CommonHandler].
type CommonHandlerFunc func(ctx context.Context, cp CommonPath) error

// HandleCommon implements [CommonHandler].
func (f CommonHandlerFunc) HandleCommon(ctx context.Context, cp CommonPath) error {
	return f(ctx, cp)
}

// ─────────────────────────────────────────────────────────────────────────────
// No-ops — silent discard, useful as placeholders
// ─────────────────────────────────────────────────────────────────────────────

// NoopExclusiveHandler silently discards all exclusive paths.
// Useful as a placeholder in [Pipeline] when only the common stream matters,
// or as the handler in an observe-only [Engine].
type NoopExclusiveHandler struct{}

// HandleExclusive implements [ExclusiveHandler].
func (NoopExclusiveHandler) HandleExclusive(_ context.Context, _ ExclusivePath) error { return nil }

// NoopCommonHandler silently discards all common paths.
type NoopCommonHandler struct{}

// HandleCommon implements [CommonHandler].
func (NoopCommonHandler) HandleCommon(_ context.Context, _ CommonPath) error { return nil }

// ─────────────────────────────────────────────────────────────────────────────
// ChainHandler — sequential composition, stops on first error
// ─────────────────────────────────────────────────────────────────────────────

// ChainExclusiveHandler invokes a sequence of [ExclusiveHandler] implementations
// in order. Stops and returns the error from the first handler that fails.
//
// Use [MultiExclusiveHandler] when you need a fan-out that continues even when
// individual handlers fail.
type ChainExclusiveHandler []ExclusiveHandler

// HandleExclusive implements [ExclusiveHandler].
func (c ChainExclusiveHandler) HandleExclusive(ctx context.Context, ep ExclusivePath) error {
	for _, h := range c {
		if err := h.HandleExclusive(ctx, ep); err != nil {
			return err
		}
	}
	return nil
}

// ChainCommonHandler invokes a sequence of [CommonHandler] implementations
// in order, stopping on the first error.
type ChainCommonHandler []CommonHandler

// HandleCommon implements [CommonHandler].
func (c ChainCommonHandler) HandleCommon(ctx context.Context, cp CommonPath) error {
	for _, h := range c {
		if err := h.HandleCommon(ctx, cp); err != nil {
			return err
		}
	}
	return nil
}

// ─────────────────────────────────────────────────────────────────────────────
// MultiHandler — fan-out composition, collects all errors
// ─────────────────────────────────────────────────────────────────────────────

// MultiExclusiveHandler fans each exclusive path out to all registered handlers.
// Unlike [ChainExclusiveHandler], every handler is always invoked even when
// earlier handlers fail. All errors are collected via [errors.Join].
type MultiExclusiveHandler []ExclusiveHandler

// HandleExclusive implements [ExclusiveHandler].
func (m MultiExclusiveHandler) HandleExclusive(ctx context.Context, ep ExclusivePath) error {
	var errs []error
	for _, h := range m {
		if err := h.HandleExclusive(ctx, ep); err != nil {
			errs = append(errs, err)
		}
	}
	return joinErrors(errs)
}

// MultiCommonHandler fans each common path out to all registered handlers,
// collecting all errors.
type MultiCommonHandler []CommonHandler

// HandleCommon implements [CommonHandler].
func (m MultiCommonHandler) HandleCommon(ctx context.Context, cp CommonPath) error {
	var errs []error
	for _, h := range m {
		if err := h.HandleCommon(ctx, cp); err != nil {
			errs = append(errs, err)
		}
	}
	return joinErrors(errs)
}

// ─────────────────────────────────────────────────────────────────────────────
// PredicateHandler — conditional routing
// ─────────────────────────────────────────────────────────────────────────────

// ExclusivePredicate is a pure function that decides whether an
// [ExclusiveHandler] should be invoked for a given path.
// It must not perform I/O, block, or have side effects.
type ExclusivePredicate func(ep ExclusivePath) bool

// PredicateExclusiveHandler wraps an [ExclusiveHandler] and only delegates
// when the predicate returns true. This is the preferred way to route by kind
// (e.g. only dirs, only files) without baking conditional logic into concrete
// handler implementations.
type PredicateExclusiveHandler struct {
	Predicate ExclusivePredicate
	Handler   ExclusiveHandler
}

// HandleExclusive implements [ExclusiveHandler].
func (p PredicateExclusiveHandler) HandleExclusive(ctx context.Context, ep ExclusivePath) error {
	if p.Predicate(ep) {
		return p.Handler.HandleExclusive(ctx, ep)
	}
	return nil
}

// CommonPredicate is a pure function that gates a [CommonHandler].
type CommonPredicate func(cp CommonPath) bool

// PredicateCommonHandler wraps a [CommonHandler] behind a predicate gate.
type PredicateCommonHandler struct {
	Predicate CommonPredicate
	Handler   CommonHandler
}

// HandleCommon implements [CommonHandler].
func (p PredicateCommonHandler) HandleCommon(ctx context.Context, cp CommonPath) error {
	if p.Predicate(cp) {
		return p.Handler.HandleCommon(ctx, cp)
	}
	return nil
}

// ─────────────────────────────────────────────────────────────────────────────
// Built-in predicates — common routing decisions ready to use
// ─────────────────────────────────────────────────────────────────────────────

// OnlyCollapsed returns a predicate that matches only collapsed exclusive
// directory entries (those that represent an entire subtree).
func OnlyCollapsed() ExclusivePredicate {
	return func(ep ExclusivePath) bool { return ep.Collapsed }
}

// OnlyKind returns a predicate that matches only exclusive entries of the
// given [PathKind].
func OnlyKind(k PathKind) ExclusivePredicate {
	return func(ep ExclusivePath) bool { return ep.Kind == k }
}

// OnlyChanged returns a predicate for common paths where content has changed.
// Returns false for paths where HashEqual is nil (comparison not performed,
// e.g. directories) or where the comparison confirmed equality.
func OnlyChanged() CommonPredicate {
	return func(cp CommonPath) bool {
		eq, checked := cp.IsContentEqual()
		return checked && !eq
	}
}

// OnlyUnchanged is the inverse of [OnlyChanged]: matches only paths where
// the hash comparison confirmed byte-for-byte equality.
func OnlyUnchanged() CommonPredicate {
	return func(cp CommonPath) bool {
		eq, checked := cp.IsContentEqual()
		return checked && eq
	}
}

// OnlyTypeMismatched returns a predicate that matches common paths where lower
// and upper have different entry types (e.g. lower=dir, upper=file).
// This follows BuildKit overlay semantics where an upper non-directory
// implicitly removes the lower directory tree.
func OnlyTypeMismatched() CommonPredicate {
	return func(cp CommonPath) bool { return cp.TypeMismatch() }
}
