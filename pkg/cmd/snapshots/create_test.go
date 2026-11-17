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
	"github.com/jcrussell/solvent-streets/internal/hint"
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
	cfg := &config.Config{ConfigID: "test", Cities: twoCityConfig()}
	ids := map[string]int64{}
	byID := map[int64]db.Store{}
	for _, c := range cfg.Cities {
		ids[c.Slug()] = int64(len(ids) + 1)
		byID[ids[c.Slug()]] = stores[c.Slug()]
	}
	root := &dbtest.MockRootStore{
		EnsureCityFunc: func(_ context.Context, slug, _, _ string) (int64, error) { return ids[slug], nil },
		ForCityFunc:    func(id int64) db.Store { return byID[id] },
	}
	return &CreateOptions{
		IO:     ios,
		RootDB: rootDBFunc(root),
		Config: func() (*config.Config, error) { return cfg, nil },
		Name:   "v1",
	}
}

// TestRunCreate_OneRefusalRefusesTheSnapshot: create judges every city, lists
// each refusal with its reason and hint, creates nothing and exits non-zero
// (ErrSilent).
func TestRunCreate_OneRefusalRefusesTheSnapshot(t *testing.T) {
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
refused v1: 1 of 2 cities refused; nothing was created
`
	if got := stdout.String(); got != want {
		t.Errorf("output:\n%s\nwant:\n%s", got, want)
	}
}

// TestRunCreate_Created: every city coherent creates the snapshot over all of
// them, in one CreateSnapshot call naming every city.
func TestRunCreate_Created(t *testing.T) {
	ios, _, stdout, _ := iostreams.Test()
	opts := createOpts(ios, map[string]db.Store{"alpha": coherentCity("r1"), "beta": coherentCity("r1")})
	if err := runCreate(context.Background(), opts); err != nil {
		t.Fatal(err)
	}
	if got, want := stdout.String(), "created v1: 2 cities\n"; got != want {
		t.Errorf("output = %q, want %q", got, want)
	}
}

// TestRunCreate_ExistingNameIsRefused: a taken name is an error whose hint
// says to delete it first.
func TestRunCreate_ExistingNameIsRefused(t *testing.T) {
	ios, _, _, _ := iostreams.Test()
	opts := createOpts(ios, map[string]db.Store{"alpha": coherentCity("r1"), "beta": coherentCity("r1")})
	root, _ := opts.RootDB()
	root.(*dbtest.MockRootStore).CreateSnapshotFunc = func(context.Context, string, int, []db.SnapshotRequest) (*db.Snapshot, error) {
		return nil, fmt.Errorf("snapshot v1: %w", db.ErrSnapshotExists)
	}
	err := runCreate(context.Background(), opts)
	var h *hint.ErrHint
	if err == nil || !errors.As(err, &h) || !strings.Contains(h.Hint, "pvmt snapshots delete v1") {
		t.Errorf("err = %v, want one hinting `pvmt snapshots delete v1`", err)
	}
}

// TestCreate_RejectsCityAndBadNames: --city and an invalid name are flag
// errors, before anything runs.
func TestCreate_RejectsCityAndBadNames(t *testing.T) {
	for name, c := range map[string]struct {
		citySet bool
		args    []string
	}{
		"--city":       {true, []string{"v1"}},
		"no name":      {false, nil},
		"leading dash": {false, []string{"--", "-v1"}},
		"slash":        {false, []string{"a/b"}},
		"reserved":     {false, []string{"latest"}},
	} {
		t.Run(name, func(t *testing.T) {
			ios, _, _, _ := iostreams.Test()
			f := &cmdutil.Factory{IOStreams: ios, CityFlagSet: func() bool { return c.citySet }}
			ran := false
			cmd := NewCmdCreate(f, func(context.Context, *CreateOptions) error { ran = true; return nil })
			cmd.SetArgs(c.args)
			cmd.SilenceErrors, cmd.SilenceUsage = true, true
			if err := cmd.Execute(); err == nil || ran {
				t.Errorf("err = %v, ran = %v; want a refusal before running", err, ran)
			}
		})
	}
}

// TestCreate_JSON: --json reports one row per refused city with its reasons
// and hint; nothing is created.
func TestCreate_JSON(t *testing.T) {
	ios, _, stdout, _ := iostreams.Test()
	stores := map[string]db.Store{"alpha": &dbtest.MockStore{}, "beta": coherentCity("r1")}
	cmd := NewCmdCreate(&cmdutil.Factory{IOStreams: ios}, func(ctx context.Context, opts *CreateOptions) error {
		o := createOpts(ios, stores)
		o.Exporter = opts.Exporter
		return runCreate(ctx, o)
	})
	cmd.SetArgs([]string{"v1", "--json", "city,status,snapshot,hex_edge_m,reasons,hint"})
	cmd.SilenceErrors, cmd.SilenceUsage = true, true
	if err := cmd.Execute(); !errors.Is(err, cmdutil.ErrSilent) {
		t.Fatalf("err = %v, want ErrSilent (alpha has nothing computed)", err)
	}
	var rows []map[string]any
	if err := json.Unmarshal(stdout.Bytes(), &rows); err != nil {
		t.Fatalf("%v: %s", err, stdout)
	}
	if len(rows) != 1 {
		t.Fatalf("rows = %v, want alpha's refusal only", rows)
	}
	a := rows[0]
	if a["city"] != "alpha" || a["status"] != "refused" || a["hint"] != "pvmt --city alpha all compute" || a["snapshot"] != nil {
		t.Errorf("alpha row = %v, want refused with the all compute hint", a)
	}
	if reasons, _ := a["reasons"].([]any); len(reasons) != 1 || !strings.Contains(reasons[0].(string), "nothing computed") {
		t.Errorf("alpha reasons = %v", a["reasons"])
	}

	// All coherent: one created row per city, naming the snapshot.
	stdout.Reset()
	stores["alpha"] = coherentCity("r1")
	cmd.SetArgs([]string{"v1", "--json", "city,status,snapshot,hex_edge_m,reasons"})
	if err := cmd.Execute(); err != nil {
		t.Fatal(err)
	}
	if err := json.Unmarshal(stdout.Bytes(), &rows); err != nil {
		t.Fatalf("%v: %s", err, stdout)
	}
	if len(rows) != 2 || rows[1]["status"] != "created" || rows[1]["snapshot"] != "v1" || rows[1]["hex_edge_m"] != config.DefaultHexEdgeM {
		t.Errorf("rows = %v, want two created in v1 at the default edge", rows)
	}
	if reasons, _ := rows[1]["reasons"].([]any); reasons == nil || len(reasons) != 0 {
		t.Errorf("beta reasons = %v, want []", rows[1]["reasons"])
	}
}

// TestRunCreate_RealStore runs create against a real database: a compute
// creates no snapshot, create freezes it, the same name again is refused, a
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

	run := func(name string) (string, error) {
		ios, _, stdout, _ := iostreams.Test()
		err := runCreate(ctx, &CreateOptions{
			IO:     ios,
			RootDB: func() (db.RootStorer, error) { return root, nil },
			Config: func() (*config.Config, error) { return cfg, nil },
			Name:   name,
		})
		return stdout.String(), err
	}
	out, err := run("v1")
	snaps, _ := store.ListSnapshots(ctx)
	if err != nil || len(snaps) != 1 || out != "created v1: 1 city\n" {
		t.Fatalf("coherent city: (%q, %v), want created", out, err)
	}
	if _, err = run("v1"); !errors.Is(err, db.ErrSnapshotExists) && (err == nil || !strings.Contains(err.Error(), "already exists")) {
		t.Fatalf("second create of v1: err = %v, want already exists", err)
	}

	roads(`{"type":"LineString","coordinates":[[0,0],[2,2]]}`)
	out, err = run("v2")
	if !errors.Is(err, cmdutil.ErrSilent) || !strings.HasPrefix(out, "alpha: refused: roads re-ingested ") ||
		!strings.Contains(out, "after the combined and roads computes\n  hint: pvmt --city alpha all compute\n") {
		t.Errorf("after a roads re-ingest: (%q, %v), want refused with the all compute hint", out, err)
	}
	snaps, err = store.ListSnapshots(ctx)
	if err != nil || len(snaps) != 1 {
		t.Errorf("snapshots after a refusal = %d (%v), want 1", len(snaps), err)
	}

	// A hashed setting changes: nothing is computed under the new hash. The
	// refusal says so and names the settings change, rather than calling
	// every ingested resource never computed (solvent-streets-vx0s).
	cfg.Grid.HexEdgeM = 150
	out, err = run("v2")
	if !errors.Is(err, cmdutil.ErrSilent) || !strings.HasPrefix(out, "alpha: refused: nothing computed under the current settings") ||
		!strings.Contains(out, "was computed, but not under the current settings") || strings.Contains(out, "never computed") {
		t.Errorf("after a settings change: (%q, %v), want the nothing-computed refusal with the stale-settings hint", out, err)
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
