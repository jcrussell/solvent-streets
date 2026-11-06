package forecast

import (
	"context"
	"testing"

	"github.com/jcrussell/solvent-streets/internal/config"
	"github.com/jcrussell/solvent-streets/internal/db"
	"github.com/jcrussell/solvent-streets/internal/resource"
)

// TestForecastResource_ReadsThePendingCompute pins the forecast auto-rerun
// against a REAL store: the forecast a recompute triggers must be built from
// the compute the draft has Put (not yet committed), not from the committed
// snapshot's older compute. The mock store's WithParts does not overlay, so
// only a real store can catch a regression here.
func TestForecastResource_ReadsThePendingCompute(t *testing.T) {
	ctx := context.Background()
	root, err := db.Open(":memory:")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = root.Close() })
	id, err := root.EnsureCity(ctx, "c", "C", "test")
	if err != nil {
		t.Fatal(err)
	}
	store := root.ForCity(id)
	saveRoads := func(run int64, area float64) {
		t.Helper()
		if err := store.SaveComputeResult(ctx, db.ComputeResult{ResourceType: resource.TypeRoads, TotalArea: area, RunID: run}); err != nil {
			t.Fatal(err)
		}
		if err := store.SaveCohortStats(ctx, []db.CohortStat{{ResourceType: resource.TypeRoads, Classification: "residential", Area: area, FeatureCount: 1, RunID: run}}); err != nil {
			t.Fatal(err)
		}
	}
	old, _ := store.CreateRun(ctx, "roads", "h")
	saveRoads(old.ID, 100)
	if _, err := store.CommitSnapshot(ctx, "h", map[string]int64{"roads": old.ID}); err != nil {
		t.Fatal(err)
	}

	d := db.NewDraft(store, "h")
	recomputed, err := d.NewRun(ctx, "roads")
	if err != nil {
		t.Fatal(err)
	}
	saveRoads(recomputed, 250)
	d.Put("roads", recomputed)

	fc := (&config.Config{}).ResolvedForecast(nil)
	rf, err := ForecastResource(ctx, d, &resource.Pavement{}, &fc)
	if err != nil {
		t.Fatal(err)
	}
	if rf.Area != 250 {
		t.Errorf("forecast area = %v, want 250 from the pending recompute (100 is the committed, stale compute)", rf.Area)
	}
	snap, err := d.Commit(ctx)
	if err != nil {
		t.Fatal(err)
	}
	var forecastRun int64
	for _, p := range snap.Parts {
		if p.Part == Producer(resource.TypeRoads) {
			forecastRun = p.RunID
		}
	}
	if forecastRun == 0 || len(rf.Results) == 0 || rf.Results[0].RunID != forecastRun {
		t.Errorf("snapshot parts %+v: want forecast:roads pointing at the run the %d forecast rows were saved under", snap.Parts, len(rf.Results))
	}
}
