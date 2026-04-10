// Package warmpool maintains a pool of pre-launched OCI preemptible instances
// in STOPPED state, ready to be instantly started when a migration is needed.
//
// Impact on latency:
//
//	Without warm pool: successor boot = 30-50s (OCI provisioning + cloud-init)
//	With warm pool:    successor ready = ~2-3s (just a StartInstance API call)
//
// How it works:
//  1. On daemon startup, pool.Ensure() launches N instances in STOPPED state.
//  2. Each warm instance has the migrator agent pre-installed (baked into the image).
//  3. On preemption, pool.Acquire() picks a warm instance and calls StartInstance.
//  4. StartInstance on a pre-provisioned VM completes in ~2-3 seconds.
//  5. After the migration, pool.Replenish() launches a replacement in background.
//
// Cost:
//
//	STOPPED preemptible instances are billed at ~10% of running cost for storage.
//	Keeping 1-2 warm instances costs pennies per hour.
package warmpool

import (
	"context"
	"encoding/json"
	"fmt"
	"os"
	"sync"
	"time"

	"github.com/cenkalti/backoff/v4"
	"github.com/oracle/oci-go-sdk/v65/core"
	"go.uber.org/zap"

	"github.com/bons/bons-ci/plugins/oracle/internal/config"
	"github.com/bons/bons-ci/plugins/oracle/internal/oci"
)

// WarmInstance represents a pre-launched but stopped instance.
type WarmInstance struct {
	OCID      string    `json:"ocid"`
	PrivateIP string    `json:"private_ip"`
	CreatedAt time.Time `json:"created_at"`
	Region    string    `json:"region"`
	Shape     string    `json:"shape"`
}

// Pool manages warm preemptible instances.
type Pool struct {
	mu         sync.Mutex
	instances  []*WarmInstance
	cfg        config.WarmPoolConfig
	ociCfg     config.OCIConfig
	session    *oci.Session
	instances_ *oci.InstanceManager
	log        *zap.Logger
	statePath  string
}

// New constructs a Pool and loads any persisted warm instance list.
func New(cfg config.WarmPoolConfig, ociCfg config.OCIConfig, session *oci.Session, log *zap.Logger) *Pool {
	p := &Pool{
		cfg:        cfg,
		ociCfg:     ociCfg,
		session:    session,
		instances_: oci.NewInstanceManager(session, log),
		log:        log,
		statePath:  cfg.StatePath,
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
// The returned instance is in RUNNING state within ~2-3 seconds.
// Returns nil if the pool is empty (caller falls back to full launch).
func (p *Pool) Acquire(ctx context.Context, targetPrivateIP string) (*WarmInstance, error) {
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

	// Start the stopped instance — this completes in ~2-3s.
	startTime := time.Now()
	if err := p.startInstance(ctx, inst.OCID, targetPrivateIP); err != nil {
		p.log.Error("failed to start warm instance — putting back and returning nil",
			zap.String("ocid", inst.OCID),
			zap.Error(err),
		)
		// Return it to the pool for cleanup later.
		p.mu.Lock()
		p.instances = append(p.instances, inst)
		p.mu.Unlock()
		return nil, nil // caller will cold-launch
	}

	p.log.Info("warm instance started",
		zap.String("ocid", inst.OCID),
		zap.Duration("start_time", time.Since(startTime)),
	)

	// Replenish the pool in background.
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
			_, _ = p.session.Compute.TerminateInstance(ctx, core.TerminateInstanceRequest{
				InstanceId: &ocid,
			})
		}(inst.OCID)
	}

	if len(stale) > 0 {
		p.save()
		// Replenish after pruning.
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
		// Initial ensure at startup.
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

// Size returns current pool depth.
func (p *Pool) Size() int {
	p.mu.Lock()
	defer p.mu.Unlock()
	return len(p.instances)
}

// ────────────────────────────────────────────────────────────────────────────

func (p *Pool) launchWarm(ctx context.Context) (*WarmInstance, error) {
	// Launch in STOPPED state via a stopped-instance shape.
	// We use the same shape/image as the live build instances.
	ocid, err := p.instances_.LaunchSuccessor(ctx, oci.LaunchSuccessorOptions{
		DisplayName: fmt.Sprintf("cicd-warm-%s", time.Now().Format("150405")),
		ImageOCID:   p.ociCfg.ImageOCID,
		// No PrivateIP — pool instances get dynamic IPs; IP is reassigned on acquire.
		FreeformTags: map[string]string{
			"oci-migrator-role": "warm-pool",
			"oci-migrator-ts":   time.Now().UTC().Format(time.RFC3339),
		},
		Timeout: 2 * time.Minute,
	})
	if err != nil {
		return nil, fmt.Errorf("launching warm instance: %w", err)
	}

	// Stop the instance immediately after it reaches RUNNING.
	if err := p.stopInstance(ctx, ocid); err != nil {
		return nil, fmt.Errorf("stopping warm instance %s: %w", ocid, err)
	}

	// Fetch the assigned private IP (needed for migration).
	ip, _ := p.instances_.GetInstancePrivateIP(ctx, ocid)

	return &WarmInstance{
		OCID:      ocid,
		PrivateIP: ip,
		CreatedAt: time.Now(),
		Region:    p.ociCfg.Region,
		Shape:     p.ociCfg.Shape,
	}, nil
}

func (p *Pool) stopInstance(ctx context.Context, ocid string) error {
	action := core.InstanceActionActionStop
	_, err := p.session.Compute.InstanceAction(ctx, core.InstanceActionRequest{
		InstanceId: &ocid,
		Action:     action,
	})
	if err != nil {
		return err
	}
	return p.instances_.WaitForState(ctx, ocid, core.InstanceLifecycleStateStopped, 90*time.Second)
}

func (p *Pool) startInstance(ctx context.Context, ocid, targetIP string) error {
	bo := backoff.NewExponentialBackOff()
	bo.MaxElapsedTime = 30 * time.Second
	bo.InitialInterval = 500 * time.Millisecond

	return backoff.Retry(func() error {
		action := core.InstanceActionActionStart
		_, err := p.session.Compute.InstanceAction(ctx, core.InstanceActionRequest{
			InstanceId: &ocid,
			Action:     action,
		})
		if err != nil {
			return err
		}
		return nil
	}, backoff.WithContext(bo, ctx))
}

func (p *Pool) save() {
	if p.statePath == "" {
		return
	}
	p.mu.Lock()
	data, err := json.MarshalIndent(p.instances, "", "  ")
	p.mu.Unlock()
	if err != nil {
		return
	}
	tmp := p.statePath + ".tmp"
	_ = os.WriteFile(tmp, data, 0o600)
	_ = os.Rename(tmp, p.statePath)
}

func (p *Pool) load() {
	if p.statePath == "" {
		return
	}
	data, err := os.ReadFile(p.statePath)
	if err != nil {
		return
	}
	var insts []*WarmInstance
	if err := json.Unmarshal(data, &insts); err != nil {
		return
	}
	p.mu.Lock()
	p.instances = insts
	p.mu.Unlock()
	p.log.Info("loaded warm pool state", zap.Int("size", len(insts)))
}
