//go:build linux

package fsmonitor

import (
	"crypto/sha256"
	"encoding/hex"
	"hash"
	"sync"
	"sync/atomic"

	"golang.org/x/sys/unix"
)

type internalFileStat struct {
	FileStat
	hasher hash.Hash // rolling hasher for accessed bytes
}

// eventProcessor handles the state and logic for event translation.
// It is decoupled from the actual fanotify file descriptor to allow testing.
type eventProcessor struct {
	mu            sync.RWMutex
	stats         map[string]*internalFileStat
	overflowCount uint64
	eventCount    uint64
}

func newEventProcessor() *eventProcessor {
	return &eventProcessor{
		stats: make(map[string]*internalFileStat),
	}
}

func (p *eventProcessor) processTask(task eventTask) {
	p.mu.Lock()
	stat, ok := p.stats[task.path]
	if !ok {
		stat = &internalFileStat{
			FileStat: FileStat{Path: task.path},
			hasher:   sha256.New(),
		}
		p.stats[task.path] = stat
	}
	p.mu.Unlock()

	if task.mask&unix.FAN_ACCESS != 0 || task.mask&FAN_PRE_ACCESS != 0 {
		atomic.AddUint64(&stat.Reads, 1)

		if task.hasR && task.data != nil {
			p.mu.Lock()
			stat.hasher.Write(task.data)
			stat.AccessChecksum = hex.EncodeToString(stat.hasher.Sum(nil))
			p.mu.Unlock()
		}
	}

	if task.mask&unix.FAN_CLOSE_WRITE != 0 {
		atomic.AddUint64(&stat.Writes, 1)
		if task.fullChecksum != "" {
			p.mu.Lock()
			stat.Checksum = task.fullChecksum
			p.mu.Unlock()
		}
	}
}

func (p *eventProcessor) snapshot() Stats {
	p.mu.RLock()
	defer p.mu.RUnlock()

	files := make(map[string]FileStat, len(p.stats))
	for k, v := range p.stats {
		files[k] = FileStat{
			Path:           v.Path,
			Reads:          atomic.LoadUint64(&v.Reads),
			Writes:         atomic.LoadUint64(&v.Writes),
			Checksum:       v.Checksum,
			AccessChecksum: v.AccessChecksum,
		}
	}

	return Stats{
		Files:         files,
		QueueOverflow: atomic.LoadUint64(&p.overflowCount),
		EventsTotal:   atomic.LoadUint64(&p.eventCount),
	}
}
