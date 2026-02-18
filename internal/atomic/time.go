package atomic

import (
	"sync/atomic"
	"time"
)

type AtomicTime struct {
	time *atomic.Int64
}

func (t *AtomicTime) Get() time.Time {
	return time.UnixMilli(t.time.Load())
}

func (t *AtomicTime) Set(nt time.Time) {
	t.time.Store(nt.UnixMilli())
}

func Time(t time.Time) *AtomicTime {
	at := atomic.Int64{}
	at.Store(t.UnixMilli())
	return &AtomicTime{
		time: &at,
	}
}
