package dbtest

import (
	"context"
	"testing"

	"github.com/jcrussell/solvent-streets/internal/resource"
)

// TestMockStore_DefaultListReturnsEmptyNotNil pins the default-return
// contract for List* methods: when the Func field is unset, each
// returns a non-nil empty slice and a nil error, so tests can
// distinguish "no rows" from "method not mocked". This is a deliberate
// divergence from the real sqliteStore, which returns NIL on zero rows
// (its List* methods use `var xs []T` + append). Empty-path JSON-shape
// assertions must use a real in-memory store, not this mock.
// Regression guard for solvent-streets-a66s.
func TestMockStore_DefaultListReturnsEmptyNotNil(t *testing.T) {
	ctx := context.Background()
	m := &MockStore{}

	if got, err := m.ListFeatures(ctx, resource.TypeRoads); err != nil || got == nil {
		t.Errorf("ListFeatures: want non-nil empty slice, got %v / %v", got, err)
	}
	if got, err := m.ListHexStats(ctx, resource.TypeRoads); err != nil || got == nil {
		t.Errorf("ListHexStats: want non-nil empty slice, got %v / %v", got, err)
	}
	if got, err := m.ListSnapshots(ctx); err != nil || got == nil {
		t.Errorf("ListSnapshots: want non-nil empty slice, got %v / %v", got, err)
	}
	if got, err := m.ListCohortStats(ctx, resource.TypeRoads); err != nil || got == nil {
		t.Errorf("ListCohortStats: want non-nil empty slice, got %v / %v", got, err)
	}

	root := &MockRootStore{}
	if got, err := root.ListCities(ctx); err != nil || got == nil {
		t.Errorf("ListCities: want non-nil empty slice, got %v / %v", got, err)
	}
}
