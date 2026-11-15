package serve

import (
	"context"
	"strings"
	"testing"

	"github.com/jcrussell/solvent-streets/internal/config"
	"github.com/jcrussell/solvent-streets/internal/db"
	"github.com/jcrussell/solvent-streets/internal/db/dbtest"
	exportpkg "github.com/jcrussell/solvent-streets/internal/export"
)

// TestPinEntries: serve shows each city's newest snapshot whatever settings it
// was created under (warning when they are not the current ones), a city with
// no snapshot its latest compute pinned as of startup (saying so), and a city
// with neither an empty map, named once with the command that fixes it. A
// city with a current snapshot is not mentioned.
func TestPinEntries(t *testing.T) {
	cfg := &config.Config{}
	type pin struct {
		snapshot int64
		runs     []int64
	}
	pins := map[string]pin{}
	entry := func(name string, snaps []db.Snapshot, latest []db.LatestPart) exportpkg.CityEntry {
		city := config.CityConfig{Name: name}
		store := &dbtest.MockStore{
			ListSnapshotsFunc: func(context.Context) ([]db.Snapshot, error) { return snaps, nil },
			LatestComputeFunc: func(context.Context, string) ([]db.LatestPart, error) { return latest, nil },
			WithRunsFunc:      func(runs []int64) db.Store { pins[name] = pin{runs: runs}; return &dbtest.MockStore{} },
		}
		store.WithSnapshotFunc = func(id int64) db.Store {
			if id == 0 { // unpinned: LatestComputeEntry clearing any snapshot pin
				return store
			}
			pins[name] = pin{snapshot: id}
			return &dbtest.MockStore{}
		}
		return exportpkg.CityEntry{Config: cfg, City: city, Slug: city.Slug(), Store: store}
	}
	cur := cfg.CityHash(&config.CityConfig{Name: "x"})
	roads := []db.SnapshotPart{{Part: "roads", RunID: 1}}
	entries := []exportpkg.CityEntry{
		entry("Fresh City", []db.Snapshot{{ID: 1, ConfigHash: cur, Parts: roads}}, []db.LatestPart{{Part: "roads", RunID: 1}}),
		entry("Stale City", []db.Snapshot{{ID: 3, ConfigHash: "0ld0ld0ld0ld0ld0", Parts: roads}, {ID: 2, ConfigHash: cur, Parts: roads}}, nil),
		entry("Draft City", nil, []db.LatestPart{{Part: "roads", RunID: 7}, {Part: "combined", RunID: 8}}),
		entry("Empty City", nil, nil),
	}

	var out strings.Builder
	if err := pinEntries(context.Background(), entries, &out); err != nil {
		t.Fatal(err)
	}
	if got := pins["Fresh City"]; got.snapshot != 1 {
		t.Errorf("Fresh City pinned to %+v, want snapshot 1", got)
	}
	if got := pins["Stale City"]; got.snapshot != 3 {
		t.Errorf("Stale City pinned to %+v, want its newest snapshot 3, whatever its hash", got)
	}
	if got := pins["Draft City"]; got.snapshot != 0 || len(got.runs) != 2 || got.runs[0] != 7 || got.runs[1] != 8 {
		t.Errorf("Draft City pinned to %+v, want its latest compute's runs [7 8]", got)
	}
	if !entries[2].ReadsLatest() || entries[0].ReadsLatest() {
		t.Error("only Draft City should read its latest compute")
	}
	// solvent-streets-u45d: the snapshot list is read with the pin.
	if len(entries[1].Snapshots) != 2 || entries[1].Snapshots[0].ID != 3 || entries[3].Snapshots == nil {
		t.Errorf("Snapshots = %v / %v, want each city's list as read at startup", entries[1].Snapshots, entries[3].Snapshots)
	}
	if len(entries[2].Latest) != 2 || len(entries[3].Latest) != 0 {
		t.Errorf("Latest = %v / %v, want each city's latest compute kept for ?snapshot=latest", entries[2].Latest, entries[3].Latest)
	}
	got := out.String()
	if strings.Count(got, "warning:") != 4 || strings.Contains(got, "Fresh City") {
		t.Errorf("want two warnings for Stale City and one each for Draft and Empty City, none for Fresh City; got:\n%s", got)
	}
	for _, want := range []string{
		"Stale City: snapshot #3 was created under other settings",
		// solvent-streets-qo1y: the picker's Latest compute is empty.
		"Stale City has nothing computed under the current settings",
		"Draft City has no snapshot; serving its latest compute (not snapshotted)",
		"pvmt --city draft-city snapshots create",
		"no snapshot for Empty City; serving it without data",
		"Empty City has never been computed",
	} {
		if !strings.Contains(got, want) {
			t.Errorf("output lacks %q; got:\n%s", want, got)
		}
	}
}

// TestPinEntries_StaleLatestWarns (solvent-streets-4300): a latest compute
// whose boundary was ingested again since is drawn on the new boundary, so
// serve says so at startup.
func TestPinEntries_StaleLatestWarns(t *testing.T) {
	cfg := &config.Config{}
	city := config.CityConfig{Name: "Moved City"}
	latest := []db.LatestPart{{Part: "roads", RunID: 1, Inputs: map[string]string{db.InputBoundary: "old", "roads": "r"}}}
	store := &dbtest.MockStore{
		LatestComputeFunc: func(context.Context, string) ([]db.LatestPart, error) { return latest, nil },
		InputStatesFunc: func(context.Context) ([]db.InputState, error) {
			return []db.InputState{{Input: db.InputBoundary, Digest: "new"}, {Input: "roads", Digest: "r"}}, nil
		},
		WithRunsFunc: func([]int64) db.Store { return &dbtest.MockStore{} },
	}
	store.WithSnapshotFunc = func(int64) db.Store { return store }
	entries := []exportpkg.CityEntry{{Config: cfg, City: city, Slug: city.Slug(), Store: store}}
	var out strings.Builder
	if err := pinEntries(context.Background(), entries, &out); err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(out.String(), "Moved City's latest compute is stale: roads (boundary re-ingested") {
		t.Errorf("no stale-latest warning:\n%s", out.String())
	}
}
