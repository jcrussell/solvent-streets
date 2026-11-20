package compute

import (
	"context"
	"errors"
	"fmt"
	"io"
	"maps"
	"math"
	"slices"
	"strings"
	"testing"

	"github.com/jcrussell/solvent-streets/internal/config"
	"github.com/jcrussell/solvent-streets/internal/db"
	"github.com/jcrussell/solvent-streets/internal/db/dbtest"
	"github.com/jcrussell/solvent-streets/internal/forecast"
	"github.com/jcrussell/solvent-streets/internal/geo"
	"github.com/jcrussell/solvent-streets/internal/resource"
	"github.com/jcrussell/solvent-streets/internal/results"
	"github.com/jcrussell/solvent-streets/internal/tui"
	"github.com/jcrussell/solvent-streets/internal/units"
	"github.com/jcrussell/solvent-streets/pkg/cmd/cmdtest"
	"github.com/jcrussell/solvent-streets/pkg/cmdutil"
	"github.com/jcrussell/solvent-streets/pkg/iostreams"
)

var testCity = cmdtest.NewTestCity()

var testCfg = cmdtest.NewTestConfig(testCity)

var testBoundary = `{"type":"Polygon","coordinates":[[[-121.84,37.64],[-121.68,37.64],[-121.68,37.72],[-121.84,37.72],[-121.84,37.64]]]}`

func TestNewCmdCompute_RunFInjection(t *testing.T) {
	ios, _, _, _ := iostreams.Test()
	// Only IOStreams is needed: runF short-circuits before any other Factory field is accessed.
	f := &cmdutil.Factory{IOStreams: ios, UnitSystem: func() units.System { return units.Imperial }}
	rt := &resource.Pavement{}

	called := false
	cmd := NewCmdCompute(f, rt, func(_ context.Context, opts *Options) error {
		called = true
		if opts.ResourceType.Type() != resource.TypeRoads {
			t.Errorf("expected KindRoads, got %v", opts.ResourceType.Type())
		}
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

func TestRunCompute_NoFeatures(t *testing.T) {
	store := &dbtest.MockStore{
		GetBoundaryFunc: func(_ context.Context) (string, error) { return testBoundary, nil },
	}
	ios, _, _, errBuf := iostreams.Test()
	f := &cmdutil.Factory{
		IOStreams:  ios,
		UnitSystem: func() units.System { return units.Imperial },
		CityDB: func() (db.Store, error) {
			return store, nil
		},
		CurrentCity: func() (*config.CityConfig, error) {
			return testCity, nil
		},
		Config: func() (*config.Config, error) {
			return testCfg, nil
		},
	}
	rt := &resource.Pavement{}

	cmd := NewCmdCompute(f, rt, nil)
	cmd.SilenceErrors = true
	cmd.SilenceUsage = true
	cmd.SetArgs([]string{})
	err := cmd.Execute()
	if !errors.Is(err, cmdutil.ErrNoResults) {
		t.Fatalf("expected ErrNoResults, got: %v", err)
	}
	if !strings.Contains(errBuf.String(), "ingest") {
		t.Errorf("stderr should suggest running ingest, got: %s", errBuf.String())
	}
}

func TestRunCompute_Success(t *testing.T) {
	store := &dbtest.MockStore{
		GetBoundaryFunc: func(_ context.Context) (string, error) { return testBoundary, nil },
		ListFeaturesFunc: func(_ context.Context, _ resource.Type) ([]db.Feature, error) {
			return []db.Feature{
				{
					ID:           "test1",
					ResourceType: resource.TypeRoads,
					Name:         "Test Rd",
					Tags:         map[string]string{"highway": "residential"},
					GeometryJSON: `{"type":"LineString","coordinates":[[-121.7700,37.6800],[-121.7690,37.6810]]}`,
				},
			}, nil
		},
	}
	ios, _, stdout, _ := iostreams.Test()
	f := &cmdutil.Factory{
		IOStreams:  ios,
		UnitSystem: func() units.System { return units.Imperial },
		CityDB: func() (db.Store, error) {
			return store, nil
		},
		CurrentCity: func() (*config.CityConfig, error) {
			return testCity, nil
		},
		Config: func() (*config.Config, error) {
			return testCfg, nil
		},
	}
	rt := &resource.Pavement{}

	cmd := NewCmdCompute(f, rt, nil)
	cmd.SilenceErrors = true
	cmd.SilenceUsage = true
	cmd.SetArgs([]string{})
	err := cmd.Execute()
	if err != nil {
		t.Fatal(err)
	}
	output := stdout.String()
	if !strings.Contains(output, "sq ft") {
		t.Errorf("expected area output with imperial units, got: %s", output)
	}
	if _, ok, _ := results.LoadTotal(context.Background(), store, resource.TypeRoads); !ok {
		t.Error("expected the roads city totals to be saved")
	}
}

// TestRunCompute_ChatterToErrOut pins finding 752q: in the plain (non-TTY,
// non-JSON) path, human progress chatter ("Loading...", "Total: N features",
// "Generated N hexes", ...) must land on ErrOut, while the actual result DATA
// (printResults / printCohortBreakdown) stays on Out. A redirected stdout must
// therefore capture only the command's data, not the progress noise.
func TestRunCompute_ChatterToErrOut(t *testing.T) {
	store := &dbtest.MockStore{
		GetBoundaryFunc:  func(_ context.Context) (string, error) { return testBoundary, nil },
		ListFeaturesFunc: roadsFeaturesFunc,
	}
	ios, _, stdout, errBuf := iostreams.Test()
	f := &cmdutil.Factory{
		IOStreams:   ios,
		UnitSystem:  func() units.System { return units.Metric },
		CityDB:      func() (db.Store, error) { return store, nil },
		CurrentCity: func() (*config.CityConfig, error) { return testCity, nil },
		Config:      func() (*config.Config, error) { return testCfg, nil },
	}

	cmd := NewCmdCompute(f, &resource.Pavement{}, nil)
	cmd.SilenceErrors = true
	cmd.SilenceUsage = true
	cmd.SetArgs([]string{})
	if err := cmd.Execute(); err != nil {
		t.Fatalf("compute: %v", err)
	}

	out := stdout.String()
	errStr := errBuf.String()

	// Progress chatter must be on ErrOut and NOT on stdout.
	chatter := []string{
		"Loading",
		"Total:",
		"Generated",
		"hexes with coverage",
	}
	for _, c := range chatter {
		if !strings.Contains(errStr, c) {
			t.Errorf("chatter %q missing from ErrOut:\n%s", c, errStr)
		}
		if strings.Contains(out, c) {
			t.Errorf("chatter %q leaked onto stdout:\n%s", c, out)
		}
	}

	// Result DATA must be on stdout.
	if !strings.Contains(out, "Results") || !strings.Contains(out, "Area:") {
		t.Errorf("expected result data on stdout, got:\n%s", out)
	}
}

// countingSource wraps a resource.Source to count buffer-and-index invocations
// so tests can assert the pipeline doesn't re-buffer the city subset or per
// classification.
type countingSource struct {
	inner       resource.Source
	pairedCalls int
}

func (c *countingSource) Type() resource.Type               { return c.inner.Type() }
func (c *countingSource) OverpassQuery(b [4]float64) string { return c.inner.OverpassQuery(b) }
func (c *countingSource) HasCohorts() bool                  { return c.inner.HasCohorts() }
func (c *countingSource) AsphaltSurfaced() bool             { return c.inner.AsphaltSurfaced() }
func (c *countingSource) BufferFeaturesPaired(ctx context.Context, f []resource.Feature, p geo.Projector) []resource.BufferedFeature {
	c.pairedCalls++
	return c.inner.BufferFeaturesPaired(ctx, f, p)
}

// TestRunCompute_BuffersFeaturesOncePerRun pins the solvent-streets-2nc fix:
// across the all-jurisdiction pass, the city-only pass, and the per-class
// cohort breakdown, every feature is buffered exactly once. Regression
// guard against re-introducing a second BufferFeaturesPaired pass on the city
// subset.
func TestRunCompute_BuffersFeaturesOncePerRun(t *testing.T) {
	store := &dbtest.MockStore{
		GetBoundaryFunc: func(_ context.Context) (string, error) { return testBoundary, nil },
		ListFeaturesFunc: func(_ context.Context, _ resource.Type) ([]db.Feature, error) {
			return []db.Feature{
				{
					ID:           "city1",
					ResourceType: resource.TypeRoads,
					Name:         "City Rd",
					Tags:         map[string]string{"highway": "residential"},
					GeometryJSON: `{"type":"LineString","coordinates":[[-121.7700,37.6800],[-121.7690,37.6810]]}`,
				},
				{
					ID:           "city2",
					ResourceType: resource.TypeRoads,
					Tags:         map[string]string{"highway": "tertiary"},
					GeometryJSON: `{"type":"LineString","coordinates":[[-121.7700,37.6820],[-121.7690,37.6830]]}`,
				},
				{
					ID:           "state1",
					ResourceType: resource.TypeRoads,
					Tags:         map[string]string{"highway": "trunk"},
					GeometryJSON: `{"type":"LineString","coordinates":[[-121.7700,37.6840],[-121.7690,37.6850]]}`,
				},
			}, nil
		},
	}
	ios, _, _, _ := iostreams.Test()
	f := &cmdutil.Factory{
		IOStreams:   ios,
		UnitSystem:  func() units.System { return units.Metric },
		CityDB:      func() (db.Store, error) { return store, nil },
		CurrentCity: func() (*config.CityConfig, error) { return testCity, nil },
		Config:      func() (*config.Config, error) { return testCfg, nil },
	}

	counter := &countingSource{inner: &resource.Pavement{}}
	cmd := NewCmdCompute(f, counter, nil)
	cmd.SilenceErrors = true
	cmd.SilenceUsage = true
	cmd.SetArgs([]string{})
	if err := cmd.Execute(); err != nil {
		t.Fatalf("compute: %v", err)
	}

	if counter.pairedCalls != 1 {
		t.Errorf("BufferFeaturesPaired called %d times; want 1 (all jurisdictions, city subset, and per-class cohorts must share the buffered slice)",
			counter.pairedCalls)
	}
}

// roadsFeaturesFunc returns a small set of road features (city + state) that
// buffer to non-empty geometries inside testBoundary, used by the fixtures
// below that need real hex stats and cohorts.
func roadsFeaturesFunc(_ context.Context, _ resource.Type) ([]db.Feature, error) {
	return []db.Feature{
		{
			ID:           "city1",
			ResourceType: resource.TypeRoads,
			Tags:         map[string]string{"highway": "residential"},
			GeometryJSON: `{"type":"LineString","coordinates":[[-121.7700,37.6800],[-121.7690,37.6810]]}`,
		},
		{
			ID:           "state1",
			ResourceType: resource.TypeRoads,
			Tags:         map[string]string{"highway": "trunk"},
			GeometryJSON: `{"type":"LineString","coordinates":[[-121.7700,37.6840],[-121.7690,37.6850]]}`,
		},
	}, nil
}

// TestRunCompute_PrebuiltGridMatchesInline pins the 7ou7.4 grid-sharing
// refactor: computing a resource with an injected PrebuiltGrid must produce
// byte-identical saved hex stats to computing it with the grid built inline.
// This is the equivalence guardrail for `all compute` reusing one clipped grid
// across the per-resource passes instead of rebuilding it each time.
func TestRunCompute_PrebuiltGridMatchesInline(t *testing.T) {
	ctx := context.Background()

	// Several short roads scattered across testBoundary so multiple hexes get
	// coverage — the more covered hexes, the more the comparison bites.
	features := []db.Feature{
		{ID: "r1", ResourceType: resource.TypeRoads, Tags: map[string]string{"highway": "residential"},
			GeometryJSON: `{"type":"LineString","coordinates":[[-121.7700,37.6800],[-121.7660,37.6820]]}`},
		{ID: "r2", ResourceType: resource.TypeRoads, Tags: map[string]string{"highway": "tertiary"},
			GeometryJSON: `{"type":"LineString","coordinates":[[-121.7500,37.6900],[-121.7450,37.6930]]}`},
		{ID: "r3", ResourceType: resource.TypeRoads, Tags: map[string]string{"highway": "trunk"},
			GeometryJSON: `{"type":"LineString","coordinates":[[-121.7800,37.7000],[-121.7750,37.7010]]}`},
	}

	run := func(grid []geo.Hex) []db.HexMetric {
		t.Helper()
		store := &dbtest.MockStore{
			GetBoundaryFunc:  func(_ context.Context) (string, error) { return testBoundary, nil },
			ListFeaturesFunc: func(_ context.Context, _ resource.Type) ([]db.Feature, error) { return features, nil },
		}
		ios, _, _, _ := iostreams.Test()
		opts := &Options{
			IO:           ios,
			UnitSystem:   func() units.System { return units.Metric },
			CityDB:       func() (db.Store, error) { return store, nil },
			CurrentCity:  func() (*config.CityConfig, error) { return testCity, nil },
			Config:       func() (*config.Config, error) { return testCfg, nil },
			ResourceType: &resource.Pavement{},
			PrebuiltGrid: grid,
		}
		if err := doCompute(ctx, io.Discard, io.Discard, io.Discard, tui.NoopNotifier{}, opts); err != nil {
			t.Fatalf("doCompute: %v", err)
		}
		return store.HexMetrics
	}

	// Build the shared grid exactly as BuildCityGrid would for this city.
	bstore := &dbtest.MockStore{GetBoundaryFunc: func(_ context.Context) (string, error) { return testBoundary, nil }}
	boundaryGJSON, bbox, proj, err := LoadBoundary(ctx, bstore, testCity)
	if err != nil {
		t.Fatalf("LoadBoundary: %v", err)
	}
	grid := BuildClippedHexGrid(ctx, testCfg, testCity, proj, bbox, boundaryGJSON, io.Discard)
	if len(grid) == 0 {
		t.Fatal("shared grid is empty; fixture would not exercise the comparison")
	}

	inline := run(nil)
	shared := run(grid)

	if len(inline) == 0 {
		t.Fatal("inline run saved no hex stats; fixture produced no coverage")
	}
	if len(inline) != len(shared) {
		t.Fatalf("hex stat count differs: inline=%d shared=%d", len(inline), len(shared))
	}
	// Compare keyed by (HexID, Metric, Subject) so ordering differences
	// don't matter; assert each value (area, coverage) matches exactly.
	type key struct{ id, metric, subject string }
	inlineByKey := make(map[key]float64, len(inline))
	for _, s := range inline {
		inlineByKey[key{s.HexID, s.Metric, s.Subject}] = s.Value
	}
	for _, s := range shared {
		want, ok := inlineByKey[key{s.HexID, s.Metric, s.Subject}]
		if !ok {
			t.Errorf("shared run produced hex %s %s (%s) absent from inline run", s.HexID, s.Metric, s.Subject)
			continue
		}
		if want != s.Value {
			t.Errorf("hex %s %s (%s): inline %v != shared %v", s.HexID, s.Metric, s.Subject, want, s.Value)
		}
	}
}

// TestRunCompute_SaveHexStatsErrorFatal pins solvent-streets-yvlv.41: per-hex
// stats are the primary output, so a SaveHexStats failure must make compute
// exit non-zero rather than warn-and-continue.
func TestRunCompute_SaveHexStatsErrorFatal(t *testing.T) {
	saveErr := errors.New("disk full")
	store := &dbtest.MockStore{
		GetBoundaryFunc:    func(_ context.Context) (string, error) { return testBoundary, nil },
		ListFeaturesFunc:   roadsFeaturesFunc,
		SaveHexMetricsFunc: func(_ context.Context, _ int64, _ []db.HexMetric) error { return saveErr },
	}
	ios, _, _, _ := iostreams.Test()
	f := &cmdutil.Factory{
		IOStreams:   ios,
		UnitSystem:  func() units.System { return units.Metric },
		CityDB:      func() (db.Store, error) { return store, nil },
		CurrentCity: func() (*config.CityConfig, error) { return testCity, nil },
		Config:      func() (*config.Config, error) { return testCfg, nil },
	}

	cmd := NewCmdCompute(f, &resource.Pavement{}, nil)
	cmd.SilenceErrors = true
	cmd.SilenceUsage = true
	cmd.SetArgs([]string{})
	err := cmd.Execute()
	if err == nil {
		t.Fatal("expected non-nil error when SaveHexStats fails")
	}
	if !errors.Is(err, saveErr) {
		t.Errorf("expected wrapped SaveHexStats error, got: %v", err)
	}
}

// TestRunCompute_CityOnlySuppressesAllCohortBreakdown pins
// solvent-streets-yvlv.42: with --city-only, the all-jurisdiction "Cohort
// breakdown" summary must be suppressed to match the gated printResults.
func TestRunCompute_CityOnlySuppressesAllCohortBreakdown(t *testing.T) {
	store := &dbtest.MockStore{
		GetBoundaryFunc:  func(_ context.Context) (string, error) { return testBoundary, nil },
		ListFeaturesFunc: roadsFeaturesFunc,
	}
	ios, _, stdout, _ := iostreams.Test()
	f := &cmdutil.Factory{
		IOStreams:   ios,
		UnitSystem:  func() units.System { return units.Metric },
		CityDB:      func() (db.Store, error) { return store, nil },
		CurrentCity: func() (*config.CityConfig, error) { return testCity, nil },
		Config:      func() (*config.Config, error) { return testCfg, nil },
	}

	cmd := NewCmdCompute(f, &resource.Pavement{}, nil)
	cmd.SilenceErrors = true
	cmd.SilenceUsage = true
	cmd.SetArgs([]string{"--city-only"})
	if err := cmd.Execute(); err != nil {
		t.Fatalf("compute: %v", err)
	}
	out := stdout.String()
	// "\nCohort breakdown:" is the all-jurisdiction header; "City cohort
	// breakdown:" is the city one (not prefixed by a bare newline+"Cohort").
	if strings.Contains(out, "\nCohort breakdown:") {
		t.Errorf("--city-only should suppress the all-jurisdiction cohort breakdown, got:\n%s", out)
	}
}

// TestRunCompute_NoFeaturesDropsThePart pins solvent-streets-yvlv.39 under
// runs and snapshots: when loadResourceFeatures reports no features, no run is
// created, and the only thing committed is the removal of the resource's part
// (a no-op in the store when the city never had it), so coverage from
// features that are gone stops being published.
func TestRunCompute_NoFeaturesDropsThePart(t *testing.T) {
	runCreated := false
	var committed map[string]int64
	store := &dbtest.MockStore{
		GetBoundaryFunc:  func(_ context.Context) (string, error) { return testBoundary, nil },
		ListFeaturesFunc: func(_ context.Context, _ resource.Type) ([]db.Feature, error) { return nil, nil },
		CreateRunFunc: func(_ context.Context, producer, hash string, _ map[string]string) (*db.Run, error) {
			runCreated = true
			return &db.Run{ID: 1, Producer: producer, ConfigHash: hash}, nil
		},
		CommitLatestFunc: func(_ context.Context, _ string, parts map[string]int64) error {
			committed = parts
			return nil
		},
	}
	ios, _, _, _ := iostreams.Test()
	f := &cmdutil.Factory{
		IOStreams:   ios,
		UnitSystem:  func() units.System { return units.Metric },
		CityDB:      func() (db.Store, error) { return store, nil },
		CurrentCity: func() (*config.CityConfig, error) { return testCity, nil },
		Config:      func() (*config.Config, error) { return testCfg, nil },
	}

	cmd := NewCmdCompute(f, &resource.Pavement{}, nil)
	cmd.SilenceErrors = true
	cmd.SilenceUsage = true
	cmd.SetArgs([]string{})
	err := cmd.Execute()
	if !errors.Is(err, cmdutil.ErrNoResults) {
		t.Fatalf("expected ErrNoResults, got: %v", err)
	}
	if runCreated {
		t.Error("no-features path created a run")
	}
	want := map[string]int64{"roads": 0}
	if !maps.Equal(committed, want) {
		t.Errorf("committed %v, want only the drop %v", committed, want)
	}
}

// TestRunCompute_InvalidBoundaryWarns pins solvent-streets-yvlv.40: when the
// stored boundary fails to parse into a projected geometry, compute must warn
// (naming the city) instead of silently computing over the full bbox.
func TestRunCompute_InvalidBoundaryWarns(t *testing.T) {
	// A GeoJSON whose coordinates span a valid bbox (so BBoxFromGeoJSON
	// succeeds and loadBoundary passes) but whose only polygon is a
	// zero-area collinear ring, so GeoJSONToProjectedGeometry yields "no
	// valid polygons" at the clip stage.
	const badGeom = `{"type":"MultiPolygon","coordinates":[[[[-121.84,37.64],[-121.68,37.72],[-121.84,37.64]]]]}`
	store := &dbtest.MockStore{
		GetBoundaryFunc:  func(_ context.Context) (string, error) { return badGeom, nil },
		ListFeaturesFunc: roadsFeaturesFunc,
	}
	ios, _, _, errBuf := iostreams.Test()
	f := &cmdutil.Factory{
		IOStreams:   ios,
		UnitSystem:  func() units.System { return units.Metric },
		CityDB:      func() (db.Store, error) { return store, nil },
		CurrentCity: func() (*config.CityConfig, error) { return testCity, nil },
		Config:      func() (*config.Config, error) { return testCfg, nil },
	}

	cmd := NewCmdCompute(f, &resource.Pavement{}, nil)
	cmd.SilenceErrors = true
	cmd.SilenceUsage = true
	cmd.SetArgs([]string{})
	if err := cmd.Execute(); err != nil {
		t.Fatalf("compute: %v", err)
	}
	stderr := errBuf.String()
	if !strings.Contains(stderr, "boundary") || !strings.Contains(stderr, testCity.Name) {
		t.Errorf("expected a boundary warning naming the city, got stderr:\n%s", stderr)
	}
}

// TestRunCompute_CancelledDoesNotSave pins solvent-streets-yvlv.43: a cancelled
// context must make the compute path return context.Canceled and must NOT
// persist truncated hex stats / compute results under a complete-looking
// snapshot. geo.ParallelMap returns partial results with no error on cancel,
// so the ctx.Err() guard between compute and persistence is the load-bearing
// fix under test.
func TestRunCompute_CancelledDoesNotSave(t *testing.T) {
	store := &dbtest.MockStore{
		GetBoundaryFunc:  func(_ context.Context) (string, error) { return testBoundary, nil },
		ListFeaturesFunc: roadsFeaturesFunc,
	}
	ios, _, _, _ := iostreams.Test()
	opts := &Options{
		IO:           ios,
		UnitSystem:   func() units.System { return units.Metric },
		CityDB:       func() (db.Store, error) { return store, nil },
		CurrentCity:  func() (*config.CityConfig, error) { return testCity, nil },
		Config:       func() (*config.Config, error) { return testCfg, nil },
		ResourceType: &resource.Pavement{},
	}

	// Pre-cancelled context: ParallelMap dispatches nothing and returns partial
	// (empty) results, then the ctx.Err() guard must fire before any save.
	ctx, cancel := context.WithCancel(context.Background())
	cancel()

	err := doCompute(ctx, io.Discard, io.Discard, io.Discard, tui.NoopNotifier{}, opts)
	if !errors.Is(err, context.Canceled) {
		t.Fatalf("expected context.Canceled, got: %v", err)
	}
	if len(store.HexMetrics) > 0 {
		t.Error("hex rows were saved on a cancelled run; truncated stats must not be persisted")
	}
	if len(store.CityMetrics) > 0 {
		t.Error("city totals were saved on a cancelled run; partial results must not be persisted")
	}
}

// TestRunCompute_CancelledDiscardsRun pins the cancellation cleanup: when a
// run has already been created and the compute is then cancelled, doCompute
// discards that run and commits no snapshot, while still returning
// context.Canceled. The discard must use a fresh (non-cancelled) context so it
// isn't itself cancelled.
func TestRunCompute_CancelledDiscardsRun(t *testing.T) {
	const runID = int64(42)
	var discardedID int64
	var discardCtxErr error
	committed := false
	store := &dbtest.MockStore{
		GetBoundaryFunc:  func(_ context.Context) (string, error) { return testBoundary, nil },
		ListFeaturesFunc: roadsFeaturesFunc,
		CreateRunFunc: func(_ context.Context, producer, hash string, _ map[string]string) (*db.Run, error) {
			return &db.Run{ID: runID, Producer: producer, ConfigHash: hash}, nil
		},
		DiscardRunFunc: func(ctx context.Context, id int64) error {
			discardedID = id
			discardCtxErr = ctx.Err()
			return nil
		},
		CommitLatestFunc: func(context.Context, string, map[string]int64) error {
			committed = true
			return nil
		},
	}
	ios, _, _, _ := iostreams.Test()
	opts := &Options{
		IO:           ios,
		UnitSystem:   func() units.System { return units.Metric },
		CityDB:       func() (db.Store, error) { return store, nil },
		CurrentCity:  func() (*config.CityConfig, error) { return testCity, nil },
		Config:       func() (*config.Config, error) { return testCfg, nil },
		ResourceType: &resource.Pavement{},
	}

	// Pre-cancelled context: CreateRun (mock ignores ctx) still returns a run,
	// then the ctx.Err() guard fires before any save, and the draft must
	// discard the run.
	ctx, cancel := context.WithCancel(context.Background())
	cancel()

	err := doCompute(ctx, io.Discard, io.Discard, io.Discard, tui.NoopNotifier{}, opts)
	if !errors.Is(err, context.Canceled) {
		t.Fatalf("expected context.Canceled, got: %v", err)
	}
	if discardedID != runID {
		t.Errorf("expected DiscardRun(%d) on cancellation, got id %d", runID, discardedID)
	}
	if discardCtxErr != nil {
		t.Errorf("DiscardRun must run on a fresh (non-cancelled) context, got ctx.Err() = %v", discardCtxErr)
	}
	if committed {
		t.Error("a cancelled compute committed a snapshot")
	}
}

func TestRunCompute_DBError(t *testing.T) {
	ios, _, _, _ := iostreams.Test()
	f := &cmdutil.Factory{
		IOStreams:  ios,
		UnitSystem: func() units.System { return units.Imperial },
		Config: func() (*config.Config, error) {
			return testCfg, nil
		},
		CurrentCity: func() (*config.CityConfig, error) {
			return testCity, nil
		},
		CityDB: func() (db.Store, error) {
			return nil, fmt.Errorf("db connection failed")
		},
	}
	rt := &resource.Pavement{}

	cmd := NewCmdCompute(f, rt, nil)
	cmd.SilenceErrors = true
	cmd.SilenceUsage = true
	cmd.SetArgs([]string{})
	err := cmd.Execute()
	if err == nil {
		t.Fatal("expected error for DB failure")
	}
	if !strings.Contains(err.Error(), "database") {
		t.Errorf("expected database error, got: %s", err.Error())
	}
}

// TestRunCompute_ParkingEstimatesSpaces pins the parking-space estimate end to
// end through the compute command: two lots with capacity tags (40 and 25)
// produce a city total of 65 on both the all-jurisdiction and city rows, and
// the per-hex space metrics (one key per scope, under the same run the
// coverage rows carry) apportion the same 65 across the hexes
// the lots touch. Lots are small squares inside the test boundary.
func TestRunCompute_ParkingEstimatesSpaces(t *testing.T) {
	lot := func(id, capacity string, lon, lat float64) db.Feature {
		d := 0.0004
		return db.Feature{
			ID:           id,
			ResourceType: resource.TypeParking,
			Tags:         map[string]string{"amenity": "parking", "capacity": capacity},
			GeometryJSON: fmt.Sprintf(`{"type":"Polygon","coordinates":[[[%f,%f],[%f,%f],[%f,%f],[%f,%f],[%f,%f]]]}`,
				lon, lat, lon+d, lat, lon+d, lat+d, lon, lat+d, lon, lat),
		}
	}
	store := &dbtest.MockStore{
		GetBoundaryFunc: func(_ context.Context) (string, error) { return testBoundary, nil },
		ListFeaturesFunc: func(_ context.Context, _ resource.Type) ([]db.Feature, error) {
			return []db.Feature{
				lot("lotA", "40", -121.7700, 37.6800),
				lot("lotB", "25", -121.7650, 37.6830),
			}, nil
		},
	}
	ios, _, stdout, _ := iostreams.Test()
	f := &cmdutil.Factory{
		IOStreams:   ios,
		UnitSystem:  func() units.System { return units.Metric },
		CityDB:      func() (db.Store, error) { return store, nil },
		CurrentCity: func() (*config.CityConfig, error) { return testCity, nil },
		Config:      func() (*config.Config, error) { return testCfg, nil },
	}
	cmd := NewCmdCompute(f, &resource.Parking{}, nil)
	cmd.SilenceErrors = true
	cmd.SilenceUsage = true
	cmd.SetArgs([]string{})
	if err := cmd.Execute(); err != nil {
		t.Fatal(err)
	}

	ctx := context.Background()
	parkingCity := resource.TypeParking.With(resource.ScopeCity)
	totals, err := results.LoadTotals(ctx, store, []resource.Type{resource.TypeParking, parkingCity})
	if err != nil || len(totals) != 2 {
		t.Fatalf("expected all + city totals, got %+v (%v)", totals, err)
	}
	for rt, r := range totals {
		if r.Spaces != 65 {
			t.Errorf("%s Spaces = %d, want 65 (capacity 40 + 25)", rt, r.Spaces)
		}
	}
	// Both passes write per-hex spaces; each must apportion the full 65 and
	// sit under the same run as the coverage rows export joins them to.
	var coverageRun int64
	spaces := map[string][]db.HexMetric{}
	for _, m := range store.HexMetrics {
		switch m.Metric {
		case resource.MetricPavedArea:
			coverageRun = m.RunID
		case resource.MetricParkingSpaces:
			spaces[m.Subject] = append(spaces[m.Subject], m)
		}
	}
	if coverageRun == 0 {
		t.Fatal("expected per-hex coverage rows")
	}
	for _, subject := range []string{"parking", "parking:city"} {
		rows, ok := spaces[subject]
		if !ok {
			t.Errorf("no %s parking spaces saved; got %v", subject, slices.Collect(maps.Keys(spaces)))
			continue
		}
		var sum float64
		for _, r := range rows {
			if r.Value <= 0 {
				t.Errorf("%s stored a non-positive row %+v", subject, r)
			}
			if r.RunID != coverageRun {
				t.Errorf("%s spaces run = %d, want the coverage run %d", subject, r.RunID, coverageRun)
			}
			sum += r.Value
		}
		if math.Abs(sum-65) > 0.01 {
			t.Errorf("%s apportions %v spaces, want 65", subject, sum)
		}
	}
	if len(spaces) != 2 {
		t.Errorf("expected exactly the two parking space subjects, got %v", slices.Collect(maps.Keys(spaces)))
	}
	if !strings.Contains(stdout.String(), "Spaces:    ~65 (estimated)") {
		t.Errorf("expected the prose summary to report ~65 estimated spaces, got:\n%s", stdout.String())
	}
}

// TestRunCompute_RoadsCarryNoSpaces: the estimate is a parking concept, so a
// roads compute writes no spaces anywhere and prints no Spaces line.
func TestRunCompute_RoadsCarryNoSpaces(t *testing.T) {
	store := &dbtest.MockStore{
		GetBoundaryFunc: func(_ context.Context) (string, error) { return testBoundary, nil },
		ListFeaturesFunc: func(_ context.Context, _ resource.Type) ([]db.Feature, error) {
			return []db.Feature{{
				ID: "r1", ResourceType: resource.TypeRoads,
				Tags:         map[string]string{"highway": "residential", "capacity": "99"},
				GeometryJSON: `{"type":"LineString","coordinates":[[-121.7700,37.6800],[-121.7690,37.6810]]}`,
			}}, nil
		},
	}
	ios, _, stdout, _ := iostreams.Test()
	f := &cmdutil.Factory{
		IOStreams:   ios,
		UnitSystem:  func() units.System { return units.Metric },
		CityDB:      func() (db.Store, error) { return store, nil },
		CurrentCity: func() (*config.CityConfig, error) { return testCity, nil },
		Config:      func() (*config.Config, error) { return testCfg, nil },
	}
	cmd := NewCmdCompute(f, &resource.Pavement{}, nil)
	cmd.SilenceErrors = true
	cmd.SilenceUsage = true
	cmd.SetArgs([]string{})
	if err := cmd.Execute(); err != nil {
		t.Fatal(err)
	}
	if len(store.CityMetrics) == 0 {
		t.Fatal("roads compute saved no city totals")
	}
	for _, m := range store.CityMetrics {
		if m.Metric == resource.MetricParkingSpaces {
			t.Errorf("roads compute saved a city parking_spaces row %+v", m)
		}
	}
	for _, m := range store.HexMetrics {
		if m.Metric == resource.MetricParkingSpaces {
			t.Errorf("roads compute saved a per-hex parking_spaces row %+v", m)
		}
	}
	if strings.Contains(stdout.String(), "Spaces:") {
		t.Errorf("roads output must not report spaces:\n%s", stdout.String())
	}
}

// TestRunCompute_SaveSpaceMetricsErrorFatal: a failed per-hex save (coverage
// and spaces go in one save) fails the parking compute, rather than leaving
// coverage published without its spaces.
func TestRunCompute_SaveSpaceMetricsErrorFatal(t *testing.T) {
	saveErr := errors.New("disk full")
	store := &dbtest.MockStore{
		GetBoundaryFunc: func(_ context.Context) (string, error) { return testBoundary, nil },
		ListFeaturesFunc: func(_ context.Context, _ resource.Type) ([]db.Feature, error) {
			return []db.Feature{{
				ID: "lot", ResourceType: resource.TypeParking,
				Tags:         map[string]string{"amenity": "parking", "capacity": "40"},
				GeometryJSON: `{"type":"Polygon","coordinates":[[[-121.7700,37.6800],[-121.7696,37.6800],[-121.7696,37.6804],[-121.7700,37.6804],[-121.7700,37.6800]]]}`,
			}}, nil
		},
		SaveHexMetricsFunc: func(_ context.Context, _ int64, _ []db.HexMetric) error { return saveErr },
	}
	ios, _, _, _ := iostreams.Test()
	f := &cmdutil.Factory{
		IOStreams:   ios,
		UnitSystem:  func() units.System { return units.Metric },
		CityDB:      func() (db.Store, error) { return store, nil },
		CurrentCity: func() (*config.CityConfig, error) { return testCity, nil },
		Config:      func() (*config.Config, error) { return testCfg, nil },
	}
	cmd := NewCmdCompute(f, &resource.Parking{}, nil)
	cmd.SilenceErrors = true
	cmd.SilenceUsage = true
	cmd.SetArgs([]string{})
	err := cmd.Execute()
	if !errors.Is(err, saveErr) {
		t.Errorf("expected wrapped SaveHexMetrics error, got: %v", err)
	}
}

// TestRunCompute_CommitsOnlyItsOwnPart: a standalone resource compute
// commits ONE snapshot swapping in exactly its own part, under the run its
// rows were saved with. No forecast is stored or rerun: every reader
// simulates the forecast from the compute's cohort stats on demand.
func TestRunCompute_CommitsOnlyItsOwnPart(t *testing.T) {
	producers := map[int64]string{}
	var next int64
	var commits []map[string]int64
	store := &dbtest.MockStore{
		GetBoundaryFunc:  func(_ context.Context) (string, error) { return testBoundary, nil },
		ListFeaturesFunc: roadsFeaturesFunc,
		CreateRunFunc: func(_ context.Context, producer, hash string, _ map[string]string) (*db.Run, error) {
			next++
			producers[next] = producer
			return &db.Run{ID: next, Producer: producer, ConfigHash: hash}, nil
		},
		CommitLatestFunc: func(_ context.Context, _ string, parts map[string]int64) error {
			commits = append(commits, parts)
			return nil
		},
	}
	ios, _, _, stderr := iostreams.Test()
	f := &cmdutil.Factory{
		IOStreams:   ios,
		UnitSystem:  func() units.System { return units.Metric },
		CityDB:      func() (db.Store, error) { return store, nil },
		CurrentCity: func() (*config.CityConfig, error) { return testCity, nil },
		Config:      func() (*config.Config, error) { return testCfg, nil },
	}
	cmd := NewCmdCompute(f, &resource.Pavement{}, nil)
	cmd.SilenceErrors = true
	cmd.SilenceUsage = true
	cmd.SetArgs([]string{})
	if err := cmd.Execute(); err != nil {
		t.Fatalf("compute: %v\nstderr:\n%s", err, stderr.String())
	}

	if len(commits) != 1 {
		t.Fatalf("expected exactly one committed snapshot, got %d: %v", len(commits), commits)
	}
	parts := commits[0]
	roadsRun := parts[string(resource.TypeRoads)]
	if want := map[string]int64{string(resource.TypeRoads): roadsRun}; roadsRun == 0 || !maps.Equal(parts, want) {
		t.Fatalf("snapshot parts = %v, want exactly roads", parts)
	}
	if len(producers) != 1 || producers[roadsRun] != string(resource.TypeRoads) {
		t.Errorf("runs created = %v, want only the roads run", producers)
	}
	if tot, _, _ := results.LoadTotal(context.Background(), store, resource.TypeRoads); tot.RunID != roadsRun {
		t.Errorf("roads totals saved under run %d, committed roads run is %d", tot.RunID, roadsRun)
	}
}

// TestRunCompute_RecordsInputs: the roads run records the digests of the
// boundary and the roads it read, and is committed into the latest compute
// under the city's hash.
func TestRunCompute_RecordsInputs(t *testing.T) {
	var hash string
	store := &dbtest.MockStore{
		GetBoundaryFunc:  func(_ context.Context) (string, error) { return testBoundary, nil },
		ListFeaturesFunc: roadsFeaturesFunc,
		Digests:          map[string]string{"boundary": "b", "roads": "r", "parking": "p"},
		CommitLatestFunc: func(_ context.Context, h string, _ map[string]int64) error {
			hash = h
			return nil
		},
	}
	ios, _, _, stderr := iostreams.Test()
	f := &cmdutil.Factory{
		IOStreams:   ios,
		UnitSystem:  func() units.System { return units.Metric },
		CityDB:      func() (db.Store, error) { return store, nil },
		CurrentCity: func() (*config.CityConfig, error) { return testCity, nil },
		Config:      func() (*config.Config, error) { return testCfg, nil },
	}
	cmd := NewCmdCompute(f, &resource.Pavement{}, nil)
	cmd.SilenceErrors, cmd.SilenceUsage = true, true
	cmd.SetArgs([]string{})
	if err := cmd.Execute(); err != nil {
		t.Fatalf("compute: %v\nstderr:\n%s", err, stderr.String())
	}
	want := map[string]string{"boundary": "b", "roads": "r"}
	if len(store.RunInputs) != 1 || !maps.Equal(store.RunInputs[1], want) {
		t.Errorf("run inputs = %v, want run 1 with %v", store.RunInputs, want)
	}
	if want := testCfg.CityHash(testCity); hash != want {
		t.Errorf("committed under hash %q, want %q", hash, want)
	}
}

// TestRunCompute_StoresRoadDecayBlend: a roads compute stores, per
// city-jurisdiction road hex, the default-rate blend of its road classes'
// decay rates (resource.MetricDefaultDecayRate) under roads:city, in the same
// run as the coverage, on exactly the hexes roads:city covers. Two parallel
// roads of different classes share their hexes, so those hexes blend strictly
// between the two class rates; the exact weighting is pinned by
// resource.TestDefaultDecayBlends.
func TestRunCompute_StoresRoadDecayBlend(t *testing.T) {
	road := func(id, highway string, lat float64) db.Feature {
		return db.Feature{
			ID: id, ResourceType: resource.TypeRoads,
			Tags:         map[string]string{"highway": highway},
			GeometryJSON: fmt.Sprintf(`{"type":"LineString","coordinates":[[-121.7800,%f],[-121.7600,%f]]}`, lat, lat),
		}
	}
	store := &dbtest.MockStore{
		GetBoundaryFunc: func(_ context.Context) (string, error) { return testBoundary, nil },
		ListFeaturesFunc: func(_ context.Context, _ resource.Type) ([]db.Feature, error) {
			return []db.Feature{road("p", "primary", 37.6800), road("r", "residential", 37.6802)}, nil
		},
	}
	ios, _, _, _ := iostreams.Test()
	f := &cmdutil.Factory{
		IOStreams:   ios,
		UnitSystem:  func() units.System { return units.Metric },
		CityDB:      func() (db.Store, error) { return store, nil },
		CurrentCity: func() (*config.CityConfig, error) { return testCity, nil },
		Config:      func() (*config.Config, error) { return testCfg, nil },
	}
	cmd := NewCmdCompute(f, &resource.Pavement{}, nil)
	cmd.SilenceErrors = true
	cmd.SilenceUsage = true
	cmd.SetArgs([]string{})
	if err := cmd.Execute(); err != nil {
		t.Fatal(err)
	}

	primary, residential := forecast.DecayRateForClass("primary"), forecast.DecayRateForClass("residential")
	covered := map[string]int64{}
	k0 := map[string]float64{}
	for _, m := range store.HexMetrics {
		switch {
		case m.Metric == resource.MetricPavedArea && m.Subject == "roads:city":
			covered[m.HexID] = m.RunID
		case m.Metric == resource.MetricDefaultDecayRate:
			if m.Subject != "roads:city" {
				t.Errorf("decay blend stored under %q, want roads:city only", m.Subject)
			}
			k0[m.HexID] = m.Value
			if run, ok := covered[m.HexID]; ok && run != m.RunID {
				t.Errorf("hex %s decay blend run %d, coverage run %d", m.HexID, m.RunID, run)
			}
		}
	}
	if len(covered) == 0 {
		t.Fatal("no roads:city coverage stored")
	}
	if !slices.Equal(slices.Sorted(maps.Keys(k0)), slices.Sorted(maps.Keys(covered))) {
		t.Errorf("decay blend hexes %v, want the roads:city hexes %v", slices.Sorted(maps.Keys(k0)), slices.Sorted(maps.Keys(covered)))
	}
	mixed := 0
	for id, k := range k0 {
		switch {
		case k > primary && k < residential:
			mixed++
		case k != primary && k != residential:
			t.Errorf("hex %s k0 = %v, outside [%v, %v]", id, k, primary, residential)
		}
	}
	if mixed == 0 {
		t.Errorf("no hex blends the two classes: %v", k0)
	}
}
