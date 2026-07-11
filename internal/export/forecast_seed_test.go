package export

import (
	"context"
	"encoding/json"
	"testing"

	"github.com/google/go-cmp/cmp"
	"github.com/google/go-cmp/cmp/cmpopts"

	"github.com/jcrussell/solvent-streets/internal/config"
	"github.com/jcrussell/solvent-streets/internal/db"
	"github.com/jcrussell/solvent-streets/internal/db/dbtest"
	"github.com/jcrussell/solvent-streets/internal/forecast"
	"github.com/jcrussell/solvent-streets/internal/resource"
)

// TestMergeCohortSeeds_KeysOnResourceAndClassification pins the invariant
// that cross-resource classification collisions stay as separate cohorts
// (matching collectCohortSeeds' single-city shape). Pre-fix, "default"
// roads and "default" parking collapsed into one summed cohort.
func TestMergeCohortSeeds_KeysOnResourceAndClassification(t *testing.T) {
	cohortsByLabel := func(m map[resource.Type][]db.CohortStat) func(context.Context, resource.Type) ([]db.CohortStat, error) {
		return func(_ context.Context, rt resource.Type) ([]db.CohortStat, error) {
			return m[rt], nil
		}
	}

	rtRoads := resource.TypeRoads
	rtParking := resource.TypeParking

	cityA := CityEntry{
		Config: &config.Config{},
		Slug:   "city-a",
		Store: &dbtest.MockStore{
			ListCohortStatsFunc: cohortsByLabel(map[resource.Type][]db.CohortStat{
				rtRoads: {
					{Classification: "primary", Area: 1000},
					{Classification: "default", Area: 500},
				},
				rtParking: {
					{Classification: "default", Area: 300},
				},
			}),
		},
	}
	cityB := CityEntry{
		Config: &config.Config{},
		Slug:   "city-b",
		Store: &dbtest.MockStore{
			ListCohortStatsFunc: cohortsByLabel(map[resource.Type][]db.CohortStat{
				rtRoads: {
					{Classification: "primary", Area: 200},
				},
				rtParking: {
					{Classification: "default", Area: 100},
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
		{Classification: "primary", Area: 1200}, // roads: 1000 + 200
		{Classification: "default", Area: 500},  // roads "default" only
		{Classification: "default", Area: 400},  // parking "default": 300 + 100
	}
	if diff := cmp.Diff(want, got, cmpopts.IgnoreFields(CohortSeed{}, "DecayRate")); diff != "" {
		t.Errorf("mergeCohortSeeds (-want +got):\n%s", diff)
	}
}

// TestBuildMultiCityForecastSeed_CarriesTreatmentCycleYears pins yvlv.17: the
// region seed must carry the resolved treatment_cycle_years so the multi-city
// landing's interactive Custom Scenario line uses the same cycle N as the
// per-city static lines. Pre-fix the field was omitted, emitting 0, which the
// WASM bridge silently resolved to the default 12 — discarding a configured
// non-default value.
func TestBuildMultiCityForecastSeed_CarriesTreatmentCycleYears(t *testing.T) {
	entry := CityEntry{
		Config: &config.Config{},
		Slug:   "city-a",
		Store:  &dbtest.MockStore{},
	}

	t.Run("configured value survives", func(t *testing.T) {
		fc := &config.ForecastConfig{TreatmentCycleYears: 8}
		seed := decodeSeed(t, entry, fc)
		if seed.TreatmentCycleYears != 8 {
			t.Errorf("TreatmentCycleYears = %v; want 8 (configured value must survive)", seed.TreatmentCycleYears)
		}
	})

	t.Run("unset resolves to default", func(t *testing.T) {
		fc := &config.ForecastConfig{}
		seed := decodeSeed(t, entry, fc)
		if seed.TreatmentCycleYears != forecast.DefaultTreatmentCycleYears {
			t.Errorf("TreatmentCycleYears = %v; want %v (default) when unset", seed.TreatmentCycleYears, forecast.DefaultTreatmentCycleYears)
		}
	})
}

// decodeSeed builds the multi-city seed for a single entry and unmarshals it.
func decodeSeed(t *testing.T, entry CityEntry, fc *config.ForecastConfig) ForecastSeedJSON {
	t.Helper()
	js, err := BuildMultiCityForecastSeed(context.Background(), fc, []CityEntry{entry})
	if err != nil {
		t.Fatalf("BuildMultiCityForecastSeed: %v", err)
	}
	var seed ForecastSeedJSON
	if err := json.Unmarshal([]byte(js), &seed); err != nil {
		t.Fatalf("unmarshal seed: %v", err)
	}
	return seed
}

// TestMergeCohortSeeds_CityScopeReadsCityLabels verifies cityScope=true
// drives the ":city"-suffixed cohort label, not the bbox label.
func TestMergeCohortSeeds_CityScopeReadsCityLabels(t *testing.T) {
	var seenLabels []resource.Type
	entry := CityEntry{
		Config: &config.Config{},
		Slug:   "city-a",
		Store: &dbtest.MockStore{
			ListCohortStatsFunc: func(_ context.Context, rt resource.Type) ([]db.CohortStat, error) {
				seenLabels = append(seenLabels, rt)
				return nil, nil
			},
		},
	}
	mergeCohortSeeds(context.Background(), []CityEntry{entry}, &config.ForecastConfig{}, true)

	for _, label := range seenLabels {
		if label.Scope() != resource.ScopeCity {
			t.Errorf("cityScope=true read label %q; want all labels to be ScopeCity", label)
		}
	}
	if len(seenLabels) == 0 {
		t.Errorf("expected ListCohortStats to be called for each resource type")
	}
}
