package db

import (
	"bytes"
	"context"
	"database/sql"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"testing"
	"testing/fstest"

	"github.com/jcrussell/solvent-streets/internal/hint"
)

// TestColdMigration_SchemaShape pins the deployment-correctness contract: a
// fresh user opening pvmt for the first time runs the embedded migrations
// and ends up with the exact schema this test asserts. Round-trip tests
// against the resulting Store can't catch a migration that compiles but
// ships an unexpected column type, missing FK, or absent index — this one
// can.
//
// Don't grow this test into a column-by-column type fingerprint; pin only
// the invariants that matter: table presence, city_id FK on scoped tables,
// the result tables' (run, measure) primary keys, and the lookup indexes the
// query planner depends on.
func TestColdMigration_SchemaShape(t *testing.T) {
	ctx := context.Background()

	root, err := Open(":memory:")
	if err != nil {
		t.Fatalf("Open(:memory:): %v", err)
	}
	t.Cleanup(func() { _ = root.Close() })

	t.Run("expected tables exist", func(t *testing.T) {
		want := []string{
			"cities",
			"city_boundaries",
			"city_inputs",
			"city_metrics",
			"features",
			"hex_metrics",
			"run_facilities",
			"run_inputs",
			"runs",
			"schema_version",
			"snapshot_boundaries",
			"snapshot_cities",
			"snapshot_parts",
			"snapshots",
			"working_parts",
		}
		got := listTables(t, ctx, root.db)
		for _, name := range want {
			if !slices.Contains(got, name) {
				t.Errorf("missing table %q; got tables %v", name, got)
			}
		}
		// Forecasts are simulated on demand from the cohort split, never
		// stored, and the pavement-specific result tables of generation 3
		// are now city_metrics / hex_metrics rows.
		for _, gone := range []string{"forecast_results", "compute_results", "hex_stats", "cohort_stats"} {
			if slices.Contains(got, gone) {
				t.Errorf("%s exists; generation 1 has no such table", gone)
			}
		}
	})

	t.Run("city-scoped tables FK to cities(id)", func(t *testing.T) {
		// Every table that holds per-city data must enforce cities(id) so
		// orphaned rows can't accumulate when a city is removed. Result rows
		// are scoped through their run instead (see
		// TestColdMigration_ResultRowsRequireARun).
		scoped := []string{
			"city_boundaries",
			"city_inputs",
			"features",
			"runs",
			"snapshot_boundaries",
			"snapshot_cities",
			"working_parts",
		}
		for _, table := range scoped {
			fks := listForeignKeys(t, ctx, root.db, table)
			var hasCityFK bool
			for _, fk := range fks {
				if fk.from == "city_id" && fk.table == "cities" && fk.to == "id" {
					hasCityFK = true
					break
				}
			}
			if !hasCityFK {
				t.Errorf("table %q missing FK city_id -> cities(id); got %+v", table, fks)
			}
		}
	})

	t.Run("result tables are keyed by run and measure", func(t *testing.T) {
		// The primary key makes each (run, measure) write-once, and its run_id
		// prefix is the index every read and delete seeks. No id, city_id or
		// computed_at: the run owns all three.
		want := map[string][]string{
			"city_metrics": {"run_id", "metric", "subject", "class"},
			"hex_metrics":  {"run_id", "metric", "subject", "hex_id"},
		}
		for table, pk := range want {
			cols := listColumns(t, ctx, root.db, table)
			got := make([]string, len(pk))
			for _, c := range cols {
				if c.pk > 0 && c.pk <= len(pk) {
					got[c.pk-1] = c.name
				}
				if c.name == "id" || c.name == "city_id" || c.name == "computed_at" {
					t.Errorf("%s.%s exists; result rows hang off their run", table, c.name)
				}
			}
			if !slices.Equal(got, pk) {
				t.Errorf("%s primary key = %v, want %v", table, got, pk)
			}
		}
	})

	t.Run("cold migration records the latest schema version", func(t *testing.T) {
		// Generation 1 is a single squashed 001_init.sql, so the highest
		// recorded schema_version is 1. Bump this when an additive 002 lands.
		var version int
		if err := root.db.QueryRowContext(ctx, `SELECT COALESCE(MAX(version), 0) FROM schema_version`).Scan(&version); err != nil {
			t.Fatalf("schema_version: %v", err)
		}
		if version != 1 {
			t.Errorf("schema_version = %d; want 1 (the squashed 001_init.sql)", version)
		}
	})

	t.Run("query-planner indexes exist", func(t *testing.T) {
		// These are the indexes the runtime path depends on. Dropping any
		// would silently turn a hot read into a table scan.
		want := map[string][]string{
			"snapshot_cities": {"idx_snapshot_cities_city", "idx_snapshot_cities_boundary"},
			"snapshot_parts":  {"idx_snapshot_parts_run"},
			"working_parts":   {"idx_working_parts_run"},
		}
		for table, indexes := range want {
			got := listIndexes(t, ctx, root.db, table)
			for _, name := range indexes {
				if !slices.Contains(got, name) {
					t.Errorf("table %q missing index %q; got %v", table, name, got)
				}
			}
		}
	})
}

// TestColdMigration_SnapshotTags pins the snapshot layout: snapshots is the
// tag (no city_id, a unique name); each city it covers is a snapshot_cities
// row, and each manifest row names its city.
func TestColdMigration_SnapshotTags(t *testing.T) {
	ctx := context.Background()
	root, err := Open(":memory:")
	if err != nil {
		t.Fatalf("Open(:memory:): %v", err)
	}
	t.Cleanup(func() { _ = root.Close() })
	want := map[string][]string{
		"snapshots":       {"id"},
		"snapshot_cities": {"snapshot_id", "city_id"},
		"snapshot_parts":  {"snapshot_id", "city_id", "part"},
	}
	for table, pk := range want {
		cols := listColumns(t, ctx, root.db, table)
		got := make([]string, len(pk))
		for _, c := range cols {
			if c.pk > 0 && c.pk <= len(pk) {
				got[c.pk-1] = c.name
			}
		}
		if !slices.Equal(got, pk) {
			t.Errorf("%s primary key = %v, want %v", table, got, pk)
		}
	}
	var names []string
	for _, c := range listColumns(t, ctx, root.db, "snapshots") {
		names = append(names, c.name)
	}
	if !slices.Equal(names, []string{"id", "name", "created_at", "compute_epoch"}) {
		t.Errorf("snapshots columns = %v, want id, name, created_at, compute_epoch", names)
	}
	var unique int
	if err := root.db.QueryRowContext(ctx,
		`SELECT COUNT(*) FROM pragma_index_list('snapshots') WHERE "unique" = 1 AND origin = 'u'`).Scan(&unique); err != nil {
		t.Fatal(err)
	}
	if unique != 1 {
		t.Errorf("snapshots has %d UNIQUE constraint(s), want 1 (name)", unique)
	}
	fks := listForeignKeys(t, ctx, root.db, "snapshot_parts")
	var toCity bool
	for _, fk := range fks {
		if fk.table == "snapshot_cities" {
			toCity = true
		}
	}
	if !toCity {
		t.Errorf("snapshot_parts has no FK to snapshot_cities; got %+v", fks)
	}
}

// TestColdMigration_FeaturesKey pins the features layout: keyed by city,
// type and id, WITHOUT ROWID, and no secondary index. The key is the table's
// only index: every features statement seeks its (city_id, resource_type)
// prefix (TestQueryPlan_FeaturesStatements).
func TestColdMigration_FeaturesKey(t *testing.T) {
	ctx := context.Background()
	root, err := Open(":memory:")
	if err != nil {
		t.Fatalf("Open(:memory:): %v", err)
	}
	t.Cleanup(func() { _ = root.Close() })
	want := []string{"city_id", "resource_type", "id"}
	got := make([]string, len(want))
	for _, c := range listColumns(t, ctx, root.db, "features") {
		if c.pk > 0 && c.pk <= len(want) {
			got[c.pk-1] = c.name
		}
	}
	if !slices.Equal(got, want) {
		t.Errorf("features primary key = %v, want %v", got, want)
	}
	var sqlText string
	if err := root.db.QueryRowContext(ctx, `SELECT sql FROM sqlite_master WHERE type = 'table' AND name = 'features'`).Scan(&sqlText); err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(sqlText, "WITHOUT ROWID") {
		t.Errorf("features is not WITHOUT ROWID:\n%s", sqlText)
	}
	for _, idx := range listIndexes(t, ctx, root.db, "features") {
		if !strings.HasPrefix(idx, "sqlite_autoindex_") {
			t.Errorf("features has secondary index %s; the primary key serves every statement", idx)
		}
	}
}

// TestColdMigration_ResultRowsRequireARun pins that every result row is
// tagged with the run that wrote it, NOT NULL with an FK to runs(id): a row no
// snapshot can reach through a run would be invisible forever.
func TestColdMigration_ResultRowsRequireARun(t *testing.T) {
	ctx := context.Background()
	root, err := Open(":memory:")
	if err != nil {
		t.Fatalf("Open(:memory:): %v", err)
	}
	t.Cleanup(func() { _ = root.Close() })
	// Every result row is tagged with the run that wrote it, NOT NULL: a
	// row no snapshot can reach through a run would be invisible forever.
	for _, table := range []string{"city_metrics", "hex_metrics", "run_inputs", "run_facilities"} {
		var run *columnInfo
		cols := listColumns(t, ctx, root.db, table)
		for i := range cols {
			if cols[i].name == "run_id" {
				run = &cols[i]
			}
			if cols[i].name == "snapshot_id" {
				t.Errorf("%s still has snapshot_id; rows belong to runs, snapshots reference runs", table)
			}
		}
		if run == nil || !run.notNull {
			t.Errorf("%s.run_id = %+v, want a NOT NULL column", table, run)
		}
		if !slices.ContainsFunc(listForeignKeys(t, ctx, root.db, table), func(fk foreignKey) bool {
			return fk.from == "run_id" && fk.table == "runs" && fk.to == "id" && fk.onDelete == "CASCADE"
		}) {
			t.Errorf("%s missing FK run_id -> runs(id) ON DELETE CASCADE", table)
		}
	}
}

// TestQueryPlan_UsesCompositeIndexes is a regression guard for the indexes
// the hot reads depend on. Every result read has the viewQuery shape —
// `run_id IN (<the snapshot's runs>) AND ...` — and must seek the result
// table's (run_id, ...) primary key, not scan every run's rows. The snapshot
// resolution and the run-reference checks must seek too.
func TestQueryPlan_UsesCompositeIndexes(t *testing.T) {
	ctx := context.Background()

	root, err := Open(":memory:")
	if err != nil {
		t.Fatalf("Open(:memory:): %v", err)
	}
	t.Cleanup(func() { _ = root.Close() })

	cases := []struct {
		name      string
		query     string
		wantIndex string
	}{
		{"city_metrics view read", `SELECT value FROM city_metrics WHERE run_id IN (?, ?, ?) AND class = ? AND subject IN (?, ?)`, "PRIMARY KEY"},
		{"hex_metrics view read", `SELECT value FROM hex_metrics WHERE run_id IN (?, ?, ?) AND metric = ? AND subject = ?`, "PRIMARY KEY"},
		{"run still referenced", sqlRunInSnapshots, "INDEX idx_snapshot_parts_run"},
		{"run in the working state", `SELECT COUNT(*) FROM working_parts WHERE run_id = ?`, "INDEX idx_working_parts_run"},
		{"input digests", `SELECT input, digest FROM city_inputs WHERE city_id = ? AND input IN (?, ?)`, "(city_id=? AND input=?)"},
		{"count a run's rows", `SELECT COUNT(*) FROM hex_metrics WHERE run_id IN (?, ?)`, "PRIMARY KEY"},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			plan := queryPlan(t, ctx, root.db, tc.query)
			// Accept both "USING INDEX" and "USING COVERING INDEX" — the
			// latter (an index-only plan) is strictly better and is what the
			// MAX(snapshot_id) subqueries resolve to.
			if !strings.Contains(plan, tc.wantIndex) {
				t.Errorf("query %q\nplan:\n%s\nwant %s", tc.query, plan, tc.wantIndex)
			}
			for _, table := range []string{"features", "city_metrics", "hex_metrics", "snapshot_cities", "snapshot_parts", "working_parts", "city_inputs"} {
				if strings.Contains(plan, "SCAN "+table+"\n") {
					t.Errorf("query %q falls back to a full table scan:\n%s", tc.query, plan)
				}
			}
		})
	}
}

// TestQueryPlan_FeaturesStatements runs EXPLAIN QUERY PLAN on every
// statement against features, exactly as the store issues it, and requires a
// primary-key search with no full scan and no temp b-tree (the digest's ORDER
// BY id must come from key order). A new features statement belongs here.
func TestQueryPlan_FeaturesStatements(t *testing.T) {
	ctx := context.Background()
	root, err := Open(":memory:")
	if err != nil {
		t.Fatalf("Open(:memory:): %v", err)
	}
	t.Cleanup(func() { _ = root.Close() })
	stale, _ := (&sqliteStore{cityID: 1}).staleFeatures([]string{"overpass", "arcgis"})

	cases := map[string]string{
		"upsert":                  sqlUpsertFeature,
		"replace all":             sqlDeleteFeatures,
		"replace some sources":    sqlDeleteFeaturesFrom(2),
		"list":                    sqlListFeatures,
		"digest in id order":      sqlFeaturesDigest,
		"gc stale types":          sqlStaleFeatureTypes(stale),
		"gc stale count":          sqlCountFeaturesWhere(stale),
		"gc stale delete":         sqlDeleteFeaturesWhere(stale),
		"stats reads city_inputs": sqlStoredFeatures,
	}
	for name, query := range cases {
		t.Run(name, func(t *testing.T) {
			plan := queryPlan(t, ctx, root.db, query)
			if strings.HasPrefix(query, "INSERT") {
				// An insert has no search; it must not scan either.
				if strings.Contains(plan, "SCAN ") {
					t.Errorf("query %q scans:\n%s", query, plan)
				}
				return
			}
			if !strings.Contains(plan, "SEARCH features USING PRIMARY KEY (city_id=? AND resource_type=?") &&
				!strings.Contains(plan, "SEARCH city_inputs USING INDEX sqlite_autoindex_city_inputs_1 (city_id=? AND input=?)") {
				t.Errorf("query %q does not seek the primary key:\n%s", query, plan)
			}
			if strings.Contains(plan, "SCAN ") || strings.Contains(plan, "USE TEMP B-TREE") {
				t.Errorf("query %q scans or sorts:\n%s", query, plan)
			}
		})
	}
}

// TestQueryPlan_SnapshotStatements runs EXPLAIN QUERY PLAN on every
// statement that resolves, lists or deletes a snapshot, exactly as the store
// issues it, and requires each table it reads to be searched by a key or
// index, never scanned, and no temp b-tree for an ORDER BY the key gives. A
// new snapshot statement belongs here.
func TestQueryPlan_SnapshotStatements(t *testing.T) {
	ctx := context.Background()
	root, err := Open(":memory:")
	if err != nil {
		t.Fatalf("Open(:memory:): %v", err)
	}
	t.Cleanup(func() { _ = root.Close() })

	cases := []struct {
		name  string
		query string
		want  []string // plan fragments, one per table read
	}{
		{"pinned city in a snapshot", sqlCityInSnapshot, []string{"SEARCH snapshot_cities USING PRIMARY KEY (snapshot_id=? AND city_id=?)"}},
		{"newest snapshot containing a city", sqlNewestSnapshotForCity, []string{"SEARCH snapshot_cities USING COVERING INDEX idx_snapshot_cities_city (city_id=?)"}},
		{"a city's manifest", sqlSnapshotParts, []string{"SEARCH snapshot_parts USING PRIMARY KEY (snapshot_id=? AND city_id=?)"}},
		{"resolve a name for a city", sqlResolveSnapshotName, []string{
			"SEARCH s USING COVERING INDEX sqlite_autoindex_snapshots_1 (name=?)",
			"SEARCH c USING PRIMARY KEY (snapshot_id=? AND city_id=?)",
		}},
		{"frozen boundary", sqlFrozenBoundary, []string{
			"SEARCH c USING PRIMARY KEY (snapshot_id=? AND city_id=?)",
			"SEARCH b USING INTEGER PRIMARY KEY (rowid=?)",
		}},
		{"run still in a snapshot", sqlRunInSnapshots, []string{"SEARCH snapshot_parts USING COVERING INDEX idx_snapshot_parts_run (run_id=?)"}},
		{"sweep frozen boundaries", sqlSweepSnapshotBoundaries, []string{
			"SEARCH snapshot_boundaries USING COVERING INDEX sqlite_autoindex_snapshot_boundaries_1 (city_id=?)",
			"idx_snapshot_cities_boundary",
		}},
		{"snapshot by name", sqlSnapshotByName, []string{"SEARCH snapshots USING COVERING INDEX sqlite_autoindex_snapshots_1 (name=?)"}},
		{"a snapshot's runs (delete)", sqlSnapshotRuns, []string{"SEARCH snapshot_parts USING PRIMARY KEY (snapshot_id=?)"}},
		{"list snapshots", sqlListSnapshots, []string{
			"SCAN s", // every snapshot is listed: a scan of the tag table, by its key
			"SEARCH c USING PRIMARY KEY (snapshot_id=?)",
		}},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			plan := queryPlan(t, ctx, root.db, tc.query)
			for _, want := range tc.want {
				if !strings.Contains(plan, want) {
					t.Errorf("query %q\nplan:\n%s\nwant %s", tc.query, plan, want)
				}
			}
			for _, table := range []string{"snapshot_cities", "snapshot_parts", "snapshot_boundaries", "c", "p", "b"} {
				if strings.Contains(plan, "SCAN "+table+"\n") || strings.HasSuffix(plan, "SCAN "+table) {
					t.Errorf("query %q scans %s:\n%s", tc.query, table, plan)
				}
			}
			if strings.Contains(plan, "USE TEMP B-TREE") {
				t.Errorf("query %q sorts:\n%s", tc.query, plan)
			}
		})
	}
}

// queryPlan runs EXPLAIN QUERY PLAN and returns the joined detail rows. The
// query may carry bind placeholders; we bind NULL for each so the planner sees
// the same shape the runtime does without needing real values.
func queryPlan(t *testing.T, ctx context.Context, d *sql.DB, query string) string {
	t.Helper()
	args := make([]any, strings.Count(query, "?"))
	rows, err := d.QueryContext(ctx, "EXPLAIN QUERY PLAN "+query, args...)
	if err != nil {
		t.Fatalf("EXPLAIN QUERY PLAN %q: %v", query, err)
	}
	defer func() { _ = rows.Close() }()
	var details []string
	for rows.Next() {
		var id, parent, notused int
		var detail string
		if err := rows.Scan(&id, &parent, &notused, &detail); err != nil {
			t.Fatalf("scan query plan: %v", err)
		}
		details = append(details, detail)
	}
	if err := rows.Err(); err != nil {
		t.Fatalf("iterate query plan: %v", err)
	}
	return strings.Join(details, "\n") + "\n"
}

type columnInfo struct {
	name    string
	typ     string
	pk      int
	notNull bool
}

type foreignKey struct {
	from, table, to, onDelete string
}

func listTables(t *testing.T, ctx context.Context, d *sql.DB) []string {
	t.Helper()
	rows, err := d.QueryContext(ctx, `SELECT name FROM sqlite_master WHERE type = 'table' ORDER BY name`)
	if err != nil {
		t.Fatalf("query tables: %v", err)
	}
	defer func() { _ = rows.Close() }()
	var names []string
	for rows.Next() {
		var n string
		if err := rows.Scan(&n); err != nil {
			t.Fatalf("scan table name: %v", err)
		}
		names = append(names, n)
	}
	if err := rows.Err(); err != nil {
		t.Fatalf("iterate tables: %v", err)
	}
	return names
}

func listColumns(t *testing.T, ctx context.Context, d *sql.DB, table string) []columnInfo {
	t.Helper()
	rows, err := d.QueryContext(ctx, `SELECT name, type, pk, "notnull" FROM pragma_table_info(?)`, table)
	if err != nil {
		t.Fatalf("pragma_table_info(%q): %v", table, err)
	}
	defer func() { _ = rows.Close() }()
	var cols []columnInfo
	for rows.Next() {
		var c columnInfo
		if err := rows.Scan(&c.name, &c.typ, &c.pk, &c.notNull); err != nil {
			t.Fatalf("scan column: %v", err)
		}
		cols = append(cols, c)
	}
	if err := rows.Err(); err != nil {
		t.Fatalf("iterate columns: %v", err)
	}
	return cols
}

func listForeignKeys(t *testing.T, ctx context.Context, d *sql.DB, table string) []foreignKey {
	t.Helper()
	rows, err := d.QueryContext(ctx, `SELECT "from", "table", "to", on_delete FROM pragma_foreign_key_list(?)`, table)
	if err != nil {
		t.Fatalf("pragma_foreign_key_list(%q): %v", table, err)
	}
	defer func() { _ = rows.Close() }()
	var fks []foreignKey
	for rows.Next() {
		var fk foreignKey
		if err := rows.Scan(&fk.from, &fk.table, &fk.to, &fk.onDelete); err != nil {
			t.Fatalf("scan fk: %v", err)
		}
		fks = append(fks, fk)
	}
	if err := rows.Err(); err != nil {
		t.Fatalf("iterate fks: %v", err)
	}
	return fks
}

func listIndexes(t *testing.T, ctx context.Context, d *sql.DB, table string) []string {
	t.Helper()
	rows, err := d.QueryContext(ctx, `SELECT name FROM pragma_index_list(?)`, table)
	if err != nil {
		t.Fatalf("pragma_index_list(%q): %v", table, err)
	}
	defer func() { _ = rows.Close() }()
	var names []string
	for rows.Next() {
		var n string
		if err := rows.Scan(&n); err != nil {
			t.Fatalf("scan index: %v", err)
		}
		names = append(names, n)
	}
	if err := rows.Err(); err != nil {
		t.Fatalf("iterate indexes: %v", err)
	}
	return names
}

// TestMigrateFS_AcceptsCustomFS verifies the byob-interfaces.3 seam:
// migrateFS reads SQL from any fs.FS, so tests can drive migration
// scenarios via fstest.MapFS without touching disk or the embedded
// production migrations. The production migrate() wraps migrateFS
// with the embedded set; this test pins that the seam works.
func TestMigrateFS_AcceptsCustomFS(t *testing.T) {
	ctx := context.Background()

	mapFS := fstest.MapFS{
		"mig/001_init.sql": &fstest.MapFile{
			Data: []byte(`CREATE TABLE t (id INTEGER PRIMARY KEY);`),
		},
		"mig/002_add.sql": &fstest.MapFile{
			Data: []byte(`ALTER TABLE t ADD COLUMN name TEXT;`),
		},
		"mig/notes.txt": &fstest.MapFile{Data: []byte("ignored")},
	}

	d, err := sql.Open("sqlite", ":memory:")
	if err != nil {
		t.Fatalf("open: %v", err)
	}
	t.Cleanup(func() { _ = d.Close() })

	if err := migrateFS(ctx, d, mapFS, "mig"); err != nil {
		t.Fatalf("migrateFS: %v", err)
	}

	var version int
	if err := d.QueryRowContext(ctx, `SELECT MAX(version) FROM schema_version`).Scan(&version); err != nil {
		t.Fatalf("schema_version: %v", err)
	}
	if version != 2 {
		t.Errorf("schema_version = %d, want 2 (notes.txt should be skipped)", version)
	}

	// Idempotent: a second run with the same FS is a no-op.
	if err := migrateFS(ctx, d, mapFS, "mig"); err != nil {
		t.Fatalf("migrateFS idempotent run: %v", err)
	}
}

// TestMigrateFS_NumericVersionOrder pins the yvlv.14 fix: migrations must
// apply in ascending numeric version order, not lexical filename order.
// Lexically "10_*.sql" < "2_*.sql", so a lexical sort would run version 10
// before version 2 and apply dependency-ordered DDL backwards. Here
// migration 10 ALTERs a column that migration 2 creates the table for; if
// they run out of numeric order, migration 10 fails. We also assert the
// recorded schema_version rows are in ascending order.
func TestMigrateFS_NumericVersionOrder(t *testing.T) {
	ctx := context.Background()

	mapFS := fstest.MapFS{
		"mig/1_a.sql": &fstest.MapFile{
			Data: []byte(`CREATE TABLE base (id INTEGER PRIMARY KEY);`),
		},
		// Creates table t. Numerically must run before 10_c.
		"mig/2_b.sql": &fstest.MapFile{
			Data: []byte(`CREATE TABLE t (id INTEGER PRIMARY KEY);`),
		},
		// Depends on table t existing — only valid if 2_b ran first. Under a
		// lexical sort ("10_c" < "2_b") this would run first and fail.
		"mig/10_c.sql": &fstest.MapFile{
			Data: []byte(`ALTER TABLE t ADD COLUMN name TEXT;`),
		},
	}

	d, err := sql.Open("sqlite", ":memory:")
	if err != nil {
		t.Fatalf("open: %v", err)
	}
	t.Cleanup(func() { _ = d.Close() })

	if err := migrateFS(ctx, d, mapFS, "mig"); err != nil {
		t.Fatalf("migrateFS: %v (migrations applied out of numeric order?)", err)
	}

	// All three applied: table t has the column 10_c added.
	cols := listColumns(t, ctx, d, "t")
	var hasName bool
	for _, c := range cols {
		if c.name == "name" {
			hasName = true
		}
	}
	if !hasName {
		t.Errorf("migration 10 (ADD COLUMN name) did not apply; columns = %+v", cols)
	}
	if !slices.Contains(listTables(t, ctx, d), "base") {
		t.Errorf("migration 1 did not apply; base table missing")
	}

	// schema_version rows recorded in ascending numeric order.
	rows, err := d.QueryContext(ctx, `SELECT version FROM schema_version ORDER BY rowid`)
	if err != nil {
		t.Fatalf("query schema_version: %v", err)
	}
	defer func() { _ = rows.Close() }()
	var got []int
	for rows.Next() {
		var v int
		if err := rows.Scan(&v); err != nil {
			t.Fatalf("scan version: %v", err)
		}
		got = append(got, v)
	}
	if err := rows.Err(); err != nil {
		t.Fatalf("iterate schema_version: %v", err)
	}
	want := []int{1, 2, 10}
	if !slices.Equal(got, want) {
		t.Errorf("schema_version apply order = %v, want %v (numeric ascending)", got, want)
	}
}

// TestMigrateFS_PartialFailureRollsBack pins the per-migration BeginTx
// contract: a multi-statement migration whose later statement fails must
// leave the database exactly as it was before the migration started — no
// orphan tables from the successful prefix, no schema_version row, and
// the prior migration's effects intact. modernc.org/sqlite otherwise runs
// each statement in its own implicit txn, so without the wrap the CREATE
// from a failing migration would persist.
func TestMigrateFS_PartialFailureRollsBack(t *testing.T) {
	ctx := context.Background()

	mapFS := fstest.MapFS{
		"mig/001_init.sql": &fstest.MapFile{
			Data: []byte(`CREATE TABLE keepers (id INTEGER PRIMARY KEY);`),
		},
		// CREATE succeeds, then INSERT into a non-existent table fails.
		// Without the tx wrap, "orphan" would survive the failure.
		"mig/002_bad.sql": &fstest.MapFile{
			Data: []byte(`
CREATE TABLE orphan (id INTEGER PRIMARY KEY);
INSERT INTO does_not_exist (id) VALUES (1);
`),
		},
	}

	d, err := sql.Open("sqlite", ":memory:")
	if err != nil {
		t.Fatalf("open: %v", err)
	}
	t.Cleanup(func() { _ = d.Close() })

	err = migrateFS(ctx, d, mapFS, "mig")
	if err == nil {
		t.Fatal("migrateFS: want error from migration 002, got nil")
	}
	if !strings.Contains(err.Error(), "002_bad.sql") {
		t.Errorf("migrateFS error = %v, want it to name the failing migration", err)
	}

	tables := listTables(t, ctx, d)
	if !slices.Contains(tables, "keepers") {
		t.Errorf("migration 001 effects lost; tables = %v", tables)
	}
	if slices.Contains(tables, "orphan") {
		t.Errorf("partial migration 002 leaked the orphan table; tables = %v", tables)
	}

	var version int
	if err := d.QueryRowContext(ctx, `SELECT COALESCE(MAX(version), 0) FROM schema_version`).Scan(&version); err != nil {
		t.Fatalf("schema_version: %v", err)
	}
	if version != 1 {
		t.Errorf("schema_version = %d, want 1 (migration 002 rolled back)", version)
	}
}

// TestMigrateFS_SchemaVersionUniqueFromTheStart pins that the one-row-per-
// migration index exists before any migration runs. It used to arrive with
// migration 002, which also had to repair duplicates a concurrent first run
// could already have recorded (q48z.3); created up front, the duplicate can
// never be written.
func TestMigrateFS_SchemaVersionUniqueFromTheStart(t *testing.T) {
	ctx := context.Background()
	d, err := sql.Open("sqlite", ":memory:")
	if err != nil {
		t.Fatalf("open: %v", err)
	}
	t.Cleanup(func() { _ = d.Close() })

	if err := migrateFS(ctx, d, fstest.MapFS{"m/001_a.sql": &fstest.MapFile{Data: []byte(`CREATE TABLE a (x INTEGER);`)}}, "m"); err != nil {
		t.Fatalf("migrateFS: %v", err)
	}
	if _, err := d.ExecContext(ctx, `INSERT INTO schema_version (version) VALUES (1)`); err == nil {
		t.Error("duplicate schema_version insert succeeded; the unique index is not enforcing")
	}
}

// openRaw opens a database file without migrating it, so a test can seed the
// shape an older pvmt left behind. It uses Open's own pragmas plus WAL in the
// DSN, as every older pvmt did, so the file starts in the state a real
// install's database is in.
func openRaw(t *testing.T, path string) *sql.DB {
	t.Helper()
	d, err := sql.Open("sqlite", path+dsnPragmas+"&_pragma=journal_mode(WAL)")
	if err != nil {
		t.Fatalf("open %s: %v", path, err)
	}
	t.Cleanup(func() { _ = d.Close() })
	return d
}

// TestOpen_RefusesOlderGeneration pins solvent-streets-25t6.2. A database an
// older pvmt created records schema_version 1, so after the squash migrateFS
// would skip the new 001_init.sql and run on the old schema. Open must refuse
// it with ErrIncompatibleDatabase, name the path in the hint, and leave the
// file byte-identical: it may hold hours of ingested data.
func TestOpen_RefusesOlderGeneration(t *testing.T) {
	for _, tc := range []struct {
		name     string
		versions []int
		gen      int    // PRAGMA user_version stamped on the seeded file; 0 = never stamped
		table    string // a table only that generation had, seeded alongside the stamp
	}{
		{"v0.4.1 (001, 002)", []int{1, 2}, 0, ""},
		{"pre-release HEAD (001, 002, 003)", []int{1, 2, 3}, 0, ""},
	} {
		t.Run(tc.name, func(t *testing.T) {
			ctx := context.Background()
			path := filepath.Join(t.TempDir(), "pvmt.db")
			d := openRaw(t, path)
			if _, err := d.ExecContext(ctx, `CREATE TABLE schema_version (version INTEGER NOT NULL);
				CREATE TABLE cities (id INTEGER PRIMARY KEY, slug TEXT, name TEXT, config_id TEXT);
				INSERT INTO cities (slug, name, config_id) VALUES ('x', 'X', 'cfg');`); err != nil {
				t.Fatalf("seed: %v", err)
			}
			for _, v := range tc.versions {
				if _, err := d.ExecContext(ctx, `INSERT INTO schema_version (version) VALUES (?)`, v); err != nil {
					t.Fatalf("seed version %d: %v", v, err)
				}
			}
			if tc.gen > 0 {
				if _, err := d.ExecContext(ctx, fmt.Sprintf(`CREATE TABLE %s (id INTEGER PRIMARY KEY);
					PRAGMA user_version = %d;`, tc.table, tc.gen)); err != nil {
					t.Fatalf("seed generation %d: %v", tc.gen, err)
				}
			}
			if err := d.Close(); err != nil {
				t.Fatalf("close: %v", err)
			}
			before, err := os.ReadFile(path)
			if err != nil {
				t.Fatalf("read before: %v", err)
			}

			root, err := Open(path)
			if err == nil {
				_ = root.Close()
				t.Fatal("Open succeeded on an older-generation database; want ErrIncompatibleDatabase")
			}
			if !errors.Is(err, ErrIncompatibleDatabase) {
				t.Fatalf("err = %v, want ErrIncompatibleDatabase", err)
			}
			if want := fmt.Sprintf("schema generation %d predates this pvmt's %d", tc.gen, schemaGeneration); !strings.Contains(err.Error(), want) {
				t.Errorf("err = %v, want it to say %q", err, want)
			}
			var h *hint.ErrHint
			if !errors.As(err, &h) || !strings.Contains(h.Hint, path) {
				t.Errorf("hint = %v, want one naming %s", h, path)
			}
			after, err := os.ReadFile(path)
			if err != nil {
				t.Fatalf("read after: %v", err)
			}
			if !bytes.Equal(before, after) {
				t.Error("refused database was modified on disk")
			}
		})
	}
}

// TestOpen_ForeignFileLeftUntouched pins solvent-streets-cmth. Open applied
// journal_mode(WAL) in the DSN, so a refused rollback-journal file was
// converted to WAL before checkGeneration ever looked at it, and a SQLite
// file no pvmt wrote was reported as "schema generation 0". A refused file
// must stay byte-identical, and one without pvmt's schema_version table is
// called what it is.
func TestOpen_ForeignFileLeftUntouched(t *testing.T) {
	ctx := context.Background()
	path := filepath.Join(t.TempDir(), "other.db")
	d, err := sql.Open("sqlite", path) // default rollback journal, no pvmt pragmas
	if err != nil {
		t.Fatalf("open: %v", err)
	}
	if _, err := d.ExecContext(ctx, `CREATE TABLE notes (id INTEGER PRIMARY KEY, body TEXT);
		INSERT INTO notes (body) VALUES ('keep me');`); err != nil {
		t.Fatalf("seed: %v", err)
	}
	if err := d.Close(); err != nil {
		t.Fatalf("close: %v", err)
	}
	before, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("read before: %v", err)
	}

	root, err := Open(path)
	if err == nil {
		_ = root.Close()
		t.Fatal("Open succeeded on a foreign SQLite file; want ErrIncompatibleDatabase")
	}
	if !errors.Is(err, ErrIncompatibleDatabase) {
		t.Fatalf("err = %v, want ErrIncompatibleDatabase", err)
	}
	if !strings.Contains(err.Error(), "not a pvmt database") {
		t.Errorf("err = %v, want it to say the file is not a pvmt database", err)
	}
	if strings.Contains(err.Error(), "schema generation") {
		t.Errorf("err = %v, should not claim a schema generation for a foreign file", err)
	}
	after, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("read after: %v", err)
	}
	if !bytes.Equal(before, after) {
		t.Error("refused foreign database was modified on disk (journal mode converted?)")
	}
	for _, companion := range []string{path + "-wal", path + "-shm"} {
		if _, err := os.Stat(companion); err == nil {
			t.Errorf("refused Open left %s behind", companion)
		}
	}
}

// TestOpen_RefusesNewerGeneration pins the downgrade direction: a database
// stamped by a newer pvmt is refused, not reinterpreted.
func TestOpen_RefusesNewerGeneration(t *testing.T) {
	ctx := context.Background()
	path := filepath.Join(t.TempDir(), "pvmt.db")
	d := openRaw(t, path)
	if _, err := d.ExecContext(ctx, fmt.Sprintf(`CREATE TABLE schema_version (version INTEGER NOT NULL); CREATE TABLE cities (id INTEGER PRIMARY KEY); PRAGMA user_version = %d;`, schemaGeneration+1)); err != nil {
		t.Fatalf("seed: %v", err)
	}
	_ = d.Close()

	if _, err := Open(path); !errors.Is(err, ErrIncompatibleDatabase) {
		t.Fatalf("err = %v, want ErrIncompatibleDatabase", err)
	}
}

// TestOpen_StampsAndReopensCurrentGeneration pins the happy path: a brand-new
// file (and a zero-byte one, which is how some tools pre-create it) is stamped
// with schemaGeneration on first open, and reopening the stamped database works.
func TestOpen_StampsAndReopensCurrentGeneration(t *testing.T) {
	for _, tc := range []struct {
		name    string
		prepare func(path string) error
	}{
		{"missing file", func(string) error { return nil }},
		{"zero-byte file", func(path string) error { return os.WriteFile(path, nil, 0o600) }},
	} {
		t.Run(tc.name, func(t *testing.T) {
			ctx := context.Background()
			path := filepath.Join(t.TempDir(), "pvmt.db")
			if err := tc.prepare(path); err != nil {
				t.Fatalf("prepare: %v", err)
			}
			for i := range 2 {
				root, err := Open(path)
				if err != nil {
					t.Fatalf("Open #%d: %v", i+1, err)
				}
				var gen int
				if err := root.db.QueryRowContext(ctx, `PRAGMA user_version`).Scan(&gen); err != nil {
					t.Fatalf("user_version: %v", err)
				}
				if gen != schemaGeneration {
					t.Errorf("Open #%d: user_version = %d, want %d", i+1, gen, schemaGeneration)
				}
				if err := root.Close(); err != nil {
					t.Fatalf("close: %v", err)
				}
			}
		})
	}
}

// TestApplyMigration_SkipsAlreadyRecordedVersion pins the other half of the
// q48z.3 fix: applyMigration re-reads the applied version INSIDE its
// transaction, so a migration whose version another process recorded between
// migrateFS's unsynchronized MAX(version) probe and this call is a no-op rather
// than a second row.
func TestApplyMigration_SkipsAlreadyRecordedVersion(t *testing.T) {
	ctx := context.Background()

	d, err := sql.Open("sqlite", ":memory:")
	if err != nil {
		t.Fatalf("open: %v", err)
	}
	t.Cleanup(func() { _ = d.Close() })

	if _, err := d.ExecContext(ctx, `CREATE TABLE schema_version (version INTEGER NOT NULL)`); err != nil {
		t.Fatalf("create schema_version: %v", err)
	}
	if _, err := d.ExecContext(ctx, `INSERT INTO schema_version (version) VALUES (7)`); err != nil {
		t.Fatalf("seed version: %v", err)
	}

	if err := applyMigration(ctx, d, "007_x.sql", 7, `CREATE TABLE must_not_exist (id INTEGER);`); err != nil {
		t.Fatalf("applyMigration on an already-recorded version: %v", err)
	}

	var rows int
	if err := d.QueryRowContext(ctx,
		`SELECT COUNT(*) FROM schema_version WHERE version = 7`).Scan(&rows); err != nil {
		t.Fatalf("count: %v", err)
	}
	if rows != 1 {
		t.Errorf("schema_version has %d rows for version 7, want 1", rows)
	}

	var tables int
	if err := d.QueryRowContext(ctx,
		`SELECT COUNT(*) FROM sqlite_master WHERE type = 'table' AND name = 'must_not_exist'`).Scan(&tables); err != nil {
		t.Fatalf("table lookup: %v", err)
	}
	if tables != 0 {
		t.Errorf("migration body ran for an already-recorded version")
	}
}

// TestMigrateFS_RejectsDuplicateVersions pins the guard that replaces the
// accidental one applyMigration's skip removed. Two files sharing a version
// prefix is an authoring mistake: the second would be silently skipped as
// "already applied" and the schema would diverge from the migration set with
// no error anywhere.
func TestMigrateFS_RejectsDuplicateVersions(t *testing.T) {
	ctx := context.Background()

	mapFS := fstest.MapFS{
		"mig/001_a.sql": &fstest.MapFile{Data: []byte(`CREATE TABLE a (id INTEGER PRIMARY KEY);`)},
		"mig/001_b.sql": &fstest.MapFile{Data: []byte(`CREATE TABLE b (id INTEGER PRIMARY KEY);`)},
	}

	d, err := sql.Open("sqlite", ":memory:")
	if err != nil {
		t.Fatalf("open: %v", err)
	}
	t.Cleanup(func() { _ = d.Close() })

	err = migrateFS(ctx, d, mapFS, "mig")
	if err == nil {
		t.Fatalf("migrateFS accepted two migrations with version 1")
	}
	if !strings.Contains(err.Error(), "duplicate migration version") {
		t.Errorf("error = %v, want it to name the duplicate version", err)
	}
}
