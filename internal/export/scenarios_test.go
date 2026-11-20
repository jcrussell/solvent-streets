package export

import (
	"context"
	"errors"
	"math"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/jcrussell/solvent-streets/internal/config"
	"github.com/jcrussell/solvent-streets/internal/db"
	"github.com/jcrussell/solvent-streets/internal/db/dbtest"
	"github.com/jcrussell/solvent-streets/internal/forecast"
	"github.com/jcrussell/solvent-streets/internal/resource"
	"github.com/jcrussell/solvent-streets/internal/results"
)

// TestExportScenariosForCity_PropagatesDBError pins the deployment-correctness
// contract: when reading the city totals fails, exportScenariosForCity
// surfaces the error and writes nothing
// — so a transient DB failure aborts the static export rather than emitting
// partial output that would mislead the downstream dashboard.
//
// Mirrors internal/server/handlers_test.go TestBuildForecasts_DBErrorEvicts for
// the export-command path. Regression caught: reverting buildResourceForecast
// to silent-swallow the underlying error (the pre-bkz behavior) breaks
// errors.Is and lets exportScenariosForCity return nil with no files.
func TestExportScenariosForCity_PropagatesDBError(t *testing.T) {
	sentinel := errors.New("db unavailable")
	store := &dbtest.MockStore{
		ListCityMetricsFunc: func(context.Context, []string) ([]db.CityMetric, error) {
			return nil, sentinel
		},
	}
	entry := CityEntry{
		Config: &config.Config{Cities: []config.CityConfig{{Name: "Test City"}}},
		City:   config.CityConfig{Name: "Test City"},
		Store:  store,
		Slug:   "test-city",
	}
	dataDir := t.TempDir()

	err := exportScenariosForCity(t.Context(), nil, entry, dataDir)
	if err == nil {
		t.Fatal("expected error from exportScenariosForCity, got nil")
	}
	if !errors.Is(err, sentinel) {
		t.Errorf("errors.Is(err, sentinel) = false; err = %v", err)
	}

	for _, name := range []string{"forecast.json", "hex-cost-summary.json", "scenarios.json"} {
		if _, statErr := os.Stat(filepath.Join(dataDir, name)); !os.IsNotExist(statErr) {
			t.Errorf("expected %s not to exist on error path; stat = %v", name, statErr)
		}
	}
}

// TestBuildResourceForecast_CityBaselineGatedOnCityScope pins the contract
// that CityBaseline is only populated when city-scope cohorts exist, and that
// Baseline is the all-jurisdiction run either way. Without a city scope the
// reference run already is the all-jurisdiction one, so a second Simulate
// would be wasted work: (a) CityBaseline is nil when city stats are empty,
// (b) with city stats, Baseline covers the larger all-jurisdiction system.
func TestBuildResourceForecast_CityBaselineGatedOnCityScope(t *testing.T) {
	now := time.Now()
	fc := &config.ForecastConfig{InitialPCI: 85, GrowthRate: 0.01, HorizonYears: 5}
	costTiers := forecast.DefaultCostTiers

	pavement := &resource.Pavement{}
	tAll := pavement.Type()
	tCity := tAll.With(resource.ScopeCity)

	t.Run("no_city_scope_skips_city_baseline", func(t *testing.T) {
		latest := map[resource.Type]results.Totals{
			tAll: {Area: 1000, ComputedAt: now},
		}
		// Empty maps for both all and city scopes — no cohort stats stored.
		cohorts := map[resource.Type][]results.Cohort{}

		fe, err := buildResourceForecast(pavement, fc, costTiers, latest, cohorts)
		if err != nil {
			t.Fatalf("buildResourceForecast: %v", err)
		}
		if fe.CityBaseline != nil {
			t.Errorf("CityBaseline = %+v; want nil when city stats are empty", fe.CityBaseline)
		}
		if len(fe.Baseline.Years) != fc.HorizonYears {
			t.Errorf("Baseline.Years length = %d; want %d", len(fe.Baseline.Years), fc.HorizonYears)
		}
	})

	t.Run("city_scope_populates_city_baseline", func(t *testing.T) {
		latest := map[resource.Type]results.Totals{
			tAll: {Area: 2000, ComputedAt: now},
		}
		cohorts := map[resource.Type][]results.Cohort{
			tCity: {{Class: "residential", Area: 500}},
		}

		fe, err := buildResourceForecast(pavement, fc, costTiers, latest, cohorts)
		if err != nil {
			t.Fatalf("buildResourceForecast: %v", err)
		}
		if fe.CityBaseline == nil {
			t.Fatal("CityBaseline = nil; want non-nil when city stats are present")
		}
		// All-scope area (2000) > city-area (500), so Baseline must reflect a
		// larger system than CityBaseline.
		if len(fe.CityBaseline.Years) == 0 || len(fe.Baseline.Years) == 0 {
			t.Fatalf("expected non-empty year slices; baseline=%d city=%d", len(fe.Baseline.Years), len(fe.CityBaseline.Years))
		}
		if fe.Baseline.Years[0].AnnualNeed <= fe.CityBaseline.Years[0].AnnualNeed {
			t.Errorf("Baseline.year0 AnnualNeed = %v; want > CityBaseline.year0 AnnualNeed = %v",
				fe.Baseline.Years[0].AnnualNeed, fe.CityBaseline.Years[0].AnnualNeed)
		}
	})
}

// The blended scenarios and the per-resource forecasts price the same network
// the same way: the baseline's year-1 need in scenarios.json is the sum of
// forecast.json's per-resource baselines, sidewalks at the sidewalk rate in
// both.
func TestScenarios_Year1NeedMatchesPerResourceForecasts(t *testing.T) {
	var rows []db.CityMetric
	rows = append(rows, results.TotalRows(resource.TypeRoads, results.Totals{Area: 9000})...)
	rows = append(rows, results.CohortRows(resource.TypeRoads, []results.Cohort{{Class: "primary", Area: 4000}, {Class: "residential", Area: 5000}})...)
	rows = append(rows, results.TotalRows(resource.TypeSidewalks, results.Totals{Area: 3000})...)
	rows = append(rows, results.CohortRows(resource.TypeSidewalks, []results.Cohort{{Class: "sidewalks", Area: 3000}})...)
	cfg := &config.Config{Cities: []config.CityConfig{{Name: "A"}}}
	entry := CityEntry{Config: cfg, City: cfg.Cities[0], Store: &dbtest.MockStore{CityMetrics: rows}, Slug: "a"}
	fc := cfg.ResolvedForecast(&entry.City)
	ctx := context.Background()

	forecasts, err := BuildForecastsForCity(ctx, entry, &fc, ConvertCostTiers(&fc))
	if err != nil {
		t.Fatal(err)
	}
	var perResource float64
	for _, fe := range forecasts {
		perResource += fe.Baseline.Years[0].AnnualNeed
	}

	data, err := BuildScenariosData(ctx, entry, &fc)
	if err != nil {
		t.Fatal(err)
	}
	blended := data["all"].([]forecast.ScenarioResult)[0].Years[0].AnnualNeed

	if perResource <= 0 || math.Abs(blended-perResource) > 1e-6*perResource {
		t.Errorf("year-1 need: scenarios.json %.2f, sum of forecast.json baselines %.2f", blended, perResource)
	}
}

// A city that has a city-maintained view but maintains none of one resource
// (every road tagged county, state or federal) gets the same answer from every
// file: that resource is absent from the city set of scenarios.json and the
// seed, and its forecast.json entry is an empty city forecast beside the
// all-jurisdiction one, not the all-jurisdiction roads under the city's name.
func TestCityScope_AResourceTheCityMaintainsNoneOfIsEmptyEverywhere(t *testing.T) {
	var rows []db.CityMetric
	rows = append(rows, results.TotalRows(resource.TypeRoads, results.Totals{Area: 9000})...)
	rows = append(rows, results.CohortRows(resource.TypeRoads, []results.Cohort{{Class: "primary", Area: 9000}})...)
	parkingCity := resource.TypeParking.With(resource.ScopeCity)
	rows = append(rows, results.TotalRows(resource.TypeParking, results.Totals{Area: 4000})...)
	rows = append(rows, results.CohortRows(resource.TypeParking, []results.Cohort{{Class: "parking", Area: 4000}})...)
	rows = append(rows, results.TotalRows(parkingCity, results.Totals{Area: 3000})...)
	rows = append(rows, results.CohortRows(parkingCity, []results.Cohort{{Class: "parking", Area: 3000}})...)
	cfg := &config.Config{Cities: []config.CityConfig{{Name: "A"}}}
	entry := CityEntry{Config: cfg, City: cfg.Cities[0], Store: &dbtest.MockStore{CityMetrics: rows}, Slug: "a"}
	fc := cfg.ResolvedForecast(&entry.City)
	fc.CurrentBudget = 1e6
	ctx := context.Background()

	forecasts, err := BuildForecastsForCity(ctx, entry, &fc, ConvertCostTiers(&fc))
	if err != nil {
		t.Fatal(err)
	}
	var cityNeed float64
	for _, fe := range forecasts {
		if fe.CityBaseline == nil {
			t.Fatalf("%s: no city baseline in a city that has city scope", fe.ResourceType)
		}
		cityNeed += fe.CityBaseline.Years[0].AnnualNeed
		if fe.ResourceType != string(resource.TypeRoads) {
			continue
		}
		if y := fe.CityBaseline.Years[0]; y.Area != 0 || y.AnnualNeed != 0 {
			t.Errorf("roads city forecast: area %.0f, need %.0f; want an empty forecast", y.Area, y.AnnualNeed)
		}
		if fe.Baseline.Years[0].Area == 0 {
			t.Error("roads all-jurisdiction baseline is empty")
		}
		if fe.BreakEvenBudget != 0 || fe.RoadsArea != 0 || fe.InsolvencyYear != nil || fe.FundingGap != nil {
			t.Errorf("roads solvency stated with no city-maintained roads: %+v", fe)
		}
	}

	data, err := BuildScenariosData(ctx, entry, &fc)
	if err != nil {
		t.Fatal(err)
	}
	blended := data["city"].([]forecast.ScenarioResult)[0].Years[0].AnnualNeed
	if cityNeed <= 0 || math.Abs(blended-cityNeed) > 1e-6*cityNeed {
		t.Errorf("city year-1 need: scenarios.json %.2f, sum of forecast.json baselines %.2f", blended, cityNeed)
	}

	cohorts, err := collectCohortSeeds(ctx, entry.Store, &fc)
	if err != nil {
		t.Fatal(err)
	}
	for _, c := range cohorts.City {
		if c.ResourceType == string(resource.TypeRoads) {
			t.Errorf("seed city cohorts include roads: %+v", c)
		}
	}
}

// scenarios.json carries each year's material demand, with the concrete
// sidewalk area netted out by the scope's asphalt share: sidewalks are a
// quarter of this network, so the mix is three quarters of what the whole
// area would take.
func TestScenarios_YearsCarryMaterialsNetOfConcrete(t *testing.T) {
	var rows []db.CityMetric
	rows = append(rows, results.TotalRows(resource.TypeRoads, results.Totals{Area: 9000})...)
	rows = append(rows, results.CohortRows(resource.TypeRoads, []results.Cohort{{Class: "residential", Area: 9000}})...)
	rows = append(rows, results.TotalRows(resource.TypeSidewalks, results.Totals{Area: 3000})...)
	rows = append(rows, results.CohortRows(resource.TypeSidewalks, []results.Cohort{{Class: "sidewalks", Area: 3000}})...)
	cfg := &config.Config{Cities: []config.CityConfig{{Name: "A"}}}
	entry := CityEntry{Config: cfg, City: cfg.Cities[0], Store: &dbtest.MockStore{CityMetrics: rows}, Slug: "a"}
	fc := cfg.ResolvedForecast(&entry.City)

	data, err := BuildScenariosData(context.Background(), entry, &fc)
	if err != nil {
		t.Fatal(err)
	}
	cycle := forecast.ResolveCycleYears(fc.TreatmentCycleYears)
	for _, sr := range data["all"].([]forecast.ScenarioResult) {
		for _, y := range sr.Years {
			if y.Materials == nil {
				t.Fatalf("%s year %d carries no materials", sr.Scenario.Name, y.Year)
			}
			tier, _ := forecast.MaterialTierForPCI(forecast.DefaultMaterialTiers, forecast.ShownPCI(y.PCI))
			want := forecast.MaterialsForArea(y.Area*0.75/cycle, tier)
			if math.Abs(y.Materials.MixTonnes-want.MixTonnes) > 1e-9*want.MixTonnes {
				t.Fatalf("%s year %d: mix %.4f t, want %.4f t (3/4 of the area is asphalt)",
					sr.Scenario.Name, y.Year, y.Materials.MixTonnes, want.MixTonnes)
			}
		}
	}
}

// forecast.json's roads entry states the area its solvency figures are
// measured on, so the live server's Compare tab (which reads forecast.json,
// with no compare.json rollup) shows the same "City-Maintained Roads" row a
// static export does.
func TestBuildForecastsForCity_RoadsEntryCarriesItsArea(t *testing.T) {
	var rows []db.CityMetric
	rows = append(rows, results.TotalRows(resource.TypeRoads, results.Totals{Area: 9000})...)
	rows = append(rows, results.CohortRows(resource.TypeRoads, []results.Cohort{{Class: "residential", Area: 9000}})...)
	rows = append(rows, results.TotalRows(resource.TypeParking, results.Totals{Area: 500})...)
	cfg := &config.Config{Cities: []config.CityConfig{{Name: "A"}}}
	entry := CityEntry{Config: cfg, City: cfg.Cities[0], Store: &dbtest.MockStore{CityMetrics: rows}, Slug: "a"}
	fc := cfg.ResolvedForecast(&entry.City)

	forecasts, err := BuildForecastsForCity(context.Background(), entry, &fc, ConvertCostTiers(&fc))
	if err != nil {
		t.Fatal(err)
	}
	for _, fe := range forecasts {
		switch fe.ResourceType {
		case "roads":
			if fe.RoadsArea <= 0 || fe.RoadsArea != fe.Primary().Years[0].Area {
				t.Errorf("roads_area = %v, want the primary baseline's year-1 area %v", fe.RoadsArea, fe.Primary().Years[0].Area)
			}
		default:
			if fe.RoadsArea != 0 {
				t.Errorf("%s carries roads_area %v; it belongs to the roads entry only", fe.ResourceType, fe.RoadsArea)
			}
		}
	}
}
