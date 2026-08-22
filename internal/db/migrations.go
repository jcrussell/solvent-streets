package db

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"io/fs"
	"sort"
	"strconv"
	"strings"
)

// migrate applies SQL migrations from migrationsFS in ascending numeric
// version order, skipping those already recorded in schema_version.
// Production callers use migrate(); tests can use migrateFS to inject an
// fstest.MapFS for hermetic migration scenarios without touching disk or
// the embed.
func migrate(ctx context.Context, d *sql.DB) error {
	return migrateFS(ctx, d, migrationsFS, "migrations")
}

// migrateFS is the fs.FS-parametrized form of migrate. Exposes the
// filesystem seam for tests; production code calls migrate.
//
// Each migration's data exec + schema_version insert runs inside a single
// BeginTx so a partial failure (e.g. statement N of a multi-statement
// migration errors after statement N-1 has executed) rolls back cleanly
// instead of leaving the schema half-applied. modernc.org/sqlite runs each
// statement in its own implicit txn otherwise, which is unsafe for the
// kind of CREATE/INSERT/DROP/RENAME sequences migrations contain.
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

	// Parse the numeric version prefix up front, then sort by the parsed
	// integer (ascending) rather than lexically. Lexical order would run
	// "10_*.sql" before "2_*.sql", applying dependency-ordered DDL
	// backwards. Entries without a ".sql" suffix or a valid numeric
	// prefix are dropped here — the same filter the old inline gate
	// applied via SplitN + strconv.Atoi, just hoisted before the sort.
	type pending struct {
		version int
		name    string
	}
	var migrations []pending
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
		migrations = append(migrations, pending{version: version, name: entry.Name()})
	}

	sort.Slice(migrations, func(i, j int) bool {
		return migrations[i].version < migrations[j].version
	})

	// Two files sharing a version prefix is an authoring mistake, and it has to
	// fail here. applyMigration skips any version already recorded (see the
	// probe there), so the second file would otherwise never execute and
	// nothing would say so — the schema would silently diverge from the
	// migration set. Previously the duplicate INSERT tripped 002's unique
	// index, which was loud but only by accident and only after 002 existed.
	for i := 1; i < len(migrations); i++ {
		if migrations[i].version == migrations[i-1].version {
			return fmt.Errorf("duplicate migration version %d: %s and %s",
				migrations[i].version, migrations[i-1].name, migrations[i].name)
		}
	}

	for _, m := range migrations {
		if m.version <= currentVersion {
			continue
		}

		data, err := fs.ReadFile(source, root+"/"+m.name)
		if err != nil {
			return fmt.Errorf("read migration %s: %w", m.name, err)
		}

		if err := applyMigration(ctx, d, m.name, m.version, string(data)); err != nil {
			return err
		}
	}

	return nil
}

func applyMigration(ctx context.Context, d *sql.DB, name string, version int, data string) (retErr error) {
	tx, err := d.BeginTx(ctx, nil)
	if err != nil {
		return fmt.Errorf("begin migration %s: %w", name, err)
	}
	defer func() {
		if retErr != nil {
			if rbErr := tx.Rollback(); rbErr != nil && !errors.Is(rbErr, sql.ErrTxDone) {
				retErr = errors.Join(retErr, fmt.Errorf("rollback migration %s: %w", name, rbErr))
			}
		}
	}()

	// Re-read inside the transaction. migrateFS's MAX(version) probe runs
	// outside any transaction, so two processes opening the same fresh
	// database can both observe version 0 and both try to apply the same
	// migration. 001_init.sql is entirely IF NOT EXISTS, which makes the
	// duplicate apply a silent no-op and the duplicate INSERT below succeed --
	// leaving two rows for one migration, which 002's unique index then
	// cannot be created over.
	//
	// This probe takes the transaction's read snapshot before any write, so in
	// WAL mode the loser of a race fails its write with a snapshot/busy error
	// instead of silently duplicating. SetMaxOpenConns(1) only serializes
	// within one process, so it does not cover this on its own.
	var applied int
	if err := tx.QueryRowContext(ctx,
		`SELECT COUNT(*) FROM schema_version WHERE version = ?`, version).Scan(&applied); err != nil {
		return fmt.Errorf("check migration %s: %w", name, err)
	}
	if applied > 0 {
		if err := tx.Rollback(); err != nil && !errors.Is(err, sql.ErrTxDone) {
			return fmt.Errorf("rollback migration %s: %w", name, err)
		}
		return nil
	}

	if _, err := tx.ExecContext(ctx, data); err != nil {
		return fmt.Errorf("exec migration %s: %w", name, err)
	}
	if _, err := tx.ExecContext(ctx, `INSERT INTO schema_version (version) VALUES (?)`, version); err != nil {
		return fmt.Errorf("record migration %s: %w", name, err)
	}

	if err := tx.Commit(); err != nil {
		return fmt.Errorf("commit migration %s: %w", name, err)
	}
	return nil
}
