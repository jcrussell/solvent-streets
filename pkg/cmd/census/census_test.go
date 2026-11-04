package census

import (
	"context"
	"database/sql"
	"errors"
	"math"
	"strings"
	"testing"

	"github.com/jcrussell/solvent-streets/internal/config"
	"github.com/jcrussell/solvent-streets/internal/db"
	"github.com/jcrussell/solvent-streets/internal/db/dbtest"
	"github.com/jcrussell/solvent-streets/internal/population"
	"github.com/jcrussell/solvent-streets/internal/resource"
	"github.com/jcrussell/solvent-streets/pkg/cmdutil"
	"github.com/jcrussell/solvent-streets/pkg/iostreams"
)

const testBoundary = `{"type":"Polygon","coordinates":[[[-121.78,37.675],[-121.75,37.675],[-121.75,37.69],[-121.78,37.69],[-121.78,37.675]]]}`

// Two blocks well inside the boundary.
var testBlocks = []db.Feature{
	{ID: "census:1", ResourceType: resource.TypeCensusBlocks, SourceAPI: "tigerweb",
		Tags:         map[string]string{resource.TagPOP100: "120", resource.TagHU100: "50"},
		GeometryJSON: `{"type":"Polygon","coordinates":[[[-121.775,37.678],[-121.770,37.678],[-121.770,37.682],[-121.775,37.682],[-121.775,37.678]]]}`},
	{ID: "census:2", ResourceType: resource.TypeCensusBlocks, SourceAPI: "tigerweb",
		Tags:         map[string]string{resource.TagPOP100: "80", resource.TagHU100: "30"},
		GeometryJSON: `{"type":"Polygon","coordinates":[[[-121.765,37.680],[-121.760,37.680],[-121.760,37.685],[-121.765,37.685],[-121.765,37.680]]]}`},
}

type saved map[string][]db.HexMetric

func fixtureStore(out saved) *dbtest.MockStore {
	snap := int64(7)
	return &dbtest.MockStore{
		GetBoundaryFunc: func(context.Context) (string, error) { return testBoundary, nil },
		ListFeaturesFunc: func(_ context.Context, rt resource.Type) ([]db.Feature, error) {
			if rt == resource.TypeCensusBlocks {
				return testBlocks, nil
			}
			return nil, nil
		},
		LatestComputeResultFunc: func(_ context.Context, rt resource.Type) (*db.ComputeResult, error) {
			if rt == resource.TypeRoads {
				return &db.ComputeResult{ResourceType: rt, SnapshotID: &snap}, nil
			}
			return nil, sql.ErrNoRows
		},
		SaveHexMetricsFunc: func(_ context.Context, metric string, snapshotID *int64, rows []db.HexMetric) error {
			if snapshotID == nil || *snapshotID != 7 {
				return errors.New("metric not attached to the roads snapshot")
			}
			out[metric] = rows
			return nil
		},
	}
}

func testFactory(store db.Store, city *config.CityConfig) *cmdutil.Factory {
	ios, _, _, _ := iostreams.Test()
	return &cmdutil.Factory{
		IOStreams:   ios,
		CityDB:      func() (db.Store, error) { return store, nil },
		CurrentCity: func() (*config.CityConfig, error) { return city, nil },
		Config: func() (*config.Config, error) {
			return &config.Config{ConfigID: "test", Cities: []config.CityConfig{*city}}, nil
		},
	}
}

// TestCensusCompute_StoresPopulationMetrics drives `pvmt census compute`
// over the mock store: all three keys are saved on the roads snapshot and
// the apportioned population sums back to the blocks' 200 residents.
func TestCensusCompute_StoresPopulationMetrics(t *testing.T) {
	out := saved{}
	city := &config.CityConfig{Name: "Livermore, CA", Overpass: true}
	cmd := NewCmdCensus(testFactory(fixtureStore(out), city))
	cmd.SilenceErrors, cmd.SilenceUsage = true, true
	cmd.SetArgs([]string{"compute"})
	if err := cmd.Execute(); err != nil {
		t.Fatal(err)
	}
	for _, k := range []string{population.KeyPopulation, population.KeyHousingUnits, population.KeyDensity} {
		if len(out[k]) == 0 {
			t.Errorf("no rows saved for %s", k)
		}
	}
	var total float64
	for _, r := range out[population.KeyPopulation] {
		total += r.Value
	}
	if math.Abs(total-200) > 1e-6 {
		t.Errorf("apportioned population = %v, want 200", total)
	}
}

// TestCensusCompute_NoBlocksIsNoResults: a city without ingested blocks is an
// empty result, not a failure.
func TestCensusCompute_NoBlocksIsNoResults(t *testing.T) {
	store := fixtureStore(saved{})
	store.ListFeaturesFunc = func(context.Context, resource.Type) ([]db.Feature, error) { return nil, nil }
	city := &config.CityConfig{Name: "Livermore, CA", Overpass: true}
	err := RunComputeForCity(context.Background(), testFactory(store, city), nil)
	if !errors.Is(err, cmdutil.ErrNoResults) {
		t.Errorf("err = %v, want ErrNoResults", err)
	}
}

// TestCensus_OptOutSkips: census = false keeps a city out of the `pvmt all`
// census steps, quietly.
func TestCensus_OptOutSkips(t *testing.T) {
	off := false
	city := &config.CityConfig{Name: "Toronto, ON", Overpass: true, Census: &off}
	store := fixtureStore(saved{})
	store.ListFeaturesFunc = func(context.Context, resource.Type) ([]db.Feature, error) {
		t.Fatal("an opted-out city must not be computed")
		return nil, nil
	}
	f := testFactory(store, city)
	for name, run := range map[string]func() error{
		"ingest":  func() error { return RunIngestForCity(context.Background(), f) },
		"compute": func() error { return RunComputeForCity(context.Background(), f, nil) },
	} {
		if err := run(); !errors.Is(err, cmdutil.ErrNoResults) {
			t.Errorf("%s: err = %v, want ErrNoResults", name, err)
		}
	}
	if !strings.Contains(f.IOStreams.ErrOut.(interface{ String() string }).String(), "census = false") {
		t.Error("the skip should say why")
	}
}
