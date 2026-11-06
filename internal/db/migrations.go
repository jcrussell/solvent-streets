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

// schemaGeneration identifies the schema 001_init.sql creates, stamped into
// the database as PRAGMA user_version. Numbered migrations evolve a database
// WITHIN a generation; a new generation is a deliberate squash of the
// migration files (a breaking change). Bump this whenever 001_init.sql is
// edited in place, because databases from the previous generation already
// record schema_version 1 and would otherwise skip the edited file and run on
// the old schema. Generation 0 is every database created before generations
// existed (v0.4.1 and earlier, plus pre-release HEADs with migration 003).
const schemaGeneration = 2

// ErrIncompatibleDatabase means the database was created by a pvmt whose
// schema generation differs from this one. It is never migrated or deleted
// automatically: the operator moves it aside and re-ingests.
var ErrIncompatibleDatabase = errors.New("incompatible database")

// migrate refuses a database from another schema generation, then applies SQL
// migrations from migrationsFS in ascending numeric version order, skipping
// those already recorded in schema_version. Production callers use migrate();
// tests can use migrateFS to inject an fstest.MapFS for hermetic migration
// scenarios without touching disk or the embed.
func migrate(ctx context.Context, d *sql.DB) error {
	if err := checkGeneration(ctx, d); err != nil {
		return err
	}
	return migrateFS(ctx, d, migrationsFS, "migrations")
}

// checkGeneration stamps an empty database with schemaGeneration and accepts
// one already stamped with it. Anything else is ErrIncompatibleDatabase: a
// database holding tables under another generation must not reach migrateFS,
// which would see schema_version 1 recorded and skip 001_init.sql entirely.
func checkGeneration(ctx context.Context, d *sql.DB) error {
	var gen int
	if err := d.QueryRowContext(ctx, `PRAGMA user_version`).Scan(&gen); err != nil {
		return fmt.Errorf("read schema generation: %w", err)
	}
	if gen == schemaGeneration {
		return nil
	}
	var tables int
	if err := d.QueryRowContext(ctx,
		`SELECT COUNT(*) FROM sqlite_master WHERE type = 'table' AND name NOT LIKE 'sqlite_%'`).Scan(&tables); err != nil {
		return fmt.Errorf("inspect database: %w", err)
	}
	if gen == 0 && tables == 0 {
		// PRAGMA takes no bound parameters; schemaGeneration is a constant.
		if _, err := d.ExecContext(ctx, fmt.Sprintf(`PRAGMA user_version = %d`, schemaGeneration)); err != nil {
			return fmt.Errorf("stamp schema generation: %w", err)
		}
		return nil
	}
	if gen > schemaGeneration {
		return fmt.Errorf("%w: schema generation %d is newer than this pvmt's %d (upgrade pvmt)",
			ErrIncompatibleDatabase, gen, schemaGeneration)
	}
	return fmt.Errorf("%w: schema generation %d predates this pvmt's %d",
		ErrIncompatibleDatabase, gen, schemaGeneration)
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
	currentVersion, err := ensureSchemaVersion(ctx, d)
	if err != nil {
		return err
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
	// migration set. The schema_version unique index would trip on the
	// duplicate INSERT, but only by accident and with a misleading error.
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

// ensureSchemaVersion creates the schema_version bookkeeping table and its
// one-row-per-migration index, and returns the highest recorded version. The
// index is enforced from the start: a concurrent first run that loses the
// race fails its INSERT loudly instead of recording a duplicate (see the
// in-transaction probe in applyMigration).
func ensureSchemaVersion(ctx context.Context, d *sql.DB) (int, error) {
	if _, err := d.ExecContext(ctx, `CREATE TABLE IF NOT EXISTS schema_version (version INTEGER NOT NULL)`); err != nil {
		return 0, fmt.Errorf("create schema_version: %w", err)
	}
	if _, err := d.ExecContext(ctx, `CREATE UNIQUE INDEX IF NOT EXISTS idx_schema_version_version ON schema_version(version)`); err != nil {
		return 0, fmt.Errorf("index schema_version: %w", err)
	}
	var version int
	if err := d.QueryRowContext(ctx, `SELECT COALESCE(MAX(version), 0) FROM schema_version`).Scan(&version); err != nil {
		return 0, fmt.Errorf("get schema version: %w", err)
	}
	return version, nil
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
	// duplicate apply a silent no-op; without this probe the duplicate INSERT
	// below would fail on the schema_version unique index instead.
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
