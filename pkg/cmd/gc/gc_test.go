package gc

import (
	"context"
	"errors"
	"slices"
	"strings"
	"testing"

	"github.com/jcrussell/solvent-streets/internal/config"
	"github.com/jcrussell/solvent-streets/internal/db"
	"github.com/jcrussell/solvent-streets/internal/db/dbtest"
	"github.com/jcrussell/solvent-streets/pkg/cmd/prompt"
	"github.com/jcrussell/solvent-streets/pkg/cmdutil"
	"github.com/jcrussell/solvent-streets/pkg/iostreams"
)

func resolveCitiesFunc(cities []config.CityConfig) func() ([]config.CityConfig, error) {
	return func() ([]config.CityConfig, error) { return cities, nil }
}

func rootDBFunc(root db.RootStorer) func() (db.RootStorer, error) {
	return func() (db.RootStorer, error) { return root, nil }
}

// reportWithCounts builds a GCReport whose total is non-zero so the run
// proceeds past the "nothing to collect" short-circuit.
func reportWithCounts() *db.GCReport {
	return &db.GCReport{
		StaleFeatures:       3,
		NullSnapshotResults: db.GCResultCounts{ForecastResults: 5, HexStats: 2},
		DanglingResults:     db.GCResultCounts{ComputeResults: 1},
	}
}

// TestNewCmdGC_RunFInjection pins the test-injection seam.
func TestNewCmdGC_RunFInjection(t *testing.T) {
	ios, _, _, _ := iostreams.Test()
	f := &cmdutil.Factory{IOStreams: ios}
	called := false
	cmd := NewCmdGC(f, func(context.Context, *Options) error {
		called = true
		return nil
	})
	cmd.SetArgs([]string{})
	if err := cmd.Execute(); err != nil {
		t.Fatal(err)
	}
	if !called {
		t.Error("runF was not called")
	}
}

// TestKeepSourcesFor verifies the config -> source-set mapping that decides
// which feature source_api values gc treats as keepers (mirrors ingest).
func TestKeepSourcesFor(t *testing.T) {
	cases := []struct {
		name          string
		city          config.CityConfig
		sweepDisabled bool
		want          []string
	}{
		// "arcgis" is kept only when ArcGISURL is set — removing arcgis_url is
		// the unambiguous case gc advertises, and it stays sweepable.
		{"overpass flag on", config.CityConfig{Overpass: true}, false, []string{"overpass"}},
		{"overpass+arcgis", config.CityConfig{Overpass: true, ArcGISURL: "https://x"}, false, []string{"overpass", "arcgis"}},
		// overpass=false + an ArcGIS URL KEEPS overpass by default
		// (solvent-streets-dnun). `overpass` defaults to false and older
		// releases ingested overpass regardless, so a user who never touched
		// the flag holds real data here, not orphans.
		{"arcgis url, overpass flag off", config.CityConfig{Overpass: false, ArcGISURL: "https://x"}, false, []string{"overpass", "arcgis"}},
		// The explicit opt-in is what drops it — for the user who really did
		// flip overpass=false and wants the old rows gone.
		{"arcgis url, overpass off, opt-in", config.CityConfig{Overpass: false, ArcGISURL: "https://x"}, true, []string{"arcgis"}},
		// The opt-in cannot drop a source the city actually enables.
		{"overpass on, opt-in", config.CityConfig{Overpass: true, ArcGISURL: "https://x"}, true, []string{"overpass", "arcgis"}},
		// Empty config under the opt-in keeps nothing; config validation forbids
		// this combination for a real city, so keepSourcesFor is only ever
		// handed valid cities. Without the opt-in it still keeps overpass, which
		// is what stops an empty keep set from reaching GCSweep.
		{"none configured, opt-in", config.CityConfig{}, true, nil},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			got := keepSourcesFor(tc.city, tc.sweepDisabled)
			if strings.Join(got, ",") != strings.Join(tc.want, ",") {
				t.Errorf("keepSourcesFor = %v, want %v", got, tc.want)
			}
		})
	}
}

// TestRunGC_ArcGISOnlyKeepsOverpassByDefault pins solvent-streets-dnun: a city
// with Overpass:false and an ArcGIS URL must NOT have its overpass rows swept
// unless the user opts in.
//
// The regression this guards is silent data loss on upgrade, not a wrong count.
// `overpass` defaults to false and validateCityFields accepts a city that sets
// arcgis_url and omits it, while older releases appended OverpassSource
// unconditionally. So a user who never touched the flag can hold a full set of
// real overpass rows, and a keep set that mirrors ingest.AllSources exactly
// would delete every one of them on the first `pvmt gc --yes` after an upgrade
// — surfacing only as a smaller paved area on the next compute.
func TestRunGC_ArcGISOnlyKeepsOverpassByDefault(t *testing.T) {
	cities := []config.CityConfig{{Name: "Alpha", Overpass: false, ArcGISURL: "https://x"}}
	var sweptKeep []string
	root := &dbtest.MockRootStore{
		EnsureCityFunc: func(context.Context, string, string, string) (int64, error) { return 1, nil },
		ForCityFunc: func(int64) db.Store {
			return &dbtest.MockStore{
				GCScanFunc: func(_ context.Context, keep []string) (*db.GCReport, error) {
					return reportWithCounts(), nil
				},
				GCSweepFunc: func(_ context.Context, keep []string) (*db.GCReport, error) {
					sweptKeep = keep
					return reportWithCounts(), nil
				},
			}
		},
	}
	ios, _, _, _ := iostreams.Test()
	opts := &Options{
		IO:            ios,
		RootDB:        rootDBFunc(root),
		ResolveCities: resolveCitiesFunc(cities),
		Yes:           true,
	}
	if err := runGC(context.Background(), opts); err != nil {
		t.Fatal(err)
	}
	if !slices.Contains(sweptKeep, "overpass") {
		t.Errorf("GCSweep keep = %v, want it to CONTAIN overpass; "+
			"overpass rows on an overpass=false city are not orphans by default", sweptKeep)
	}
	if !slices.Contains(sweptKeep, "arcgis") {
		t.Errorf("GCSweep keep = %v, want it to contain arcgis", sweptKeep)
	}
}

// TestRunGC_RetainedCountFailureIsNotFatal pins solvent-streets-q48z.21.
//
// retainedOverpassRows issues a SECOND GCScan purely to derive the cosmetic
// "kept (overpass off)" line. scanCities used to wrap its error as `scan %s`
// and return it, so a query that feeds nothing but an informational message
// could fail a gc run whose real scan — and whole sweep — had succeeded.
// It must warn and carry on instead.
//
// The city deliberately omits overpass and sets arcgis_url: that is the only
// shape for which retainedOverpassRows touches the DB at all (it returns
// (0, nil) up front when sweepDisabled || city.Overpass).
func TestRunGC_RetainedCountFailureIsNotFatal(t *testing.T) {
	cities := []config.CityConfig{{Name: "Alpha", Overpass: false, ArcGISURL: "https://x"}}
	scans := 0
	swept := false
	root := &dbtest.MockRootStore{
		EnsureCityFunc: func(context.Context, string, string, string) (int64, error) { return 1, nil },
		ForCityFunc: func(int64) db.Store {
			return &dbtest.MockStore{
				GCScanFunc: func(_ context.Context, keep []string) (*db.GCReport, error) {
					scans++
					if scans == 1 {
						return reportWithCounts(), nil // the primary scan succeeds
					}
					return nil, errors.New("boom") // the cosmetic rescan does not
				},
				GCSweepFunc: func(_ context.Context, keep []string) (*db.GCReport, error) {
					swept = true
					return reportWithCounts(), nil
				},
			}
		},
	}
	ios, _, _, errOut := iostreams.Test()
	opts := &Options{
		IO:            ios,
		RootDB:        rootDBFunc(root),
		ResolveCities: resolveCitiesFunc(cities),
		Yes:           true,
	}
	if err := runGC(context.Background(), opts); err != nil {
		t.Fatalf("runGC = %v, want nil; a cosmetic rescan must not fail the run", err)
	}
	if scans < 2 {
		t.Fatalf("GCScan called %d time(s); the test never exercised the rescan path", scans)
	}
	if !swept {
		t.Error("GCSweep was not called; the run aborted before doing its real work")
	}
	if !strings.Contains(errOut.String(), "warning:") {
		t.Errorf("no warning printed for the failed rescan:\n%s", errOut.String())
	}
}

// TestRunGC_SweepDisabledSourcesDropsOverpass pins the other half: the explicit
// opt-in is what makes an overpass=false city's overpass rows sweepable, for the
// user who really did flip the flag.
func TestRunGC_SweepDisabledSourcesDropsOverpass(t *testing.T) {
	cities := []config.CityConfig{{Name: "Alpha", Overpass: false, ArcGISURL: "https://x"}}
	var sweptKeep []string
	root := &dbtest.MockRootStore{
		EnsureCityFunc: func(context.Context, string, string, string) (int64, error) { return 1, nil },
		ForCityFunc: func(int64) db.Store {
			return &dbtest.MockStore{
				GCScanFunc: func(_ context.Context, keep []string) (*db.GCReport, error) {
					if slices.Contains(keep, "overpass") {
						t.Errorf("GCScan keep = %v, want it NOT to contain overpass under the opt-in", keep)
					}
					return reportWithCounts(), nil
				},
				GCSweepFunc: func(_ context.Context, keep []string) (*db.GCReport, error) {
					sweptKeep = keep
					return reportWithCounts(), nil
				},
			}
		},
	}
	ios, _, _, _ := iostreams.Test()
	opts := &Options{
		IO:                   ios,
		RootDB:               rootDBFunc(root),
		ResolveCities:        resolveCitiesFunc(cities),
		Yes:                  true,
		SweepDisabledSources: true,
	}
	if err := runGC(context.Background(), opts); err != nil {
		t.Fatal(err)
	}
	if slices.Contains(sweptKeep, "overpass") {
		t.Errorf("GCSweep keep = %v, want it NOT to contain overpass", sweptKeep)
	}
	if !slices.Contains(sweptKeep, "arcgis") {
		t.Errorf("GCSweep keep = %v, want it to contain arcgis", sweptKeep)
	}
}

// TestRunGC_ReportsRetainedOverpassRows pins that the rows gc holds back are
// NAMED rather than silently omitted: the scan re-runs with the strict keep set,
// diffs the stale counts, and prints the difference with the remedy.
//
// Without this line the report reads as "3 stale features" whether or not
// another 7 rows were spared, and a user with a genuinely stale overpass source
// would have no way to find out gc is not going to clean it.
func TestRunGC_ReportsRetainedOverpassRows(t *testing.T) {
	cities := []config.CityConfig{{Name: "Alpha", Overpass: false, ArcGISURL: "https://x"}}
	root := &dbtest.MockRootStore{
		EnsureCityFunc: func(context.Context, string, string, string) (int64, error) { return 1, nil },
		ForCityFunc: func(int64) db.Store {
			return &dbtest.MockStore{
				GCScanFunc: func(_ context.Context, keep []string) (*db.GCReport, error) {
					// The strict pass (no "overpass") finds 7 more stale rows
					// than the safe one; those 7 are the retained overpass rows.
					if slices.Contains(keep, "overpass") {
						return &db.GCReport{StaleFeatures: 3}, nil
					}
					return &db.GCReport{StaleFeatures: 10}, nil
				},
			}
		},
	}
	ios, _, _, stderr := iostreams.Test()
	opts := &Options{
		IO:            ios,
		RootDB:        rootDBFunc(root),
		ResolveCities: resolveCitiesFunc(cities),
		DryRun:        true,
	}
	if err := runGC(context.Background(), opts); err != nil {
		t.Fatal(err)
	}
	out := stderr.String()
	if !strings.Contains(out, "kept (overpass off):   7") {
		t.Errorf("report did not name the 7 retained overpass rows:\n%s", out)
	}
	if !strings.Contains(out, "--sweep-disabled-sources") {
		t.Errorf("report did not name the remedy flag:\n%s", out)
	}
}

// TestRunGC_DryRunScansButDoesNotSweep pins the dry-run contract: GCScan is
// called, GCSweep is NOT, nothing is deleted, and the per-table counts are
// reported.
func TestRunGC_DryRunScansButDoesNotSweep(t *testing.T) {
	cities := []config.CityConfig{{Name: "Alpha", Overpass: true}}
	var scanned, swept bool
	root := &dbtest.MockRootStore{
		EnsureCityFunc: func(context.Context, string, string, string) (int64, error) { return 1, nil },
		ForCityFunc: func(int64) db.Store {
			return &dbtest.MockStore{
				GCScanFunc: func(_ context.Context, keep []string) (*db.GCReport, error) {
					scanned = true
					if len(keep) != 1 || keep[0] != "overpass" {
						t.Errorf("keepSources = %v, want [overpass]", keep)
					}
					return reportWithCounts(), nil
				},
				GCSweepFunc: func(context.Context, []string) (*db.GCReport, error) {
					swept = true
					return &db.GCReport{}, nil
				},
			}
		},
	}
	ios, _, stdout, stderr := iostreams.Test()
	opts := &Options{
		IO:            ios,
		RootDB:        rootDBFunc(root),
		ResolveCities: resolveCitiesFunc(cities),
		DryRun:        true,
	}
	if err := runGC(context.Background(), opts); err != nil {
		t.Fatal(err)
	}
	if !scanned {
		t.Error("GCScan was not called")
	}
	if swept {
		t.Error("GCSweep was called during --dry-run")
	}
	if got := stdout.String(); got != "" {
		t.Errorf("stdout should be empty; got: %q", got)
	}
	out := stderr.String()
	for _, want := range []string{"stale features:        3", "forecast=5", "Dry run", "nothing deleted"} {
		if !strings.Contains(out, want) {
			t.Errorf("expected %q in output, got: %s", want, out)
		}
	}
}

// TestRunGC_YesSweeps pins that --yes skips the prompt and calls GCSweep.
func TestRunGC_YesSweeps(t *testing.T) {
	cities := []config.CityConfig{{Name: "Alpha", Overpass: true}}
	var swept bool
	root := &dbtest.MockRootStore{
		EnsureCityFunc: func(context.Context, string, string, string) (int64, error) { return 1, nil },
		ForCityFunc: func(int64) db.Store {
			return &dbtest.MockStore{
				GCScanFunc:  func(context.Context, []string) (*db.GCReport, error) { return reportWithCounts(), nil },
				GCSweepFunc: func(context.Context, []string) (*db.GCReport, error) { swept = true; return reportWithCounts(), nil },
			}
		},
	}
	ios, _, _, stderr := iostreams.Test()
	opts := &Options{
		IO:            ios,
		RootDB:        rootDBFunc(root),
		ResolveCities: resolveCitiesFunc(cities),
		Yes:           true,
	}
	if err := runGC(context.Background(), opts); err != nil {
		t.Fatal(err)
	}
	if !swept {
		t.Error("GCSweep was not called with --yes")
	}
	if !strings.Contains(stderr.String(), "Collected") {
		t.Errorf("expected 'Collected' summary on stderr, got: %s", stderr.String())
	}
}

// TestRunGC_NoTTYWithoutYesIsFlagError pins byob-prompter.3: with orphans
// to collect, no TTY, and no --yes, gc refuses with a --yes hint and never
// sweeps.
func TestRunGC_NoTTYWithoutYesIsFlagError(t *testing.T) {
	cities := []config.CityConfig{{Name: "Alpha", Overpass: true}}
	var swept bool
	root := &dbtest.MockRootStore{
		EnsureCityFunc: func(context.Context, string, string, string) (int64, error) { return 1, nil },
		ForCityFunc: func(int64) db.Store {
			return &dbtest.MockStore{
				GCScanFunc:  func(context.Context, []string) (*db.GCReport, error) { return reportWithCounts(), nil },
				GCSweepFunc: func(context.Context, []string) (*db.GCReport, error) { swept = true; return &db.GCReport{}, nil },
			}
		},
	}
	ios, _, _, _ := iostreams.Test() // stdin TTY defaults to false
	opts := &Options{
		IO:            ios,
		RootDB:        rootDBFunc(root),
		ResolveCities: resolveCitiesFunc(cities),
	}
	err := runGC(context.Background(), opts)
	var fe *cmdutil.FlagError
	if !errors.As(err, &fe) {
		t.Fatalf("want *cmdutil.FlagError, got %T: %v", err, err)
	}
	var hint *cmdutil.ErrHint
	if !errors.As(err, &hint) || !strings.Contains(hint.Hint, "--yes") {
		t.Errorf("expected --yes hint, got: %v", err)
	}
	if swept {
		t.Error("GCSweep was called despite refusal")
	}
}

// TestRunGC_NothingToCollect verifies the no-op path: a zero-total report
// short-circuits before any prompt or sweep.
func TestRunGC_NothingToCollect(t *testing.T) {
	cities := []config.CityConfig{{Name: "Alpha", Overpass: true}}
	var swept bool
	root := &dbtest.MockRootStore{
		EnsureCityFunc: func(context.Context, string, string, string) (int64, error) { return 1, nil },
		ForCityFunc: func(int64) db.Store {
			return &dbtest.MockStore{
				GCScanFunc:  func(context.Context, []string) (*db.GCReport, error) { return &db.GCReport{}, nil },
				GCSweepFunc: func(context.Context, []string) (*db.GCReport, error) { swept = true; return &db.GCReport{}, nil },
			}
		},
	}
	ios, _, _, stderr := iostreams.Test()
	ios.SetStdinTTY(true) // would prompt — must not, nothing to do
	opts := &Options{
		IO:            ios,
		Prompter:      &prompt.Stub{}, // zero confirms queued: a prompt would panic
		RootDB:        rootDBFunc(root),
		ResolveCities: resolveCitiesFunc(cities),
	}
	if err := runGC(context.Background(), opts); err != nil {
		t.Fatal(err)
	}
	if swept {
		t.Error("GCSweep was called when there was nothing to collect")
	}
	if !strings.Contains(stderr.String(), "Nothing to collect.") {
		t.Errorf("expected no-op hint on stderr, got: %s", stderr.String())
	}
}

// TestRunGC_PromptCancelDoesNotSweep pins the "no" branch: declining the
// prompt returns ErrCancel and never sweeps.
func TestRunGC_PromptCancelDoesNotSweep(t *testing.T) {
	cities := []config.CityConfig{{Name: "Alpha", Overpass: true}}
	var swept bool
	root := &dbtest.MockRootStore{
		EnsureCityFunc: func(context.Context, string, string, string) (int64, error) { return 1, nil },
		ForCityFunc: func(int64) db.Store {
			return &dbtest.MockStore{
				GCScanFunc:  func(context.Context, []string) (*db.GCReport, error) { return reportWithCounts(), nil },
				GCSweepFunc: func(context.Context, []string) (*db.GCReport, error) { swept = true; return &db.GCReport{}, nil },
			}
		},
	}
	ios, _, _, stderr := iostreams.Test()
	ios.SetStdinTTY(true)
	opts := &Options{
		IO:            ios,
		Prompter:      &prompt.Stub{Confirms: []bool{false}},
		RootDB:        rootDBFunc(root),
		ResolveCities: resolveCitiesFunc(cities),
	}
	err := runGC(context.Background(), opts)
	if !errors.Is(err, cmdutil.ErrCancel) {
		t.Errorf("want ErrCancel, got: %v", err)
	}
	if swept {
		t.Error("GCSweep was called after the user declined")
	}
	if !strings.Contains(stderr.String(), "Canceled.") {
		t.Errorf("expected 'Canceled.' on stderr, got: %s", stderr.String())
	}
}
