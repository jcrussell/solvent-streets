package snapshots

import (
	"context"
	"encoding/json"
	"errors"
	"strings"
	"testing"

	"github.com/jcrussell/solvent-streets/internal/config"
	"github.com/jcrussell/solvent-streets/internal/db"
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
		if err := root.ForCity(id).SaveBoundary(ctx, `{"type":"Polygon","coordinates":[[[0,0],[1,0],[1,1],[0,0]]]}`, "test"); err != nil {
			t.Fatal(err)
		}
	}
	// compute commits roads (area, with a hex of coverage so the city has a
	// grid), census (pop people on one hex) and fire (one hex per response).
	compute := func(slug string, area, pop float64, fire ...float64) {
		t.Helper()
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
			results.HexCoverageRows(resource.TypeRoads, []results.HexCoverage{{HexID: "h1", Area: area, PctCovered: 50}}))
		put(db.ProducerCensus, nil, []db.HexMetric{{HexID: "h1", Metric: population.KeyPopulation, Value: pop}})
		var rows []db.HexMetric
		for i, v := range fire {
			rows = append(rows, db.HexMetric{HexID: string(rune('a' + i)), Metric: services.MetricFire, Value: v})
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
	compute("alpha", 100, 10, 100, 400) // half within the 5:20 threshold
	compute("beta", 200, 20, 100)
	create("v1", 100, "alpha", "beta")
	compute("alpha", 150, 12, 100, 100)
	compute("gamma", 50, 5, 100)
	create("v2", 150, "alpha", "gamma")
	create("v3", 150, "gamma")
	return &diffFixture{root: root, cfg: cfg}
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
		"fire_response_s median":                 {400.0, 100.0},
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
