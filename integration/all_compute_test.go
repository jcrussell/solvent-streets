package integration

import (
	"context"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/jcrussell/solvent-streets/internal/config"
	"github.com/jcrussell/solvent-streets/internal/db"
	"github.com/jcrussell/solvent-streets/internal/resource"
	"github.com/jcrussell/solvent-streets/internal/units"
	"github.com/jcrussell/solvent-streets/pkg/cmd/all"
	"github.com/jcrussell/solvent-streets/pkg/cmd/compute"
	"github.com/jcrussell/solvent-streets/pkg/cmdutil"
	"github.com/jcrussell/solvent-streets/pkg/iostreams"
)

// TestAllCompute_SharedGridMatchesStandalone pins solvent-streets-7ou7.4
// end-to-end against a real sqlite store: `all compute` now builds the clipped
// hex grid once per city and shares it across the roads/parking/sidewalks
// passes and the combined pass, instead of rebuilding + reclipping it four
// times. This exercises the actual orchestration (BuildCityGrid →
// RunResourceForCity → RunCombined) and asserts:
//
//  1. Equivalence: the roads (and roads:city) hex stats it writes are identical
//     to those written by the standalone `roads compute` path, which builds and
//     clips its grid inline (PrebuiltGrid nil). Sharing the grid must not change
//     a single hex's area or coverage.
//  2. The combined pass ran against the shared grid (a "combined" compute result
//     is written).
//  3. The grid is shared, not rebuilt per resource: `all compute` prints the
//     "reusing shared clipped hex grid" line and never the inline
//     "Computing hex grid" line — the standalone path still prints the latter.
func TestAllCompute_SharedGridMatchesStandalone(t *testing.T) {
	ctx := context.Background()

	// --- Shared-grid path: run the real `all compute` command. ---
	storeA := newAllFixtureStore(t, ctx)
	iosA, _, _, errA := iostreams.Test()
	fA := allComputeFactory(iosA, storeA)
	allCmd := all.NewCmdAll(fA)
	allCmd.SilenceErrors, allCmd.SilenceUsage = true, true
	allCmd.SetArgs([]string{"compute"})
	if err := allCmd.ExecuteContext(ctx); err != nil {
		t.Fatalf("all compute: %v\nstderr:\n%s", err, errA.String())
	}

	// --- Baseline: standalone `roads compute` builds + clips its grid inline. ---
	storeB := newAllFixtureStore(t, ctx)
	iosB, _, _, errB := iostreams.Test()
	fB := allComputeFactory(iosB, storeB)
	rcmd := compute.NewCmdCompute(fB, &resource.Pavement{}, nil)
	rcmd.SilenceErrors, rcmd.SilenceUsage = true, true
	rcmd.SetArgs(nil)
	if err := rcmd.ExecuteContext(ctx); err != nil {
		t.Fatalf("standalone roads compute: %v", err)
	}

	// (1) Equivalence: roads and roads:city hex stats must match exactly.
	rtCity := resource.TypeRoads.With(resource.ScopeCity)
	roadsA := hexStatsByID(t, ctx, storeA, resource.TypeRoads)
	if len(roadsA) == 0 {
		t.Fatal("all compute wrote no roads hex stats; fixture produced no coverage")
	}
	assertHexStatsEqual(t, "roads", roadsA, hexStatsByID(t, ctx, storeB, resource.TypeRoads))
	assertHexStatsEqual(t, "roads:city",
		hexStatsByID(t, ctx, storeA, rtCity),
		hexStatsByID(t, ctx, storeB, rtCity))

	// (2) The combined pass ran against the shared grid.
	combined, err := storeA.LatestComputeResult(ctx, resource.CombinedAll)
	if err != nil {
		t.Fatalf("all compute wrote no %q result: %v", resource.CombinedAll, err)
	}
	if combined.TotalArea <= 0 {
		t.Errorf("combined TotalArea = %v; want > 0", combined.TotalArea)
	}

	// (3) Perf signal: shared once, not rebuilt per resource. These are
	// progress lines, so they land on ErrOut (chatter), not Out (data).
	if !strings.Contains(errA.String(), "Reusing shared clipped hex grid") {
		t.Errorf("all compute did not report reusing a shared grid; got:\n%s", errA.String())
	}
	if strings.Contains(errA.String(), "Computing hex grid") {
		t.Errorf("all compute rebuilt the grid inline (found \"Computing hex grid\"); want the shared grid reused:\n%s", errA.String())
	}
	if !strings.Contains(errB.String(), "Computing hex grid") {
		t.Errorf("standalone compute should build its grid inline; got:\n%s", errB.String())
	}
}

func hexStatsByID(t *testing.T, ctx context.Context, store db.Store, rt resource.Type) map[string]db.HexStat {
	t.Helper()
	stats, err := store.ListHexStats(ctx, rt)
	if err != nil {
		t.Fatalf("ListHexStats(%s): %v", rt, err)
	}
	m := make(map[string]db.HexStat, len(stats))
	for _, s := range stats {
		m[s.HexID] = s
	}
	return m
}

// assertHexStatsEqual compares two hex-stat sets by hex id, requiring identical
// Area and PctCovered — an inline-vs-shared grid must produce byte-identical
// per-hex output.
func assertHexStatsEqual(t *testing.T, label string, got, want map[string]db.HexStat) {
	t.Helper()
	if len(got) != len(want) {
		t.Errorf("%s: hex count differs: all-compute=%d standalone=%d", label, len(got), len(want))
	}
	for id, w := range want {
		g, ok := got[id]
		if !ok {
			t.Errorf("%s: hex %s present in standalone but missing from all-compute", label, id)
			continue
		}
		if g.Area != w.Area || g.PctCovered != w.PctCovered {
			t.Errorf("%s: hex %s differs: all-compute {area=%v pct=%v} != standalone {area=%v pct=%v}",
				label, id, g.Area, g.PctCovered, w.Area, w.PctCovered)
		}
	}
}

func allComputeFactory(ios *iostreams.IOStreams, store db.Store) *cmdutil.Factory {
	cfg := &config.Config{ConfigID: "test", Cities: []config.CityConfig{{Name: "Grid City"}}}
	city := cfg.Cities[0]
	return &cmdutil.Factory{
		IOStreams:   ios,
		UnitSystem:  func() units.System { return units.Metric },
		Config:      func() (*config.Config, error) { return cfg, nil },
		CurrentCity: func() (*config.CityConfig, error) { return &city, nil },
		CityDB:      func() (db.Store, error) { return store, nil },
		CityFlagSet: func() bool { return true },
	}
}

// newAllFixtureStore opens a fresh sqlite store and loads a boundary plus a few
// roads (mixed jurisdictions/cohorts), one parking lot, and one sidewalk — all
// inside a ~1km Livermore bbox, small enough for a fast hex grid.
func newAllFixtureStore(t *testing.T, ctx context.Context) db.Store {
	t.Helper()
	root, err := db.Open(filepath.Join(t.TempDir(), "all.db"))
	if err != nil {
		t.Fatalf("db.Open: %v", err)
	}
	t.Cleanup(func() { _ = root.Close() })

	cityID, err := root.EnsureCity(ctx, "grid-city", "Grid City", "test")
	if err != nil {
		t.Fatalf("EnsureCity: %v", err)
	}
	store := root.ForCity(cityID)

	const boundary = `{"type":"Polygon","coordinates":[[[-121.770,37.680],[-121.760,37.680],[-121.760,37.690],[-121.770,37.690],[-121.770,37.680]]]}`
	if err := store.SaveBoundary(ctx, boundary, "fixture"); err != nil {
		t.Fatalf("SaveBoundary: %v", err)
	}

	roads := []db.Feature{
		feat("road:res", resource.TypeRoads, map[string]string{"highway": "residential"},
			`{"type":"LineString","coordinates":[[-121.768,37.682],[-121.762,37.688]]}`),
		feat("road:ter", resource.TypeRoads, map[string]string{"highway": "tertiary"},
			`{"type":"LineString","coordinates":[[-121.767,37.683],[-121.763,37.687]]}`),
		feat("road:trunk", resource.TypeRoads, map[string]string{"highway": "trunk"},
			`{"type":"LineString","coordinates":[[-121.766,37.681],[-121.764,37.689]]}`),
	}
	if err := store.UpsertFeatures(ctx, resource.TypeRoads, roads, nil); err != nil {
		t.Fatalf("UpsertFeatures roads: %v", err)
	}
	parking := []db.Feature{feat("park:1", resource.TypeParking, map[string]string{"amenity": "parking"},
		`{"type":"Polygon","coordinates":[[[-121.766,37.684],[-121.765,37.684],[-121.765,37.685],[-121.766,37.685],[-121.766,37.684]]]}`)}
	if err := store.UpsertFeatures(ctx, resource.TypeParking, parking, nil); err != nil {
		t.Fatalf("UpsertFeatures parking: %v", err)
	}
	sidewalks := []db.Feature{feat("walk:1", resource.TypeSidewalks, map[string]string{"footway": "sidewalk"},
		`{"type":"LineString","coordinates":[[-121.767,37.685],[-121.763,37.685]]}`)}
	if err := store.UpsertFeatures(ctx, resource.TypeSidewalks, sidewalks, nil); err != nil {
		t.Fatalf("UpsertFeatures sidewalks: %v", err)
	}
	return store
}

func feat(id string, rt resource.Type, tags map[string]string, geomJSON string) db.Feature {
	return db.Feature{
		ID:           id,
		ResourceType: rt,
		Tags:         tags,
		GeometryJSON: geomJSON,
		SourceAPI:    "fixture",
		FetchedAt:    time.Unix(0, 0),
	}
}
