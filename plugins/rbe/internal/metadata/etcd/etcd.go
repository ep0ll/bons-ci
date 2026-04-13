// Package etcd implements metadata.Store backed by etcd v3.
package etcd

import (
	"context"
	"fmt"
	"time"

	clientv3 "go.etcd.io/etcd/client/v3"
	"go.uber.org/zap"

	"github.com/bons/bons-ci/plugins/rbe/internal/metadata"
)

// Config holds the etcd client configuration.
type Config struct {
	Endpoints   []string
	DialTimeout time.Duration
	Username    string
	Password    string
	// TLS config paths
	CertFile string
	KeyFile  string
	CAFile   string
	// Namespace prefix applied to every key (useful for multi-tenant setups).
	Namespace string
}

// Store is the etcd implementation of metadata.Store.
type Store struct {
	client *clientv3.Client
	ns     string // namespace prefix
	log    *zap.Logger
}

// New creates a new etcd Store.
func New(cfg Config, log *zap.Logger) (*Store, error) {
	if cfg.DialTimeout == 0 {
		cfg.DialTimeout = 5 * time.Second
	}

	etcdCfg := clientv3.Config{
		Endpoints:   cfg.Endpoints,
		DialTimeout: cfg.DialTimeout,
		Username:    cfg.Username,
		Password:    cfg.Password,
		Logger:      log.Named("etcd-driver"),
	}

	if cfg.CertFile != "" {
		tlsCfg, err := buildTLSConfig(cfg.CertFile, cfg.KeyFile, cfg.CAFile)
		if err != nil {
			return nil, fmt.Errorf("etcd tls: %w", err)
		}
		etcdCfg.TLS = tlsCfg
	}

	client, err := clientv3.New(etcdCfg)
	if err != nil {
		return nil, fmt.Errorf("etcd connect: %w", err)
	}

	ns := cfg.Namespace
	if ns != "" && ns[len(ns)-1] != '/' {
		ns += "/"
	}

	return &Store{client: client, ns: ns, log: log.Named("etcd")}, nil
}

// ─── key helpers ─────────────────────────────────────────────────────────────

func (s *Store) k(key []byte) string { return s.ns + string(key) }
func (s *Store) end(key []byte) string {
	e := metadata.PrefixEnd([]byte(s.k(key)))
	if e == nil {
		return clientv3.GetPrefixRangeEnd(s.k(key))
	}
	return string(e)
}

// ─── Point operations ────────────────────────────────────────────────────────

func (s *Store) Get(ctx context.Context, key []byte) ([]byte, error) {
	resp, err := s.client.Get(ctx, s.k(key))
	if err != nil {
		return nil, fmt.Errorf("etcd get: %w", err)
	}
	if len(resp.Kvs) == 0 {
		return nil, metadata.ErrNotFound
	}
	return resp.Kvs[0].Value, nil
}

func (s *Store) Put(ctx context.Context, key, value []byte) error {
	_, err := s.client.Put(ctx, s.k(key), string(value))
	if err != nil {
		return fmt.Errorf("etcd put: %w", err)
	}
	return nil
}

func (s *Store) PutWithTTL(ctx context.Context, key, value []byte, ttl time.Duration) error {
	leaseID, err := s.GrantLease(ctx, ttl)
	if err != nil {
		return err
	}
	_, err = s.client.Put(ctx, s.k(key), string(value), clientv3.WithLease(clientv3.LeaseID(leaseID)))
	if err != nil {
		return fmt.Errorf("etcd put ttl: %w", err)
	}
	return nil
}

func (s *Store) Delete(ctx context.Context, key []byte) error {
	_, err := s.client.Delete(ctx, s.k(key))
	return err
}

// ─── Atomic CAS ──────────────────────────────────────────────────────────────

func (s *Store) CompareAndSwap(ctx context.Context, key, expected, newValue []byte) (bool, error) {
	k := s.k(key)
	txnResp, err := s.client.Txn(ctx).
		If(clientv3.Compare(clientv3.Value(k), "=", string(expected))).
		Then(clientv3.OpPut(k, string(newValue))).
		Commit()
	if err != nil {
		return false, fmt.Errorf("etcd cas: %w", err)
	}
	return txnResp.Succeeded, nil
}

func (s *Store) CompareAndDelete(ctx context.Context, key, expected []byte) (bool, error) {
	k := s.k(key)
	txnResp, err := s.client.Txn(ctx).
		If(clientv3.Compare(clientv3.Value(k), "=", string(expected))).
		Then(clientv3.OpDelete(k)).
		Commit()
	if err != nil {
		return false, fmt.Errorf("etcd cad: %w", err)
	}
	return txnResp.Succeeded, nil
}

// ─── Range operations ────────────────────────────────────────────────────────

func (s *Store) Scan(ctx context.Context, start, end []byte, limit int) ([]metadata.KV, error) {
	opts := []clientv3.OpOption{clientv3.WithRange(s.ns + string(end))}
	if limit > 0 {
		opts = append(opts, clientv3.WithLimit(int64(limit)))
	}
	resp, err := s.client.Get(ctx, s.ns+string(start), opts...)
	if err != nil {
		return nil, fmt.Errorf("etcd scan: %w", err)
	}
	kvs := make([]metadata.KV, len(resp.Kvs))
	for i, kv := range resp.Kvs {
		kvs[i] = metadata.KV{
			Key:         []byte(string(kv.Key)[len(s.ns):]),
			Value:       kv.Value,
			ModRevision: kv.ModRevision,
		}
	}
	return kvs, nil
}

func (s *Store) ScanPrefix(ctx context.Context, prefix []byte, limit int) ([]metadata.KV, error) {
	opts := []clientv3.OpOption{clientv3.WithPrefix()}
	if limit > 0 {
		opts = append(opts, clientv3.WithLimit(int64(limit)))
	}
	resp, err := s.client.Get(ctx, s.k(prefix), opts...)
	if err != nil {
		return nil, fmt.Errorf("etcd scan prefix: %w", err)
	}
	kvs := make([]metadata.KV, len(resp.Kvs))
	for i, kv := range resp.Kvs {
		kvs[i] = metadata.KV{
			Key:         []byte(string(kv.Key)[len(s.ns):]),
			Value:       kv.Value,
			ModRevision: kv.ModRevision,
		}
	}
	return kvs, nil
}

func (s *Store) DeleteRange(ctx context.Context, start, end []byte) (int64, error) {
	resp, err := s.client.Delete(ctx, s.ns+string(start),
		clientv3.WithRange(s.ns+string(end)))
	if err != nil {
		return 0, fmt.Errorf("etcd delete range: %w", err)
	}
	return resp.Deleted, nil
}

// ─── Transactions ────────────────────────────────────────────────────────────

func (s *Store) Txn(ctx context.Context) (metadata.Txn, error) {
	return &etcdTxn{store: s, ctx: ctx, ops: []clientv3.Op{}}, nil
}

type etcdTxn struct {
	store *Store
	ctx   context.Context
	ops   []clientv3.Op
	// snapshot reads — we read eagerly to support Get-within-Txn
}

func (t *etcdTxn) Get(key []byte) ([]byte, error) {
	resp, err := t.store.client.Get(t.ctx, t.store.k(key))
	if err != nil {
		return nil, err
	}
	if len(resp.Kvs) == 0 {
		return nil, metadata.ErrNotFound
	}
	return resp.Kvs[0].Value, nil
}

func (t *etcdTxn) Put(key, value []byte) error {
	t.ops = append(t.ops, clientv3.OpPut(t.store.k(key), string(value)))
	return nil
}

func (t *etcdTxn) PutWithTTL(key, value []byte, ttl time.Duration) error {
	leaseID, err := t.store.GrantLease(t.ctx, ttl)
	if err != nil {
		return err
	}
	t.ops = append(t.ops, clientv3.OpPut(t.store.k(key), string(value),
		clientv3.WithLease(clientv3.LeaseID(leaseID))))
	return nil
}

func (t *etcdTxn) Delete(key []byte) error {
	t.ops = append(t.ops, clientv3.OpDelete(t.store.k(key)))
	return nil
}

func (t *etcdTxn) Scan(start, end []byte, limit int) ([]metadata.KV, error) {
	return t.store.Scan(t.ctx, start, end, limit)
}

func (t *etcdTxn) Commit() error {
	if len(t.ops) == 0 {
		return nil
	}
	resp, err := t.store.client.Txn(t.ctx).Then(t.ops...).Commit()
	if err != nil {
		return fmt.Errorf("etcd txn commit: %w", err)
	}
	if !resp.Succeeded {
		return metadata.ErrTxnConflict
	}
	return nil
}

// ─── Watch ───────────────────────────────────────────────────────────────────

func (s *Store) Watch(ctx context.Context, key []byte, fn func(metadata.Event) error) error {
	wch := s.client.Watch(ctx, s.k(key))
	return s.drainWatch(wch, fn)
}

func (s *Store) WatchPrefix(ctx context.Context, prefix []byte, fn func(metadata.Event) error) error {
	wch := s.client.Watch(ctx, s.k(prefix), clientv3.WithPrefix())
	return s.drainWatch(wch, fn)
}

func (s *Store) drainWatch(wch clientv3.WatchChan, fn func(metadata.Event) error) error {
	for wr := range wch {
		if wr.Err() != nil {
			return wr.Err()
		}
		for _, ev := range wr.Events {
			me := metadata.Event{
				KV: metadata.KV{
					Key:         []byte(string(ev.Kv.Key)[len(s.ns):]),
					Value:       ev.Kv.Value,
					ModRevision: ev.Kv.ModRevision,
				},
			}
			if ev.Type == clientv3.EventTypeDelete {
				me.Type = metadata.EventDelete
			} else {
				me.Type = metadata.EventPut
			}
			if ev.PrevKv != nil {
				prev := metadata.KV{
					Key:         []byte(string(ev.PrevKv.Key)[len(s.ns):]),
					Value:       ev.PrevKv.Value,
					ModRevision: ev.PrevKv.ModRevision,
				}
				me.PrevKV = &prev
			}
			if err := fn(me); err != nil {
				return err
			}
		}
	}
	return nil
}

// ─── Leases ──────────────────────────────────────────────────────────────────

func (s *Store) GrantLease(ctx context.Context, ttl time.Duration) (int64, error) {
	resp, err := s.client.Grant(ctx, int64(ttl.Seconds()))
	if err != nil {
		return 0, fmt.Errorf("etcd grant lease: %w", err)
	}
	return int64(resp.ID), nil
}

func (s *Store) KeepAlive(ctx context.Context, leaseID int64) error {
	_, err := s.client.KeepAliveOnce(ctx, clientv3.LeaseID(leaseID))
	if err != nil {
		return metadata.ErrLeaseExpired
	}
	return nil
}

func (s *Store) RevokeLease(ctx context.Context, leaseID int64) error {
	_, err := s.client.Revoke(ctx, clientv3.LeaseID(leaseID))
	return err
}

func (s *Store) PutWithLease(ctx context.Context, key, value []byte, leaseID int64) error {
	_, err := s.client.Put(ctx, s.k(key), string(value),
		clientv3.WithLease(clientv3.LeaseID(leaseID)))
	return err
}

func (s *Store) Close() error {
	return s.client.Close()
}

// ─── TLS helper ──────────────────────────────────────────────────────────────

func buildTLSConfig(certFile, keyFile, caFile string) (interface{}, error) {
	// Uses crypto/tls — omitted for brevity; real impl loads PEM files.
	// Return *tls.Config.
	return nil, nil
}
