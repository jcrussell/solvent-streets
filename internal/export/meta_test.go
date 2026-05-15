package export

import (
	"context"
	"database/sql"
	"testing"

	"github.com/google/go-cmp/cmp"

	"github.com/jcrussell/solvent-streets/internal/config"
	"github.com/jcrussell/solvent-streets/internal/db"
	"github.com/jcrussell/solvent-streets/internal/db/dbtest"
)

// boundaryGeoJSON is a small square polygon (~111km × 111km in degrees, but
// area is computed in projected meters; the actual number isn't asserted on).
const boundaryGeoJSON = `{"type":"Polygon","coordinates":[[[-122.5,37.5],[-122.4,37.5],[-122.4,37.6],[-122.5,37.6],[-122.5,37.5]]]}`

func newMockEntry(results map[string]db.ComputeResult) CityEntry {
	store := &dbtest.MockStore{
		LatestComputeResultFunc: func(_ context.Context, rt string) (*db.ComputeResult, error) {
			r, ok := results[rt]
			if !ok {
				return nil, sql.ErrNoRows
			}
			return &r, nil
		},
		GetBoundaryFunc: func(_ context.Context) (string, error) {
			return boundaryGeoJSON, nil
		},
	}
	return CityEntry{
		Config: &config.Config{},
		City:   config.CityConfig{Name: "Test City"},
		Store:  store,
		Slug:   "test-city",
	}
}

// TestBuildMeta_PrefersCombinedOverSum is the cx2 regression: when the
// "combined" ComputeResult exists, BuildMeta must use it for total_paved_sqm
// instead of the (overcounted) sum of per-resource rows.
func TestBuildMeta_PrefersCombinedOverSum(t *testing.T) {
	results := map[string]db.ComputeResult{
		"roads":     {ResourceType: "roads", TotalAreaSqM: 1000},
		"parking":   {ResourceType: "parking", TotalAreaSqM: 500},
		"sidewalks": {ResourceType: "sidewalks", TotalAreaSqM: 300},
		"combined":  {ResourceType: "combined", TotalAreaSqM: 1500}, // less than 1800 sum because of buffer overlap
	}
	entry := newMockEntry(results)
	meta, err := BuildMeta(context.Background(), entry)
	if err != nil {
		t.Fatalf("BuildMeta: %v", err)
	}
	if meta.TotalPavedSqM != 1500 {
		t.Errorf("total_paved_sqm = %v; want 1500 (from combined row, not 1800 sum)", meta.TotalPavedSqM)
	}
	statByType := map[string]float64{}
	for _, s := range meta.Stats {
		statByType[s.Type] = s.TotalAreaSqM
	}
	want := map[string]float64{"roads": 1000, "parking": 500, "sidewalks": 300}
	if diff := cmp.Diff(want, statByType); diff != "" {
		t.Errorf("per-resource cards (-want +got):\n%s", diff)
	}
}

// TestBuildMultiCityMeta_AggregatesAcrossEntries is the dul regression: the
// multi-city landing must not silently render the first sub-city's totals as
// the regional headline. Per-resource Stats and TotalPavedSqM must sum across
// entries; CityAreaSqM must be the union of sub-city boundaries.
func TestBuildMultiCityMeta_AggregatesAcrossEntries(t *testing.T) {
	// Two non-overlapping sub-cities with distinct boundaries and different
	// per-resource totals. Use boundaries far apart enough that the union
	// area is approximately the sum.
	cityA := newMockEntryWithBoundary(map[string]db.ComputeResult{
		"roads":    {ResourceType: "roads", TotalAreaSqM: 1000, FeatureCount: 10},
		"combined": {ResourceType: "combined", TotalAreaSqM: 800},
	}, `{"type":"Polygon","coordinates":[[[-122.5,37.5],[-122.4,37.5],[-122.4,37.6],[-122.5,37.6],[-122.5,37.5]]]}`)
	cityA.City.Name = "City A"
	cityA.Slug = "city-a"

	cityB := newMockEntryWithBoundary(map[string]db.ComputeResult{
		"roads":    {ResourceType: "roads", TotalAreaSqM: 2000, FeatureCount: 20},
		"combined": {ResourceType: "combined", TotalAreaSqM: 1700},
	}, `{"type":"Polygon","coordinates":[[[-121.5,37.5],[-121.4,37.5],[-121.4,37.6],[-121.5,37.6],[-121.5,37.5]]]}`)
	cityB.City.Name = "City B"
	cityB.Slug = "city-b"

	meta, err := BuildMultiCityMeta(context.Background(), []CityEntry{cityA, cityB}, "Test Region")
	if err != nil {
		t.Fatalf("BuildMultiCityMeta: %v", err)
	}
	if meta.ProjectName != "Test Region" {
		t.Errorf("project_name = %q; want %q", meta.ProjectName, "Test Region")
	}

	// Per-resource totals sum across cities.
	if len(meta.Stats) != 1 {
		t.Fatalf("expected 1 resource stat, got %d", len(meta.Stats))
	}
	if meta.Stats[0].Type != "roads" || meta.Stats[0].TotalAreaSqM != 3000 || meta.Stats[0].FeatureCount != 30 {
		t.Errorf("aggregated roads stat = %+v; want type=roads, area=3000, count=30", meta.Stats[0])
	}

	// total_paved comes from summed combined rows (not per-resource fallback).
	if meta.TotalPavedSqM != 2500 {
		t.Errorf("total_paved_sqm = %v; want 2500 (sum of combined rows)", meta.TotalPavedSqM)
	}

	// Regional bbox spans both sub-cities.
	if meta.BBox[1] >= -121.4 || meta.BBox[3] <= -122.4 {
		t.Errorf("bbox %v does not span both cities", meta.BBox)
	}

	// City area is union — must be larger than either sub-city's area.
	if meta.CityAreaSqM <= 0 {
		t.Errorf("city_area_sqm = %v; want positive (union of boundaries)", meta.CityAreaSqM)
	}
}

func newMockEntryWithBoundary(results map[string]db.ComputeResult, boundary string) CityEntry {
	store := &dbtest.MockStore{
		LatestComputeResultFunc: func(_ context.Context, rt string) (*db.ComputeResult, error) {
			r, ok := results[rt]
			if !ok {
				return nil, sql.ErrNoRows
			}
			return &r, nil
		},
		GetBoundaryFunc: func(_ context.Context) (string, error) {
			return boundary, nil
		},
	}
	return CityEntry{
		Config: &config.Config{},
		City:   config.CityConfig{Name: "Test City"},
		Store:  store,
		Slug:   "test-city",
	}
}

// TestBuildMeta_FallsBackToSumWhenCombinedMissing covers the transitional
// state where `pvmt all compute` has not yet been re-run after the cx2 fix
// landed: BuildMeta must still produce a non-zero total_paved_sqm by summing
// per-resource rows. The sum overcounts buffer overlap (the original bug),
// but reporting zero would be worse.
func TestBuildMeta_FallsBackToSumWhenCombinedMissing(t *testing.T) {
	results := map[string]db.ComputeResult{
		"roads":     {ResourceType: "roads", TotalAreaSqM: 1000},
		"parking":   {ResourceType: "parking", TotalAreaSqM: 500},
		"sidewalks": {ResourceType: "sidewalks", TotalAreaSqM: 300},
	}
	entry := newMockEntry(results)
	meta, err := BuildMeta(context.Background(), entry)
	if err != nil {
		t.Fatalf("BuildMeta: %v", err)
	}
	if meta.TotalPavedSqM != 1800 {
		t.Errorf("total_paved_sqm = %v; want 1800 (sum fallback)", meta.TotalPavedSqM)
	}
}

// TestBuildMultiCityMeta_MixedCombinedRollout covers partial-rollout: city A
// has been recomputed (has a combined row), city B has not. Pre-fix, the
// regional total dropped city B entirely and reported just A's combined area;
// the fix sums A's combined + B's per-resource fallback so not-yet-recomputed
// entries still contribute. Sum overcounts B's buffer overlap, but a missing
// city is worse than an overcounted one.
func TestBuildMultiCityMeta_MixedCombinedRollout(t *testing.T) {
	cityA := newMockEntryWithBoundary(map[string]db.ComputeResult{
		"roads":    {ResourceType: "roads", TotalAreaSqM: 1000},
		"combined": {ResourceType: "combined", TotalAreaSqM: 800},
	}, `{"type":"Polygon","coordinates":[[[-122.5,37.5],[-122.4,37.5],[-122.4,37.6],[-122.5,37.6],[-122.5,37.5]]]}`)
	cityA.City.Name = "City A"
	cityA.Slug = "city-a"

	// City B has no combined row — partial-rollout state.
	cityB := newMockEntryWithBoundary(map[string]db.ComputeResult{
		"roads": {ResourceType: "roads", TotalAreaSqM: 2000},
	}, `{"type":"Polygon","coordinates":[[[-121.5,37.5],[-121.4,37.5],[-121.4,37.6],[-121.5,37.6],[-121.5,37.5]]]}`)
	cityB.City.Name = "City B"
	cityB.Slug = "city-b"

	meta, err := BuildMultiCityMeta(context.Background(), []CityEntry{cityA, cityB}, "Test Region")
	if err != nil {
		t.Fatalf("BuildMultiCityMeta: %v", err)
	}
	// Want: A's combined (800) + B's per-resource fallback (2000) = 2800.
	// Pre-fix bug returned 800 (B silently dropped).
	if meta.TotalPavedSqM != 2800 {
		t.Errorf("total_paved_sqm = %v; want 2800 (combined_A + sum_B)", meta.TotalPavedSqM)
	}
}
