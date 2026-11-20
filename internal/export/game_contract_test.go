package export

import (
	"context"
	"encoding/json"
	"testing"

	"github.com/jcrussell/solvent-streets/internal/config"
	"github.com/jcrussell/solvent-streets/internal/db/dbtest"
	"github.com/jcrussell/solvent-streets/internal/export/exporttest"
	"github.com/jcrussell/solvent-streets/internal/game"
	"github.com/jcrussell/solvent-streets/internal/geo"
	"github.com/jcrussell/solvent-streets/internal/resource"
	"github.com/jcrussell/solvent-streets/internal/results"
)

// The /play board is built in the browser from three exported files:
// hexgrid.geojson (the board), forecast_seed.json (the forecast inputs) and
// forecast.json (the budget). These tests pin that contract from the Go side:
// the keys game.js reads exist with the types it reads, and the game.Config it
// assembles from them starts a game whose pregame projection agrees with the
// Financials insolvency year.

// boardEntry is a city with a board (per-hex city roads and decay rates) and
// roads cohorts in both scopes, so the seed and forecast.json describe the
// same network the board does.
func boardEntry(t *testing.T) CityEntry {
	t.Helper()
	entry := roadEntry(t, &config.Config{Grid: config.GridConfig{HexEdgeM: 200}}, "Board City", "board-city", exportBoundaryA)
	store := entry.Store.(*dbtest.MockStore)
	for rt, cohorts := range map[resource.Type][]results.Cohort{
		resource.TypeRoads:                          {{Class: "primary", Area: 400, Features: 1}, {Class: "residential", Area: 600, Features: 1}},
		resource.TypeRoads.With(resource.ScopeCity): {{Class: "primary", Area: 200, Features: 1}, {Class: "residential", Area: 400, Features: 1}},
	} {
		store.CityMetrics = append(store.CityMetrics, results.CohortRows(rt, cohorts)...)
	}
	return entry
}

// playFiles is one city's three board files, marshalled as the export writes
// them.
type playFiles struct {
	hexgrid, seed, forecast []byte
}

func exportPlayFiles(t *testing.T, entry CityEntry, fc config.ForecastConfig) playFiles {
	t.Helper()
	ctx := context.Background()
	_, lon, lat, err := entry.BBoxAndCenter(ctx)
	if err != nil {
		t.Fatalf("BBoxAndCenter: %v", err)
	}
	grid, err := BuildHexGeoJSON(ctx, entry, geo.NewUTMProjector(lon, lat))
	if err != nil || grid == nil {
		t.Fatalf("BuildHexGeoJSON: %v (nil: %v)", err, grid == nil)
	}
	seed, err := BuildForecastSeed(ctx, &fc, entry.Store)
	if err != nil {
		t.Fatalf("BuildForecastSeed: %v", err)
	}
	forecasts, err := BuildForecastsForCity(ctx, entry, &fc, ConvertCostTiers(&fc))
	if err != nil {
		t.Fatalf("BuildForecastsForCity: %v", err)
	}
	var out playFiles
	out.seed = []byte(seed)
	if out.hexgrid, err = json.Marshal(grid); err != nil {
		t.Fatal(err)
	}
	if out.forecast, err = json.Marshal(forecasts); err != nil {
		t.Fatal(err)
	}
	return out
}

// playConfig assembles the gameInit config from the exported files the way
// game.js boot() does: the board is the hexes carrying k, at their city-scope
// roads area; the forecast inputs come from the seed, roads cohorts only
// (city scope first); the budget is the roads current_budget, else the roads
// break-even budget.
func playConfig(t *testing.T, files playFiles) game.Config {
	t.Helper()
	type scope map[string]struct {
		Area float64 `json:"area"`
	}
	var grid struct {
		Features []struct {
			Properties struct {
				ID       string  `json:"id"`
				K        float64 `json:"k"`
				CitySame int     `json:"city_same"`
				All      scope   `json:"all"`
				City     scope   `json:"city"`
			} `json:"properties"`
		} `json:"features"`
	}
	var seed struct {
		game.Config
		Cohorts     []cohortSeedJSON `json:"cohorts"`
		CityCohorts []cohortSeedJSON `json:"city_cohorts"`
	}
	var forecasts []struct {
		ResourceType    string  `json:"resource_type"`
		CurrentBudget   float64 `json:"current_budget"`
		BreakEvenBudget float64 `json:"break_even_budget"`
	}
	for name, err := range map[string]error{
		"hexgrid":  json.Unmarshal(files.hexgrid, &grid),
		"seed":     json.Unmarshal(files.seed, &seed),
		"forecast": json.Unmarshal(files.forecast, &forecasts),
	} {
		if err != nil {
			t.Fatalf("decode %s: %v", name, err)
		}
	}

	cfg := seed.Config
	cfg.Hexes = nil
	for _, f := range grid.Features {
		p := f.Properties
		city := p.City
		if p.CitySame == 1 {
			city = p.All
		}
		roads, ok := city["roads"]
		if p.K <= 0 || !ok {
			continue
		}
		cfg.Hexes = append(cfg.Hexes, game.HexConfig{ID: p.ID, RoadsArea: roads.Area, DecayRate: p.K})
	}
	cfg.Cohorts = roadCohorts(seed.CityCohorts)
	if len(cfg.Cohorts) == 0 {
		cfg.Cohorts = roadCohorts(seed.Cohorts)
	}
	for _, f := range forecasts {
		if f.ResourceType != string(resource.TypeRoads) {
			continue
		}
		cfg.AnnualBudget = f.CurrentBudget
		if cfg.AnnualBudget <= 0 {
			cfg.AnnualBudget = f.BreakEvenBudget
		}
	}
	return cfg
}

type cohortSeedJSON struct {
	ResourceType string `json:"resource_type"`
	game.CohortConfig
}

func roadCohorts(seeds []cohortSeedJSON) []game.CohortConfig {
	var out []game.CohortConfig
	for _, c := range seeds {
		if c.ResourceType == string(resource.TypeRoads) {
			out = append(out, c.CohortConfig)
		}
	}
	return out
}

// TestForecastSeedJSON_PlayContract pins the forecast_seed.json keys game.js
// reads into the gameInit config.
func TestForecastSeedJSON_PlayContract(t *testing.T) {
	files := exportPlayFiles(t, boardEntry(t), exporttest.ForecastConfig())
	checkBindings(t, files.seed, []binding{
		{".initial_pci", []string{"number"}},
		{".horizon_years", []string{"number"}},
		{".treatment_cycle_years", []string{"number"}},
		{".growth_rate", []string{"number"}},
		{".cost_overhead", []string{"number"}},
		{".cost_tiers[]", []string{"object"}},
		{".cost_tiers[].min_pci", []string{"number"}},
		{".cost_tiers[].max_pci", []string{"number"}},
		{".cost_tiers[].cost_per_sqm", []string{"number"}},
		{".cost_tiers[].label", []string{"string"}},
		{".cohorts[].resource_type", []string{"string"}},
		{".cohorts[].classification", []string{"string"}},
		{".cohorts[].area", []string{"number"}},
		{".cohorts[].decay_rate", []string{"number"}},
		{".city_cohorts[].resource_type", []string{"string", "absent"}},
		{".city_cohorts[].classification", []string{"string", "absent"}},
		{".city_cohorts[].area", []string{"number", "absent"}},
		{".city_cohorts[].decay_rate", []string{"number", "absent"}},
	})
}

// TestHexGridJSON_PlayContract pins the hexgrid.geojson keys the /play board
// reads: the format version, each feature's id and geometry, the board hex's
// k, and the city-scope roads area through city_same or a city object.
func TestHexGridJSON_PlayContract(t *testing.T) {
	files := exportPlayFiles(t, boardEntry(t), exporttest.ForecastConfig())
	checkBindings(t, files.hexgrid, []binding{
		{".v", []string{"number"}},
		{".features[].geometry.type", []string{"string"}},
		{".features[].geometry.coordinates", []string{"array"}},
		{".features[].properties.id", []string{"string"}},
		{".features[].properties.k", []string{"number", "absent"}},
		{".features[].properties.city_same", []string{"number", "absent"}},
		{".features[].properties.all.roads.area", []string{"number", "absent"}},
		{".features[].properties.city.roads.area", []string{"number", "absent"}},
	})
	if cfg := playConfig(t, files); len(cfg.Hexes) == 0 {
		t.Fatal("the fixture board has no hex carrying k")
	}
}

// TestPlayConfig_FromExportStartsAGame: the gameInit config game.js builds
// from an export starts a game, and its pregame projection at the roads
// budget names the year forecast.json's insolvency_year does — bare and at a
// loaded cost overhead, at budgets that run out and one that does not.
func TestPlayConfig_FromExportStartsAGame(t *testing.T) {
	entry := boardEntry(t)
	insolvent := 0
	for _, overhead := range []float64{1, 1.5} {
		for _, budgetShare := range []float64{0.05, 0.2, 0.5, 2} {
			fc := exporttest.ForecastConfig()
			fc.CostOverhead = overhead
			// Long enough for an underfunded network to defer a full
			// treatment cycle of need, which the fixture's 10 years is not.
			fc.HorizonYears = 30
			// Size the budget off the network's own break-even, so the
			// shares are meaningful whatever the fixture's area.
			fc.CurrentBudget = 0
			breakEven := playConfig(t, exportPlayFiles(t, entry, fc)).AnnualBudget
			if breakEven <= 0 {
				t.Fatalf("overhead %g: no break-even budget", overhead)
			}
			fc.CurrentBudget = budgetShare * breakEven

			files := exportPlayFiles(t, entry, fc)
			cfg := playConfig(t, files)
			if _, err := game.New(cfg); err != nil {
				t.Fatalf("overhead %g, budget %.0f: game.New refused the exported config: %v", overhead, fc.CurrentBudget, err)
			}
			if cfg.CostOverhead != overhead {
				t.Errorf("overhead %g: the seed carried cost_overhead %g", overhead, cfg.CostOverhead)
			}

			var forecasts []struct {
				ResourceType   string `json:"resource_type"`
				InsolvencyYear *int   `json:"insolvency_year"`
			}
			if err := json.Unmarshal(files.forecast, &forecasts); err != nil {
				t.Fatal(err)
			}
			var want *int
			for _, f := range forecasts {
				if f.ResourceType == string(resource.TypeRoads) {
					want = f.InsolvencyYear
				}
			}
			year, ok := game.ProjectInsolvency(cfg)
			switch {
			case want == nil && ok:
				t.Errorf("overhead %g, budget %.0f: Play projects insolvency in year %d; Financials says solvent", overhead, fc.CurrentBudget, year)
			case want != nil && (!ok || year != *want):
				t.Errorf("overhead %g, budget %.0f: Play projects (%d, %v); Financials says year %d", overhead, fc.CurrentBudget, year, ok, *want)
			case want != nil:
				insolvent++
			}
		}
	}
	if insolvent == 0 {
		t.Fatal("no budget ran out, so the projection was never compared against an insolvency year")
	}
}
