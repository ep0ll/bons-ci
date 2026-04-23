// Package warmpool maintains a pool of pre-launched regular OCI instances in
// STOPPED state, ready to be instantly started when a migration is needed.
//
// Why regular (non-preemptible) instances for the pool?
//
// OCI preemptible instances can only be Terminated — they do NOT support
// Stop/Start via InstanceAction.  The warm pool needs instances that can be
// stopped cheaply (storage-only billing) and started rapidly (~2–3s).
// Regular instances support this lifecycle.  When acquired, the warm instance
// is started (regular, so it won't be preempted mid-restore) and the build
// continues on it as a stable VM.
//
// Cost:
//
//	STOPPED regular instance = storage billing only (~$0.003/hour for 100GB boot vol).
//	Keeping 1 warm instance: negligible cost, saves 30–35s per migration.
//
// How it works:
//  1. On daemon startup, Ensure() launches N instances in STOPPED state.
//  2. Each warm instance has the migrator agent pre-installed (baked image).
//  3. On preemption, Acquire() calls StartInstance (~2–3s to RUNNING).
//  4. After migration, Replenish() launches a replacement in background.
package warmpool

import (
	"context"
	"encoding/json"
	"fmt"
	"os"
	"sync"
	"time"

	"github.com/oracle/oci-go-sdk/v65/core"
	"go.uber.org/zap"

	"github.com/bons/bons-ci/plugins/oracle/internal/config"
	"github.com/bons/bons-ci/plugins/oracle/internal/oci"
)

const (
	warmPoolStartTimeout = 60 * time.Second
	warmPoolStopTimeout  = 90 * time.Second
)

// WarmInstance represents a pre-launched but stopped regular instance.
type WarmInstance struct {
	OCID      string    `json:"ocid"`
	PrivateIP string    `json:"private_ip"`
	CreatedAt time.Time `json:"created_at"`
	Region    string    `json:"region"`
	Shape     string    `json:"shape"`
}

// Pool manages warm regular instances.
type Pool struct {
	mu          sync.Mutex
	instances   []*WarmInstance
	cfg         config.WarmPoolConfig
	ociCfg      config.OCIConfig
	session     *oci.Session
	instanceMgr *oci.InstanceManager
	log         *zap.Logger
}

// New constructs a Pool and loads any persisted warm instance list.
func New(cfg config.WarmPoolConfig, ociCfg config.OCIConfig, session *oci.Session, log *zap.Logger) *Pool {
	p := &Pool{
		cfg:         cfg,
		ociCfg:      ociCfg,
		session:     session,
		instanceMgr: oci.NewInstanceManager(session, log),
		log:         log,
	}
	p.load()
	return p
}

// Ensure guarantees the pool has at least MinSize healthy warm instances.
// Should be called at startup and periodically.
func (p *Pool) Ensure(ctx context.Context) error {
	p.mu.Lock()
	current := len(p.instances)
	need := p.cfg.MinSize - current
	p.mu.Unlock()

	if need <= 0 {
		return nil
	}

	p.log.Info("warm pool replenishment needed",
		zap.Int("current", current),
		zap.Int("target", p.cfg.MinSize),
		zap.Int("launching", need),
	)

	errs := make(chan error, need)
	for i := 0; i < need; i++ {
		go func() {
			inst, err := p.launchWarm(ctx)
			if err != nil {
				errs <- err
				return
			}
			p.mu.Lock()
			p.instances = append(p.instances, inst)
			p.mu.Unlock()
			p.save()
			errs <- nil
		}()
	}

	var firstErr error
	for i := 0; i < need; i++ {
		if err := <-errs; err != nil && firstErr == nil {
			firstErr = err
		}
	}
	return firstErr
}

// Acquire removes and returns the best available warm instance, starting it immediately.
// The returned instance is in RUNNING state within ~2–3 seconds.
// Returns nil, nil if the pool is empty (caller falls back to cold launch).
func (p *Pool) Acquire(ctx context.Context) (*WarmInstance, error) {
	p.mu.Lock()
	if len(p.instances) == 0 {
		p.mu.Unlock()
		p.log.Warn("warm pool empty — falling back to cold launch")
		return nil, nil
	}

	// Pick the oldest instance (FIFO) to maximise freshness of the pool.
	inst := p.instances[0]
	p.instances = p.instances[1:]
	p.mu.Unlock()
	p.save()

	p.log.Info("acquiring warm instance",
		zap.String("ocid", inst.OCID),
		zap.Duration("age", time.Since(inst.CreatedAt)),
	)

	startTime := time.Now()
	// StartInstance works only for regular instances — these pool instances
	// are NOT preemptible, so Stop/Start is fully supported by OCI.
	if err := p.instanceMgr.StartInstance(ctx, inst.OCID, warmPoolStartTimeout); err != nil {
		p.log.Error("failed to start warm instance — returning to pool for cleanup",
			zap.String("ocid", inst.OCID),
			zap.Error(err),
		)
		// Return to pool tail for cleanup by Prune() later.
		p.mu.Lock()
		p.instances = append(p.instances, inst)
		p.mu.Unlock()
		return nil, nil // caller will cold-launch
	}

	p.log.Info("warm instance started",
		zap.String("ocid", inst.OCID),
		zap.Duration("start_time", time.Since(startTime)),
	)

	// Replenish the pool asynchronously so the next migration is covered.
	go func() {
		replenishCtx, cancel := context.WithTimeout(context.Background(), 5*time.Minute)
		defer cancel()
		if err := p.Ensure(replenishCtx); err != nil {
			p.log.Warn("pool replenishment failed", zap.Error(err))
		}
	}()

	return inst, nil
}

// Prune removes and terminates warm instances older than MaxAge.
func (p *Pool) Prune(ctx context.Context) {
	p.mu.Lock()
	var keep []*WarmInstance
	var stale []*WarmInstance
	for _, inst := range p.instances {
		if time.Since(inst.CreatedAt) > p.cfg.MaxAge {
			stale = append(stale, inst)
		} else {
			keep = append(keep, inst)
		}
	}
	p.instances = keep
	p.mu.Unlock()

	for _, inst := range stale {
		p.log.Info("pruning stale warm instance",
			zap.String("ocid", inst.OCID),
			zap.Duration("age", time.Since(inst.CreatedAt)),
		)
		go func(ocid string) {
			// Regular instances can be terminated normally.
			preserveBoot := false
			_, _ = p.session.Compute.TerminateInstance(ctx, core.TerminateInstanceRequest{
				InstanceId:         &ocid,
				PreserveBootVolume: &preserveBoot,
			})
		}(inst.OCID)
	}

	if len(stale) > 0 {
		p.save()
		go func() {
			repCtx, cancel := context.WithTimeout(context.Background(), 5*time.Minute)
			defer cancel()
			_ = p.Ensure(repCtx)
		}()
	}
}

// StartBackground runs pool maintenance (prune + ensure) on a ticker.
func (p *Pool) StartBackground(ctx context.Context) {
	go func() {
		ticker := time.NewTicker(p.cfg.MaintenanceInterval)
		defer ticker.Stop()

		// Initial ensure at startup — fill the pool before any migration.
		if err := p.Ensure(ctx); err != nil {
			p.log.Warn("initial pool ensure failed", zap.Error(err))
		}

		for {
			select {
			case <-ticker.C:
				p.Prune(ctx)
				if err := p.Ensure(ctx); err != nil {
					p.log.Warn("pool maintenance failed", zap.Error(err))
				}
			case <-ctx.Done():
				return
			}
		}
	}()
}

// Size returns current pool depth (thread-safe).
func (p *Pool) Size() int {
	p.mu.Lock()
	defer p.mu.Unlock()
	return len(p.instances)
}

// ────────────────────────────────────────────────────────────────────────────

// launchWarm launches a regular (non-preemptible) instance and immediately
// stops it so it enters STOPPED state for cheap storage-only billing.
// Regular instances support Stop/Start; preemptible instances do not.
func (p *Pool) launchWarm(ctx context.Context) (*WarmInstance, error) {
	ocid, err := p.instanceMgr.LaunchSuccessor(ctx, oci.LaunchSuccessorOptions{
		DisplayName: fmt.Sprintf("cicd-warm-%s", time.Now().Format("150405")),
		ImageOCID:   p.ociCfg.ImageOCID,
		// Preemptible: false — MUST be false for warm pool.
		// Preemptible instances cannot be stopped and restarted.
		Preemptible: false,
		// No PrivateIP — pool instances get auto-assigned IPs.
		// The IP will be different from the source; that's expected.
		FreeformTags: map[string]string{
			"oci-migrator-role": "warm-pool",
			"oci-migrator-ts":   time.Now().UTC().Format(time.RFC3339),
			"managed-by":        "oci-live-migrator",
		},
		Timeout: 2 * time.Minute,
	})
	if err != nil {
		return nil, fmt.Errorf("launching warm instance: %w", err)
	}

	// Fetch IP before stopping (it remains assigned even when stopped).
	ip, _ := p.instanceMgr.GetInstancePrivateIPNoCache(ctx, ocid)

	// Stop the instance — regular instances support this, preemptible don't.
	if err := p.instanceMgr.StopInstance(ctx, ocid, warmPoolStopTimeout); err != nil {
		// Terminate rather than leave running (we'd pay for idle compute).
		preserveBoot := false
		_, _ = p.session.Compute.TerminateInstance(ctx, core.TerminateInstanceRequest{
			InstanceId:         &ocid,
			PreserveBootVolume: &preserveBoot,
		})
		return nil, fmt.Errorf("stopping warm instance %s: %w", ocid, err)
	}

	p.log.Info("warm instance ready (STOPPED)",
		zap.String("ocid", ocid),
		zap.String("ip", ip),
	)

	return &WarmInstance{
		OCID:      ocid,
		PrivateIP: ip,
		CreatedAt: time.Now(),
		Region:    p.ociCfg.Region,
		Shape:     p.ociCfg.Shape,
	}, nil
}

func (p *Pool) save() {
	if p.cfg.StatePath == "" {
		return
	}
	p.mu.Lock()
	data, err := json.MarshalIndent(p.instances, "", "  ")
	p.mu.Unlock()
	if err != nil {
		return
	}
	tmp := p.cfg.StatePath + ".tmp"
	_ = os.WriteFile(tmp, data, 0o600)
	_ = os.Rename(tmp, p.cfg.StatePath)
}

func (p *Pool) load() {
	if p.cfg.StatePath == "" {
		return
	}
	data, err := os.ReadFile(p.cfg.StatePath)
	if err != nil {
		return // first run, no state file
	}
	var insts []*WarmInstance
	if err := json.Unmarshal(data, &insts); err != nil {
		p.log.Warn("warm pool state file corrupted — starting fresh", zap.Error(err))
		return
	}
	p.mu.Lock()
	p.instances = insts
	p.mu.Unlock()
	p.log.Info("loaded warm pool state", zap.Int("size", len(insts)))
}
