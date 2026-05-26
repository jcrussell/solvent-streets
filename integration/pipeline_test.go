// Package integration holds end-to-end tests that wire several pkg/cmd
// subcommands together against a real sqlite store. They exist to catch
// connectedness regressions — bugs that compile, pass per-package unit
// tests, but break the chain between stages (compute writes shape X,
// forecast reads shape Y).
package integration

import (
	"context"
	"encoding/json"
	"math"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/jcrussell/solvent-streets/internal/config"
	"github.com/jcrussell/solvent-streets/internal/db"
	"github.com/jcrussell/solvent-streets/internal/resource"
	"github.com/jcrussell/solvent-streets/internal/units"
	"github.com/jcrussell/solvent-streets/pkg/cmd/compute"
	"github.com/jcrussell/solvent-streets/pkg/cmd/export"
	"github.com/jcrussell/solvent-streets/pkg/cmd/forecast"
	"github.com/jcrussell/solvent-streets/pkg/cmdutil"
	"github.com/jcrussell/solvent-streets/pkg/iostreams"
)

// TestPipeline_ComputeForecastExport pins the data-flow contract across
// compute → forecast → export. Each stage is independently unit-tested,
// but no other test wires them: a regression that, say, has compute write
// hex_stats with a different resource_type label than export reads would
// produce silently empty heatmaps.
//
// Ingest is intentionally skipped — its HTTP path is tested in pkg/cmd/
// ingest, and the value of this test is the *connectedness* of the
// downstream stages, not the upstream fetch. Features and boundary are
// pre-loaded directly into the store.
//
// Removing any stage (or breaking the resource_type label contract
// between them) causes a downstream assertion to fail; that's the
// regression signal.
func TestPipeline_ComputeForecastExport(t *testing.T) {
	ctx := context.Background()

	// Real sqlite RootStore: migrations run, FKs enforce, indexes used —
	// not the mock layer.
	dbPath := filepath.Join(t.TempDir(), "pipeline.db")
	root, err := db.Open(dbPath)
	if err != nil {
		t.Fatalf("db.Open: %v", err)
	}
	t.Cleanup(func() { _ = root.Close() })

	city := config.CityConfig{Name: "Pipeline City"}
	cityID, err := root.EnsureCity(ctx, city.Slug(), city.Name, "")
	if err != nil {
		t.Fatalf("EnsureCity: %v", err)
	}
	store := root.ForCity(cityID)

	loadFixtures(t, ctx, store)

	cfg := &config.Config{Cities: []config.CityConfig{city}}
	outDir := filepath.Join(t.TempDir(), "dist")

	f := factoryFor(cfg, &city, store, root)

	// compute roads, then parking. Order doesn't matter for the assertions
	// below, but running both proves the per-resource scoping in hex_stats
	// (city_id, resource_type) works across two writes against the same
	// store.
	for _, rt := range []resource.Source{&resource.Pavement{}, &resource.Parking{}} {
		cmd := compute.NewCmdCompute(f, rt, nil)
		cmd.SilenceErrors, cmd.SilenceUsage = true, true
		cmd.SetArgs(nil)
		if err := cmd.ExecuteContext(ctx); err != nil {
			t.Fatalf("compute %s: %v", rt.Type(), err)
		}
	}

	// forecast: reads compute_results + cohort_stats, writes forecast_results.
	fcmd := forecast.NewCmdForecast(f, nil)
	fcmd.SilenceErrors, fcmd.SilenceUsage = true, true
	fcmd.SetArgs([]string{"--scenarios=false"})
	if err := fcmd.ExecuteContext(ctx); err != nil {
		t.Fatalf("forecast: %v", err)
	}

	// export: reads everything, writes the static-site files.
	ecmd := export.NewCmdExport(f, nil)
	ecmd.SilenceErrors, ecmd.SilenceUsage = true, true
	ecmd.SetArgs([]string{"--output", outDir, "--clean"})
	if err := ecmd.ExecuteContext(ctx); err != nil {
		t.Fatalf("export: %v", err)
	}

	// Single-city exports go flat: dist/data/* + dist/index.html, no per-city
	// subdirectory. A multi-city export would nest under <slug>/.
	cityDir := outDir

	// Dump the export tree on failure so the assertions below are
	// debuggable.
	t.Cleanup(func() {
		if !t.Failed() {
			return
		}
		_ = filepath.Walk(outDir, func(p string, _ os.FileInfo, _ error) error {
			t.Logf("  %s", p)
			return nil
		})
	})

	t.Run("expected files exist", func(t *testing.T) {
		for _, name := range []string{
			"data/meta.json",
			"data/scenarios.json",
			"data/forecast.json",
			"index.html",
		} {
			path := filepath.Join(cityDir, name)
			if _, err := os.Stat(path); err != nil {
				t.Errorf("missing exported file %s: %v", name, err)
			}
		}
	})

	t.Run("meta.json carries the fixture city", func(t *testing.T) {
		var meta map[string]any
		readJSON(t, filepath.Join(cityDir, "data", "meta.json"), &meta)
		if got, _ := meta["project_name"].(string); got != city.Name {
			t.Errorf("meta.project_name = %q; want %q", got, city.Name)
		}
	})

	t.Run("forecast.json reflects both computed resources with finite values", func(t *testing.T) {
		var forecasts []map[string]any
		readJSON(t, filepath.Join(cityDir, "data", "forecast.json"), &forecasts)

		// Cross-stage contract: each compute pass writes a compute_result;
		// forecast reads each compute_result and emits one ForecastExport;
		// export serializes them all to forecast.json. Skipping any of
		// {roads compute, parking compute, forecast, export} drops a
		// resource from this set.
		gotResources := map[string]bool{}
		for _, fc := range forecasts {
			rt, _ := fc["resource_type"].(string)
			gotResources[rt] = true
		}
		for _, want := range []string{"roads", "parking"} {
			if !gotResources[want] {
				t.Errorf("forecast.json missing resource %q; got %v", want, gotResources)
			}
		}

		// First forecast must have a non-empty years series with finite PCI
		// — that's "forecast actually ran" rather than "an empty entry was
		// emitted."
		first := forecasts[0]
		baseline, _ := first["baseline"].(map[string]any)
		years, _ := baseline["years"].([]any)
		if len(years) == 0 {
			t.Fatalf("first forecast has no baseline years; got %+v", first)
		}
		y0, _ := years[0].(map[string]any)
		pci, ok := y0["pci"].(float64)
		if !ok {
			t.Fatalf("year 0 pci missing or non-numeric: %+v", y0)
		}
		if math.IsNaN(pci) || math.IsInf(pci, 0) {
			t.Errorf("year 0 pci = %v; want finite", pci)
		}
	})
}

// loadFixtures inserts the boundary plus one road and one parking feature.
// Coordinates form a ~1km bbox in Livermore, CA — small enough to make
// hex-grid generation fast, real enough that UTM projection has a
// reasonable zone.
func loadFixtures(t *testing.T, ctx context.Context, store db.Store) {
	t.Helper()
	const boundary = `{"type":"Polygon","coordinates":[[[-121.770,37.680],[-121.760,37.680],[-121.760,37.690],[-121.770,37.690],[-121.770,37.680]]]}`
	if err := store.SaveBoundary(ctx, boundary, "fixture"); err != nil {
		t.Fatalf("SaveBoundary: %v", err)
	}
	rtRoads := resource.TypeRoads
	rtParking := resource.TypeParking
	if err := store.UpsertFeatures(ctx, rtRoads, []db.Feature{{
		ID:           "fixture:road:1",
		ResourceType: rtRoads,
		Name:         "Fixture Rd",
		Tags:         map[string]string{"highway": "residential"},
		GeometryJSON: `{"type":"LineString","coordinates":[[-121.768,37.682],[-121.762,37.688]]}`,
		SourceAPI:    "fixture",
		FetchedAt:    time.Now(),
	}}); err != nil {
		t.Fatalf("UpsertFeatures roads: %v", err)
	}
	if err := store.UpsertFeatures(ctx, rtParking, []db.Feature{{
		ID:           "fixture:parking:1",
		ResourceType: rtParking,
		Name:         "Fixture Lot",
		Tags:         map[string]string{"amenity": "parking"},
		GeometryJSON: `{"type":"Polygon","coordinates":[[[-121.766,37.684],[-121.765,37.684],[-121.765,37.685],[-121.766,37.685],[-121.766,37.684]]]}`,
		SourceAPI:    "fixture",
		FetchedAt:    time.Now(),
	}}); err != nil {
		t.Fatalf("UpsertFeatures parking: %v", err)
	}
}

// factoryFor returns a Factory wired to a real store. The lazy closures
// match the production shape — every command in the pipeline pulls only
// the deps it declares.
func factoryFor(cfg *config.Config, city *config.CityConfig, store db.Store, root *db.RootStore) *cmdutil.Factory {
	ios, _, _, _ := iostreams.Test()
	return &cmdutil.Factory{
		IOStreams:   ios,
		UnitSystem:  func() units.System { return units.Metric },
		Config:      func() (*config.Config, error) { return cfg, nil },
		CurrentCity: func() (*config.CityConfig, error) { return city, nil },
		CityDB:      func() (db.Store, error) { return store, nil },
		RootDB:      func() (*db.RootStore, error) { return root, nil },
		CityFlagSet: func() bool { return true },
	}
}

func readJSON(t *testing.T, path string, v any) {
	t.Helper()
	data, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("read %s: %v", path, err)
	}
	if err := json.Unmarshal(data, v); err != nil {
		// Surface the start of the file for diagnosis.
		preview := string(data)
		if len(preview) > 200 {
			preview = preview[:200] + "…"
		}
		t.Fatalf("unmarshal %s: %v\npreview: %s", path, err, preview)
	}
}
