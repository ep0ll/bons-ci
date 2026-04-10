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
	"github.com/pkg/errors"
)

type IngestManager interface {
	content.IngestManager
	IngestMapper
}

type IngestMapper interface {
	Get(context.Context, string) (ActiveIngestion, error)
	Put(context.Context, ActiveIngestion) (bool, error)
	Delete(context.Context, string) (bool, error)
}

type ActiveIngestion interface {
	// Abort completely cancels the active ingest operation.
	Abort(ctx context.Context) error
	// ID retrives the reference and descriptor of the active ingestion
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

func Ingestion(writer content.Writer, ref string, desc ocispecs.Descriptor) ActiveIngestion {
	return &activeIngestion{
		ref:       ref,
		desc:      desc,
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
	return a.Writer.Status()
}

// Write implements ActiveIngestion.
func (a *activeIngestion) Write(p []byte) (n int, err error) {
	n, err = a.Writer.Write(p)
	if err == io.EOF {
		err = a.Writer.Commit(context.Background(), 0, "")
	}

	if err != nil && err != io.EOF {
		return n, err
	}

	if len(p) != n {
		return n, io.ErrShortWrite
	}

	a.updatedAt.Set(time.Now())
	return n, nil
}

var _ ActiveIngestion = &activeIngestion{}

type ingestion struct {
	// Map of reference to ActiveIngestion
	ingestions sync.Map
}

// Delete implements IngestManager.
func (i *ingestion) Delete(_ context.Context, ref string) (bool, error) {
	_, ok := i.ingestions.LoadAndDelete(ref)
	if ok {
		return true, nil
	}

	return false, ErrNoActiveIngestion
}

// Put implements IngestManager.
func (i *ingestion) Put(_ context.Context, ai ActiveIngestion) (bool, error) {
	ref, _ := ai.ID()
	_, ok := i.ingestions.Load(ref)
	if ok {
		return true, errors.Wrapf(ErrDupActiveIngestion, "[%s]", ref)
	}

	i.ingestions.Store(ref, ai)
	return false, nil
}

// Abort implements IngestManager.
func (i *ingestion) Abort(ctx context.Context, ref string) error {
	ingest, err := i.Get(ctx, ref)
	if err != nil {
		return err
	}

	return ingest.Abort(ctx)
}

// Get implements IngestManager.
func (i *ingestion) Get(_ context.Context, ref string) (ActiveIngestion, error) {
	active, ok := i.ingestions.Load(ref)
	if !ok {
		return nil, errors.Wrapf(ErrNoActiveIngestion, "reference: %s", ref)
	}

	return active.(ActiveIngestion), nil
}

// ListStatuses implements IngestManager.
func (i *ingestion) ListStatuses(ctx context.Context, filters ...string) (status []content.Status, err error) {
	ingests, err := i.filterIngestion(ctx, filters...)
	if err != nil {
		return nil, err
	}

	for _, ing := range ingests {
		st, err := ing.Status()
		if err != nil {
			return nil, err
		}

		status = append(status, st)
	}

	return status, nil
}

// Status implements IngestManager.
func (i *ingestion) Status(ctx context.Context, ref string) (content.Status, error) {
	ing, err := i.Get(ctx, ref)
	if err != nil {
		return content.Status{}, err
	}

	return ing.Status()
}

func (i *ingestion) filterIngestion(ctx context.Context, filters ...string) (ingests []ActiveIngestion, err error) {
	for _, f := range filters {
		switch b4, after, ok := strings.Cut(f, "."); b4 {
		case "ref":
			if !ok {
				return nil, ErrRequiredReference
			}

			active, err := i.Get(ctx, after)
			if err != nil {
				return nil, err
			}
			ingests = append(ingests, active)
		}
	}

	return ingests, nil
}

var _ IngestManager = &ingestion{}
