package results

import (
	"context"
	"errors"
	"slices"
	"testing"

	"github.com/google/go-cmp/cmp"

	"github.com/jcrussell/solvent-streets/internal/db"
	"github.com/jcrussell/solvent-streets/internal/db/dbtest"
	"github.com/jcrussell/solvent-streets/internal/resource"
)

var (
	roads       = resource.TypeRoads
	roadsCity   = resource.TypeRoads.With(resource.ScopeCity)
	parking     = resource.TypeParking
	parkingCity = resource.TypeParking.With(resource.ScopeCity)
)

// TestRoundTrip: what the row builders write, the loaders read back, through
// the store's own ordering.
func TestRoundTrip(t *testing.T) {
	ctx := context.Background()
	store := &dbtest.MockStore{}
	rows := append(TotalRows(roads, Totals{Area: 1000, Features: 4}),
		CohortRows(roads, []Cohort{{Class: "residential", Area: 700, Features: 3}, {Class: "primary", Area: 300, Features: 1}})...)
	rows = append(rows, TotalRows(parkingCity, Totals{Area: 50, Features: 2, Spaces: 9})...)
	if err := store.SaveCityMetrics(ctx, 7, rows); err != nil {
		t.Fatal(err)
	}
	if err := store.SaveHexMetrics(ctx, 7, HexCoverageRows(roads, []HexCoverage{{HexID: "b", Area: 2, PctCovered: 20}, {HexID: "a", Area: 1, PctCovered: 10}})); err != nil {
		t.Fatal(err)
	}

	totals, cohorts, err := Load(ctx, store, []resource.Type{roads, roadsCity, parkingCity})
	if err != nil {
		t.Fatal(err)
	}
	wantTotals := map[resource.Type]Totals{
		roads:       {Area: 1000, Features: 4, RunID: 7},
		parkingCity: {Area: 50, Features: 2, Spaces: 9, RunID: 7},
	}
	if diff := cmp.Diff(wantTotals, totals); diff != "" {
		t.Errorf("totals (-want +got):\n%s", diff)
	}
	// Cohorts come back ordered by class, whatever order they were written in.
	wantCohorts := map[resource.Type][]Cohort{
		roads: {{Class: "primary", Area: 300, Features: 1}, {Class: "residential", Area: 700, Features: 3}},
	}
	if diff := cmp.Diff(wantCohorts, cohorts); diff != "" {
		t.Errorf("cohorts (-want +got):\n%s", diff)
	}

	if _, ok, err := LoadTotal(ctx, store, roadsCity); err != nil || ok {
		t.Errorf("LoadTotal(roads:city) = (_, %v, %v), want not computed", ok, err)
	}
	cov, err := LoadHexCoverage(ctx, store, roads)
	if err != nil {
		t.Fatal(err)
	}
	if want := []HexCoverage{{HexID: "a", Area: 1, PctCovered: 10}, {HexID: "b", Area: 2, PctCovered: 20}}; !slices.Equal(cov, want) {
		t.Errorf("coverage = %+v, want %+v (by hex id)", cov, want)
	}
	if ok, err := HasHexCoverage(ctx, store, []resource.Type{parking, roads}); err != nil || !ok {
		t.Errorf("HasHexCoverage(parking, roads) = (%v, %v), want true", ok, err)
	}
	if ok, _ := HasHexCoverage(ctx, store, []resource.Type{parking}); ok {
		t.Error("HasHexCoverage(parking) = true, want false")
	}
}

// TestTotalRows_SpacesOnlyForParking: every total is written even when 0 (a
// missing row means never computed), and parking_spaces only for parking.
func TestTotalRows_SpacesOnlyForParking(t *testing.T) {
	metrics := func(rows []db.CityMetric) []string {
		var out []string
		for _, r := range rows {
			out = append(out, r.Metric)
		}
		return out
	}
	if got := metrics(TotalRows(roads, Totals{})); !slices.Equal(got, []string{"paved_area_m2", "feature_count"}) {
		t.Errorf("roads rows = %v", got)
	}
	if got := metrics(TotalRows(parkingCity, Totals{})); !slices.Equal(got, []string{"paved_area_m2", "feature_count", "parking_spaces"}) {
		t.Errorf("parking:city rows = %v", got)
	}
}

// TestPivot_NewerRunWins: should two runs in one view both write a subject,
// the newer run's totals and cohorts are read, never a mix.
func TestPivot_NewerRunWins(t *testing.T) {
	var rows []db.CityMetric
	for _, r := range []struct {
		run  int64
		area float64
	}{{2, 200}, {1, 100}} {
		for _, m := range append(TotalRows(roads, Totals{Area: r.area}), CohortRows(roads, []Cohort{{Class: "primary", Area: r.area}, {Class: "service", Area: r.area}})...) {
			m.RunID = r.run
			rows = append(rows, m)
		}
	}
	// The store's order: subject, class, metric, run.
	slices.SortStableFunc(rows, func(a, b db.CityMetric) int {
		if a.Class != b.Class {
			if a.Class < b.Class {
				return -1
			}
			return 1
		}
		if a.Metric != b.Metric {
			if a.Metric < b.Metric {
				return -1
			}
			return 1
		}
		return int(a.RunID - b.RunID)
	})
	totals, cohorts := Pivot(rows)
	if got := totals[roads]; got.Area != 200 || got.RunID != 2 {
		t.Errorf("totals = %+v, want run 2's 200", got)
	}
	want := []Cohort{{Class: "primary", Area: 200}, {Class: "service", Area: 200}}
	if diff := cmp.Diff(want, cohorts[roads]); diff != "" {
		t.Errorf("cohorts (-want +got):\n%s", diff)
	}
}

// PavedArea prefers the combined (de-duplicated) total in the asked scope and
// otherwise sums that scope's per-resource totals.
func TestPavedArea(t *testing.T) {
	ctx := context.Background()
	var rows []db.CityMetric
	rows = append(rows, TotalRows(resource.TypeRoads, Totals{Area: 100})...)
	rows = append(rows, TotalRows(resource.TypeParking, Totals{Area: 30})...)
	rows = append(rows, TotalRows(resource.TypeRoads.With(resource.ScopeCity), Totals{Area: 60})...)
	store := &dbtest.MockStore{CityMetrics: rows}

	area, deduped, err := PavedArea(ctx, store, resource.ScopeAll)
	if err != nil || deduped || area != 130 {
		t.Errorf("no combined total: got (%v, %v, %v), want the per-resource sum 130, not de-duplicated", area, deduped, err)
	}
	area, deduped, err = PavedArea(ctx, store, resource.ScopeCity)
	if err != nil || deduped || area != 60 {
		t.Errorf("city scope: got (%v, %v, %v), want 60", area, deduped, err)
	}

	store.CityMetrics = append(store.CityMetrics, TotalRows(resource.CombinedAll, Totals{Area: 120})...)
	area, deduped, err = PavedArea(ctx, store, resource.ScopeAll)
	if err != nil || !deduped || area != 120 {
		t.Errorf("with a combined total: got (%v, %v, %v), want 120, de-duplicated", area, deduped, err)
	}
	if area, deduped, _ = PavedArea(ctx, store, resource.ScopeCity); deduped || area != 60 {
		t.Errorf("an all-scope combined total must not answer for the city scope: got (%v, %v)", area, deduped)
	}

	failing := &dbtest.MockStore{ListCityMetricsFunc: func(context.Context, []string) ([]db.CityMetric, error) {
		return nil, errors.New("db unavailable")
	}}
	if _, _, err := PavedArea(ctx, failing, resource.ScopeAll); err == nil {
		t.Error("a read failure must be returned, not reported as zero area")
	}
}
