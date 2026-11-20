package export

import (
	"context"
	"encoding/json"
	"errors"
	"testing"

	"github.com/google/go-cmp/cmp"
	"github.com/google/go-cmp/cmp/cmpopts"

	"github.com/jcrussell/solvent-streets/internal/config"
	"github.com/jcrussell/solvent-streets/internal/db"
	"github.com/jcrussell/solvent-streets/internal/db/dbtest"
	"github.com/jcrussell/solvent-streets/internal/forecast"
	"github.com/jcrussell/solvent-streets/internal/resource"
	"github.com/jcrussell/solvent-streets/internal/results"
)

// TestMergeCohortSeeds_KeysOnResourceAndClassification pins the invariant
// that cross-resource classification collisions stay as separate cohorts
// (matching collectCohortSeeds' single-city shape): "default" roads and
// "default" parking must not collapse into one summed cohort.
func TestMergeCohortSeeds_KeysOnResourceAndClassification(t *testing.T) {
	rtRoads := resource.TypeRoads
	rtParking := resource.TypeParking

	cityA := CityEntry{
		Config: &config.Config{},
		Slug:   "city-a",
		Store: &dbtest.MockStore{
			CityMetrics: cohortsFixture(map[resource.Type][]results.Cohort{
				rtRoads: {
					{Class: "primary", Area: 1000},
					{Class: "default", Area: 500},
				},
				rtParking: {
					{Class: "default", Area: 300},
				},
			}),
		},
	}
	cityB := CityEntry{
		Config: &config.Config{},
		Slug:   "city-b",
		Store: &dbtest.MockStore{
			CityMetrics: cohortsFixture(map[resource.Type][]results.Cohort{
				rtRoads: {
					{Class: "primary", Area: 200},
				},
				rtParking: {
					{Class: "default", Area: 100},
				},
			}),
		},
	}

	fc := &config.ForecastConfig{}
	got, _, _ := mergeCohortSeeds(context.Background(), []CityEntry{cityA, cityB}, fc, false)

	// Three distinct (resource, classification) pairs across both cities:
	// roads/primary, roads/default, parking/default. Keying on classification
	// alone would collapse the two "default" entries into one.
	// Order is first-seen: resource.All order, then each resource's cohorts
	// by class, as the store returns them.
	want := []CohortSeed{
		{ResourceType: "roads", Classification: "default", Area: 500},   // roads "default" only
		{ResourceType: "roads", Classification: "primary", Area: 1200},  // roads: 1000 + 200
		{ResourceType: "parking", Classification: "default", Area: 400}, // parking "default": 300 + 100
	}
	if diff := cmp.Diff(want, got, cmpopts.IgnoreFields(CohortSeed{}, "DecayRate")); diff != "" {
		t.Errorf("mergeCohortSeeds (-want +got):\n%s", diff)
	}
}

// TestMergeCohortSeeds_PerCityDecayRate: a city whose
// own forecast overrides decay_rate keeps its road cohorts at that rate in the
// regional seed — the rate its forecast.json uses — rather than taking the
// region's. Cities that resolve to the same rate still merge.
func TestMergeCohortSeeds_PerCityDecayRate(t *testing.T) {
	cfg := &config.Config{}
	roads := func(area float64) *dbtest.MockStore {
		return &dbtest.MockStore{CityMetrics: cohortsFixture(map[resource.Type][]results.Cohort{
			resource.TypeRoads: {{Class: "primary", Area: area}},
		})}
	}
	plainA := CityEntry{Config: cfg, City: config.CityConfig{Name: "A"}, Slug: "a", Store: roads(100)}
	plainB := CityEntry{Config: cfg, City: config.CityConfig{Name: "B"}, Slug: "b", Store: roads(50)}
	override := CityEntry{
		Config: cfg,
		City:   config.CityConfig{Name: "C", Forecast: &config.ForecastConfig{DecayRate: 0.05}},
		Slug:   "c",
		Store:  roads(30),
	}

	got, _, err := mergeCohortSeeds(context.Background(), []CityEntry{plainA, override, plainB}, &config.ForecastConfig{}, false)
	if err != nil {
		t.Fatalf("mergeCohortSeeds: %v", err)
	}
	want := []CohortSeed{
		{ResourceType: "roads", Classification: "primary", Area: 150, DecayRate: forecast.ClassDecayRate("primary", 0)},
		{ResourceType: "roads", Classification: "primary", Area: 30, DecayRate: forecast.ClassDecayRate("primary", 0.05)},
	}
	if want[0].DecayRate == want[1].DecayRate {
		t.Fatal("fixture: the override must change the primary rate")
	}
	if diff := cmp.Diff(want, got); diff != "" {
		t.Errorf("mergeCohortSeeds (-want +got):\n%s", diff)
	}
}

// TestBuildMultiCityForecastSeed_CarriesTreatmentCycleYears: the region seed
// must carry the resolved treatment_cycle_years so the multi-city landing's
// interactive Custom Scenario line uses the same cycle N as the per-city
// static lines. A missing field decodes as 0, which the WASM bridge silently
// resolves to the default 12 — discarding a configured non-default value.
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
// drives the ":city"-suffixed cohort label, not the all label.
func TestMergeCohortSeeds_CityScopeReadsCityLabels(t *testing.T) {
	var seen []string
	entry := CityEntry{
		Config: &config.Config{},
		Slug:   "city-a",
		Store: &dbtest.MockStore{
			ListCityMetricsFunc: func(_ context.Context, subjects []string) ([]db.CityMetric, error) {
				seen = append(seen, subjects...)
				return nil, nil
			},
		},
	}
	if _, _, err := mergeCohortSeeds(context.Background(), []CityEntry{entry}, &config.ForecastConfig{}, true); err != nil {
		t.Fatal(err)
	}

	for _, subject := range seen {
		if resource.Type(subject).Scope() != resource.ScopeCity {
			t.Errorf("cityScope=true read subject %q; want every subject to be city-scoped", subject)
		}
	}
	if len(seen) == 0 {
		t.Errorf("expected the cohort split to be read for each resource type")
	}
}

// TestForecastSeed_AsphaltShareNetsOutSidewalks:
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
// scenario.years[].area sums; the combined compute rows behind area are
// deduped union geometry and would give a slightly different, wrong answer.
func TestForecastSeed_AsphaltShareNetsOutSidewalks(t *testing.T) {
	ctx := context.Background()
	fc := &config.ForecastConfig{HorizonYears: 5, InitialPCI: 80}

	// Roads 700 + parking 300 = 1000 asphalt, sidewalks 250 concrete, in the
	// all scope; a deliberately different split in the city scope so a swapped
	// or shared share shows up.
	cohorts := map[resource.Type][]results.Cohort{
		resource.TypeRoads:                              {{Class: "residential", Area: 700}},
		resource.TypeParking:                            {{Class: "parking", Area: 300}},
		resource.TypeSidewalks:                          {{Class: "sidewalks", Area: 250}},
		resource.TypeRoads.With(resource.ScopeCity):     {{Class: "residential", Area: 400}},
		resource.TypeParking.With(resource.ScopeCity):   {{Class: "parking", Area: 100}},
		resource.TypeSidewalks.With(resource.ScopeCity): {{Class: "sidewalks", Area: 100}},
	}
	store := &dbtest.MockStore{CityMetrics: cohortsFixture(cohorts)}

	got, err := collectCohortSeeds(ctx, store, fc)
	if err != nil {
		t.Fatalf("collectCohortSeeds: %v", err)
	}
	// 1000 asphalt of 1250 total.
	if want := 1000.0 / 1250.0; got.AllAsphaltShare != want {
		t.Errorf("AllAsphaltShare = %v; want %v (roads 700 + parking 300 of 1250)", got.AllAsphaltShare, want)
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
	store := &dbtest.MockStore{}
	got, err := collectCohortSeeds(context.Background(), store, &config.ForecastConfig{})
	if err != nil {
		t.Fatalf("collectCohortSeeds: %v", err)
	}
	if got.AllAsphaltShare != 1 || got.CityAsphaltShare != 1 {
		t.Errorf("shares = all %v, city %v; want 1 and 1 (no cohorts means no netting)",
			got.AllAsphaltShare, got.CityAsphaltShare)
	}
}

// TestMergeCohortSeeds_AsphaltShareIsRegionWide: a region's asphalt share is
// the region's asphalt area over its total area, NOT the average of its
// cities' shares — a large sidewalk-heavy city and a small one must not carry
// equal weight. Cities here are deliberately different sizes with different
// internal splits so an averaging implementation gives a different number.
func TestMergeCohortSeeds_AsphaltShareIsRegionWide(t *testing.T) {
	entry := func(roads, sidewalks float64) CityEntry {
		stats := map[resource.Type][]results.Cohort{
			resource.TypeRoads:     {{Class: "residential", Area: roads}},
			resource.TypeSidewalks: {{Class: "sidewalks", Area: sidewalks}},
		}
		return CityEntry{Store: &dbtest.MockStore{CityMetrics: cohortsFixture(stats)}}
	}
	// big: 900/1000 asphalt (0.9). small: 10/100 asphalt (0.1).
	// Region-wide: 910/1100. Averaging the two would give 0.5.
	entries := []CityEntry{entry(900, 100), entry(10, 90)}
	_, share, _ := mergeCohortSeeds(context.Background(), entries, &config.ForecastConfig{}, false)
	if want := 910.0 / 1100.0; share != want {
		t.Errorf("region asphalt share = %v; want %v (area-weighted, not the 0.5 average of 0.9 and 0.1)", share, want)
	}
}

// TestSeedDecayRate_MatchesForecastCohorts: the
// seed cohorts the dashboard and WASM simulate, and the game's hexes, resolve
// each class's decay (forecast.ClassDecayRate) exactly as forecast.json's
// cohorts (forecast.CohortsFromInputs) do. With a
// decay_rate override the seeds once set every road class to the override
// while forecast.json scaled each class around it, so the Financials headline
// changed numbers once the WASM run replaced the forecast.json one.
func TestSeedDecayRate_MatchesForecastCohorts(t *testing.T) {
	classes := []string{"parking", "sidewalks", "footway", "unknown", "primary_link"}
	for c := range forecast.RoadDecayRates {
		classes = append(classes, c)
	}
	for _, override := range []float64{0, 0.03, 0.06} {
		for _, c := range classes {
			want := forecast.CohortsFromInputs([]forecast.CohortInput{{Classification: c, Area: 1}}, 85, override)[0].DecayRate
			if got := forecast.ClassDecayRate(c, override); got != want {
				t.Errorf("override %g, class %q: seed decay %g, forecast.json decay %g", override, c, got, want)
			}
		}
	}
}

// A failed cohort read fails the multi-city seed, as it fails a single city's:
// a seed quietly missing one city's cohorts would publish a region forecast
// that undercounts the network.
func TestBuildMultiCityForecastSeed_CohortReadErrorIsReturned(t *testing.T) {
	ok := &dbtest.MockStore{}
	failing := &dbtest.MockStore{ListCityMetricsFunc: func(_ context.Context, subjects []string) ([]db.CityMetric, error) {
		return nil, errors.New("db unavailable")
	}}
	cfg := &config.Config{Cities: []config.CityConfig{{Name: "A"}, {Name: "B"}}}
	entries := []CityEntry{
		{Config: cfg, City: cfg.Cities[0], Store: ok, Slug: "a"},
		{Config: cfg, City: cfg.Cities[1], Store: failing, Slug: "b"},
	}
	if _, err := BuildMultiCityForecastSeed(context.Background(), &config.ForecastConfig{}, entries); err == nil {
		t.Fatal("expected the failed read to be returned")
	}
	if _, _, err := mergeCohortSeeds(context.Background(), entries, &config.ForecastConfig{}, true); err == nil {
		t.Fatal("mergeCohortSeeds must return a failed cohort read")
	}
}

// One city's seed and a one-city region's seed are the same document: both are
// assembled by marshalForecastSeed from the same settings, areas and cohorts.
func TestForecastSeed_SingleAndMultiCityAgree(t *testing.T) {
	var rows []db.CityMetric
	rows = append(rows, results.TotalRows(resource.TypeRoads, results.Totals{Area: 900})...)
	rows = append(rows, results.CohortRows(resource.TypeRoads, []results.Cohort{{Class: "primary", Area: 400}, {Class: "residential", Area: 500}})...)
	rows = append(rows, results.CohortRows(resource.TypeSidewalks, []results.Cohort{{Class: "sidewalks", Area: 100}})...)
	store := &dbtest.MockStore{CityMetrics: rows}
	cfg := &config.Config{Cities: []config.CityConfig{{Name: "A"}}}
	entry := CityEntry{Config: cfg, City: cfg.Cities[0], Store: store, Slug: "a"}
	fc := cfg.ResolvedForecast(&entry.City)

	single, err := BuildForecastSeed(context.Background(), &fc, store)
	if err != nil {
		t.Fatal(err)
	}
	multi, err := BuildMultiCityForecastSeed(context.Background(), &fc, []CityEntry{entry})
	if err != nil {
		t.Fatal(err)
	}
	if single != multi {
		t.Errorf("seeds differ:\nsingle %s\n multi %s", single, multi)
	}
}

// TestForecastSeed_AreasAreTheMaintainedArea:
// the seed's areas are the maintained area — roads + parking + sidewalks
// summed, the basis the cohorts sum to and the forecast prices — not the
// deduplicated paved footprint meta.json publishes as total_paved. The
// browser divides the forecast's dollars by them for every per-area figure,
// so a footprint here would mix two bases in one ratio.
func TestForecastSeed_AreasAreTheMaintainedArea(t *testing.T) {
	ctx := context.Background()
	totals := map[resource.Type]results.Totals{
		resource.TypeRoads:                              {Area: 700},
		resource.TypeParking:                            {Area: 300},
		resource.TypeSidewalks:                          {Area: 250},
		resource.TypeRoads.With(resource.ScopeCity):     {Area: 400},
		resource.TypeSidewalks.With(resource.ScopeCity): {Area: 100},
		// The footprint: smaller than the sum by the overlap.
		resource.CombinedAll:  {Area: 1100},
		resource.CombinedCity: {Area: 450},
	}
	entry := newMockEntry(totals)
	seedJS, err := BuildForecastSeed(ctx, &config.ForecastConfig{HorizonYears: 5, InitialPCI: 80}, entry.Store)
	if err != nil {
		t.Fatalf("BuildForecastSeed: %v", err)
	}
	var seed map[string]any
	if err := json.Unmarshal([]byte(seedJS), &seed); err != nil {
		t.Fatalf("unmarshal seed: %v", err)
	}
	if seed["maintained_area"] != 1250.0 || seed["city_maintained_area"] != 500.0 {
		t.Errorf("seed areas = %v / %v, want the per-resource sums 1250 / 500, not the footprint 1100 / 450",
			seed["maintained_area"], seed["city_maintained_area"])
	}
	for _, old := range []string{"total_area", "city_paved"} {
		if _, ok := seed[old]; ok {
			t.Errorf("seed still carries %q", old)
		}
	}
	meta, err := BuildMeta(ctx, entry, 0)
	if err != nil {
		t.Fatalf("BuildMeta: %v", err)
	}
	if meta.TotalPaved != 1100 {
		t.Errorf("meta total_paved = %v, want the footprint 1100", meta.TotalPaved)
	}
}
