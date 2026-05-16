package db

import (
	"context"
	"database/sql"
	"fmt"
	"io/fs"
	"sort"
	"strconv"
	"strings"
)

// migrate applies SQL migrations from migrationsFS in lexical order,
// skipping those already recorded in schema_version. Production callers
// use migrate(); tests can use migrateFS to inject an fstest.MapFS for
// hermetic migration scenarios without touching disk or the embed.
func migrate(ctx context.Context, d *sql.DB) error {
	return migrateFS(ctx, d, migrationsFS, "migrations")
}

// migrateFS is the fs.FS-parametrized form of migrate. Exposes the
// filesystem seam for tests; production code calls migrate.
func migrateFS(ctx context.Context, d *sql.DB, source fs.FS, root string) error {
	if _, err := d.ExecContext(ctx, `CREATE TABLE IF NOT EXISTS schema_version (version INTEGER NOT NULL)`); err != nil {
		return fmt.Errorf("create schema_version: %w", err)
	}

	var currentVersion int
	if err := d.QueryRowContext(ctx, `SELECT COALESCE(MAX(version), 0) FROM schema_version`).Scan(&currentVersion); err != nil {
		return fmt.Errorf("get schema version: %w", err)
	}

	entries, err := fs.ReadDir(source, root)
	if err != nil {
		return fmt.Errorf("read migrations dir: %w", err)
	}

	sort.Slice(entries, func(i, j int) bool {
		return entries[i].Name() < entries[j].Name()
	})

	for _, entry := range entries {
		if !strings.HasSuffix(entry.Name(), ".sql") {
			continue
		}
		parts := strings.SplitN(entry.Name(), "_", 2)
		if len(parts) < 2 {
			continue
		}
		version, err := strconv.Atoi(parts[0])
		if err != nil {
			continue
		}

		if version <= currentVersion {
			continue
		}

		data, err := fs.ReadFile(source, root+"/"+entry.Name())
		if err != nil {
			return fmt.Errorf("read migration %s: %w", entry.Name(), err)
		}

		if _, err := d.ExecContext(ctx, string(data)); err != nil {
			return fmt.Errorf("exec migration %s: %w", entry.Name(), err)
		}

		if _, err := d.ExecContext(ctx, `INSERT INTO schema_version (version) VALUES (?)`, version); err != nil {
			return fmt.Errorf("record migration %s: %w", entry.Name(), err)
		}
	}

	return nil
}
