package snapshots

import (
	"context"
	"encoding/json"
	"errors"
	"math"
	"strings"
	"testing"

	"github.com/jcrussell/solvent-streets/internal/config"
	"github.com/jcrussell/solvent-streets/internal/db"
	"github.com/jcrussell/solvent-streets/internal/geo"
	"github.com/jcrussell/solvent-streets/internal/population"
	"github.com/jcrussell/solvent-streets/internal/resource"
	"github.com/jcrussell/solvent-streets/internal/results"
	"github.com/jcrussell/solvent-streets/internal/services"
	"github.com/jcrussell/solvent-streets/internal/units"
	"github.com/jcrussell/solvent-streets/pkg/cmdutil"
	"github.com/jcrussell/solvent-streets/pkg/iostreams"
)

// diffFixture is a real database with cities alpha, beta and gamma and three
// snapshots: v1 (alpha and beta at 100 m), v2 (alpha recomputed at 150 m
// with more road, more people and better fire coverage, and gamma) and v3
// (gamma only).
type diffFixture struct {
	root *db.RootStore
	cfg  *config.Config
}

func newDiffFixture(t *testing.T) *diffFixture {
	t.Helper()
	ctx := context.Background()
	root, err := db.Open(":memory:")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = root.Close() })
	cfg := &config.Config{ConfigID: "test", Cities: []config.CityConfig{{Name: "Alpha"}, {Name: "Beta"}, {Name: "Gamma"}}}
	ids := map[string]int64{}
	for _, c := range cfg.Cities {
		id, err := root.EnsureCity(ctx, c.Slug(), c.Name, "test")
		if err != nil {
			t.Fatal(err)
		}
		ids[c.Slug()] = id
		if err := root.ForCity(id).SaveBoundary(ctx, diffBoundary, "test"); err != nil {
			t.Fatal(err)
		}
	}
	// compute commits roads (area, with a hex of coverage so the city has a
	// grid), census (pop people on one hex) and fire (one hex per response),
	// on hexes the map draws at the snapshot's hex edge: the diff, like the
	// stats panel, summarises only those.
	compute := func(slug string, edge, area, pop float64, fire ...float64) {
		t.Helper()
		hexes := interiorHexIDs(t, diffBoundary, edge, len(fire)+1)
		store := root.ForCity(ids[slug])
		parts := map[string]int64{}
		put := func(producer string, city []db.CityMetric, hex []db.HexMetric) {
			run, err := store.CreateRun(ctx, producer, "h", nil)
			if err != nil {
				t.Fatal(err)
			}
			if len(city) > 0 {
				if err := store.SaveCityMetrics(ctx, run.ID, city); err != nil {
					t.Fatal(err)
				}
			}
			if err := store.SaveHexMetrics(ctx, run.ID, hex); err != nil {
				t.Fatal(err)
			}
			parts[producer] = run.ID
		}
		put("roads", results.TotalRows(resource.TypeRoads, results.Totals{Area: area, Features: 3}),
			results.HexCoverageRows(resource.TypeRoads, []results.HexCoverage{{HexID: hexes[0], Area: area, PctCovered: 50}}))
		put(db.ProducerCensus, nil, []db.HexMetric{{HexID: hexes[0], Metric: population.KeyPopulation, Value: pop}})
		var rows []db.HexMetric
		for i, v := range fire {
			rows = append(rows, db.HexMetric{HexID: hexes[i+1], Metric: services.MetricFire, Value: v})
		}
		put(db.ServiceProducer("fire"), nil, rows)
		if err := store.CommitLatest(ctx, "h", config.ComputeEpoch, parts); err != nil {
			t.Fatal(err)
		}
	}
	create := func(name string, edge float64, slugs ...string) {
		t.Helper()
		var reqs []db.SnapshotRequest
		for _, s := range slugs {
			reqs = append(reqs, db.SnapshotRequest{CityID: ids[s], ConfigHash: "h", HexEdgeM: edge})
		}
		if _, err := root.CreateSnapshot(ctx, name, config.ComputeEpoch, reqs); err != nil {
			t.Fatal(err)
		}
	}
	compute("alpha", 100, 100, 10, 100, 400) // half within the 5:20 threshold
	compute("beta", 100, 200, 20, 100)
	create("v1", 100, "alpha", "beta")
	compute("alpha", 150, 150, 12, 100, 100)
	compute("gamma", 150, 50, 5, 100)
	create("v2", 150, "alpha", "gamma")
	create("v3", 150, "gamma")
	return &diffFixture{root: root, cfg: cfg}
}

// diffBoundary is every fixture city's boundary: a triangle about 1 km on a
// side, a few hundred hexes at the fixture's edges.
const diffBoundary = `{"type":"Polygon","coordinates":[[[0,0],[0.01,0],[0.01,0.01],[0,0]]]}`

// interiorHexIDs returns n ids of whole (unclipped) hexes of boundary's grid
// at edge, which every export draws.
func interiorHexIDs(t *testing.T, boundary string, edge float64, n int) []string {
	t.Helper()
	bbox, err := geo.BBoxFromGeoJSON(boundary)
	if err != nil {
		t.Fatal(err)
	}
	proj := geo.NewUTMProjector(geo.CenterFromBBox(bbox))
	hexes, err := geo.ClipHexesToBoundaryGeoJSON(context.Background(), geo.CityLattice(proj, bbox, edge), boundary, proj, nil)
	if err != nil {
		t.Fatal(err)
	}
	full := 3 * math.Sqrt(3) / 2 * edge * edge
	var ids []string
	for _, h := range hexes {
		if h.Geom.Area() > 0.999*full {
			ids = append(ids, h.ID)
		}
	}
	if len(ids) < n {
		t.Fatalf("%d whole hexes at %g m, need %d", len(ids), edge, n)
	}
	return ids[:n]
}

func (fx *diffFixture) run(t *testing.T, args ...string) (stdout, stderr string, err error) {
	t.Helper()
	ios, _, out, errOut := iostreams.Test()
	cmd := NewCmdDiff(&cmdutil.Factory{IOStreams: ios}, func(ctx context.Context, opts *DiffOptions) error {
		opts.RootDB = rootDBFunc(fx.root)
		opts.Config = func() (*config.Config, error) { return fx.cfg, nil }
		opts.UnitSystem = func() units.System { return units.Metric }
		return runDiff(ctx, opts)
	})
	cmd.SetArgs(args)
	cmd.SilenceErrors, cmd.SilenceUsage = true, true
	err = cmd.Execute()
	return out.String(), errOut.String(), err
}

// TestDiff_Table: a city in both is compared and flagged, cities in one are
// added or removed after it, and the header names both epochs.
func TestDiff_Table(t *testing.T) {
	fx := newDiffFixture(t)
	out, errOut, err := fx.run(t, "v1", "v2")
	if err != nil {
		t.Fatal(err)
	}
	lines := strings.Split(strings.TrimSpace(out), "\n")
	if len(lines) != 3 {
		t.Fatalf("rows = %q, want alpha, beta, gamma", out)
	}
	want := "alpha\tchanged\t0.01 ha\t0.01 ha\t+0.01 ha (+50.0%)\t2\t50.0% -> 100.0%\thex edge 100 -> 150 m"
	if lines[0] != want {
		t.Errorf("alpha row = %q, want %q", lines[0], want)
	}
	if !strings.HasPrefix(lines[1], "beta\tremoved\t") || !strings.HasPrefix(lines[2], "gamma\tadded\t") {
		t.Errorf("rows = %q, want beta removed then gamma added", lines[1:])
	}
	if !strings.Contains(errOut, "v1 (compute epoch") || !strings.Contains(errOut, "1 city in both, 2 only in one") {
		t.Errorf("header = %q", errOut)
	}
}

// TestDiff_JSON: every measure with a, b, delta and delta_share; the
// headline and flags; and nothing to compare for an added city.
func TestDiff_JSON(t *testing.T) {
	fx := newDiffFixture(t)
	out, _, err := fx.run(t, "v1", "v2", "--json", strings.Join(diffFields, ","))
	if err != nil {
		t.Fatal(err)
	}
	var rows []map[string]any
	if err := json.Unmarshal([]byte(out), &rows); err != nil {
		t.Fatalf("%v: %s", err, out)
	}
	if len(rows) != 3 {
		t.Fatalf("rows = %v", rows)
	}
	a := rows[0]
	if a["city"] != "alpha" || a["total_paved_a"] != 100.0 || a["total_paved_b"] != 150.0 || a["total_paved_delta"] != 50.0 ||
		a["total_paved_delta_share"] != 0.5 || a["hex_edge_changed"] != true || a["boundary_changed"] != false ||
		a["config_hash_changed"] != false || a["hex_edge_m_a"] != 100.0 || a["hex_edge_m_b"] != 150.0 {
		t.Errorf("alpha = %v", a)
	}
	got := map[string][2]any{}
	for _, m := range a["measures"].([]any) {
		m := m.(map[string]any)
		got[m["metric"].(string)+" "+m["subject"].(string)] = [2]any{m["a"], m["b"]}
	}
	for key, want := range map[string][2]any{
		"paved_area_m2 roads":                    {100.0, 150.0},
		"feature_count roads":                    {3.0, 3.0},
		"population ":                            {10.0, 12.0},
		"fire_response_s median":                 {250.0, 100.0},
		"within_threshold_share fire_response_s": {0.5, 1.0},
	} {
		if got[key] != want {
			t.Errorf("measure %q = %v, want %v", key, got[key], want)
		}
	}
	if g := rows[2]; g["status"] != "added" || g["measures"] != nil || g["total_paved_a"] != nil || g["compute_epoch_a"] != nil {
		t.Errorf("gamma = %v, want an added city with nothing compared", g)
	}
}

// TestDiff_SameSnapshot: a snapshot against itself is unchanged everywhere.
func TestDiff_SameSnapshot(t *testing.T) {
	fx := newDiffFixture(t)
	out, _, err := fx.run(t, "v1", "v1", "--json", "city,status,total_paved_delta,measures")
	if err != nil {
		t.Fatal(err)
	}
	var rows []struct {
		City     string       `json:"city"`
		Status   string       `json:"status"`
		Delta    float64      `json:"total_paved_delta"`
		Measures []measureRow `json:"measures"`
	}
	if err := json.Unmarshal([]byte(out), &rows); err != nil {
		t.Fatal(err)
	}
	if len(rows) != 2 {
		t.Fatalf("rows = %+v", rows)
	}
	for _, r := range rows {
		if r.Status != "unchanged" || r.Delta != 0 || len(r.Measures) == 0 {
			t.Errorf("%s = %+v, want unchanged", r.City, r)
		}
		for _, m := range r.Measures {
			if m.Delta == nil || *m.Delta != 0 {
				t.Errorf("%s %s %s delta = %v, want 0", r.City, m.Metric, m.Subject, m.Delta)
			}
		}
	}
}

// TestDiff_NoCityInBoth exits 3 (ErrNoResults); an unknown name is an error.
func TestDiff_NoCityInBoth(t *testing.T) {
	fx := newDiffFixture(t)
	if _, errOut, err := fx.run(t, "v1", "v3"); !errors.Is(err, cmdutil.ErrNoResults) || !strings.Contains(errOut, "no city is in both v1 and v3") {
		t.Errorf("v1 vs v3: err = %v, stderr %q; want ErrNoResults", err, errOut)
	}
	if _, _, err := fx.run(t, "v1", "nope"); err == nil || !strings.Contains(err.Error(), "snapshot nope not found") {
		t.Errorf("unknown name: err = %v", err)
	}
	if _, _, err := fx.run(t, "v1"); err == nil {
		t.Error("one name was accepted")
	}
}

// TestSameResults: a city's two sides are read once only when every part
// comes from the same run and the grid is drawn on the same boundary at the
// same edge; any other difference reads both.
func TestSameResults(t *testing.T) {
	base := db.SnapshotCity{ID: 1, CityID: 7, HexEdgeM: 100, BoundaryDigest: "b1", ConfigHash: "h1",
		Parts: []db.SnapshotPart{{Part: "roads", RunID: 10}, {Part: "census", RunID: 11}}}
	other := func(edit func(*db.SnapshotCity)) db.SnapshotCity {
		c := base
		c.ID = 2
		c.Parts = append([]db.SnapshotPart(nil), base.Parts...)
		edit(&c)
		return c
	}
	cases := []struct {
		name string
		b    db.SnapshotCity
		want bool
	}{
		{"same runs, another snapshot and config hash", other(func(c *db.SnapshotCity) { c.ConfigHash = "h2" }), true},
		{"parts listed in another order", other(func(c *db.SnapshotCity) { c.Parts[0], c.Parts[1] = c.Parts[1], c.Parts[0] }), true},
		{"a recomputed part", other(func(c *db.SnapshotCity) { c.Parts[1].RunID = 12 }), false},
		{"a part added", other(func(c *db.SnapshotCity) { c.Parts = append(c.Parts, db.SnapshotPart{Part: "service:fire", RunID: 13}) }), false},
		{"another boundary", other(func(c *db.SnapshotCity) { c.BoundaryDigest = "b2" }), false},
		{"another hex edge", other(func(c *db.SnapshotCity) { c.HexEdgeM = 150 }), false},
		{"another city", other(func(c *db.SnapshotCity) { c.CityID = 8 }), false},
	}
	for _, tc := range cases {
		if got := sameResults(base, tc.b); got != tc.want {
			t.Errorf("%s: sameResults = %v, want %v", tc.name, got, tc.want)
		}
	}
}
