package export

import (
	"context"
	"database/sql"
	"encoding/json"
	"flag"
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

	got := BuildScenariosData(context.Background(), entry, &fc)
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
