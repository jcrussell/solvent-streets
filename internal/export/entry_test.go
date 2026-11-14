package export

import (
	"context"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/jcrussell/solvent-streets/internal/config"
	"github.com/jcrussell/solvent-streets/internal/db"
	"github.com/jcrussell/solvent-streets/internal/db/dbtest"
	"github.com/jcrussell/solvent-streets/internal/hint"
	"github.com/jcrussell/solvent-streets/internal/resource"
	"github.com/jcrussell/solvent-streets/internal/results"
)

// TestPinNewest_PinsNewestAnyHash: an export reads ONE snapshot per city,
// the newest whatever settings it was created under, warns when those are
// not the current ones, notes a latest compute not yet snapshotted, and keeps
// reading the pinned snapshot after a later compute or create.
func TestPinNewest_PinsNewestAnyHash(t *testing.T) {
	ctx := context.Background()
	cfg := &config.Config{}
	c := newSQLiteCity(t, cfg, "Test City", exportBoundaryA)
	hash := cfg.CityHash(&c.city)
	roads := func(h string, area float64) int64 {
		t.Helper()
		run, err := c.store.CreateRun(ctx, "roads", h, nil)
		if err != nil {
			t.Fatal(err)
		}
		if err := c.store.SaveCityMetrics(ctx, run.ID, results.TotalRows(resource.TypeRoads, results.Totals{Area: area})); err != nil {
			t.Fatal(err)
		}
		return run.ID
	}
	if _, err := dbtest.Publish(ctx, c.store, hash, 100, map[string]int64{"roads": roads(hash, 100)}); err != nil {
		t.Fatal(err)
	}
	newest, err := dbtest.Publish(ctx, c.store, "another-config", 100, map[string]int64{"roads": roads("another-config", 999)})
	if err != nil {
		t.Fatal(err)
	}
	if err := c.store.CommitLatest(ctx, hash, map[string]int64{"roads": roads(hash, 200)}); err != nil {
		t.Fatal(err)
	}

	entries := []CityEntry{c.entry()}
	var warn strings.Builder
	if err := PinNewest(ctx, entries, false, &warn); err != nil {
		t.Fatal(err)
	}
	if _, err := dbtest.Publish(ctx, c.store, hash, 100, map[string]int64{"roads": roads(hash, 300)}); err != nil { // lands "mid-export"
		t.Fatal(err)
	}
	r, ok, err := results.LoadTotal(ctx, entries[0].Store, resource.TypeRoads)
	if err != nil || !ok || r.Area != 999 {
		t.Errorf("pinned read = (%+v, %v), want the newest snapshot's 999 whatever its hash", r, err)
	}
	got := warn.String()
	if want := fmt.Sprintf("snapshot #%d was created under other settings", newest.ID); !strings.Contains(got, want) ||
		!strings.Contains(got, "exporting it as created") {
		t.Errorf("warnings = %q; want the other-settings warning for snapshot %d", got, newest.ID)
	}
	if !strings.Contains(got, "note: Test City: the latest compute has roads not yet snapshotted") {
		t.Errorf("warnings = %q; want a note that roads is not yet snapshotted", got)
	}
}

// TestPinNewest_RefusesCitiesWithNoSnapshot: one city without a snapshot
// refuses the whole export, naming every such city with the command that
// fixes it (all compute first when nothing is computed either), and pins
// nothing. In a multi-city export a city with no boundary either is left for
// the exporter to skip.
func TestPinNewest_RefusesCitiesWithNoSnapshot(t *testing.T) {
	ctx := context.Background()
	cfg := &config.Config{}
	pins := 0
	entry := func(name, boundary string, snaps []db.Snapshot, latest []db.LatestPart) CityEntry {
		city := config.CityConfig{Name: name}
		return CityEntry{Config: cfg, City: city, Slug: city.Slug(), Store: &dbtest.MockStore{
			ListSnapshotsFunc: func(context.Context) ([]db.Snapshot, error) { return snaps, nil },
			LatestComputeFunc: func(context.Context, string) ([]db.LatestPart, error) { return latest, nil },
			GetBoundaryFunc:   func(context.Context) (string, error) { return boundary, nil },
			WithSnapshotFunc:  func(int64) db.Store { pins++; return &dbtest.MockStore{} },
		}}
	}
	entries := []CityEntry{
		entry("Done", exportBoundaryA, []db.Snapshot{{ID: 1, ConfigHash: cfg.CityHash(&config.CityConfig{Name: "Done"}), Parts: paved}}, nil),
		entry("Computed", exportBoundaryA, nil, []db.LatestPart{{Part: "roads", RunID: 1}}),
		entry("Bare", exportBoundaryA, nil, nil),
		entry("Unbounded", "", nil, nil),
	}
	err := PinNewest(ctx, entries, true, io.Discard)
	var h *hint.ErrHint
	if !errors.Is(err, ErrNoSnapshot) || !errors.As(err, &h) {
		t.Fatalf("err = %v, want a hinted ErrNoSnapshot", err)
	}
	for _, want := range []string{
		"Computed: pvmt --city computed snapshots create",
		"Bare: pvmt --city bare all compute && pvmt --city bare snapshots create",
	} {
		if !strings.Contains(h.Hint, want) {
			t.Errorf("hint %q lacks %q", h.Hint, want)
		}
	}
	if strings.Contains(h.Hint, "Done") || strings.Contains(h.Hint, "Unbounded") {
		t.Errorf("hint %q names a city with a snapshot or one the export skips", h.Hint)
	}
	if !strings.Contains(err.Error(), "2 cities have none") {
		t.Errorf("err = %q, want the count of cities with none", err)
	}
	if pins != 0 {
		t.Errorf("a refused export pinned %d cities, want none", pins)
	}
	if err := PinNewest(ctx, entries[3:], false, io.Discard); !errors.Is(err, ErrNoSnapshot) {
		t.Errorf("single-city export of a city with no snapshot or boundary = %v, want ErrNoSnapshot", err)
	}
}

// TestExport_NoSnapshotWritesNothing: the refusal comes before any file is
// written.
func TestExport_NoSnapshotWritesNothing(t *testing.T) {
	cfg := &config.Config{}
	entry := exportTestEntry(cfg, "Solo City", "solo-city", exportBoundaryA, nil)
	entry.Store.(*dbtest.MockStore).ListSnapshotsFunc = func(context.Context) ([]db.Snapshot, error) { return nil, nil }
	dir := filepath.Join(t.TempDir(), "site")
	e := New([]CityEntry{entry}, cfg, dir, "metric", testAssets(t))
	if err := e.Run(context.Background()); !errors.Is(err, ErrNoSnapshot) {
		t.Fatalf("Run = %v, want ErrNoSnapshot", err)
	}
	if _, err := os.Stat(dir); !errors.Is(err, os.ErrNotExist) {
		t.Errorf("a refused export created %s (stat err %v)", dir, err)
	}
}

// TestSnapshotSettingsWarning_IncludedCityUsesSourceHash: a city pulled in via
// [[include]] reads the snapshots its SOURCE config wrote, not the including
// config's, and its source's snapshot counts as current. Without this every
// city of the union config would warn of other settings, and its computes
// would write a namespace nothing else reads — which is exactly why
// `make site` could not run.
//
// The union also gives the city a forecast calibration its source lacks, the
// way examples/all backfills cited figures; [forecast] is not a compute input,
// so the source's snapshot still applies.
func TestSnapshotSettingsWarning_IncludedCityUsesSourceHash(t *testing.T) {
	dir := t.TempDir()
	write := func(rel, data string) string {
		t.Helper()
		p := filepath.Join(dir, rel)
		if err := os.MkdirAll(filepath.Dir(p), 0o755); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(p, []byte(data), 0o644); err != nil {
			t.Fatal(err)
		}
		return p
	}
	leaf, err := config.Load(write("bay/pvmt.toml", "config_id = \"bay-area-ca\"\n[grid]\nhex_edge_m = 80\n"+
		"[[cities]]\nname = \"Oakland, CA\"\noverpass = true\n"))
	if err != nil {
		t.Fatal(err)
	}
	write("top50/pvmt.toml", "config_id = \"top50\"\n[[cities]]\nname = \"Oakland, CA\"\noverpass = true\n"+
		"[cities.forecast]\ninitial_pci = 58\n")
	cfg, err := config.Load(write("all/pvmt.toml", "[[include]]\npath = \"../bay/pvmt.toml\"\n"+
		"[[include]]\npath = \"../top50/pvmt.toml\"\n"))
	if err != nil {
		t.Fatal(err)
	}
	city := cfg.Cities[0]
	if got := cfg.ResolvedForecast(&city).InitialPCI; got != 58 {
		t.Fatalf("fixture: union initial_pci = %g, want the backfilled 58", got)
	}
	sourceHash := leaf.CityHash(&leaf.Cities[0])
	entry := CityEntry{
		Config: cfg,
		City:   city,
		Slug:   city.Slug(),
		Store: &dbtest.MockStore{
			ListSnapshotsFunc: func(_ context.Context) ([]db.Snapshot, error) {
				// Only the source's snapshot exists — the union config never
				// computed anything of its own.
				return []db.Snapshot{{ID: 1, ConfigHash: sourceHash, Parts: paved}}, nil
			},
		},
	}
	if w := entry.SnapshotSettingsWarning(&db.Snapshot{ID: 1, ConfigHash: sourceHash, Parts: paved}); w != "" {
		t.Fatalf("included city should match its source snapshot, got warning %q", w)
	}
	if got := cfg.CityConfigID(&city); got != "bay-area-ca" {
		t.Errorf("CityConfigID = %q, want the source's cities row %q", got, "bay-area-ca")
	}
}

// TestSnapshotSettingsWarning_IncludedServicesMatchSource pins
// solvent-streets-qtue: an [[include]]d city whose own config sets
// [[services]] runs under them in the union too, so the union reads the
// snapshot its source computed instead of refusing it as diverged.
func TestSnapshotSettingsWarning_IncludedServicesMatchSource(t *testing.T) {
	dir := t.TempDir()
	write := func(rel, data string) string {
		t.Helper()
		p := filepath.Join(dir, rel)
		if err := os.MkdirAll(filepath.Dir(p), 0o755); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(p, []byte(data), 0o644); err != nil {
			t.Fatal(err)
		}
		return p
	}
	leaf, err := config.Load(write("leaf/pvmt.toml", "config_id = \"leaf\"\n[[services]]\nname = \"fire\"\nturnout_s = 60\n"+
		"[[cities]]\nname = \"Reno, NV\"\noverpass = true\n"))
	if err != nil {
		t.Fatal(err)
	}
	cfg, err := config.Load(write("all/pvmt.toml", "[[include]]\npath = \"../leaf/pvmt.toml\"\n"))
	if err != nil {
		t.Fatal(err)
	}
	city := cfg.Cities[0]
	leafHash := leaf.CityHash(&leaf.Cities[0])
	entry := CityEntry{
		Config: cfg, City: city, Slug: city.Slug(),
		Store: &dbtest.MockStore{
			ListSnapshotsFunc: func(_ context.Context) ([]db.Snapshot, error) {
				// Only the leaf's own snapshot exists.
				return []db.Snapshot{{ID: 1, ConfigHash: leafHash, Parts: paved}}, nil
			},
		},
	}
	if w := entry.SnapshotSettingsWarning(&db.Snapshot{ID: 1, ConfigHash: leafHash, Parts: paved}); w != "" {
		t.Fatalf("included city should read its source's snapshot, got warning %q", w)
	}
	if got := cfg.ResolvedServices(&entry.City); len(got) != 1 || got[0].TurnoutS == nil || *got[0].TurnoutS != 60 {
		t.Errorf("ResolvedServices = %+v, want the leaf's fire turnout_s = 60", got)
	}
}

// paved is the manifest of a snapshot that holds a pavement compute, which
// every exported snapshot has (snapshots create refuses one without).
var paved = []db.SnapshotPart{{Part: "roads", RunID: 1}}

// TestRequirePavement: export's defensive check refuses a snapshot that holds
// only metric parts (fire coverage, census), which would publish a blank
// city.
func TestRequirePavement(t *testing.T) {
	entry := CityEntry{City: config.CityConfig{Name: "Test City"}}
	err := RequirePavement(entry, &db.Snapshot{ID: 3, Parts: []db.SnapshotPart{{Part: "service:fire"}}})
	if err == nil || !strings.Contains(err.Error(), "no pavement") || !strings.Contains(err.Error(), "service:fire") {
		t.Errorf("err = %v, want one naming the missing pavement compute", err)
	}
	if err := RequirePavement(entry, &db.Snapshot{ID: 4, Parts: paved}); err != nil {
		t.Errorf("paved snapshot: %v", err)
	}
}

// TestSnapshotSettingsWarning: a snapshot under the current settings is
// silent; one under others names the snapshot and how to publish the current
// settings.
func TestSnapshotSettingsWarning(t *testing.T) {
	cfg := &config.Config{}
	city := config.CityConfig{Name: "Test City"}
	entry := CityEntry{Config: cfg, City: city, Slug: city.Slug()}
	if w := entry.SnapshotSettingsWarning(&db.Snapshot{ID: 1, ConfigHash: cfg.CityHash(&city)}); w != "" {
		t.Errorf("current snapshot warned %q", w)
	}
	w := entry.SnapshotSettingsWarning(&db.Snapshot{ID: 2, ConfigHash: "0ld0ld0ld0ld0ld0"})
	for _, want := range []string{"snapshot #2", "0ld0ld0ld0ld0ld0", "hex_edge_m", "pvmt --city test-city snapshots create"} {
		if !strings.Contains(w, want) {
			t.Errorf("warning %q lacks %q", w, want)
		}
	}
}

// StaleSettings is the one test for "computed, but not readable under the
// current settings" that status and forecast share.
func TestStaleSettings(t *testing.T) {
	cfg := &config.Config{}
	city := &config.CityConfig{Name: "Test City"}
	old := []db.Snapshot{{ID: 1, ConfigHash: "0ld0ld0ld0ld0ld0"}}
	latest := []db.LatestPart{{Part: "roads", RunID: 2}}
	for _, tc := range []struct {
		name   string
		snaps  []db.Snapshot
		latest []db.LatestPart
		want   bool
	}{
		{"never computed", nil, nil, false},
		{"computed under the current settings", old, latest, false},
		{"computed only under other settings", old, nil, true},
	} {
		if got := StaleSettings(cfg, city, tc.snaps, tc.latest); got != tc.want {
			t.Errorf("%s: StaleSettings = %t, want %t", tc.name, got, tc.want)
		}
	}
}
