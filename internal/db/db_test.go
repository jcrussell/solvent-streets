package db

import (
	"context"
	"database/sql"
	"errors"
	"strings"
	"testing"
	"time"

	"github.com/jcrussell/solvent-streets/internal/resource"
)

var (
	rtRoads   = resource.TypeRoads
	rtParking = resource.TypeParking
)

// openTestStore opens an in-memory DB and returns a Store scoped to a test city.
func openTestStore(t *testing.T) Store {
	t.Helper()
	root, err := Open(":memory:")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = root.Close() })
	id, err := root.EnsureCity(context.Background(), "test-city", "Test City")
	if err != nil {
		t.Fatal(err)
	}
	return root.ForCity(id)
}

func TestStoreRoundTrip(t *testing.T) {
	ctx := context.Background()
	store := openTestStore(t)

	features := []Feature{
		{ID: "osm:way:1", ResourceType: rtRoads, Name: "Main St", Tags: map[string]string{"highway": "primary"}, GeometryJSON: `{"type":"LineString","coordinates":[[-121.76,37.68],[-121.75,37.68]]}`, SourceAPI: "overpass", FetchedAt: time.Now()},
		{ID: "osm:way:2", ResourceType: rtRoads, Name: "Oak Ave", Tags: map[string]string{"highway": "residential"}, GeometryJSON: `{"type":"LineString","coordinates":[[-121.76,37.69],[-121.75,37.69]]}`, SourceAPI: "overpass", FetchedAt: time.Now()},
	}

	if err := store.UpsertFeatures(ctx, rtRoads, features); err != nil {
		t.Fatal(err)
	}

	got, err := store.ListFeatures(ctx, rtRoads)
	if err != nil {
		t.Fatal(err)
	}
	if len(got) != 2 {
		t.Errorf("expected 2 features, got %d", len(got))
	}

	// Upsert same features — should update, not duplicate
	if err := store.UpsertFeatures(ctx, rtRoads, features); err != nil {
		t.Fatal(err)
	}
	got, err = store.ListFeatures(ctx, rtRoads)
	if err != nil {
		t.Fatal(err)
	}
	if len(got) != 2 {
		t.Errorf("expected 2 features after upsert, got %d", len(got))
	}
}

// TestSnapshotPinningAcrossResultTables exercises the append-only Save methods
// + WithSnapshot read filtering across compute_results, hex_stats, cohort_stats,
// and forecast_results. Two distinct snapshots coexist; reads pinned to each
// id see only that snapshot's rows.
func TestSnapshotPinningAcrossResultTables(t *testing.T) {
	ctx := context.Background()
	store := openTestStore(t)

	snap1, err := store.CreateSnapshot(ctx, "hash-1")
	if err != nil {
		t.Fatal(err)
	}
	snap2, err := store.CreateSnapshot(ctx, "hash-2")
	if err != nil {
		t.Fatal(err)
	}

	for _, snap := range []*Snapshot{snap1, snap2} {
		id := snap.ID
		if err := store.SaveComputeResult(ctx, ComputeResult{
			ResourceType: rtRoads, TotalAreaSqM: float64(id * 100), FeatureCount: int(id), SnapshotID: &id,
		}); err != nil {
			t.Fatal(err)
		}
		if err := store.SaveHexStats(ctx, []HexStat{
			{HexID: "h1", ResourceType: rtRoads, AreaSqM: float64(id * 10), PctCovered: 0.5, SnapshotID: &id},
		}); err != nil {
			t.Fatal(err)
		}
		if err := store.SaveCohortStats(ctx, []CohortStat{
			{ResourceType: rtRoads, Classification: "primary", AreaSqM: float64(id * 1000), FeatureCount: 1, SnapshotID: &id},
		}); err != nil {
			t.Fatal(err)
		}
		if err := store.SaveForecastResults(ctx, []ForecastResult{
			{ResourceType: rtRoads, Year: 2026, PCI: float64(id * 10), AreaSqM: 100, TreatmentCost: 200, TreatmentTier: "preventive", SnapshotID: &id},
		}); err != nil {
			t.Fatal(err)
		}
	}

	// Unpinned reads must return only the latest snapshot's rows per
	// (city, resource_type). Returning every snapshot's rows would
	// duplicate every export, server, and forecast read after a re-run.
	latest, err := store.LatestComputeResult(ctx, rtRoads)
	if err != nil {
		t.Fatal(err)
	}
	if latest.TotalAreaSqM != float64(snap2.ID*100) {
		t.Errorf("unpinned latest: expected snap2 area, got %v", latest.TotalAreaSqM)
	}
	hexAll, _ := store.ListHexStats(ctx, rtRoads)
	if len(hexAll) != 1 || hexAll[0].AreaSqM != float64(snap2.ID*10) {
		t.Errorf("unpinned hex_stats: want only snap2's row (area %v), got %+v", snap2.ID*10, hexAll)
	}
	cohortAll, _ := store.ListCohortStats(ctx, rtRoads)
	if len(cohortAll) != 1 || cohortAll[0].AreaSqM != float64(snap2.ID*1000) {
		t.Errorf("unpinned cohort_stats: want only snap2's row (area %v), got %+v", snap2.ID*1000, cohortAll)
	}
	fcAll, _ := store.ListForecastResults(ctx, rtRoads)
	if len(fcAll) != 1 || fcAll[0].PCI != float64(snap2.ID*10) {
		t.Errorf("unpinned forecast_results: want only snap2's row (pci %v), got %+v", snap2.ID*10, fcAll)
	}

	// Pinned to snap1.
	pinned1 := store.WithSnapshot(snap1.ID)
	cr1, err := pinned1.LatestComputeResult(ctx, rtRoads)
	if err != nil {
		t.Fatal(err)
	}
	if cr1.TotalAreaSqM != float64(snap1.ID*100) {
		t.Errorf("pinned snap1: expected %v area, got %v", snap1.ID*100, cr1.TotalAreaSqM)
	}
	hex1, _ := pinned1.ListHexStats(ctx, rtRoads)
	if len(hex1) != 1 || hex1[0].AreaSqM != float64(snap1.ID*10) {
		t.Errorf("pinned snap1 hex: want 1 row with area %v, got %+v", snap1.ID*10, hex1)
	}
	cohort1, _ := pinned1.ListCohortStats(ctx, rtRoads)
	if len(cohort1) != 1 || cohort1[0].AreaSqM != float64(snap1.ID*1000) {
		t.Errorf("pinned snap1 cohort: want 1 row with area %v, got %+v", snap1.ID*1000, cohort1)
	}
	fc1, _ := pinned1.ListForecastResults(ctx, rtRoads)
	if len(fc1) != 1 || fc1[0].PCI != float64(snap1.ID*10) {
		t.Errorf("pinned snap1 forecast: want 1 row with pci %v, got %+v", snap1.ID*10, fc1)
	}

	// Pinned to snap2 sees only snap2's row.
	pinned2 := store.WithSnapshot(snap2.ID)
	cr2, err := pinned2.LatestComputeResult(ctx, rtRoads)
	if err != nil {
		t.Fatal(err)
	}
	if cr2.TotalAreaSqM != float64(snap2.ID*100) {
		t.Errorf("pinned snap2: expected %v area, got %v", snap2.ID*100, cr2.TotalAreaSqM)
	}
}

// TestListReads_LatestSnapshotPerResource pins the regression: when the
// same resource type writes hex_stats / cohort_stats / forecast_results
// rows under two different snapshots (e.g. `pvmt compute roads` ran
// twice), an unpinned read returns only the later snapshot's rows.
// Without this, every `pvmt export` after a re-run emits duplicated
// hex GeoJSON features.
func TestListReads_LatestSnapshotPerResource(t *testing.T) {
	ctx := context.Background()
	store := openTestStore(t)

	snapOld, err := store.CreateSnapshot(ctx, "old")
	if err != nil {
		t.Fatal(err)
	}
	snapNew, err := store.CreateSnapshot(ctx, "new")
	if err != nil {
		t.Fatal(err)
	}

	// Same hex_id, same resource_type, two snapshots — the duplication
	// pattern produced by re-running `pvmt compute`.
	for _, sid := range []int64{snapOld.ID, snapNew.ID} {
		id := sid
		if err := store.SaveHexStats(ctx, []HexStat{
			{HexID: "h1", ResourceType: rtRoads, AreaSqM: float64(id), PctCovered: 50, SnapshotID: &id},
			{HexID: "h2", ResourceType: rtRoads, AreaSqM: float64(id), PctCovered: 50, SnapshotID: &id},
		}); err != nil {
			t.Fatal(err)
		}
		if err := store.SaveCohortStats(ctx, []CohortStat{
			{ResourceType: rtRoads, Classification: "primary", AreaSqM: float64(id), FeatureCount: 1, SnapshotID: &id},
		}); err != nil {
			t.Fatal(err)
		}
		if err := store.SaveForecastResults(ctx, []ForecastResult{
			{ResourceType: rtRoads, Year: 2026, PCI: float64(id), AreaSqM: 1, TreatmentCost: 1, TreatmentTier: "preventive", SnapshotID: &id},
		}); err != nil {
			t.Fatal(err)
		}
	}

	hex, _ := store.ListHexStats(ctx, rtRoads)
	if len(hex) != 2 {
		t.Errorf("unpinned ListHexStats: got %d rows, want 2 (latest snapshot only)", len(hex))
	}
	for _, h := range hex {
		if h.AreaSqM != float64(snapNew.ID) {
			t.Errorf("hex row from wrong snapshot: AreaSqM=%v, want %v", h.AreaSqM, snapNew.ID)
		}
	}

	co, _ := store.ListCohortStats(ctx, rtRoads)
	if len(co) != 1 || co[0].AreaSqM != float64(snapNew.ID) {
		t.Errorf("unpinned ListCohortStats: want 1 row from snapNew (area %v), got %+v", snapNew.ID, co)
	}

	fc, _ := store.ListForecastResults(ctx, rtRoads)
	if len(fc) != 1 || fc[0].PCI != float64(snapNew.ID) {
		t.Errorf("unpinned ListForecastResults: want 1 row from snapNew (pci %v), got %+v", snapNew.ID, fc)
	}

	// Pinning to the older snapshot still returns its rows verbatim.
	old := store.WithSnapshot(snapOld.ID)
	if hex, _ := old.ListHexStats(ctx, rtRoads); len(hex) != 2 || hex[0].AreaSqM != float64(snapOld.ID) {
		t.Errorf("pinned snapOld ListHexStats: want 2 rows with area %v, got %+v", snapOld.ID, hex)
	}
}

// TestListReads_NullLegacyFallback covers the migration-002 legacy
// path: when a city has only NULL-snapshot_id rows (pre-snapshot data),
// unpinned reads still surface them. SQLite's `IS` operator makes
// `snapshot_id IS (SELECT MAX(...))` evaluate to true for NULL rows
// when MAX returns NULL (no snapshot-tagged rows exist).
func TestListReads_NullLegacyFallback(t *testing.T) {
	ctx := context.Background()
	store := openTestStore(t)

	// Write rows directly with NULL snapshot_id — simulating data
	// inserted before migration 002 added the column.
	if err := store.SaveHexStats(ctx, []HexStat{
		{HexID: "legacy", ResourceType: rtRoads, AreaSqM: 42, PctCovered: 100, SnapshotID: nil},
	}); err != nil {
		t.Fatal(err)
	}
	if err := store.SaveCohortStats(ctx, []CohortStat{
		{ResourceType: rtRoads, Classification: "primary", AreaSqM: 7, FeatureCount: 1, SnapshotID: nil},
	}); err != nil {
		t.Fatal(err)
	}
	if err := store.SaveForecastResults(ctx, []ForecastResult{
		{ResourceType: rtRoads, Year: 2026, PCI: 99, AreaSqM: 1, TreatmentCost: 1, TreatmentTier: "preventive", SnapshotID: nil},
	}); err != nil {
		t.Fatal(err)
	}

	if hex, _ := store.ListHexStats(ctx, rtRoads); len(hex) != 1 || hex[0].AreaSqM != 42 {
		t.Errorf("legacy ListHexStats: want 1 NULL-snapshot row (area 42), got %+v", hex)
	}
	if co, _ := store.ListCohortStats(ctx, rtRoads); len(co) != 1 || co[0].AreaSqM != 7 {
		t.Errorf("legacy ListCohortStats: want 1 NULL-snapshot row (area 7), got %+v", co)
	}
	if fc, _ := store.ListForecastResults(ctx, rtRoads); len(fc) != 1 || fc[0].PCI != 99 {
		t.Errorf("legacy ListForecastResults: want 1 NULL-snapshot row (pci 99), got %+v", fc)
	}
}

// TestListReads_ConfigHashScoping pins the new third axis: when the
// store is unpinned but tagged with WithConfigHash(H), reads return
// only rows whose snapshot's config_hash matches H. Two configs
// writing to the same (city, resource_type) must coexist — the bug
// this guards against is the slug-sharing case in examples/ (austin
// in single-city pvmt.toml at 100m vs austin in city-nerd at 150m
// sharing city_id, where unpinned reads were returning whichever
// snapshot wrote last and producing incompatible hex_id namespaces).
//
// Also pins the pin precedence: WithSnapshot(N) wins over
// WithConfigHash(H) because it's the more specific request — matches
// how the server's ?snapshot=N URL param overrides any other scoping.
func TestListReads_ConfigHashScoping(t *testing.T) {
	ctx := context.Background()
	store := openTestStore(t)

	snapH1, err := store.CreateSnapshot(ctx, "hash-1")
	if err != nil {
		t.Fatal(err)
	}
	snapH2, err := store.CreateSnapshot(ctx, "hash-2")
	if err != nil {
		t.Fatal(err)
	}

	for _, snap := range []*Snapshot{snapH1, snapH2} {
		id := snap.ID
		if err := store.SaveHexStats(ctx, []HexStat{
			{HexID: "h1", ResourceType: rtRoads, AreaSqM: float64(id * 10), PctCovered: 50, SnapshotID: &id},
		}); err != nil {
			t.Fatal(err)
		}
		if err := store.SaveCohortStats(ctx, []CohortStat{
			{ResourceType: rtRoads, Classification: "primary", AreaSqM: float64(id * 100), FeatureCount: 1, SnapshotID: &id},
		}); err != nil {
			t.Fatal(err)
		}
		if err := store.SaveForecastResults(ctx, []ForecastResult{
			{ResourceType: rtRoads, Year: 2026, PCI: float64(id * 5), AreaSqM: 1, TreatmentCost: 1, TreatmentTier: "preventive", SnapshotID: &id},
		}); err != nil {
			t.Fatal(err)
		}
		if err := store.SaveComputeResult(ctx, ComputeResult{
			ResourceType: rtRoads, TotalAreaSqM: float64(id), FeatureCount: int(id), SnapshotID: &id,
		}); err != nil {
			t.Fatal(err)
		}
	}

	// Unpinned + no config hash: arm 3 — latest overall (snapH2's row).
	if hex, _ := store.ListHexStats(ctx, rtRoads); len(hex) != 1 || hex[0].AreaSqM != float64(snapH2.ID*10) {
		t.Errorf("unpinned-no-hash ListHexStats: want snapH2's row (area %v), got %+v", snapH2.ID*10, hex)
	}

	// Unpinned + WithConfigHash(hash-1): arm 2 — only snapH1's row.
	h1 := store.WithConfigHash("hash-1")
	if hex, _ := h1.ListHexStats(ctx, rtRoads); len(hex) != 1 || hex[0].AreaSqM != float64(snapH1.ID*10) {
		t.Errorf("WithConfigHash(hash-1) ListHexStats: want snapH1's row (area %v), got %+v", snapH1.ID*10, hex)
	}
	if co, _ := h1.ListCohortStats(ctx, rtRoads); len(co) != 1 || co[0].AreaSqM != float64(snapH1.ID*100) {
		t.Errorf("WithConfigHash(hash-1) ListCohortStats: want snapH1's row (area %v), got %+v", snapH1.ID*100, co)
	}
	if fc, _ := h1.ListForecastResults(ctx, rtRoads); len(fc) != 1 || fc[0].PCI != float64(snapH1.ID*5) {
		t.Errorf("WithConfigHash(hash-1) ListForecastResults: want snapH1's row (pci %v), got %+v", snapH1.ID*5, fc)
	}
	if cr, err := h1.LatestComputeResult(ctx, rtRoads); err != nil || cr.TotalAreaSqM != float64(snapH1.ID) {
		t.Errorf("WithConfigHash(hash-1) LatestComputeResult: want snapH1's row (area %v), got %+v err=%v", snapH1.ID, cr, err)
	}

	// Unpinned + WithConfigHash(hash-2): arm 2 — only snapH2's row.
	h2 := store.WithConfigHash("hash-2")
	if hex, _ := h2.ListHexStats(ctx, rtRoads); len(hex) != 1 || hex[0].AreaSqM != float64(snapH2.ID*10) {
		t.Errorf("WithConfigHash(hash-2) ListHexStats: want snapH2's row (area %v), got %+v", snapH2.ID*10, hex)
	}

	// Unpinned + WithConfigHash(no-match): arm 2 with empty subquery —
	// no NULL-snapshot rows exist, so returns empty (NOT a fallback to
	// latest overall — that's the contract the slug-collision fix
	// relies on).
	if hex, _ := store.WithConfigHash("no-match").ListHexStats(ctx, rtRoads); len(hex) != 0 {
		t.Errorf("WithConfigHash(no-match) ListHexStats: want 0 rows, got %+v", hex)
	}

	// WithSnapshot(snapH1) + WithConfigHash(hash-2): pin precedence —
	// snapshot wins because it's more specific.
	mixed := store.WithSnapshot(snapH1.ID).WithConfigHash("hash-2")
	if hex, _ := mixed.ListHexStats(ctx, rtRoads); len(hex) != 1 || hex[0].AreaSqM != float64(snapH1.ID*10) {
		t.Errorf("WithSnapshot+WithConfigHash: snapshot pin must win, want snapH1's row, got %+v", hex)
	}
}

// TestDeleteSnapshot_CascadesAndCityScoped pins two contracts the
// snapshots rm/prune CLI relies on: (1) a single-tx delete cascades to
// every FK-linked result table, and (2) the city scope prevents a
// caller in city B from deleting a snapshot owned by city A.
func TestDeleteSnapshot_CascadesAndCityScoped(t *testing.T) {
	ctx := context.Background()
	root, err := Open(":memory:")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = root.Close() })

	idA, _ := root.EnsureCity(ctx, "a", "A")
	idB, _ := root.EnsureCity(ctx, "b", "B")
	storeA := root.ForCity(idA)
	storeB := root.ForCity(idB)

	snap, err := storeA.CreateSnapshot(ctx, "h")
	if err != nil {
		t.Fatal(err)
	}
	sid := snap.ID

	if err := storeA.SaveComputeResult(ctx, ComputeResult{ResourceType: rtRoads, TotalAreaSqM: 1, SnapshotID: &sid}); err != nil {
		t.Fatal(err)
	}
	if err := storeA.SaveHexStats(ctx, []HexStat{{HexID: "h1", ResourceType: rtRoads, SnapshotID: &sid}}); err != nil {
		t.Fatal(err)
	}
	if err := storeA.SaveCohortStats(ctx, []CohortStat{{ResourceType: rtRoads, Classification: "primary", SnapshotID: &sid}}); err != nil {
		t.Fatal(err)
	}
	if err := storeA.SaveForecastResults(ctx, []ForecastResult{{ResourceType: rtRoads, Year: 2026, SnapshotID: &sid}}); err != nil {
		t.Fatal(err)
	}

	// City B cannot delete city A's snapshot.
	ok, err := storeB.DeleteSnapshot(ctx, sid)
	if err != nil {
		t.Fatalf("DeleteSnapshot across cities: %v", err)
	}
	if ok {
		t.Errorf("DeleteSnapshot from wrong city should return false, got true")
	}
	// Sanity: snapshot still exists in city A.
	snaps, _ := storeA.ListSnapshots(ctx)
	if len(snaps) != 1 {
		t.Fatalf("city A should still have 1 snapshot after cross-city delete attempt, got %d", len(snaps))
	}

	// City A deletes successfully and cascades to every result table.
	ok, err = storeA.DeleteSnapshot(ctx, sid)
	if err != nil {
		t.Fatalf("DeleteSnapshot: %v", err)
	}
	if !ok {
		t.Errorf("DeleteSnapshot expected true, got false")
	}

	snaps, _ = storeA.ListSnapshots(ctx)
	if len(snaps) != 0 {
		t.Errorf("snapshots after delete: got %d, want 0", len(snaps))
	}
	pinned := storeA.WithSnapshot(sid)
	if hs, _ := pinned.ListHexStats(ctx, rtRoads); len(hs) != 0 {
		t.Errorf("hex_stats after delete: got %d, want 0", len(hs))
	}
	if cs, _ := pinned.ListCohortStats(ctx, rtRoads); len(cs) != 0 {
		t.Errorf("cohort_stats after delete: got %d, want 0", len(cs))
	}
	if fc, _ := pinned.ListForecastResults(ctx, rtRoads); len(fc) != 0 {
		t.Errorf("forecast_results after delete: got %d, want 0", len(fc))
	}
	var cr int
	if err := root.db.QueryRowContext(ctx, `SELECT COUNT(*) FROM compute_results WHERE snapshot_id = ?`, sid).Scan(&cr); err != nil {
		t.Fatal(err)
	}
	if cr != 0 {
		t.Errorf("compute_results after delete: got %d, want 0", cr)
	}

	// Idempotent / not-found returns (false, nil).
	ok, err = storeA.DeleteSnapshot(ctx, sid)
	if err != nil {
		t.Errorf("DeleteSnapshot on already-deleted id: %v", err)
	}
	if ok {
		t.Errorf("DeleteSnapshot on already-deleted id: want false, got true")
	}
	// Invalid id is a clear error.
	if _, err := storeA.DeleteSnapshot(ctx, 0); err == nil {
		t.Errorf("DeleteSnapshot(0) should error")
	}
}

// TestResolveSnapshot verifies city-scoped existence checks and the error
// shape the server handler relies on for 404 mapping.
func TestResolveSnapshot(t *testing.T) {
	ctx := context.Background()
	root, err := Open(":memory:")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = root.Close() })

	idA, _ := root.EnsureCity(ctx, "a", "A")
	idB, _ := root.EnsureCity(ctx, "b", "B")
	storeA := root.ForCity(idA)
	storeB := root.ForCity(idB)

	snapA, err := storeA.CreateSnapshot(ctx, "x")
	if err != nil {
		t.Fatal(err)
	}

	// City A sees its own snapshot.
	if err := storeA.ResolveSnapshot(ctx, snapA.ID); err != nil {
		t.Errorf("ResolveSnapshot on owning city: %v", err)
	}
	// City B does not — snapshot belongs to city A.
	if err := storeB.ResolveSnapshot(ctx, snapA.ID); err == nil {
		t.Errorf("ResolveSnapshot across cities should fail, got nil")
	}
	// Unknown id is not nil error.
	if err := storeA.ResolveSnapshot(ctx, 99999); err == nil {
		t.Errorf("ResolveSnapshot on unknown id should fail, got nil")
	}
	// Invalid id (<=0) returns a non-nil error too.
	if err := storeA.ResolveSnapshot(ctx, 0); err == nil {
		t.Errorf("ResolveSnapshot on 0 should fail, got nil")
	}
}

func TestStoreComputeResult(t *testing.T) {
	ctx := context.Background()
	store := openTestStore(t)

	result := ComputeResult{
		ResourceType: rtRoads,
		TotalAreaSqM: 92903,
		FeatureCount: 500,
	}
	if err := store.SaveComputeResult(ctx, result); err != nil {
		t.Fatal(err)
	}

	got, err := store.LatestComputeResult(ctx, rtRoads)
	if err != nil {
		t.Fatal(err)
	}
	if got.TotalAreaSqM != 92903 {
		t.Errorf("expected area 92903, got %f", got.TotalAreaSqM)
	}
	if got.FeatureCount != 500 {
		t.Errorf("expected 500 features, got %d", got.FeatureCount)
	}
}

func TestStoreStats(t *testing.T) {
	ctx := context.Background()
	store := openTestStore(t)

	features := []Feature{
		{ID: "1", Name: "test", Tags: map[string]string{}, GeometryJSON: `{}`, FetchedAt: time.Now()},
	}
	if err := store.UpsertFeatures(ctx, rtParking, features); err != nil {
		t.Fatal(err)
	}

	info, err := store.Stats(ctx, rtParking)
	if err != nil {
		t.Fatal(err)
	}
	if info.FeatureCount != 1 {
		t.Errorf("expected 1 feature, got %d", info.FeatureCount)
	}
}

func TestStoreResourceTypes(t *testing.T) {
	ctx := context.Background()
	store := openTestStore(t)

	if err := store.UpsertFeatures(ctx, rtRoads, []Feature{{ID: "1", Tags: map[string]string{}, GeometryJSON: `{}`, FetchedAt: time.Now()}}); err != nil {
		t.Fatal(err)
	}
	if err := store.UpsertFeatures(ctx, rtParking, []Feature{{ID: "1", Tags: map[string]string{}, GeometryJSON: `{}`, FetchedAt: time.Now()}}); err != nil {
		t.Fatal(err)
	}

	types, err := store.ResourceTypes(ctx)
	if err != nil {
		t.Fatal(err)
	}
	if len(types) != 2 {
		t.Errorf("expected 2 types, got %d", len(types))
	}
}

func TestBoundaryRoundTrip(t *testing.T) {
	ctx := context.Background()
	store := openTestStore(t)

	// No boundary initially
	got, err := store.GetBoundary(ctx)
	if err != nil {
		t.Fatal(err)
	}
	if got != "" {
		t.Errorf("expected empty boundary, got %q", got)
	}

	// Save boundary
	gj := `{"type":"Polygon","coordinates":[[[-121.9,37.6],[-121.8,37.6],[-121.8,37.7],[-121.9,37.7],[-121.9,37.6]]]}`
	if err := store.SaveBoundary(ctx, gj, "nominatim"); err != nil {
		t.Fatal(err)
	}

	// Read back
	got, err = store.GetBoundary(ctx)
	if err != nil {
		t.Fatal(err)
	}
	if got != gj {
		t.Errorf("boundary mismatch:\n got: %s\nwant: %s", got, gj)
	}

	// Upsert replaces
	gj2 := `{"type":"Polygon","coordinates":[[[-122.0,37.5],[-121.7,37.5],[-121.7,37.8],[-122.0,37.8],[-122.0,37.5]]]}`
	if err := store.SaveBoundary(ctx, gj2, "nominatim"); err != nil {
		t.Fatal(err)
	}
	got, err = store.GetBoundary(ctx)
	if err != nil {
		t.Fatal(err)
	}
	if got != gj2 {
		t.Errorf("expected updated boundary")
	}
}

func TestCityIsolation(t *testing.T) {
	ctx := context.Background()
	root, err := Open(":memory:")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = root.Close() })

	id1, err := root.EnsureCity(ctx, "city-a", "City A")
	if err != nil {
		t.Fatal(err)
	}
	id2, err := root.EnsureCity(ctx, "city-b", "City B")
	if err != nil {
		t.Fatal(err)
	}

	storeA := root.ForCity(id1)
	storeB := root.ForCity(id2)

	// Insert features into city A
	if err := storeA.UpsertFeatures(ctx, rtRoads, []Feature{{ID: "1", Tags: map[string]string{}, GeometryJSON: `{}`, FetchedAt: time.Now()}}); err != nil {
		t.Fatal(err)
	}

	// City B should see nothing
	got, err := storeB.ListFeatures(ctx, rtRoads)
	if err != nil {
		t.Fatal(err)
	}
	if len(got) != 0 {
		t.Errorf("city B should have 0 features, got %d", len(got))
	}

	// City A should see its feature
	got, err = storeA.ListFeatures(ctx, rtRoads)
	if err != nil {
		t.Fatal(err)
	}
	if len(got) != 1 {
		t.Errorf("city A should have 1 feature, got %d", len(got))
	}
}

func TestEnsureCityIdempotent(t *testing.T) {
	ctx := context.Background()
	root, err := Open(":memory:")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = root.Close() })

	id1, err := root.EnsureCity(ctx, "livermore-ca", "Livermore, CA")
	if err != nil {
		t.Fatal(err)
	}
	id2, err := root.EnsureCity(ctx, "livermore-ca", "Livermore, CA")
	if err != nil {
		t.Fatal(err)
	}
	if id1 != id2 {
		t.Errorf("expected same id, got %d and %d", id1, id2)
	}
}

// TestWithTx pins the three contracts of the withTx helper used by every
// transactional Save method in store.go: success commits, fn-returned
// errors roll back and are returned unwrapped, and a begin-tx failure is
// wrapped with the "begin tx" prefix that callers rely on.
func TestWithTx(t *testing.T) {
	ctx := context.Background()

	t.Run("success commits", func(t *testing.T) {
		root, err := Open(":memory:")
		if err != nil {
			t.Fatal(err)
		}
		t.Cleanup(func() { _ = root.Close() })
		cityID, err := root.EnsureCity(ctx, "c", "C")
		if err != nil {
			t.Fatal(err)
		}
		s := root.ForCity(cityID).(*sqliteStore)

		err = s.withTx(ctx, func(tx *sql.Tx) error {
			_, err := tx.ExecContext(ctx, `INSERT INTO city_boundaries (city_id, geometry_json, source, fetched_at) VALUES (?, ?, ?, CURRENT_TIMESTAMP)`, cityID, `{"x":1}`, "test")
			return err
		})
		if err != nil {
			t.Fatalf("withTx success path: %v", err)
		}
		got, err := s.GetBoundary(ctx)
		if err != nil {
			t.Fatal(err)
		}
		if got != `{"x":1}` {
			t.Errorf("commit did not persist row: got %q", got)
		}
	})

	t.Run("fn error rolls back and returns unwrapped", func(t *testing.T) {
		root, err := Open(":memory:")
		if err != nil {
			t.Fatal(err)
		}
		t.Cleanup(func() { _ = root.Close() })
		cityID, err := root.EnsureCity(ctx, "c", "C")
		if err != nil {
			t.Fatal(err)
		}
		s := root.ForCity(cityID).(*sqliteStore)

		sentinel := errors.New("fn failed")
		err = s.withTx(ctx, func(tx *sql.Tx) error {
			if _, err := tx.ExecContext(ctx, `INSERT INTO city_boundaries (city_id, geometry_json, source, fetched_at) VALUES (?, ?, ?, CURRENT_TIMESTAMP)`, cityID, `{"x":1}`, "test"); err != nil {
				t.Fatal(err)
			}
			return sentinel
		})
		if !errors.Is(err, sentinel) {
			t.Errorf("withTx should return fn error unwrapped: got %v, want %v", err, sentinel)
		}
		got, err := s.GetBoundary(ctx)
		if err != nil {
			t.Fatal(err)
		}
		if got != "" {
			t.Errorf("rollback failed: row persisted as %q", got)
		}
	})

	t.Run("begin tx error wrapped", func(t *testing.T) {
		root, err := Open(":memory:")
		if err != nil {
			t.Fatal(err)
		}
		cityID, err := root.EnsureCity(ctx, "c", "C")
		if err != nil {
			t.Fatal(err)
		}
		s := root.ForCity(cityID).(*sqliteStore)
		if err := root.Close(); err != nil {
			t.Fatal(err)
		}

		err = s.withTx(ctx, func(tx *sql.Tx) error { return nil })
		if err == nil {
			t.Fatal("expected error after Close, got nil")
		}
		if !strings.HasPrefix(err.Error(), "begin tx:") {
			t.Errorf("expected 'begin tx:' prefix, got %q", err.Error())
		}
	})
}

func TestForeignKeyEnforcement(t *testing.T) {
	ctx := context.Background()
	root, err := Open(":memory:")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = root.Close() })

	// Use a city_id that doesn't exist in the cities table.
	bogus := root.ForCity(9999)
	err = bogus.UpsertFeatures(ctx, rtRoads, []Feature{
		{ID: "1", Tags: map[string]string{}, GeometryJSON: `{}`, FetchedAt: time.Now()},
	})
	if err == nil {
		t.Fatal("expected FK violation error when inserting feature with nonexistent city_id")
	}
}

func TestListCities(t *testing.T) {
	ctx := context.Background()
	root, err := Open(":memory:")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = root.Close() })

	// Migration seeds a "default" city, so start by counting baseline.
	baseline, err := root.ListCities(ctx)
	if err != nil {
		t.Fatal(err)
	}

	if _, err := root.EnsureCity(ctx, "a", "A"); err != nil {
		t.Fatal(err)
	}
	if _, err := root.EnsureCity(ctx, "b", "B"); err != nil {
		t.Fatal(err)
	}

	cities, err := root.ListCities(ctx)
	if err != nil {
		t.Fatal(err)
	}
	if len(cities) != len(baseline)+2 {
		t.Errorf("expected %d cities, got %d", len(baseline)+2, len(cities))
	}
}
