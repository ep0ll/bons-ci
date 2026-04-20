//go:build linux

// Package pipeline provides a composable, back-pressured stage pipeline for
// transforming fanotify events into hash results.
package pipeline

import (
	"context"
	"fmt"
	"sync"
	"sync/atomic"
	"time"

	"github.com/bons/bons-ci/plugins/checksummer/pkg/fanotify"
	"github.com/bons/bons-ci/plugins/checksummer/pkg/filekey"
	"github.com/bons/bons-ci/plugins/checksummer/pkg/hooks"
	"github.com/bons/bons-ci/plugins/checksummer/pkg/metrics"
)

// ─────────────────────────── Item ────────────────────────────────────────────

// Item flows through the pipeline stages, accumulating results.
type Item struct {
	Event       *fanotify.Event
	Key         filekey.Key
	Hash        []byte
	FileSize    int64
	MtimeNs     int64
	Path        string
	Err         error
	EnqueuedAt  time.Time
	ProcessedAt time.Time
}

// Done marks the item processed and closes its event fd.
func (it *Item) Done() {
	it.ProcessedAt = time.Now()
	if it.Event != nil {
		_ = it.Event.Close()
	}
}

// ─────────────────────────── Stage ───────────────────────────────────────────

// StageFn processes one Item.  Non-nil error marks the item failed.
type StageFn func(ctx context.Context, item *Item) error

// Stage is one named processing step.
type Stage struct {
	Name    string
	Fn      StageFn
	Workers int
	BufSize int
	in      chan *Item
	enabled atomic.Bool
	dropped atomic.Int64
	errors  atomic.Int64
}

// NewStage creates a Stage.
func NewStage(name string, fn StageFn, workers, bufSize int) *Stage {
	if workers <= 0 {
		workers = 1
	}
	if bufSize <= 0 {
		bufSize = 256
	}
	s := &Stage{
		Name:    name,
		Fn:      fn,
		Workers: workers,
		BufSize: bufSize,
		in:      make(chan *Item, bufSize),
	}
	s.enabled.Store(true)
	return s
}

func (s *Stage) Enable()         { s.enabled.Store(true) }
func (s *Stage) Disable()        { s.enabled.Store(false) }
func (s *Stage) IsEnabled() bool { return s.enabled.Load() }
func (s *Stage) Dropped() int64  { return s.dropped.Load() }
func (s *Stage) Errors() int64   { return s.errors.Load() }

// ─────────────────────────── Pipeline ────────────────────────────────────────

// Pipeline chains Stages end-to-end.  Safe for concurrent use.
type Pipeline struct {
	mu      sync.RWMutex
	stages  []*Stage
	hooks   *hooks.HookSet
	metrics *metrics.Recorder
	cancel  context.CancelFunc
	wg      sync.WaitGroup
	running atomic.Bool
}

// PipelineOption configures a Pipeline.
type PipelineOption func(*Pipeline)

// WithHooks injects a hook set.
func WithHooks(hs *hooks.HookSet) PipelineOption { return func(p *Pipeline) { p.hooks = hs } }

// WithMetrics injects a metrics recorder.
func WithMetrics(m *metrics.Recorder) PipelineOption { return func(p *Pipeline) { p.metrics = m } }

// New creates a Pipeline.
func New(opts ...PipelineOption) *Pipeline {
	p := &Pipeline{
		hooks:   hooks.NewHookSet(),
		metrics: &metrics.Recorder{},
	}
	for _, o := range opts {
		o(p)
	}
	return p
}

// Append adds a stage at the end of the chain.
func (p *Pipeline) Append(s *Stage) {
	p.mu.Lock()
	p.stages = append(p.stages, s)
	p.mu.Unlock()
}

// Insert adds a stage at position idx.
func (p *Pipeline) Insert(idx int, s *Stage) error {
	p.mu.Lock()
	defer p.mu.Unlock()
	if idx < 0 || idx > len(p.stages) {
		return fmt.Errorf("pipeline: insert idx %d out of range [0,%d]", idx, len(p.stages))
	}
	tail := append([]*Stage{}, p.stages[idx:]...)
	p.stages = append(p.stages[:idx], s)
	p.stages = append(p.stages, tail...)
	return nil
}

// Remove removes the first stage with the given name.
func (p *Pipeline) Remove(name string) bool {
	p.mu.Lock()
	defer p.mu.Unlock()
	for i, s := range p.stages {
		if s.Name == name {
			p.stages = append(p.stages[:i], p.stages[i+1:]...)
			return true
		}
	}
	return false
}

// Start launches all stage workers.  Idempotent.
func (p *Pipeline) Start(ctx context.Context) {
	if !p.running.CompareAndSwap(false, true) {
		return
	}
	pctx, cancel := context.WithCancel(ctx)
	p.cancel = cancel

	p.mu.RLock()
	snapshot := make([]*Stage, len(p.stages))
	copy(snapshot, p.stages)
	p.mu.RUnlock()

	for i, stage := range snapshot {
		var next chan *Item
		if i+1 < len(snapshot) {
			next = snapshot[i+1].in
		}
		for range stage.Workers {
			p.wg.Add(1)
			go p.runWorker(pctx, stage, next)
		}
	}
}

// Stop drains and shuts down all stages.
func (p *Pipeline) Stop() {
	if !p.running.CompareAndSwap(true, false) {
		return
	}
	if p.cancel != nil {
		p.cancel()
	}
	p.mu.RLock()
	for _, s := range p.stages {
		close(s.in)
	}
	p.mu.RUnlock()
	p.wg.Wait()
}

// Dispatch sends an item to the first stage.
// drop=true drops the item (closes fd) if the channel is full.
func (p *Pipeline) Dispatch(item *Item, drop bool) bool {
	p.mu.RLock()
	stages := p.stages
	p.mu.RUnlock()
	if len(stages) == 0 {
		item.Done()
		return true
	}
	item.EnqueuedAt = time.Now()
	if drop {
		select {
		case stages[0].in <- item:
			return true
		default:
			stages[0].dropped.Add(1)
			item.Done()
			return false
		}
	}
	stages[0].in <- item
	return true
}

func (p *Pipeline) runWorker(ctx context.Context, stage *Stage, next chan<- *Item) {
	defer p.wg.Done()
	for item := range stage.in {
		if ctx.Err() != nil {
			item.Done()
			continue
		}
		if stage.IsEnabled() {
			if err := stage.Fn(ctx, item); err != nil {
				item.Err = err
				stage.errors.Add(1)
				_ = p.hooks.OnError.Execute(ctx, hooks.ErrorPayload{
					Op: stage.Name, Key: item.Key.String(), Err: err,
				}, hooks.ContinueOnError)
				item.Done()
				continue
			}
		}
		if next != nil {
			select {
			case next <- item:
			case <-ctx.Done():
				item.Done()
			}
		} else {
			item.Done()
		}
	}
}

// StageStats returns per-stage snapshots.
func (p *Pipeline) StageStats() []StageSnapshot {
	p.mu.RLock()
	defer p.mu.RUnlock()
	out := make([]StageSnapshot, len(p.stages))
	for i, s := range p.stages {
		out[i] = StageSnapshot{
			Name:    s.Name,
			Enabled: s.IsEnabled(),
			Dropped: s.Dropped(),
			Errors:  s.Errors(),
			BufUsed: len(s.in),
			BufCap:  cap(s.in),
		}
	}
	return out
}

// StageSnapshot is a point-in-time view of one stage.
type StageSnapshot struct {
	Name    string
	Enabled bool
	Dropped int64
	Errors  int64
	BufUsed int
	BufCap  int
}

func (s StageSnapshot) String() string {
	return fmt.Sprintf("stage=%q enabled=%v buf=%d/%d dropped=%d errors=%d",
		s.Name, s.Enabled, s.BufUsed, s.BufCap, s.Dropped, s.Errors)
}
