package forecast

import (
	"context"
	"database/sql"
	"errors"
	"testing"

	"github.com/jcrussell/solvent-streets/internal/config"
	"github.com/jcrussell/solvent-streets/internal/db"
	"github.com/jcrussell/solvent-streets/internal/db/dbtest"
	"github.com/jcrussell/solvent-streets/internal/resource"
	"github.com/jcrussell/solvent-streets/internal/units"
	"github.com/jcrussell/solvent-streets/pkg/cmdutil"
	"github.com/jcrussell/solvent-streets/pkg/iostreams"
)

// TestForecastRow_ExportData_AllFieldsPopulated guards S2: the handwritten
// switch in forecastRow.ExportData is now the JSON contract — a typo
// silently drops a field.
func TestForecastRow_ExportData_AllFieldsPopulated(t *testing.T) {
	rtRoads := resource.TypeRoads
	r := forecastRow{db.ForecastResult{
		ResourceType:  rtRoads,
		Year:          2030,
		PCI:           72.5,
		Area:          1500.0,
		TreatmentCost: 42000.0,
		TreatmentTier: "mill-and-overlay",
	}}
	out := r.ExportData(forecastFields)
	if len(out) != len(forecastFields) {
		t.Fatalf("want %d keys, got %d: %v", len(forecastFields), len(out), out)
	}
	for _, f := range forecastFields {
		if _, ok := out[f]; !ok {
			t.Errorf("missing field %q", f)
		}
	}
	if out["resourceType"] != rtRoads || out["year"] != 2030 {
		t.Errorf("unexpected values: %+v", out)
	}
}

// TestForecastAllResources_PropagatesNonNoRowsError guards yvlv.38: a genuine
// query failure (locked DB, dropped table) must propagate, not fold into the
// "no compute results, skipping" path that previously exited 0.
func TestForecastAllResources_PropagatesNonNoRowsError(t *testing.T) {
	ios, _, _, _ := iostreams.Test()
	opts := &Options{IO: ios}
	fc := &config.ForecastConfig{InitialPCI: 85}

	boom := errors.New("database is locked")
	store := &dbtest.MockStore{
		LatestComputeResultFunc: func(_ context.Context, _ resource.Type) (*db.ComputeResult, error) {
			return nil, boom
		},
	}

	_, err := forecastAllResources(context.Background(), opts, store, fc, 5, nil, units.Metric)
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

// TestForecastAllResources_AllSkippedReturnsNoResults guards yvlv.38: when every
// resource is un-computed (sql.ErrNoRows) the command returns ErrNoResults
// (exit 3), not (nil, nil) → exit 0.
func TestForecastAllResources_AllSkippedReturnsNoResults(t *testing.T) {
	ios, _, _, _ := iostreams.Test()
	opts := &Options{IO: ios}
	fc := &config.ForecastConfig{InitialPCI: 85}

	store := &dbtest.MockStore{
		LatestComputeResultFunc: func(_ context.Context, _ resource.Type) (*db.ComputeResult, error) {
			return nil, sql.ErrNoRows
		},
	}

	results, err := forecastAllResources(context.Background(), opts, store, fc, 5, nil, units.Metric)
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
