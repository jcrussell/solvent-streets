package dbtest

import (
	"context"
	"testing"

	"github.com/jcrussell/solvent-streets/internal/resource"
)

// TestMockStore_DefaultListReturnsEmptyNotNil pins the default-return
// contract for List* methods: when the Func field is unset, each
// returns a non-nil empty slice and a nil error. The real sqliteStore
// builds its result by rows-iteration so the slice is never nil; tests
// that distinguish "no rows" from "method not mocked" would otherwise
// be silently miscovered. Regression guard for solvent-streets-a66s.
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
	if got, err := m.ListForecastResults(ctx, resource.TypeRoads); err != nil || got == nil {
		t.Errorf("ListForecastResults: want non-nil empty slice, got %v / %v", got, err)
	}
	if got, err := m.ListCohortStats(ctx, resource.TypeRoads); err != nil || got == nil {
		t.Errorf("ListCohortStats: want non-nil empty slice, got %v / %v", got, err)
	}
	if got, err := m.ResourceTypes(ctx); err != nil || got == nil {
		t.Errorf("ResourceTypes: want non-nil empty slice, got %v / %v", got, err)
	}

	root := &MockRootStore{}
	if got, err := root.ListCities(ctx); err != nil || got == nil {
		t.Errorf("ListCities: want non-nil empty slice, got %v / %v", got, err)
	}
}
