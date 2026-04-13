// Package etcd implements metadata.Store on top of etcd v3.
package etcd

import (
	"context"
	"fmt"
	"time"

	clientv3 "go.etcd.io/etcd/client/v3"
	"go.etcd.io/etcd/client/v3/concurrency"

	"github.com/bons/bons-ci/plugins/rbe/pkg/metadata"
)

// Config holds etcd connection parameters.
type Config struct {
	Endpoints   []string
	DialTimeout time.Duration
	Username    string
	Password    string
	// TLS
	CertFile string
	KeyFile  string
	CAFile   string
}

// Store wraps the etcd v3 client.
type Store struct {
	client *clientv3.Client
}

// New creates a new etcd-backed metadata store.
func New(cfg Config) (*Store, error) {
	if cfg.DialTimeout == 0 {
		cfg.DialTimeout = 5 * time.Second
	}
	etcdCfg := clientv3.Config{
		Endpoints:   cfg.Endpoints,
		DialTimeout: cfg.DialTimeout,
		Username:    cfg.Username,
		Password:    cfg.Password,
	}
	client, err := clientv3.New(etcdCfg)
	if err != nil {
		return nil, fmt.Errorf("etcd: dial %v: %w", cfg.Endpoints, err)
	}
	return &Store{client: client}, nil
}

// ── Single-key ops ─────────────────────────────────────────────────────────

func (s *Store) Get(ctx context.Context, key []byte) ([]byte, error) {
	resp, err := s.client.Get(ctx, string(key))
	if err != nil {
		return nil, fmt.Errorf("etcd: get %s: %w", key, err)
	}
	if len(resp.Kvs) == 0 {
		return nil, metadata.ErrKeyNotFound
	}
	return resp.Kvs[0].Value, nil
}

func (s *Store) Put(ctx context.Context, key, value []byte, opts ...metadata.PutOption) error {
	po := applyOpts(opts)
	etcdOpts := []clientv3.OpOption{}

	var leaseID clientv3.LeaseID
	if po.ttlSeconds > 0 {
		grant, err := s.client.Grant(ctx, po.ttlSeconds)
		if err != nil {
			return fmt.Errorf("etcd: lease grant: %w", err)
		}
		leaseID = grant.ID
		etcdOpts = append(etcdOpts, clientv3.WithLease(leaseID))
	}

	if po.onlyIfAbsent {
		txn := s.client.Txn(ctx).
			If(clientv3.Compare(clientv3.Version(string(key)), "=", 0)).
			Then(clientv3.OpPut(string(key), string(value), etcdOpts...))
		_, err := txn.Commit()
		return err
	}

	if po.prevRevision > 0 {
		txn := s.client.Txn(ctx).
			If(clientv3.Compare(clientv3.ModRevision(string(key)), "=", po.prevRevision)).
			Then(clientv3.OpPut(string(key), string(value), etcdOpts...))
		resp, err := txn.Commit()
		if err != nil {
			return err
		}
		if !resp.Succeeded {
			return fmt.Errorf("etcd: cas failed for %s", key)
		}
		return nil
	}

	_, err := s.client.Put(ctx, string(key), string(value), etcdOpts...)
	return err
}

func (s *Store) Delete(ctx context.Context, key []byte) error {
	_, err := s.client.Delete(ctx, string(key))
	return err
}

// ── Range / prefix scans ──────────────────────────────────────────────────

func (s *Store) Scan(ctx context.Context, start, end []byte, limit int) ([]metadata.KVPair, error) {
	opts := []clientv3.OpOption{clientv3.WithRange(string(end))}
	if limit > 0 {
		opts = append(opts, clientv3.WithLimit(int64(limit)))
	}
	resp, err := s.client.Get(ctx, string(start), opts...)
	if err != nil {
		return nil, err
	}
	return toKVPairs(resp), nil
}

func (s *Store) ScanPrefix(ctx context.Context, prefix []byte, limit int) ([]metadata.KVPair, error) {
	opts := []clientv3.OpOption{clientv3.WithPrefix()}
	if limit > 0 {
		opts = append(opts, clientv3.WithLimit(int64(limit)))
	}
	resp, err := s.client.Get(ctx, string(prefix), opts...)
	if err != nil {
		return nil, err
	}
	return toKVPairs(resp), nil
}

// ── Atomic ops ────────────────────────────────────────────────────────────

func (s *Store) CompareAndSwap(ctx context.Context, key, oldVal, newVal []byte) (bool, error) {
	txn := s.client.Txn(ctx).
		If(clientv3.Compare(clientv3.Value(string(key)), "=", string(oldVal))).
		Then(clientv3.OpPut(string(key), string(newVal)))
	resp, err := txn.Commit()
	if err != nil {
		return false, err
	}
	return resp.Succeeded, nil
}

func (s *Store) AtomicIncrement(ctx context.Context, key []byte, delta int64) (int64, error) {
	// etcd does not have a native increment; use STM for serialisable retry.
	var result int64
	_, err := concurrency.NewSTM(s.client, func(stm concurrency.STM) error {
		raw := stm.Get(string(key))
		var cur int64
		if raw != "" {
			fmt.Sscanf(raw, "%d", &cur)
		}
		result = cur + delta
		stm.Put(string(key), fmt.Sprintf("%d", result))
		return nil
	}, concurrency.WithAbortContext(ctx))
	return result, err
}

// ── Transactions ──────────────────────────────────────────────────────────

func (s *Store) Txn(ctx context.Context, fn func(metadata.Txn) error) error {
	_, err := concurrency.NewSTM(s.client, func(stm concurrency.STM) error {
		return fn(&etcdTxn{stm: stm})
	}, concurrency.WithAbortContext(ctx))
	return err
}

// ── Watch ─────────────────────────────────────────────────────────────────

func (s *Store) Watch(ctx context.Context, key []byte) (<-chan metadata.WatchEvent, error) {
	ch := make(chan metadata.WatchEvent, 64)
	wch := s.client.Watch(ctx, string(key))
	go pumpWatch(wch, ch)
	return ch, nil
}

func (s *Store) WatchPrefix(ctx context.Context, prefix []byte) (<-chan metadata.WatchEvent, error) {
	ch := make(chan metadata.WatchEvent, 64)
	wch := s.client.Watch(ctx, string(prefix), clientv3.WithPrefix())
	go pumpWatch(wch, ch)
	return ch, nil
}

func (s *Store) Close() error { return s.client.Close() }

// ── helpers ───────────────────────────────────────────────────────────────

func toKVPairs(resp *clientv3.GetResponse) []metadata.KVPair {
	pairs := make([]metadata.KVPair, len(resp.Kvs))
	for i, kv := range resp.Kvs {
		pairs[i] = metadata.KVPair{Key: kv.Key, Value: kv.Value, ModRevision: kv.ModRevision}
	}
	return pairs
}

func pumpWatch(wch clientv3.WatchChan, out chan<- metadata.WatchEvent) {
	defer close(out)
	for resp := range wch {
		for _, ev := range resp.Events {
			we := metadata.WatchEvent{
				Key:         ev.Kv.Key,
				Value:       ev.Kv.Value,
				ModRevision: ev.Kv.ModRevision,
			}
			if ev.Type == clientv3.EventTypeDelete {
				we.Type = metadata.WatchEventDelete
				if ev.PrevKv != nil {
					we.PrevValue = ev.PrevKv.Value
				}
			} else {
				we.Type = metadata.WatchEventPut
			}
			out <- we
		}
	}
}

func applyOpts(opts []metadata.PutOption) *putOptions {
	po := &putOptions{}
	for _, o := range opts {
		o(po)
	}
	return po
}

type putOptions struct {
	ttlSeconds   int64
	prevRevision int64
	onlyIfAbsent bool
}

func (o *putOptions) apply(opt metadata.PutOption) {
	// hack: reflect into private fields — use exported helper in real code
}

// etcdTxn wraps an STM for the metadata.Txn interface.
type etcdTxn struct{ stm concurrency.STM }

func (t *etcdTxn) Get(key []byte) ([]byte, error) {
	v := t.stm.Get(string(key))
	if v == "" {
		return nil, metadata.ErrKeyNotFound
	}
	return []byte(v), nil
}

func (t *etcdTxn) Put(key, value []byte, _ ...metadata.PutOption) error {
	t.stm.Put(string(key), string(value))
	return nil
}

func (t *etcdTxn) Delete(key []byte) error {
	t.stm.Del(string(key))
	return nil
}

func (t *etcdTxn) Scan(start, end []byte, limit int) ([]metadata.KVPair, error) {
	// STM does not support range reads; callers should use Get for specific keys
	return nil, fmt.Errorf("etcd stm: range scan not supported; use explicit gets")
}

func (t *etcdTxn) ScanPrefix(prefix []byte, limit int) ([]metadata.KVPair, error) {
	return nil, fmt.Errorf("etcd stm: prefix scan not supported; use explicit gets")
}
