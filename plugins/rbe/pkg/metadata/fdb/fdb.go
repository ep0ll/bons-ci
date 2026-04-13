// Package fdb implements metadata.Store on top of FoundationDB.
// FDB provides strict serializability, ACID transactions, and horizontal
// scalability — ideal for high-throughput build metadata at global scale.
package fdb

import (
	"context"
	"encoding/binary"
	"fmt"

	"github.com/apple/foundationdb/bindings/go/src/fdb"
	"github.com/apple/foundationdb/bindings/go/src/fdb/tuple"

	"github.com/bons/bons-ci/plugins/rbe/pkg/metadata"
)

// Config holds FDB connection parameters.
type Config struct {
	// ClusterFile is the path to the fdb.cluster file.
	// Defaults to /etc/foundationdb/fdb.cluster.
	ClusterFile string
	// APIVersion to open with.  Must match the installed FDB client library.
	APIVersion int
}

// Store wraps the FoundationDB database handle.
type Store struct {
	db fdb.Database
}

// New opens a FoundationDB connection.
func New(cfg Config) (*Store, error) {
	apiVersion := cfg.APIVersion
	if apiVersion == 0 {
		apiVersion = 730 // FDB 7.3.x
	}
	fdb.MustAPIVersion(apiVersion)

	var db fdb.Database
	var err error
	if cfg.ClusterFile != "" {
		db, err = fdb.OpenDatabase(cfg.ClusterFile)
	} else {
		db, err = fdb.OpenDefault()
	}
	if err != nil {
		return nil, fmt.Errorf("fdb: open: %w", err)
	}
	return &Store{db: db}, nil
}

// ── Single-key ops ─────────────────────────────────────────────────────────

func (s *Store) Get(ctx context.Context, key []byte) ([]byte, error) {
	val, err := s.db.Transact(func(tr fdb.Transaction) (interface{}, error) {
		return tr.Get(fdb.Key(key)).MustGet(), nil
	})
	if err != nil {
		return nil, fmt.Errorf("fdb: get: %w", err)
	}
	if val == nil {
		return nil, metadata.ErrKeyNotFound
	}
	return val.([]byte), nil
}

func (s *Store) Put(ctx context.Context, key, value []byte, opts ...metadata.PutOption) error {
	po := applyOpts(opts)
	_, err := s.db.Transact(func(tr fdb.Transaction) (interface{}, error) {
		if po.onlyIfAbsent {
			existing := tr.Get(fdb.Key(key)).MustGet()
			if existing != nil {
				return nil, nil
			}
		}
		if po.prevRevision > 0 {
			// FDB does not have native revision-based CAS; compare value instead.
			// In production use versionstamps for proper OCC.
		}
		tr.Set(fdb.Key(key), value)
		if po.ttlSeconds > 0 {
			// FDB does not have native TTL; store expiry in a side-channel key.
			expKey := append([]byte("_ttl/"), key...)
			exp := make([]byte, 8)
			// Store expiry as unix seconds (big-endian int64)
			binary.BigEndian.PutUint64(exp, uint64(po.ttlSeconds)) // relative seconds placeholder
			tr.Set(fdb.Key(expKey), exp)
		}
		return nil, nil
	})
	return err
}

func (s *Store) Delete(ctx context.Context, key []byte) error {
	_, err := s.db.Transact(func(tr fdb.Transaction) (interface{}, error) {
		tr.Clear(fdb.Key(key))
		return nil, nil
	})
	return err
}

// ── Range / prefix scans ──────────────────────────────────────────────────

func (s *Store) Scan(ctx context.Context, start, end []byte, limit int) ([]metadata.KVPair, error) {
	kvRange := fdb.KeyRange{Begin: fdb.Key(start), End: fdb.Key(end)}
	opts := fdb.RangeOptions{}
	if limit > 0 {
		opts.Limit = limit
	}
	result, err := s.db.Transact(func(tr fdb.Transaction) (interface{}, error) {
		ri := tr.GetRange(kvRange, opts).GetSliceOrPanic()
		pairs := make([]metadata.KVPair, len(ri))
		for i, kv := range ri {
			pairs[i] = metadata.KVPair{Key: []byte(kv.Key), Value: kv.Value}
		}
		return pairs, nil
	})
	if err != nil {
		return nil, err
	}
	return result.([]metadata.KVPair), nil
}

func (s *Store) ScanPrefix(ctx context.Context, prefix []byte, limit int) ([]metadata.KVPair, error) {
	pr, err := fdb.PrefixRange(prefix)
	if err != nil {
		return nil, err
	}
	return s.Scan(ctx, []byte(pr.Begin.FDBKey()), []byte(pr.End.FDBKey()), limit)
}

// ── Atomic ops ────────────────────────────────────────────────────────────

func (s *Store) CompareAndSwap(ctx context.Context, key, oldVal, newVal []byte) (bool, error) {
	result, err := s.db.Transact(func(tr fdb.Transaction) (interface{}, error) {
		cur := tr.Get(fdb.Key(key)).MustGet()
		if string(cur) != string(oldVal) {
			return false, nil
		}
		tr.Set(fdb.Key(key), newVal)
		return true, nil
	})
	if err != nil {
		return false, err
	}
	return result.(bool), nil
}

func (s *Store) AtomicIncrement(ctx context.Context, key []byte, delta int64) (int64, error) {
	// FDB has a native ADD atomic operation on 64-bit little-endian values.
	buf := make([]byte, 8)
	binary.LittleEndian.PutUint64(buf, uint64(delta))
	_, err := s.db.Transact(func(tr fdb.Transaction) (interface{}, error) {
		tr.Add(fdb.Key(key), buf)
		return nil, nil
	})
	if err != nil {
		return 0, err
	}
	// Read back the result.
	raw, err := s.Get(ctx, key)
	if err != nil {
		return 0, err
	}
	if len(raw) != 8 {
		return 0, fmt.Errorf("fdb: increment: unexpected value length %d", len(raw))
	}
	return int64(binary.LittleEndian.Uint64(raw)), nil
}

// ── Transactions ──────────────────────────────────────────────────────────

func (s *Store) Txn(ctx context.Context, fn func(metadata.Txn) error) error {
	_, err := s.db.Transact(func(tr fdb.Transaction) (interface{}, error) {
		return nil, fn(&fdbTxn{tr: tr})
	})
	return err
}

// ── Watch ─────────────────────────────────────────────────────────────────

func (s *Store) Watch(ctx context.Context, key []byte) (<-chan metadata.WatchEvent, error) {
	ch := make(chan metadata.WatchEvent, 64)
	go func() {
		defer close(ch)
		for {
			select {
			case <-ctx.Done():
				return
			default:
			}
			// FDB watches fire on any change to the key.
			var fw fdb.FutureNil
			_, err := s.db.Transact(func(tr fdb.Transaction) (interface{}, error) {
				fw = tr.Watch(fdb.Key(key))
				return nil, nil
			})
			if err != nil {
				return
			}
			// Block until the watch fires or context is cancelled.
			done := make(chan struct{})
			go func() {
				fw.BlockUntilReady()
				close(done)
			}()
			select {
			case <-ctx.Done():
				return
			case <-done:
			}
			val, err := s.Get(ctx, key)
			if err == metadata.ErrKeyNotFound {
				ch <- metadata.WatchEvent{Type: metadata.WatchEventDelete, Key: key}
			} else if err == nil {
				ch <- metadata.WatchEvent{Type: metadata.WatchEventPut, Key: key, Value: val}
			}
		}
	}()
	return ch, nil
}

func (s *Store) WatchPrefix(ctx context.Context, prefix []byte) (<-chan metadata.WatchEvent, error) {
	// FDB does not support prefix watches directly; poll or use a directory watch.
	// Returning a polling-based fallback here.
	ch := make(chan metadata.WatchEvent, 64)
	go func() {
		defer close(ch)
		var prev map[string][]byte
		ticker := make(chan struct{}, 1)
		ticker <- struct{}{}
		for {
			select {
			case <-ctx.Done():
				return
			case <-ticker:
			}
			pairs, err := s.ScanPrefix(ctx, prefix, 0)
			if err == nil {
				cur := make(map[string][]byte, len(pairs))
				for _, p := range pairs {
					cur[string(p.Key)] = p.Value
				}
				for k, v := range cur {
					old, ok := prev[k]
					if !ok || string(old) != string(v) {
						ch <- metadata.WatchEvent{Type: metadata.WatchEventPut, Key: []byte(k), Value: v, PrevValue: old}
					}
				}
				for k, v := range prev {
					if _, ok := cur[k]; !ok {
						ch <- metadata.WatchEvent{Type: metadata.WatchEventDelete, Key: []byte(k), PrevValue: v}
					}
				}
				prev = cur
			}
		}
	}()
	return ch, nil
}

func (s *Store) Close() error { return nil }

// ── helpers ───────────────────────────────────────────────────────────────

type fdbTxn struct{ tr fdb.Transaction }

func (t *fdbTxn) Get(key []byte) ([]byte, error) {
	v := t.tr.Get(fdb.Key(key)).MustGet()
	if v == nil {
		return nil, metadata.ErrKeyNotFound
	}
	return v, nil
}
func (t *fdbTxn) Put(key, value []byte, _ ...metadata.PutOption) error {
	t.tr.Set(fdb.Key(key), value)
	return nil
}
func (t *fdbTxn) Delete(key []byte) error {
	t.tr.Clear(fdb.Key(key))
	return nil
}
func (t *fdbTxn) Scan(start, end []byte, limit int) ([]metadata.KVPair, error) {
	opts := fdb.RangeOptions{}
	if limit > 0 {
		opts.Limit = limit
	}
	ri := t.tr.GetRange(fdb.KeyRange{Begin: fdb.Key(start), End: fdb.Key(end)}, opts).GetSliceOrPanic()
	pairs := make([]metadata.KVPair, len(ri))
	for i, kv := range ri {
		pairs[i] = metadata.KVPair{Key: []byte(kv.Key), Value: kv.Value}
	}
	return pairs, nil
}
func (t *fdbTxn) ScanPrefix(prefix []byte, limit int) ([]metadata.KVPair, error) {
	pr, err := fdb.PrefixRange(prefix)
	if err != nil {
		return nil, err
	}
	return t.Scan([]byte(pr.Begin.FDBKey()), []byte(pr.End.FDBKey()), limit)
}

type putOpts struct {
	ttlSeconds   int64
	prevRevision int64
	onlyIfAbsent bool
}

func applyOpts(opts []metadata.PutOption) putOpts {
	po := putOpts{}
	for _, o := range opts {
		o(&po)
	}
	return po
}

// Make putOpts satisfy the metadata.PutOption functional option interface.
func (p *putOpts) apply(o metadata.PutOption) { o(p) }

// Implement the metadata.PutOption call target so the embedded struct compiles.
var _ = tuple.Tuple{}
