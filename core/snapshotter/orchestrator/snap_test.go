// White-box tests for the snapshot pipeline.
//
// Being in the same package grants access to unexported symbols —
// buildChainTable, cloneOpts, chainInfo, workerResult, resultHeap — which
// allows direct unit tests of internal helpers alongside full integration
// tests of RunSnapshotPipeline.
//
// Run with the race detector (strongly recommended):
//
//	go test -race -count=1 ./...
//
// Run benchmarks:
//
//	go test -bench=. -benchmem ./...
package pipeline

import (
	"context"
	"errors"
	"fmt"
	"runtime"
	"strconv"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/containerd/containerd/v2/core/mount"
	"github.com/containerd/containerd/v2/core/snapshots"
	"github.com/containerd/errdefs"
	"github.com/opencontainers/go-digest"
	"github.com/opencontainers/image-spec/identity"
	ocispec "github.com/opencontainers/image-spec/specs-go/v1"
)

// ─────────────────────────────────────────────────────────────────────────────
// Mock snapshotter
// ─────────────────────────────────────────────────────────────────────────────

// commitRec records one successful Commit call for post-test assertions.
type commitRec struct {
	name   string
	key    string
	parent string // extracted by applying opts to a blank Info
}

// mockSnapshotter is a thread-safe injectable fake that implements
// snapshots.Snapshotter.
//
// Only the five methods used by the pipeline (Stat, Prepare, Mounts, Commit,
// Remove) are meaningfully implemented; all others return sentinel errors so
// callers notice immediately.
//
// # Concurrency model
//
// Hooks (onPrepare, onCommit, onRemove, onMounts) are read-only once set and
// must be configured before any pipeline goroutine touches the mock. State
// mutations (committed/active maps, call logs) are protected by mu. Hooks are
// called without mu held to avoid deadlocks; if a hook needs to observe mock
// state it must acquire mu itself.
type mockSnapshotter struct {
	mu        sync.Mutex
	committed map[string]snapshots.Info // committed-name → Info{Kind=Committed}
	active    map[string]struct{}       // active-key → present

	// Error-injection hooks — nil means success.
	onPrepare func(key string) error
	onCommit  func(name, key string) error
	onRemove  func(key string) error
	onMounts  func(key string) error

	// Call logs — appended under mu; read via accessor methods after pipeline exits.
	prepareLog []string
	commitLog  []commitRec
	removeLog  []string
	mountsLog  []string
}

func newMock() *mockSnapshotter {
	return &mockSnapshotter{
		committed: make(map[string]snapshots.Info),
		active:    make(map[string]struct{}),
	}
}

// preCommit pre-populates a committed snapshot, simulating a previously
// completed pipeline run.
func (m *mockSnapshotter) preCommit(name, parent string) {
	m.mu.Lock()
	defer m.mu.Unlock()
	m.committed[name] = snapshots.Info{
		Kind:   snapshots.KindCommitted,
		Name:   name,
		Parent: parent,
	}
}

// preActive pre-populates an active snapshot, simulating a partial run where
// Prepare succeeded but Commit never happened.
func (m *mockSnapshotter) preActive(key string) {
	m.mu.Lock()
	defer m.mu.Unlock()
	m.active[key] = struct{}{}
}

// ── snapshots.Snapshotter implementation ─────────────────────────────────────

func (m *mockSnapshotter) Stat(ctx context.Context, key string) (snapshots.Info, error) {
	if ctx.Err() != nil {
		return snapshots.Info{}, ctx.Err()
	}
	m.mu.Lock()
	defer m.mu.Unlock()
	if info, ok := m.committed[key]; ok {
		return info, nil
	}
	if _, ok := m.active[key]; ok {
		return snapshots.Info{Kind: snapshots.KindActive, Name: key}, nil
	}
	return snapshots.Info{}, errdefs.ErrNotFound
}

func (m *mockSnapshotter) Prepare(ctx context.Context, key, parent string, opts ...snapshots.Opt) ([]mount.Mount, error) {
	if ctx.Err() != nil {
		return nil, ctx.Err()
	}
	if m.onPrepare != nil {
		if err := m.onPrepare(key); err != nil {
			return nil, err
		}
	}
	m.mu.Lock()
	defer m.mu.Unlock()
	m.prepareLog = append(m.prepareLog, key)
	if _, ok := m.active[key]; ok {
		return nil, errdefs.ErrAlreadyExists
	}
	// A key may collide with a committed name only for the root layer where
	// diffID == chainID. In that case Prepare must also return AlreadyExists.
	if _, ok := m.committed[key]; ok {
		return nil, errdefs.ErrAlreadyExists
	}
	m.active[key] = struct{}{}
	return []mount.Mount{{Type: "bind", Source: key}}, nil
}

func (m *mockSnapshotter) Mounts(ctx context.Context, key string) ([]mount.Mount, error) {
	if ctx.Err() != nil {
		return nil, ctx.Err()
	}
	if m.onMounts != nil {
		if err := m.onMounts(key); err != nil {
			return nil, err
		}
	}
	m.mu.Lock()
	defer m.mu.Unlock()
	m.mountsLog = append(m.mountsLog, key)
	if _, ok := m.active[key]; !ok {
		return nil, fmt.Errorf("Mounts %q: %w", key, errdefs.ErrNotFound)
	}
	return []mount.Mount{{Type: "bind", Source: key}}, nil
}

func (m *mockSnapshotter) Commit(ctx context.Context, name, key string, opts ...snapshots.Opt) error {
	if ctx.Err() != nil {
		return ctx.Err()
	}
	if m.onCommit != nil {
		if err := m.onCommit(name, key); err != nil {
			return err
		}
	}
	parent := applyOptsParent(opts)
	m.mu.Lock()
	defer m.mu.Unlock()
	if _, ok := m.committed[name]; ok {
		return errdefs.ErrAlreadyExists
	}
	delete(m.active, key)
	m.committed[name] = snapshots.Info{Kind: snapshots.KindCommitted, Name: name, Parent: parent}
	m.commitLog = append(m.commitLog, commitRec{name: name, key: key, parent: parent})
	return nil
}

func (m *mockSnapshotter) Remove(ctx context.Context, key string) error {
	if m.onRemove != nil {
		if err := m.onRemove(key); err != nil {
			return err
		}
	}
	m.mu.Lock()
	defer m.mu.Unlock()
	m.removeLog = append(m.removeLog, key)
	delete(m.active, key)
	return nil
}

// Unused by the pipeline — return sentinel errors so misuse is immediately visible.
func (m *mockSnapshotter) Update(_ context.Context, _ snapshots.Info, _ ...string) (snapshots.Info, error) {
	return snapshots.Info{}, errors.New("mockSnapshotter: Update not implemented")
}
func (m *mockSnapshotter) Usage(_ context.Context, _ string) (snapshots.Usage, error) {
	return snapshots.Usage{}, errors.New("mockSnapshotter: Usage not implemented")
}
func (m *mockSnapshotter) View(_ context.Context, _, _ string, _ ...snapshots.Opt) ([]mount.Mount, error) {
	return nil, errors.New("mockSnapshotter: View not implemented")
}
func (m *mockSnapshotter) Walk(_ context.Context, _ snapshots.WalkFunc, _ ...string) error {
	return errors.New("mockSnapshotter: Walk not implemented")
}
func (m *mockSnapshotter) Close() error { return nil }

// ── Thread-safe log accessors ─────────────────────────────────────────────────

func (m *mockSnapshotter) Commits() []commitRec {
	m.mu.Lock()
	defer m.mu.Unlock()
	cp := make([]commitRec, len(m.commitLog))
	copy(cp, m.commitLog)
	return cp
}

func (m *mockSnapshotter) Removes() []string {
	m.mu.Lock()
	defer m.mu.Unlock()
	cp := make([]string, len(m.removeLog))
	copy(cp, m.removeLog)
	return cp
}

func (m *mockSnapshotter) Prepares() []string {
	m.mu.Lock()
	defer m.mu.Unlock()
	cp := make([]string, len(m.prepareLog))
	copy(cp, m.prepareLog)
	return cp
}

func (m *mockSnapshotter) MountsCalled() []string {
	m.mu.Lock()
	defer m.mu.Unlock()
	cp := make([]string, len(m.mountsLog))
	copy(cp, m.mountsLog)
	return cp
}

// ─────────────────────────────────────────────────────────────────────────────
// Test helpers
// ─────────────────────────────────────────────────────────────────────────────

// newRootFS builds a synthetic RootFS with n layers using deterministic
// digests: digest.FromString("layer-<i>") for each index i.
func newRootFS(n int) ocispec.RootFS {
	dids := make([]digest.Digest, n)
	for i := range dids {
		dids[i] = digest.FromString(fmt.Sprintf("layer-%d", i))
	}
	return ocispec.RootFS{Type: "layers", DiffIDs: dids}
}

// seqLabel returns a Labels map containing only LabelSnapshotterEventIndex.
func seqLabel(seq int) map[string]string {
	return map[string]string{LabelSnapshotterEventIndex: strconv.Itoa(seq)}
}

// noopAction is an Action that always succeeds without doing any work.
var noopAction = func(_ context.Context, _ []mount.Mount) error { return nil }

// failAction returns an Action that always returns err.
func failAction(err error) func(context.Context, []mount.Mount) error {
	return func(_ context.Context, _ []mount.Mount) error { return err }
}

// mountCapturingAction appends received mounts to *got under mu.
func mountCapturingAction(got *[]mount.Mount, mu *sync.Mutex) func(context.Context, []mount.Mount) error {
	return func(_ context.Context, mounts []mount.Mount) error {
		mu.Lock()
		defer mu.Unlock()
		*got = append(*got, mounts...)
		return nil
	}
}

// makeEvent builds an Event for the given seq with the provided Action.
func makeEvent(seq int, action func(context.Context, []mount.Mount) error) Event {
	return Event{Active: EventSnapshotter{
		Labels: seqLabel(seq),
		Action: action,
	}}
}

// buildEvents creates n events (seqs 0..n-1) each using noopAction.
func buildEvents(n int) []Event {
	evs := make([]Event, n)
	for i := range evs {
		evs[i] = makeEvent(i, noopAction)
	}
	return evs
}

// sendAll sends all events in order then closes the channel.
// Must be called in a separate goroutine when the pipeline buffer is smaller
// than len(events).
func sendAll(ch chan<- Event, events []Event) {
	for _, e := range events {
		ch <- e
	}
	close(ch)
}

// requireClosedSendOnly verifies that sending panics, which is the only
// observable closed-state check available for a send-only channel.
func requireClosedSendOnly(t *testing.T, ch chan<- Event) {
	t.Helper()
	defer func() {
		if r := recover(); r == nil {
			t.Fatal("expected panic when sending to closed channel")
		}
	}()
	ch <- Event{}
}

// waitResult reads from errCh with a 10-second timeout.
// Returns the error value (may be nil) or fails the test on timeout.
func waitResult(t *testing.T, errCh <-chan error) error {
	t.Helper()
	select {
	case err := <-errCh:
		return err
	case <-time.After(10 * time.Second):
		t.Fatal("timeout: pipeline did not finish within 10s")
		return nil
	}
}

// requireNoErr calls waitResult and fails if the result is non-nil.
func requireNoErr(t *testing.T, errCh <-chan error) {
	t.Helper()
	if err := waitResult(t, errCh); err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
}

// requireErr calls waitResult and fails if the result is nil.
func requireErr(t *testing.T, errCh <-chan error) error {
	t.Helper()
	err := waitResult(t, errCh)
	if err == nil {
		t.Fatal("expected an error but got nil")
	}
	return err
}

// applyOptsParent applies opts to a blank Info and returns the Parent field,
// mirroring containerd's snapshots.WithParent implementation.
func applyOptsParent(opts []snapshots.Opt) string {
	var info snapshots.Info
	for _, o := range opts {
		_ = o(&info)
	}
	return info.Parent
}

// ─────────────────────────────────────────────────────────────────────────────
// buildChainTable tests
// ─────────────────────────────────────────────────────────────────────────────

func TestBuildChainTable_Empty(t *testing.T) {
	chains := buildChainTable(ocispec.RootFS{})
	if len(chains) != 0 {
		t.Fatalf("expected empty table, got %d entries", len(chains))
	}
}

func TestBuildChainTable_SingleLayer(t *testing.T) {
	rootFS := newRootFS(1)
	chains := buildChainTable(rootFS)

	if len(chains) != 1 {
		t.Fatalf("expected 1 entry, got %d", len(chains))
	}
	d0 := rootFS.DiffIDs[0]
	wantDiffID := d0.String()
	wantChainID := identity.ChainID(rootFS.DiffIDs[:1]).String()

	if chains[0].diffID != wantDiffID {
		t.Errorf("diffID: got %q, want %q", chains[0].diffID, wantDiffID)
	}
	if chains[0].chainID != wantChainID {
		t.Errorf("chainID: got %q, want %q", chains[0].chainID, wantChainID)
	}
	if chains[0].parentChainID != "" {
		t.Errorf("root layer must have empty parentChainID, got %q", chains[0].parentChainID)
	}
	// OCI spec invariant: for a single-element DiffID slice, ChainID == DiffID.
	if chains[0].diffID != chains[0].chainID {
		t.Errorf("for single layer, diffID must equal chainID; diffID=%q chainID=%q",
			chains[0].diffID, chains[0].chainID)
	}
}

func TestBuildChainTable_ThreeLayers_ChainIDs(t *testing.T) {
	rootFS := newRootFS(3)
	dids := rootFS.DiffIDs
	chains := buildChainTable(rootFS)

	if len(chains) != 3 {
		t.Fatalf("expected 3 entries, got %d", len(chains))
	}

	// Root layer: no parent, chainID == ChainID([d0]).
	if chains[0].parentChainID != "" {
		t.Errorf("layer 0: parentChainID must be empty, got %q", chains[0].parentChainID)
	}
	if chains[0].chainID != identity.ChainID(dids[:1]).String() {
		t.Errorf("layer 0: chainID mismatch")
	}

	// Layer 1: parent == ChainID([d0]), chainID == ChainID([d0,d1]).
	wantParent1 := identity.ChainID(dids[:1]).String()
	if chains[1].parentChainID != wantParent1 {
		t.Errorf("layer 1: parentChainID got %q, want %q", chains[1].parentChainID, wantParent1)
	}
	if chains[1].chainID != identity.ChainID(dids[:2]).String() {
		t.Errorf("layer 1: chainID mismatch")
	}

	// Layer 2: parent == ChainID([d0,d1]), chainID == ChainID([d0,d1,d2]).
	wantParent2 := identity.ChainID(dids[:2]).String()
	if chains[2].parentChainID != wantParent2 {
		t.Errorf("layer 2: parentChainID got %q, want %q", chains[2].parentChainID, wantParent2)
	}
	if chains[2].chainID != identity.ChainID(dids[:3]).String() {
		t.Errorf("layer 2: chainID mismatch")
	}
}

func TestBuildChainTable_Deterministic(t *testing.T) {
	// Two calls with the same rootFS must produce identical tables.
	rootFS := newRootFS(10)
	a := buildChainTable(rootFS)
	b := buildChainTable(rootFS)
	for i := range a {
		if a[i] != b[i] {
			t.Fatalf("table[%d] differs between calls: %+v vs %+v", i, a[i], b[i])
		}
	}
}

// ─────────────────────────────────────────────────────────────────────────────
// cloneOpts tests
// ─────────────────────────────────────────────────────────────────────────────

func TestCloneOpts_Nil(t *testing.T) {
	if cloneOpts(nil) != nil {
		t.Error("cloneOpts(nil) must return nil")
	}
}

func TestCloneOpts_Empty(t *testing.T) {
	if cloneOpts([]snapshots.Opt{}) != nil {
		t.Error("cloneOpts([]) must return nil")
	}
}

func TestCloneOpts_NonEmpty_Independence(t *testing.T) {
	opt := func(i *snapshots.Info) error { i.Parent = "x"; return nil }
	original := []snapshots.Opt{opt}

	clone := cloneOpts(original)
	if len(clone) != 1 {
		t.Fatalf("clone len: got %d, want 1", len(clone))
	}

	// Appending to clone must not affect original's backing array.
	extra := func(i *snapshots.Info) error { return nil }
	clone = append(clone, extra)
	if len(original) != 1 {
		t.Error("appending to clone must not grow original")
	}
}

func TestCloneOpts_Capacity(t *testing.T) {
	// The pre-allocated +1 slot means a single append never reallocates.
	opt := func(*snapshots.Info) error { return nil }
	opts := []snapshots.Opt{opt, opt, opt}
	clone := cloneOpts(opts)
	if cap(clone) != len(opts)+1 {
		t.Errorf("capacity: got %d, want %d", cap(clone), len(opts)+1)
	}
}

// ─────────────────────────────────────────────────────────────────────────────
// Pipeline — happy-path tests
// ─────────────────────────────────────────────────────────────────────────────

func TestRunSnapshotPipeline_ZeroLayers(t *testing.T) {
	sn := newMock()
	eventCh, errCh := RunSnapshotPipeline(context.Background(), sn, ocispec.RootFS{}, 1)

	// The fast path closes the channel before returning, so any send panics.
	requireClosedSendOnly(t, eventCh)
	requireNoErr(t, errCh)
}

func TestRunSnapshotPipeline_SingleLayer(t *testing.T) {
	t.Parallel()
	sn := newMock()
	rootFS := newRootFS(1)

	eventCh, errCh := RunSnapshotPipeline(context.Background(), sn, rootFS, 1)
	go sendAll(eventCh, buildEvents(1))
	requireNoErr(t, errCh)

	commits := sn.Commits()
	if len(commits) != 1 {
		t.Fatalf("expected 1 commit, got %d", len(commits))
	}
	chains := buildChainTable(rootFS)
	if commits[0].name != chains[0].chainID {
		t.Errorf("commit name: got %q, want %q", commits[0].name, chains[0].chainID)
	}
	if commits[0].key != chains[0].diffID {
		t.Errorf("commit key: got %q, want %q", commits[0].key, chains[0].diffID)
	}
	if commits[0].parent != "" {
		t.Errorf("root layer must have no parent, got %q", commits[0].parent)
	}
}

func TestRunSnapshotPipeline_MultiLayer_InOrder(t *testing.T) {
	t.Parallel()
	const n = 5
	sn := newMock()
	rootFS := newRootFS(n)

	eventCh, errCh := RunSnapshotPipeline(context.Background(), sn, rootFS, 2)
	go sendAll(eventCh, buildEvents(n))
	requireNoErr(t, errCh)

	assertCommitOrder(t, sn, rootFS)
}

func TestRunSnapshotPipeline_MultiLayer_ReverseOrder(t *testing.T) {
	// Events sent in reverse order; the committer must still commit in seq order.
	t.Parallel()
	const n = 6
	sn := newMock()
	rootFS := newRootFS(n)

	eventCh, errCh := RunSnapshotPipeline(context.Background(), sn, rootFS, 4)
	go func() {
		evs := buildEvents(n)
		for i := n - 1; i >= 0; i-- {
			eventCh <- evs[i]
		}
		close(eventCh)
	}()
	requireNoErr(t, errCh)

	assertCommitOrder(t, sn, rootFS)
}

func TestRunSnapshotPipeline_DefaultWorkers(t *testing.T) {
	// numWorkers == 0 must default to runtime.NumCPU without panicking.
	t.Parallel()
	const n = 4
	sn := newMock()
	rootFS := newRootFS(n)

	eventCh, errCh := RunSnapshotPipeline(context.Background(), sn, rootFS, 0)
	go sendAll(eventCh, buildEvents(n))
	requireNoErr(t, errCh)

	if len(sn.Commits()) != n {
		t.Errorf("commits: got %d, want %d", len(sn.Commits()), n)
	}
}

// ─────────────────────────────────────────────────────────────────────────────
// Pipeline — idempotency tests
// ─────────────────────────────────────────────────────────────────────────────

func TestRunSnapshotPipeline_AllAlreadyCommitted(t *testing.T) {
	// A fully pre-committed image must bypass Prepare and Action entirely.
	t.Parallel()
	const n = 3
	sn := newMock()
	rootFS := newRootFS(n)
	chains := buildChainTable(rootFS)

	for i, ci := range chains {
		parent := ""
		if i > 0 {
			parent = chains[i-1].chainID
		}
		sn.preCommit(ci.chainID, parent)
	}

	eventCh, errCh := RunSnapshotPipeline(context.Background(), sn, rootFS, 2)
	go sendAll(eventCh, buildEvents(n))
	requireNoErr(t, errCh)

	if got := sn.Prepares(); len(got) != 0 {
		t.Errorf("expected 0 Prepare calls, got %d: %v", len(got), got)
	}
	if got := sn.Commits(); len(got) != 0 {
		t.Errorf("expected 0 Commit calls, got %d", len(got))
	}
}

func TestRunSnapshotPipeline_PartiallyAlreadyCommitted(t *testing.T) {
	// Only layers beyond the pre-committed range should be processed.
	t.Parallel()
	const n = 4
	sn := newMock()
	rootFS := newRootFS(n)
	chains := buildChainTable(rootFS)

	// Pre-commit the first two layers.
	for i := 0; i < 2; i++ {
		parent := ""
		if i > 0 {
			parent = chains[i-1].chainID
		}
		sn.preCommit(chains[i].chainID, parent)
	}

	var actionCalls atomic.Int32
	evs := make([]Event, n)
	for i := range evs {
		evs[i] = makeEvent(i, func(_ context.Context, _ []mount.Mount) error {
			actionCalls.Add(1)
			return nil
		})
	}

	eventCh, errCh := RunSnapshotPipeline(context.Background(), sn, rootFS, 2)
	go sendAll(eventCh, evs)
	requireNoErr(t, errCh)

	if got := actionCalls.Load(); got != 2 {
		t.Errorf("expected 2 Action calls (layers 2,3), got %d", got)
	}
	if got := sn.Commits(); len(got) != 2 {
		t.Errorf("expected 2 Commit calls, got %d", len(got))
	}
}

func TestRunSnapshotPipeline_PrepareAlreadyExists(t *testing.T) {
	// Prepare returning ErrAlreadyExists (partial run) must fall back to
	// Mounts and forward those mounts to Action.
	t.Parallel()
	const n = 2
	sn := newMock()
	rootFS := newRootFS(n)
	chains := buildChainTable(rootFS)

	sn.preActive(chains[1].diffID)

	var mountsReceived int
	var mu sync.Mutex
	evs := make([]Event, n)
	for i := range evs {
		i := i
		evs[i] = makeEvent(i, func(_ context.Context, mounts []mount.Mount) error {
			mu.Lock()
			if len(mounts) > 0 {
				mountsReceived++
			}
			mu.Unlock()
			return nil
		})
	}

	eventCh, errCh := RunSnapshotPipeline(context.Background(), sn, rootFS, 2)
	go sendAll(eventCh, evs)
	requireNoErr(t, errCh)

	if mc := sn.MountsCalled(); len(mc) == 0 {
		t.Error("expected Mounts to be called for the pre-existing active snapshot")
	}
	if got := sn.Commits(); len(got) != n {
		t.Errorf("expected %d commits, got %d", n, len(got))
	}
}

func TestRunSnapshotPipeline_CommitAlreadyExists(t *testing.T) {
	// Commit returning ErrAlreadyExists must be treated as success.
	t.Parallel()
	sn := newMock()
	rootFS := newRootFS(1)
	chains := buildChainTable(rootFS)

	sn.onCommit = func(name, _ string) error {
		if name == chains[0].chainID {
			return errdefs.ErrAlreadyExists
		}
		return nil
	}

	eventCh, errCh := RunSnapshotPipeline(context.Background(), sn, rootFS, 1)
	go sendAll(eventCh, buildEvents(1))
	requireNoErr(t, errCh)
}

// ─────────────────────────────────────────────────────────────────────────────
// Pipeline — error propagation tests
// ─────────────────────────────────────────────────────────────────────────────

func TestRunSnapshotPipeline_PrepareError(t *testing.T) {
	t.Parallel()
	sn := newMock()
	rootFS := newRootFS(3)
	chains := buildChainTable(rootFS)
	wantErr := errors.New("disk full")

	sn.onPrepare = func(key string) error {
		if key == chains[1].diffID {
			return wantErr
		}
		return nil
	}

	eventCh, errCh := RunSnapshotPipeline(context.Background(), sn, rootFS, 2)
	go sendAll(eventCh, buildEvents(3))

	err := requireErr(t, errCh)
	if !errors.Is(err, wantErr) {
		t.Errorf("error chain: got %v, want %v somewhere in chain", err, wantErr)
	}
}

func TestRunSnapshotPipeline_ActionError_RemoveCalled(t *testing.T) {
	// A failed Action must trigger Remove on the active snapshot.
	t.Parallel()
	sn := newMock()
	rootFS := newRootFS(3)
	chains := buildChainTable(rootFS)
	wantErr := errors.New("unpack failed")

	evs := make([]Event, 3)
	for i := range evs {
		i := i
		evs[i] = makeEvent(i, func(_ context.Context, _ []mount.Mount) error {
			if i == 2 {
				return wantErr
			}
			return nil
		})
	}

	eventCh, errCh := RunSnapshotPipeline(context.Background(), sn, rootFS, 2)
	go sendAll(eventCh, evs)

	err := requireErr(t, errCh)
	if !errors.Is(err, wantErr) {
		t.Errorf("error chain: got %v, want wrapped %v", err, wantErr)
	}

	removed := sn.Removes()
	found := false
	for _, r := range removed {
		if r == chains[2].diffID {
			found = true
			break
		}
	}
	if !found {
		t.Errorf("expected Remove(%q) after Action failure; removes=%v",
			chains[2].diffID, removed)
	}
}

func TestRunSnapshotPipeline_ActionError_RemoveFailureIgnored(t *testing.T) {
	// Remove returning an error after Action failure must not replace the
	// Action error in the pipeline result.
	t.Parallel()
	sn := newMock()
	rootFS := newRootFS(1)
	actionErr := errors.New("action boom")
	removeErr := errors.New("remove boom")

	sn.onRemove = func(_ string) error { return removeErr }

	eventCh, errCh := RunSnapshotPipeline(context.Background(), sn, rootFS, 1)
	go sendAll(eventCh, []Event{makeEvent(0, failAction(actionErr))})

	err := requireErr(t, errCh)
	if !errors.Is(err, actionErr) {
		t.Errorf("expected actionErr in chain, got: %v", err)
	}
	if errors.Is(err, removeErr) {
		t.Error("removeErr must not appear in the error chain")
	}
}

func TestRunSnapshotPipeline_CommitError(t *testing.T) {
	t.Parallel()
	sn := newMock()
	rootFS := newRootFS(2)
	chains := buildChainTable(rootFS)
	wantErr := errors.New("store corrupted")

	sn.onCommit = func(name, _ string) error {
		if name == chains[1].chainID {
			return wantErr
		}
		return nil
	}

	eventCh, errCh := RunSnapshotPipeline(context.Background(), sn, rootFS, 2)
	go sendAll(eventCh, buildEvents(2))

	err := requireErr(t, errCh)
	if !errors.Is(err, wantErr) {
		t.Errorf("error chain: got %v, want %v", err, wantErr)
	}
}

func TestRunSnapshotPipeline_MountsError(t *testing.T) {
	// Mounts called after Prepare ErrAlreadyExists: if Mounts fails, the
	// worker must return the Mounts error, not the Prepare error.
	t.Parallel()
	sn := newMock()
	rootFS := newRootFS(1)
	chains := buildChainTable(rootFS)
	mountsErr := errors.New("mounts unavailable")

	sn.preActive(chains[0].diffID)
	sn.onMounts = func(_ string) error { return mountsErr }

	eventCh, errCh := RunSnapshotPipeline(context.Background(), sn, rootFS, 1)
	go sendAll(eventCh, buildEvents(1))

	err := requireErr(t, errCh)
	if !errors.Is(err, mountsErr) {
		t.Errorf("expected mountsErr, got: %v", err)
	}
}

// ─────────────────────────────────────────────────────────────────────────────
// Pipeline — context cancellation tests
// ─────────────────────────────────────────────────────────────────────────────

func TestRunSnapshotPipeline_ContextAlreadyCancelled(t *testing.T) {
	t.Parallel()
	ctx, cancel := context.WithCancel(context.Background())
	cancel() // pre-cancel before the pipeline starts

	sn := newMock()
	rootFS := newRootFS(3)

	eventCh, errCh := RunSnapshotPipeline(ctx, sn, rootFS, 2)
	go sendAll(eventCh, buildEvents(3))

	err := requireErr(t, errCh)
	if !errors.Is(err, context.Canceled) {
		t.Errorf("expected context.Canceled, got: %v", err)
	}
}

func TestRunSnapshotPipeline_ContextCancelledMidPipeline(t *testing.T) {
	t.Parallel()
	ctx, cancel := context.WithCancel(context.Background())

	sn := newMock()
	rootFS := newRootFS(10)

	// Cancel after the first successful Commit.
	var commitCount atomic.Int32
	sn.onCommit = func(name, key string) error {
		if commitCount.Add(1) == 1 {
			cancel()
		}
		return nil
	}

	eventCh, errCh := RunSnapshotPipeline(ctx, sn, rootFS, 2)
	go sendAll(eventCh, buildEvents(10))

	err := waitResult(t, errCh)
	// Either nil (race: all layers committed before cancel) or a context error.
	if err != nil && !errors.Is(err, context.Canceled) {
		t.Errorf("expected nil or context.Canceled, got: %v", err)
	}
}

func TestRunSnapshotPipeline_CancelNoDeadlock(t *testing.T) {
	// After cancellation, workers must keep draining eventCh so the caller's
	// send loop never blocks permanently (drain invariant).
	t.Parallel()
	ctx, cancel := context.WithCancel(context.Background())
	sn := newMock()
	const n = 20
	rootFS := newRootFS(n)

	// Fail the very first Action to trigger cancellation immediately.
	evs := make([]Event, n)
	for i := range evs {
		i := i
		evs[i] = makeEvent(i, func(_ context.Context, _ []mount.Mount) error {
			if i == 0 {
				return errors.New("injected failure")
			}
			return nil
		})
	}

	eventCh, errCh := RunSnapshotPipeline(ctx, sn, rootFS, 2)
	defer cancel()

	// If the drain invariant is violated, this goroutine blocks and the
	// select below times out.
	done := make(chan struct{})
	go func() {
		sendAll(eventCh, evs)
		close(done)
	}()

	select {
	case <-done:
	case <-time.After(5 * time.Second):
		t.Fatal("send goroutine blocked: drain invariant violated")
	}

	requireErr(t, errCh)
}

// ─────────────────────────────────────────────────────────────────────────────
// Pipeline — label and sequence validation tests
// ─────────────────────────────────────────────────────────────────────────────

func TestRunSnapshotPipeline_MissingSeqLabel(t *testing.T) {
	t.Parallel()
	sn := newMock()
	rootFS := newRootFS(1)

	bad := Event{Active: EventSnapshotter{Labels: nil, Action: noopAction}}

	eventCh, errCh := RunSnapshotPipeline(context.Background(), sn, rootFS, 1)
	go func() { eventCh <- bad; close(eventCh) }()

	err := requireErr(t, errCh)
	if !strings.Contains(err.Error(), "invalid sequence number") {
		t.Errorf("expected 'invalid sequence number' in error, got: %v", err)
	}
}

func TestRunSnapshotPipeline_NonNumericSeqLabel(t *testing.T) {
	t.Parallel()
	sn := newMock()
	rootFS := newRootFS(1)

	bad := Event{Active: EventSnapshotter{
		Labels: map[string]string{LabelSnapshotterEventIndex: "not-a-number"},
		Action: noopAction,
	}}

	eventCh, errCh := RunSnapshotPipeline(context.Background(), sn, rootFS, 1)
	go func() { eventCh <- bad; close(eventCh) }()

	err := requireErr(t, errCh)
	if !strings.Contains(err.Error(), "invalid sequence number") {
		t.Errorf("expected 'invalid sequence number' in error, got: %v", err)
	}
}

func TestRunSnapshotPipeline_SeqNegative(t *testing.T) {
	t.Parallel()
	sn := newMock()
	rootFS := newRootFS(2)

	bad := Event{Active: EventSnapshotter{
		Labels: map[string]string{LabelSnapshotterEventIndex: "-1"},
		Action: noopAction,
	}}

	eventCh, errCh := RunSnapshotPipeline(context.Background(), sn, rootFS, 1)
	go func() { eventCh <- bad; close(eventCh) }()

	err := requireErr(t, errCh)
	if !strings.Contains(err.Error(), "out of range") {
		t.Errorf("expected 'out of range' in error, got: %v", err)
	}
}

func TestRunSnapshotPipeline_SeqTooLarge(t *testing.T) {
	t.Parallel()
	sn := newMock()
	rootFS := newRootFS(2)

	bad := Event{Active: EventSnapshotter{
		Labels: map[string]string{LabelSnapshotterEventIndex: "999"},
		Action: noopAction,
	}}

	eventCh, errCh := RunSnapshotPipeline(context.Background(), sn, rootFS, 1)
	go func() { eventCh <- bad; close(eventCh) }()

	err := requireErr(t, errCh)
	if !strings.Contains(err.Error(), "out of range") {
		t.Errorf("expected 'out of range' in error, got: %v", err)
	}
}

func TestRunSnapshotPipeline_SeqGap(t *testing.T) {
	// Sending seqs 0 and 2 but omitting seq 1 must produce a seq-gap error.
	t.Parallel()
	sn := newMock()
	rootFS := newRootFS(3)

	eventCh, errCh := RunSnapshotPipeline(context.Background(), sn, rootFS, 3)
	go func() {
		eventCh <- makeEvent(0, noopAction)
		eventCh <- makeEvent(2, noopAction) // seq 1 deliberately omitted
		close(eventCh)
	}()

	err := requireErr(t, errCh)
	if !strings.Contains(err.Error(), "seq gap") {
		t.Errorf("expected 'seq gap' in error, got: %v", err)
	}
}

func TestRunSnapshotPipeline_EarlyClose(t *testing.T) {
	// Closing the event channel before all layers are delivered must return
	// an error, not nil.
	t.Parallel()
	sn := newMock()
	rootFS := newRootFS(3)

	eventCh, errCh := RunSnapshotPipeline(context.Background(), sn, rootFS, 2)
	go func() {
		eventCh <- makeEvent(0, noopAction)
		eventCh <- makeEvent(1, noopAction)
		close(eventCh) // layer 2 never delivered
	}()

	err := requireErr(t, errCh)
	if !strings.Contains(err.Error(), "early close") {
		t.Errorf("expected 'early close' in error, got: %v", err)
	}
}

// ─────────────────────────────────────────────────────────────────────────────
// Pipeline — correctness tests
// ─────────────────────────────────────────────────────────────────────────────

func TestRunSnapshotPipeline_ParentChainCorrect(t *testing.T) {
	// The committer must attach WithParent(chainID[i-1]) to every non-root layer.
	t.Parallel()
	const n = 4
	sn := newMock()
	rootFS := newRootFS(n)

	eventCh, errCh := RunSnapshotPipeline(context.Background(), sn, rootFS, 2)
	go sendAll(eventCh, buildEvents(n))
	requireNoErr(t, errCh)

	assertCommitOrder(t, sn, rootFS)
}

func TestRunSnapshotPipeline_ActionReceivesMounts(t *testing.T) {
	// Each Action must receive a non-empty mount slice from Prepare.
	t.Parallel()
	const n = 3
	sn := newMock()
	rootFS := newRootFS(n)

	var mu sync.Mutex
	var allMounts [][]mount.Mount

	evs := make([]Event, n)
	for i := range evs {
		evs[i] = makeEvent(i, func(_ context.Context, mounts []mount.Mount) error {
			mu.Lock()
			allMounts = append(allMounts, append([]mount.Mount(nil), mounts...))
			mu.Unlock()
			return nil
		})
	}

	eventCh, errCh := RunSnapshotPipeline(context.Background(), sn, rootFS, 2)
	go sendAll(eventCh, evs)
	requireNoErr(t, errCh)

	if len(allMounts) != n {
		t.Fatalf("expected %d mount slices, got %d", n, len(allMounts))
	}
	for i, mounts := range allMounts {
		if len(mounts) == 0 {
			t.Errorf("Action %d received empty mount slice", i)
		}
	}
}

func TestRunSnapshotPipeline_ConcurrentCorrectness(t *testing.T) {
	// numWorkers > len(layers) stresses concurrent code paths; the race
	// detector will catch any data races.
	t.Parallel()
	const n = 8
	sn := newMock()
	rootFS := newRootFS(n)

	eventCh, errCh := RunSnapshotPipeline(context.Background(), sn, rootFS, n*2)
	go sendAll(eventCh, buildEvents(n))
	requireNoErr(t, errCh)

	assertCommitOrder(t, sn, rootFS)
}

func TestRunSnapshotPipeline_LargePipeline(t *testing.T) {
	// 100 layers exercising the heap drain loop.
	t.Parallel()
	const n = 100
	sn := newMock()
	rootFS := newRootFS(n)

	workers := runtime.NumCPU()
	if workers < 2 {
		workers = 2
	}
	eventCh, errCh := RunSnapshotPipeline(context.Background(), sn, rootFS, workers)
	go sendAll(eventCh, buildEvents(n))
	requireNoErr(t, errCh)

	if got := len(sn.Commits()); got != n {
		t.Errorf("commits: got %d, want %d", got, n)
	}
	assertCommitOrder(t, sn, rootFS)
}

func TestRunSnapshotPipeline_ErrorPropagatesOverContextCanceled(t *testing.T) {
	// When a real error and context.Canceled both occur, the real error must
	// be surfaced rather than the derived context.Canceled.
	t.Parallel()
	sn := newMock()
	rootFS := newRootFS(5)
	realErr := errors.New("real error")

	evs := make([]Event, 5)
	for i := range evs {
		i := i
		evs[i] = makeEvent(i, func(_ context.Context, _ []mount.Mount) error {
			if i == 0 {
				return realErr
			}
			return nil
		})
	}

	eventCh, errCh := RunSnapshotPipeline(context.Background(), sn, rootFS, 4)
	go sendAll(eventCh, evs)

	err := requireErr(t, errCh)
	if !errors.Is(err, realErr) && !errors.Is(err, context.Canceled) {
		t.Errorf("expected realErr or context.Canceled, got: %v", err)
	}
}

func TestRunSnapshotPipeline_CommitOptionsNotMutated(t *testing.T) {
	// cloneOpts must prevent the committer from mutating the caller's
	// CommitOptions slice through aliasing.
	t.Parallel()
	sn := newMock()
	rootFS := newRootFS(2)

	sharedOpt := func(i *snapshots.Info) error { return nil }
	sharedOpts := []snapshots.Opt{sharedOpt}
	origCap := cap(sharedOpts)

	evs := make([]Event, 2)
	for i := range evs {
		evs[i] = Event{Active: EventSnapshotter{
			Labels:        seqLabel(i),
			Action:        noopAction,
			CommitOptions: sharedOpts, // same slice shared across events
		}}
	}

	eventCh, errCh := RunSnapshotPipeline(context.Background(), sn, rootFS, 2)
	go sendAll(eventCh, evs)
	requireNoErr(t, errCh)

	if cap(sharedOpts) != origCap {
		t.Errorf("CommitOptions capacity mutated: before=%d after=%d", origCap, cap(sharedOpts))
	}
}

// ─────────────────────────────────────────────────────────────────────────────
// Assertion helpers
// ─────────────────────────────────────────────────────────────────────────────

// assertCommitOrder verifies that:
//  1. The number of commits equals the number of layers.
//  2. Each commit name matches the expected chainID.
//  3. Each commit parent is the chain ID of the preceding layer (or "" for root).
func assertCommitOrder(t *testing.T, sn *mockSnapshotter, rootFS ocispec.RootFS) {
	t.Helper()
	chains := buildChainTable(rootFS)
	commits := sn.Commits()

	if len(commits) != len(chains) {
		t.Errorf("commit count: got %d, want %d", len(commits), len(chains))
		return
	}

	byName := make(map[string]commitRec, len(commits))
	for _, c := range commits {
		byName[c.name] = c
	}

	for i, ci := range chains {
		rec, ok := byName[ci.chainID]
		if !ok {
			t.Errorf("layer %d: no commit found for chainID %q", i, ci.chainID)
			continue
		}
		if rec.key != ci.diffID {
			t.Errorf("layer %d: commit key got %q, want %q", i, rec.key, ci.diffID)
		}
		if rec.parent != ci.parentChainID {
			t.Errorf("layer %d: commit parent got %q, want %q", i, rec.parent, ci.parentChainID)
		}
	}
}

// ─────────────────────────────────────────────────────────────────────────────
// Benchmarks
// ─────────────────────────────────────────────────────────────────────────────

// benchmarkPipeline runs the pipeline for n layers with w workers using
// noopAction to measure pipeline overhead in isolation.
func benchmarkPipeline(b *testing.B, n, w int) {
	b.Helper()
	rootFS := newRootFS(n)
	evs := buildEvents(n)

	b.ResetTimer()
	b.ReportAllocs()
	for range b.N {
		sn := newMock()
		eventCh, errCh := RunSnapshotPipeline(context.Background(), sn, rootFS, w)
		go sendAll(eventCh, evs)
		if err := <-errCh; err != nil {
			b.Fatal(err)
		}
	}
}

func BenchmarkPipeline_1Layer_1Worker(b *testing.B)    { benchmarkPipeline(b, 1, 1) }
func BenchmarkPipeline_10Layers_1Worker(b *testing.B)  { benchmarkPipeline(b, 10, 1) }
func BenchmarkPipeline_10Layers_4Workers(b *testing.B) { benchmarkPipeline(b, 10, 4) }
func BenchmarkPipeline_100Layers_1Worker(b *testing.B) { benchmarkPipeline(b, 100, 1) }
func BenchmarkPipeline_100Layers_NumCPU(b *testing.B) {
	benchmarkPipeline(b, 100, runtime.NumCPU())
}

func BenchmarkBuildChainTable_10(b *testing.B) {
	rootFS := newRootFS(10)
	b.ResetTimer()
	b.ReportAllocs()
	for range b.N {
		_ = buildChainTable(rootFS)
	}
}

func BenchmarkBuildChainTable_100(b *testing.B) {
	rootFS := newRootFS(100)
	b.ResetTimer()
	b.ReportAllocs()
	for range b.N {
		_ = buildChainTable(rootFS)
	}
}

func BenchmarkResultHeap_PushPop(b *testing.B) {
	// Isolates the heap operations used in the committer hot path.
	b.ReportAllocs()
	h := make(resultHeap, 0, 8)
	for range b.N {
		for i := 7; i >= 0; i-- {
			h = append(h, workerResult{seq: i})
		}
		for i := range h {
			_ = h[i]
		}
		h = h[:0]
	}
}
