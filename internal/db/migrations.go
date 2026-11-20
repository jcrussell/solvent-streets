package db

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"io/fs"
	"os"
	"path/filepath"
	"sort"
	"strconv"
	"strings"
)

// schemaGeneration identifies the schema 001_init.sql creates, stamped into
// the database as PRAGMA user_version. Numbered migrations evolve a database
// WITHIN a generation; a new generation is a deliberate squash of the
// migration files (a breaking change). 001_init.sql is frozen: every schema
// change is an additive numbered migration (002_*.sql onward) that migrateFS
// applies. A squash would edit 001 in place and must bump this, because
// databases from the previous generation already record schema_version 1 and
// would otherwise skip the edited file and run on the old schema. Generation 0
// is every database created before generations existed (v0.4.1 and earlier).
// Generation 1 is the v0.5.0 baseline; databases built from pre-release
// revisions of 001_init.sql are unsupported and must be rebuilt.
const schemaGeneration = 1

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
	if err := enableWAL(ctx, d); err != nil {
		return err
	}
	return migrateFS(ctx, d, migrationsFS, "migrations")
}

// enableWAL switches an accepted database to WAL. It runs only after
// checkGeneration, so a refused file keeps its journal mode and its bytes
// (solvent-streets-cmth). The mode is persistent in the file, so later
// connections inherit it; an in-memory database reports "memory" and stays
// that way, which is fine.
func enableWAL(ctx context.Context, d *sql.DB) error {
	var mode string
	if err := d.QueryRowContext(ctx, `PRAGMA journal_mode = WAL`).Scan(&mode); err != nil {
		return fmt.Errorf("enable WAL: %w", err)
	}
	if !strings.EqualFold(mode, "wal") && !strings.EqualFold(mode, "memory") {
		return fmt.Errorf("enable WAL: journal_mode is %q", mode)
	}
	return nil
}

// checkGeneration stamps an empty database with schemaGeneration and accepts
// one already stamped with it. Anything else is ErrIncompatibleDatabase: a
// database holding tables under another generation must not reach migrateFS,
// which would see schema_version 1 recorded and skip 001_init.sql entirely.
func checkGeneration(ctx context.Context, d *sql.DB) error {
	gen, tables, err := readGeneration(ctx, d)
	if err != nil {
		return err
	}
	if !acceptable(gen, tables) {
		// About to refuse. Another process may be creating this database
		// right now: it stamps the generation, then creates the tables, and
		// the two reads above can straddle that, seeing generation 0 beside
		// its first tables. Read again under a write transaction (the DSN's
		// _txlock=immediate), which waits for that writer, and refuse only
		// if the answer holds. Only this path takes the lock, so opening a
		// healthy database never waits behind a long ingest.
		tx, err := d.BeginTx(ctx, nil)
		if err != nil {
			return fmt.Errorf("read schema generation: %w", err)
		}
		gen, tables, err = readGeneration(ctx, tx)
		_ = tx.Rollback()
		if err != nil {
			return err
		}
	}
	if gen == schemaGeneration {
		return nil
	}
	if acceptable(gen, tables) {
		// PRAGMA takes no bound parameters; schemaGeneration is a constant.
		if _, err := d.ExecContext(ctx, fmt.Sprintf(`PRAGMA user_version = %d`, schemaGeneration)); err != nil {
			return fmt.Errorf("stamp schema generation: %w", err)
		}
		return nil
	}
	return refusal(ctx, d, gen)
}

// acceptable reports whether checkGeneration takes a file of generation gen
// holding tables tables: one stamped with schemaGeneration, or an empty one,
// which it stamps.
func acceptable(gen, tables int) bool {
	return gen == schemaGeneration || (gen == 0 && tables == 0)
}

// probeGeneration reads the generation of the database at path on a
// read-only connection and returns checkGeneration's refusal for a file it
// would refuse, so Open can refuse it without ever opening it read-write.
// Closing a read-write connection checkpoints a hot WAL into the main file
// and deletes -wal and -shm; a read-only connection cannot, so the refused
// file stays byte-identical. It returns nil when the file is missing, cannot
// be read read-only, or would be accepted: the read-write path then decides
// as it always has. Both reads run in one transaction, so a database another
// process is creating (stamp, then tables) is never seen half-made.
func probeGeneration(ctx context.Context, path string) error {
	if path == ":memory:" {
		return nil
	}
	if _, err := os.Stat(path); err != nil {
		return nil
	}
	// A file: URI, so SQLite applies mode=ro. '%' and '#' are its escape and
	// fragment characters; Open has already refused a path holding '?'.
	uri := "file:" + strings.NewReplacer("%", "%25", "#", "%23").Replace(filepath.ToSlash(path)) +
		"?mode=ro&_pragma=busy_timeout(5000)"
	d, err := sql.Open("sqlite", uri)
	if err != nil {
		return nil
	}
	defer func() { _ = d.Close() }()
	tx, err := d.BeginTx(ctx, &sql.TxOptions{ReadOnly: true})
	if err != nil {
		return nil
	}
	defer func() { _ = tx.Rollback() }()
	gen, tables, err := readGeneration(ctx, tx)
	if err != nil || acceptable(gen, tables) {
		return nil
	}
	if err := refusal(ctx, tx, gen); errors.Is(err, ErrIncompatibleDatabase) {
		return err
	}
	return nil
}

// refusal is the ErrIncompatibleDatabase for a file of generation gen that
// checkGeneration does not accept, read through q.
func refusal(ctx context.Context, q rowQueryer, gen int) error {
	// Every pvmt database, of any generation, has the schema_version table.
	// A file with tables but without it is some other program's SQLite
	// file, and calling it "generation 0" sends the operator to re-ingest
	// over a file that was never pvmt's (solvent-streets-cmth).
	var hasVersions int
	if err := q.QueryRowContext(ctx,
		`SELECT COUNT(*) FROM sqlite_master WHERE type = 'table' AND name = 'schema_version'`).Scan(&hasVersions); err != nil {
		return fmt.Errorf("inspect database: %w", err)
	}
	if hasVersions == 0 {
		return fmt.Errorf("%w: not a pvmt database (it has tables but no schema_version table)",
			ErrIncompatibleDatabase)
	}
	if gen > schemaGeneration {
		return fmt.Errorf("%w: schema generation %d is newer than this pvmt's %d (upgrade pvmt)",
			ErrIncompatibleDatabase, gen, schemaGeneration)
	}
	return fmt.Errorf("%w: schema generation %d predates this pvmt's %d",
		ErrIncompatibleDatabase, gen, schemaGeneration)
}

// rowQueryer is what readGeneration needs: a *sql.DB or a *sql.Tx.
type rowQueryer interface {
	QueryRowContext(ctx context.Context, query string, args ...any) *sql.Row
}

// readGeneration returns the file's schema generation (PRAGMA user_version)
// and how many non-internal tables it holds.
func readGeneration(ctx context.Context, q rowQueryer) (gen, tables int, err error) {
	if err := q.QueryRowContext(ctx, `PRAGMA user_version`).Scan(&gen); err != nil {
		return 0, 0, fmt.Errorf("read schema generation: %w", err)
	}
	if err := q.QueryRowContext(ctx,
		`SELECT COUNT(*) FROM sqlite_master WHERE type = 'table' AND name NOT LIKE 'sqlite_%'`).Scan(&tables); err != nil {
		return 0, 0, fmt.Errorf("inspect database: %w", err)
	}
	return gen, tables, nil
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
	// prefix are dropped here, before the sort.
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
