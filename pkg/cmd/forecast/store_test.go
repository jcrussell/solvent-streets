package forecast

import (
	"context"
	"database/sql"
	"encoding/json"
	"math"
	"path/filepath"
	"strings"
	"testing"

	"github.com/jcrussell/solvent-streets/internal/config"
	"github.com/jcrussell/solvent-streets/internal/db"
	"github.com/jcrussell/solvent-streets/internal/resource"
	"github.com/jcrussell/solvent-streets/internal/results"
	"github.com/jcrussell/solvent-streets/internal/units"
	"github.com/jcrussell/solvent-streets/pkg/cmdutil"
	"github.com/jcrussell/solvent-streets/pkg/iostreams"
)

// seedComputedCity opens a file-backed store at path and commits one roads
// compute (compute result plus cohort stats) under cfg's hash for city, the
// way `pvmt roads compute` leaves it. It returns the city's store.
func seedComputedCity(t *testing.T, path string, cfg *config.Config, city *config.CityConfig) db.Store {
	t.Helper()
	ctx := context.Background()
	root, err := db.Open(context.Background(), path)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = root.Close() })
	id, err := root.EnsureCity(ctx, city.Slug(), city.Name, cfg.CityConfigID(city))
	if err != nil {
		t.Fatal(err)
	}
	store := root.ForCity(id)
	hash := cfg.CityHash(city)
	run, err := store.CreateRun(ctx, string(resource.TypeRoads), hash, nil)
	if err != nil {
		t.Fatal(err)
	}
	rows := append(results.TotalRows(resource.TypeRoads, results.Totals{Area: 100_000, Features: 2}),
		results.CohortRows(resource.TypeRoads, []results.Cohort{
			{Class: "residential", Area: 60_000, Features: 1},
			{Class: "primary", Area: 40_000, Features: 1},
		})...)
	if err := store.SaveCityMetrics(ctx, run.ID, rows); err != nil {
		t.Fatal(err)
	}
	if err := store.CommitLatest(ctx, hash, config.ComputeEpoch, map[string]int64{string(resource.TypeRoads): run.ID}); err != nil {
		t.Fatal(err)
	}
	return store.WithLatestCompute(hash)
}

// countRows counts the runs and snapshots in the database file at path,
// bypassing the store.
func countRows(t *testing.T, path string) (runs, snapshots int) {
	t.Helper()
	d, err := sql.Open("sqlite", "file:"+path+"?mode=ro")
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = d.Close() }()
	if err := d.QueryRow(`SELECT (SELECT COUNT(*) FROM runs), (SELECT COUNT(*) FROM snapshots)`).Scan(&runs, &snapshots); err != nil {
		t.Fatal(err)
	}
	return runs, snapshots
}

// runForecastJSON runs `pvmt forecast --json ...` for city against store and
// returns the decoded rows. extra are more flags.
func runForecastJSON(t *testing.T, cfg *config.Config, city *config.CityConfig, store db.Store, extra ...string) []map[string]any {
	t.Helper()
	rows, _ := runForecastJSONStderr(t, cfg, city, store, extra...)
	return rows
}

// runForecastJSONStderr is runForecastJSON that also returns what the
// command wrote to stderr.
func runForecastJSONStderr(t *testing.T, cfg *config.Config, city *config.CityConfig, store db.Store, extra ...string) ([]map[string]any, string) {
	t.Helper()
	ios, _, stdout, stderr := iostreams.Test()
	f := &cmdutil.Factory{
		IOStreams:   ios,
		UnitSystem:  func() units.System { return units.Metric },
		Config:      func() (*config.Config, error) { return cfg, nil },
		CurrentCity: func() (*config.CityConfig, error) { return city, nil },
		CityDB:      func() (db.Store, error) { return store, nil },
		CityFlagSet: func() bool { return true },
	}
	cmd := NewCmdForecast(f, nil)
	cmd.SilenceErrors, cmd.SilenceUsage = true, true
	cmd.SetArgs(append([]string{"--json", "city,scope,resource_type,year,pci,area,annual_need,cost_tier"}, extra...))
	if err := cmd.ExecuteContext(context.Background()); err != nil {
		t.Fatalf("forecast: %v\nstderr:\n%s", err, stderr.String())
	}
	var rows []map[string]any
	if err := json.Unmarshal(stdout.Bytes(), &rows); err != nil {
		t.Fatalf("decode --json output: %v\n%s", err, stdout.String())
	}
	return rows, stderr.String()
}

// TestForecast_SimulatesFromStoredCohortsAndWritesNothing pins `pvmt
// forecast` against a real store: it prints a forecast simulated from the
// snapshot's stored compute and cohort stats, and creates no run and commits
// no snapshot. Nothing is stored for a forecast; every reader simulates it.
func TestForecast_SimulatesFromStoredCohortsAndWritesNothing(t *testing.T) {
	path := filepath.Join(t.TempDir(), "pvmt.db")
	cfg := &config.Config{
		ConfigID: "test",
		Forecast: config.ForecastConfig{HorizonYears: 7},
		Cities:   []config.CityConfig{{Name: "Test City", Overpass: true}},
	}
	city := &cfg.Cities[0]
	store := seedComputedCity(t, path, cfg, city)
	runsBefore, snapsBefore := countRows(t, path)

	rows := runForecastJSON(t, cfg, city, store)

	if len(rows) != 7 {
		t.Fatalf("got %d rows, want one per year of the 7-year horizon (roads only): %v", len(rows), rows)
	}
	for i, r := range rows {
		if r["city"] != "test-city" || r["resource_type"] != string(resource.TypeRoads) || r["year"] != float64(i+1) {
			t.Errorf("row %d = %v, want test-city roads year %d", i, r, i+1)
		}
		// growth_rate defaults to 0, so every year carries the stored area.
		if area, _ := r["area"].(float64); math.Abs(area-100_000) > 1e-6 {
			t.Errorf("row %d area = %v, want the stored 100000 sq m", i, r["area"])
		}
		if need, _ := r["annual_need"].(float64); need <= 0 {
			t.Errorf("row %d annual_need = %v, want a positive cost", i, r["annual_need"])
		}
	}
	if runs, snaps := countRows(t, path); runs != runsBefore || snaps != snapsBefore {
		t.Errorf("forecast wrote to the database: runs %d -> %d, snapshots %d -> %d; it must only read",
			runsBefore, runs, snapsBefore, snaps)
	}
}

// TestForecast_ForecastEditNeedsNoRecompute: [forecast] settings are not part
// of the snapshot hash, so after a forecast-only edit the city's snapshot
// still applies and `pvmt forecast` simulates under the edited settings with
// no recompute.
func TestForecast_ForecastEditNeedsNoRecompute(t *testing.T) {
	path := filepath.Join(t.TempDir(), "pvmt.db")
	computed := &config.Config{
		ConfigID: "test",
		Forecast: config.ForecastConfig{HorizonYears: 7},
		Cities:   []config.CityConfig{{Name: "Test City", Overpass: true}},
	}
	store := seedComputedCity(t, path, computed, &computed.Cities[0])

	edited := &config.Config{
		ConfigID: "test",
		Forecast: config.ForecastConfig{HorizonYears: 3, DecayRate: 0.09, InitialPCI: 60, GrowthRate: 0.02},
		Cities:   []config.CityConfig{{Name: "Test City", Overpass: true}},
	}
	if got, want := edited.CityHash(&edited.Cities[0]), computed.CityHash(&computed.Cities[0]); got != want {
		t.Fatalf("a forecast-only edit moved the hash (%q vs %q)", got, want)
	}
	rows := runForecastJSON(t, edited, &edited.Cities[0], store)
	if len(rows) != 3 {
		t.Fatalf("got %d rows, want the edited 3-year horizon: %v", len(rows), rows)
	}
	// growth_rate 0.02 now applies: the area grows past the stored 100000.
	if area, _ := rows[2]["area"].(float64); area <= 100_000 {
		t.Errorf("year 3 area = %v, want the stored area grown by the edited growth_rate", rows[2]["area"])
	}
}

// TestForecast_DefaultsToTheCityMaintainedScope: like the dashboard's
// headline, pvmt forecast covers the pavement the city maintains when the
// city has city-scope results, and --scope all covers every jurisdiction.
// Each --json row names the scope it covers.
func TestForecast_DefaultsToTheCityMaintainedScope(t *testing.T) {
	ctx := context.Background()
	path := filepath.Join(t.TempDir(), "pvmt.db")
	cfg := &config.Config{
		ConfigID: "test",
		Forecast: config.ForecastConfig{HorizonYears: 3},
		Cities:   []config.CityConfig{{Name: "Test City", Overpass: true}},
	}
	city := &cfg.Cities[0]
	store := seedComputedCity(t, path, cfg, city)

	// Add the city-maintained view: the city maintains 30,000 of the 100,000 m².
	hash := cfg.CityHash(city)
	run, err := store.CreateRun(ctx, string(resource.TypeRoads)+"-city", hash, nil)
	if err != nil {
		t.Fatal(err)
	}
	rtCity := resource.TypeRoads.With(resource.ScopeCity)
	rows := append(results.TotalRows(rtCity, results.Totals{Area: 30_000, Features: 1}),
		results.CohortRows(rtCity, []results.Cohort{{Class: "residential", Area: 30_000, Features: 1}})...)
	if err := store.SaveCityMetrics(ctx, run.ID, rows); err != nil {
		t.Fatal(err)
	}
	if err := store.CommitLatest(ctx, hash, config.ComputeEpoch, map[string]int64{string(resource.TypeRoads) + "-city": run.ID}); err != nil {
		t.Fatal(err)
	}

	for _, tc := range []struct {
		flags []string
		scope string
		area  float64
	}{
		{nil, "city", 30_000},
		{[]string{"--scope", "city"}, "city", 30_000},
		{[]string{"--scope", "all"}, "all", 100_000},
	} {
		got := runForecastJSON(t, cfg, city, store, tc.flags...)
		if len(got) != 3 {
			t.Fatalf("%v: got %d rows, want 3: %v", tc.flags, len(got), got)
		}
		for _, r := range got {
			if area, _ := r["area"].(float64); r["scope"] != tc.scope || math.Abs(area-tc.area) > 1e-6 {
				t.Errorf("%v: row %v, want scope %q over %v m²", tc.flags, r, tc.scope, tc.area)
			}
		}
	}
}

// TestForecast_NoCityScopeFallsBackToEveryJurisdiction: a city with no
// city-maintained results computed is forecast over every jurisdiction, as
// the dashboard does, and the command says so rather than silently changing
// what the figures cover.
func TestForecast_NoCityScopeFallsBackToEveryJurisdiction(t *testing.T) {
	path := filepath.Join(t.TempDir(), "pvmt.db")
	cfg := &config.Config{
		ConfigID: "test",
		Forecast: config.ForecastConfig{HorizonYears: 3},
		Cities:   []config.CityConfig{{Name: "Test City", Overpass: true}},
	}
	city := &cfg.Cities[0]
	store := seedComputedCity(t, path, cfg, city) // all-jurisdiction rows only

	rows, stderr := runForecastJSONStderr(t, cfg, city, store)
	if len(rows) != 3 {
		t.Fatalf("got %d rows, want 3: %v", len(rows), rows)
	}
	for _, r := range rows {
		if area, _ := r["area"].(float64); r["scope"] != "all" || math.Abs(area-100_000) > 1e-6 {
			t.Errorf("row %v, want scope all over the stored 100000 m²", r)
		}
	}
	if !strings.Contains(stderr, "No city-maintained pavement is computed for this city, so the forecast covers every jurisdiction") {
		t.Errorf("no note that the forecast fell back to every jurisdiction; stderr:\n%s", stderr)
	}
	// Asked for every jurisdiction, there is nothing to say.
	if _, stderr := runForecastJSONStderr(t, cfg, city, store, "--scope", "all"); strings.Contains(stderr, "No city-maintained") {
		t.Errorf("--scope all printed the fallback note:\n%s", stderr)
	}
}
