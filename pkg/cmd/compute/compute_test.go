package compute

import (
	"context"
	"errors"
	"fmt"
	"io"
	"math"
	"strings"
	"testing"

	"github.com/jcrussell/solvent-streets/internal/config"
	"github.com/jcrussell/solvent-streets/internal/db"
	"github.com/jcrussell/solvent-streets/internal/db/dbtest"
	"github.com/jcrussell/solvent-streets/internal/geo"
	"github.com/jcrussell/solvent-streets/internal/resource"
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
	var savedResult *db.ComputeResult
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
		SaveComputeResultFunc: func(_ context.Context, r db.ComputeResult) error {
			savedResult = &r
			return nil
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
	if savedResult == nil {
		t.Error("expected SaveComputeResult to be called")
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
// guard against re-introducing a second BufferFeatures pass on the city
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

	run := func(grid []geo.Hex) []db.HexStat {
		t.Helper()
		var saved []db.HexStat
		store := &dbtest.MockStore{
			GetBoundaryFunc:  func(_ context.Context) (string, error) { return testBoundary, nil },
			ListFeaturesFunc: func(_ context.Context, _ resource.Type) ([]db.Feature, error) { return features, nil },
			SaveHexStatsFunc: func(_ context.Context, s []db.HexStat) error {
				saved = append(saved, s...)
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
			PrebuiltGrid: grid,
		}
		if err := doCompute(ctx, io.Discard, io.Discard, io.Discard, tui.NoopNotifier{}, opts); err != nil {
			t.Fatalf("doCompute: %v", err)
		}
		return saved
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
	// Compare keyed by (HexID, ResourceType) so ordering differences don't
	// matter; assert Area and PctCovered match exactly.
	type key struct {
		id string
		rt resource.Type
	}
	inlineByKey := make(map[key]db.HexStat, len(inline))
	for _, s := range inline {
		inlineByKey[key{s.HexID, s.ResourceType}] = s
	}
	for _, s := range shared {
		want, ok := inlineByKey[key{s.HexID, s.ResourceType}]
		if !ok {
			t.Errorf("shared run produced hex %s (%s) absent from inline run", s.HexID, s.ResourceType)
			continue
		}
		if want.Area != s.Area || want.PctCovered != s.PctCovered {
			t.Errorf("hex %s (%s): inline {area=%v pct=%v} != shared {area=%v pct=%v}",
				s.HexID, s.ResourceType, want.Area, want.PctCovered, s.Area, s.PctCovered)
		}
	}
}

// TestRunCompute_SaveHexStatsErrorFatal pins solvent-streets-yvlv.41: per-hex
// stats are the primary output, so a SaveHexStats failure must make compute
// exit non-zero rather than warn-and-continue.
func TestRunCompute_SaveHexStatsErrorFatal(t *testing.T) {
	saveErr := errors.New("disk full")
	store := &dbtest.MockStore{
		GetBoundaryFunc:  func(_ context.Context) (string, error) { return testBoundary, nil },
		ListFeaturesFunc: roadsFeaturesFunc,
		SaveHexStatsFunc: func(_ context.Context, _ []db.HexStat) error { return saveErr },
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

// TestRunCompute_NoFeaturesSkipsSnapshot pins solvent-streets-yvlv.39: the
// snapshot must not be created when loadResourceFeatures reports no features,
// otherwise `snapshots prune` counts an empty run toward --keep.
func TestRunCompute_NoFeaturesSkipsSnapshot(t *testing.T) {
	snapshotCreated := false
	store := &dbtest.MockStore{
		GetBoundaryFunc:  func(_ context.Context) (string, error) { return testBoundary, nil },
		ListFeaturesFunc: func(_ context.Context, _ resource.Type) ([]db.Feature, error) { return nil, nil },
		CreateSnapshotFunc: func(_ context.Context, _ string) (*db.Snapshot, error) {
			snapshotCreated = true
			return &db.Snapshot{ID: 1}, nil
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
	if snapshotCreated {
		t.Error("CreateSnapshot was called on the no-features path; expected snapshot creation to be skipped")
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
	var savedHexStats, savedResults bool
	store := &dbtest.MockStore{
		GetBoundaryFunc:  func(_ context.Context) (string, error) { return testBoundary, nil },
		ListFeaturesFunc: roadsFeaturesFunc,
		SaveHexStatsFunc: func(_ context.Context, _ []db.HexStat) error {
			savedHexStats = true
			return nil
		},
		SaveComputeResultFunc: func(_ context.Context, _ db.ComputeResult) error {
			savedResults = true
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

	// Pre-cancelled context: ParallelMap dispatches nothing and returns partial
	// (empty) results, then the ctx.Err() guard must fire before any save.
	ctx, cancel := context.WithCancel(context.Background())
	cancel()

	err := doCompute(ctx, io.Discard, io.Discard, io.Discard, tui.NoopNotifier{}, opts)
	if !errors.Is(err, context.Canceled) {
		t.Fatalf("expected context.Canceled, got: %v", err)
	}
	if savedHexStats {
		t.Error("SaveHexStats was called on a cancelled run; truncated stats must not be persisted")
	}
	if savedResults {
		t.Error("SaveComputeResult was called on a cancelled run; partial results must not be persisted")
	}
}

// TestRunCompute_CancelledDeletesSnapshot pins the cancellation-cleanup fix:
// when a snapshot has already been created and the compute is then cancelled,
// doCompute must delete that snapshot (so `snapshots prune` doesn't count an
// empty run toward --keep) while still returning context.Canceled. Deletion
// must use a fresh (non-cancelled) context so it isn't itself cancelled.
func TestRunCompute_CancelledDeletesSnapshot(t *testing.T) {
	const snapID = int64(42)
	var deletedID int64
	var deleteCtxErr error
	store := &dbtest.MockStore{
		GetBoundaryFunc:  func(_ context.Context) (string, error) { return testBoundary, nil },
		ListFeaturesFunc: roadsFeaturesFunc,
		CreateSnapshotFunc: func(_ context.Context, _ string) (*db.Snapshot, error) {
			return &db.Snapshot{ID: snapID}, nil
		},
		DeleteSnapshotFunc: func(ctx context.Context, id int64) (bool, error) {
			deletedID = id
			deleteCtxErr = ctx.Err()
			return true, nil
		},
		SaveHexStatsFunc:      func(_ context.Context, _ []db.HexStat) error { return nil },
		SaveComputeResultFunc: func(_ context.Context, _ db.ComputeResult) error { return nil },
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

	// Pre-cancelled context: CreateSnapshot (mock ignores ctx) still returns a
	// snapshot, then the ctx.Err() guard fires before any save, and the
	// deferred cleanup must delete the snapshot.
	ctx, cancel := context.WithCancel(context.Background())
	cancel()

	err := doCompute(ctx, io.Discard, io.Discard, io.Discard, tui.NoopNotifier{}, opts)
	if !errors.Is(err, context.Canceled) {
		t.Fatalf("expected context.Canceled, got: %v", err)
	}
	if deletedID != snapID {
		t.Errorf("expected DeleteSnapshot(%d) on cancellation, got id %d", snapID, deletedID)
	}
	if deleteCtxErr != nil {
		t.Errorf("DeleteSnapshot must run on a fresh (non-cancelled) context, got ctx.Err() = %v", deleteCtxErr)
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
// the per-hex rows apportion the same 65 across the hexes the lots touch. Lots
// are small squares inside the test boundary.
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
	var results []db.ComputeResult
	var hexRows []db.HexStat
	store := &dbtest.MockStore{
		GetBoundaryFunc: func(_ context.Context) (string, error) { return testBoundary, nil },
		ListFeaturesFunc: func(_ context.Context, _ resource.Type) ([]db.Feature, error) {
			return []db.Feature{
				lot("lotA", "40", -121.7700, 37.6800),
				lot("lotB", "25", -121.7650, 37.6830),
			}, nil
		},
		SaveComputeResultFunc: func(_ context.Context, r db.ComputeResult) error {
			results = append(results, r)
			return nil
		},
		SaveHexStatsFunc: func(_ context.Context, rows []db.HexStat) error {
			hexRows = append(hexRows, rows...)
			return nil
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

	if len(results) != 2 {
		t.Fatalf("expected all + city compute results, got %d: %+v", len(results), results)
	}
	for _, r := range results {
		if r.EstimatedSpaces != 65 {
			t.Errorf("%s EstimatedSpaces = %d, want 65 (capacity 40 + 25)", r.ResourceType, r.EstimatedSpaces)
		}
	}
	// Both passes write hex rows; each pass's rows must apportion the full 65.
	sums := map[resource.Type]float64{}
	for _, h := range hexRows {
		sums[h.ResourceType] += h.EstimatedSpaces
	}
	for rt, sum := range sums {
		if math.Abs(sum-65) > 0.01 {
			t.Errorf("%s hex rows apportion %v spaces, want 65", rt, sum)
		}
	}
	if len(sums) != 2 {
		t.Errorf("expected hex rows for parking and parking:city, got %v", sums)
	}
	if !strings.Contains(stdout.String(), "Spaces:    ~65 (estimated)") {
		t.Errorf("expected the prose summary to report ~65 estimated spaces, got:\n%s", stdout.String())
	}
}

// TestRunCompute_RoadsCarryNoSpaces: the estimate is a parking concept, so a
// roads compute writes 0 spaces everywhere and prints no Spaces line.
func TestRunCompute_RoadsCarryNoSpaces(t *testing.T) {
	var results []db.ComputeResult
	store := &dbtest.MockStore{
		GetBoundaryFunc: func(_ context.Context) (string, error) { return testBoundary, nil },
		ListFeaturesFunc: func(_ context.Context, _ resource.Type) ([]db.Feature, error) {
			return []db.Feature{{
				ID: "r1", ResourceType: resource.TypeRoads,
				Tags:         map[string]string{"highway": "residential", "capacity": "99"},
				GeometryJSON: `{"type":"LineString","coordinates":[[-121.7700,37.6800],[-121.7690,37.6810]]}`,
			}}, nil
		},
		SaveComputeResultFunc: func(_ context.Context, r db.ComputeResult) error {
			results = append(results, r)
			return nil
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
	for _, r := range results {
		if r.EstimatedSpaces != 0 {
			t.Errorf("%s EstimatedSpaces = %d, want 0", r.ResourceType, r.EstimatedSpaces)
		}
	}
	if strings.Contains(stdout.String(), "Spaces:") {
		t.Errorf("roads output must not report spaces:\n%s", stdout.String())
	}
}
