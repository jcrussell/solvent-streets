package db

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"maps"
	"path/filepath"
	"slices"
	"sync"
	"testing"
	"time"
)

// testEdge is the hex edge test snapshots are committed at.
const testEdge = 100.0

// commitRun creates a run for producer under hash, lets write store rows
// tagged with it, commits it into the latest compute and snapshots that
// (snapshotLatest). It returns the run and snapshot ids.
func commitRun(t *testing.T, store Store, hash, producer string, write func(run int64)) (int64, int64) {
	t.Helper()
	ctx := context.Background()
	run, err := store.CreateRun(ctx, producer, hash, nil)
	if err != nil {
		t.Fatalf("CreateRun(%s): %v", producer, err)
	}
	if write != nil {
		write(run.ID)
	}
	if err := store.CommitLatest(ctx, hash, testEpoch, map[string]int64{producer: run.ID}); err != nil {
		t.Fatalf("CommitLatest(%s): %v", producer, err)
	}
	return run.ID, snapshotLatest(t, store, hash)
}

// snapshotLatest freezes the latest compute under hash, unchecked, and
// returns the snapshot's id.
func snapshotLatest(t *testing.T, store Store, hash string) int64 {
	t.Helper()
	snap, err := createCitySnapshot(store, hash, testEdge, nil)
	if err != nil {
		t.Fatalf("CreateSnapshot(%s): %v", hash, err)
	}
	return snap.ID
}

// roadsRun / fireRun / parkingRun write recognisable rows, so a read can
// tell which run they came from by the value. roadsRun writes two city rows
// (total and one cohort class) and one hex row.
func roadsRun(t *testing.T, store Store, area float64) func(int64) {
	t.Helper()
	return func(run int64) {
		ctx := context.Background()
		if err := store.SaveCityMetrics(ctx, run, []CityMetric{
			{Metric: "paved_area_m2", Subject: "roads", Value: area},
			{Metric: "paved_area_m2", Subject: "roads", Class: "primary", Value: area},
		}); err != nil {
			t.Fatal(err)
		}
		if err := store.SaveHexMetrics(ctx, run, []HexMetric{{HexID: "h1", Metric: "paved_area_m2", Subject: "roads", Value: area}}); err != nil {
			t.Fatal(err)
		}
	}
}

func fireRun(t *testing.T, store Store, rows ...HexMetric) func(int64) {
	t.Helper()
	return func(run int64) {
		for i := range rows {
			rows[i].Metric = "fire_response_s"
		}
		if err := store.SaveHexMetrics(context.Background(), run, rows); err != nil {
			t.Fatal(err)
		}
	}
}

func parkingRun(t *testing.T, store Store, area float64) func(int64) {
	t.Helper()
	return func(run int64) {
		if err := store.SaveCityMetrics(context.Background(), run, []CityMetric{{Metric: "paved_area_m2", Subject: "parking", Value: area}}); err != nil {
			t.Fatal(err)
		}
	}
}

// cityArea is subject's paved-area city total in the view; ok is false when
// the view holds none.
func cityArea(t *testing.T, store Store, subject string) (area float64, ok bool) {
	t.Helper()
	rows, err := store.ListCityMetrics(context.Background(), []string{subject})
	if err != nil {
		t.Fatalf("ListCityMetrics(%s): %v", subject, err)
	}
	for _, m := range rows {
		if m.Metric == "paved_area_m2" && m.Class == "" {
			return m.Value, true
		}
	}
	return 0, false
}

func roadsArea(t *testing.T, store Store) float64 {
	t.Helper()
	area, ok := cityArea(t, store, "roads")
	if !ok {
		t.Fatal("no roads total in the view")
	}
	return area
}

func fireHexes(t *testing.T, store Store) []string {
	t.Helper()
	rows, err := store.ListHexMetrics(context.Background(), "fire_response_s", "")
	if err != nil {
		t.Fatal(err)
	}
	ids := make([]string, len(rows))
	for i, r := range rows {
		ids[i] = r.HexID
	}
	return ids
}

func parts(t *testing.T, store Store, snapID int64) map[string]int64 {
	t.Helper()
	snaps, err := store.ListSnapshots(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	for _, s := range snaps {
		if s.ID == snapID {
			out := map[string]int64{}
			for _, p := range s.Parts {
				out[p.Part] = p.RunID
			}
			return out
		}
	}
	t.Fatalf("snapshot %d not listed", snapID)
	return nil
}

// commitLatestRun is commitRun under hash "h" without the snapshot: the run
// only updates the latest compute, as every compute command does. It returns
// the run id.
func commitLatestRun(t *testing.T, store Store, producer string, write func(run int64)) int64 {
	t.Helper()
	const hash = "h"
	ctx := context.Background()
	run, err := store.CreateRun(ctx, producer, hash, nil)
	if err != nil {
		t.Fatalf("CreateRun(%s): %v", producer, err)
	}
	if write != nil {
		write(run.ID)
	}
	if err := store.CommitLatest(ctx, hash, testEpoch, map[string]int64{producer: run.ID}); err != nil {
		t.Fatalf("CommitLatest(%s): %v", producer, err)
	}
	return run.ID
}

// latestParts is the latest compute under hash as part -> run.
func latestParts(t *testing.T, store Store, hash string) map[string]int64 {
	t.Helper()
	latest, err := store.LatestCompute(context.Background(), hash)
	if err != nil {
		t.Fatal(err)
	}
	out := map[string]int64{}
	for _, p := range latest {
		out[p.Part] = p.RunID
	}
	return out
}

// TestCommitLatest_SwapsOnlyItsPart: recomputing roads swaps the new roads
// run into the latest compute and carries every other part (fire coverage
// here) forward untouched, and no commit creates a
// snapshot: a snapshot is only ever made by CreateSnapshot, and one made
// earlier keeps reading what it froze.
func TestCommitLatest_SwapsOnlyItsPart(t *testing.T) {
	ctx := context.Background()
	store := openTestStore(t)
	roads1 := commitLatestRun(t, store, "roads", roadsRun(t, store, 100))
	fire1 := commitLatestRun(t, store, "service:fire", fireRun(t, store, HexMetric{HexID: "f1", Value: 300}))
	if snaps, _ := store.ListSnapshots(ctx); len(snaps) != 0 {
		t.Fatalf("commits created snapshots: %+v", snaps)
	}
	s1 := snapshotLatest(t, store, "h")
	roads2 := commitLatestRun(t, store, "roads", roadsRun(t, store, 200))

	if got := latestParts(t, store, "h"); !maps.Equal(got, map[string]int64{"roads": roads2, "service:fire": fire1}) {
		t.Errorf("latest compute after recomputing roads = %v, want roads=%d (new) and fire=%d (carried)", got, roads2, fire1)
	}
	latest := store.WithLatestCompute("h")
	if got := roadsArea(t, latest); got != 200 {
		t.Errorf("latest roads area = %v, want 200 from the recompute", got)
	}
	if got := fireHexes(t, latest); !slices.Equal(got, []string{"f1"}) {
		t.Errorf("fire hexes after a roads recompute = %v, want [f1] (carried forward, not hidden)", got)
	}
	if got := parts(t, store, s1); !maps.Equal(got, map[string]int64{"roads": roads1, "service:fire": fire1}) {
		t.Errorf("snapshot %d after the recompute = %v, want it unchanged (roads=%d)", s1, got, roads1)
	}
	if got := roadsArea(t, store); got != 100 {
		t.Errorf("unpinned (newest snapshot) roads = %v, want the frozen 100", got)
	}
	if snaps, _ := store.ListSnapshots(ctx); len(snaps) != 1 {
		t.Errorf("snapshots = %d, want only the one created", len(snaps))
	}
	// The replaced run is left for gc, not deleted at commit. roads1 is
	// held by the snapshot, so replace roads2, which nothing else holds.
	commitLatestRun(t, store, "roads", nil)
	if n := runCount(t, store, roads2); n != 1 {
		t.Errorf("replaced run %d deleted at commit (%d rows), want it left for gc", roads2, n)
	}
}

// runCount is 1 when run exists, else 0.
func runCount(t *testing.T, store Store, run int64) int {
	t.Helper()
	var n int
	if err := store.(*sqliteStore).db.QueryRowContext(context.Background(),
		`SELECT COUNT(*) FROM runs WHERE id = ?`, run).Scan(&n); err != nil {
		t.Fatal(err)
	}
	return n
}

// TestSnapshot_PinnedReadIsTheWholeCity: pinning a snapshot reads every part
// of it, not just the resource computed in its newest run, and nothing from a
// later snapshot leaks in.
func TestSnapshot_PinnedReadIsTheWholeCity(t *testing.T) {
	ctx := context.Background()
	store := openTestStore(t)
	commitRun(t, store, "h", "roads", roadsRun(t, store, 100))
	_, s2 := commitRun(t, store, "h", "parking", parkingRun(t, store, 7))
	commitRun(t, store, "h", "roads", roadsRun(t, store, 200))

	pinned := store.WithSnapshot(s2)
	if got := roadsArea(t, pinned); got != 100 {
		t.Errorf("pinned roads = %v, want 100 (the roads run in snapshot %d, not the later recompute)", got, s2)
	}
	if park, ok := cityArea(t, pinned, "parking"); !ok || park != 7 {
		t.Errorf("pinned parking = (%v, %v), want 7: a pin reads every part, not only the run that made it", park, ok)
	}
	stats, err := pinned.ListHexMetrics(ctx, "paved_area_m2", "roads")
	if err != nil || len(stats) != 1 || stats[0].Value != 100 {
		t.Errorf("pinned roads hex rows = (%+v, %v), want the one 100 row", stats, err)
	}
}

// TestSnapshot_EmptyRunHidesMetric: a producer rerun that stores nothing
// replaces its part with an empty run, so the metric disappears instead of an
// older run's rows resurfacing. The older snapshot still reads them.
func TestSnapshot_EmptyRunHidesMetric(t *testing.T) {
	store := openTestStore(t)
	_, s1 := commitRun(t, store, "h", "service:fire", fireRun(t, store, HexMetric{HexID: "f1", Value: 300}))
	commitRun(t, store, "h", "service:fire", fireRun(t, store))

	if got := fireHexes(t, store); len(got) != 0 {
		t.Errorf("fire after an empty rerun = %v, want none", got)
	}
	if got := fireHexes(t, store.WithSnapshot(s1)); !slices.Equal(got, []string{"f1"}) {
		t.Errorf("the earlier snapshot = %v, want its own [f1]", got)
	}
}

// TestSnapshot_ConfigHashesDoNotMix: a snapshot only ever holds runs of its
// own config hash, and nothing is carried across hashes, because hex ids from
// another config's grid would land on the wrong hexes.
func TestSnapshot_ConfigHashesDoNotMix(t *testing.T) {
	store := openTestStore(t)
	commitRun(t, store, "A", "roads", roadsRun(t, store, 100))
	_, sB := commitRun(t, store, "B", "parking", parkingRun(t, store, 7))

	if got := parts(t, store, sB); len(got) != 1 || got["parking"] == 0 {
		t.Errorf("first hash-B snapshot = %v, want only parking (roads belongs to hash A)", got)
	}
	if got := roadsArea(t, store.WithLatestCompute("A")); got != 100 {
		t.Errorf("hash A roads = %v, want 100", got)
	}
	if _, ok := cityArea(t, store.WithLatestCompute("A"), "parking"); ok {
		t.Error("hash A reads parking, want none (parking belongs to hash B)")
	}
	if _, ok := cityArea(t, store.WithLatestCompute("B"), "roads"); ok {
		t.Error("hash B reads roads, want none (roads belongs to hash A)")
	}
	if _, ok := cityArea(t, store.WithLatestCompute("C"), "roads"); ok {
		t.Error("an unknown hash reads roads, want none")
	}
	// Unpinned reads the newest snapshot of any hash.
	if _, ok := cityArea(t, store, "parking"); !ok {
		t.Error("unpinned parking missing, want the newest (hash B) snapshot")
	}
}

// TestSnapshot_UncommittedRunsAreInvisible: rows written under a run are not
// readable until the run is committed into the latest compute.
func TestSnapshot_UncommittedRunsAreInvisible(t *testing.T) {
	ctx := context.Background()
	store := openTestStore(t)
	commitRun(t, store, "h", "roads", roadsRun(t, store, 100))
	run, err := store.CreateRun(ctx, "roads", "h", nil)
	if err != nil {
		t.Fatal(err)
	}
	roadsRun(t, store, 200)(run.ID)

	latest := store.WithLatestCompute("h")
	if got := roadsArea(t, latest); got != 100 {
		t.Errorf("committed view = %v, want 100 (the pending run is invisible)", got)
	}
	if err := store.CommitLatest(ctx, "h", testEpoch, map[string]int64{"roads": run.ID}); err != nil {
		t.Fatal(err)
	}
	if got := roadsArea(t, latest); got != 200 {
		t.Errorf("after committing the run = %v, want 200", got)
	}
}

// TestCommitLatest_RejectsInconsistentRuns: a manifest may only reference
// this city's runs, each produced for the part it fills under the commit's
// hash. A rejected commit changes nothing.
func TestCommitLatest_RejectsInconsistentRuns(t *testing.T) {
	ctx := context.Background()
	root, err := Open(":memory:")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = root.Close() })
	idA, _ := root.EnsureCity(ctx, "a", "A", "test")
	idB, _ := root.EnsureCity(ctx, "b", "B", "test")
	storeA, storeB := root.ForCity(idA), root.ForCity(idB)

	roads, _ := storeA.CreateRun(ctx, "roads", "h", nil)
	other, _ := storeB.CreateRun(ctx, "roads", "h", nil)
	for name, parts := range map[string]map[string]int64{
		"wrong part":     {"parking": roads.ID},
		"wrong hash":     {"roads": roads.ID},
		"another city's": {"roads": other.ID},
		"unknown run":    {"roads": 9999},
	} {
		hash := "h"
		if name == "wrong hash" {
			hash = "other"
		}
		if err := storeA.CommitLatest(ctx, hash, testEpoch, parts); err == nil {
			t.Errorf("%s: commit succeeded, want an error", name)
		}
	}
	for _, hash := range []string{"h", "other"} {
		if got := latestParts(t, storeA, hash); len(got) != 0 {
			t.Errorf("rejected commits left a latest compute under %s: %v", hash, got)
		}
	}
}

// TestDeleteSnapshot_KeepsRunsOtherSnapshotsUse: deleting a snapshot removes
// only runs neither another snapshot nor the working state references, so
// every surviving snapshot, and the working state, still reads exactly what
// it did.
func TestDeleteSnapshot_KeepsRunsOtherSnapshotsUse(t *testing.T) {
	ctx := context.Background()
	root, err := Open(":memory:")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = root.Close() })
	id, _ := root.EnsureCity(ctx, "a", "A", "test")
	store := root.ForCity(id)

	roads1, s1 := commitRun(t, store, "h", "roads", roadsRun(t, store, 100))
	fire, s2 := commitRun(t, store, "h", "service:fire", fireRun(t, store, HexMetric{HexID: "f1"}))
	roads2, s3 := commitRun(t, store, "h", "roads", roadsRun(t, store, 200))

	if ok, err := deleteSnapshotID(store, s3); err != nil || !ok {
		t.Fatalf("DeleteSnapshot(s3) = (%v, %v)", ok, err)
	}
	// The working state is still s3's manifest, so its runs stay.
	if n := countRows(t, root, `SELECT COUNT(*) FROM runs WHERE id = ?`, roads2); n != 1 {
		t.Error("the roads run the working state references was deleted with s3")
	}
	// s2 is now the newest: roads 100 and fire intact.
	if got := roadsArea(t, store); got != 100 {
		t.Errorf("roads after deleting s3 = %v, want 100 from s2", got)
	}
	if got := fireHexes(t, store); !slices.Equal(got, []string{"f1"}) {
		t.Errorf("fire after deleting s3 = %v, want [f1] (the run s2 still uses)", got)
	}
	if n := countRows(t, root, `SELECT COUNT(*) FROM runs WHERE id = ?`, roads1); n != 1 {
		t.Error("the roads run s2 still references was deleted")
	}

	// roads1 is in s1 and s2 only: it goes with the last of them. The fire
	// run is in the working state too, so it stays.
	for _, snap := range []int64{s1, s2} {
		if ok, err := deleteSnapshotID(store, snap); err != nil || !ok {
			t.Fatalf("DeleteSnapshot(%d) = (%v, %v)", snap, ok, err)
		}
	}
	if n := countRows(t, root, `SELECT COUNT(*) FROM runs WHERE id = ?`, roads1); n != 0 {
		t.Error("the roads run no snapshot or working part references survived")
	}
	if n := countRows(t, root, `SELECT COUNT(*) FROM hex_metrics WHERE run_id = ?`, roads1); n != 0 {
		t.Error("rows of the deleted run survived")
	}
	if n := countRows(t, root, `SELECT COUNT(*) FROM runs WHERE id = ?`, fire); n != 1 {
		t.Error("the fire run the working state references was deleted")
	}
}

// TestDiscardRun: an uncommitted run and its rows go; a committed one is
// refused, because removing it would change a snapshot.
func TestDiscardRun(t *testing.T) {
	ctx := context.Background()
	store := openTestStore(t)
	committed, _ := commitRun(t, store, "h", "roads", roadsRun(t, store, 100))
	pending, err := store.CreateRun(ctx, "roads", "h", nil)
	if err != nil {
		t.Fatal(err)
	}
	roadsRun(t, store, 200)(pending.ID)

	if err := store.DiscardRun(ctx, pending.ID); err != nil {
		t.Fatalf("DiscardRun(pending): %v", err)
	}
	raw := store.(*sqliteStore)
	var n int
	if err := raw.db.QueryRowContext(ctx, `SELECT COUNT(*) FROM city_metrics WHERE run_id = ?`, pending.ID).Scan(&n); err != nil || n != 0 {
		t.Errorf("discarded run's rows remain: %d (%v)", n, err)
	}
	if err := store.DiscardRun(ctx, committed); err == nil {
		t.Error("DiscardRun on a committed run succeeded, want a refusal")
	}
	if got := roadsArea(t, store); got != 100 {
		t.Errorf("roads after the refused discard = %v, want 100", got)
	}
}

// TestGC_OrphanRuns: gc sweeps runs no snapshot references once they are
// older than OrphanRunGrace, with their rows, and leaves young orphans (a
// compute may still be running), committed runs and features alone.
func TestGC_OrphanRuns(t *testing.T) {
	ctx := context.Background()
	root, err := Open(":memory:")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = root.Close() })
	id, _ := root.EnsureCity(ctx, "a", "A", "test")
	store := root.ForCity(id)
	seedGCFeature(t, root, id, "f1", "overpass")

	committed, _ := commitRun(t, store, "h", "roads", roadsRun(t, store, 100))
	old, _ := store.CreateRun(ctx, "roads", "h", nil)
	roadsRun(t, store, 1)(old.ID)
	young, _ := store.CreateRun(ctx, "parking", "h", nil)
	parkingRun(t, store, 1)(young.ID)
	stale := time.Now().Add(-2 * OrphanRunGrace).Unix()
	if _, err := root.db.ExecContext(ctx, `UPDATE runs SET computed_at = ? WHERE id IN (?, ?)`, stale, old.ID, committed); err != nil {
		t.Fatal(err)
	}

	scan, err := store.GCScan(ctx, []string{"overpass"}, testEpoch)
	if err != nil {
		t.Fatal(err)
	}
	want := GCReport{OrphanRuns: 1, OrphanRunResults: GCResultCounts{CityMetrics: 2, HexMetrics: 1}}
	if *scan != want {
		t.Errorf("scan = %+v, want %+v (only the old orphan)", *scan, want)
	}
	if n := countRows(t, root, `SELECT COUNT(*) FROM runs`); n != 3 {
		t.Errorf("scan wrote: %d runs remain, want 3", n)
	}
	swept, err := store.GCSweep(ctx, []string{"overpass"}, testEpoch)
	if err != nil {
		t.Fatal(err)
	}
	if *swept != want {
		t.Errorf("sweep = %+v, want %+v", *swept, want)
	}
	for run, wantN := range map[int64]int{old.ID: 0, young.ID: 1, committed: 1} {
		if n := countRows(t, root, `SELECT COUNT(*) FROM runs WHERE id = ?`, run); n != wantN {
			t.Errorf("run %d count = %d after sweep, want %d", run, n, wantN)
		}
	}
	if got := roadsArea(t, store); got != 100 {
		t.Errorf("committed roads after the sweep = %v, want 100", got)
	}
	// The swept run's rows went with it (ON DELETE CASCADE).
	for _, table := range []string{"city_metrics", "hex_metrics"} {
		if n := countRows(t, root, `SELECT COUNT(*) FROM `+table+` WHERE run_id = ?`, old.ID); n != 0 {
			t.Errorf("%s rows of the swept run = %d, want 0", table, n)
		}
	}
}

// TestGC_SupersededEpoch: a latest compute committed
// under an older ComputeEpoch can never be produced again, so gc drops it and
// sweeps its runs. Another config's latest compute from the current epoch is
// kept, and so is an old-epoch run a snapshot still holds.
func TestGC_SupersededEpoch(t *testing.T) {
	ctx := context.Background()
	root, err := Open(":memory:")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = root.Close() })
	id, _ := root.EnsureCity(ctx, "a", "A", "test")
	store := root.ForCity(id)

	put := func(hash, producer string, epoch int) int64 {
		t.Helper()
		run, err := store.CreateRun(ctx, producer, hash, nil)
		if err != nil {
			t.Fatal(err)
		}
		if err := store.CommitLatest(ctx, hash, epoch, map[string]int64{producer: run.ID}); err != nil {
			t.Fatal(err)
		}
		return run.ID
	}
	oldRoads := put("old", "roads", 1)
	oldParking := put("old", "parking", 1)
	snapped := put("old-snapped", "roads", 1)
	if _, err := createCitySnapshot(store, "old-snapped", 100, nil); err != nil {
		t.Fatal(err)
	}
	other := put("other", "roads", 2)
	stale := time.Now().Add(-2 * OrphanRunGrace).Unix()
	if _, err := root.db.ExecContext(ctx, `UPDATE runs SET computed_at = ?`, stale); err != nil {
		t.Fatal(err)
	}

	scan, err := store.GCScan(ctx, nil, 2)
	if err != nil {
		t.Fatal(err)
	}
	if scan.SupersededParts != 3 || scan.OrphanRuns != 2 {
		t.Errorf("scan = %+v, want 3 superseded parts and 2 orphan runs", *scan)
	}
	if n := countRows(t, root, `SELECT COUNT(*) FROM working_parts`); n != 4 {
		t.Errorf("scan wrote: %d working parts remain, want 4", n)
	}
	swept, err := store.GCSweep(ctx, nil, 2)
	if err != nil {
		t.Fatal(err)
	}
	if *swept != *scan {
		t.Errorf("sweep = %+v, want what the scan counted, %+v", *swept, *scan)
	}
	for run, wantN := range map[int64]int{oldRoads: 0, oldParking: 0, snapped: 1, other: 1} {
		if n := countRows(t, root, `SELECT COUNT(*) FROM runs WHERE id = ?`, run); n != wantN {
			t.Errorf("run %d count = %d after sweep, want %d", run, n, wantN)
		}
	}
	if parts, err := store.LatestCompute(ctx, "other"); err != nil || len(parts) != 1 {
		t.Errorf("current-epoch latest compute of another config = %+v, %v; want it kept", parts, err)
	}

	// The same database read by the older binary keeps everything it can
	// still produce: an epoch newer than its own is not superseded.
	again, err := store.GCScan(ctx, nil, 1)
	if err != nil {
		t.Fatal(err)
	}
	if again.SupersededParts != 0 || again.OrphanRuns != 0 {
		t.Errorf("scan at the older epoch = %+v, want nothing to collect", *again)
	}
}

// TestListSnapshots_Manifests: newest first by id, each with its parts sorted.
func TestListSnapshots_Manifests(t *testing.T) {
	ctx := context.Background()
	store := openTestStore(t)
	roads, s1 := commitRun(t, store, "h", "roads", nil)
	fire, s2 := commitRun(t, store, "h", "service:fire", nil)

	snaps, err := store.ListSnapshots(ctx)
	if err != nil {
		t.Fatal(err)
	}
	if len(snaps) != 2 || snaps[0].ID != s2 || snaps[1].ID != s1 {
		t.Fatalf("snapshots = %+v, want [%d %d]", snaps, s2, s1)
	}
	got := snaps[0].Parts
	if len(got) != 2 || got[0].Part != "roads" || got[0].RunID != roads || got[1].Part != "service:fire" || got[1].RunID != fire {
		t.Errorf("newest manifest = %+v, want roads=%d then service:fire=%d", got, roads, fire)
	}
	if got[0].ComputedAt.IsZero() || snaps[0].CreatedAt.IsZero() || snaps[0].ConfigHash != "h" {
		t.Errorf("snapshot metadata not populated: %+v", snaps[0])
	}
}

// TestListSnapshots_TimestampsMarshalLikeTheCLI guards that marshaling a
// listed snapshot (what /api/snapshots serves) gives the same timestamp
// string the CLI prints: stored unix seconds read back in UTC, whatever the
// local zone.
func TestListSnapshots_TimestampsMarshalLikeTheCLI(t *testing.T) {
	ctx := context.Background()
	store := openTestStore(t)
	run, snap := commitRun(t, store, "h", "roads", nil)
	raw := store.(*sqliteStore).db
	t.Setenv("TZ", "America/Los_Angeles")
	stored := time.Date(2026, 3, 4, 5, 6, 7, 123456789, time.FixedZone("", -7*3600)).Unix()
	if _, err := raw.ExecContext(ctx, `UPDATE snapshots SET created_at = ? WHERE id = ?`, stored, snap); err != nil {
		t.Fatal(err)
	}
	if _, err := raw.ExecContext(ctx, `UPDATE runs SET computed_at = ? WHERE id = ?`, stored, run); err != nil {
		t.Fatal(err)
	}

	snaps, err := store.ListSnapshots(ctx)
	if err != nil {
		t.Fatal(err)
	}
	if len(snaps) != 1 || len(snaps[0].Parts) != 1 {
		t.Fatalf("snapshots = %+v, want one with one part", snaps)
	}
	b, err := json.Marshal(snaps[0])
	if err != nil {
		t.Fatal(err)
	}
	var got struct {
		CreatedAt string `json:"created_at"`
		Parts     []struct {
			ComputedAt string `json:"computed_at"`
		} `json:"parts"`
	}
	if err := json.Unmarshal(b, &got); err != nil {
		t.Fatal(err)
	}
	const want = "2026-03-04T12:06:07Z"
	if cli := snaps[0].CreatedAt.UTC().Format(time.RFC3339); cli != want {
		t.Errorf("CLI created_at = %q, want %q", cli, want)
	}
	if got.CreatedAt != want {
		t.Errorf("marshaled created_at = %q, want %q", got.CreatedAt, want)
	}
	if got.Parts[0].ComputedAt != want {
		t.Errorf("marshaled computed_at = %q, want %q", got.Parts[0].ComputedAt, want)
	}
}

// TestResolveSnapshot: a name resolves for a city it covers; another city's,
// and an unknown name, do not.
func TestResolveSnapshot(t *testing.T) {
	ctx := context.Background()
	root, err := Open(":memory:")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = root.Close() })
	idA, _ := root.EnsureCity(ctx, "a", "A", "test")
	idB, _ := root.EnsureCity(ctx, "b", "B", "test")
	storeA := root.ForCity(idA)
	commitLatestRun(t, storeA, "roads", nil)
	snap, err := root.CreateSnapshot(ctx, "v1", testEpoch, []SnapshotRequest{{CityID: idA, ConfigHash: "h", HexEdgeM: testEdge}})
	if err != nil {
		t.Fatal(err)
	}

	if got, err := storeA.ResolveSnapshot(ctx, "v1"); err != nil || got != snap.ID {
		t.Errorf("covered city: (%d, %v), want %d", got, err, snap.ID)
	}
	for name, s := range map[string]func() (int64, error){
		"another city": func() (int64, error) { return root.ForCity(idB).ResolveSnapshot(ctx, "v1") },
		"unknown name": func() (int64, error) { return storeA.ResolveSnapshot(ctx, "v2") },
	} {
		if _, err := s(); !errors.Is(err, sql.ErrNoRows) {
			t.Errorf("%s: err = %v, want sql.ErrNoRows", name, err)
		}
	}
}

// TestDraft: a draft's runs stay invisible until Commit, which swaps exactly
// the put parts into the latest compute and discards runs that were created
// but never put, and Discard deletes every run it created.
func TestDraft(t *testing.T) {
	ctx := context.Background()
	store := openTestStore(t)
	commitLatestRun(t, store, "service:fire", fireRun(t, store, HexMetric{HexID: "f1"}))
	latest := store.WithLatestCompute("h")

	d := NewDraft(store, "h", testEpoch)
	roads, err := d.NewRun(ctx, "roads", nil)
	if err != nil {
		t.Fatal(err)
	}
	roadsRun(t, store, 100)(roads)
	d.Put("roads", roads)
	if _, ok := cityArea(t, latest, "roads"); ok {
		t.Error("put but uncommitted roads is readable, want invisible")
	}
	abandoned, _ := d.NewRun(ctx, "parking", nil)
	parkingRun(t, store, 5)(abandoned)

	if err := d.Commit(ctx); err != nil {
		t.Fatal(err)
	}
	if got := latestParts(t, store, "h"); len(got) != 2 || got["roads"] != roads || got["service:fire"] == 0 {
		t.Errorf("latest compute = %v, want roads=%d plus the carried fire part", got, roads)
	}
	if got := roadsArea(t, latest); got != 100 {
		t.Errorf("roads after Commit = %v, want 100", got)
	}
	if n := runCount(t, store, abandoned); n != 0 {
		t.Errorf("the never-put run survived Commit (%d)", n)
	}

	if err := NewDraft(store, "h", testEpoch).Commit(ctx); err != nil {
		t.Errorf("empty draft Commit = %v, want nil", err)
	}
	if snaps, _ := store.ListSnapshots(ctx); len(snaps) != 0 {
		t.Errorf("Commit created snapshots: %+v", snaps)
	}

	d2 := NewDraft(store, "h", testEpoch)
	r2, _ := d2.NewRun(ctx, "roads", nil)
	d2.Put("roads", r2)
	if err := d2.Discard(ctx); err != nil {
		t.Fatal(err)
	}
	if n := runCount(t, store, r2); n != 0 {
		t.Errorf("Discard left run %d behind (%d)", r2, n)
	}
	if got := roadsArea(t, latest); got != 100 {
		t.Errorf("roads after a discarded draft = %v, want the committed 100", got)
	}
}

// TestDraft_DropRemovesPart: Drop takes a part out of the latest compute,
// e.g. a service whose stations are gone, so its coverage is not frozen by
// the next snapshot. A snapshot that already holds it is unchanged.
func TestDraft_DropRemovesPart(t *testing.T) {
	ctx := context.Background()
	store := openTestStore(t)
	_, s1 := commitRun(t, store, "h", "service:fire", nil)
	d := NewDraft(store, "h", testEpoch)
	d.Drop("service:fire")
	if err := d.Commit(ctx); err != nil {
		t.Fatal(err)
	}
	if got := latestParts(t, store, "h"); len(got) != 0 {
		t.Errorf("latest compute after Drop = %v, want empty", got)
	}
	if got := parts(t, store, s1); len(got) != 1 {
		t.Errorf("snapshot %d after Drop = %v, want its fire part kept", s1, got)
	}
}

// TestHexMetrics_WriteOnce: a run's rows for a metric are written once.
// Saving the metric again in the same run fails on the primary key and leaves
// the first save in place; another metric of the run is its own row; an
// unknown metric lists (nil, nil).
func TestHexMetrics_WriteOnce(t *testing.T) {
	ctx := context.Background()
	store := openTestStore(t)
	commitRun(t, store, "h", "census", func(run int64) {
		if err := store.SaveHexMetrics(ctx, run, []HexMetric{{HexID: "h1", Metric: "population", Value: 12}}); err != nil {
			t.Fatal(err)
		}
		if err := store.SaveHexMetrics(ctx, run, []HexMetric{{HexID: "h1", Metric: "density", Value: 3, Ref: "x"}}); err != nil {
			t.Fatal(err)
		}
		if err := store.SaveHexMetrics(ctx, run, []HexMetric{{HexID: "h1", Metric: "population", Value: 9}}); err == nil {
			t.Error("second population save for h1 in one run succeeded, want a primary-key failure")
		}
	})
	pop, _ := store.ListHexMetrics(ctx, "population", "")
	if len(pop) != 1 || pop[0].HexID != "h1" || pop[0].Value != 12 || pop[0].Metric != "population" {
		t.Errorf("population = %+v, want the first save (h1=12)", pop)
	}
	den, _ := store.ListHexMetrics(ctx, "density", "")
	if len(den) != 1 || den[0].Ref != "x" {
		t.Errorf("density = %+v, want its own row", den)
	}
	if none, err := store.ListHexMetrics(ctx, "nope", ""); none != nil || err != nil {
		t.Errorf("unknown metric = (%v, %v), want (nil, nil)", none, err)
	}
}

// TestSaveHexMetrics_Atomic: the rows of one save commit or roll back
// together. A trigger aborts the second metric's insert, and the first
// metric's rows must roll back with it.
func TestSaveHexMetrics_Atomic(t *testing.T) {
	ctx := context.Background()
	store := openTestStore(t)
	run, _ := commitRun(t, store, "h", "census", func(run int64) {
		if err := store.SaveHexMetrics(ctx, run, []HexMetric{
			{HexID: "h1", Metric: "a_metric", Value: 1},
			{HexID: "h1", Metric: "b_metric", Value: 2},
		}); err != nil {
			t.Fatal(err)
		}
	})
	raw := store.(*sqliteStore).db
	if _, err := raw.ExecContext(ctx, `CREATE TRIGGER fail_b BEFORE INSERT ON hex_metrics
		WHEN NEW.metric = 'b_metric' BEGIN SELECT RAISE(ABORT, 'boom'); END`); err != nil {
		t.Fatal(err)
	}
	if err := store.SaveHexMetrics(ctx, run, []HexMetric{
		{HexID: "h2", Metric: "a_metric", Value: 10},
		{HexID: "h2", Metric: "b_metric", Value: 20},
	}); err == nil {
		t.Fatal("expected the aborted insert to fail the save")
	}
	for key, want := range map[string]float64{"a_metric": 1, "b_metric": 2} {
		rows, err := store.ListHexMetrics(ctx, key, "")
		if err != nil {
			t.Fatal(err)
		}
		if len(rows) != 1 || rows[0].Value != want {
			t.Errorf("%s = %+v after a failed save, want the previous value %v (rolled back)", key, rows, want)
		}
	}
}

// TestHasHexMetric agrees with ListHexMetrics: false with no snapshot and
// for subjects or metrics without rows, true once any requested subject has
// rows, and scoped to the resolved snapshot like every other read.
func TestHasHexMetric(t *testing.T) {
	ctx := context.Background()
	store := openTestStore(t)
	both := []string{"roads", "parking"}
	if ok, err := store.HasHexMetric(ctx, "paved_area_m2", both); err != nil || ok {
		t.Fatalf("no snapshot: (%v, %v), want (false, nil)", ok, err)
	}
	_, snap := commitRun(t, store, "hash", "parking", func(run int64) {
		if err := store.SaveHexMetrics(ctx, run, []HexMetric{{HexID: "h1", Metric: "paved_area_m2", Subject: "parking", Value: 1}}); err != nil {
			t.Fatal(err)
		}
	})
	if ok, err := store.HasHexMetric(ctx, "paved_area_m2", []string{"roads"}); err != nil || ok {
		t.Errorf("roads only: (%v, %v), want (false, nil)", ok, err)
	}
	if ok, err := store.HasHexMetric(ctx, "coverage_pct", both); err != nil || ok {
		t.Errorf("another metric: (%v, %v), want (false, nil)", ok, err)
	}
	if ok, err := store.HasHexMetric(ctx, "paved_area_m2", both); err != nil || !ok {
		t.Errorf("roads+parking: (%v, %v), want (true, nil)", ok, err)
	}
	if ok, _ := store.WithLatestCompute("other").HasHexMetric(ctx, "paved_area_m2", both); ok {
		t.Error("a different config hash must not see the rows")
	}
	if ok, _ := store.WithSnapshot(snap+100).HasHexMetric(ctx, "paved_area_m2", both); ok {
		t.Error("a pin to a snapshot that does not exist must see nothing")
	}
}

// TestCityMetrics_RoundTrip: rows read back with the run's id and timestamp,
// ordered by subject, class, metric; Stats reads the class-"" totals; a
// repeated (metric, subject, class) in one run fails on the primary key.
func TestCityMetrics_RoundTrip(t *testing.T) {
	ctx := context.Background()
	store := openTestStore(t)
	run, _ := commitRun(t, store, "h", "parking", func(run int64) {
		if err := store.SaveCityMetrics(ctx, run, []CityMetric{
			{Metric: "parking_spaces", Subject: "parking", Value: 31},
			{Metric: "paved_area_m2", Subject: "parking", Value: 900},
			{Metric: "paved_area_m2", Subject: "parking", Class: "parking", Value: 900},
		}); err != nil {
			t.Fatal(err)
		}
		if err := store.SaveCityMetrics(ctx, run, []CityMetric{{Metric: "paved_area_m2", Subject: "parking", Value: 1}}); err == nil {
			t.Error("a second parking total in one run succeeded, want a primary-key failure")
		}
	})
	rows, err := store.ListCityMetrics(ctx, []string{"parking", "roads"})
	if err != nil {
		t.Fatal(err)
	}
	var got []string
	for _, m := range rows {
		got = append(got, m.Subject+"/"+m.Class+"/"+m.Metric)
		if m.RunID != run || m.ComputedAt.IsZero() {
			t.Errorf("row %+v: want run %d and the run's timestamp", m, run)
		}
	}
	if want := []string{"parking//parking_spaces", "parking//paved_area_m2", "parking/parking/paved_area_m2"}; !slices.Equal(got, want) {
		t.Errorf("rows = %v, want %v", got, want)
	}
	info, err := store.Stats(ctx, rtParking)
	if err != nil || info.EstimatedSpaces != 31 || info.TotalArea != 900 || info.LastComputeAt == nil {
		t.Errorf("Stats(parking) = (%+v, %v), want 900 m² and 31 spaces", info, err)
	}
}

// TestCommitLatest_ConcurrentProcesses: two pvmt processes (two handles on
// one file) committing at once must both succeed. Commits read the base
// manifest and then write, so a DEFERRED transaction loses the race with
// SQLITE_BUSY_SNAPSHOT (517), which busy_timeout cannot retry, and the
// caller then discards its runs. Write transactions take the lock up front
// (_txlock=immediate) so the loser waits instead.
func TestCommitLatest_ConcurrentProcesses(t *testing.T) {
	ctx := context.Background()
	path := filepath.Join(t.TempDir(), "pvmt.db")
	var stores [2]Store
	for i := range stores {
		root, err := Open(path)
		if err != nil {
			t.Fatal(err)
		}
		t.Cleanup(func() { _ = root.Close() })
		id, err := root.EnsureCity(ctx, "a", "A", "test")
		if err != nil {
			t.Fatal(err)
		}
		stores[i] = root.ForCity(id)
	}
	const perProcess = 25
	errs := make(chan error, 2*perProcess)
	last := [2]int64{}
	var wg sync.WaitGroup
	for i, store := range stores {
		producer := []string{"roads", "parking"}[i]
		wg.Go(func() {
			for range perProcess {
				run, err := store.CreateRun(ctx, producer, "h", nil)
				if err == nil {
					err = store.CommitLatest(ctx, "h", testEpoch, map[string]int64{producer: run.ID})
					last[i] = run.ID
				}
				errs <- err
			}
		})
	}
	wg.Wait()
	close(errs)
	for err := range errs {
		if err != nil {
			t.Fatalf("concurrent commit failed: %v", err)
		}
	}
	// Every commit composed on the true newest base: the latest compute has
	// both producers' last runs, none lost to a stale base.
	if got := latestParts(t, stores[0], "h"); !maps.Equal(got, map[string]int64{"roads": last[0], "parking": last[1]}) {
		t.Errorf("final latest compute = %v, want roads=%d parking=%d", got, last[0], last[1])
	}
}

// TestCommitLatest_NoOp: a commit that would not change the manifest leaves
// the latest compute as it was, and never mints a snapshot. Dropping a part
// that is not there is such a no-op: it is how a census-disabled or
// station-less city reports "nothing to do".
func TestCommitLatest_NoOp(t *testing.T) {
	ctx := context.Background()
	store := openTestStore(t)

	if err := store.CommitLatest(ctx, "h", testEpoch, map[string]int64{"census": 0}); err != nil {
		t.Fatal(err)
	}
	if got := latestParts(t, store, "h"); len(got) != 0 {
		t.Fatalf("a drop-only commit with no base left %v", got)
	}

	roads := commitLatestRun(t, store, "roads", nil)
	for name, parts := range map[string]map[string]int64{
		"drop of an absent part": {"census": 0},
		"same run again":         {"roads": roads},
		"nothing":                {},
	} {
		if err := store.CommitLatest(ctx, "h", testEpoch, parts); err != nil {
			t.Errorf("%s: %v", name, err)
		}
		if got := latestParts(t, store, "h"); !maps.Equal(got, map[string]int64{"roads": roads}) {
			t.Errorf("%s: latest compute = %v, want roads=%d", name, got, roads)
		}
	}
	if snaps, _ := store.ListSnapshots(ctx); len(snaps) != 0 {
		t.Errorf("commits minted snapshots: %d", len(snaps))
	}
	if err := store.CommitLatest(ctx, "", testEpoch, map[string]int64{"roads": roads}); err == nil {
		t.Error("commit with an empty config hash succeeded; it would take its base from any hash")
	}
}
