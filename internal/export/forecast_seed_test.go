package export

import (
	"context"
	"testing"

	"pvmt/internal/config"
	"pvmt/internal/db"
	"pvmt/internal/db/dbtest"
)

// TestMergeCohortSeeds_KeysOnResourceAndClassification pins the invariant
// that cross-resource classification collisions stay as separate cohorts
// (matching collectCohortSeeds' single-city shape). Pre-fix, "default"
// roads and "default" parking collapsed into one summed cohort.
func TestMergeCohortSeeds_KeysOnResourceAndClassification(t *testing.T) {
	cohortsByLabel := func(m map[string][]db.CohortStat) func(context.Context, string) ([]db.CohortStat, error) {
		return func(_ context.Context, rt string) ([]db.CohortStat, error) {
			return m[rt], nil
		}
	}

	cityA := CityEntry{
		Config: &config.Config{},
		Slug:   "city-a",
		Store: &dbtest.MockStore{
			ListCohortStatsFunc: cohortsByLabel(map[string][]db.CohortStat{
				"roads": {
					{Classification: "primary", AreaSqM: 1000},
					{Classification: "default", AreaSqM: 500},
				},
				"parking": {
					{Classification: "default", AreaSqM: 300},
				},
			}),
		},
	}
	cityB := CityEntry{
		Config: &config.Config{},
		Slug:   "city-b",
		Store: &dbtest.MockStore{
			ListCohortStatsFunc: cohortsByLabel(map[string][]db.CohortStat{
				"roads": {
					{Classification: "primary", AreaSqM: 200},
				},
				"parking": {
					{Classification: "default", AreaSqM: 100},
				},
			}),
		},
	}

	fc := &config.ForecastConfig{}
	got := mergeCohortSeeds(context.Background(), []CityEntry{cityA, cityB}, fc, false)

	// Three distinct (resource, classification) pairs across both cities:
	// roads/primary, roads/default, parking/default. With the pre-fix bucket
	// keyed on classification only, the two "default" entries collapsed into
	// one and len(got) was 2.
	if len(got) != 3 {
		t.Fatalf("len(cohorts) = %d; want 3 (roads/primary, roads/default, parking/default — got %+v)", len(got), got)
	}

	type want struct {
		Class string
		Area  float64
	}
	wants := []want{
		{Class: "primary", Area: 1200}, // roads: 1000 + 200
		{Class: "default", Area: 500},  // roads "default" only
		{Class: "default", Area: 400},  // parking "default": 300 + 100
	}
	for i, w := range wants {
		if got[i].Classification != w.Class || got[i].AreaSqM != w.Area {
			t.Errorf("got[%d] = {class=%q, area=%v}; want {class=%q, area=%v}",
				i, got[i].Classification, got[i].AreaSqM, w.Class, w.Area)
		}
	}
}

// TestMergeCohortSeeds_CityScopeReadsCityLabels verifies cityScope=true
// drives the ":city"-suffixed cohort label, not the bbox label.
func TestMergeCohortSeeds_CityScopeReadsCityLabels(t *testing.T) {
	var seenLabels []string
	entry := CityEntry{
		Config: &config.Config{},
		Slug:   "city-a",
		Store: &dbtest.MockStore{
			ListCohortStatsFunc: func(_ context.Context, rt string) ([]db.CohortStat, error) {
				seenLabels = append(seenLabels, rt)
				return nil, nil
			},
		},
	}
	mergeCohortSeeds(context.Background(), []CityEntry{entry}, &config.ForecastConfig{}, true)

	for _, label := range seenLabels {
		if len(label) < len(":city") || label[len(label)-len(":city"):] != ":city" {
			t.Errorf("cityScope=true read label %q; want all labels to end in :city", label)
		}
	}
	if len(seenLabels) == 0 {
		t.Errorf("expected ListCohortStats to be called for each resource type")
	}
}
