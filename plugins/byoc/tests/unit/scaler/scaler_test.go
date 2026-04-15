package scaler_test

import (
	"context"
	"fmt"
	"testing"
	"time"

	"github.com/bons/bons-ci/plugins/byoc/internal/observability"
	"github.com/bons/bons-ci/plugins/byoc/internal/scaler"
	"github.com/bons/bons-ci/plugins/byoc/internal/store"
	"github.com/prometheus/client_golang/prometheus"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"golang.org/x/time/rate"
)

// ── In-memory store stub for unit tests ───────────────────────────────────────

type memStore struct {
	tenants map[string]*store.Tenant
	runners []*store.Runner
	locks   map[string]struct{}
}

func newMemStore() *memStore {
	return &memStore{
		tenants: make(map[string]*store.Tenant),
		runners: make([]*store.Runner, 0),
		locks:   make(map[string]struct{}),
	}
}

func (m *memStore) CreateTenant(_ context.Context, t *store.Tenant) error {
	m.tenants[t.ID] = t
	return nil
}
func (m *memStore) GetTenant(_ context.Context, id string) (*store.Tenant, error) {
	t, ok := m.tenants[id]
	if !ok {
		return nil, store.ErrNotFound
	}
	return t, nil
}
func (m *memStore) UpdateTenant(_ context.Context, t *store.Tenant) error {
	m.tenants[t.ID] = t
	return nil
}
func (m *memStore) ListTenants(_ context.Context, _ store.TenantFilter) ([]*store.Tenant, error) {
	var out []*store.Tenant
	for _, t := range m.tenants {
		out = append(out, t)
	}
	return out, nil
}
func (m *memStore) CreateRunner(_ context.Context, r *store.Runner) error {
	m.runners = append(m.runners, r)
	return nil
}
func (m *memStore) GetRunner(_ context.Context, id string) (*store.Runner, error) {
	for _, r := range m.runners {
		if r.ID == id {
			return r, nil
		}
	}
	return nil, store.ErrNotFound
}
func (m *memStore) UpdateRunnerStatus(_ context.Context, id string, status store.RunnerStatus, _ store.RunnerUpdateOpts) error {
	for _, r := range m.runners {
		if r.ID == id {
			r.Status = status
			return nil
		}
	}
	return store.ErrNotFound
}
func (m *memStore) ListRunners(_ context.Context, f store.RunnerFilter) ([]*store.Runner, error) {
	var out []*store.Runner
	for _, r := range m.runners {
		if f.TenantID != nil && r.TenantID != *f.TenantID {
			continue
		}
		if f.Status != nil && r.Status != *f.Status {
			continue
		}
		out = append(out, r)
		if f.Limit > 0 && len(out) >= f.Limit {
			break
		}
	}
	return out, nil
}
func (m *memStore) CountRunners(_ context.Context, f store.RunnerFilter) (int64, error) {
	var count int64
	for _, r := range m.runners {
		if f.TenantID != nil && r.TenantID != *f.TenantID {
			continue
		}
		if f.Status != nil && r.Status != *f.Status {
			continue
		}
		count++
	}
	return count, nil
}
func (m *memStore) DeleteRunner(_ context.Context, id string) error {
	for i, r := range m.runners {
		if r.ID == id {
			m.runners = append(m.runners[:i], m.runners[i+1:]...)
			return nil
		}
	}
	return nil
}
func (m *memStore) AcquireIdempotencyLock(_ context.Context, key string, _ time.Duration) (bool, error) {
	if _, exists := m.locks[key]; exists {
		return false, nil
	}
	m.locks[key] = struct{}{}
	return true, nil
}
func (m *memStore) Ping(_ context.Context) error { return nil }

// ── Helpers ───────────────────────────────────────────────────────────────────

func newTestScaler(t *testing.T, s store.Store) *scaler.Scaler {
	t.Helper()
	reg := prometheus.NewRegistry()
	metrics := observability.NewMetrics(reg)
	cfg := scaler.Config{
		// High burst so rate limiter does not interfere with most tests.
		DefaultProvisionRate:  rate.Every(time.Millisecond),
		DefaultProvisionBurst: 1000,
		IdempotencyTTL:        time.Hour,
	}
	return scaler.New(s, metrics, cfg, observability.NewLogger(observability.LogConfig{Level: "error"}))
}

func activeTenant(maxRunners int) *store.Tenant {
	return &store.Tenant{
		ID:             "test-tenant",
		Status:         store.TenantStatusActive,
		MaxRunners:     maxRunners,
		IdleTimeoutSec: 300,
	}
}

// ── Tests ─────────────────────────────────────────────────────────────────────

func TestScaleUp_NewJob_NoRunners_ShouldProvision(t *testing.T) {
	s := newMemStore()
	sc := newTestScaler(t, s)
	tenant := activeTenant(10)

	result, err := sc.ScaleUp(context.Background(), tenant, 1001)
	require.NoError(t, err)
	assert.Equal(t, scaler.DecisionProvision, result.Decision)
}

func TestScaleUp_DuplicateEvent_ShouldReturnDuplicate(t *testing.T) {
	s := newMemStore()
	sc := newTestScaler(t, s)
	tenant := activeTenant(10)

	// First call acquires the lock.
	r1, err := sc.ScaleUp(context.Background(), tenant, 2002)
	require.NoError(t, err)
	assert.Equal(t, scaler.DecisionProvision, r1.Decision)

	// Second call with same job ID should be a duplicate.
	r2, err := sc.ScaleUp(context.Background(), tenant, 2002)
	require.NoError(t, err)
	assert.Equal(t, scaler.DecisionDuplicate, r2.Decision)
}

func TestScaleUp_IdleRunnerAvailable_ShouldAssignIdle(t *testing.T) {
	s := newMemStore()
	idleSince := time.Now().Add(-10 * time.Second)
	_ = s.CreateRunner(context.Background(), &store.Runner{
		ID:        "idle-runner-1",
		TenantID:  "test-tenant",
		Status:    store.RunnerStatusIdle,
		IdleSince: &idleSince,
	})

	sc := newTestScaler(t, s)
	tenant := activeTenant(10)

	result, err := sc.ScaleUp(context.Background(), tenant, 3003)
	require.NoError(t, err)
	assert.Equal(t, scaler.DecisionAssignIdle, result.Decision)
	assert.Equal(t, "idle-runner-1", result.IdleRunnerID)
}

func TestScaleUp_MaxRunnersReached_ShouldEnqueue(t *testing.T) {
	s := newMemStore()
	ctx := context.Background()

	// Fill the tenant to its max (2 runners).
	for i := 0; i < 2; i++ {
		_ = s.CreateRunner(ctx, &store.Runner{
			ID:       fmt.Sprintf("runner-%d", i),
			TenantID: "test-tenant",
			Status:   store.RunnerStatusBusy,
		})
	}

	sc := newTestScaler(t, s)
	tenant := activeTenant(2) // max = 2

	result, err := sc.ScaleUp(ctx, tenant, 4004)
	require.NoError(t, err)
	assert.Equal(t, scaler.DecisionEnqueue, result.Decision)
}

func TestScaleUp_RateLimited_ShouldReturnRateLimited(t *testing.T) {
	s := newMemStore()
	reg := prometheus.NewRegistry()
	metrics := observability.NewMetrics(reg)

	// Rate limiter with burst=1 — first call succeeds, subsequent calls fail.
	cfg := scaler.Config{
		DefaultProvisionRate:  rate.Every(time.Hour), // very slow refill
		DefaultProvisionBurst: 1,
		IdempotencyTTL:        time.Hour,
	}
	sc := scaler.New(s, metrics, cfg, observability.NewLogger(observability.LogConfig{Level: "error"}))
	tenant := activeTenant(100)

	// First call consumes the single token.
	r1, err := sc.ScaleUp(context.Background(), tenant, 5001)
	require.NoError(t, err)
	assert.Equal(t, scaler.DecisionProvision, r1.Decision)

	// Second call — bucket empty → rate limited.
	r2, err := sc.ScaleUp(context.Background(), tenant, 5002)
	require.NoError(t, err)
	assert.Equal(t, scaler.DecisionRateLimited, r2.Decision)
}

func TestScaleDown_IdleRunnerPastTimeout_ShouldReturnForTermination(t *testing.T) {
	s := newMemStore()
	ctx := context.Background()

	oldIdleSince := time.Now().Add(-10 * time.Minute) // past 5 min idle timeout
	_ = s.CreateRunner(ctx, &store.Runner{
		ID:        "stale-runner",
		TenantID:  "test-tenant",
		Status:    store.RunnerStatusIdle,
		IdleSince: &oldIdleSince,
	})

	recentIdle := time.Now().Add(-1 * time.Minute) // within timeout
	_ = s.CreateRunner(ctx, &store.Runner{
		ID:        "fresh-runner",
		TenantID:  "test-tenant",
		Status:    store.RunnerStatusIdle,
		IdleSince: &recentIdle,
	})

	reg := prometheus.NewRegistry()
	metrics := observability.NewMetrics(reg)
	sc := scaler.New(s, metrics, scaler.Config{}, observability.NewLogger(observability.LogConfig{Level: "error"}))

	tenant := &store.Tenant{
		ID:             "test-tenant",
		Status:         store.TenantStatusActive,
		MaxRunners:     10,
		IdleTimeoutSec: 300, // 5 minutes
	}

	toTerminate, err := sc.ScaleDown(ctx, tenant)
	require.NoError(t, err)
	assert.Len(t, toTerminate, 1)
	assert.Equal(t, "stale-runner", toTerminate[0])
}

func TestScaleDown_NoIdleRunners_ShouldReturnEmpty(t *testing.T) {
	s := newMemStore()
	ctx := context.Background()

	_ = s.CreateRunner(ctx, &store.Runner{
		ID:       "busy-runner",
		TenantID: "test-tenant",
		Status:   store.RunnerStatusBusy,
	})

	reg := prometheus.NewRegistry()
	metrics := observability.NewMetrics(reg)
	sc := scaler.New(s, metrics, scaler.Config{}, observability.NewLogger(observability.LogConfig{Level: "error"}))

	tenant := &store.Tenant{
		ID: "test-tenant", Status: store.TenantStatusActive,
		MaxRunners: 10, IdleTimeoutSec: 300,
	}

	toTerminate, err := sc.ScaleDown(ctx, tenant)
	require.NoError(t, err)
	assert.Empty(t, toTerminate)
}
