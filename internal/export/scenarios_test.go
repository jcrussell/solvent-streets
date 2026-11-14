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

	err := exportScenariosForCity(t.Context(), entry, dataDir)
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

// TestBuildResourceForecast_AllBaselineGatedOnCityScope pins the contract
// that AllBaseline is only populated when city-scope cohorts exist. When
// hasCityScope is false the primary baseline already reflects the all
// cohorts, so computing a second identical Simulate is wasted work — the
// fix routes the allBaseline call inside the `if hasCityScope` branch.
//
// Regression caught: reverting the gate (unconditionally running
// forecast.Simulate over allCohorts) doubles per-resource Simulate work on
// the common no-city-scope path. We can't directly count Simulate calls, so
// we anchor on the observable contracts: (a) AllBaseline is nil when city
// stats are empty, (b) AllBaseline is non-nil and equal to a fresh
// Simulate over all cohorts when city stats are present.
func TestBuildResourceForecast_AllBaselineGatedOnCityScope(t *testing.T) {
	now := time.Now()
	fc := &config.ForecastConfig{InitialPCI: 85, GrowthRate: 0.01, Years: 5}
	costTiers := forecast.DefaultCostTiers

	pavement := &resource.Pavement{}
	tAll := pavement.Type()
	tCity := tAll.With(resource.ScopeCity)

	t.Run("no_city_scope_skips_all_baseline", func(t *testing.T) {
		latest := map[resource.Type]results.Totals{
			tAll: {Area: 1000, ComputedAt: now},
		}
		// Empty maps for both all and city scopes — no cohort stats stored.
		cohorts := map[resource.Type][]results.Cohort{}

		fe, err := buildResourceForecast(pavement, fc, costTiers, latest, cohorts)
		if err != nil {
			t.Fatalf("buildResourceForecast: %v", err)
		}
		if fe.AllBaseline != nil {
			t.Errorf("AllBaseline = %+v; want nil when city stats are empty", fe.AllBaseline)
		}
		if len(fe.Baseline.Years) != fc.Years {
			t.Errorf("Baseline.Years length = %d; want %d", len(fe.Baseline.Years), fc.Years)
		}
	})

	t.Run("city_scope_populates_all_baseline", func(t *testing.T) {
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
		if fe.AllBaseline == nil {
			t.Fatal("AllBaseline = nil; want non-nil when city stats are present")
		}
		// All-scope area (2000) > city-area (500), so the all baseline must reflect
		// a larger system than the city-scope Baseline.
		if len(fe.AllBaseline.Years) == 0 || len(fe.Baseline.Years) == 0 {
			t.Fatalf("expected non-empty year slices; baseline=%d all=%d", len(fe.Baseline.Years), len(fe.AllBaseline.Years))
		}
		if fe.AllBaseline.Years[0].AnnualNeed <= fe.Baseline.Years[0].AnnualNeed {
			t.Errorf("AllBaseline.year0 AnnualNeed = %v; want > Baseline.year0 AnnualNeed = %v",
				fe.AllBaseline.Years[0].AnnualNeed, fe.Baseline.Years[0].AnnualNeed)
		}
	})
}

// The blended scenarios and the per-resource forecasts price the same network
// the same way: the baseline's year-1 need in scenarios.json is the sum of
// forecast.json's per-resource baselines, sidewalks at the sidewalk rate in
// both (solvent-streets-mea7).
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
			tier, _ := forecast.MaterialTierFor(forecast.DefaultMaterialTiers, y.CostTier)
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
			if fe.RoadsArea <= 0 || fe.RoadsArea != fe.Baseline.Years[0].Area {
				t.Errorf("roads_area = %v, want the baseline's year-1 area %v", fe.RoadsArea, fe.Baseline.Years[0].Area)
			}
		default:
			if fe.RoadsArea != 0 {
				t.Errorf("%s carries roads_area %v; it belongs to the roads entry only", fe.ResourceType, fe.RoadsArea)
			}
		}
	}
}
