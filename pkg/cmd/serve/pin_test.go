package serve

import (
	"context"
	"errors"
	"maps"
	"slices"
	"strings"
	"testing"

	"github.com/jcrussell/solvent-streets/internal/config"
	"github.com/jcrussell/solvent-streets/internal/db"
	"github.com/jcrussell/solvent-streets/internal/db/dbtest"
	exportpkg "github.com/jcrussell/solvent-streets/internal/export"
	"github.com/jcrussell/solvent-streets/internal/hint"
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
	entry := func(name string, snaps []db.SnapshotCity, latest []db.LatestPart) exportpkg.CityEntry {
		city := config.CityConfig{Name: name}
		store := &dbtest.MockStore{
			ListSnapshotsFunc: func(context.Context) ([]db.SnapshotCity, error) { return snaps, nil },
			LatestComputeFunc: func(context.Context, string) ([]db.LatestPart, error) { return latest, nil },
			WithRunsFunc:      func(runs []int64) db.Store { pins[name] = pin{runs: runs}; return &dbtest.MockStore{} },
			GetBoundaryFunc:   func(context.Context) (string, error) { return `{"type":"Polygon"}`, nil },
		}
		store.WithSnapshotFunc = func(id int64, _ string) db.Store {
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
		entry("Fresh City", []db.SnapshotCity{{ID: 1, ConfigHash: cur, Parts: roads}}, []db.LatestPart{{Part: "roads", RunID: 1}}),
		entry("Stale City", []db.SnapshotCity{{ID: 3, Name: "v3", ConfigHash: "0ld0ld0ld0ld0ld0", Parts: roads}, {ID: 2, ConfigHash: cur, Parts: roads}}, nil),
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
	// The snapshot list is read with the pin.
	if len(entries[1].Snapshots) != 2 || entries[1].Snapshots[0].ID != 3 || entries[3].Snapshots == nil {
		t.Errorf("Snapshots = %v / %v, want each city's list as read at startup", entries[1].Snapshots, entries[3].Snapshots)
	}
	if len(entries[2].Latest) != 2 || len(entries[3].Latest) != 0 {
		t.Errorf("Latest = %v / %v, want each city's latest compute kept for ?snapshot=latest", entries[2].Latest, entries[3].Latest)
	}
	got := out.String()
	// Two warnings for Stale City (its snapshot's settings, and the empty
	// latest compute the picker offers), one each for Draft and Empty City,
	// none for Fresh City.
	warned := map[string]int{}
	for _, w := range warnings(got) {
		for _, e := range entries {
			if strings.Contains(w, e.City.Name) {
				warned[e.City.Name]++
			}
		}
	}
	if want := map[string]int{"Stale City": 2, "Draft City": 1, "Empty City": 1}; !maps.Equal(warned, want) || len(warnings(got)) != 4 {
		t.Errorf("warnings per city = %v, want %v; got:\n%s", warned, want, got)
	}
	// Stale City's first is export's settings warning for its newest
	// snapshot (whose wording internal/export owns).
	if w := entries[1].SnapshotSettingsWarning(&entries[1].Snapshots[0]); w == "" || !strings.Contains(got, "warning: "+w) {
		t.Errorf("output lacks Stale City's settings warning %q; got:\n%s", w, got)
	}
	// docs/troubleshooting.md ("City has no snapshot") quotes the next two
	// warnings for users to search for; this is the one test that pins them.
	// Draft City's also names the command that publishes its latest compute.
	if w := warningsNaming(got, "Draft City"); len(w) != 1 ||
		!strings.HasPrefix(w[0], "warning: Draft City has no snapshot; serving its latest compute (not snapshotted)") ||
		!strings.Contains(w[0], "'pvmt snapshots create <name>'") {
		t.Errorf("Draft City's warning = %q, want the documented one with the snapshots create command", w)
	}
	if w := warningsNaming(got, "Empty City"); len(w) != 1 || w[0] != "warning: no snapshot for Empty City; serving it without data" {
		t.Errorf("Empty City's warning = %q, want the documented one", w)
	}
	// Empty City's hint names the compute that gives it data.
	if !strings.Contains(got, "hint: ") || !strings.Contains(got, "'pvmt --city empty-city all compute'") {
		t.Errorf("output lacks Empty City's hint naming its compute; got:\n%s", got)
	}
}

// warnings returns the "warning: ..." lines of a startup's stderr.
func warnings(out string) []string {
	var ws []string
	for line := range strings.SplitSeq(out, "\n") {
		if strings.HasPrefix(line, "warning: ") {
			ws = append(ws, line)
		}
	}
	return ws
}

// warningsNaming returns the warnings that name every one of subjects.
func warningsNaming(out string, subjects ...string) []string {
	var ws []string
	for _, w := range warnings(out) {
		if !slices.ContainsFunc(subjects, func(s string) bool { return !strings.Contains(w, s) }) {
			ws = append(ws, w)
		}
	}
	return ws
}

// TestPinEntries_StaleLatestWarns: a latest compute
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
		WithRunsFunc:    func([]int64) db.Store { return &dbtest.MockStore{} },
		GetBoundaryFunc: func(context.Context) (string, error) { return `{"type":"Polygon"}`, nil },
	}
	store.WithSnapshotFunc = func(int64, string) db.Store { return store }
	entries := []exportpkg.CityEntry{{Config: cfg, City: city, Slug: city.Slug(), Store: store}}
	var out strings.Builder
	if err := pinEntries(context.Background(), entries, &out); err != nil {
		t.Fatal(err)
	}
	// The warning names the city, the stale part and the input that moved.
	if len(warningsNaming(out.String(), "Moved City", "roads", "boundary")) != 1 {
		t.Errorf("no stale-latest warning naming Moved City's roads and the boundary:\n%s", out.String())
	}
}

// TestPinEntries_RefusesWithNothingToServe: serve refuses to start (exit 3)
// when no city has a boundary, naming the ingest, or when none has a snapshot
// or a latest compute, naming the compute, and prints no per-city warnings.
func TestPinEntries_RefusesWithNothingToServe(t *testing.T) {
	cfg := &config.Config{}
	entry := func(name, boundary string) exportpkg.CityEntry {
		city := config.CityConfig{Name: name}
		store := &dbtest.MockStore{
			GetBoundaryFunc: func(context.Context) (string, error) { return boundary, nil },
		}
		store.WithSnapshotFunc = func(int64, string) db.Store { return store }
		return exportpkg.CityEntry{Config: cfg, City: city, Slug: city.Slug(), Store: store}
	}
	for _, tc := range []struct {
		name    string
		entries []exportpkg.CityEntry
		command string // the hint's: what to run first
	}{
		{"fresh database", []exportpkg.CityEntry{entry("A", ""), entry("B", "")}, "pvmt all ingest"},
		{"never computed", []exportpkg.CityEntry{entry("A", `{"type":"Polygon"}`)}, "pvmt all compute"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			var out strings.Builder
			err := pinEntries(context.Background(), tc.entries, &out)
			if _, ok := errors.AsType[*hint.PrereqError](err); !ok {
				t.Fatalf("err = %v, want a PrereqError (exit 3)", err)
			}
			h, ok := errors.AsType[*hint.ErrHint](err)
			if !ok || !strings.Contains(h.Hint, tc.command) {
				t.Errorf("err = %v (hint %v), want the hint to name %q", err, h, tc.command)
			}
			if out.Len() != 0 {
				t.Errorf("a refused serve warned:\n%s", out.String())
			}
		})
	}
}

// TestPinNamed: --snapshot pins every city to the named snapshot, keeps each
// city's latest compute and snapshot list, warns about one frozen under other
// settings, and refuses to start when a city is not in it.
func TestPinNamed(t *testing.T) {
	cfg := &config.Config{}
	pins := map[string]int64{}
	entry := func(name string, has map[string]db.SnapshotCity) exportpkg.CityEntry {
		city := config.CityConfig{Name: name}
		var list []db.SnapshotCity
		for _, s := range has {
			list = append(list, s)
		}
		store := &dbtest.MockStore{
			ListSnapshotsFunc: func(context.Context) ([]db.SnapshotCity, error) { return list, nil },
			LatestComputeFunc: func(context.Context, string) ([]db.LatestPart, error) {
				return []db.LatestPart{{Part: "roads", RunID: 9}}, nil
			},
			ResolveSnapshotFunc: func(_ context.Context, snap string) (int64, error) {
				if s, ok := has[snap]; ok {
					return s.ID, nil
				}
				return 0, db.ErrSnapshotNotFound
			},
		}
		store.WithSnapshotFunc = func(id int64, _ string) db.Store {
			pins[name] = id
			for _, s := range has {
				if s.ID == id {
					return &dbtest.MockStore{ViewSnapshotFunc: func(context.Context) (*db.SnapshotCity, error) { return &s, nil }}
				}
			}
			return &dbtest.MockStore{}
		}
		return exportpkg.CityEntry{Config: cfg, City: city, Slug: city.Slug(), Store: store}
	}
	cur := cfg.CityHash(&config.CityConfig{Name: "x"})
	build := func() []exportpkg.CityEntry {
		return []exportpkg.CityEntry{
			entry("A City", map[string]db.SnapshotCity{"v1": {ID: 1, Name: "v1", ConfigHash: "0ld0ld0ld0ld0ld0"}, "v2": {ID: 2, Name: "v2", ConfigHash: cur}}),
			entry("B City", map[string]db.SnapshotCity{"v1": {ID: 1, Name: "v1", ConfigHash: cur}}),
		}
	}
	entries := build()
	var out strings.Builder
	if err := pinNamed(context.Background(), entries, "v1", &out); err != nil {
		t.Fatal(err)
	}
	if pins["A City"] != 1 || pins["B City"] != 1 {
		t.Errorf("pins = %v, want both cities on v1", pins)
	}
	if len(entries[0].Latest) != 1 || len(entries[0].Snapshots) != 2 {
		t.Errorf("A City kept latest %v and snapshots %v, want both read", entries[0].Latest, entries[0].Snapshots)
	}
	// One warning: export's settings warning for A City's v1, with the
	// ending docs/troubleshooting.md quotes for serve.
	a := entries[0]
	want := "warning: " + a.SnapshotSettingsWarning(&db.SnapshotCity{ID: 1, Name: "v1", ConfigHash: "0ld0ld0ld0ld0ld0"}) + "; serving it as created"
	if got := warnings(out.String()); len(got) != 1 || got[0] != want {
		t.Errorf("warnings = %q, want only A City's other-settings warning %q", got, want)
	}

	err := pinNamed(context.Background(), build(), "v2", &out)
	if !errors.Is(err, exportpkg.ErrNoSnapshot) || !strings.Contains(err.Error(), "B City") {
		t.Errorf("v2 (without B City): err = %v, want ErrNoSnapshot naming B City", err)
	}
}
