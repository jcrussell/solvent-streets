package db

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"path/filepath"
	"sync/atomic"
	"testing"
)

// testSnapshots numbers the snapshots createCitySnapshot names.
var testSnapshots atomic.Int64

// createCitySnapshot freezes store's city alone into a new snapshot named
// "t<n>", through RootStore.CreateSnapshot, and returns the city's entry.
// A refusal is returned unwrapped: the city's own error.
func createCitySnapshot(store Store, hash string, hexEdgeM float64, check CheckFunc) (*SnapshotCity, error) {
	s := store.(*sqliteStore)
	root := &RootStore{db: s.db}
	snap, err := root.CreateSnapshot(context.Background(), fmt.Sprintf("t%d", testSnapshots.Add(1)), testEpoch,
		[]SnapshotRequest{{CityID: s.cityID, ConfigHash: hash, HexEdgeM: hexEdgeM, Check: check}})
	if refused, ok := errors.AsType[*SnapshotRefusedError](err); ok {
		return nil, refused.Refusals[0].Err
	}
	if err != nil {
		return nil, err
	}
	return &snap.Cities[0], nil
}

// deleteSnapshotID deletes the snapshot with id through
// RootStore.DeleteSnapshot; (false, nil) when there is none.
func deleteSnapshotID(store Store, id int64) (bool, error) {
	s := store.(*sqliteStore)
	var name string
	err := s.db.QueryRowContext(context.Background(), `SELECT name FROM snapshots WHERE id = ?`, id).Scan(&name)
	if errors.Is(err, sql.ErrNoRows) {
		return false, nil
	}
	if err != nil {
		return false, err
	}
	return (&RootStore{db: s.db}).DeleteSnapshot(context.Background(), name)
}

// openTwoCities opens a database with cities a and b, each with a boundary
// and a committed roads run under hash "h".
func openTwoCities(t *testing.T) (root *RootStore, a, b int64) {
	t.Helper()
	ctx := context.Background()
	root, err := Open(":memory:")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = root.Close() })
	a, _ = root.EnsureCity(ctx, "a", "A", "test")
	b, _ = root.EnsureCity(ctx, "b", "B", "test")
	for _, id := range []int64{a, b} {
		store := root.ForCity(id)
		if err := store.SaveBoundary(ctx, geomA, "nominatim"); err != nil {
			t.Fatal(err)
		}
		commitLatestRun(t, store, "roads", roadsRun(t, store, float64(100*id)))
	}
	return root, a, b
}

func requests(ids ...int64) []SnapshotRequest {
	out := make([]SnapshotRequest, len(ids))
	for i, id := range ids {
		out[i] = SnapshotRequest{CityID: id, ConfigHash: "h", HexEdgeM: testEdge}
	}
	return out
}

// TestCreateSnapshot_Tag: one create freezes every city under one name, the
// tag lists them by slug with their manifests, and each city's store reads
// its own runs from it.
func TestCreateSnapshot_Tag(t *testing.T) {
	ctx := context.Background()
	root, a, b := openTwoCities(t)
	snap, err := root.CreateSnapshot(ctx, "v0.5.0", 7, requests(b, a))
	if err != nil {
		t.Fatal(err)
	}
	if snap.Name != "v0.5.0" || snap.ComputeEpoch != 7 || snap.CityCount != 2 || snap.CreatedAt.IsZero() {
		t.Fatalf("snapshot = %+v", snap)
	}
	if snap.Cities[0].CitySlug != "a" || snap.Cities[1].CitySlug != "b" {
		t.Errorf("cities = %s, %s; want a, b (by slug)", snap.Cities[0].CitySlug, snap.Cities[1].CitySlug)
	}
	for _, c := range snap.Cities {
		if c.ID != snap.ID || c.Name != snap.Name || c.ConfigHash != "h" || c.HexEdgeM != testEdge ||
			len(c.Parts) != 1 || c.Parts[0].Part != "roads" || c.BoundaryDigest == "" {
			t.Errorf("city %s = %+v", c.CitySlug, c)
		}
	}
	for _, id := range []int64{a, b} {
		if got := roadsArea(t, root.ForCity(id).WithSnapshot(snap.ID)); got != float64(100*id) {
			t.Errorf("city %d pinned roads = %v, want %v", id, got, 100*id)
		}
		got, err := root.ForCity(id).ResolveSnapshot(ctx, "v0.5.0")
		if err != nil || got != snap.ID {
			t.Errorf("city %d ResolveSnapshot = (%d, %v), want %d", id, got, err, snap.ID)
		}
	}
	got, err := root.Snapshot(ctx, "v0.5.0")
	if err != nil || got.ID != snap.ID || len(got.Cities) != 2 {
		t.Errorf("Snapshot(v0.5.0) = (%+v, %v)", got, err)
	}
	if _, err := root.Snapshot(ctx, "nope"); !errors.Is(err, ErrSnapshotNotFound) {
		t.Errorf("unknown name: err = %v, want ErrSnapshotNotFound", err)
	}
}

// TestCreateSnapshot_AllOrNothing: one refused city refuses the whole
// snapshot, every refusal is reported, and nothing is written.
func TestCreateSnapshot_AllOrNothing(t *testing.T) {
	ctx := context.Background()
	root, a, b := openTwoCities(t)
	empty, _ := root.EnsureCity(ctx, "c", "C", "test")
	refuse := errors.New("stale")
	reqs := requests(a, b, empty)
	reqs[1].Check = func([]LatestPart, []InputState) error { return refuse }
	_, err := root.CreateSnapshot(ctx, "v1", testEpoch, reqs)
	var refused *SnapshotRefusedError
	if !errors.As(err, &refused) {
		t.Fatalf("err = %v, want a *SnapshotRefusedError", err)
	}
	if len(refused.Refusals) != 2 || refused.Refusals[0].CityID != b || !errors.Is(refused.Refusals[0].Err, refuse) ||
		refused.Refusals[1].CityID != empty || !errors.Is(refused.Refusals[1].Err, ErrNothingComputed) {
		t.Errorf("refusals = %+v, want b (its check) then c (nothing computed)", refused.Refusals)
	}
	for _, table := range []string{"snapshots", "snapshot_cities", "snapshot_parts", "snapshot_boundaries"} {
		if n := countRows(t, root, `SELECT COUNT(*) FROM `+table); n != 0 {
			t.Errorf("a refused create left %d %s row(s)", n, table)
		}
	}
	// The name stays free.
	if _, err := root.CreateSnapshot(ctx, "v1", testEpoch, requests(a, b)); err != nil {
		t.Errorf("create after a refusal: %v", err)
	}
}

// TestCreateSnapshot_Refuses: a taken name, an invalid name, a city twice, an
// empty hash or a zero edge.
func TestCreateSnapshot_Refuses(t *testing.T) {
	ctx := context.Background()
	root, a, b := openTwoCities(t)
	if _, err := root.CreateSnapshot(ctx, "v1", testEpoch, requests(a)); err != nil {
		t.Fatal(err)
	}
	if _, err := root.CreateSnapshot(ctx, "v1", testEpoch, requests(a, b)); !errors.Is(err, ErrSnapshotExists) {
		t.Errorf("taken name: err = %v, want ErrSnapshotExists", err)
	}
	bad := requests(a)
	bad[0].ConfigHash = ""
	zero := requests(a)
	zero[0].HexEdgeM = 0
	for name, c := range map[string]struct {
		name string
		reqs []SnapshotRequest
	}{
		"invalid name": {"-v2", requests(a)},
		"no cities":    {"v2", nil},
		"city twice":   {"v2", requests(a, a)},
		"empty hash":   {"v2", bad},
		"zero edge":    {"v2", zero},
	} {
		if _, err := root.CreateSnapshot(ctx, c.name, testEpoch, c.reqs); err == nil {
			t.Errorf("%s was accepted", name)
		}
	}
	if n := countRows(t, root, `SELECT COUNT(*) FROM snapshots`); n != 1 {
		t.Errorf("%d snapshots, want 1", n)
	}
}

// TestCheckSnapshotName pins the one name rule.
func TestCheckSnapshotName(t *testing.T) {
	for _, ok := range []string{"v0.5.0", "2026-11", "a", "release_1", "A-b.c_d"} {
		if err := CheckSnapshotName(ok); err != nil {
			t.Errorf("%q: %v", ok, err)
		}
	}
	long := "a"
	for len(long) < 65 {
		long += "a"
	}
	for _, bad := range []string{"", "-v1", ".v1", "_v1", "v/1", "v 1", "v1\n", long, "latest!", "latest"} {
		if err := CheckSnapshotName(bad); err == nil {
			t.Errorf("%q was accepted", bad)
		}
	}
}

// TestDeleteSnapshot_ThenCreateReusesRuns: delete removes the tag with its
// cities and manifests (cascade), keeps runs the latest compute still
// references, so a create of the same name freezes the same runs; and a run
// only the deleted snapshot held goes with it.
func TestDeleteSnapshot_ThenCreateReusesRuns(t *testing.T) {
	ctx := context.Background()
	root, a, b := openTwoCities(t)
	first, err := root.CreateSnapshot(ctx, "v1", testEpoch, requests(a, b))
	if err != nil {
		t.Fatal(err)
	}
	oldRoadsA := first.Cities[0].Parts[0].RunID
	// Recompute a: its old run is now held only by v1.
	storeA := root.ForCity(a)
	newRoadsA := commitLatestRun(t, storeA, "roads", roadsRun(t, storeA, 999))

	if ok, err := root.DeleteSnapshot(ctx, "v1"); err != nil || !ok {
		t.Fatalf("DeleteSnapshot = (%v, %v)", ok, err)
	}
	if ok, err := root.DeleteSnapshot(ctx, "v1"); err != nil || ok {
		t.Errorf("second delete = (%v, %v), want (false, nil)", ok, err)
	}
	for _, table := range []string{"snapshots", "snapshot_cities", "snapshot_parts", "snapshot_boundaries"} {
		if n := countRows(t, root, `SELECT COUNT(*) FROM `+table); n != 0 {
			t.Errorf("delete left %d %s row(s)", n, table)
		}
	}
	if n := countRows(t, root, `SELECT COUNT(*) FROM runs WHERE id = ?`, oldRoadsA); n != 0 {
		t.Error("the run only v1 held survived its delete")
	}
	if n := countRows(t, root, `SELECT COUNT(*) FROM hex_metrics WHERE run_id = ?`, oldRoadsA); n != 0 {
		t.Error("rows of the swept run survived")
	}

	again, err := root.CreateSnapshot(ctx, "v1", testEpoch, requests(a, b))
	if err != nil {
		t.Fatal(err)
	}
	if again.ID == first.ID {
		t.Error("the re-created snapshot reused the deleted id")
	}
	if got := again.Cities[0].Parts[0].RunID; got != newRoadsA {
		t.Errorf("a's roads = run %d, want the recomputed %d", got, newRoadsA)
	}
	if got, want := again.Cities[1].Parts[0].RunID, first.Cities[1].Parts[0].RunID; got != want {
		t.Errorf("b's roads = run %d, want the unchanged %d reused", got, want)
	}
}

// TestRootListSnapshots: newest first, with city counts.
func TestRootListSnapshots(t *testing.T) {
	ctx := context.Background()
	root, a, b := openTwoCities(t)
	if snaps, err := root.ListSnapshots(ctx); err != nil || snaps != nil {
		t.Fatalf("empty ListSnapshots = (%v, %v), want (nil, nil)", snaps, err)
	}
	if _, err := root.CreateSnapshot(ctx, "v1", testEpoch, requests(a)); err != nil {
		t.Fatal(err)
	}
	if _, err := root.CreateSnapshot(ctx, "v2", testEpoch, requests(a, b)); err != nil {
		t.Fatal(err)
	}
	snaps, err := root.ListSnapshots(ctx)
	if err != nil {
		t.Fatal(err)
	}
	if len(snaps) != 2 || snaps[0].Name != "v2" || snaps[0].CityCount != 2 || snaps[1].Name != "v1" || snaps[1].CityCount != 1 {
		t.Errorf("snapshots = %+v, want v2 (2 cities) then v1 (1)", snaps)
	}
	if snaps[0].Cities != nil {
		t.Error("ListSnapshots filled Cities")
	}
	// A city's list holds only the snapshots that cover it.
	cityB, err := root.ForCity(b).ListSnapshots(ctx)
	if err != nil || len(cityB) != 1 || cityB[0].Name != "v2" {
		t.Errorf("b's snapshots = (%+v, %v), want just v2", cityB, err)
	}
	if _, err := root.ForCity(b).ResolveSnapshot(ctx, "v1"); !errors.Is(err, ErrSnapshotNotFound) {
		t.Errorf("b resolving v1 (without it): err = %v, want ErrSnapshotNotFound", err)
	}
}

// TestSnapshotNames lists the names newest first through a read-only
// connection, and nothing for a path with no database.
func TestSnapshotNames(t *testing.T) {
	path := filepath.Join(t.TempDir(), FileName)
	root, err := Open(path)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = root.Close() })
	ctx := context.Background()
	for i, name := range []string{"2026-10", "2026-11"} {
		if _, err := root.db.ExecContext(ctx, `INSERT INTO snapshots (name, created_at, compute_epoch) VALUES (?, ?, 1)`, name, 1000+i); err != nil {
			t.Fatal(err)
		}
	}
	names, err := SnapshotNames(ctx, path)
	if err != nil || len(names) != 2 || names[0] != "2026-11" || names[1] != "2026-10" {
		t.Errorf("SnapshotNames = %v, %v; want [2026-11 2026-10]", names, err)
	}
}
