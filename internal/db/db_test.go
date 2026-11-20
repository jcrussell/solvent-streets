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
	root, err := Open(context.Background(), ":memory:")
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
		{ID: "arc:1", ResourceType: rtRoads, Tags: map[string]string{}, GeometryJSON: testPoint, SourceAPI: "arcgis", FetchedAt: time.Now()},
		{ID: "osm:1", ResourceType: rtRoads, Tags: map[string]string{}, GeometryJSON: testPoint, SourceAPI: "overpass", FetchedAt: time.Now()},
	}
	if err := store.UpsertFeatures(ctx, rtRoads, initial, nil); err != nil {
		t.Fatal(err)
	}

	// Re-ingest where arcgis was down: only overpass succeeded, so scope the
	// replace to "overpass". The arcgis row must survive.
	overpassOnly := []Feature{
		{ID: "osm:2", ResourceType: rtRoads, Tags: map[string]string{}, GeometryJSON: testPoint, SourceAPI: "overpass", FetchedAt: time.Now()},
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

// TestCityStoreClose_KeepsPoolOpen pins the yvlv.15 fix: city-scoped stores
// returned by ForCity share the RootStore's single *sql.DB pool, so
// sqliteStore.Close must be a no-op. Closing one city store must leave the
// other city store and the RootStore fully usable.
func TestCityStoreClose_KeepsPoolOpen(t *testing.T) {
	ctx := context.Background()
	root, err := Open(context.Background(), ":memory:")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = root.Close() })

	idA, err := root.EnsureCity(ctx, "a", "A", "test")
	if err != nil {
		t.Fatal(err)
	}
	idB, err := root.EnsureCity(ctx, "b", "B", "test")
	if err != nil {
		t.Fatal(err)
	}
	storeA := root.ForCity(idA)
	storeB := root.ForCity(idB)

	// Closing one city store must not tear down the shared pool.
	if err := storeA.Close(); err != nil {
		t.Fatalf("city store Close: %v", err)
	}

	// The sibling city store still works.
	if _, err := storeB.CreateRun(ctx, "roads", "after-sibling-close", nil); err != nil {
		t.Errorf("sibling store after other city Close: %v", err)
	}
	// The RootStore still works.
	if _, err := root.EnsureCity(ctx, "c", "C", "test"); err != nil {
		t.Errorf("RootStore.EnsureCity after city Close: %v", err)
	}
	// Even the closed city store still works, since Close is a no-op and the
	// pool is owned by RootStore.
	if _, err := storeA.CreateRun(ctx, "roads", "reused-after-close", nil); err != nil {
		t.Errorf("closed city store still shares live pool: %v", err)
	}
}

// testPoint is a geometry for tests that do not look at it.
const testPoint = `{"type":"Point","coordinates":[-122.2711,37.8044]}`

// seedGCFeature inserts a feature with an explicit source_api for the gc
// tests, bypassing UpsertFeatures (which would scope deletes by source).
func seedGCFeature(t *testing.T, root *RootStore, cityID int64, id, sourceAPI string) {
	t.Helper()
	geometry, err := encodeGeometry(testPoint)
	if err != nil {
		t.Fatal(err)
	}
	_, err = root.db.ExecContext(context.Background(),
		`INSERT INTO features (id, resource_type, city_id, name, tags, geometry, source_api, fetched_at)
		 VALUES (?, ?, ?, '', '{}', ?, ?, unixepoch())`,
		id, rtRoads, cityID, geometry, sourceAPI)
	if err != nil {
		t.Fatalf("seed feature %s: %v", id, err)
	}
}

func countRows(t *testing.T, root *RootStore, query string, args ...any) int {
	t.Helper()
	var n int
	if err := root.db.QueryRowContext(context.Background(), query, args...).Scan(&n); err != nil {
		t.Fatalf("count: %v", err)
	}
	return n
}

// TestGC_EmptyKeepSourcesSkipsFeatureSweep pins the data-loss guard: with
// no resolvable sources, gc must NOT count or delete any feature rows
// (otherwise every feature would look stale).
func TestGC_EmptyKeepSourcesSkipsFeatureSweep(t *testing.T) {
	ctx := context.Background()
	root, err := Open(context.Background(), ":memory:")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = root.Close() })

	idA, _ := root.EnsureCity(ctx, "a", "A", "test")
	storeA := root.ForCity(idA)

	seedGCFeature(t, root, idA, "f1", "overpass")
	seedGCFeature(t, root, idA, "f2", "oldsource")

	report, err := storeA.GCScan(ctx, nil, testEpoch)
	if err != nil {
		t.Fatalf("GCScan: %v", err)
	}
	if report.StaleFeatures != 0 {
		t.Errorf("empty keepSources scan StaleFeatures = %d, want 0", report.StaleFeatures)
	}

	swept, err := storeA.GCSweep(ctx, nil, testEpoch)
	if err != nil {
		t.Fatalf("GCSweep: %v", err)
	}
	if swept.StaleFeatures != 0 {
		t.Errorf("empty keepSources sweep StaleFeatures = %d, want 0", swept.StaleFeatures)
	}
	if n := countRows(t, root, `SELECT COUNT(*) FROM features WHERE city_id = ?`, idA); n != 2 {
		t.Errorf("empty keepSources deleted features (DATA LOSS): %d remain, want 2", n)
	}
}

func TestStoreStats(t *testing.T) {
	ctx := context.Background()
	store := openTestStore(t)

	features := []Feature{
		{ID: "1", Name: "test", Tags: map[string]string{}, GeometryJSON: testPoint, FetchedAt: time.Now()},
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
	root, err := Open(context.Background(), ":memory:")
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
	if err := storeA.UpsertFeatures(ctx, rtRoads, []Feature{{ID: "1", Tags: map[string]string{}, GeometryJSON: testPoint, FetchedAt: time.Now()}}, nil); err != nil {
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
	root, err := Open(context.Background(), ":memory:")
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

// TestEnsureCityRefreshesName: when a
// display name changes but slugifies to the same value (e.g. a punctuation or
// capitalization edit), re-running EnsureCity must refresh the stored name in
// place rather than keeping the stale one, while preserving the row id.
func TestEnsureCityRefreshesName(t *testing.T) {
	ctx := context.Background()
	root, err := Open(context.Background(), ":memory:")
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

// TestEnsureCityDistinctByConfigID:
// two configs that share a city slug (e.g. both define "Austin, TX")
// must resolve to distinct city ids when they have distinct config_ids,
// so re-ingest under one config does not clobber features written under
// the other.
func TestEnsureCityDistinctByConfigID(t *testing.T) {
	ctx := context.Background()
	root, err := Open(context.Background(), ":memory:")
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
	root, err := Open(context.Background(), ":memory:")
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
// the same pvmt.toml from two cwd / path-spelling combinations (as
// `make site` and `pvmt all` do) and confirm
// both reach the same cities row.
func TestEnsureCityCollapsesPathSpellings(t *testing.T) {
	ctx := context.Background()
	root, err := Open(context.Background(), ":memory:")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = root.Close() })

	dir := t.TempDir()
	path := filepath.Join(dir, "pvmt.toml")
	if err := os.WriteFile(path, []byte(`[[cities]]
name = "Pathville"
overpass = true
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
		root, err := Open(context.Background(), ":memory:")
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
			_, err := tx.ExecContext(ctx, `INSERT INTO city_boundaries (city_id, geometry_json, source, fetched_at) VALUES (?, ?, ?, unixepoch())`, cityID, `{"x":1}`, "test")
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
		root, err := Open(context.Background(), ":memory:")
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
			if _, err := tx.ExecContext(ctx, `INSERT INTO city_boundaries (city_id, geometry_json, source, fetched_at) VALUES (?, ?, ?, unixepoch())`, cityID, `{"x":1}`, "test"); err != nil {
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
		root, err := Open(context.Background(), ":memory:")
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
	root, err := Open(context.Background(), ":memory:")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = root.Close() })

	// Use a city_id that doesn't exist in the cities table.
	bogus := root.ForCity(9999)
	err = bogus.UpsertFeatures(ctx, rtRoads, []Feature{
		{ID: "1", Tags: map[string]string{}, GeometryJSON: testPoint, FetchedAt: time.Now()},
	}, nil)
	if err == nil {
		t.Fatal("expected FK violation error when inserting feature with nonexistent city_id")
	}
}

// TestOpenDSN pins the itpe fix: the sqlite DSN is built by appending a
// pragma query string to the path, so a path already containing '?' would
// corrupt that query string and silently drop the WAL/busy_timeout/
// foreign_keys pragmas. Open must reject such a path, and for a clean path
// the pragmas must actually take effect.
func TestOpenDSN(t *testing.T) {
	ctx := context.Background()

	t.Run("rejects path with query separator", func(t *testing.T) {
		bad := filepath.Join(t.TempDir(), "pvmt.db") + "?_pragma=foreign_keys(off)"
		if _, err := Open(context.Background(), bad); err == nil || !strings.Contains(err.Error(), "must not contain '?'") {
			t.Fatalf("Open(context.Background(), %q): want error mentioning \"must not contain '?'\", got %v", bad, err)
		}
	})

	t.Run("pragmas take effect on a clean file path", func(t *testing.T) {
		root, err := Open(context.Background(), filepath.Join(t.TempDir(), "pvmt.db"))
		if err != nil {
			t.Fatal(err)
		}
		t.Cleanup(func() { _ = root.Close() })

		var fk int
		if err := root.db.QueryRowContext(ctx, `PRAGMA foreign_keys`).Scan(&fk); err != nil {
			t.Fatal(err)
		}
		if fk != 1 {
			t.Errorf("foreign_keys pragma = %d, want 1 (on)", fk)
		}
		var mode string
		if err := root.db.QueryRowContext(ctx, `PRAGMA journal_mode`).Scan(&mode); err != nil {
			t.Fatal(err)
		}
		if !strings.EqualFold(mode, "wal") {
			t.Errorf("journal_mode = %q, want wal", mode)
		}
	})
}

func TestListCities(t *testing.T) {
	ctx := context.Background()
	root, err := Open(context.Background(), ":memory:")
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

// TestGC_KeepsFacilityRows: facility rows always come from Overpass whether
// or not the city enables it for pavement, so the stale-source sweep must
// leave them alone even when "overpass" is not in the keep list.
func TestGC_KeepsFacilityRows(t *testing.T) {
	ctx := context.Background()
	store := openTestStore(t)
	fac := resource.FacilityType("fire")
	if err := store.UpsertFeatures(ctx, fac, []Feature{{ID: "osm:node:1", ResourceType: fac, GeometryJSON: `{"type":"Point","coordinates":[0,0]}`, SourceAPI: "overpass"}}, nil); err != nil {
		t.Fatal(err)
	}
	if err := store.UpsertFeatures(ctx, rtRoads, []Feature{{ID: "osm:way:1", ResourceType: rtRoads, GeometryJSON: `{"type":"LineString","coordinates":[[0,0],[1,1]]}`, SourceAPI: "overpass"}}, nil); err != nil {
		t.Fatal(err)
	}
	rep, err := store.GCScan(ctx, []string{"arcgis"}, testEpoch)
	if err != nil {
		t.Fatal(err)
	}
	if rep.StaleFeatures != 1 {
		t.Errorf("stale = %d, want 1 (the road, not the facility)", rep.StaleFeatures)
	}
	if _, err := store.GCSweep(ctx, []string{"arcgis"}, testEpoch); err != nil {
		t.Fatal(err)
	}
	left, _ := store.ListFeatures(ctx, fac)
	if len(left) != 1 {
		t.Errorf("facility row deleted by the stale-source sweep: %+v", left)
	}
}

// TestGC_KeepsOwnSourcedLayerRows: an attribute layer (census blocks, source
// "tigerweb") is never in a city's keep list, yet must survive the sweep —
// the sweep applies only to the pavement types, so no per-prefix exemption
// is needed for a new own-sourced type.
func TestGC_KeepsOwnSourcedLayerRows(t *testing.T) {
	ctx := context.Background()
	store := openTestStore(t)
	blk := resource.TypeCensusBlocks
	poly := `{"type":"Polygon","coordinates":[[[0,0],[1,0],[1,1],[0,0]]]}`
	if err := store.UpsertFeatures(ctx, blk, []Feature{{ID: "census:060014507011000", ResourceType: blk, GeometryJSON: poly, SourceAPI: "tigerweb"}}, nil); err != nil {
		t.Fatal(err)
	}
	if err := store.UpsertFeatures(ctx, rtRoads, []Feature{{ID: "osm:way:1", ResourceType: rtRoads, GeometryJSON: `{"type":"LineString","coordinates":[[0,0],[1,1]]}`, SourceAPI: "overpass"}}, nil); err != nil {
		t.Fatal(err)
	}
	rep, err := store.GCScan(ctx, []string{"arcgis"}, testEpoch)
	if err != nil {
		t.Fatal(err)
	}
	if rep.StaleFeatures != 1 {
		t.Errorf("stale = %d, want 1 (the road, not the census block)", rep.StaleFeatures)
	}
	if _, err := store.GCSweep(ctx, []string{"arcgis"}, testEpoch); err != nil {
		t.Fatal(err)
	}
	if left, _ := store.ListFeatures(ctx, blk); len(left) != 1 {
		t.Errorf("census block deleted by the stale-source sweep: %+v", left)
	}
	if left, _ := store.ListFeatures(ctx, rtRoads); len(left) != 0 {
		t.Errorf("stale road survived the sweep: %+v", left)
	}
}

// testEpoch is the ComputeEpoch the db tests commit under.
const testEpoch = 1

// TestCityStore_EnsuresOnce: CityStore creates the city row once and returns
// the same id, and a store scoped to it, on every call.
func TestCityStore_EnsuresOnce(t *testing.T) {
	ctx := context.Background()
	root, err := Open(context.Background(), ":memory:")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = root.Close() })

	id1, store, err := CityStore(ctx, root, "a", "A", "test")
	if err != nil || store == nil {
		t.Fatalf("CityStore = (%d, %v, %v)", id1, store, err)
	}
	id2, _, err := CityStore(ctx, root, "a", "A", "test")
	if err != nil || id2 != id1 {
		t.Errorf("second CityStore = (%d, %v), want id %d", id2, err, id1)
	}
	if cities, err := root.ListCities(ctx); err != nil || len(cities) != 1 {
		t.Errorf("ListCities = (%v, %v), want one city", cities, err)
	}

	// The store is the ensured city's: what it writes, that city reads, and
	// another city does not.
	if err := store.SaveBoundary(ctx, geomA, "nominatim"); err != nil {
		t.Fatal(err)
	}
	if got, err := root.ForCity(id1).GetBoundary(ctx); err != nil || got == "" {
		t.Errorf("city a's boundary through ForCity = (%q, %v), want the one saved", got, err)
	}
	_, other, err := CityStore(ctx, root, "b", "B", "test")
	if err != nil {
		t.Fatal(err)
	}
	if got, err := other.GetBoundary(ctx); err != nil || got != "" {
		t.Errorf("city b's boundary = (%q, %v), want none", got, err)
	}
}

// TestOpen_CreatesOwnerOnly: a database pvmt creates, its -wal and -shm
// files, and the directory it creates for it let no one but the owner in,
// whatever the umask; an existing file keeps its mode.
func TestOpen_CreatesOwnerOnly(t *testing.T) {
	dir := filepath.Join(t.TempDir(), "data")
	path := filepath.Join(dir, FileName)
	root, err := Open(context.Background(), path)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := root.EnsureCity(context.Background(), "a", "A", "test"); err != nil {
		t.Fatal(err)
	}
	for _, p := range []string{dir, path, path + "-wal", path + "-shm"} {
		info, err := os.Stat(p)
		if err != nil {
			t.Fatal(err)
		}
		if perm := info.Mode().Perm(); perm&0o077 != 0 || perm&0o600 != 0o600 {
			t.Errorf("%s mode = %v, want owner-only", p, perm)
		}
	}
	_ = root.Close()

	if err := os.Chmod(path, 0o644); err != nil {
		t.Fatal(err)
	}
	root, err = Open(context.Background(), path)
	if err != nil {
		t.Fatalf("reopen a 0644 database: %v", err)
	}
	_ = root.Close()
	if info, _ := os.Stat(path); info.Mode().Perm() != 0o644 {
		t.Errorf("reopen changed an existing database's mode to %v", info.Mode().Perm())
	}
}
