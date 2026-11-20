package integration

import (
	"bytes"
	"context"
	"errors"
	"maps"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"testing"

	"github.com/jcrussell/solvent-streets/internal/config"
	"github.com/jcrussell/solvent-streets/internal/db"
	exportpkg "github.com/jcrussell/solvent-streets/internal/export"
	"github.com/jcrussell/solvent-streets/internal/resource"
	"github.com/jcrussell/solvent-streets/pkg/cmd/all"
	"github.com/jcrussell/solvent-streets/pkg/cmd/compute"
	"github.com/jcrussell/solvent-streets/pkg/cmd/export"
	servicescmd "github.com/jcrussell/solvent-streets/pkg/cmd/services"
	"github.com/jcrussell/solvent-streets/pkg/cmd/snapshots"
	"github.com/jcrussell/solvent-streets/pkg/cmdutil"
	"github.com/jcrussell/solvent-streets/pkg/iostreams"
)

// TestSnapshots_LatestComputeSwapsOnlyItsPart drives real commands against a
// real store through the latest-compute lifecycle:
//
//  1. `all compute` commits ONE update of the latest compute holding every
//     step's part, fire coverage included, and a census part only when there
//     is census. It creates no snapshot.
//  2. A standalone `roads compute` swaps in a new roads run, and carries every
//     other part (fire coverage, parking, the combined pass) forward
//     untouched. It creates no snapshot either.
//  3. A services compute that finds no stations drops its part, so the old
//     coverage stops being part of the latest compute while everything else
//     stays.
func TestSnapshots_LatestComputeSwapsOnlyItsPart(t *testing.T) {
	ctx := context.Background()
	store := newAllFixtureStore(t, ctx)
	addFixtureStation(t, ctx, store, `{"type":"Point","coordinates":[-121.765,37.684]}`)
	ios, _, _, stderr := iostreams.Test()
	f := allComputeFactory(ios, store)

	// 1. One `all compute`, one update, no snapshot.
	runAllCompute(t, ctx, f, stderr)
	if snaps := listSnapshots(t, store); len(snaps) != 0 {
		t.Fatalf("all compute created %d snapshots, want none: %+v", len(snaps), snaps)
	}
	first := latestOf(t, store)
	wantParts := []string{"combined", "parking", "roads", "service:fire", "sidewalks"}
	if got := slices.Sorted(maps.Keys(first)); !slices.Equal(got, wantParts) {
		t.Errorf("all compute parts = %v, want %v (no census part: the fixture has no blocks)", got, wantParts)
	}
	fireRows := hexMetricCount(t, store, "fire_response_s")
	if fireRows == 0 {
		t.Fatal("fire coverage reached no hexes; the fixture station should reach the fixture roads")
	}

	// 2. A standalone roads recompute swaps roads only.
	rcmd := compute.NewCmdCompute(f, &resource.Pavement{}, nil)
	rcmd.SilenceErrors, rcmd.SilenceUsage = true, true
	rcmd.SetArgs(nil)
	if err := rcmd.ExecuteContext(ctx); err != nil {
		t.Fatalf("roads compute: %v\nstderr:\n%s", err, stderr.String())
	}
	if snaps := listSnapshots(t, store); len(snaps) != 0 {
		t.Fatalf("roads compute created %d snapshots, want none", len(snaps))
	}
	second := latestOf(t, store)
	for part, run := range first {
		swapped := part == "roads"
		if swapped && second[part] == run {
			t.Errorf("%s still points at run %d after a roads recompute; want a new run", part, run)
		}
		if !swapped && second[part] != run {
			t.Errorf("%s changed from run %d to %d on a ROADS recompute; only roads may change", part, run, second[part])
		}
	}
	if len(second) != len(first) {
		t.Errorf("parts after roads compute = %v, want the same set as %v", second, first)
	}
	if got := hexMetricCount(t, store, "fire_response_s"); got != fireRows {
		t.Errorf("fire coverage after a roads recompute = %d hexes, want the same %d (it used to vanish)", got, fireRows)
	}

	// 3. No stations left: the fire part is dropped, nothing else moves.
	if err := store.UpsertFeatures(ctx, resource.FacilityType("fire"), nil, nil); err != nil {
		t.Fatal(err)
	}
	err := servicescmd.RunComputeForCity(ctx, f, servicescmd.DefaultService, nil, nil)
	if !errors.Is(err, cmdutil.ErrNoResults) {
		t.Fatalf("services compute with no stations = %v, want ErrNoResults", err)
	}
	third := latestOf(t, store)
	if _, ok := third["service:fire"]; ok {
		t.Errorf("parts after losing every station = %v, want service:fire dropped", third)
	}
	if third["roads"] != second["roads"] || third["parking"] != second["parking"] {
		t.Errorf("dropping fire moved other parts: %v -> %v", second, third)
	}
	if got := hexMetricCount(t, store, "fire_response_s"); got != 0 {
		t.Errorf("fire coverage still in the latest compute (%d hexes) after every station was removed", got)
	}
}

// TestSnapshots_FrozenAfterReingestAndConfigEdit: a snapshot exports exactly
// as created. After it is created, the boundary and the fire station are
// re-ingested somewhere else, and hex_edge_m and turnout_s are edited, with no
// recompute: a second export is identical to the first in its hex grid (ids,
// areas, the Play board's k), its boundary, and its station markers and
// coverage legend, and it warns that the snapshot was created under other
// settings.
func TestSnapshots_FrozenAfterReingestAndConfigEdit(t *testing.T) {
	ctx := context.Background()
	root, store := newAllFixture(t, ctx)
	addFixtureStation(t, ctx, store, `{"type":"Point","coordinates":[-121.765,37.684]}`)
	cfg := allFixtureConfig()
	ios, _, _, stderr := iostreams.Test()
	f := allFixtureFactory(ios, root, store, cfg)

	runAllCompute(t, ctx, f, stderr)
	snapshotsCreate(t, ctx, f)
	stderr.Reset()
	dirA := runExport(t, ctx, f, stderr)
	if strings.Contains(stderr.String(), "warning:") {
		t.Errorf("export A under the snapshot's own settings warned:\n%s", stderr.String())
	}

	const moved = `{"type":"Polygon","coordinates":[[[-121.772,37.678],[-121.762,37.678],[-121.762,37.688],[-121.772,37.688],[-121.772,37.678]]]}`
	if err := store.SaveBoundary(ctx, moved, "fixture"); err != nil {
		t.Fatal(err)
	}
	addFixtureStation(t, ctx, store, `{"type":"Point","coordinates":[-121.768,37.687]}`)
	cfg.Grid.HexEdgeM = 150
	turnout := 200.0
	cfg.Services = []config.ServiceOverride{{Name: "fire", TurnoutS: &turnout}}

	stderr.Reset()
	dirB := runExport(t, ctx, f, stderr)
	if got := stderr.String(); !strings.Contains(got, "was created under other settings") || !strings.Contains(got, "exporting it as created") {
		t.Errorf("export B lacks the other-settings warning:\n%s", got)
	}

	hexes := 0
	for _, name := range []string{exportpkg.HexGridFileName, exportpkg.BoundaryFileName, exportpkg.HexMetricsFileName} {
		a, b := readDataFile(t, dirA, name), readDataFile(t, dirB, name)
		if !bytes.Equal(a, b) {
			t.Errorf("%s changed after a re-ingest and a config edit with no new snapshot:\nA: %.300s\nB: %.300s", name, a, b)
		}
		if name == exportpkg.HexGridFileName {
			hexes = bytes.Count(a, []byte(`"Feature"`))
			if !bytes.Contains(a, []byte(`"k":`)) {
				t.Errorf("%s carries no Play-board k to compare", name)
			}
		}
		if name == exportpkg.HexMetricsFileName && !bytes.Contains(a, []byte(`"points"`)) {
			t.Errorf("%s carries no station markers to compare", name)
		}
	}
	if hexes == 0 {
		t.Error("the export drew no hexes; the comparison proves nothing")
	}
}

// TestSnapshotsCreate_RefusesStale: after a roads re-ingest and a roads
// compute alone, the combined pass is stale (it read the old roads), so
// create refuses the city and names `all compute`; once
// `all compute` has run, create succeeds.
func TestSnapshotsCreate_RefusesStale(t *testing.T) {
	ctx := context.Background()
	root, store := newAllFixture(t, ctx)
	ios, _, stdout, stderr := iostreams.Test()
	f := allFixtureFactory(ios, root, store, allFixtureConfig())

	runAllCompute(t, ctx, f, stderr)
	snapshotsCreate(t, ctx, f)

	road := feat("road:res", resource.TypeRoads, map[string]string{"highway": "residential"},
		`{"type":"LineString","coordinates":[[-121.769,37.681],[-121.761,37.689]]}`)
	if err := store.UpsertFeatures(ctx, resource.TypeRoads, []db.Feature{road}, nil); err != nil {
		t.Fatal(err)
	}
	rcmd := compute.NewCmdCompute(f, &resource.Pavement{}, nil)
	rcmd.SilenceErrors, rcmd.SilenceUsage = true, true
	rcmd.SetArgs(nil)
	if err := rcmd.ExecuteContext(ctx); err != nil {
		t.Fatalf("roads compute: %v\nstderr:\n%s", err, stderr.String())
	}

	stdout.Reset()
	stderr.Reset()
	all := *f
	all.CityFlagSet = nil // a snapshot covers every configured city
	cmd := snapshots.NewCmdSnapshots(&all)
	cmd.SilenceErrors, cmd.SilenceUsage = true, true
	cmd.SetArgs([]string{"create", "stale"})
	err := cmd.ExecuteContext(ctx)
	out := stdout.String() + stderr.String()
	if !errors.Is(err, cmdutil.ErrSilent) || !strings.Contains(out, "grid-city: refused:") ||
		!strings.Contains(out, "combined") || !strings.Contains(out, "hint: pvmt --city grid-city all compute") {
		t.Fatalf("create after a roads-only recompute = (%v)\n%s\nwant refused, naming the combined pass and all compute", err, out)
	}
	if snaps := listSnapshots(t, store); len(snaps) != 1 {
		t.Errorf("snapshots after the refusal = %d, want the first one only", len(snaps))
	}

	runAllCompute(t, ctx, f, stderr)
	snapshotsCreate(t, ctx, f)
	if snaps := listSnapshots(t, store); len(snaps) != 2 {
		t.Errorf("snapshots after all compute and create = %d, want 2", len(snaps))
	}
}

// addFixtureStation replaces the fixture city's fire stations with one at
// point (a GeoJSON Point).
func addFixtureStation(t *testing.T, ctx context.Context, store db.Store, point string) {
	t.Helper()
	fire := resource.FacilityType("fire")
	station := feat("osm:node:1", fire, map[string]string{"amenity": "fire_station"}, point)
	station.SourceAPI = "overpass"
	if err := store.UpsertFeatures(ctx, fire, []db.Feature{station}, nil); err != nil {
		t.Fatal(err)
	}
}

func runAllCompute(t *testing.T, ctx context.Context, f *cmdutil.Factory, stderr *bytes.Buffer) {
	t.Helper()
	cmd := all.NewCmdAll(f)
	cmd.SilenceErrors, cmd.SilenceUsage = true, true
	cmd.SetArgs([]string{"compute"})
	if err := cmd.ExecuteContext(ctx); err != nil {
		t.Fatalf("all compute: %v\nstderr:\n%s", err, stderr.String())
	}
}

// runExport exports f's city to a fresh directory, with any extra flags, and
// returns it.
func runExport(t *testing.T, ctx context.Context, f *cmdutil.Factory, stderr *bytes.Buffer, flags ...string) string {
	t.Helper()
	dir := filepath.Join(t.TempDir(), "site")
	cmd := export.NewCmdExport(f, nil)
	cmd.SilenceErrors, cmd.SilenceUsage = true, true
	cmd.SetArgs(append([]string{"--output", dir}, flags...))
	if err := cmd.ExecuteContext(ctx); err != nil {
		t.Fatalf("export: %v\nstderr:\n%s", err, stderr.String())
	}
	return dir
}

func readDataFile(t *testing.T, dir, name string) []byte {
	t.Helper()
	b, err := os.ReadFile(filepath.Join(dir, exportpkg.DataDirName, name))
	if err != nil {
		t.Fatal(err)
	}
	return b
}

// latestOf is the store's latest compute (it is a WithLatestCompute view)
// as part -> run.
func latestOf(t *testing.T, store db.Store) map[string]int64 {
	t.Helper()
	cfg := allFixtureConfig()
	latest, err := store.LatestCompute(context.Background(), cfg.CityHash(&cfg.Cities[0]))
	if err != nil {
		t.Fatal(err)
	}
	out := map[string]int64{}
	for _, p := range latest {
		out[p.Part] = p.RunID
	}
	return out
}

func listSnapshots(t *testing.T, store db.Store) []db.SnapshotCity {
	t.Helper()
	snaps, err := store.ListSnapshots(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	return snaps
}

func hexMetricCount(t *testing.T, store db.Store, metric string) int {
	t.Helper()
	rows, err := store.ListHexMetrics(context.Background(), metric, "")
	if err != nil {
		t.Fatal(err)
	}
	return len(rows)
}

// TestExport_SnapshotByName: export publishes the newest snapshot by default
// and the named one with --snapshot, byte for byte as created; a configured
// city the named snapshot does not cover refuses the export before --clean
// touches the previous site.
func TestExport_SnapshotByName(t *testing.T) {
	ctx := context.Background()
	root, store := newAllFixture(t, ctx)
	cfg := allFixtureConfig()
	ios, _, _, stderr := iostreams.Test()
	f := allFixtureFactory(ios, root, store, cfg)

	runAllCompute(t, ctx, f, stderr)
	snapshotsCreate(t, ctx, f)
	first := listSnapshots(t, store)[0].Name
	dirFirst := runExport(t, ctx, f, stderr)

	// Recompute at another edge and snapshot again: the default export
	// follows the newest; --snapshot reproduces the first.
	cfg.Grid.HexEdgeM = 150
	runAllCompute(t, ctx, f, stderr)
	snapshotsCreate(t, ctx, f)
	dirNewest := runExport(t, ctx, f, stderr)
	dirNamed := runExport(t, ctx, f, stderr, "--snapshot", first)
	grid := exportpkg.HexGridFileName
	if bytes.Equal(readDataFile(t, dirFirst, grid), readDataFile(t, dirNewest, grid)) {
		t.Fatal("the newest snapshot exported the first one's grid; the comparison proves nothing")
	}
	if !bytes.Equal(readDataFile(t, dirFirst, grid), readDataFile(t, dirNamed, grid)) {
		t.Errorf("--snapshot %s did not reproduce its grid", first)
	}
	var meta exportpkg.MetaJSON
	readJSON(t, filepath.Join(dirNamed, exportpkg.DataDirName, exportpkg.MetaFileName), &meta)
	if meta.Snapshot != first || meta.SnapshotDate == "" {
		t.Errorf("meta.json snapshot = %q (%q), want %s with its date", meta.Snapshot, meta.SnapshotDate, first)
	}

	// A second configured city, with a boundary but in no snapshot.
	other := config.CityConfig{Name: "Other City"}
	id, err := root.EnsureCity(ctx, other.Slug(), other.Name, cfg.CityConfigID(&other))
	if err != nil {
		t.Fatal(err)
	}
	if err := root.ForCity(id).SaveBoundary(ctx, `{"type":"Polygon","coordinates":[[[-121.7,37.6],[-121.6,37.6],[-121.6,37.7],[-121.7,37.6]]]}`, "fixture"); err != nil {
		t.Fatal(err)
	}
	cfg.Cities = append(cfg.Cities, other)
	all := *f
	all.CityFlagSet = nil
	before, err := os.ReadDir(dirNamed)
	if err != nil {
		t.Fatal(err)
	}
	cmd := export.NewCmdExport(&all, nil)
	cmd.SilenceErrors, cmd.SilenceUsage = true, true
	cmd.SetArgs([]string{"--output", dirNamed, "--clean", "--snapshot", first})
	err = cmd.ExecuteContext(ctx)
	if !errors.Is(err, exportpkg.ErrNoSnapshot) || !strings.Contains(err.Error(), "Other City") {
		t.Fatalf("export of a snapshot missing a city: err = %v, want ErrNoSnapshot naming Other City", err)
	}
	after, err := os.ReadDir(dirNamed)
	if err != nil || len(after) != len(before) {
		t.Errorf("the refused export touched the previous site: %d entries, was %d (%v)", len(after), len(before), err)
	}
}
