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

// goldenFixtureEntry builds a deterministic CityEntry: hand-picked compute
// results per resource, both bbox and :city scopes, so BuildScenariosData
// exercises the dual-scope branch (out["city"] and out["bbox"] both set).
func goldenFixtureEntry(t *testing.T) CityEntry {
	t.Helper()
	results := map[resource.Type]db.ComputeResult{
		resource.TypeRoads:                              {ResourceType: resource.TypeRoads, TotalAreaSqM: 1_500_000, FeatureCount: 800},
		resource.TypeRoads.With(resource.ScopeCity):     {ResourceType: resource.TypeRoads.With(resource.ScopeCity), TotalAreaSqM: 900_000, FeatureCount: 480},
		resource.TypeParking:                            {ResourceType: resource.TypeParking, TotalAreaSqM: 200_000, FeatureCount: 120},
		resource.TypeParking.With(resource.ScopeCity):   {ResourceType: resource.TypeParking.With(resource.ScopeCity), TotalAreaSqM: 150_000, FeatureCount: 90},
		resource.TypeSidewalks:                          {ResourceType: resource.TypeSidewalks, TotalAreaSqM: 100_000, FeatureCount: 250},
		resource.TypeSidewalks.With(resource.ScopeCity): {ResourceType: resource.TypeSidewalks.With(resource.ScopeCity), TotalAreaSqM: 80_000, FeatureCount: 200},
	}
	store := &dbtest.MockStore{
		LatestComputeResultFunc: func(_ context.Context, key resource.Type) (*db.ComputeResult, error) {
			r, ok := results[key]
			if !ok {
				return nil, sql.ErrNoRows
			}
			return &r, nil
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
