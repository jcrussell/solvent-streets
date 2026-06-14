package db

import (
	"context"
	"database/sql"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/jcrussell/solvent-streets/internal/config"
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
	id, err := root.EnsureCity(context.Background(), "test-city", "Test City", "test")
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

	if err := store.UpsertFeatures(ctx, rtRoads, features, nil); err != nil {
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
	if err := store.UpsertFeatures(ctx, rtRoads, features, nil); err != nil {
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

// TestUpsertFeatures_ScopedBySourcePreservesOtherSources locks in the
// partial-source data-loss fix: a re-ingest scoped to one source must replace
// only that source's rows and leave other sources' rows intact, while a nil
// scope replaces everything for the resource.
func TestUpsertFeatures_ScopedBySourcePreservesOtherSources(t *testing.T) {
	ctx := context.Background()
	store := openTestStore(t)

	// Initial full ingest: one row from each of two sources.
	initial := []Feature{
		{ID: "arc:1", ResourceType: rtRoads, Tags: map[string]string{}, GeometryJSON: `{}`, SourceAPI: "arcgis", FetchedAt: time.Now()},
		{ID: "osm:1", ResourceType: rtRoads, Tags: map[string]string{}, GeometryJSON: `{}`, SourceAPI: "overpass", FetchedAt: time.Now()},
	}
	if err := store.UpsertFeatures(ctx, rtRoads, initial, nil); err != nil {
		t.Fatal(err)
	}

	// Re-ingest where arcgis was down: only overpass succeeded, so scope the
	// replace to "overpass". The arcgis row must survive.
	overpassOnly := []Feature{
		{ID: "osm:2", ResourceType: rtRoads, Tags: map[string]string{}, GeometryJSON: `{}`, SourceAPI: "overpass", FetchedAt: time.Now()},
	}
	if err := store.UpsertFeatures(ctx, rtRoads, overpassOnly, []string{"overpass"}); err != nil {
		t.Fatal(err)
	}

	got, err := store.ListFeatures(ctx, rtRoads)
	if err != nil {
		t.Fatal(err)
	}
	ids := map[string]bool{}
	for _, f := range got {
		ids[f.ID] = true
	}
	if !ids["arc:1"] {
		t.Error("scoped re-ingest wiped the arcgis row that should have survived")
	}
	if ids["osm:1"] {
		t.Error("scoped re-ingest left the old overpass row; it should have been replaced")
	}
	if !ids["osm:2"] {
		t.Error("scoped re-ingest did not store the new overpass row")
	}

	// A nil-scope upsert replaces the whole resource set.
	if err := store.UpsertFeatures(ctx, rtRoads, overpassOnly, nil); err != nil {
		t.Fatal(err)
	}
	got, err = store.ListFeatures(ctx, rtRoads)
	if err != nil {
		t.Fatal(err)
	}
	if len(got) != 1 || got[0].ID != "osm:2" {
		t.Errorf("nil-scope upsert should replace everything; got %+v", got)
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
			ResourceType: rtRoads, TotalArea: float64(id * 100), FeatureCount: int(id), SnapshotID: &id,
		}); err != nil {
			t.Fatal(err)
		}
		if err := store.SaveHexStats(ctx, []HexStat{
			{HexID: "h1", ResourceType: rtRoads, Area: float64(id * 10), PctCovered: 0.5, SnapshotID: &id},
		}); err != nil {
			t.Fatal(err)
		}
		if err := store.SaveCohortStats(ctx, []CohortStat{
			{ResourceType: rtRoads, Classification: "primary", Area: float64(id * 1000), FeatureCount: 1, SnapshotID: &id},
		}); err != nil {
			t.Fatal(err)
		}
		if err := store.SaveForecastResults(ctx, []ForecastResult{
			{ResourceType: rtRoads, Year: 2026, PCI: float64(id * 10), Area: 100, TreatmentCost: 200, TreatmentTier: "preventive", SnapshotID: &id},
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
	if latest.TotalArea != float64(snap2.ID*100) {
		t.Errorf("unpinned latest: expected snap2 area, got %v", latest.TotalArea)
	}
	hexAll, _ := store.ListHexStats(ctx, rtRoads)
	if len(hexAll) != 1 || hexAll[0].Area != float64(snap2.ID*10) {
		t.Errorf("unpinned hex_stats: want only snap2's row (area %v), got %+v", snap2.ID*10, hexAll)
	}
	cohortAll, _ := store.ListCohortStats(ctx, rtRoads)
	if len(cohortAll) != 1 || cohortAll[0].Area != float64(snap2.ID*1000) {
		t.Errorf("unpinned cohort_stats: want only snap2's row (area %v), got %+v", snap2.ID*1000, cohortAll)
	}

	// Pinned to snap1.
	pinned1 := store.WithSnapshot(snap1.ID)
	cr1, err := pinned1.LatestComputeResult(ctx, rtRoads)
	if err != nil {
		t.Fatal(err)
	}
	if cr1.TotalArea != float64(snap1.ID*100) {
		t.Errorf("pinned snap1: expected %v area, got %v", snap1.ID*100, cr1.TotalArea)
	}
	hex1, _ := pinned1.ListHexStats(ctx, rtRoads)
	if len(hex1) != 1 || hex1[0].Area != float64(snap1.ID*10) {
		t.Errorf("pinned snap1 hex: want 1 row with area %v, got %+v", snap1.ID*10, hex1)
	}
	cohort1, _ := pinned1.ListCohortStats(ctx, rtRoads)
	if len(cohort1) != 1 || cohort1[0].Area != float64(snap1.ID*1000) {
		t.Errorf("pinned snap1 cohort: want 1 row with area %v, got %+v", snap1.ID*1000, cohort1)
	}

	// Pinned to snap2 sees only snap2's row.
	pinned2 := store.WithSnapshot(snap2.ID)
	cr2, err := pinned2.LatestComputeResult(ctx, rtRoads)
	if err != nil {
		t.Fatal(err)
	}
	if cr2.TotalArea != float64(snap2.ID*100) {
		t.Errorf("pinned snap2: expected %v area, got %v", snap2.ID*100, cr2.TotalArea)
	}
}

// TestSnapshotBatchQuery_PinnedAndConfigHashArms locks the batch read
// path (LatestComputeResults / ListCohortStatsForTypes), which hand-builds
// an IN(...) placeholder list plus positional binds in snapshotBatchQuery.
// The single-row variants (LatestComputeResult / ListCohortStats) are well
// covered, but the batch arms run against real SQLite only here — handler
// tests exercise them solely through dbtest.MockStore, bypassing the SQL.
// We seed two resource types across two snapshots, then assert the batch
// result equals the single-row result per type for (a) a pinned snapshot
// id and (b) a config-hash pin. A bind-ordering or placeholder mistake in
// the batch SQL's arm-1 (pinned) or arm-2 (config hash) fails here.
func TestSnapshotBatchQuery_PinnedAndConfigHashArms(t *testing.T) {
	ctx := context.Background()
	store := openTestStore(t)

	types := []resource.Type{rtRoads, rtParking}

	snap1, err := store.CreateSnapshot(ctx, "hash-1")
	if err != nil {
		t.Fatal(err)
	}
	snap2, err := store.CreateSnapshot(ctx, "hash-2")
	if err != nil {
		t.Fatal(err)
	}

	// Seed both resource types under both snapshots so the batch query's
	// per-type partitioning and IN(...) list are actually exercised.
	for _, snap := range []*Snapshot{snap1, snap2} {
		id := snap.ID
		for ti, rt := range types {
			if err := store.SaveComputeResult(ctx, ComputeResult{
				ResourceType: rt, TotalArea: float64(id*100 + int64(ti)), FeatureCount: int(id), SnapshotID: &id,
			}); err != nil {
				t.Fatal(err)
			}
			if err := store.SaveCohortStats(ctx, []CohortStat{
				{ResourceType: rt, Classification: "primary", Area: float64(id*1000 + int64(ti)), FeatureCount: 1, SnapshotID: &id},
				{ResourceType: rt, Classification: "residential", Area: float64(id*2000 + int64(ti)), FeatureCount: 2, SnapshotID: &id},
			}); err != nil {
				t.Fatal(err)
			}
		}
	}

	// assertBatchMatchesSingleRow checks that, for the given scoped store,
	// LatestComputeResults/ListCohortStatsForTypes return exactly what the
	// per-type single-row methods return for every seeded type.
	assertBatchMatchesSingleRow := func(t *testing.T, scoped Store, label string) {
		t.Helper()

		batchCR, err := scoped.LatestComputeResults(ctx, types)
		if err != nil {
			t.Fatalf("%s: LatestComputeResults: %v", label, err)
		}
		batchCohort, err := scoped.ListCohortStatsForTypes(ctx, types)
		if err != nil {
			t.Fatalf("%s: ListCohortStatsForTypes: %v", label, err)
		}

		for _, rt := range types {
			singleCR, err := scoped.LatestComputeResult(ctx, rt)
			if err != nil {
				t.Fatalf("%s: LatestComputeResult(%s): %v", label, rt, err)
			}
			bCR := batchCR[rt]
			if bCR == nil {
				t.Fatalf("%s: batch LatestComputeResults missing type %s", label, rt)
			}
			if bCR.TotalArea != singleCR.TotalArea || bCR.SnapshotID == nil || singleCR.SnapshotID == nil || *bCR.SnapshotID != *singleCR.SnapshotID {
				t.Errorf("%s: batch compute result for %s = %+v; single-row = %+v", label, rt, bCR, singleCR)
			}

			singleCohort, err := scoped.ListCohortStats(ctx, rt)
			if err != nil {
				t.Fatalf("%s: ListCohortStats(%s): %v", label, rt, err)
			}
			bCohort := batchCohort[rt]
			if len(bCohort) != len(singleCohort) {
				t.Fatalf("%s: batch cohort rows for %s = %d; single-row = %d", label, rt, len(bCohort), len(singleCohort))
			}
			// Compare as multisets keyed by classification+area; row order
			// is not contractual.
			want := map[string]float64{}
			for _, c := range singleCohort {
				want[c.Classification] = c.Area
			}
			for _, c := range bCohort {
				if want[c.Classification] != c.Area {
					t.Errorf("%s: batch cohort for %s/%s area = %v; single-row = %v", label, rt, c.Classification, c.Area, want[c.Classification])
				}
			}
		}
	}

	// Arm 1: pinned snapshot id (the ?snapshot=N time-travel path).
	assertBatchMatchesSingleRow(t, store.WithSnapshot(snap1.ID), "pinned-snap1")
	assertBatchMatchesSingleRow(t, store.WithSnapshot(snap2.ID), "pinned-snap2")

	// Arm 2: config-hash pin.
	assertBatchMatchesSingleRow(t, store.WithConfigHash("hash-1"), "config-hash-1")
	assertBatchMatchesSingleRow(t, store.WithConfigHash("hash-2"), "config-hash-2")

	// Sanity: the pinned arm actually selects the pinned snapshot's data,
	// not just whatever the single-row method also returns. snap1's roads
	// area is snap1.ID*100; confirm the batch read reflects that.
	pinned := store.WithSnapshot(snap1.ID)
	cr, err := pinned.LatestComputeResults(ctx, types)
	if err != nil {
		t.Fatal(err)
	}
	if got := cr[rtRoads]; got == nil || got.TotalArea != float64(snap1.ID*100) {
		t.Errorf("pinned-snap1 batch roads compute result = %+v; want area %v", got, snap1.ID*100)
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
			{HexID: "h1", ResourceType: rtRoads, Area: float64(id), PctCovered: 50, SnapshotID: &id},
			{HexID: "h2", ResourceType: rtRoads, Area: float64(id), PctCovered: 50, SnapshotID: &id},
		}); err != nil {
			t.Fatal(err)
		}
		if err := store.SaveCohortStats(ctx, []CohortStat{
			{ResourceType: rtRoads, Classification: "primary", Area: float64(id), FeatureCount: 1, SnapshotID: &id},
		}); err != nil {
			t.Fatal(err)
		}
		if err := store.SaveForecastResults(ctx, []ForecastResult{
			{ResourceType: rtRoads, Year: 2026, PCI: float64(id), Area: 1, TreatmentCost: 1, TreatmentTier: "preventive", SnapshotID: &id},
		}); err != nil {
			t.Fatal(err)
		}
	}

	hex, _ := store.ListHexStats(ctx, rtRoads)
	if len(hex) != 2 {
		t.Errorf("unpinned ListHexStats: got %d rows, want 2 (latest snapshot only)", len(hex))
	}
	for _, h := range hex {
		if h.Area != float64(snapNew.ID) {
			t.Errorf("hex row from wrong snapshot: Area=%v, want %v", h.Area, snapNew.ID)
		}
	}

	co, _ := store.ListCohortStats(ctx, rtRoads)
	if len(co) != 1 || co[0].Area != float64(snapNew.ID) {
		t.Errorf("unpinned ListCohortStats: want 1 row from snapNew (area %v), got %+v", snapNew.ID, co)
	}

	// Pinning to the older snapshot still returns its rows verbatim.
	old := store.WithSnapshot(snapOld.ID)
	if hex, _ := old.ListHexStats(ctx, rtRoads); len(hex) != 2 || hex[0].Area != float64(snapOld.ID) {
		t.Errorf("pinned snapOld ListHexStats: want 2 rows with area %v, got %+v", snapOld.ID, hex)
	}
}

// TestListReads_NullLegacyFallback covers the NULL-snapshot_id legacy
// path: when a city has only rows whose snapshot_id IS NULL (no matching
// snapshot exists), unpinned reads still surface them. SQLite's `IS` operator makes
// `snapshot_id IS (SELECT MAX(...))` evaluate to true for NULL rows
// when MAX returns NULL (no snapshot-tagged rows exist).
func TestListReads_NullLegacyFallback(t *testing.T) {
	ctx := context.Background()
	store := openTestStore(t)

	// Write rows directly with NULL snapshot_id — simulating legacy
	// data inserted before snapshot tagging existed.
	if err := store.SaveHexStats(ctx, []HexStat{
		{HexID: "legacy", ResourceType: rtRoads, Area: 42, PctCovered: 100, SnapshotID: nil},
	}); err != nil {
		t.Fatal(err)
	}
	if err := store.SaveCohortStats(ctx, []CohortStat{
		{ResourceType: rtRoads, Classification: "primary", Area: 7, FeatureCount: 1, SnapshotID: nil},
	}); err != nil {
		t.Fatal(err)
	}
	if err := store.SaveForecastResults(ctx, []ForecastResult{
		{ResourceType: rtRoads, Year: 2026, PCI: 99, Area: 1, TreatmentCost: 1, TreatmentTier: "preventive", SnapshotID: nil},
	}); err != nil {
		t.Fatal(err)
	}

	if hex, _ := store.ListHexStats(ctx, rtRoads); len(hex) != 1 || hex[0].Area != 42 {
		t.Errorf("legacy ListHexStats: want 1 NULL-snapshot row (area 42), got %+v", hex)
	}
	if co, _ := store.ListCohortStats(ctx, rtRoads); len(co) != 1 || co[0].Area != 7 {
		t.Errorf("legacy ListCohortStats: want 1 NULL-snapshot row (area 7), got %+v", co)
	}
}

// TestListReads_ConfigHashScoping pins the new third axis: when the
// store is unpinned but tagged with WithConfigHash(H), reads return
// only rows whose snapshot's config_hash matches H. Two configs
// writing to the same (city, resource_type) must coexist — the bug
// this guards against is the slug-sharing case in examples/ (Livermore
// in single-city livermore-ca vs Livermore in the bay-area-ca metro at
// a different hex_edge_m, sharing city_id, where unpinned reads were
// returning whichever snapshot wrote last and producing incompatible
// hex_id namespaces).
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
			{HexID: "h1", ResourceType: rtRoads, Area: float64(id * 10), PctCovered: 50, SnapshotID: &id},
		}); err != nil {
			t.Fatal(err)
		}
		if err := store.SaveCohortStats(ctx, []CohortStat{
			{ResourceType: rtRoads, Classification: "primary", Area: float64(id * 100), FeatureCount: 1, SnapshotID: &id},
		}); err != nil {
			t.Fatal(err)
		}
		if err := store.SaveForecastResults(ctx, []ForecastResult{
			{ResourceType: rtRoads, Year: 2026, PCI: float64(id * 5), Area: 1, TreatmentCost: 1, TreatmentTier: "preventive", SnapshotID: &id},
		}); err != nil {
			t.Fatal(err)
		}
		if err := store.SaveComputeResult(ctx, ComputeResult{
			ResourceType: rtRoads, TotalArea: float64(id), FeatureCount: int(id), SnapshotID: &id,
		}); err != nil {
			t.Fatal(err)
		}
	}

	// Unpinned + no config hash: arm 3 — latest overall (snapH2's row).
	if hex, _ := store.ListHexStats(ctx, rtRoads); len(hex) != 1 || hex[0].Area != float64(snapH2.ID*10) {
		t.Errorf("unpinned-no-hash ListHexStats: want snapH2's row (area %v), got %+v", snapH2.ID*10, hex)
	}

	// Unpinned + WithConfigHash(hash-1): arm 2 — only snapH1's row.
	h1 := store.WithConfigHash("hash-1")
	if hex, _ := h1.ListHexStats(ctx, rtRoads); len(hex) != 1 || hex[0].Area != float64(snapH1.ID*10) {
		t.Errorf("WithConfigHash(hash-1) ListHexStats: want snapH1's row (area %v), got %+v", snapH1.ID*10, hex)
	}
	if co, _ := h1.ListCohortStats(ctx, rtRoads); len(co) != 1 || co[0].Area != float64(snapH1.ID*100) {
		t.Errorf("WithConfigHash(hash-1) ListCohortStats: want snapH1's row (area %v), got %+v", snapH1.ID*100, co)
	}
	if cr, err := h1.LatestComputeResult(ctx, rtRoads); err != nil || cr.TotalArea != float64(snapH1.ID) {
		t.Errorf("WithConfigHash(hash-1) LatestComputeResult: want snapH1's row (area %v), got %+v err=%v", snapH1.ID, cr, err)
	}

	// Unpinned + WithConfigHash(hash-2): arm 2 — only snapH2's row.
	h2 := store.WithConfigHash("hash-2")
	if hex, _ := h2.ListHexStats(ctx, rtRoads); len(hex) != 1 || hex[0].Area != float64(snapH2.ID*10) {
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
	if hex, _ := mixed.ListHexStats(ctx, rtRoads); len(hex) != 1 || hex[0].Area != float64(snapH1.ID*10) {
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

	idA, _ := root.EnsureCity(ctx, "a", "A", "test")
	idB, _ := root.EnsureCity(ctx, "b", "B", "test")
	storeA := root.ForCity(idA)
	storeB := root.ForCity(idB)

	snap, err := storeA.CreateSnapshot(ctx, "h")
	if err != nil {
		t.Fatal(err)
	}
	sid := snap.ID

	if err := storeA.SaveComputeResult(ctx, ComputeResult{ResourceType: rtRoads, TotalArea: 1, SnapshotID: &sid}); err != nil {
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
	var fcCount int
	if err := root.db.QueryRowContext(ctx, `SELECT COUNT(*) FROM forecast_results WHERE snapshot_id = ?`, sid).Scan(&fcCount); err != nil {
		t.Fatal(err)
	}
	if fcCount != 0 {
		t.Errorf("forecast_results after delete: got %d, want 0", fcCount)
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

	idA, _ := root.EnsureCity(ctx, "a", "A", "test")
	idB, _ := root.EnsureCity(ctx, "b", "B", "test")
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
		TotalArea:    92903,
		FeatureCount: 500,
	}
	if err := store.SaveComputeResult(ctx, result); err != nil {
		t.Fatal(err)
	}

	got, err := store.LatestComputeResult(ctx, rtRoads)
	if err != nil {
		t.Fatal(err)
	}
	if got.TotalArea != 92903 {
		t.Errorf("expected area 92903, got %f", got.TotalArea)
	}
	if got.FeatureCount != 500 {
		t.Errorf("expected 500 features, got %d", got.FeatureCount)
	}
}

// TestSaveForecastResults_ReplacesOnRerun pins the fix for accumulating
// forecast rows: re-running with the same (resource_type, snapshot_id) must
// replace the prior rows, not append duplicates. Covers both the snapshot-
// tagged path and the legacy NULL-snapshot path.
func TestSaveForecastResults_ReplacesOnRerun(t *testing.T) {
	ctx := context.Background()
	store := openTestStore(t)

	snap, err := store.CreateSnapshot(ctx, "hash-1")
	if err != nil {
		t.Fatal(err)
	}

	rows := func(snapshotID *int64, pci float64) []ForecastResult {
		return []ForecastResult{
			{ResourceType: rtRoads, Year: 1, PCI: pci, Area: 100, TreatmentCost: 10, SnapshotID: snapshotID},
			{ResourceType: rtRoads, Year: 2, PCI: pci, Area: 100, TreatmentCost: 10, SnapshotID: snapshotID},
		}
	}

	countRows := func() int {
		t.Helper()
		var n int
		if err := store.(*sqliteStore).db.QueryRowContext(ctx,
			`SELECT COUNT(*) FROM forecast_results WHERE resource_type = ?`, rtRoads).Scan(&n); err != nil {
			t.Fatal(err)
		}
		return n
	}

	// Tagged: two re-runs for the same snapshot must leave only the second run.
	if err := store.SaveForecastResults(ctx, rows(&snap.ID, 80)); err != nil {
		t.Fatal(err)
	}
	if err := store.SaveForecastResults(ctx, rows(&snap.ID, 70)); err != nil {
		t.Fatal(err)
	}
	if got := countRows(); got != 2 {
		t.Fatalf("expected 2 forecast rows after re-run, got %d (accumulation regression)", got)
	}
	var stalePCI int
	if err := store.(*sqliteStore).db.QueryRowContext(ctx,
		`SELECT COUNT(*) FROM forecast_results WHERE resource_type = ? AND pci != 70`, rtRoads).Scan(&stalePCI); err != nil {
		t.Fatal(err)
	}
	if stalePCI != 0 {
		t.Errorf("expected all rows replaced with PCI 70, found %d rows with stale PCI", stalePCI)
	}

	// Legacy NULL-snapshot rows must also replace (= ? never matches NULL).
	if err := store.SaveForecastResults(ctx, rows(nil, 60)); err != nil {
		t.Fatal(err)
	}
	if err := store.SaveForecastResults(ctx, rows(nil, 50)); err != nil {
		t.Fatal(err)
	}
	var nullCount int
	if err := store.(*sqliteStore).db.QueryRowContext(ctx,
		`SELECT COUNT(*) FROM forecast_results WHERE resource_type = ? AND snapshot_id IS NULL`, rtRoads).Scan(&nullCount); err != nil {
		t.Fatal(err)
	}
	if nullCount != 2 {
		t.Fatalf("expected 2 NULL-snapshot rows after re-run, got %d", nullCount)
	}
	// The earlier tagged rows must survive the NULL-targeted replace.
	var taggedCount int
	if err := store.(*sqliteStore).db.QueryRowContext(ctx,
		`SELECT COUNT(*) FROM forecast_results WHERE resource_type = ? AND snapshot_id = ?`, rtRoads, snap.ID).Scan(&taggedCount); err != nil {
		t.Fatal(err)
	}
	if taggedCount != 2 {
		t.Fatalf("NULL-snapshot replace clobbered tagged rows: got %d tagged, want 2", taggedCount)
	}
}

func TestStoreStats(t *testing.T) {
	ctx := context.Background()
	store := openTestStore(t)

	features := []Feature{
		{ID: "1", Name: "test", Tags: map[string]string{}, GeometryJSON: `{}`, FetchedAt: time.Now()},
	}
	if err := store.UpsertFeatures(ctx, rtParking, features, nil); err != nil {
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

// TestStoreStatsPropagatesComputeError ensures a genuine DB failure from the
// compute-result lookup propagates rather than being folded into the silent
// "never computed" branch (which previously masked it as TotalArea=0).
func TestStoreStatsPropagatesComputeError(t *testing.T) {
	ctx := context.Background()
	store := openTestStore(t)

	// Feature queries must succeed so Stats reaches the compute-result lookup.
	if err := store.UpsertFeatures(ctx, rtParking, []Feature{
		{ID: "1", Tags: map[string]string{}, GeometryJSON: `{}`, FetchedAt: time.Now()},
	}, nil); err != nil {
		t.Fatal(err)
	}

	// Break only the compute_results table so LatestComputeResult errors with
	// "no such table" while the feature count/fetched_at queries still work.
	if _, err := store.(*sqliteStore).db.ExecContext(ctx, `DROP TABLE compute_results`); err != nil {
		t.Fatal(err)
	}

	if _, err := store.Stats(ctx, rtParking); err == nil {
		t.Fatal("expected Stats to propagate the compute-result query error, got nil")
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

	id1, err := root.EnsureCity(ctx, "city-a", "City A", "test")
	if err != nil {
		t.Fatal(err)
	}
	id2, err := root.EnsureCity(ctx, "city-b", "City B", "test")
	if err != nil {
		t.Fatal(err)
	}

	storeA := root.ForCity(id1)
	storeB := root.ForCity(id2)

	// Insert features into city A
	if err := storeA.UpsertFeatures(ctx, rtRoads, []Feature{{ID: "1", Tags: map[string]string{}, GeometryJSON: `{}`, FetchedAt: time.Now()}}, nil); err != nil {
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

	id1, err := root.EnsureCity(ctx, "livermore-ca", "Livermore, CA", "test")
	if err != nil {
		t.Fatal(err)
	}
	id2, err := root.EnsureCity(ctx, "livermore-ca", "Livermore, CA", "test")
	if err != nil {
		t.Fatal(err)
	}
	if id1 != id2 {
		t.Errorf("expected same id, got %d and %d", id1, id2)
	}
}

// TestEnsureCityRefreshesName pins the solvent-streets-a2z8.3 fix: when a
// display name changes but slugifies to the same value (e.g. a punctuation or
// capitalization edit), re-running EnsureCity must refresh the stored name in
// place rather than keeping the stale one, while preserving the row id.
func TestEnsureCityRefreshesName(t *testing.T) {
	ctx := context.Background()
	root, err := Open(":memory:")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = root.Close() })

	id1, err := root.EnsureCity(ctx, "st-louis", "St. Louis", "test")
	if err != nil {
		t.Fatal(err)
	}
	id2, err := root.EnsureCity(ctx, "st-louis", "St Louis", "test")
	if err != nil {
		t.Fatal(err)
	}
	if id1 != id2 {
		t.Fatalf("expected same id after name refresh, got %d and %d", id1, id2)
	}

	cities, err := root.ListCities(ctx)
	if err != nil {
		t.Fatal(err)
	}
	if len(cities) != 1 {
		t.Fatalf("expected 1 city, got %d", len(cities))
	}
	if cities[0].Name != "St Louis" {
		t.Errorf("expected refreshed name %q, got %q", "St Louis", cities[0].Name)
	}
}

// TestEnsureCityDistinctByConfigID pins the solvent-streets-zqul fix:
// two configs that share a city slug (e.g. both define "Austin, TX")
// must resolve to distinct city ids when they have distinct config_ids,
// so re-ingest under one config does not clobber features written under
// the other.
func TestEnsureCityDistinctByConfigID(t *testing.T) {
	ctx := context.Background()
	root, err := Open(":memory:")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = root.Close() })

	idA, err := root.EnsureCity(ctx, "austin-tx", "Austin, TX", "cfg-a")
	if err != nil {
		t.Fatal(err)
	}
	idB, err := root.EnsureCity(ctx, "austin-tx", "Austin, TX", "cfg-b")
	if err != nil {
		t.Fatal(err)
	}
	if idA == idB {
		t.Errorf("expected distinct ids for same slug under different config_id; got %d for both", idA)
	}

	// Same (slug, config_id) is still idempotent.
	idA2, err := root.EnsureCity(ctx, "austin-tx", "Austin, TX", "cfg-a")
	if err != nil {
		t.Fatal(err)
	}
	if idA != idA2 {
		t.Errorf("expected idempotent id for same (slug, config_id); got %d then %d", idA, idA2)
	}
}

// TestEnsureCityRejectsEmptyConfigID guards against a programmatic caller
// that bypasses config.Load (which auto-populates ConfigID). The empty
// string was the legacy "no config" sentinel; it is now a load-bearing
// error so we surface the bug at the call site instead of producing a
// row that collides with any other empty-config_id row.
func TestEnsureCityRejectsEmptyConfigID(t *testing.T) {
	ctx := context.Background()
	root, err := Open(":memory:")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = root.Close() })

	_, err = root.EnsureCity(ctx, "austin-tx", "Austin, TX", "")
	if err == nil {
		t.Fatal("expected error for empty config_id, got nil")
	}
	if !strings.Contains(err.Error(), "config_id is required") {
		t.Errorf("error %q does not mention config_id requirement", err)
	}
}

// TestEnsureCityCollapsesPathSpellings is the end-to-end intent: load
// the same pvmt.toml from two cwd / path-spelling combinations (the
// gensite-vs-pvmt-all scenario from solvent-streets-kevc) and confirm
// both reach the same cities row.
func TestEnsureCityCollapsesPathSpellings(t *testing.T) {
	ctx := context.Background()
	root, err := Open(":memory:")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = root.Close() })

	dir := t.TempDir()
	path := filepath.Join(dir, "pvmt.toml")
	if err := os.WriteFile(path, []byte(`[[cities]]
name = "Pathville"
`), 0o644); err != nil {
		t.Fatal(err)
	}

	cfgAbs, err := config.Load(path)
	if err != nil {
		t.Fatal(err)
	}
	t.Chdir(dir)
	cfgRel, err := config.Load("pvmt.toml")
	if err != nil {
		t.Fatal(err)
	}

	if cfgAbs.ConfigID != cfgRel.ConfigID {
		t.Fatalf("ConfigID mismatch: abs=%q rel=%q", cfgAbs.ConfigID, cfgRel.ConfigID)
	}

	idAbs, err := root.EnsureCity(ctx, "pathville", "Pathville", cfgAbs.ConfigID)
	if err != nil {
		t.Fatal(err)
	}
	idRel, err := root.EnsureCity(ctx, "pathville", "Pathville", cfgRel.ConfigID)
	if err != nil {
		t.Fatal(err)
	}
	if idAbs != idRel {
		t.Errorf("expected same city_id for abs and relative path spellings; got %d and %d", idAbs, idRel)
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
		cityID, err := root.EnsureCity(ctx, "c", "C", "test")
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
		cityID, err := root.EnsureCity(ctx, "c", "C", "test")
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
		cityID, err := root.EnsureCity(ctx, "c", "C", "test")
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
	}, nil)
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

	if _, err := root.EnsureCity(ctx, "a", "A", "test"); err != nil {
		t.Fatal(err)
	}
	if _, err := root.EnsureCity(ctx, "b", "B", "test"); err != nil {
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
