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

func migrate(ctx context.Context, d *sql.DB) error {
	// Create schema_version table if not exists
	_, err := d.ExecContext(ctx, `CREATE TABLE IF NOT EXISTS schema_version (version INTEGER NOT NULL)`)
	if err != nil {
		return fmt.Errorf("create schema_version: %w", err)
	}

	// Get current version
	var currentVersion int
	err = d.QueryRowContext(ctx, `SELECT COALESCE(MAX(version), 0) FROM schema_version`).Scan(&currentVersion)
	if err != nil {
		return fmt.Errorf("get schema version: %w", err)
	}

	// List migration files
	entries, err := fs.ReadDir(migrationsFS, "migrations")
	if err != nil {
		return fmt.Errorf("read migrations dir: %w", err)
	}

	// Sort by name (already numerically ordered by convention)
	sort.Slice(entries, func(i, j int) bool {
		return entries[i].Name() < entries[j].Name()
	})

	for _, entry := range entries {
		if !strings.HasSuffix(entry.Name(), ".sql") {
			continue
		}
		// Extract version number from filename like "001_init.sql"
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

		data, err := migrationsFS.ReadFile("migrations/" + entry.Name())
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
