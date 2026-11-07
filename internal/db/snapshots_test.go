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

	"github.com/jcrussell/solvent-streets/internal/resource"
)

// commitRun creates a run for producer under hash, lets write store rows
// tagged with it, and commits a snapshot that swaps in just that part. It
// returns the run and snapshot ids.
func commitRun(t *testing.T, store Store, hash, producer string, write func(run int64)) (int64, int64) {
	t.Helper()
	ctx := context.Background()
	run, err := store.CreateRun(ctx, producer, hash)
	if err != nil {
		t.Fatalf("CreateRun(%s): %v", producer, err)
	}
	if write != nil {
		write(run.ID)
	}
	snap, err := store.CommitSnapshot(ctx, hash, map[string]int64{producer: run.ID})
	if err != nil {
		t.Fatalf("CommitSnapshot(%s): %v", producer, err)
	}
	return run.ID, snap.ID
}

// roadsRun / fireRun / parkingRun write one recognisable row each, so a read
// can tell which run it came from by the value.
func roadsRun(t *testing.T, store Store, area float64) func(int64) {
	t.Helper()
	return func(run int64) {
		ctx := context.Background()
		if err := store.SaveComputeResult(ctx, ComputeResult{ResourceType: rtRoads, TotalArea: area, RunID: run}); err != nil {
			t.Fatal(err)
		}
		if err := store.SaveHexStats(ctx, []HexStat{{HexID: "h1", ResourceType: rtRoads, Area: area, RunID: run}}); err != nil {
			t.Fatal(err)
		}
		if err := store.SaveCohortStats(ctx, []CohortStat{{ResourceType: rtRoads, Classification: "primary", Area: area, RunID: run}}); err != nil {
			t.Fatal(err)
		}
	}
}

func fireRun(t *testing.T, store Store, rows ...HexMetric) func(int64) {
	t.Helper()
	return func(run int64) {
		if err := store.SaveHexMetrics(context.Background(), "fire_response_s", run, rows); err != nil {
			t.Fatal(err)
		}
	}
}

func parkingRun(t *testing.T, store Store, area float64) func(int64) {
	t.Helper()
	return func(run int64) {
		if err := store.SaveComputeResult(context.Background(), ComputeResult{ResourceType: rtParking, TotalArea: area, RunID: run}); err != nil {
			t.Fatal(err)
		}
	}
}

func roadsArea(t *testing.T, store Store) float64 {
	t.Helper()
	r, err := store.LatestComputeResult(context.Background(), rtRoads)
	if err != nil {
		t.Fatalf("LatestComputeResult(roads): %v", err)
	}
	return r.TotalArea
}

func fireHexes(t *testing.T, store Store) []string {
	t.Helper()
	rows, err := store.ListHexMetrics(context.Background(), "fire_response_s")
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

// TestSnapshot_RecomputeSwapsOnlyItsPart is the headline contract of epic
// solvent-streets-25t6: recomputing roads produces a new snapshot that swaps
// in the new roads run and carries every other part (fire coverage here)
// forward untouched. The old behaviour hid fire until it was rerun.
func TestSnapshot_RecomputeSwapsOnlyItsPart(t *testing.T) {
	store := openTestStore(t)
	roads1, _ := commitRun(t, store, "h", "roads", roadsRun(t, store, 100))
	fire1, s2 := commitRun(t, store, "h", "service:fire", fireRun(t, store, HexMetric{HexID: "f1", Value: 300}))
	roads2, s3 := commitRun(t, store, "h", "roads", roadsRun(t, store, 200))

	if got := parts(t, store, s2); !maps.Equal(got, map[string]int64{"roads": roads1, "service:fire": fire1}) {
		t.Errorf("snapshot after the fire run = %v, want roads=%d fire=%d", got, roads1, fire1)
	}
	if got := parts(t, store, s3); !maps.Equal(got, map[string]int64{"roads": roads2, "service:fire": fire1}) {
		t.Errorf("snapshot after recomputing roads = %v, want roads=%d (new) and fire=%d (carried)", got, roads2, fire1)
	}
	if got := roadsArea(t, store); got != 200 {
		t.Errorf("roads area = %v, want 200 from the recompute", got)
	}
	if got := fireHexes(t, store); !slices.Equal(got, []string{"f1"}) {
		t.Errorf("fire hexes after a roads recompute = %v, want [f1] (carried forward, not hidden)", got)
	}
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
	park, err := pinned.LatestComputeResult(ctx, rtParking)
	if err != nil || park.TotalArea != 7 {
		t.Errorf("pinned parking = (%+v, %v), want 7: a pin reads every part, not only the run that made it", park, err)
	}
	stats, err := pinned.ListHexStats(ctx, rtRoads)
	if err != nil || len(stats) != 1 || stats[0].Area != 100 {
		t.Errorf("pinned roads hex stats = (%+v, %v), want the one 100 row", stats, err)
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
	ctx := context.Background()
	store := openTestStore(t)
	commitRun(t, store, "A", "roads", roadsRun(t, store, 100))
	_, sB := commitRun(t, store, "B", "parking", parkingRun(t, store, 7))

	if got := parts(t, store, sB); len(got) != 1 || got["parking"] == 0 {
		t.Errorf("first hash-B snapshot = %v, want only parking (roads belongs to hash A)", got)
	}
	if got := roadsArea(t, store.WithConfigHash("A")); got != 100 {
		t.Errorf("hash A roads = %v, want 100", got)
	}
	if _, err := store.WithConfigHash("A").LatestComputeResult(ctx, rtParking); !errors.Is(err, sql.ErrNoRows) {
		t.Errorf("hash A parking err = %v, want sql.ErrNoRows", err)
	}
	if _, err := store.WithConfigHash("B").LatestComputeResult(ctx, rtRoads); !errors.Is(err, sql.ErrNoRows) {
		t.Errorf("hash B roads err = %v, want sql.ErrNoRows", err)
	}
	if _, err := store.WithConfigHash("C").LatestComputeResult(ctx, rtRoads); !errors.Is(err, sql.ErrNoRows) {
		t.Errorf("unknown hash err = %v, want sql.ErrNoRows", err)
	}
	// Unpinned reads the newest snapshot of any hash.
	if _, err := store.LatestComputeResult(ctx, rtParking); err != nil {
		t.Errorf("unpinned parking: %v, want the newest (hash B) snapshot", err)
	}
}

// TestSnapshot_UncommittedRunsAreInvisible: rows written under a run are not
// readable until a snapshot references the run, except through a WithParts
// view, which is how a command reads its own pending output.
func TestSnapshot_UncommittedRunsAreInvisible(t *testing.T) {
	ctx := context.Background()
	store := openTestStore(t)
	commitRun(t, store, "h", "roads", roadsRun(t, store, 100))
	run, err := store.CreateRun(ctx, "roads", "h")
	if err != nil {
		t.Fatal(err)
	}
	roadsRun(t, store, 200)(run.ID)

	if got := roadsArea(t, store); got != 100 {
		t.Errorf("committed view = %v, want 100 (the pending run is invisible)", got)
	}
	if got := roadsArea(t, store.WithParts("h", map[string]int64{"roads": run.ID})); got != 200 {
		t.Errorf("WithParts view = %v, want 200 from the pending run", got)
	}
	dropped := store.WithParts("h", map[string]int64{"roads": 0})
	if _, err := dropped.LatestComputeResult(ctx, rtRoads); !errors.Is(err, sql.ErrNoRows) {
		t.Errorf("WithParts dropping roads: err = %v, want sql.ErrNoRows", err)
	}
}

// TestCommitSnapshot_RejectsInconsistentRuns: a manifest may only reference
// this city's runs, each produced for the part it fills under the snapshot's
// hash. A rejected commit creates no snapshot.
func TestCommitSnapshot_RejectsInconsistentRuns(t *testing.T) {
	ctx := context.Background()
	root, err := Open(":memory:")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = root.Close() })
	idA, _ := root.EnsureCity(ctx, "a", "A", "test")
	idB, _ := root.EnsureCity(ctx, "b", "B", "test")
	storeA, storeB := root.ForCity(idA), root.ForCity(idB)

	roads, _ := storeA.CreateRun(ctx, "roads", "h")
	other, _ := storeB.CreateRun(ctx, "roads", "h")
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
		if _, err := storeA.CommitSnapshot(ctx, hash, parts); err == nil {
			t.Errorf("%s: commit succeeded, want an error", name)
		}
	}
	if snaps, _ := storeA.ListSnapshots(ctx); len(snaps) != 0 {
		t.Errorf("rejected commits left snapshots behind: %+v", snaps)
	}
}

// TestDeleteSnapshot_KeepsRunsOtherSnapshotsUse: deleting a snapshot removes
// only runs no other snapshot references, so every surviving snapshot still
// reads exactly what it did. Another city's id is not found.
func TestDeleteSnapshot_KeepsRunsOtherSnapshotsUse(t *testing.T) {
	ctx := context.Background()
	root, err := Open(":memory:")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = root.Close() })
	id, _ := root.EnsureCity(ctx, "a", "A", "test")
	other, _ := root.EnsureCity(ctx, "b", "B", "test")
	store := root.ForCity(id)

	roads1, _ := commitRun(t, store, "h", "roads", roadsRun(t, store, 100))
	commitRun(t, store, "h", "service:fire", fireRun(t, store, HexMetric{HexID: "f1"}))
	roads2, s3 := commitRun(t, store, "h", "roads", roadsRun(t, store, 200))

	if ok, err := root.ForCity(other).DeleteSnapshot(ctx, s3); err != nil || ok {
		t.Errorf("another city deleting s3 = (%v, %v), want (false, nil)", ok, err)
	}
	if ok, err := store.DeleteSnapshot(ctx, s3); err != nil || !ok {
		t.Fatalf("DeleteSnapshot(s3) = (%v, %v)", ok, err)
	}
	if n := countRows(t, root, `SELECT COUNT(*) FROM runs WHERE id = ?`, roads2); n != 0 {
		t.Error("the roads run only s3 used survived its deletion")
	}
	if n := countRows(t, root, `SELECT COUNT(*) FROM hex_stats WHERE run_id = ?`, roads2); n != 0 {
		t.Error("rows of the deleted run survived")
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
}

// TestDiscardRun: an uncommitted run and its rows go; a committed one is
// refused, because removing it would change a snapshot.
func TestDiscardRun(t *testing.T) {
	ctx := context.Background()
	store := openTestStore(t)
	committed, _ := commitRun(t, store, "h", "roads", roadsRun(t, store, 100))
	pending, err := store.CreateRun(ctx, "roads", "h")
	if err != nil {
		t.Fatal(err)
	}
	roadsRun(t, store, 200)(pending.ID)

	if err := store.DiscardRun(ctx, pending.ID); err != nil {
		t.Fatalf("DiscardRun(pending): %v", err)
	}
	raw := store.(*sqliteStore)
	var n int
	if err := raw.db.QueryRowContext(ctx, `SELECT COUNT(*) FROM compute_results WHERE run_id = ?`, pending.ID).Scan(&n); err != nil || n != 0 {
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
	old, _ := store.CreateRun(ctx, "roads", "h")
	roadsRun(t, store, 1)(old.ID)
	young, _ := store.CreateRun(ctx, "parking", "h")
	parkingRun(t, store, 1)(young.ID)
	stale := time.Now().Add(-2 * OrphanRunGrace).UTC().Format(time.DateTime)
	if _, err := root.db.ExecContext(ctx, `UPDATE runs SET computed_at = ? WHERE id IN (?, ?)`, stale, old.ID, committed); err != nil {
		t.Fatal(err)
	}

	scan, err := store.GCScan(ctx, []string{"overpass"})
	if err != nil {
		t.Fatal(err)
	}
	want := GCReport{OrphanRuns: 1, OrphanRunResults: GCResultCounts{ComputeResults: 1, HexStats: 1, CohortStats: 1}}
	if *scan != want {
		t.Errorf("scan = %+v, want %+v (only the old orphan)", *scan, want)
	}
	if n := countRows(t, root, `SELECT COUNT(*) FROM runs`); n != 3 {
		t.Errorf("scan wrote: %d runs remain, want 3", n)
	}
	swept, err := store.GCSweep(ctx, []string{"overpass"})
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
// string the CLI prints, even when the stored value carries an offset and
// sub-second digits.
func TestListSnapshots_TimestampsMarshalLikeTheCLI(t *testing.T) {
	ctx := context.Background()
	store := openTestStore(t)
	run, snap := commitRun(t, store, "h", "roads", nil)
	raw := store.(*sqliteStore).db
	const stored = "2026-03-04 05:06:07.123456789-07:00"
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

// TestResolveSnapshot: an owned id resolves; another city's, an unknown and
// a non-positive id do not.
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
	_, snap := commitRun(t, storeA, "x", "roads", nil)

	if err := storeA.ResolveSnapshot(ctx, snap); err != nil {
		t.Errorf("owning city: %v", err)
	}
	for name, s := range map[string]func() error{
		"another city": func() error { return root.ForCity(idB).ResolveSnapshot(ctx, snap) },
		"unknown id":   func() error { return storeA.ResolveSnapshot(ctx, 99999) },
		"zero id":      func() error { return storeA.ResolveSnapshot(ctx, 0) },
	} {
		if err := s(); err == nil {
			t.Errorf("%s resolved, want an error", name)
		}
	}
}

// TestDraft: a draft's Store sees parts as they are put, Commit writes one
// snapshot with exactly those parts swapped in and discards runs that were
// created but never put, and Discard deletes every run it created.
func TestDraft(t *testing.T) {
	ctx := context.Background()
	store := openTestStore(t)
	commitRun(t, store, "h", "service:fire", fireRun(t, store, HexMetric{HexID: "f1"}))

	d := NewDraft(store, "h")
	roads, err := d.NewRun(ctx, "roads")
	if err != nil {
		t.Fatal(err)
	}
	roadsRun(t, store, 100)(roads)
	if _, err := d.Store().LatestComputeResult(ctx, rtRoads); !errors.Is(err, sql.ErrNoRows) {
		t.Errorf("draft Store before Put: err = %v, want sql.ErrNoRows", err)
	}
	d.Put("roads", roads)
	if got := roadsArea(t, d.Store()); got != 100 {
		t.Errorf("draft Store after Put = %v, want 100", got)
	}
	abandoned, _ := d.NewRun(ctx, "parking")
	parkingRun(t, store, 5)(abandoned)

	snap, err := d.Commit(ctx)
	if err != nil {
		t.Fatal(err)
	}
	if got := parts(t, store, snap.ID); len(got) != 2 || got["roads"] != roads || got["service:fire"] == 0 {
		t.Errorf("committed manifest = %v, want roads=%d plus the carried fire part", got, roads)
	}
	raw := store.(*sqliteStore)
	var n int
	if err := raw.db.QueryRowContext(ctx, `SELECT COUNT(*) FROM runs WHERE id = ?`, abandoned).Scan(&n); err != nil || n != 0 {
		t.Errorf("the never-put run survived Commit (%d, %v)", n, err)
	}

	if s, err := NewDraft(store, "h").Commit(ctx); err != nil || s == nil || s.ID != snap.ID {
		t.Errorf("empty draft Commit = (%v, %v), want the current snapshot %d unchanged", s, err, snap.ID)
	}

	d2 := NewDraft(store, "h")
	r2, _ := d2.NewRun(ctx, "roads")
	d2.Put("roads", r2)
	if err := d2.Discard(ctx); err != nil {
		t.Fatal(err)
	}
	if err := raw.db.QueryRowContext(ctx, `SELECT COUNT(*) FROM runs WHERE id = ?`, r2).Scan(&n); err != nil || n != 0 {
		t.Errorf("Discard left run %d behind (%d, %v)", r2, n, err)
	}
	if got := roadsArea(t, store); got != 100 {
		t.Errorf("roads after a discarded draft = %v, want the committed 100", got)
	}
}

// TestDraft_DropRemovesPart: Drop takes a part out of the next snapshot, e.g.
// a forecast whose rerun failed and must not be paired with a newer compute.
func TestDraft_DropRemovesPart(t *testing.T) {
	ctx := context.Background()
	store := openTestStore(t)
	commitRun(t, store, "h", "forecast:roads", nil)
	d := NewDraft(store, "h")
	d.Drop("forecast:roads")
	snap, err := d.Commit(ctx)
	if err != nil {
		t.Fatal(err)
	}
	if got := parts(t, store, snap.ID); len(got) != 0 {
		t.Errorf("manifest after Drop = %v, want empty", got)
	}
}

// TestHexMetrics_ReplaceWithinRun: saving the same metric twice in one run
// keeps the last save, another metric of the run is untouched, and an unknown
// metric lists (nil, nil).
func TestHexMetrics_ReplaceWithinRun(t *testing.T) {
	ctx := context.Background()
	store := openTestStore(t)
	commitRun(t, store, "h", "census", func(run int64) {
		for _, save := range []struct {
			metric string
			rows   []HexMetric
		}{
			{"population", []HexMetric{{HexID: "h1", Value: 12}}},
			{"density", []HexMetric{{HexID: "h1", Value: 3, Ref: "x"}}},
			{"population", []HexMetric{{HexID: "h2", Value: 9}}},
		} {
			if err := store.SaveHexMetrics(ctx, save.metric, run, save.rows); err != nil {
				t.Fatal(err)
			}
		}
	})
	pop, _ := store.ListHexMetrics(ctx, "population")
	if len(pop) != 1 || pop[0].HexID != "h2" || pop[0].Value != 9 || pop[0].Metric != "population" {
		t.Errorf("population = %+v, want only the last save (h2=9)", pop)
	}
	den, _ := store.ListHexMetrics(ctx, "density")
	if len(den) != 1 || den[0].Ref != "x" {
		t.Errorf("density = %+v, want its own row untouched", den)
	}
	if none, err := store.ListHexMetrics(ctx, "nope"); none != nil || err != nil {
		t.Errorf("unknown metric = (%v, %v), want (nil, nil)", none, err)
	}
}

// TestSaveHexMetricSet_Atomic: the keys of one set commit or roll back
// together. A trigger aborts the second key's insert (sorted order), and the
// first key's replacement must roll back with it.
func TestSaveHexMetricSet_Atomic(t *testing.T) {
	ctx := context.Background()
	store := openTestStore(t)
	run, _ := commitRun(t, store, "h", "census", func(run int64) {
		if err := store.SaveHexMetricSet(ctx, run, map[string][]HexMetric{
			"a_metric": {{HexID: "h1", Value: 1}},
			"b_metric": {{HexID: "h1", Value: 2}},
		}); err != nil {
			t.Fatal(err)
		}
	})
	raw := store.(*sqliteStore).db
	if _, err := raw.ExecContext(ctx, `CREATE TRIGGER fail_b BEFORE INSERT ON hex_metrics
		WHEN NEW.metric = 'b_metric' BEGIN SELECT RAISE(ABORT, 'boom'); END`); err != nil {
		t.Fatal(err)
	}
	if err := store.SaveHexMetricSet(ctx, run, map[string][]HexMetric{
		"a_metric": {{HexID: "h1", Value: 10}},
		"b_metric": {{HexID: "h1", Value: 20}},
	}); err == nil {
		t.Fatal("expected the aborted insert to fail the set")
	}
	for key, want := range map[string]float64{"a_metric": 1, "b_metric": 2} {
		rows, err := store.ListHexMetrics(ctx, key)
		if err != nil {
			t.Fatal(err)
		}
		if len(rows) != 1 || rows[0].Value != want {
			t.Errorf("%s = %+v after a failed set, want the previous value %v (rolled back)", key, rows, want)
		}
	}
}

// TestHasHexStats agrees with ListHexStats: false with no snapshot and for
// types without rows, true once any requested type has rows, and scoped to the
// resolved snapshot like every other read.
func TestHasHexStats(t *testing.T) {
	ctx := context.Background()
	store := openTestStore(t)
	both := []resource.Type{rtRoads, rtParking}
	if ok, err := store.HasHexStats(ctx, both); err != nil || ok {
		t.Fatalf("no snapshot: (%v, %v), want (false, nil)", ok, err)
	}
	_, snap := commitRun(t, store, "hash", "parking", func(run int64) {
		if err := store.SaveHexStats(ctx, []HexStat{{HexID: "h1", ResourceType: rtParking, Area: 1, PctCovered: 1, RunID: run}}); err != nil {
			t.Fatal(err)
		}
	})
	if ok, err := store.HasHexStats(ctx, []resource.Type{rtRoads}); err != nil || ok {
		t.Errorf("roads only: (%v, %v), want (false, nil)", ok, err)
	}
	if ok, err := store.HasHexStats(ctx, both); err != nil || !ok {
		t.Errorf("roads+parking: (%v, %v), want (true, nil)", ok, err)
	}
	if ok, _ := store.WithConfigHash("other").HasHexStats(ctx, both); ok {
		t.Error("a different config hash must not see the rows")
	}
	if ok, _ := store.WithSnapshot(snap+100).HasHexStats(ctx, both); ok {
		t.Error("a pin to a snapshot that does not exist must see nothing")
	}
}

// TestEstimatedSpacesRoundTrip: the parking compute's estimated_spaces is read
// back by every reader (single, batched, Stats); a resource that never sets it
// reads 0.
func TestEstimatedSpacesRoundTrip(t *testing.T) {
	ctx := context.Background()
	store := openTestStore(t)
	commitRun(t, store, "h", "parking", func(run int64) {
		if err := store.SaveComputeResult(ctx, ComputeResult{ResourceType: rtParking, TotalArea: 900, FeatureCount: 3, EstimatedSpaces: 31, RunID: run}); err != nil {
			t.Fatal(err)
		}
	})
	commitRun(t, store, "h", "roads", roadsRun(t, store, 5000))

	got, err := store.LatestComputeResult(ctx, rtParking)
	if err != nil || got.EstimatedSpaces != 31 {
		t.Errorf("LatestComputeResult(parking) = (%+v, %v), want 31 spaces", got, err)
	}
	batch, err := store.LatestComputeResults(ctx, []resource.Type{rtParking, rtRoads})
	if err != nil || batch[rtParking].EstimatedSpaces != 31 || batch[rtRoads].EstimatedSpaces != 0 {
		t.Errorf("LatestComputeResults = (%+v, %v), want parking 31 / roads 0", batch, err)
	}
	info, err := store.Stats(ctx, rtParking)
	if err != nil || info.EstimatedSpaces != 31 {
		t.Errorf("Stats(parking) = (%+v, %v), want 31", info, err)
	}
}

// TestGCResultTablesCoversResultTables: gc and deletion share one list of
// run-tagged tables, each with its own counter.
func TestGCResultTablesCoversResultTables(t *testing.T) {
	var counts GCResultCounts
	pairs := gcResultTables(&counts)
	if len(pairs) != len(resultTables) {
		t.Fatalf("gcResultTables has %d entries, resultTables has %d", len(pairs), len(resultTables))
	}
	seen := map[*int]string{}
	for i, p := range pairs {
		if p.table != resultTables[i] {
			t.Errorf("gcResultTables[%d] = %q, want %q", i, p.table, resultTables[i])
		}
		if prev, dup := seen[p.count]; dup {
			t.Errorf("%s and %s share a counter", prev, p.table)
		}
		seen[p.count] = p.table
	}
}

// TestCommitSnapshot_ConcurrentProcesses: two pvmt processes (two handles on
// one file) committing at once must both succeed. Commits read the base
// snapshot and then write, so a DEFERRED transaction loses the race with
// SQLITE_BUSY_SNAPSHOT (517), which busy_timeout cannot retry, and the
// caller then discards its runs. Write transactions take the lock up front
// (_txlock=immediate) so the loser waits instead.
func TestCommitSnapshot_ConcurrentProcesses(t *testing.T) {
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
	var wg sync.WaitGroup
	for i, store := range stores {
		producer := []string{"roads", "parking"}[i]
		wg.Go(func() {
			for range perProcess {
				run, err := store.CreateRun(ctx, producer, "h")
				if err == nil {
					_, err = store.CommitSnapshot(ctx, "h", map[string]int64{producer: run.ID})
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
	snaps, err := stores[0].ListSnapshots(ctx)
	if err != nil {
		t.Fatal(err)
	}
	if len(snaps) != 2*perProcess {
		t.Errorf("snapshots = %d, want %d", len(snaps), 2*perProcess)
	}
	// Every commit composed on the true newest base: the final snapshot has
	// both producers' latest runs, none lost to a stale base.
	if got := parts(t, stores[0], snaps[0].ID); len(got) != 2 {
		t.Errorf("final manifest = %v, want both roads and parking", got)
	}
}

// TestCommitSnapshot_NoOpWritesNothing: a commit that would not change the
// manifest mints no snapshot (it would take a `snapshots prune` slot), and a
// hash with no snapshot never gets an EMPTY one, which export would publish
// as a blank city. Dropping a part that is not there is such a no-op: it is
// how a census-disabled or station-less city reports "nothing to do".
func TestCommitSnapshot_NoOpWritesNothing(t *testing.T) {
	ctx := context.Background()
	store := openTestStore(t)

	snap, err := store.CommitSnapshot(ctx, "h", map[string]int64{"census": 0})
	if err != nil || snap != nil {
		t.Errorf("drop-only commit with no base = (%+v, %v), want (nil, nil)", snap, err)
	}
	if snaps, _ := store.ListSnapshots(ctx); len(snaps) != 0 {
		t.Fatalf("a no-op commit created snapshots: %+v", snaps)
	}

	roads, s1 := commitRun(t, store, "h", "roads", nil)
	for name, parts := range map[string]map[string]int64{
		"drop of an absent part": {"census": 0},
		"same run again":         {"roads": roads},
		"nothing":                {},
	} {
		got, err := store.CommitSnapshot(ctx, "h", parts)
		if err != nil || got == nil || got.ID != s1 {
			t.Errorf("%s: commit = (%+v, %v), want the unchanged snapshot %d", name, got, err, s1)
		}
	}
	if snaps, _ := store.ListSnapshots(ctx); len(snaps) != 1 {
		t.Errorf("no-op commits minted snapshots: %d, want 1", len(snaps))
	}
	if _, err := store.CommitSnapshot(ctx, "", map[string]int64{"roads": roads}); err == nil {
		t.Error("commit with an empty config hash succeeded; it would take its base from any hash")
	}
}

// TestStaleFor: stale means "computed, but not under these settings"; a city
// never computed is not stale (its hint is a different one).
func TestStaleFor(t *testing.T) {
	for _, tc := range []struct {
		name  string
		snaps []Snapshot
		want  bool
	}{
		{"never computed", nil, false},
		{"only other hashes", []Snapshot{{ID: 2, ConfigHash: "b"}, {ID: 1, ConfigHash: "c"}}, true},
		{"one matches", []Snapshot{{ID: 2, ConfigHash: "b"}, {ID: 1, ConfigHash: "a"}}, false},
	} {
		if got := StaleFor(tc.snaps, "a"); got != tc.want {
			t.Errorf("%s: StaleFor = %t, want %t", tc.name, got, tc.want)
		}
	}
}
