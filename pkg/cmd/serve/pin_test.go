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
		return exportpkg.CityEntry{
			Config: cfg, City: city, Slug: city.Slug(),
			Store: &dbtest.MockStore{
				ListSnapshotsFunc: func(context.Context) ([]db.Snapshot, error) { return snaps, nil },
				LatestComputeFunc: func(context.Context, string) ([]db.LatestPart, error) { return latest, nil },
				WithSnapshotFunc:  func(id int64) db.Store { pins[name] = pin{snapshot: id}; return &dbtest.MockStore{} },
				WithRunsFunc:      func(runs []int64) db.Store { pins[name] = pin{runs: runs}; return &dbtest.MockStore{} },
			},
		}
	}
	cur := cfg.CityHash(&config.CityConfig{Name: "x"})
	roads := []db.SnapshotPart{{Part: "roads", RunID: 1}}
	entries := []exportpkg.CityEntry{
		entry("Fresh City", []db.Snapshot{{ID: 1, ConfigHash: cur, Parts: roads}}, nil),
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
	got := out.String()
	if strings.Count(got, "warning:") != 3 || strings.Contains(got, "Fresh City") {
		t.Errorf("want one warning each for Stale, Draft and Empty City, none for Fresh City; got:\n%s", got)
	}
	for _, want := range []string{
		"Stale City: snapshot #3 was created under other settings",
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
