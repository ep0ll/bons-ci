//go:build !linux

package fsmonitor

import (
	"context"
)

type unsupportedMonitor struct{}

func New() (Monitor, error) {
	return nil, ErrNotSupported
}

func (m *unsupportedMonitor) Add(path string) error {
	return ErrNotSupported
}

func (m *unsupportedMonitor) Run(ctx context.Context) error {
	return ErrNotSupported
}

func (m *unsupportedMonitor) Snapshot() Stats {
	return Stats{}
}
