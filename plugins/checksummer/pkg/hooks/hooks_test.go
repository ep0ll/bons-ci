// pkg/hooks/hooks_test.go
package hooks_test

import (
	"context"
	"errors"
	"sync"
	"testing"

	"github.com/bons/bons-ci/plugins/checksummer/pkg/hooks"
)

func TestPriorityOrdering(t *testing.T) {
	reg := hooks.NewRegistry[hooks.HashPayload]()
	var order []string
	var mu sync.Mutex
	record := func(name string) hooks.Handler[hooks.HashPayload] {
		return func(_ context.Context, _ hooks.HashPayload) error {
			mu.Lock()
			order = append(order, name)
			mu.Unlock()
			return nil
		}
	}
	reg.Register(hooks.NewHook("c", hooks.PriorityLast, record("c")))
	reg.Register(hooks.NewHook("a", hooks.PriorityFirst, record("a")))
	reg.Register(hooks.NewHook("b", hooks.PriorityNormal, record("b")))

	_ = reg.Execute(context.Background(), hooks.HashPayload{}, hooks.StopOnError)

	if len(order) != 3 || order[0] != "a" || order[1] != "b" || order[2] != "c" {
		t.Errorf("wrong order: %v", order)
	}
}

func TestStopOnError(t *testing.T) {
	reg := hooks.NewRegistry[hooks.HashPayload]()
	called := 0
	reg.Register(hooks.NewHook("a", hooks.PriorityFirst, func(_ context.Context, _ hooks.HashPayload) error {
		called++
		return errors.New("fail")
	}))
	reg.Register(hooks.NewHook("b", hooks.PriorityLast, func(_ context.Context, _ hooks.HashPayload) error {
		called++
		return nil
	}))
	err := reg.Execute(context.Background(), hooks.HashPayload{}, hooks.StopOnError)
	if err == nil {
		t.Fatal("expected error")
	}
	if called != 1 {
		t.Errorf("expected 1 call, got %d", called)
	}
}

func TestContinueOnError(t *testing.T) {
	reg := hooks.NewRegistry[hooks.HashPayload]()
	called := 0
	for _, name := range []string{"a", "b", "c"} {
		n := name
		reg.Register(hooks.NewHook(n, hooks.PriorityNormal, func(_ context.Context, _ hooks.HashPayload) error {
			called++
			return errors.New("err " + n)
		}))
	}
	err := reg.Execute(context.Background(), hooks.HashPayload{}, hooks.ContinueOnError)
	if err == nil {
		t.Fatal("expected error")
	}
	if called != 3 {
		t.Errorf("all 3 hooks should run, got %d", called)
	}
	var me *hooks.MultiError
	if !errors.As(err, &me) {
		t.Errorf("expected MultiError, got %T", err)
	}
	if len(me.Errors) != 3 {
		t.Errorf("expected 3 errors, got %d", len(me.Errors))
	}
}

func TestIgnoreErrors(t *testing.T) {
	reg := hooks.NewRegistry[hooks.HashPayload]()
	reg.Register(hooks.NewHook("a", hooks.PriorityNormal, func(_ context.Context, _ hooks.HashPayload) error {
		return errors.New("ignored")
	}))
	err := reg.Execute(context.Background(), hooks.HashPayload{}, hooks.IgnoreErrors)
	if err != nil {
		t.Errorf("expected nil, got %v", err)
	}
}

func TestDisableHook(t *testing.T) {
	reg := hooks.NewRegistry[hooks.HashPayload]()
	fired := false
	reg.Register(hooks.NewHook("x", hooks.PriorityNormal, func(_ context.Context, _ hooks.HashPayload) error {
		fired = true
		return nil
	}))
	reg.SetEnabled("x", false)
	_ = reg.Execute(context.Background(), hooks.HashPayload{}, hooks.StopOnError)
	if fired {
		t.Error("disabled hook must not fire")
	}
	reg.SetEnabled("x", true)
	_ = reg.Execute(context.Background(), hooks.HashPayload{}, hooks.StopOnError)
	if !fired {
		t.Error("re-enabled hook must fire")
	}
}

func TestUnregister(t *testing.T) {
	reg := hooks.NewRegistry[hooks.HashPayload]()
	fired := false
	reg.Register(hooks.NewHook("del", hooks.PriorityNormal, func(_ context.Context, _ hooks.HashPayload) error {
		fired = true
		return nil
	}))
	if !reg.Unregister("del") {
		t.Fatal("expected Unregister to return true")
	}
	_ = reg.Execute(context.Background(), hooks.HashPayload{}, hooks.StopOnError)
	if fired {
		t.Error("unregistered hook must not fire")
	}
}

func TestReplace(t *testing.T) {
	reg := hooks.NewRegistry[hooks.HashPayload]()
	calls := 0
	reg.Register(hooks.NewHook("h", hooks.PriorityNormal, func(_ context.Context, _ hooks.HashPayload) error {
		calls++
		return nil
	}))
	// Replace with a new handler.
	reg.Register(hooks.NewHook("h", hooks.PriorityNormal, func(_ context.Context, _ hooks.HashPayload) error {
		calls += 10
		return nil
	}))
	_ = reg.Execute(context.Background(), hooks.HashPayload{}, hooks.StopOnError)
	if calls != 10 {
		t.Errorf("expected replaced hook, got calls=%d", calls)
	}
	if reg.Len() != 1 {
		t.Errorf("expected 1 hook after replace, got %d", reg.Len())
	}
}

func TestContextCancellation(t *testing.T) {
	reg := hooks.NewRegistry[hooks.HashPayload]()
	reg.Register(hooks.NewHook("slow", hooks.PriorityNormal, func(ctx context.Context, _ hooks.HashPayload) error {
		return nil
	}))
	ctx, cancel := context.WithCancel(context.Background())
	cancel() // already cancelled
	err := reg.Execute(ctx, hooks.HashPayload{}, hooks.StopOnError)
	if err == nil {
		t.Error("expected context error")
	}
}

func TestConcurrentRegisterExecute(t *testing.T) {
	reg := hooks.NewRegistry[hooks.HashPayload]()
	var wg sync.WaitGroup
	for i := 0; i < 50; i++ {
		i := i
		wg.Add(2)
		go func() {
			defer wg.Done()
			reg.Register(hooks.NewHook(itoa(i), hooks.PriorityNormal,
				func(_ context.Context, _ hooks.HashPayload) error { return nil }))
		}()
		go func() {
			defer wg.Done()
			_ = reg.Execute(context.Background(), hooks.HashPayload{}, hooks.ContinueOnError)
		}()
	}
	wg.Wait()
}

func TestNilHandler(t *testing.T) {
	reg := hooks.NewRegistry[hooks.HashPayload]()
	// nil handler is a valid no-op placeholder
	reg.Register(hooks.NewHook[hooks.HashPayload]("noop", hooks.PriorityNormal, nil))
	err := reg.Execute(context.Background(), hooks.HashPayload{}, hooks.StopOnError)
	if err != nil {
		t.Errorf("nil handler hook should be skipped: %v", err)
	}
}

func itoa(n int) string {
	if n == 0 {
		return "0"
	}
	var buf [20]byte
	pos := len(buf)
	for n > 0 {
		pos--
		buf[pos] = byte(n%10) + '0'
		n /= 10
	}
	return string(buf[pos:])
}
