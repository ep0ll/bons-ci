package filekey_test

import (
	"os"
	"sync"
	"testing"

	"github.com/bons/bons-ci/plugins/checksummer/pkg/filekey"
)

// ─────────────────────────── helpers ─────────────────────────────────────────

func writeTempFile(t *testing.T, content []byte) *os.File {
	t.Helper()
	f, err := os.CreateTemp("", "fktest-*.bin")
	if err != nil {
		t.Fatalf("CreateTemp: %v", err)
	}
	t.Cleanup(func() { os.Remove(f.Name()) })
	if _, err := f.Write(content); err != nil {
		t.Fatalf("write: %v", err)
	}
	return f
}

// ─────────────────────────── Key value type ───────────────────────────────────

func TestKeyIsZero(t *testing.T) {
	var k filekey.Key
	if !k.IsZero() {
		t.Error("zero Key should report IsZero()==true")
	}
}

func TestKeyNotZeroAfterResolve(t *testing.T) {
	f := writeTempFile(t, []byte("hello"))
	r := filekey.DefaultResolver
	k, err := r.FromPath(f.Name())
	if err != nil {
		t.Fatalf("FromPath: %v", err)
	}
	if k.IsZero() {
		t.Error("resolved key should not be zero")
	}
}

func TestSameFileSameKey(t *testing.T) {
	f := writeTempFile(t, []byte("data"))
	r := filekey.DefaultResolver
	k1, err := r.FromPath(f.Name())
	if err != nil {
		t.Fatalf("k1: %v", err)
	}
	k2, err := r.FromPath(f.Name())
	if err != nil {
		t.Fatalf("k2: %v", err)
	}
	if !k1.Equal(k2) {
		t.Errorf("same file must have same key: %s vs %s", k1, k2)
	}
}

func TestDifferentFilesDifferentKeys(t *testing.T) {
	f1 := writeTempFile(t, []byte("aaa"))
	f2 := writeTempFile(t, []byte("bbb"))
	r := filekey.DefaultResolver
	k1, _ := r.FromPath(f1.Name())
	k2, _ := r.FromPath(f2.Name())
	if k1.Equal(k2) {
		t.Error("different files must have different keys")
	}
}

func TestSFKeyDeterministic(t *testing.T) {
	f := writeTempFile(t, []byte("x"))
	r := filekey.DefaultResolver
	k, _ := r.FromPath(f.Name())
	s1 := k.SFKey()
	s2 := k.SFKey()
	if s1 != s2 {
		t.Error("SFKey must be deterministic")
	}
	if len(s1) == 0 {
		t.Error("SFKey must be non-empty")
	}
}

func TestSFKeyUniquePerFile(t *testing.T) {
	f1 := writeTempFile(t, []byte("a"))
	f2 := writeTempFile(t, []byte("b"))
	r := filekey.DefaultResolver
	k1, _ := r.FromPath(f1.Name())
	k2, _ := r.FromPath(f2.Name())
	if k1.SFKey() == k2.SFKey() {
		t.Error("different files must produce different SFKey values")
	}
}

func TestKeyString(t *testing.T) {
	f := writeTempFile(t, []byte("z"))
	r := filekey.DefaultResolver
	k, _ := r.FromPath(f.Name())
	s := k.String()
	if len(s) == 0 {
		t.Error("String() must be non-empty")
	}
	t.Logf("key string: %s (source=%d)", s, k.Source)
}

func TestKeyHash(t *testing.T) {
	f := writeTempFile(t, []byte("q"))
	r := filekey.DefaultResolver
	k, _ := r.FromPath(f.Name())
	h1 := k.Hash()
	h2 := k.Hash()
	if h1 != h2 {
		t.Error("Hash() must be deterministic")
	}
}

func TestFromFDSameAsFromPath(t *testing.T) {
	f := writeTempFile(t, []byte("fd-test"))
	r := filekey.DefaultResolver

	kPath, err := r.FromPath(f.Name())
	if err != nil {
		t.Fatalf("FromPath: %v", err)
	}
	kFD, err := r.FromFD(int(f.Fd()))
	if err != nil {
		t.Fatalf("FromFD: %v", err)
	}
	if !kPath.Equal(kFD) {
		t.Errorf("FromPath and FromFD should be equal: %s vs %s", kPath, kFD)
	}
}

func TestSameMethod(t *testing.T) {
	f := writeTempFile(t, []byte("same-test"))
	r := filekey.DefaultResolver
	same, err := r.Same(int(f.Fd()), int(f.Fd()))
	if err != nil {
		t.Fatalf("Same: %v", err)
	}
	if !same {
		t.Error("same fd should report Same()==true")
	}
}

func TestDisableHandlesFallback(t *testing.T) {
	f := writeTempFile(t, []byte("stat-fallback"))
	r := &filekey.Resolver{DisableHandles: true}
	k, err := r.FromPath(f.Name())
	if err != nil {
		t.Fatalf("FromPath with DisableHandles: %v", err)
	}
	if k.IsZero() {
		t.Error("stat fallback should produce non-zero key")
	}
	if k.Source != filekey.SourceStat {
		t.Errorf("want SourceStat, got %d", k.Source)
	}
}

func TestConcurrentFromPath(t *testing.T) {
	f := writeTempFile(t, []byte("concurrent"))
	r := filekey.DefaultResolver
	var wg sync.WaitGroup
	keys := make([]filekey.Key, 100)
	errs := make([]error, 100)
	for i := 0; i < 100; i++ {
		i := i
		wg.Add(1)
		go func() {
			defer wg.Done()
			keys[i], errs[i] = r.FromPath(f.Name())
		}()
	}
	wg.Wait()
	for i, err := range errs {
		if err != nil {
			t.Errorf("[%d] err: %v", i, err)
		}
	}
	for i := 1; i < len(keys); i++ {
		if !keys[0].Equal(keys[i]) {
			t.Errorf("key[0] != key[%d]: %s vs %s", i, keys[0], keys[i])
		}
	}
}
