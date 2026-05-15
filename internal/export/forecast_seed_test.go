package export

import (
	"context"
	"testing"

	"github.com/google/go-cmp/cmp"
	"github.com/google/go-cmp/cmp/cmpopts"

	"github.com/jcrussell/solvent-streets/internal/config"
	"github.com/jcrussell/solvent-streets/internal/db"
	"github.com/jcrussell/solvent-streets/internal/db/dbtest"
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
	// roads/primary, roads/default, parking/default. Pre-fix bucket keyed on
	// classification only collapsed the two "default" entries into one.
	want := []CohortSeed{
		{Classification: "primary", AreaSqM: 1200}, // roads: 1000 + 200
		{Classification: "default", AreaSqM: 500},  // roads "default" only
		{Classification: "default", AreaSqM: 400},  // parking "default": 300 + 100
	}
	if diff := cmp.Diff(want, got, cmpopts.IgnoreFields(CohortSeed{}, "DecayRate")); diff != "" {
		t.Errorf("mergeCohortSeeds (-want +got):\n%s", diff)
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
