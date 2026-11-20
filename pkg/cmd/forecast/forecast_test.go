package forecast

import (
	"context"
	"errors"
	"math"
	"path/filepath"
	"regexp"
	"strconv"
	"strings"
	"testing"

	"github.com/jcrussell/solvent-streets/internal/config"
	"github.com/jcrussell/solvent-streets/internal/db"
	"github.com/jcrussell/solvent-streets/internal/db/dbtest"
	fcpkg "github.com/jcrussell/solvent-streets/internal/forecast"
	"github.com/jcrussell/solvent-streets/internal/resource"
	"github.com/jcrussell/solvent-streets/internal/units"
	"github.com/jcrussell/solvent-streets/pkg/cmdutil"
	"github.com/jcrussell/solvent-streets/pkg/iostreams"
)

// TestForecastRow_ExportData_AllFieldsPopulated guards S2: the handwritten
// switch in forecastRow.ExportData is the JSON contract — a typo silently
// drops a field.
func TestForecastRow_ExportData_AllFieldsPopulated(t *testing.T) {
	rtRoads := resource.TypeRoads
	r := forecastRow{
		City:         "livermore-ca",
		ResourceType: rtRoads,
		Year:         2030,
		PCI:          72.5,
		Area:         1500.0,
		AnnualNeed:   42000.0,
		CostTier:     "mill-and-overlay",
	}
	out := r.ExportData(forecastFields)
	if len(out) != len(forecastFields) {
		t.Fatalf("want %d keys, got %d: %v", len(forecastFields), len(out), out)
	}
	for _, f := range forecastFields {
		if _, ok := out[f]; !ok {
			t.Errorf("missing field %q", f)
		}
	}
	if out["city"] != "livermore-ca" || out["resource_type"] != string(rtRoads) || out["year"] != 2030 {
		t.Errorf("unexpected values: %+v", out)
	}
}

// TestForecastAllResources_PropagatesNonNoRowsError: a genuine
// query failure (locked DB, dropped table) must propagate, not fold into the
// "no compute results, skipping" path, which exits 0.
func TestForecastAllResources_PropagatesNonNoRowsError(t *testing.T) {
	ios, _, _, _ := iostreams.Test()
	opts := &Options{IO: ios}
	fc := &config.ForecastConfig{InitialPCI: 85, HorizonYears: 5}

	boom := errors.New("database is locked")
	store := &dbtest.MockStore{
		ListCityMetricsFunc: func(context.Context, []string) ([]db.CityMetric, error) {
			return nil, boom
		},
	}

	_, err := forecastAllResources(context.Background(), opts, store, "c", fc, units.Metric)
	if err == nil {
		t.Fatal("expected error to propagate, got nil")
	}
	if !errors.Is(err, boom) {
		t.Fatalf("expected wrapped %v, got: %v", boom, err)
	}
	if errors.Is(err, cmdutil.ErrNoResults) {
		t.Fatalf("real query failure must not be ErrNoResults, got: %v", err)
	}
}

// TestForecastAllResources_AllSkippedReturnsNoResults: when every
// resource is un-computed (no city totals) the command returns ErrNoResults
// (exit 3), not (nil, nil) → exit 0.
func TestForecastAllResources_AllSkippedReturnsNoResults(t *testing.T) {
	ios, _, _, _ := iostreams.Test()
	opts := &Options{IO: ios}
	fc := &config.ForecastConfig{InitialPCI: 85, HorizonYears: 5}

	store := &dbtest.MockStore{}

	results, err := forecastAllResources(context.Background(), opts, store, "c", fc, units.Metric)
	if !errors.Is(err, cmdutil.ErrNoResults) {
		t.Fatalf("expected ErrNoResults, got: %v", err)
	}
	if results != nil {
		t.Fatalf("expected nil results, got: %v", results)
	}
}

func TestNewCmdForecast_RunFInjection(t *testing.T) {
	ios, _, _, _ := iostreams.Test()
	f := &cmdutil.Factory{IOStreams: ios}

	var gotOpts *Options
	cmd := NewCmdForecast(f, func(_ context.Context, opts *Options) error {
		gotOpts = opts
		return nil
	})
	cmd.SetArgs([]string{"--scenarios=false"})
	if err := cmd.Execute(); err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if gotOpts == nil {
		t.Fatal("runF was not invoked")
	}
	if gotOpts.IO != ios {
		t.Errorf("opts.IO not propagated from factory")
	}
	if gotOpts.Scenarios {
		t.Errorf("expected --scenarios=false to set Scenarios to false")
	}
}

func TestNewCmdForecast_DefaultScenariosTrue(t *testing.T) {
	ios, _, _, _ := iostreams.Test()
	f := &cmdutil.Factory{IOStreams: ios}

	var gotOpts *Options
	cmd := NewCmdForecast(f, func(_ context.Context, opts *Options) error {
		gotOpts = opts
		return nil
	})
	cmd.SetArgs([]string{})
	if err := cmd.Execute(); err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if !gotOpts.Scenarios {
		t.Errorf("expected default Scenarios to be true")
	}
}

func TestNewCmdForecast_JqAndTemplateMutuallyExclusive(t *testing.T) {
	ios, _, _, _ := iostreams.Test()
	f := &cmdutil.Factory{IOStreams: ios}

	cmd := NewCmdForecast(f, func(_ context.Context, _ *Options) error { return nil })
	cmd.SetArgs([]string{"--json", "year", "--jq", ".", "--template", "{{.}}"})
	cmd.SilenceErrors = true
	cmd.SilenceUsage = true
	err := cmd.Execute()
	if err == nil {
		t.Fatal("expected error when --jq and --template both set")
	}
}

// TestRenderBaselineTable_AreaPctSumsTo100UnderGrowth pins the cohort "Area %"
// column against a base-mixing regression: CohortSummary.Area is the final-year
// *grown* area, so normalizing it by the year-0 network area made the column sum
// to the growth factor (120% at 20yr / 1%) rather than 100%, contradicting the
// "Current area" line printed directly above it. The denominator must be derived
// from the cohort areas themselves.
func TestRenderBaselineTable_AreaPctSumsTo100UnderGrowth(t *testing.T) {
	const growthRate = 0.01
	const years = 20

	params := fcpkg.NewParams(growthRate, nil, 12, 1)
	cohorts := []fcpkg.Cohort{
		{Classification: "primary", Area: 250_000, DecayRate: 0.05, InitialPCI: 85},
		{Classification: "secondary", Area: 500_000, DecayRate: 0.04, InitialPCI: 75},
		{Classification: "residential", Area: 750_000, DecayRate: 0.03, InitialPCI: 65},
	}
	// year0Area is what the caller passes as `area` (result.Area): the
	// un-grown network area. The grown cohort areas exceed it by 1+r*N = 1.20.
	var year0Area float64
	for _, c := range cohorts {
		year0Area += c.Area
	}

	baseline := fcpkg.Simulate(
		fcpkg.Scenario{Name: "dn", Label: "DN", Strategy: fcpkg.StrategyDoNothing},
		cohorts, years, params,
	)

	var grownArea float64
	for _, c := range baseline.FinalCohorts {
		grownArea += c.Area
	}
	if wantGrown := year0Area * (1 + growthRate*years); math.Abs(grownArea-wantGrown) > 1e-6 {
		t.Fatalf("precondition: grown cohort area = %.4f, want %.4f", grownArea, wantGrown)
	}
	if grownArea <= year0Area {
		t.Fatal("precondition: growth must make the two bases differ, else the test is vacuous")
	}

	ios, _, out, _ := iostreams.Test()
	err := renderBaselineTable(ios, resource.ByType(resource.TypeRoads), "all", year0Area, 75,
		baseline, 0, years, units.Metric)
	if err != nil {
		t.Fatalf("renderBaselineTable: %v", err)
	}

	pcts := regexp.MustCompile(`(\d+\.\d)%`).FindAllStringSubmatch(out.String(), -1)
	if len(pcts) != len(cohorts) {
		t.Fatalf("expected %d percentage cells, got %d\noutput:\n%s", len(cohorts), len(pcts), out.String())
	}
	var sum float64
	for _, m := range pcts {
		v, perr := strconv.ParseFloat(m[1], 64)
		if perr != nil {
			t.Fatalf("parse %q: %v", m[1], perr)
		}
		sum += v
	}
	// Tolerance covers only the %.1f rounding of each of the 3 cells.
	if math.Abs(sum-100) > 0.15 {
		t.Errorf("Area %% column sums to %.1f%%, want 100%%; the denominator is "+
			"mixing the year-0 network area with grown cohort areas\noutput:\n%s", sum, out.String())
	}
}

// TestRunForecast_StaleSnapshotsSayWhy: a city
// computed only under other settings used to print "no compute results for
// roads, skipping" once per resource and exit 3 with no hint why. Now it says
// once, with export's wording, and still exits 3 (ErrNoResults).
func TestRunForecast_StaleSnapshotsSayWhy(t *testing.T) {
	ios, _, _, stderr := iostreams.Test()
	cfg := &config.Config{}
	city := &config.CityConfig{Name: "Test City"}
	store := &dbtest.MockStore{
		ListSnapshotsFunc: func(context.Context) ([]db.SnapshotCity, error) {
			return []db.SnapshotCity{{ID: 1, ConfigHash: "0ld0ld0ld0ld0ld0"}}, nil
		},
	}
	opts := &Options{
		IO:          ios,
		Config:      func() (*config.Config, error) { return cfg, nil },
		CurrentCity: func() (*config.CityConfig, error) { return city, nil },
		CityDB:      func() (db.Store, error) { return store, nil },
		UnitSystem:  func() units.System { return units.Metric },
	}
	err := runForecast(context.Background(), opts)
	if !errors.Is(err, cmdutil.ErrNoResults) {
		t.Fatalf("err = %v, want ErrNoResults (exit 3)", err)
	}
	out := stderr.String()
	if want := cfg.StaleHint(city); !strings.Contains(out, want) {
		t.Errorf("stderr lacks the shared stale-snapshot hint:\n%s", out)
	}
	if strings.Contains(out, "no compute results") {
		t.Errorf("stderr still has the bare per-resource warnings:\n%s", out)
	}
}

// TestRunForecast_LatestComputeIsNotStale: forecast reads the latest compute,
// so a city recomputed under the current settings but never snapshotted under
// them is current; the stale hint is for a city with nothing computed under
// them.
func TestRunForecast_LatestComputeIsNotStale(t *testing.T) {
	ios, _, _, stderr := iostreams.Test()
	cfg := &config.Config{}
	city := &config.CityConfig{Name: "Test City"}
	store := &dbtest.MockStore{
		ListSnapshotsFunc: func(context.Context) ([]db.SnapshotCity, error) {
			return []db.SnapshotCity{{ID: 1, ConfigHash: "0ld0ld0ld0ld0ld0"}}, nil
		},
		LatestComputeFunc: func(context.Context, string) ([]db.LatestPart, error) {
			return []db.LatestPart{{Part: "roads", RunID: 2}}, nil
		},
	}
	opts := &Options{
		IO:          ios,
		Config:      func() (*config.Config, error) { return cfg, nil },
		CurrentCity: func() (*config.CityConfig, error) { return city, nil },
		CityDB:      func() (db.Store, error) { return store, nil },
		UnitSystem:  func() units.System { return units.Metric },
	}
	_ = runForecast(context.Background(), opts)
	if out := stderr.String(); strings.Contains(out, cfg.StaleHint(city)) {
		t.Errorf("stale hint printed for a city with a latest compute under the current settings:\n%s", out)
	}
}

// TestRunForecast_Snapshot: --snapshot forecasts from the named snapshot's
// view of the city, skipping the latest-compute staleness check, and a
// snapshot that does not cover the city says so.
func TestRunForecast_Snapshot(t *testing.T) {
	cfg := &config.Config{}
	city := &config.CityConfig{Name: "Test City"}
	var pinned int64
	view := &dbtest.MockStore{}
	store := &dbtest.MockStore{
		ResolveSnapshotFunc: func(_ context.Context, name string) (int64, error) {
			if name == "2026-11" {
				return 7, nil
			}
			return 0, db.ErrSnapshotNotFound
		},
		WithSnapshotFunc: func(id int64) db.Store { pinned = id; return view },
		LatestComputeFunc: func(context.Context, string) ([]db.LatestPart, error) {
			t.Error("--snapshot read the latest compute")
			return nil, nil
		},
	}
	run := func(name string) error {
		ios, _, _, _ := iostreams.Test()
		return runForecast(context.Background(), &Options{
			IO:          ios,
			Config:      func() (*config.Config, error) { return cfg, nil },
			CurrentCity: func() (*config.CityConfig, error) { return city, nil },
			CityDB:      func() (db.Store, error) { return store, nil },
			UnitSystem:  func() units.System { return units.Metric },
			Snapshot:    name,
		})
	}
	if err := run("2026-11"); !errors.Is(err, cmdutil.ErrNoResults) || pinned != 7 {
		t.Errorf("err = %v, pinned %d; want the empty snapshot view (ErrNoResults) of snapshot 7", err, pinned)
	}
	if err := run("2025-01"); err == nil || !strings.Contains(err.Error(), `snapshot "2025-01" does not cover Test City`) {
		t.Errorf("err = %v, want a not-covered error", err)
	}
}

// TestForecast_UnknownSnapshotRefusedOnce: a --snapshot no snapshot has is
// refused before any city runs, not once per city.
func TestForecast_UnknownSnapshotRefusedOnce(t *testing.T) {
	root, err := db.Open(context.Background(), filepath.Join(t.TempDir(), db.FileName))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = root.Close() })
	ios, _, _, _ := iostreams.Test()
	cfg := &config.Config{Cities: []config.CityConfig{{Name: "Alpha, CA"}, {Name: "Beta, CA"}}}
	f := &cmdutil.Factory{
		IOStreams:   ios,
		RootDB:      func() (*db.RootStore, error) { return root, nil },
		Config:      func() (*config.Config, error) { return cfg, nil },
		CurrentCity: func() (*config.CityConfig, error) { return &cfg.Cities[0], nil },
		CityFlagSet: func() bool { return false },
		CityDB:      func() (db.Store, error) { t.Fatal("a city ran"); return nil, errors.New("unreachable") },
		UnitSystem:  func() units.System { return units.Metric },
	}
	cmd := NewCmdForecast(f, nil)
	cmd.SetArgs([]string{"--snapshot", "2025-01"})
	cmd.SilenceErrors, cmd.SilenceUsage = true, true
	if err := cmd.Execute(); err == nil || err.Error() != `no snapshot named "2025-01"` {
		t.Errorf("err = %v, want one no-snapshot error", err)
	}
}
