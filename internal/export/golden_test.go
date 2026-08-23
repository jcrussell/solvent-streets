package export

import (
	"context"
	"database/sql"
	"encoding/json"
	"flag"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/google/go-cmp/cmp"

	"github.com/jcrussell/solvent-streets/internal/config"
	"github.com/jcrussell/solvent-streets/internal/db"
	"github.com/jcrussell/solvent-streets/internal/db/dbtest"
	"github.com/jcrussell/solvent-streets/internal/resource"
)

// update refreshes the golden files when set. Run intentionally after a
// contract change: `go test ./internal/export -run Golden -update`.
var update = flag.Bool("update", false, "update golden files in testdata/golden/")

// TestScenariosJSON_Golden pins the dashboard's data contract. scenarios.json
// is consumed by the static-site frontend at known JSON paths
// (data.bbox.<resource>.year1_cost, data.city.<resource>.scenarios[*]...).
// A silent shape change — renamed key, dropped field, type swap — would
// break the dashboard at the next deploy with no compile-time signal.
//
// The fixture is fully deterministic: hand-picked compute results, a
// frozen ForecastConfig, and no time.Now() in the BuildScenariosData code
// path. Floating-point output is stable across re-runs on the same Go
// minor version; a minor-version bump that nudges the last digit is a
// real signal that the underlying math changed and should be reviewed.
func TestScenariosJSON_Golden(t *testing.T) {
	entry := goldenFixtureEntry(t)
	fc := goldenForecastConfig()

	got, err := BuildScenariosData(context.Background(), entry, &fc)
	if err != nil {
		t.Fatalf("BuildScenariosData: %v", err)
	}
	gotBytes, err := json.MarshalIndent(got, "", "  ")
	if err != nil {
		t.Fatalf("marshal: %v", err)
	}
	gotBytes = append(gotBytes, '\n')

	goldenPath := filepath.Join("testdata", "golden", "scenarios.json")
	if *update {
		if err := os.MkdirAll(filepath.Dir(goldenPath), 0o755); err != nil {
			t.Fatalf("mkdir golden: %v", err)
		}
		if err := os.WriteFile(goldenPath, gotBytes, 0o644); err != nil {
			t.Fatalf("write golden: %v", err)
		}
		t.Logf("updated %s", goldenPath)
		return
	}

	wantBytes, err := os.ReadFile(goldenPath)
	if err != nil {
		t.Fatalf("read golden (run with -update to create): %v", err)
	}
	if diff := cmp.Diff(string(wantBytes), string(gotBytes)); diff != "" {
		t.Errorf("scenarios.json mismatch (-want +got):\n%s", diff)
	}
}

// TestForecastJSON_Golden pins the per-resource forecast.json contract,
// including the roads-only solvency metrics (insolvency_year,
// break_even_budget, current_budget, funding_gap) consumed by the dashboard's
// Financials headline and the cross-city leaderboard. forecast.json had no
// golden before the solvency build; this closes that coverage gap. The fixture
// is fully deterministic (same hand-picked compute results and frozen
// ForecastConfig as the scenarios golden), and goldenForecastConfig sets a
// nonzero CurrentBudget so the budget-dependent fields are exercised.
func TestForecastJSON_Golden(t *testing.T) {
	entry := goldenFixtureEntry(t)
	fc := goldenForecastConfig()

	got, err := BuildForecastsForCity(context.Background(), entry, &fc, ConvertCostTiers(&fc))
	if err != nil {
		t.Fatalf("BuildForecastsForCity: %v", err)
	}
	gotBytes, err := json.MarshalIndent(got, "", "  ")
	if err != nil {
		t.Fatalf("marshal: %v", err)
	}
	gotBytes = append(gotBytes, '\n')

	goldenPath := filepath.Join("testdata", "golden", "forecast.json")
	if *update {
		if err := os.MkdirAll(filepath.Dir(goldenPath), 0o755); err != nil {
			t.Fatalf("mkdir golden: %v", err)
		}
		if err := os.WriteFile(goldenPath, gotBytes, 0o644); err != nil {
			t.Fatalf("write golden: %v", err)
		}
		t.Logf("updated %s", goldenPath)
		return
	}

	wantBytes, err := os.ReadFile(goldenPath)
	if err != nil {
		t.Fatalf("read golden (run with -update to create): %v", err)
	}
	if diff := cmp.Diff(string(wantBytes), string(gotBytes)); diff != "" {
		t.Errorf("forecast.json mismatch (-want +got):\n%s", diff)
	}
}

// goldenFixtureEntry builds a deterministic CityEntry: hand-picked compute
// results per resource, both bbox and :city scopes, so BuildScenariosData
// exercises the dual-scope branch (out["city"] and out["bbox"] both set).
// Also used, with a boundary and a snapshot bolted on, to generate the JS
// specs' data fixtures — see jsFixtureEntry.
func goldenFixtureEntry(t *testing.T) CityEntry {
	t.Helper()
	results := map[resource.Type]db.ComputeResult{
		resource.TypeRoads:                              {ResourceType: resource.TypeRoads, TotalArea: 1_500_000, FeatureCount: 800},
		resource.TypeRoads.With(resource.ScopeCity):     {ResourceType: resource.TypeRoads.With(resource.ScopeCity), TotalArea: 900_000, FeatureCount: 480},
		resource.TypeParking:                            {ResourceType: resource.TypeParking, TotalArea: 200_000, FeatureCount: 120},
		resource.TypeParking.With(resource.ScopeCity):   {ResourceType: resource.TypeParking.With(resource.ScopeCity), TotalArea: 150_000, FeatureCount: 90},
		resource.TypeSidewalks:                          {ResourceType: resource.TypeSidewalks, TotalArea: 100_000, FeatureCount: 250},
		resource.TypeSidewalks.With(resource.ScopeCity): {ResourceType: resource.TypeSidewalks.With(resource.ScopeCity), TotalArea: 80_000, FeatureCount: 200},
	}
	// Per-class cohort stats per resource and scope. BuildScenariosData and
	// BuildForecastsForCity now build their lines from these multi-cohort seeds
	// (the same source as the interactive WASM line), so the golden must supply
	// them to exercise that path rather than the single-synthetic-cohort
	// fallback. Class areas are split so they roughly sum to the matching
	// compute TotalArea above; the road classes carry distinct decay rates so
	// the blended (multi-cohort) curve differs from a single-rate cohort.
	cohorts := map[resource.Type][]db.CohortStat{
		resource.TypeRoads: {
			{ResourceType: resource.TypeRoads, Classification: "primary", Area: 500_000, FeatureCount: 200},
			{ResourceType: resource.TypeRoads, Classification: "residential", Area: 1_000_000, FeatureCount: 600},
		},
		resource.TypeRoads.With(resource.ScopeCity): {
			{ResourceType: resource.TypeRoads.With(resource.ScopeCity), Classification: "primary", Area: 300_000, FeatureCount: 120},
			{ResourceType: resource.TypeRoads.With(resource.ScopeCity), Classification: "residential", Area: 600_000, FeatureCount: 360},
		},
		resource.TypeParking: {
			{ResourceType: resource.TypeParking, Classification: "parking", Area: 200_000, FeatureCount: 120},
		},
		resource.TypeParking.With(resource.ScopeCity): {
			{ResourceType: resource.TypeParking.With(resource.ScopeCity), Classification: "parking", Area: 150_000, FeatureCount: 90},
		},
		resource.TypeSidewalks: {
			{ResourceType: resource.TypeSidewalks, Classification: "sidewalks", Area: 100_000, FeatureCount: 250},
		},
		resource.TypeSidewalks.With(resource.ScopeCity): {
			{ResourceType: resource.TypeSidewalks.With(resource.ScopeCity), Classification: "sidewalks", Area: 80_000, FeatureCount: 200},
		},
	}
	store := &dbtest.MockStore{
		LatestComputeResultFunc: func(_ context.Context, key resource.Type) (*db.ComputeResult, error) {
			r, ok := results[key]
			if !ok {
				return nil, sql.ErrNoRows
			}
			return &r, nil
		},
		ListCohortStatsFunc: func(_ context.Context, key resource.Type) ([]db.CohortStat, error) {
			return cohorts[key], nil
		},
	}
	return CityEntry{
		Config: &config.Config{},
		City:   config.CityConfig{Name: "Golden City"},
		Store:  store,
		Slug:   "golden-city",
	}
}

// goldenForecastConfig returns the frozen forecast config used by the
// golden test. Keep these values stable; updates should run with -update
// and be reviewed in the diff.
//
// Also the basis for the JS specs' fixtures (js_harness_test.go), which
// relabel one tier and lengthen the horizon but otherwise follow this. A change
// here moves what internal/export/templates/__tests__ runs against.
func goldenForecastConfig() config.ForecastConfig {
	return config.ForecastConfig{
		Years:      10,
		InitialPCI: 85,
		DecayRate:  1.5,
		GrowthRate: 0.01,
		// Pinned explicitly (not left 0-to-default) so a change to
		// DefaultTreatmentCycleYears doesn't silently rewrite this golden. Gates
		// annual_need/break_even by 1/12; PCI columns are unchanged.
		TreatmentCycleYears: 12,
		// Nonzero so the roads solvency metrics (insolvency_year,
		// break_even_budget, current_budget, funding_gap) in forecast.json
		// serialize to real values — the forecast.json golden covers nothing
		// otherwise. BuildScenariosData ignores CurrentBudget, so scenarios.json
		// is unaffected.
		CurrentBudget: 20_000_000,
		CostTiers: []config.CostTierCfg{
			{MinPCI: 70, MaxPCI: 100, CostPerSqM: 2.0, Label: "preventive"},
			{MinPCI: 50, MaxPCI: 70, CostPerSqM: 12.0, Label: "rehab"},
			{MinPCI: 0, MaxPCI: 50, CostPerSqM: 60.0, Label: "reconstruct"},
		},
	}
}

// TestResolvedTOML_StripsConfigID guards against config_id leaking into
// the published static site. ResolvedTOML powers the Config tab visible
// to anyone who loads the rendered index.html, so an auto-computed
// host-path-derived ConfigID hash (or even a user-set value treated as
// internal plumbing) must not appear there.
func TestResolvedTOML_StripsConfigID(t *testing.T) {
	cfg := &config.Config{
		ConfigID: "secret-host-hash",
		Cities:   []config.CityConfig{{Name: "Test"}},
	}
	out := ResolvedTOML(cfg)
	if strings.Contains(out, "config_id") {
		t.Errorf("ResolvedTOML output contains config_id; must be stripped.\nOutput:\n%s", out)
	}
	if strings.Contains(out, "secret-host-hash") {
		t.Errorf("ResolvedTOML output leaked ConfigID value.\nOutput:\n%s", out)
	}
}

// TestResolvedTOML_StripsZeroCurrentBudget guards the Config tab against a
// fabricated `current_budget = 0.0` for uncalibrated cities. current_budget
// uses 0 as a "not provided" sentinel and BurntSushi emits zero floats
// (its isEmpty has no float case), so the encoded TOML must be stripped.
// A configured budget must still survive.
func TestResolvedTOML_StripsZeroCurrentBudget(t *testing.T) {
	uncalibrated := &config.Config{
		Cities: []config.CityConfig{{Name: "Nowhere", Forecast: &config.ForecastConfig{}}},
	}
	if out := ResolvedTOML(uncalibrated); strings.Contains(out, "current_budget") {
		t.Errorf("ResolvedTOML leaked a zero current_budget for an uncalibrated config:\n%s", out)
	}

	calibrated := &config.Config{
		Forecast: config.ForecastConfig{CurrentBudget: 4_850_000},
		Cities:   []config.CityConfig{{Name: "Somewhere"}},
	}
	out := ResolvedTOML(calibrated)
	if !strings.Contains(out, "current_budget") {
		t.Errorf("ResolvedTOML dropped a configured current_budget:\n%s", out)
	}
}

// TestResolvedTOML_ShowsPerCityMinHexArea pins l51o's Config-tab half: the
// per-city min_hex_area must be published as its *resolved* value. CityConfig
// emits the field unconditionally, so an unresolved city would print
// `min_hex_area = 0.0` right below a top level showing the resolved default —
// a misleading published artifact.
func TestResolvedTOML_ShowsPerCityMinHexArea(t *testing.T) {
	cfg := &config.Config{
		Display: config.DisplayConfig{MinHexArea: 40},
		Cities: []config.CityConfig{
			{Name: "Inherits"},
			{Name: "Fine City", HexEdgeM: 25, MinHexArea: 5},
		},
	}
	out := ResolvedTOML(cfg)
	if strings.Contains(out, "min_hex_area = 0.0") {
		t.Errorf("Config tab published an unresolved min_hex_area = 0.0:\n%s", out)
	}
	// The inheriting city resolves to the top-level 40 (so 40 appears twice:
	// top-level [display] plus that city), and the override keeps its 5.
	if got := strings.Count(out, "min_hex_area = 40.0"); got != 2 {
		t.Errorf("expected top-level and inherited min_hex_area = 40.0 (2 occurrences), got %d:\n%s", got, out)
	}
	if !strings.Contains(out, "min_hex_area = 5.0") {
		t.Errorf("per-city min_hex_area override (5) not shown in Config tab:\n%s", out)
	}
}

// TestResolvedTOML_MinHexAreaFallsBackToDefault: a config that sets no
// threshold anywhere publishes the applied default for both the top level and
// every city, rather than a zero that reads as "slivers are never dropped".
func TestResolvedTOML_MinHexAreaFallsBackToDefault(t *testing.T) {
	cfg := &config.Config{Cities: []config.CityConfig{{Name: "Nowhere"}}}
	out := ResolvedTOML(cfg)
	want := fmt.Sprintf("min_hex_area = %.1f", config.DefaultMinHexArea)
	if got := strings.Count(out, want); got != 2 {
		t.Errorf("expected %q twice (top level + city), got %d:\n%s", want, got, out)
	}
}

// TestResolvedTOML_MaterializesExportDefaults pins that the [export] block
// reports the values the pipeline actually uses. Both fields resolve lazily
// through Config accessors and neither has omitempty, so an unset block used to
// publish sentinel zeros -- contradicting docs/configuration.md (5 and 10) while
// every other section materialized its defaults.
func TestResolvedTOML_MaterializesExportDefaults(t *testing.T) {
	cfg := &config.Config{Cities: []config.CityConfig{{Name: "Nowhere"}}}
	out := ResolvedTOML(cfg)
	for _, want := range []string{
		fmt.Sprintf("coordinate_decimals = %d", config.DefaultCoordinateDecimals),
		fmt.Sprintf("boundary_simplify_m = %.1f", config.DefaultBoundarySimplifyM),
	} {
		if !strings.Contains(out, want) {
			t.Errorf("expected %q in resolved TOML:\n%s", want, out)
		}
	}
}

// TestResolvedTOML_PreservesBoundarySimplifyOptOut guards the asymmetry in
// Config.BoundarySimplifyM: it keys on zero-vs-nonzero, not positive, because a
// NEGATIVE tolerance is the documented byte-exact opt-out. Materializing the
// default with a `<= 0` guard would silently convert that opt-out into 10 m.
func TestResolvedTOML_PreservesBoundarySimplifyOptOut(t *testing.T) {
	cfg := &config.Config{
		Export: config.ExportConfig{BoundarySimplifyM: -1, CoordinateDecimals: 7},
		Cities: []config.CityConfig{{Name: "Nowhere"}},
	}
	out := ResolvedTOML(cfg)
	if !strings.Contains(out, "boundary_simplify_m = -1.0") {
		t.Errorf("the negative opt-out must survive resolution:\n%s", out)
	}
	if !strings.Contains(out, "coordinate_decimals = 7") {
		t.Errorf("an explicit coordinate_decimals must survive resolution:\n%s", out)
	}
}

// TestResolvedTOML_ShowsPerCityResolvedCalibration pins solvent-streets-xl1t:
// the Config tab must reflect each city's effective, resolved calibration
// (the per-metro hex_edge/forecast an [[include]] flattens onto the city),
// not a parent-only view that hides those overrides.
func TestResolvedTOML_ShowsPerCityResolvedCalibration(t *testing.T) {
	cfg := &config.Config{
		Grid:     config.GridConfig{HexEdgeM: 100},
		Forecast: config.ForecastConfig{DecayRate: 0.088},
		Cities: []config.CityConfig{
			{Name: "Default City"},
			{Name: "Fine City", HexEdgeM: 250},
		},
	}
	out := ResolvedTOML(cfg)
	if !strings.Contains(out, "250") {
		t.Errorf("per-city hex_edge_m override (250) not shown in Config tab:\n%s", out)
	}
	// Every city now carries its resolved forecast, so decay_rate appears
	// per-city (2 cities) on top of the top-level occurrence.
	if got := strings.Count(out, "decay_rate"); got < 2 {
		t.Errorf("expected per-city resolved forecast (decay_rate ≥2 occurrences), got %d:\n%s", got, out)
	}
}
