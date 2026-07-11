package compute

import (
	"context"
	"errors"
	"fmt"
	"io"
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
func (c *countingSource) BufferFeaturesPaired(ctx context.Context, f []resource.Feature, p *geo.UTMProjector) []resource.BufferedFeature {
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

	err := doCompute(ctx, io.Discard, io.Discard, tui.NoopNotifier{}, opts)
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

	err := doCompute(ctx, io.Discard, io.Discard, tui.NoopNotifier{}, opts)
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
