//go:build linux

package store_test

import (
	"bufio"
	"context"
	"encoding/json"
	"os"
	"sync"
	"testing"
	"time"

	"github.com/bons/bons-ci/plugins/checksummer/pkg/store"
)

// ─────────────────────────── helpers ─────────────────────────────────────────

func rec(key, path string, hash []byte, size int64) store.Record {
	return store.Record{
		Key:  key,
		Path: path,
		Size: size,
	}.WithHash(hash)
}

// ─────────────────────────── MemStore ────────────────────────────────────────

func TestMemStorePutGet(t *testing.T) {
	ms := store.NewMemStore()
	ctx := context.Background()

	r := rec("k1", "/usr/lib/libssl.so", []byte{1, 2, 3}, 12345)
	if err := ms.Put(ctx, r); err != nil {
		t.Fatalf("Put: %v", err)
	}

	got, ok, err := ms.Get(ctx, "k1")
	if err != nil || !ok {
		t.Fatalf("Get: ok=%v err=%v", ok, err)
	}
	if got.Path != r.Path || got.HashHex != r.HashHex {
		t.Errorf("record mismatch: got %+v", got)
	}
}

func TestMemStoreMiss(t *testing.T) {
	ms := store.NewMemStore()
	_, ok, err := ms.Get(context.Background(), "nonexistent")
	if err != nil || ok {
		t.Errorf("expected miss, got ok=%v err=%v", ok, err)
	}
}

func TestMemStoreUpdate(t *testing.T) {
	ms := store.NewMemStore()
	ctx := context.Background()

	ms.Put(ctx, rec("k", "/x", []byte{1}, 100))
	ms.Put(ctx, rec("k", "/x", []byte{2}, 200)) // overwrite

	got, _, _ := ms.Get(ctx, "k")
	if got.Size != 200 {
		t.Errorf("want updated size 200, got %d", got.Size)
	}
}

func TestMemStoreLen(t *testing.T) {
	ms := store.NewMemStore()
	ctx := context.Background()
	if ms.Len() != 0 {
		t.Error("empty store should have Len()==0")
	}
	for i := 0; i < 5; i++ {
		ms.Put(ctx, rec(itoa(i), "/x", []byte{byte(i)}, 0))
	}
	if ms.Len() != 5 {
		t.Errorf("want 5, got %d", ms.Len())
	}
	// Overwrite existing key – Len should not grow.
	ms.Put(ctx, rec("0", "/x", []byte{99}, 0))
	if ms.Len() != 5 {
		t.Errorf("overwrite should not increase Len; want 5, got %d", ms.Len())
	}
}

func TestMemStoreAll(t *testing.T) {
	ms := store.NewMemStore()
	ctx := context.Background()
	for i := 0; i < 10; i++ {
		ms.Put(ctx, rec(itoa(i), "/x"+itoa(i), []byte{byte(i)}, int64(i)))
	}
	all := ms.All()
	if len(all) != 10 {
		t.Errorf("All: want 10, got %d", len(all))
	}
}

func TestMemStoreConcurrent(t *testing.T) {
	ms := store.NewMemStore()
	ctx := context.Background()
	var wg sync.WaitGroup
	for i := 0; i < 500; i++ {
		i := i
		wg.Add(1)
		go func() {
			defer wg.Done()
			k := itoa(i % 50)
			ms.Put(ctx, rec(k, "/x", []byte{byte(i)}, int64(i)))
			_, _, _ = ms.Get(ctx, k)
		}()
	}
	wg.Wait()
}

// ─────────────────────────── FileStore ───────────────────────────────────────

func TestFileStorePutGet(t *testing.T) {
	f, _ := os.CreateTemp("", "store-*.ndjson")
	f.Close()
	t.Cleanup(func() { os.Remove(f.Name()) })

	fs, err := store.NewFileStore(f.Name())
	if err != nil {
		t.Fatalf("NewFileStore: %v", err)
	}
	defer fs.Close()

	ctx := context.Background()
	r := rec("key1", "/usr/lib/libcrypto.so", []byte{0xAB, 0xCD}, 99999)
	if err := fs.Put(ctx, r); err != nil {
		t.Fatalf("Put: %v", err)
	}

	got, ok, err := fs.Get(ctx, "key1")
	if err != nil || !ok {
		t.Fatalf("Get: ok=%v err=%v", ok, err)
	}
	if got.Path != r.Path {
		t.Errorf("path mismatch: got %s", got.Path)
	}
}

func TestFileStoreLen(t *testing.T) {
	f, _ := os.CreateTemp("", "store-*.ndjson")
	f.Close()
	t.Cleanup(func() { os.Remove(f.Name()) })

	fs, _ := store.NewFileStore(f.Name())
	defer fs.Close()

	ctx := context.Background()
	for i := 0; i < 5; i++ {
		fs.Put(ctx, rec(itoa(i), "/x", []byte{byte(i)}, 0))
	}
	if fs.Len() != 5 {
		t.Errorf("want 5, got %d", fs.Len())
	}
}

func TestFileStorePersistence(t *testing.T) {
	f, _ := os.CreateTemp("", "persist-*.ndjson")
	f.Close()
	t.Cleanup(func() { os.Remove(f.Name()) })

	// Write records.
	fs, _ := store.NewFileStore(f.Name())
	ctx := context.Background()
	for i := 0; i < 3; i++ {
		fs.Put(ctx, rec(itoa(i), "/p"+itoa(i), []byte{byte(i)}, int64(i*100)))
	}
	fs.Close()

	// Read back the NDJSON file and verify all 3 records.
	file, _ := os.Open(f.Name())
	defer file.Close()
	count := 0
	sc := bufio.NewScanner(file)
	for sc.Scan() {
		var m map[string]interface{}
		if err := json.Unmarshal(sc.Bytes(), &m); err != nil {
			t.Errorf("invalid JSON line: %s", sc.Text())
		}
		if _, ok := m["key"]; !ok {
			t.Error("missing 'key' field")
		}
		if _, ok := m["hash"]; !ok {
			t.Error("missing 'hash' field")
		}
		count++
	}
	if count != 3 {
		t.Errorf("expected 3 records in file, got %d", count)
	}
}

func TestFileStoreFlush(t *testing.T) {
	f, _ := os.CreateTemp("", "flush-*.ndjson")
	f.Close()
	t.Cleanup(func() { os.Remove(f.Name()) })

	fs, _ := store.NewFileStore(f.Name())
	ctx := context.Background()
	fs.Put(ctx, rec("k", "/x", []byte{1}, 1))
	if err := fs.Flush(); err != nil {
		t.Fatalf("Flush: %v", err)
	}

	// File should contain data.
	info, _ := os.Stat(f.Name())
	if info.Size() == 0 {
		t.Error("file should not be empty after Flush")
	}
	fs.Close()
}

func TestFileStoreClosed(t *testing.T) {
	f, _ := os.CreateTemp("", "closed-*.ndjson")
	f.Close()
	t.Cleanup(func() { os.Remove(f.Name()) })

	fs, _ := store.NewFileStore(f.Name())
	fs.Close()

	err := fs.Put(context.Background(), rec("k", "/x", []byte{1}, 1))
	if err == nil {
		t.Error("Put after Close should return error")
	}
}

func TestFileStoreStats(t *testing.T) {
	f, _ := os.CreateTemp("", "stats-*.ndjson")
	f.Close()
	t.Cleanup(func() { os.Remove(f.Name()) })

	fs, _ := store.NewFileStore(f.Name())
	defer fs.Close()

	ctx := context.Background()
	for i := 0; i < 5; i++ {
		fs.Put(ctx, rec(itoa(i), "/x", []byte{byte(i)}, 0))
	}
	writes, _ := fs.Stats()
	if writes != 5 {
		t.Errorf("want 5 writes, got %d", writes)
	}
}

// ─────────────────────────── MultiStore ──────────────────────────────────────

func TestMultiStorePutGet(t *testing.T) {
	m1 := store.NewMemStore()
	m2 := store.NewMemStore()
	ms := store.NewMultiStore(m1, m2)
	ctx := context.Background()

	r := rec("k", "/x", []byte{1}, 10)
	if err := ms.Put(ctx, r); err != nil {
		t.Fatalf("Put: %v", err)
	}

	// Both backends should have it.
	if _, ok, _ := m1.Get(ctx, "k"); !ok {
		t.Error("m1 should have record")
	}
	if _, ok, _ := m2.Get(ctx, "k"); !ok {
		t.Error("m2 should have record")
	}

	got, ok, _ := ms.Get(ctx, "k")
	if !ok || got.Path != r.Path {
		t.Errorf("MultiStore Get failed: ok=%v got=%+v", ok, got)
	}
}

func TestMultiStoreLen(t *testing.T) {
	m1 := store.NewMemStore()
	m2 := store.NewMemStore()
	ms := store.NewMultiStore(m1, m2)
	ctx := context.Background()
	ms.Put(ctx, rec("k", "/x", []byte{1}, 0))
	if ms.Len() != 1 {
		t.Errorf("want 1, got %d", ms.Len())
	}
}

// ─────────────────────────── NopStore ────────────────────────────────────────

func TestNopStore(t *testing.T) {
	var ns store.NopStore
	ctx := context.Background()
	if err := ns.Put(ctx, rec("k", "/x", []byte{}, 0)); err != nil {
		t.Errorf("NopStore.Put: %v", err)
	}
	_, ok, err := ns.Get(ctx, "k")
	if err != nil || ok {
		t.Errorf("NopStore.Get: expected miss, got ok=%v err=%v", ok, err)
	}
	if ns.Len() != 0 {
		t.Error("NopStore.Len should be 0")
	}
	if err := ns.Close(); err != nil {
		t.Errorf("NopStore.Close: %v", err)
	}
}

// ─────────────────────────── Record helpers ───────────────────────────────────

func TestRecordWithHash(t *testing.T) {
	r := store.Record{Key: "k", Path: "/x"}.WithHash([]byte{0xDE, 0xAD})
	if r.HashHex != "dead" {
		t.Errorf("want dead, got %s", r.HashHex)
	}
	if len(r.Hash) != 2 {
		t.Errorf("want 2 hash bytes, got %d", len(r.Hash))
	}
}

func TestRecordString(t *testing.T) {
	r := store.Record{
		Key:     "k",
		Path:    "/usr/lib/x.so",
		HashHex: "abcdef",
		Size:    1024,
	}
	s := r.String()
	if len(s) == 0 {
		t.Error("String() should be non-empty")
	}
}

func TestRecordComputedAt(t *testing.T) {
	f, _ := os.CreateTemp("", "ts-*.ndjson")
	f.Close()
	t.Cleanup(func() { os.Remove(f.Name()) })

	fs, _ := store.NewFileStore(f.Name())
	defer fs.Close()

	r := rec("k", "/x", []byte{1}, 1)
	before := time.Now().UTC()
	fs.Put(context.Background(), r)
	after := time.Now().UTC()

	got, ok, _ := fs.Get(context.Background(), "k")
	if !ok {
		t.Fatal("record not found")
	}
	if got.ComputedAt.Before(before) || got.ComputedAt.After(after) {
		t.Errorf("ComputedAt %v outside range [%v, %v]", got.ComputedAt, before, after)
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
