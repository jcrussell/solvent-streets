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
	got, _ := mergeCohortSeeds(context.Background(), []CityEntry{cityA, cityB}, fc, false)

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

// TestForecastSeeds_CarryMaterialTiers pins that both the per-city seed
// (BuildForecastSeed, consumed by the Materials tab) and the region seed
// (BuildMultiCityForecastSeed) ship the default material tiers, label-aligned
// with the cost tiers. Without them the Materials tab has no per-tier intensity
// to multiply treated area by.
func TestForecastSeeds_CarryMaterialTiers(t *testing.T) {
	fc := &config.ForecastConfig{}

	assertMaterialTiers := func(t *testing.T, seed ForecastSeedJSON) {
		t.Helper()
		if len(seed.MaterialTiers) == 0 {
			t.Fatalf("MaterialTiers is empty; want the default asphalt tiers")
		}
		// The binder→oil factor must ship so the Materials tab shares the Go
		// model's constant instead of its own duplicated literal.
		if seed.BarrelsPerTonBinder != forecast.BarrelsPerTonBinder {
			t.Errorf("BarrelsPerTonBinder = %v, want %v", seed.BarrelsPerTonBinder, forecast.BarrelsPerTonBinder)
		}
		// Every cost tier must have a same-labeled material tier.
		for _, ct := range seed.CostTiers {
			found := false
			for _, mt := range seed.MaterialTiers {
				if mt.Label == ct.Label {
					found = true
					break
				}
			}
			if !found {
				t.Errorf("no material tier for cost tier %q", ct.Label)
			}
		}
	}

	t.Run("per-city seed", func(t *testing.T) {
		js, err := BuildForecastSeed(context.Background(), fc, &dbtest.MockStore{})
		if err != nil {
			t.Fatalf("BuildForecastSeed: %v", err)
		}
		var seed ForecastSeedJSON
		if err := json.Unmarshal([]byte(js), &seed); err != nil {
			t.Fatalf("unmarshal seed: %v", err)
		}
		assertMaterialTiers(t, seed)
	})

	t.Run("region seed", func(t *testing.T) {
		entry := CityEntry{Config: &config.Config{}, Slug: "city-a", Store: &dbtest.MockStore{}}
		assertMaterialTiers(t, decodeSeed(t, entry, fc))
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

// TestForecastSeed_AsphaltShareNetsOutSidewalks pins solvent-streets-q48z.11.
//
// DefaultMaterialTiers describes flexible (asphalt) pavement — mix mass and
// asphalt-cement binder per m² — and forecast/material.go says outright that
// sidewalks are concrete. But the tiers were attached to the seed
// unconditionally, and the areas the Materials tab multiplies them by
// (scenario.years[].area) sum every resource in resource.All. So the mix,
// binder and "Annual Binder (Oil)" headlines billed concrete sidewalk panels,
// which consume zero bitumen, as hot-mix asphalt — overstated by the sidewalk
// share, measured at 1.4% (alameda-ca) to 8.9% (austin-tx) in a real export.
//
// The share must be computed on the COHORT area basis, because that is what
// scenario.years[].area sums; the combined compute rows behind total_area are
// deduped union geometry and would give a slightly different, wrong answer.
func TestForecastSeed_AsphaltShareNetsOutSidewalks(t *testing.T) {
	ctx := context.Background()
	fc := &config.ForecastConfig{Years: 5, InitialPCI: 80}

	// Roads 700 + parking 300 = 1000 asphalt, sidewalks 250 concrete, in the
	// bbox scope; a deliberately different split in the city scope so a swapped
	// or shared share shows up.
	cohorts := map[resource.Type][]db.CohortStat{
		resource.TypeRoads:                              {{Classification: "residential", Area: 700}},
		resource.TypeParking:                            {{Classification: "parking", Area: 300}},
		resource.TypeSidewalks:                          {{Classification: "sidewalks", Area: 250}},
		resource.TypeRoads.With(resource.ScopeCity):     {{Classification: "residential", Area: 400}},
		resource.TypeParking.With(resource.ScopeCity):   {{Classification: "parking", Area: 100}},
		resource.TypeSidewalks.With(resource.ScopeCity): {{Classification: "sidewalks", Area: 100}},
	}
	store := &dbtest.MockStore{
		ListCohortStatsFunc: func(_ context.Context, rt resource.Type) ([]db.CohortStat, error) {
			return cohorts[rt], nil
		},
	}

	got, err := collectCohortSeeds(ctx, store, fc)
	if err != nil {
		t.Fatalf("collectCohortSeeds: %v", err)
	}
	// 1000 asphalt of 1250 total.
	if want := 1000.0 / 1250.0; got.BBoxAsphaltShare != want {
		t.Errorf("BBoxAsphaltShare = %v; want %v (roads 700 + parking 300 of 1250)", got.BBoxAsphaltShare, want)
	}
	// 500 asphalt of 600 total — a different ratio, so the two are not shared.
	if want := 500.0 / 600.0; got.CityAsphaltShare != want {
		t.Errorf("CityAsphaltShare = %v; want %v (roads 400 + parking 100 of 600)", got.CityAsphaltShare, want)
	}

	// And it reaches the browser under the keys app.js reads.
	seedJS, err := BuildForecastSeed(ctx, fc, store)
	if err != nil {
		t.Fatalf("BuildForecastSeed: %v", err)
	}
	var seed map[string]any
	if err := json.Unmarshal([]byte(seedJS), &seed); err != nil {
		t.Fatalf("unmarshal seed: %v", err)
	}
	if got, want := seed["asphalt_area_share"], 1000.0/1250.0; got != want {
		t.Errorf("seed asphalt_area_share = %v; want %v", got, want)
	}
	if got, want := seed["city_asphalt_area_share"], 500.0/600.0; got != want {
		t.Errorf("seed city_asphalt_area_share = %v; want %v", got, want)
	}
}

// TestForecastSeed_AsphaltShareIsOneWithoutCohorts: 1 is the identity for the
// browser's multiplication, so a store with no cohort stats — a fresh DB, where
// BuildScenariosData falls back to singleCohortScenarios over the aggregate
// compute area — must behave exactly as it did before the netting existed.
// A 0 here would blank every Materials figure.
func TestForecastSeed_AsphaltShareIsOneWithoutCohorts(t *testing.T) {
	store := &dbtest.MockStore{
		ListCohortStatsFunc: func(context.Context, resource.Type) ([]db.CohortStat, error) { return nil, nil },
	}
	got, err := collectCohortSeeds(context.Background(), store, &config.ForecastConfig{})
	if err != nil {
		t.Fatalf("collectCohortSeeds: %v", err)
	}
	if got.BBoxAsphaltShare != 1 || got.CityAsphaltShare != 1 {
		t.Errorf("shares = bbox %v, city %v; want 1 and 1 (no cohorts means no netting)",
			got.BBoxAsphaltShare, got.CityAsphaltShare)
	}
}

// TestMergeCohortSeeds_AsphaltShareIsRegionWide: a region's asphalt share is
// the region's asphalt area over its total area, NOT the average of its
// cities' shares — a large sidewalk-heavy city and a small one must not carry
// equal weight. Cities here are deliberately different sizes with different
// internal splits so an averaging implementation gives a different number.
func TestMergeCohortSeeds_AsphaltShareIsRegionWide(t *testing.T) {
	entry := func(roads, sidewalks float64) CityEntry {
		stats := map[resource.Type][]db.CohortStat{
			resource.TypeRoads:     {{Classification: "residential", Area: roads}},
			resource.TypeSidewalks: {{Classification: "sidewalks", Area: sidewalks}},
		}
		return CityEntry{Store: &dbtest.MockStore{
			ListCohortStatsForTypesFunc: func(_ context.Context, types []resource.Type) (map[resource.Type][]db.CohortStat, error) {
				out := map[resource.Type][]db.CohortStat{}
				for _, t := range types {
					if s, ok := stats[t]; ok {
						out[t] = s
					}
				}
				return out, nil
			},
		}}
	}
	// big: 900/1000 asphalt (0.9). small: 10/100 asphalt (0.1).
	// Region-wide: 910/1100. Averaging the two would give 0.5.
	entries := []CityEntry{entry(900, 100), entry(10, 90)}
	_, share := mergeCohortSeeds(context.Background(), entries, &config.ForecastConfig{}, false)
	if want := 910.0 / 1100.0; share != want {
		t.Errorf("region asphalt share = %v; want %v (area-weighted, not the 0.5 average of 0.9 and 0.1)", share, want)
	}
}
