//go:build linux

package hashdb_test

import (
	"os"
	"path/filepath"
	"sync"
	"testing"

	"github.com/bons/bons-ci/plugins/checksummer/pkg/filekey"
	"github.com/bons/bons-ci/plugins/checksummer/pkg/hashdb"
)

// ─────────────────────────── helpers ─────────────────────────────────────────

func tempDB(t *testing.T) (*hashdb.DB, string) {
	t.Helper()
	dir := t.TempDir()
	db, err := hashdb.Open(dir)
	if err != nil {
		t.Fatalf("Open: %v", err)
	}
	t.Cleanup(func() { db.Close() })
	return db, dir
}

func handleKey(mountID int, hbytes []byte) filekey.Key {
	k := filekey.Key{Source: filekey.SourceHandle, MountID: mountID}
	k.HandleLen = copy(k.Handle[:], hbytes)
	return k
}

var (
	key1  = handleKey(42, []byte{0x01, 0x02, 0x03, 0x04})
	key2  = handleKey(42, []byte{0x05, 0x06, 0x07, 0x08})
	key3  = handleKey(99, []byte{0x01, 0x02, 0x03, 0x04}) // same handle, different mount
	hash1 = make32(0xAB)
	hash2 = make32(0xCD)
)

func make32(b byte) []byte {
	h := make([]byte, 32)
	for i := range h {
		h[i] = b
	}
	return h
}

// ─────────────────────────── basic get/put ────────────────────────────────────

func TestGetMiss(t *testing.T) {
	db, _ := tempDB(t)
	_, ok := db.Get(key1, 1000, 512)
	if ok {
		t.Error("expected miss on empty db")
	}
}

func TestPutGet(t *testing.T) {
	db, _ := tempDB(t)
	db.Put(key1, 1000, 512, hash1)

	got, ok := db.Get(key1, 1000, 512)
	if !ok {
		t.Fatal("expected hit after Put")
	}
	if string(got) != string(hash1) {
		t.Errorf("hash mismatch: %x vs %x", got, hash1)
	}
}

func TestStaleOnMtime(t *testing.T) {
	db, _ := tempDB(t)
	db.Put(key1, 1000, 512, hash1)

	_, ok := db.Get(key1, 1001, 512) // different mtime
	if ok {
		t.Error("expected stale miss when mtime changed")
	}
}

func TestStaleOnSize(t *testing.T) {
	db, _ := tempDB(t)
	db.Put(key1, 1000, 512, hash1)

	_, ok := db.Get(key1, 1000, 513) // different size
	if ok {
		t.Error("expected stale miss when size changed")
	}
}

func TestDifferentKeysIndependent(t *testing.T) {
	db, _ := tempDB(t)
	db.Put(key1, 1000, 512, hash1)
	db.Put(key2, 2000, 1024, hash2)

	got1, ok1 := db.Get(key1, 1000, 512)
	got2, ok2 := db.Get(key2, 2000, 1024)

	if !ok1 || string(got1) != string(hash1) {
		t.Errorf("key1: ok=%v hash=%x", ok1, got1)
	}
	if !ok2 || string(got2) != string(hash2) {
		t.Errorf("key2: ok=%v hash=%x", ok2, got2)
	}
}

func TestSameMountDifferentHandle(t *testing.T) {
	db, _ := tempDB(t)
	db.Put(key1, 100, 100, hash1)
	db.Put(key3, 100, 100, hash2) // same handle bytes, different mount_id

	got1, ok1 := db.Get(key1, 100, 100)
	got3, ok3 := db.Get(key3, 100, 100)

	if !ok1 || string(got1) != string(hash1) {
		t.Error("key1 miss")
	}
	if !ok3 || string(got3) != string(hash2) {
		t.Error("key3 miss")
	}
}

func TestStatKeySkipped(t *testing.T) {
	db, _ := tempDB(t)
	statKey := filekey.Key{Source: filekey.SourceStat, Dev: 17, Ino: 999}
	db.Put(statKey, 1000, 512, hash1)

	_, ok := db.Get(statKey, 1000, 512)
	if ok {
		t.Error("stat-based keys must not be persisted (not stable across mounts)")
	}
}

// ─────────────────────────── persistence ─────────────────────────────────────

func TestPersistAcrossReopen(t *testing.T) {
	dir := t.TempDir()

	// Session 1: open, write, flush, close.
	db1, err := hashdb.Open(dir)
	if err != nil {
		t.Fatalf("Open 1: %v", err)
	}
	db1.Put(key1, 5000, 2048, hash1)
	db1.Put(key2, 6000, 4096, hash2)
	if err := db1.Close(); err != nil {
		t.Fatalf("Close 1: %v", err)
	}

	// Session 2: reopen, expect all entries loaded.
	db2, err := hashdb.Open(dir)
	if err != nil {
		t.Fatalf("Open 2: %v", err)
	}
	defer db2.Close()

	got1, ok1 := db2.Get(key1, 5000, 2048)
	got2, ok2 := db2.Get(key2, 6000, 4096)

	if !ok1 || string(got1) != string(hash1) {
		t.Errorf("session2 key1: ok=%v", ok1)
	}
	if !ok2 || string(got2) != string(hash2) {
		t.Errorf("session2 key2: ok=%v", ok2)
	}
}

func TestUpdateOverwrites(t *testing.T) {
	dir := t.TempDir()

	db1, _ := hashdb.Open(dir)
	db1.Put(key1, 1000, 512, hash1)
	db1.Close()

	// Simulate file modification: same key, new mtime/size/hash.
	db2, _ := hashdb.Open(dir)
	db2.Put(key1, 2000, 1024, hash2) // updated entry
	db2.Close()

	db3, _ := hashdb.Open(dir)
	defer db3.Close()

	_, old := db3.Get(key1, 1000, 512)      // old validation → stale
	got, fresh := db3.Get(key1, 2000, 1024) // new validation → hit

	if old {
		t.Error("old mtime should be stale after update")
	}
	if !fresh || string(got) != string(hash2) {
		t.Errorf("fresh: ok=%v hash=%x", fresh, got)
	}
}

func TestShardFilesCreated(t *testing.T) {
	dir := t.TempDir()
	db, _ := hashdb.Open(dir)

	// Write to enough different keys to touch multiple shards.
	for i := 0; i < 64; i++ {
		k := handleKey(42, []byte{byte(i), byte(i >> 8), byte(i >> 16), 0x42})
		db.Put(k, int64(i)*1000, int64(i)*512, hash1)
	}
	if err := db.Close(); err != nil {
		t.Fatalf("Close: %v", err)
	}

	// At least some shard files should exist.
	entries, _ := os.ReadDir(dir)
	binFiles := 0
	for _, e := range entries {
		if filepath.Ext(e.Name()) == ".bin" {
			binFiles++
		}
	}
	if binFiles == 0 {
		t.Error("expected at least one shard .bin file")
	}
}

// ─────────────────────────── cross-mergedview dedup ──────────────────────────

// TestCrossViewDedup validates the core claim: if file F from lowerdir L
// is accessed through mergedview1 and mergedview2, both produce the same
// filekey.Key (because name_to_handle_at returns the lower fs handle), and
// the hashdb correctly serves the cached hash for both views.
func TestCrossViewDedup(t *testing.T) {
	dir := t.TempDir()
	db, _ := hashdb.Open(dir)
	defer db.Close()

	// Both mergedview1 and mergedview2 share lowerdir L.
	// name_to_handle_at for file F in L returns the same (mount_id=42, handle=H)
	// regardless of which mergedview the fd was opened through.
	sharedLowerdirKey := handleKey(42, []byte{0xDE, 0xAD, 0xBE, 0xEF})

	// Time T1: accessed via mergedview1, hash computed and stored.
	db.Put(sharedLowerdirKey, 1000000000, 8192, hash1)

	// Time T2 (same or different process): accessed via mergedview2.
	// Key is identical → cache hit.
	got, ok := db.Get(sharedLowerdirKey, 1000000000, 8192)
	if !ok {
		t.Fatal("cross-view dedup: expected cache hit for shared lowerdir file")
	}
	if string(got) != string(hash1) {
		t.Errorf("cross-view dedup: hash mismatch %x vs %x", got, hash1)
	}

	hits, misses, stores := db.Stats()
	t.Logf("hits=%d misses=%d stores=%d", hits, misses, stores)
}

// ─────────────────────────── concurrency ─────────────────────────────────────

func TestConcurrentPutGet(t *testing.T) {
	db, _ := tempDB(t)
	var wg sync.WaitGroup
	for i := 0; i < 500; i++ {
		i := i
		k := handleKey(42, []byte{byte(i), byte(i >> 8), byte(i >> 16), 0xFF})
		h := make32(byte(i))
		wg.Add(1)
		go func() {
			defer wg.Done()
			db.Put(k, int64(i), int64(i*100), h)
			got, ok := db.Get(k, int64(i), int64(i*100))
			if !ok {
				t.Errorf("concurrent Get miss for i=%d", i)
			}
			if ok && string(got) != string(h) {
				t.Errorf("concurrent hash mismatch i=%d", i)
			}
		}()
	}
	wg.Wait()
}

// ─────────────────────────── stats ───────────────────────────────────────────

func TestStats(t *testing.T) {
	db, _ := tempDB(t)
	db.Put(key1, 100, 100, hash1)
	db.Get(key1, 100, 100) // hit
	db.Get(key1, 200, 100) // miss (stale)
	db.Get(key2, 100, 100) // miss (not present)

	hits, misses, stores := db.Stats()
	if hits != 1 {
		t.Errorf("hits: want 1, got %d", hits)
	}
	if misses != 2 {
		t.Errorf("misses: want 2, got %d", misses)
	}
	if stores != 1 {
		t.Errorf("stores: want 1, got %d", stores)
	}
}
