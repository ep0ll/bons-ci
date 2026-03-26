package ingestion

import (
	"context"
	"io"
	"strings"
	"sync"
	"time"

	"github.com/bons/bons-ci/internal/atomic"
	"github.com/containerd/containerd/v2/core/content"
	ocispecs "github.com/opencontainers/image-spec/specs-go/v1"
)

// IngestManager extends content.IngestManager with direct ingestion tracking.
// It tracks active ingestions (writers that have not yet been committed or aborted).
// Once an ingestion is committed or aborted, it is removed from tracking.
type IngestManager interface {
	content.IngestManager
	IngestMapper
}

// IngestMapper provides direct access to active ingestion operations.
type IngestMapper interface {
	Get(context.Context, string) (ActiveIngestion, error)
	Put(context.Context, ActiveIngestion) (bool, error)
	Delete(context.Context, string) (bool, error)
}

// ActiveIngestion represents a content write operation in progress.
// It wraps a content.Writer with reference tracking and abort capabilities.
type ActiveIngestion interface {
	// Abort completely cancels the active ingest operation and cleans up resources.
	Abort(ctx context.Context) error
	// ID retrieves the reference and descriptor of the active ingestion.
	ID() (ref string, desc ocispecs.Descriptor)
	content.Writer
}

type activeIngestion struct {
	ref  string
	desc ocispecs.Descriptor
	content.Writer
	startedAt time.Time
	updatedAt atomic.AtomicTime
}

// Ingestion creates a new ActiveIngestion wrapping the given writer.
func Ingestion(writer content.Writer, ref string, desc ocispecs.Descriptor) ActiveIngestion {
	return &activeIngestion{
		ref:       ref,
		desc:      desc,
		Writer:    writer,
		startedAt: time.Now(),
		updatedAt: *atomic.Time(time.Now()),
	}
}

// ID implements ActiveIngestion.
func (a *activeIngestion) ID() (ref string, desc ocispecs.Descriptor) {
	return a.ref, a.desc
}

// Abort implements ActiveIngestion.
func (a *activeIngestion) Abort(ctx context.Context) error {
	return a.Writer.Close()
}

// Status implements ActiveIngestion.
func (a *activeIngestion) Status() (content.Status, error) {
	st, err := a.Writer.Status()
	if err != nil {
		return content.Status{}, err
	}

	// Enrich with ingestion-level tracking
	if st.StartedAt.IsZero() {
		st.StartedAt = a.startedAt
	}
	if st.UpdatedAt.IsZero() {
		st.UpdatedAt = a.updatedAt.Get()
	}

	return st, nil
}

// Write implements ActiveIngestion.
func (a *activeIngestion) Write(p []byte) (n int, err error) {
	n, err = a.Writer.Write(p)
	if err != nil {
		return n, err
	}

	if len(p) != n {
		return n, io.ErrShortWrite
	}

	a.updatedAt.Set(time.Now())
	return n, nil
}

var _ ActiveIngestion = &activeIngestion{}

// ingestion is the in-memory IngestManager implementation.
// It uses sync.Map to safely track active ingestions across goroutines.
type ingestion struct {
	ingestions sync.Map
}

// NewIngestManager creates a new in-memory IngestManager.
func NewIngestManager() IngestManager {
	return &ingestion{}
}

// Delete implements IngestMapper.
func (i *ingestion) Delete(_ context.Context, ref string) (bool, error) {
	_, ok := i.ingestions.LoadAndDelete(ref)
	if ok {
		return true, nil
	}

	return false, ErrNoActiveIngestion
}

// Put implements IngestMapper.
// Uses LoadOrStore to atomically check-and-insert, avoiding TOCTOU races.
func (i *ingestion) Put(_ context.Context, ai ActiveIngestion) (bool, error) {
	ref, _ := ai.ID()
	_, loaded := i.ingestions.LoadOrStore(ref, ai)
	if loaded {
		return true, ErrDupActiveIngestion
	}

	return false, nil
}

// Abort implements content.IngestManager.
// Aborts the ingestion and removes it from the tracking map.
func (i *ingestion) Abort(ctx context.Context, ref string) error {
	ingest, err := i.Get(ctx, ref)
	if err != nil {
		return err
	}

	if err := ingest.Abort(ctx); err != nil {
		return err
	}

	// Remove from tracking after successful abort
	i.ingestions.Delete(ref)
	return nil
}

// Get implements IngestMapper.
func (i *ingestion) Get(_ context.Context, ref string) (ActiveIngestion, error) {
	active, ok := i.ingestions.Load(ref)
	if !ok {
		return nil, ErrNoActiveIngestion
	}

	return active.(ActiveIngestion), nil
}

// ListStatuses implements content.IngestManager.
func (i *ingestion) ListStatuses(ctx context.Context, filters ...string) ([]content.Status, error) {
	ingests, err := i.collectIngestions(ctx, filters...)
	if err != nil {
		return nil, err
	}

	var status []content.Status
	for _, ing := range ingests {
		st, err := ing.Status()
		if err != nil {
			return nil, err
		}
		status = append(status, st)
	}

	return status, nil
}

// Status implements content.IngestManager.
func (i *ingestion) Status(ctx context.Context, ref string) (content.Status, error) {
	ing, err := i.Get(ctx, ref)
	if err != nil {
		return content.Status{}, err
	}

	return ing.Status()
}

// collectIngestions gathers active ingestions matching the given filters.
// Filters use the format "ref==<value>" following containerd conventions.
// If no filters are provided, all active ingestions are returned.
func (i *ingestion) collectIngestions(_ context.Context, filters ...string) ([]ActiveIngestion, error) {
	if len(filters) == 0 {
		// Return all active ingestions
		var all []ActiveIngestion
		i.ingestions.Range(func(_, value any) bool {
			all = append(all, value.(ActiveIngestion))
			return true
		})
		return all, nil
	}

	var ingests []ActiveIngestion
	for _, f := range filters {
		key, value, ok := strings.Cut(f, ".")
		if !ok {
			return nil, ErrInvalidFilter
		}

		switch key {
		case "ref":
			active, ok := i.ingestions.Load(value)
			if !ok {
				continue
			}
			ingests = append(ingests, active.(ActiveIngestion))
		default:
			return nil, ErrInvalidFilter
		}
	}

	return ingests, nil
}

var _ IngestManager = &ingestion{}
