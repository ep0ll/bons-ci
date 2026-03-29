package fsmonitor_test

import (
	"context"
	"os"
	"path/filepath"
	"runtime"
	"testing"
	"time"

	"github.com/bons/bons-ci/pkg/fsmonitor"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestFSMonitor_Basic(t *testing.T) {
	if runtime.GOOS != "linux" {
		mon, err := fsmonitor.New()
		assert.ErrorIs(t, err, fsmonitor.ErrNotSupported)
		assert.Nil(t, mon)
		return
	}

	// This test requires root/CAP_SYS_ADMIN on Linux.
	// We check for permission error.
	mon, err := fsmonitor.New()
	if err != nil {
		t.Skipf("Skipping fanotify test (likely lack of CAP_SYS_ADMIN): %v", err)
		return
	}

	tmpDir := t.TempDir()
	err = mon.Add(tmpDir)
	require.NoError(t, err)

	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
	defer cancel()

	go func() {
		err := mon.Run(ctx)
		if err != nil && err != context.Canceled {
			t.Errorf("Run failed: %v", err)
		}
	}()

	// Perform some file operations
	testFile := filepath.Join(tmpDir, "test.txt")
	err = os.WriteFile(testFile, []byte("hello world"), 0644)
	require.NoError(t, err)

	// Wait for event processing
	time.Sleep(500 * time.Millisecond)

	stats := mon.Snapshot()
	require.NotEmpty(t, stats.Files)

	found := false
	for _, f := range stats.Files {
		if filepath.Base(f.Path) == "test.txt" {
			found = true
			assert.NotEmpty(t, f.Checksum)
			assert.Equal(t, uint64(1), f.Writes)
		}
	}
	assert.True(t, found, "test.txt not found in stats")
}
