// Package tikv implements metadata.Store on top of TiKV.
package tikv

import (
	"context"
	"encoding/binary"
	"fmt"

	"github.com/tikv/client-go/v2/config"
	"github.com/tikv/client-go/v2/rawkv"
	"github.com/tikv/client-go/v2/txnkv"
	"github.com/tikv/client-go/v2/txnkv/transaction"

	"github.com/bons/bons-ci/plugins/rbe/pkg/metadata"
)

// Config holds TiKV PD endpoints.
type Config struct {
	PDAddresses []string
	Security    config.Security
}

// Store wraps the TiKV transactional client.
type Store struct {
	txnClient *txnkv.Client
	rawClient *rawkv.Client
}

// New creates a new TiKV-backed metadata store.
func New(cfg Config) (*Store, error) {
	txnClient, err := txnkv.NewClient(cfg.PDAddresses, config.WithSecurity(cfg.Security))
	if err != nil {
		return nil, fmt.Errorf("tikv: txn client: %w", err)
	}
	rawClient, err := rawkv.NewClient(context.Background(), cfg.PDAddresses, cfg.Security)
	if err != nil {
		_ = txnClient.Close()
		return nil, fmt.Errorf("tikv: raw client: %w", err)
	}
	return &Store{txnClient: txnClient, rawClient: rawClient}, nil
}

func (s *Store) Get(ctx context.Context, key []byte) ([]byte, error) {
	tx, err := s.txnClient.Begin()
	if err != nil {
		return nil, err
	}
	val, err := tx.Get(ctx, key)
	if err != nil {
		if isNotFound(err) {
			return nil, metadata.ErrKeyNotFound
		}
		return nil, err
	}
	_ = tx.Rollback()
	return val, nil
}

func (s *Store) Put(ctx context.Context, key, value []byte, opts ...metadata.PutOption) error {
	po := applyPutOpts(opts)
	if po.ttlSeconds > 0 {
		return s.rawClient.PutWithTTL(ctx, key, value, uint64(po.ttlSeconds))
	}
	tx, err := s.txnClient.Begin()
	if err != nil {
		return err
	}
	if po.onlyIfAbsent {
		existing, err := tx.Get(ctx, key)
		if err == nil && existing != nil {
			_ = tx.Rollback()
			return nil
		}
	}
	if err := tx.Set(key, value); err != nil {
		_ = tx.Rollback()
		return err
	}
	return tx.Commit(ctx)
}

func (s *Store) Delete(ctx context.Context, key []byte) error {
	tx, err := s.txnClient.Begin()
	if err != nil {
		return err
	}
	if err := tx.Delete(key); err != nil {
		_ = tx.Rollback()
		return err
	}
	return tx.Commit(ctx)
}

func (s *Store) Scan(ctx context.Context, start, end []byte, limit int) ([]metadata.KVPair, error) {
	tx, err := s.txnClient.Begin()
	if err != nil {
		return nil, err
	}
	defer tx.Rollback() //nolint:errcheck
	iter, err := tx.Iter(start, end)
	if err != nil {
		return nil, err
	}
	defer iter.Close()
	var pairs []metadata.KVPair
	for iter.Valid() && (limit == 0 || len(pairs) < limit) {
		pairs = append(pairs, metadata.KVPair{Key: iter.Key(), Value: iter.Value()})
		if err := iter.Next(); err != nil {
			break
		}
	}
	return pairs, nil
}

func (s *Store) ScanPrefix(ctx context.Context, prefix []byte, limit int) ([]metadata.KVPair, error) {
	end := prefixEnd(prefix)
	return s.Scan(ctx, prefix, end, limit)
}

func (s *Store) CompareAndSwap(ctx context.Context, key, oldVal, newVal []byte) (bool, error) {
	tx, err := s.txnClient.Begin()
	if err != nil {
		return false, err
	}
	cur, err := tx.Get(ctx, key)
	if err != nil && !isNotFound(err) {
		_ = tx.Rollback()
		return false, err
	}
	if string(cur) != string(oldVal) {
		_ = tx.Rollback()
		return false, nil
	}
	if err := tx.Set(key, newVal); err != nil {
		_ = tx.Rollback()
		return false, err
	}
	if err := tx.Commit(ctx); err != nil {
		return false, err
	}
	return true, nil
}

func (s *Store) AtomicIncrement(ctx context.Context, key []byte, delta int64) (int64, error) {
	for {
		tx, err := s.txnClient.Begin()
		if err != nil {
			return 0, err
		}
		raw, err := tx.Get(ctx, key)
		var cur int64
		if err == nil && len(raw) == 8 {
			cur = int64(binary.BigEndian.Uint64(raw))
		}
		result := cur + delta
		buf := make([]byte, 8)
		binary.BigEndian.PutUint64(buf, uint64(result))
		if err := tx.Set(key, buf); err != nil {
			_ = tx.Rollback()
			return 0, err
		}
		if err := tx.Commit(ctx); err != nil {
			if isConflict(err) {
				continue
			}
			return 0, err
		}
		return result, nil
	}
}

func (s *Store) Txn(ctx context.Context, fn func(metadata.Txn) error) error {
	for {
		tx, err := s.txnClient.Begin()
		if err != nil {
			return err
		}
		if err := fn(&tikvTxn{tx: tx, ctx: ctx}); err != nil {
			_ = tx.Rollback()
			return err
		}
		if err := tx.Commit(ctx); err != nil {
			if isConflict(err) {
				continue
			}
			return err
		}
		return nil
	}
}

func (s *Store) Watch(ctx context.Context, key []byte) (<-chan metadata.WatchEvent, error) {
	// TiKV does not have a native push-watch API on the txnkv client.
	// We simulate it with polling. Production code should use CDC (Ticdc/PD watch).
	return pollWatch(ctx, s, key, false), nil
}

func (s *Store) WatchPrefix(ctx context.Context, prefix []byte) (<-chan metadata.WatchEvent, error) {
	return pollWatch(ctx, s, prefix, true), nil
}

func (s *Store) Close() error {
	_ = s.rawClient.Close()
	return s.txnClient.Close()
}

// ── helpers ───────────────────────────────────────────────────────────────

type tikvTxn struct {
	tx  *transaction.KVTxn
	ctx context.Context
}

func (t *tikvTxn) Get(key []byte) ([]byte, error) {
	v, err := t.tx.Get(t.ctx, key)
	if isNotFound(err) {
		return nil, metadata.ErrKeyNotFound
	}
	return v, err
}
func (t *tikvTxn) Put(key, value []byte, _ ...metadata.PutOption) error { return t.tx.Set(key, value) }
func (t *tikvTxn) Delete(key []byte) error                              { return t.tx.Delete(key) }
func (t *tikvTxn) Scan(start, end []byte, limit int) ([]metadata.KVPair, error) {
	iter, err := t.tx.Iter(start, end)
	if err != nil {
		return nil, err
	}
	defer iter.Close()
	var pairs []metadata.KVPair
	for iter.Valid() && (limit == 0 || len(pairs) < limit) {
		pairs = append(pairs, metadata.KVPair{Key: iter.Key(), Value: iter.Value()})
		if err := iter.Next(); err != nil {
			break
		}
	}
	return pairs, nil
}
func (t *tikvTxn) ScanPrefix(prefix []byte, limit int) ([]metadata.KVPair, error) {
	return t.Scan(prefix, prefixEnd(prefix), limit)
}

func prefixEnd(prefix []byte) []byte {
	end := make([]byte, len(prefix))
	copy(end, prefix)
	for i := len(end) - 1; i >= 0; i-- {
		end[i]++
		if end[i] != 0 {
			return end
		}
	}
	return nil // overflow: scan to end of keyspace
}

func isNotFound(err error) bool {
	if err == nil {
		return false
	}
	return err.Error() == "key not found"
}

func isConflict(err error) bool {
	if err == nil {
		return false
	}
	s := err.Error()
	return s == "write conflict" || s == "transaction conflict"
}

func applyPutOpts(opts []metadata.PutOption) putOpts {
	po := putOpts{}
	for _, o := range opts {
		o(&po)
	}
	return po
}

type putOpts struct {
	ttlSeconds   int64
	prevRevision int64
	onlyIfAbsent bool
}

// pollWatch is a fallback polling-based watch.
// In production replace with TiKV CDC or placement-driver watch.
func pollWatch(ctx context.Context, s *Store, key []byte, prefix bool) <-chan metadata.WatchEvent {
	ch := make(chan metadata.WatchEvent, 64)
	go func() {
		defer close(ch)
		var prev map[string][]byte
		for {
			select {
			case <-ctx.Done():
				return
			default:
			}
			var pairs []metadata.KVPair
			var err error
			if prefix {
				pairs, err = s.ScanPrefix(ctx, key, 0)
			} else {
				v, e := s.Get(ctx, key)
				if e == nil {
					pairs = []metadata.KVPair{{Key: key, Value: v}}
				}
				err = e
			}
			if err == nil {
				cur := map[string][]byte{}
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
	return ch
}
