package snapshots

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"strings"
	"testing"
	"time"

	"github.com/jcrussell/solvent-streets/internal/config"
	"github.com/jcrussell/solvent-streets/internal/db"
	"github.com/jcrussell/solvent-streets/internal/db/dbtest"
	"github.com/jcrussell/solvent-streets/internal/resource"
	"github.com/jcrussell/solvent-streets/pkg/cmdutil"
	"github.com/jcrussell/solvent-streets/pkg/iostreams"
)

var ingestedAt = time.Date(2026, 11, 14, 10, 2, 0, 0, time.UTC)

// coherentCity is a mock city whose latest compute read exactly its current
// inputs: roads and the combined pass over a boundary and roads.
func coherentCity(roadsNow string) *dbtest.MockStore {
	read := map[string]string{"boundary": "b1", "roads": "r1"}
	combined := map[string]string{"boundary": "b1", "roads": "r1", "parking": "", "sidewalks": ""}
	return &dbtest.MockStore{
		LatestComputeFunc: func(context.Context, string) ([]db.LatestPart, error) {
			return []db.LatestPart{
				{Part: "combined", RunID: 2, Inputs: combined},
				{Part: "roads", RunID: 1, Inputs: read},
			}, nil
		},
		InputStatesFunc: func(context.Context) ([]db.InputState, error) {
			return []db.InputState{
				{Input: "boundary", Digest: "b1", IngestedAt: ingestedAt},
				{Input: "roads", Digest: roadsNow, IngestedAt: ingestedAt},
			}, nil
		},
	}
}

func createOpts(ios *iostreams.IOStreams, stores map[string]db.Store) *CreateOptions {
	ids := map[string]int64{}
	byID := map[int64]db.Store{}
	for slug, s := range stores {
		ids[slug] = int64(len(ids) + 1)
		byID[ids[slug]] = s
	}
	root := &dbtest.MockRootStore{
		EnsureCityFunc: func(_ context.Context, slug, _, _ string) (int64, error) { return ids[slug], nil },
		ForCityFunc:    func(id int64) db.Store { return byID[id] },
	}
	cfg := &config.Config{ConfigID: "test", Cities: twoCityConfig()}
	return &CreateOptions{
		IO:            ios,
		RootDB:        rootDBFunc(root),
		ResolveCities: resolveCitiesFunc(cfg.Cities),
		Config:        func() (*config.Config, error) { return cfg, nil },
	}
}

// TestRunCreate_RefusesOneCityAndSnapshotsTheOther: create processes every
// city, prints each outcome with the refusal's reason and hint, and exits
// non-zero (ErrSilent) because one was refused.
func TestRunCreate_RefusesOneCityAndSnapshotsTheOther(t *testing.T) {
	ios, _, stdout, _ := iostreams.Test()
	opts := createOpts(ios, map[string]db.Store{
		"alpha": coherentCity("r2"), // roads re-ingested since
		"beta":  coherentCity("r1"),
	})
	err := runCreate(context.Background(), opts)
	if !errors.Is(err, cmdutil.ErrSilent) {
		t.Fatalf("err = %v, want ErrSilent", err)
	}
	want := `alpha: refused: roads re-ingested 2026-11-14 10:02 UTC after the combined and roads computes
  hint: pvmt --city alpha all compute
beta: created #1
`
	if got := stdout.String(); got != want {
		t.Errorf("output:\n%s\nwant:\n%s", got, want)
	}
}

// TestRunCreate_Unchanged: a city whose newest snapshot already freezes its
// latest compute reports it unchanged, and the run succeeds.
func TestRunCreate_Unchanged(t *testing.T) {
	ios, _, stdout, _ := iostreams.Test()
	unchanged := func(id int64) *dbtest.MockStore {
		s := coherentCity("r1")
		s.CreateSnapshotFunc = func(_ context.Context, _ string, edge float64, check db.CheckFunc) (*db.Snapshot, bool, error) {
			if err := check(nil, nil); err == nil {
				t.Error("check passed an empty latest compute")
			}
			return &db.Snapshot{ID: id, HexEdgeM: edge}, false, nil
		}
		return s
	}
	opts := createOpts(ios, map[string]db.Store{"alpha": unchanged(7), "beta": unchanged(9)})
	if err := runCreate(context.Background(), opts); err != nil {
		t.Fatal(err)
	}
	if got, want := stdout.String(), "alpha: unchanged (#7)\nbeta: unchanged (#9)\n"; got != want {
		t.Errorf("output = %q, want %q", got, want)
	}
}

// TestCreate_JSON: --json reports one row per city with its status, the
// snapshot's id and edge, and the refusal reasons.
func TestCreate_JSON(t *testing.T) {
	ios, _, stdout, _ := iostreams.Test()
	stores := map[string]db.Store{"alpha": &dbtest.MockStore{}, "beta": coherentCity("r1")}
	cmd := NewCmdCreate(&cmdutil.Factory{IOStreams: ios}, func(ctx context.Context, opts *CreateOptions) error {
		o := createOpts(ios, stores)
		o.Exporter = opts.Exporter
		return runCreate(ctx, o)
	})
	cmd.SetArgs([]string{"--json", "city,status,snapshot_id,hex_edge_m,reasons,hint"})
	cmd.SilenceErrors, cmd.SilenceUsage = true, true
	if err := cmd.Execute(); !errors.Is(err, cmdutil.ErrSilent) {
		t.Fatalf("err = %v, want ErrSilent (alpha has nothing computed)", err)
	}
	var rows []map[string]any
	if err := json.Unmarshal(stdout.Bytes(), &rows); err != nil {
		t.Fatalf("%v: %s", err, stdout)
	}
	if len(rows) != 2 {
		t.Fatalf("rows = %v, want 2", rows)
	}
	a, b := rows[0], rows[1]
	if a["status"] != "refused" || a["hint"] != "pvmt --city alpha all compute" || a["snapshot_id"] != nil {
		t.Errorf("alpha row = %v, want refused with the all compute hint", a)
	}
	if reasons, _ := a["reasons"].([]any); len(reasons) != 1 || !strings.Contains(reasons[0].(string), "no pavement compute") {
		t.Errorf("alpha reasons = %v", a["reasons"])
	}
	if b["status"] != "created" || b["snapshot_id"] != 1.0 || b["hex_edge_m"] != config.DefaultHexEdgeM {
		t.Errorf("beta row = %v, want created #1 at the default edge", b)
	}
	if reasons, _ := b["reasons"].([]any); reasons == nil || len(reasons) != 0 {
		t.Errorf("beta reasons = %v, want []", b["reasons"])
	}
}

// TestRunCreate_RealStore runs create against a real database: a compute
// creates no snapshot, create freezes it, a second create is unchanged, a
// roads re-ingest refuses the city, and nothing is written for it.
func TestRunCreate_RealStore(t *testing.T) {
	ctx := context.Background()
	root, err := db.Open(":memory:")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = root.Close() })
	cfg := &config.Config{ConfigID: "test", Cities: []config.CityConfig{{Name: "Alpha"}}}
	city := &cfg.Cities[0]
	id, err := root.EnsureCity(ctx, city.Slug(), city.Name, cfg.ConfigID)
	if err != nil {
		t.Fatal(err)
	}
	store := root.ForCity(id)
	if err := store.SaveBoundary(ctx, `{"type":"Polygon","coordinates":[[[0,0],[1,0],[1,1],[0,0]]]}`, "nominatim"); err != nil {
		t.Fatal(err)
	}
	roads := func(geom string) {
		t.Helper()
		if err := store.UpsertFeatures(ctx, resource.TypeRoads, []db.Feature{{ID: "w1", GeometryJSON: geom, SourceAPI: "osm"}}, nil); err != nil {
			t.Fatal(err)
		}
	}
	roads(`{"type":"LineString","coordinates":[[0,0],[1,1]]}`)
	draft := db.NewDraft(store, cfg.CityHash(city), config.ComputeEpoch)
	for _, producer := range []string{"roads", "combined"} {
		in, err := db.ReadProducerInputs(ctx, store, producer)
		if err != nil {
			t.Fatal(err)
		}
		run, err := draft.NewRun(ctx, producer, in)
		if err != nil {
			t.Fatal(err)
		}
		draft.Put(producer, run)
	}
	if err := draft.Commit(ctx); err != nil {
		t.Fatal(err)
	}
	if snaps, err := store.ListSnapshots(ctx); err != nil || len(snaps) != 0 {
		t.Fatalf("a compute created snapshots: %v (%v)", snaps, err)
	}

	run := func() (string, error) {
		ios, _, stdout, _ := iostreams.Test()
		err := runCreate(ctx, &CreateOptions{
			IO:            ios,
			RootDB:        func() (db.RootStorer, error) { return root, nil },
			ResolveCities: resolveCitiesFunc(cfg.Cities),
			Config:        func() (*config.Config, error) { return cfg, nil },
		})
		return stdout.String(), err
	}
	out, err := run()
	snaps, _ := store.ListSnapshots(ctx)
	if err != nil || len(snaps) != 1 || out != fmt.Sprintf("alpha: created #%d\n", snaps[0].ID) {
		t.Fatalf("coherent city: (%q, %v), want created", out, err)
	}
	snapID := snaps[0].ID
	if out, err = run(); err != nil || out != fmt.Sprintf("alpha: unchanged (#%d)\n", snapID) {
		t.Fatalf("second create: (%q, %v), want unchanged #%d", out, err, snapID)
	}

	roads(`{"type":"LineString","coordinates":[[0,0],[2,2]]}`)
	out, err = run()
	if !errors.Is(err, cmdutil.ErrSilent) || !strings.HasPrefix(out, "alpha: refused: roads re-ingested ") ||
		!strings.Contains(out, "after the combined and roads computes\n  hint: pvmt --city alpha all compute\n") {
		t.Errorf("after a roads re-ingest: (%q, %v), want refused with the all compute hint", out, err)
	}
	snaps, err = store.ListSnapshots(ctx)
	if err != nil || len(snaps) != 1 {
		t.Errorf("snapshots after a refusal = %d (%v), want 1", len(snaps), err)
	}
}

// TestSnapshots_VerbAliases: ls and rm still reach list and delete.
func TestSnapshots_VerbAliases(t *testing.T) {
	ios, _, _, _ := iostreams.Test()
	group := NewCmdSnapshots(&cmdutil.Factory{IOStreams: ios})
	for alias, want := range map[string]string{"ls": "list", "rm": "delete"} {
		cmd, _, err := group.Find([]string{alias})
		if err != nil || cmd.Name() != want {
			t.Errorf("snapshots %s resolves to %v (%v), want %s", alias, cmd, err, want)
		}
	}
}
