package forecast

import (
	"context"
	"database/sql"
	"errors"
	"math"
	"regexp"
	"strconv"
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
	// year0Area is what the caller passes as `area` (result.TotalArea): the
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
	err := renderBaselineTable(ios, resource.ByType(resource.TypeRoads), year0Area, 75,
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
