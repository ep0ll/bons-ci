//go:build linux

package filter_test

import (
	"context"
	"sync"
	"testing"

	"sync/atomic"

	"github.com/bons/bons-ci/plugins/checksummer/pkg/filter"
	"github.com/bons/bons-ci/plugins/checksummer/pkg/hooks"
)

// ─────────────────────────── helpers ─────────────────────────────────────────

func evt(path string, pid int32, mask uint64) hooks.EventPayload {
	return hooks.EventPayload{Path: path, Pid: pid, Mask: mask}
}

func pass(t *testing.T, f filter.Filter, e hooks.EventPayload) {
	t.Helper()
	if err := f.Evaluate(context.Background(), e); err != nil {
		t.Errorf("expected pass for %+v, got %v", e, err)
	}
}

func skip(t *testing.T, f filter.Filter, e hooks.EventPayload) {
	t.Helper()
	if err := f.Evaluate(context.Background(), e); err == nil {
		t.Errorf("expected skip for %+v, but got pass", e)
	}
}

// ─────────────────────────── NotPaths ────────────────────────────────────────

func TestNotPaths(t *testing.T) {
	f := filter.NotPaths("/proc", "/sys", "/dev")
	pass(t, f, evt("/usr/lib/libssl.so", 100, 0x20))
	pass(t, f, evt("/var/lib/containerd/rootfs/lib/libc.so", 101, 0x20))
	skip(t, f, evt("/proc/self/maps", 100, 0x20))
	skip(t, f, evt("/sys/kernel/debug", 100, 0x20))
	skip(t, f, evt("/dev/null", 100, 0x20))
}

func TestOnlyPaths(t *testing.T) {
	f := filter.OnlyPaths("/overlay", "/run/containerd")
	pass(t, f, evt("/overlay/merged/usr/lib/x.so", 1, 0x20))
	pass(t, f, evt("/run/containerd/rootfs/bin/sh", 2, 0x20))
	skip(t, f, evt("/tmp/random.bin", 3, 0x20))
}

// ─────────────────────────── Extensions ──────────────────────────────────────

func TestExtensions(t *testing.T) {
	f := filter.Extensions(".so", ".py", ".rb")
	pass(t, f, evt("/usr/lib/libssl.so", 1, 0))
	pass(t, f, evt("/usr/local/lib/python3.11/subprocess.py", 1, 0))
	skip(t, f, evt("/usr/bin/bash", 1, 0))
	skip(t, f, evt("/etc/passwd", 1, 0))
}

func TestExtensionsCaseInsensitive(t *testing.T) {
	f := filter.Extensions(".SO", ".PY")
	pass(t, f, evt("/lib/libx.so", 1, 0))
	pass(t, f, evt("/scripts/run.py", 1, 0))
}

func TestNotExtensions(t *testing.T) {
	f := filter.NotExtensions(".log", ".tmp")
	pass(t, f, evt("/usr/lib/libssl.so", 1, 0))
	skip(t, f, evt("/var/log/syslog.log", 1, 0))
	skip(t, f, evt("/tmp/staging.tmp", 1, 0))
}

// ─────────────────────────── GlobMatch ───────────────────────────────────────

func TestGlobMatch(t *testing.T) {
	f := filter.GlobMatch("*.so", "*.so.*", "lib*.a")
	pass(t, f, evt("/usr/lib/libssl.so", 1, 0))
	pass(t, f, evt("/usr/lib/libssl.so.3", 1, 0))
	pass(t, f, evt("/usr/lib/libm.a", 1, 0))
	skip(t, f, evt("/usr/bin/python3", 1, 0))
	skip(t, f, evt("/etc/hosts", 1, 0))
}

// ─────────────────────────── PID filters ─────────────────────────────────────

func TestNotPID(t *testing.T) {
	f := filter.NotPID(1, 2)
	pass(t, f, evt("/lib/x.so", 100, 0))
	skip(t, f, evt("/lib/x.so", 1, 0))
	skip(t, f, evt("/lib/x.so", 2, 0))
}

func TestOnlyPID(t *testing.T) {
	f := filter.OnlyPID(100, 200)
	pass(t, f, evt("/x", 100, 0))
	pass(t, f, evt("/x", 200, 0))
	skip(t, f, evt("/x", 50, 0))
}

// ─────────────────────────── Mask filters ────────────────────────────────────

func TestMaskContains(t *testing.T) {
	const FAN_OPEN_EXEC = uint64(0x1000)
	f := filter.MaskContains(FAN_OPEN_EXEC)
	pass(t, f, evt("/bin/sh", 1, FAN_OPEN_EXEC|0x0020))
	skip(t, f, evt("/bin/sh", 1, 0x0020)) // open but not exec
}

func TestMaskExcludes(t *testing.T) {
	const FAN_ACCESS = uint64(0x0001)
	f := filter.MaskExcludes(FAN_ACCESS)
	pass(t, f, evt("/x", 1, 0x0020)) // open only
	skip(t, f, evt("/x", 1, FAN_ACCESS|0x0020))
}

// ─────────────────────────── Combinators ─────────────────────────────────────

func TestAnd(t *testing.T) {
	f := filter.And(
		filter.OnlyPaths("/lib"),
		filter.Extensions(".so"),
	)
	pass(t, f, evt("/lib/libssl.so", 1, 0))
	skip(t, f, evt("/lib/libssl.a", 1, 0))  // wrong ext
	skip(t, f, evt("/bin/libssl.so", 1, 0)) // wrong path
}

func TestOr(t *testing.T) {
	f := filter.Or(
		filter.Extensions(".so"),
		filter.Extensions(".py"),
	)
	pass(t, f, evt("/lib/x.so", 1, 0))
	pass(t, f, evt("/lib/x.py", 1, 0))
	skip(t, f, evt("/lib/x.rb", 1, 0))
}

func TestNot(t *testing.T) {
	f := filter.Not(filter.NotPaths("/proc"))
	// Not(NotPaths("/proc")) = OnlyPaths("/proc")
	pass(t, f, evt("/proc/self/maps", 1, 0))
	skip(t, f, evt("/usr/lib/x.so", 1, 0))
}

func TestNestedCombinators(t *testing.T) {
	f := filter.And(
		filter.NotPaths("/proc", "/sys"),
		filter.Or(
			filter.Extensions(".so"),
			filter.GlobMatch("python*", "ruby*"),
		),
		filter.NotPID(1),
	)

	pass(t, f, evt("/usr/lib/libssl.so", 100, 0))
	pass(t, f, evt("/usr/bin/python3", 100, 0))
	skip(t, f, evt("/proc/maps", 100, 0))       // excluded path
	skip(t, f, evt("/usr/lib/libssl.so", 1, 0)) // excluded PID
	skip(t, f, evt("/usr/bin/bash", 100, 0))    // wrong ext & name
}

// ─────────────────────────── Sampler ─────────────────────────────────────────

func TestSamplerEvery1(t *testing.T) {
	s := filter.NewSampler(1)
	for i := 0; i < 10; i++ {
		if err := s.Evaluate(context.Background(), evt("/x", 1, 0)); err != nil {
			t.Errorf("sampler(1): call %d: expected pass, got %v", i, err)
		}
	}
}

func TestSamplerEveryN(t *testing.T) {
	s := filter.NewSampler(3)
	passed := 0
	for i := 0; i < 30; i++ {
		if err := s.Evaluate(context.Background(), evt("/x", 1, 0)); err == nil {
			passed++
		}
	}
	if passed != 10 {
		t.Errorf("sampler(3): want 10 passes in 30 calls, got %d", passed)
	}
}

func TestSamplerConcurrent(t *testing.T) {
	s := filter.NewSampler(2)
	var wg sync.WaitGroup
	for i := 0; i < 200; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			_ = s.Evaluate(context.Background(), evt("/x", 1, 0))
		}()
	}
	wg.Wait()
}

// ─────────────────────────── SeenPaths ───────────────────────────────────────

func TestSeenPathsDedup(t *testing.T) {
	sp := filter.NewSeenPaths()
	pass(t, sp, evt("/lib/libssl.so", 1, 0)) // first time
	skip(t, sp, evt("/lib/libssl.so", 1, 0)) // duplicate
	pass(t, sp, evt("/lib/libm.so", 1, 0))   // different path
	skip(t, sp, evt("/lib/libm.so", 2, 0))   // same path, different pid
}

func TestSeenPathsReset(t *testing.T) {
	sp := filter.NewSeenPaths()
	pass(t, sp, evt("/lib/x.so", 1, 0))
	skip(t, sp, evt("/lib/x.so", 1, 0))
	sp.Reset()
	pass(t, sp, evt("/lib/x.so", 1, 0)) // should pass again after reset
}

func TestSeenPathsConcurrent(t *testing.T) {
	sp := filter.NewSeenPaths()
	const paths = 100
	var passCount, skipCount int64
	var wg sync.WaitGroup

	for i := 0; i < paths*3; i++ {
		path := "/lib/lib" + itoa(i%paths) + ".so"
		wg.Add(1)
		go func(p string) {
			defer wg.Done()
			if err := sp.Evaluate(context.Background(), evt(p, 1, 0)); err == nil {
				increment(&passCount)
			} else {
				increment(&skipCount)
			}
		}(path)
	}
	wg.Wait()

	if passCount != paths {
		t.Errorf("expected %d unique paths, got %d passes", paths, passCount)
	}
	if passCount+skipCount != paths*3 {
		t.Errorf("total events mismatch: %d+%d != %d", passCount, skipCount, paths*3)
	}
}

// ─────────────────────────── Hook adapter ────────────────────────────────────

func TestHookAdapter(t *testing.T) {
	f := filter.NotPaths("/proc")
	hookFn := filter.Hook(f)

	if err := hookFn(context.Background(), evt("/usr/lib/x.so", 1, 0)); err != nil {
		t.Errorf("hook: expected pass, got %v", err)
	}
	if err := hookFn(context.Background(), evt("/proc/maps", 1, 0)); err == nil {
		t.Error("hook: expected skip")
	}
}

func TestFilterFuncHook(t *testing.T) {
	f := filter.FilterFunc(func(_ context.Context, e hooks.EventPayload) error {
		if e.Pid > 1000 {
			return filter.ErrSkip
		}
		return nil
	})
	if err := f.Hook()(context.Background(), evt("/x", 500, 0)); err != nil {
		t.Error("expected pass for pid 500")
	}
	if err := f.Hook()(context.Background(), evt("/x", 5000, 0)); err == nil {
		t.Error("expected skip for pid 5000")
	}
}

// ─────────────────────────── helpers ─────────────────────────────────────────

func increment(n *int64) { atomic.AddInt64(n, 1) }

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
