package differ

import "context"

// ─────────────────────────────────────────────────────────────────────────────
// ExclusiveHandler
// ─────────────────────────────────────────────────────────────────────────────

// ExclusiveHandler processes entries that exist only in the lower directory.
//
// Implementations receive [ExclusivePath] values from the exclusive stream.
// Collapsed directory entries (Collapsed==true) represent an entire subtree
// that the handler should treat as a single atomic unit — e.g. one
// os.RemoveAll call rather than per-file operations.
//
// All implementations must be safe for concurrent use: the [Engine] may
// invoke HandleExclusive from multiple goroutines simultaneously.
type ExclusiveHandler interface {
	// HandleExclusive is called once per exclusive path.
	// Returning a non-nil error causes the [Engine] to collect the error and
	// (depending on engine configuration) optionally abort the pipeline.
	HandleExclusive(ctx context.Context, ep ExclusivePath) error
}

// ─────────────────────────────────────────────────────────────────────────────
// CommonHandler
// ─────────────────────────────────────────────────────────────────────────────

// CommonHandler processes entries that exist in both lower and upper.
//
// When called, cp.HashEqual is already populated (for regular files and
// symlinks). The handler inspects the equality result to decide what action to
// take — e.g. delete from merged if content is identical to lower, overwrite
// if changed, or skip.
//
// All implementations must be safe for concurrent use.
type CommonHandler interface {
	// HandleCommon is called once per common path.
	HandleCommon(ctx context.Context, cp CommonPath) error
}

// ─────────────────────────────────────────────────────────────────────────────
// ExclusiveHandlerFunc / CommonHandlerFunc — adapter types
// ─────────────────────────────────────────────────────────────────────────────

// ExclusiveHandlerFunc is a function that implements [ExclusiveHandler].
// It allows plain functions and closures to satisfy the interface without a
// named struct type.
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
// NoopExclusiveHandler / NoopCommonHandler
// ─────────────────────────────────────────────────────────────────────────────

// NoopExclusiveHandler silently discards all exclusive paths.
// Useful as a placeholder or in unit tests that only care about the common stream.
type NoopExclusiveHandler struct{}

// HandleExclusive implements [ExclusiveHandler].
func (NoopExclusiveHandler) HandleExclusive(_ context.Context, _ ExclusivePath) error {
	return nil
}

// NoopCommonHandler silently discards all common paths.
type NoopCommonHandler struct{}

// HandleCommon implements [CommonHandler].
func (NoopCommonHandler) HandleCommon(_ context.Context, _ CommonPath) error {
	return nil
}

// ─────────────────────────────────────────────────────────────────────────────
// ChainExclusiveHandler / ChainCommonHandler — composite handlers
// ─────────────────────────────────────────────────────────────────────────────

// ChainExclusiveHandler invokes a sequence of [ExclusiveHandler] implementations
// in order. If any handler returns an error, the chain stops and that error is
// returned. Use [MultiExclusiveHandler] for a fan-out strategy that continues
// despite individual errors.
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

// ChainCommonHandler invokes a sequence of [CommonHandler] implementations in
// order, stopping on the first error.
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
// MultiExclusiveHandler / MultiCommonHandler — fan-out handlers
// ─────────────────────────────────────────────────────────────────────────────

// MultiExclusiveHandler fans out each exclusive path to all registered handlers,
// collecting all errors via errors.Join. Unlike [ChainExclusiveHandler], every
// handler is always invoked even when earlier handlers fail.
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

// MultiCommonHandler fans out each common path to all registered handlers,
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
// PredicateExclusiveHandler / PredicateCommonHandler — conditional routing
// ─────────────────────────────────────────────────────────────────────────────

// ExclusivePredicate is a function that decides whether an [ExclusiveHandler]
// should be invoked for the given path. It must not perform I/O or block.
type ExclusivePredicate func(ep ExclusivePath) bool

// PredicateExclusiveHandler wraps an [ExclusiveHandler] and only delegates to
// it when the predicate returns true. This is the preferred mechanism for
// kind-based routing (e.g. handle only dirs, only files) without baking
// conditional logic into concrete handler implementations.
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

// CommonPredicate is a function that gates a [CommonHandler].
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
// Predicate constructors — common routing decisions
// ─────────────────────────────────────────────────────────────────────────────

// OnlyCollapsed is a predicate that matches only collapsed exclusive dirs.
func OnlyCollapsed() ExclusivePredicate {
	return func(ep ExclusivePath) bool { return ep.Collapsed }
}

// OnlyKind returns a predicate that matches only entries of the given kind.
func OnlyKind(k PathKind) ExclusivePredicate {
	return func(ep ExclusivePath) bool { return ep.Kind == k }
}

// OnlyChanged is a predicate for common paths where content has changed.
// It returns false for paths whose HashEqual is nil (comparison not performed,
// e.g. directories) or where the hash comparison confirmed equality.
func OnlyChanged() CommonPredicate {
	return func(cp CommonPath) bool {
		eq, checked := cp.IsContentEqual()
		return checked && !eq
	}
}

// OnlyUnchanged is the inverse of [OnlyChanged]: matches paths where hash
// comparison confirmed equality.
func OnlyUnchanged() CommonPredicate {
	return func(cp CommonPath) bool {
		eq, checked := cp.IsContentEqual()
		return checked && eq
	}
}

// OnlyTypeMismatched matches common paths where lower and upper have different
// entry types (e.g. lower=dir, upper=file), following BuildKit overlay semantics.
func OnlyTypeMismatched() CommonPredicate {
	return func(cp CommonPath) bool { return cp.TypeMismatch() }
}
