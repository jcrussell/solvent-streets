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
// the hex_stats auto-increment id (so multiple snapshots can coexist for
// the same hex/resource/city), and the lookup indexes the query planner
// depends on.
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
			"cohort_stats",
			"compute_results",
			"features",
			"hex_metrics",
			"hex_stats",
			"runs",
			"schema_version",
			"snapshot_parts",
			"snapshots",
		}
		got := listTables(t, ctx, root.db)
		for _, name := range want {
			if !slices.Contains(got, name) {
				t.Errorf("missing table %q; got tables %v", name, got)
			}
		}
		// Forecasts are simulated on demand from cohort_stats, never stored.
		if slices.Contains(got, "forecast_results") {
			t.Error("forecast_results exists; nothing writes or reads stored forecasts")
		}
	})

	t.Run("city-scoped tables FK to cities(id)", func(t *testing.T) {
		// Every table that holds per-city data must enforce cities(id) so
		// orphaned rows can't accumulate when a city is removed.
		scoped := []string{
			"city_boundaries",
			"features",
			"runs",
			"snapshots",
			"compute_results",
			"hex_stats",
			"cohort_stats",
			"hex_metrics",
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

	t.Run("hex_stats has auto-increment id PK", func(t *testing.T) {
		// hex_stats uses an auto-increment id so multiple snapshots can
		// coexist for the same (hex_id, resource_type, city_id). Reverting
		// to a composite PK on those three columns would silently re-introduce
		// the snapshot-overwrite bug.
		cols := listColumns(t, ctx, root.db, "hex_stats")
		var id *columnInfo
		for i := range cols {
			if cols[i].name == "id" {
				id = &cols[i]
				break
			}
		}
		if id == nil {
			t.Fatalf("hex_stats missing id column; got %+v", cols)
		}
		if id.pk != 1 {
			t.Errorf("hex_stats.id pk = %d; want 1 (primary key)", id.pk)
		}
		if id.typ != "INTEGER" {
			t.Errorf("hex_stats.id type = %q; want INTEGER (auto-increment)", id.typ)
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

	t.Run("estimated_spaces is on compute_results only", func(t *testing.T) {
		hasSpaces := func(table string) bool {
			return slices.ContainsFunc(listColumns(t, ctx, root.db, table), func(c columnInfo) bool { return c.name == "estimated_spaces" })
		}
		if !hasSpaces("compute_results") {
			t.Error("compute_results missing estimated_spaces column")
		}
		// Per-hex spaces are hex_metrics rows (resource.SpacesMetricKey).
		if hasSpaces("hex_stats") {
			t.Error("hex_stats must not carry estimated_spaces; per-hex spaces live in hex_metrics")
		}
	})

	t.Run("query-planner indexes exist", func(t *testing.T) {
		// These are the indexes the runtime path depends on. Dropping any
		// would silently turn a hot read into a table scan.
		want := map[string][]string{
			"features":        {"idx_features_city_type", "idx_features_city_type_time"},
			"hex_stats":       {"idx_hex_stats_city_type_run", "idx_hex_stats_run"},
			"compute_results": {"idx_compute_results_city_type_run", "idx_compute_results_run"},
			"cohort_stats":    {"idx_cohort_stats_city_type_run", "idx_cohort_stats_run"},
			"hex_metrics":     {"idx_hex_metrics_city_metric_run", "idx_hex_metrics_run"},
			"snapshots":       {"idx_snapshots_city_hash"},
			"snapshot_parts":  {"idx_snapshot_parts_run"},
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
	for _, table := range resultTables {
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
			return fk.from == "run_id" && fk.table == "runs" && fk.to == "id"
		}) {
			t.Errorf("%s missing FK run_id -> runs(id)", table)
		}
	}
}

// TestQueryPlan_UsesCompositeIndexes is a regression guard for the
// (city_id, key, run_id) composite indexes. Every result read has the
// viewQuery shape — `city_id = ? AND key IN (...) AND run_id IN (...)` — and
// must seek the composite, not scan all of a city's rows across every run.
// The snapshot resolution and the run-reference checks must seek too.
func TestQueryPlan_UsesCompositeIndexes(t *testing.T) {
	ctx := context.Background()

	root, err := Open(":memory:")
	if err != nil {
		t.Fatalf("Open(:memory:): %v", err)
	}
	t.Cleanup(func() { _ = root.Close() })

	view := func(cols, table, key string) string {
		return `SELECT ` + cols + ` FROM ` + table + ` WHERE city_id = ? AND ` + key + ` IN (?, ?) AND run_id IN (?, ?, ?)`
	}
	cases := []struct {
		name      string
		query     string
		wantIndex string
	}{
		{"hex_stats view read", view("hex_id", "hex_stats", "resource_type"), "idx_hex_stats_city_type_run"},
		{"compute_results view read", view("id", "compute_results", "resource_type") + ` ORDER BY computed_at DESC, id DESC LIMIT 1`, "idx_compute_results_city_type_run"},
		{"cohort_stats view read", view("classification", "cohort_stats", "resource_type"), "idx_cohort_stats_city_type_run"},
		{"hex_metrics view read", view("hex_id", "hex_metrics", "metric"), "idx_hex_metrics_city_metric_run"},
		{"features outer filter", `SELECT id FROM features WHERE resource_type = ? AND city_id = ?`, "idx_features_city_type"},
		{"features last ingest lookup", `SELECT fetched_at FROM features WHERE resource_type = ? AND city_id = ? ORDER BY fetched_at DESC LIMIT 1`, "idx_features_city_type_time"},
		{"newest snapshot for a hash", `SELECT id FROM snapshots WHERE city_id = ? AND config_hash = ? ORDER BY id DESC LIMIT 1`, "idx_snapshots_city_hash"},
		{"run still referenced", `SELECT COUNT(*) FROM snapshot_parts WHERE run_id = ?`, "idx_snapshot_parts_run"},
		{"delete a run's rows", `DELETE FROM hex_stats WHERE run_id IN (?, ?)`, "idx_hex_stats_run"},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			plan := queryPlan(t, ctx, root.db, tc.query)
			// Accept both "USING INDEX" and "USING COVERING INDEX" — the
			// latter (an index-only plan) is strictly better and is what the
			// MAX(snapshot_id) subqueries resolve to.
			if !strings.Contains(plan, "INDEX "+tc.wantIndex) {
				t.Errorf("query %q\nplan:\n%s\nwant INDEX %s", tc.query, plan, tc.wantIndex)
			}
			for _, table := range []string{"hex_stats", "features", "compute_results", "cohort_stats", "hex_metrics", "snapshots", "snapshot_parts"} {
				if strings.Contains(plan, "SCAN "+table+"\n") {
					t.Errorf("query %q falls back to a full table scan:\n%s", tc.query, plan)
				}
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
	from, table, to string
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
	rows, err := d.QueryContext(ctx, `SELECT "from", "table", "to" FROM pragma_foreign_key_list(?)`, table)
	if err != nil {
		t.Fatalf("pragma_foreign_key_list(%q): %v", table, err)
	}
	defer func() { _ = rows.Close() }()
	var fks []foreignKey
	for rows.Next() {
		var fk foreignKey
		if err := rows.Scan(&fk.from, &fk.table, &fk.to); err != nil {
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
// shape an older pvmt left behind. It uses Open's own pragmas (WAL and so
// on), as every older pvmt did, so the file starts in the state a real
// install's database is in.
func openRaw(t *testing.T, path string) *sql.DB {
	t.Helper()
	d, err := sql.Open("sqlite", path+dsnPragmas)
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
//
// Generation 2 is the pre-release schema that still stored forecasts
// (forecast_results, forecast:<resource> snapshot parts); generation 3 dropped
// them by editing 001_init.sql in place, so a generation-2 database records
// schema_version 1 too and must be refused the same way.
func TestOpen_RefusesOlderGeneration(t *testing.T) {
	for _, tc := range []struct {
		name     string
		versions []int
		gen      int // PRAGMA user_version stamped on the seeded file; 0 = never stamped
	}{
		{"v0.4.1 (001, 002)", []int{1, 2}, 0},
		{"pre-release HEAD (001, 002, 003)", []int{1, 2, 3}, 0},
		{"pre-release generation 2 (stored forecasts)", []int{1}, 2},
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
				if _, err := d.ExecContext(ctx, fmt.Sprintf(`CREATE TABLE forecast_results (id INTEGER PRIMARY KEY);
					PRAGMA user_version = %d;`, tc.gen)); err != nil {
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

// TestOpen_RefusesNewerGeneration pins the downgrade direction: a database
// stamped by a newer pvmt is refused, not reinterpreted.
func TestOpen_RefusesNewerGeneration(t *testing.T) {
	ctx := context.Background()
	path := filepath.Join(t.TempDir(), "pvmt.db")
	d := openRaw(t, path)
	if _, err := d.ExecContext(ctx, fmt.Sprintf(`CREATE TABLE cities (id INTEGER PRIMARY KEY); PRAGMA user_version = %d;`, schemaGeneration+1)); err != nil {
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
