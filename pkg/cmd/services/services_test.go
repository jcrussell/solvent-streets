package services

import (
	"context"
	"database/sql"
	"errors"
	"strings"
	"testing"

	"github.com/jcrussell/solvent-streets/internal/config"
	"github.com/jcrussell/solvent-streets/internal/db"
	"github.com/jcrussell/solvent-streets/internal/db/dbtest"
	"github.com/jcrussell/solvent-streets/internal/resource"
	"github.com/jcrussell/solvent-streets/internal/services"
	"github.com/jcrussell/solvent-streets/pkg/cmdutil"
	"github.com/jcrussell/solvent-streets/pkg/iostreams"
)

const testBoundary = `{"type":"Polygon","coordinates":[[[-121.78,37.675],[-121.75,37.675],[-121.75,37.69],[-121.78,37.69],[-121.78,37.675]]]}`

var testCity = &config.CityConfig{Name: "Livermore, CA", Overpass: true}

func testCfg() *config.Config {
	return &config.Config{ConfigID: "test", Cities: []config.CityConfig{*testCity}}
}

// fixtureStore holds one straight road (two segments) and one fire station
// beside it, with a roads compute snapshot to attach the metric to.
func fixtureStore(saved *[]db.HexMetric) *dbtest.MockStore {
	snap := int64(7)
	return &dbtest.MockStore{
		GetBoundaryFunc: func(context.Context) (string, error) { return testBoundary, nil },
		ListFeaturesFunc: func(_ context.Context, rt resource.Type) ([]db.Feature, error) {
			if rt == resource.TypeRoads {
				return []db.Feature{{
					ID: "osm:way:1", ResourceType: rt, SourceAPI: "overpass",
					Tags:         map[string]string{"highway": "residential"},
					GeometryJSON: `{"type":"LineString","coordinates":[[-121.7750000,37.6800000],[-121.7650000,37.6800000],[-121.7550000,37.6800000]]}`,
				}}, nil
			}
			if rt == resource.FacilityType("fire") {
				return []db.Feature{{
					ID: "osm:node:9", ResourceType: rt, Name: "Station 9", SourceAPI: "overpass",
					GeometryJSON: `{"type":"Point","coordinates":[-121.7750000,37.6805000]}`,
				}}, nil
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
			for _, r := range rows {
				r.Metric, r.SnapshotID = metric, snapshotID
				*saved = append(*saved, r)
			}
			if len(rows) == 0 {
				*saved = append(*saved, db.HexMetric{Metric: metric, SnapshotID: snapshotID, HexID: "<cleared>"})
			}
			return nil
		},
	}
}

func testFactory(store db.Store) *cmdutil.Factory {
	ios, _, _, _ := iostreams.Test()
	return &cmdutil.Factory{
		IOStreams:   ios,
		CityDB:      func() (db.Store, error) { return store, nil },
		CurrentCity: func() (*config.CityConfig, error) { return testCity, nil },
		Config:      func() (*config.Config, error) { return testCfg(), nil },
	}
}

// TestServicesCompute_StoresFireMetric drives `pvmt services compute` end to
// end over the mock store: every saved row is the fire metric attached to the
// roads snapshot, carries at least the turnout, references the station, and
// the hex nearest the station is faster than one at the far end of the road.
func TestServicesCompute_StoresFireMetric(t *testing.T) {
	var saved []db.HexMetric
	f := testFactory(fixtureStore(&saved))
	cmd := NewCmdServices(f)
	cmd.SilenceErrors, cmd.SilenceUsage = true, true
	cmd.SetArgs([]string{"compute"})
	if err := cmd.Execute(); err != nil {
		t.Fatal(err)
	}
	if len(saved) == 0 {
		t.Fatal("no hex metrics saved")
	}
	spec := services.Registry["fire"]
	var minV, maxV = saved[0].Value, saved[0].Value
	for _, m := range saved {
		if m.Metric != services.MetricFire || m.SnapshotID == nil || *m.SnapshotID != 7 || m.Ref != "osm:node:9" {
			t.Fatalf("row %+v: want metric %s on snapshot 7 referencing osm:node:9", m, services.MetricFire)
		}
		if m.Value < spec.TurnoutS {
			t.Errorf("row %+v below turnout %v", m, spec.TurnoutS)
		}
		minV, maxV = min(minV, m.Value), max(maxV, m.Value)
	}
	if maxV <= minV {
		t.Errorf("expected a spread of response times along the road, got min=max=%v", minV)
	}
}

func TestServicesCompute_UnknownServiceIsFlagError(t *testing.T) {
	var saved []db.HexMetric
	f := testFactory(fixtureStore(&saved))
	cmd := NewCmdServices(f)
	cmd.SilenceErrors, cmd.SilenceUsage = true, true
	cmd.SetArgs([]string{"compute", "--service", "police"})
	err := cmd.Execute()
	if err == nil || !strings.Contains(err.Error(), "unknown service") {
		t.Fatalf("expected an unknown-service flag error, got %v", err)
	}
	if len(saved) != 0 {
		t.Error("nothing must be saved for an unknown service")
	}
}

// TestServicesCompute_NoRoadsSnapshotHints: coverage attaches to the roads
// snapshot, so a city that never ran roads compute gets a hinted error, not a
// metric nobody can join.
func TestServicesCompute_NoRoadsSnapshotHints(t *testing.T) {
	var saved []db.HexMetric
	store := fixtureStore(&saved)
	store.LatestComputeResultFunc = func(context.Context, resource.Type) (*db.ComputeResult, error) { return nil, sql.ErrNoRows }
	f := testFactory(store)
	cmd := NewCmdServices(f)
	cmd.SilenceErrors, cmd.SilenceUsage = true, true
	cmd.SetArgs([]string{"compute"})
	err := cmd.Execute()
	if err == nil || !strings.Contains(err.Error(), "roads compute") {
		t.Fatalf("expected a hinted no-snapshot error, got %v", err)
	}
}

// TestServicesCompute_NoFacilitiesIsNoResults: without stations the city is an
// empty result (exit 3 / silent skip in `all`), not a failure.
func TestServicesCompute_NoFacilitiesIsNoResults(t *testing.T) {
	var saved []db.HexMetric
	store := fixtureStore(&saved)
	inner := store.ListFeaturesFunc
	store.ListFeaturesFunc = func(ctx context.Context, rt resource.Type) ([]db.Feature, error) {
		if rt.IsFacility() {
			return nil, nil
		}
		return inner(ctx, rt)
	}
	f := testFactory(store)
	err := RunComputeForCity(context.Background(), f, "fire", nil)
	if err == nil || !strings.Contains(err.Error(), cmdutil.ErrNoResults.Error()) {
		t.Fatalf("expected ErrNoResults, got %v", err)
	}
}

// TestServicesCompute_ConfigFacilityAndOverrides: a [[cities.facilities]]
// station is routed from like an OSM one (referenced as config:fire:0), and a
// [[services]] override changes the turnout every row carries.
func TestServicesCompute_ConfigFacilityAndOverrides(t *testing.T) {
	var saved []db.HexMetric
	store := fixtureStore(&saved)
	inner := store.ListFeaturesFunc
	store.ListFeaturesFunc = func(ctx context.Context, rt resource.Type) ([]db.Feature, error) {
		if rt.IsFacility() {
			return nil, nil
		}
		return inner(ctx, rt)
	}
	f := testFactory(store)
	city := *testCity
	city.Facilities = []config.FacilityConfig{{Service: "fire", Name: "Manual", Lat: 37.6805, Lon: -121.755}}
	f.CurrentCity = func() (*config.CityConfig, error) { return &city, nil }
	f.Config = func() (*config.Config, error) {
		cfg := testCfg()
		cfg.Services = []config.ServiceOverride{{Name: "fire", TurnoutS: 200}}
		return cfg, nil
	}
	if err := RunComputeForCity(context.Background(), f, "fire", nil); err != nil {
		t.Fatal(err)
	}
	if len(saved) == 0 {
		t.Fatal("no rows saved from the config facility")
	}
	for _, m := range saved {
		if m.Ref != "config:fire:0" {
			t.Fatalf("row %+v should reference the config facility", m)
		}
		if m.Value < 200 {
			t.Fatalf("row %+v below the overridden 200 s turnout", m)
		}
	}
}

// TestServicesCompute_CancelledSavesNothing pins the guard between routing
// and persistence: the context is cancelled from inside a store callback, so
// the pre-check passes and routing runs under a cancelled ctx; the
// post-routing ctx.Err() check is what must stop the save.
func TestServicesCompute_CancelledSavesNothing(t *testing.T) {
	var saved []db.HexMetric
	store := fixtureStore(&saved)
	ctx, cancel := context.WithCancel(context.Background())
	inner := store.GetBoundaryFunc
	store.GetBoundaryFunc = func(ctx context.Context) (string, error) {
		cancel() // after inputs are loaded, before routing
		return inner(ctx)
	}
	f := testFactory(store)
	err := RunComputeForCity(ctx, f, "fire", nil)
	if err == nil || !errors.Is(err, context.Canceled) {
		t.Fatalf("expected a cancellation error, got %v", err)
	}
	if len(saved) != 0 {
		t.Errorf("cancelled run persisted %d rows", len(saved))
	}
}

// TestServicesCompute_UnknownOverrideRejected: a [[services]] entry naming an
// unregistered service is a config error, not a silent no-op.
func TestServicesCompute_UnknownOverrideRejected(t *testing.T) {
	var saved []db.HexMetric
	f := testFactory(fixtureStore(&saved))
	f.Config = func() (*config.Config, error) {
		cfg := testCfg()
		cfg.Services = []config.ServiceOverride{{Name: "fire ", TurnoutS: 1}}
		return cfg, nil
	}
	err := RunComputeForCity(context.Background(), f, "fire", nil)
	if err == nil || !errors.Is(err, config.ErrInvalidConfig) || !strings.Contains(err.Error(), `"fire "`) {
		t.Fatalf("expected ErrInvalidConfig naming the bad service, got %v", err)
	}
	if len(saved) != 0 {
		t.Error("nothing must be saved for an invalid config")
	}
}

// TestServicesCompute_NoReachClearsPreviousRows: when routing reaches no hex
// (the only station is beyond the snap radius) the save still runs with zero
// rows so the previous run's coverage is cleared, and the user is warned.
func TestServicesCompute_NoReachClearsPreviousRows(t *testing.T) {
	var saved []db.HexMetric
	store := fixtureStore(&saved)
	inner := store.ListFeaturesFunc
	store.ListFeaturesFunc = func(ctx context.Context, rt resource.Type) ([]db.Feature, error) {
		if rt.IsFacility() {
			return []db.Feature{{ID: "osm:node:far", ResourceType: rt, Name: "Far", SourceAPI: "overpass",
				GeometryJSON: `{"type":"Point","coordinates":[-121.7750000,37.6880000]}`}}, nil // ~900 m north of the road
		}
		return inner(ctx, rt)
	}
	f := testFactory(store)
	if err := RunComputeForCity(context.Background(), f, "fire", nil); err != nil {
		t.Fatal(err)
	}
	if len(saved) != 1 || saved[0].HexID != "<cleared>" || saved[0].Metric != services.MetricFire {
		t.Fatalf("expected one empty save clearing %s, got %+v", services.MetricFire, saved)
	}
}
