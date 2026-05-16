package db

import (
	"context"
	"database/sql"
	"slices"
	"testing"
	"testing/fstest"
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
// the hex_stats auto-increment id introduced by 002, and the lookup indexes
// the query planner depends on.
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
			"forecast_results",
			"hex_stats",
			"schema_version",
			"snapshots",
		}
		got := listTables(t, ctx, root.db)
		for _, name := range want {
			if !slices.Contains(got, name) {
				t.Errorf("missing table %q; got tables %v", name, got)
			}
		}
	})

	t.Run("city-scoped tables FK to cities(id)", func(t *testing.T) {
		// Every table that holds per-city data must enforce cities(id) so
		// orphaned rows can't accumulate when a city is removed.
		scoped := []string{
			"city_boundaries",
			"features",
			"snapshots",
			"compute_results",
			"hex_stats",
			"forecast_results",
			"cohort_stats",
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
		// 002_snapshot_history.sql rebuilt hex_stats to make multiple
		// snapshots coexist for the same (hex_id, resource_type, city_id).
		// Reverting to the original composite PK would silently re-introduce
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

	t.Run("query-planner indexes exist", func(t *testing.T) {
		// These are the indexes the runtime path depends on. Dropping any
		// would silently turn a hot read into a table scan.
		want := map[string][]string{
			"features":         {"idx_features_resource_type", "idx_features_city"},
			"hex_stats":        {"idx_hex_stats_lookup", "idx_hex_stats_city"},
			"compute_results":  {"idx_compute_results_resource_type", "idx_compute_results_city"},
			"forecast_results": {"idx_forecast_results_type", "idx_forecast_results_snapshot", "idx_forecast_results_city"},
			"cohort_stats":     {"idx_cohort_stats_city", "idx_cohort_stats_resource_type"},
			"snapshots":        {"idx_snapshots_city"},
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

type columnInfo struct {
	name string
	typ  string
	pk   int
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
	rows, err := d.QueryContext(ctx, `SELECT name, type, pk FROM pragma_table_info(?)`, table)
	if err != nil {
		t.Fatalf("pragma_table_info(%q): %v", table, err)
	}
	defer func() { _ = rows.Close() }()
	var cols []columnInfo
	for rows.Next() {
		var c columnInfo
		if err := rows.Scan(&c.name, &c.typ, &c.pk); err != nil {
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
