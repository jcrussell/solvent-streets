package gc

import (
	"context"
	"errors"
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
		name string
		city config.CityConfig
		want []string
	}{
		{"overpass only", config.CityConfig{Overpass: true}, []string{"overpass"}},
		{"overpass+arcgis", config.CityConfig{Overpass: true, ArcGISURL: "https://x"}, []string{"overpass", "arcgis"}},
		{"arcgis only", config.CityConfig{ArcGISURL: "https://x"}, []string{"arcgis"}},
		{"none", config.CityConfig{}, nil},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			got := keepSourcesFor(tc.city)
			if strings.Join(got, ",") != strings.Join(tc.want, ",") {
				t.Errorf("keepSourcesFor = %v, want %v", got, tc.want)
			}
		})
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
