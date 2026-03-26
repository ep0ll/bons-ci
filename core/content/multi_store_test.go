package content

import (
	"context"
	"testing"
	"time"

	"github.com/containerd/containerd/v2/core/content"
)

type mockListStore struct {
	content.Store
	statuses []content.Status
}

func (m *mockListStore) ListStatuses(ctx context.Context, filters ...string) ([]content.Status, error) {
	return m.statuses, nil
}

func TestMultiStoreListStatusesAggregation(t *testing.T) {
	ctx := context.Background()
	now := time.Now()

	store1 := &mockListStore{
		statuses: []content.Status{
			{Ref: "ref1", Expected: "digest1", UpdatedAt: now},
			{Ref: "ref2", Expected: "digest2", UpdatedAt: now},
		},
	}

	store2 := &mockListStore{
		statuses: []content.Status{
			{Ref: "ref2", Expected: "digest2", UpdatedAt: now}, // duplicate
			{Ref: "ref3", Expected: "digest3", UpdatedAt: now},
		},
	}

	ms := NewMultiContentStore(store1, store2)
	statuses, err := ms.ListStatuses(ctx)
	if err != nil {
		t.Fatalf("ListStatuses failed: %v", err)
	}

	if len(statuses) != 3 {
		t.Errorf("Expected 3 statuses, got %d", len(statuses))
	}

	refs := make(map[string]bool)
	for _, st := range statuses {
		refs[st.Ref] = true
	}

	for _, ref := range []string{"ref1", "ref2", "ref3"} {
		if !refs[ref] {
			t.Errorf("Expected status list to contain ref %q", ref)
		}
	}
}
