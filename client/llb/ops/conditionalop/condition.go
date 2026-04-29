package conditionalop

import (
	"context"
	"os"
	"runtime"
)

// ─────────────────────────────────────────────────────────────────────────────
// Condition interface
// ─────────────────────────────────────────────────────────────────────────────

// Condition evaluates a predicate and returns a string key that selects a
// branch. Conditions are evaluated at marshal time, making the selection
// static in the emitted definition.
type Condition interface {
	Evaluate(ctx context.Context) (string, error)
}

// ConditionFunc adapts a plain function to the Condition interface.
type ConditionFunc func(ctx context.Context) (string, error)

// Evaluate implements Condition.
func (f ConditionFunc) Evaluate(ctx context.Context) (string, error) { return f(ctx) }

// ─────────────────────────────────────────────────────────────────────────────
// Built-in conditions
// ─────────────────────────────────────────────────────────────────────────────

// EnvCondition reads an environment variable and returns its value as the key.
// If the variable is not set, returns the empty string.
func EnvCondition(name string) Condition {
	return ConditionFunc(func(_ context.Context) (string, error) {
		return os.Getenv(name), nil
	})
}

// LiteralCondition always returns the given key. Useful for testing and
// forced-branch scenarios.
func LiteralCondition(key string) Condition {
	return ConditionFunc(func(_ context.Context) (string, error) {
		return key, nil
	})
}

// PlatformCondition returns the current GOOS as the key. This is evaluated
// at marshal time on the client, not on the build worker.
func PlatformCondition() Condition {
	return ConditionFunc(func(_ context.Context) (string, error) {
		return runtime.GOOS, nil
	})
}

// ArchCondition returns the current GOARCH as the key.
func ArchCondition() Condition {
	return ConditionFunc(func(_ context.Context) (string, error) {
		return runtime.GOARCH, nil
	})
}

// BoolCondition returns "true" or "false" based on a predicate function.
func BoolCondition(predicate func(ctx context.Context) (bool, error)) Condition {
	return ConditionFunc(func(ctx context.Context) (string, error) {
		v, err := predicate(ctx)
		if err != nil {
			return "", err
		}
		if v {
			return "true", nil
		}
		return "false", nil
	})
}

// NotCondition negates a boolean condition result.
func NotCondition(cond Condition) Condition {
	return ConditionFunc(func(ctx context.Context) (string, error) {
		v, err := cond.Evaluate(ctx)
		if err != nil {
			return "", err
		}
		if v == "true" {
			return "false", nil
		}
		return "true", nil
	})
}
