package db

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"maps"
	"slices"
	"strings"
	"time"

	"modernc.org/sqlite"
	sqlite3 "modernc.org/sqlite/lib"
)

// OrphanRunGrace is how old an unreferenced run must be before gc treats it
// as abandoned. A run is unreferenced from CreateRun until its command
// commits, and one city of `pvmt all compute` holds its first run until every
// later step (parking, sidewalks, the combined pass, coverage, census) is
// done, which for a large city can take hours. A day is far beyond any real
// compute, and an orphan only costs disk until then.
const OrphanRunGrace = 24 * time.Hour

// commitBusyBudget bounds how long CommitSnapshot keeps retrying while another
// process holds the write lock past busy_timeout. Giving up discards the
// command's runs, possibly hours of compute, so it waits generously.
const commitBusyBudget = 2 * time.Minute

// resultTables is the canonical set of tables whose rows carry a run_id.
// Every entry is a compile-time literal, never user input, so interpolating
// it into SQL is safe (gosec G202 does not apply).
var resultTables = []string{
	"compute_results",
	"hex_stats",
	"cohort_stats",
	"hex_metrics",
}

// WithSnapshot returns a view that reads exactly the given snapshot. A
// snapshotID of 0 returns an unpinned view. A pin wins over WithConfigHash
// (it is the more specific request, matching the server's ?snapshot=N).
// The underlying DB connection is shared.
func (s *sqliteStore) WithSnapshot(snapshotID int64) Store {
	cp := *s
	cp.snapshotID = snapshotID
	return &cp
}

// WithConfigHash returns a view that reads the newest snapshot committed
// under configHash. Export and serve use it so a city shared by two configs
// at different hex_edge_m never reads the other config's (incompatible)
// hex_id namespace. An empty hash reads the newest snapshot of any hash.
func (s *sqliteStore) WithConfigHash(configHash string) Store {
	cp := *s
	cp.configHash = configHash
	return &cp
}

// WithParts returns a view of the newest snapshot for configHash with parts
// overlaid (a run id of 0 removes the part), exactly as CommitSnapshot would
// apply them. The map is copied, so later changes to parts are not seen.
func (s *sqliteStore) WithParts(configHash string, parts map[string]int64) Store {
	cp := *s
	cp.snapshotID = 0
	cp.configHash = configHash
	cp.overlay = maps.Clone(parts)
	return &cp
}

// resolvedSnapshotID returns the snapshot this view reads: the pinned id, else
// the newest snapshot for the view's config hash (any hash when unset). It
// returns 0 when the city has no such snapshot.
func (s *sqliteStore) resolvedSnapshotID(ctx context.Context, q queryer) (int64, error) {
	if s.snapshotID > 0 {
		return s.snapshotID, nil
	}
	return newestSnapshotID(ctx, q, s.cityID, s.configHash)
}

// queryer is the read surface shared by *sql.DB and *sql.Tx.
type queryer interface {
	QueryContext(ctx context.Context, query string, args ...any) (*sql.Rows, error)
	QueryRowContext(ctx context.Context, query string, args ...any) *sql.Row
}

// newestSnapshotID is the newest snapshot for the city, restricted to
// configHash when it is non-empty. Ordered by id, which is monotonic, rather
// than created_at, which has second granularity. 0 when none.
func newestSnapshotID(ctx context.Context, q queryer, cityID int64, configHash string) (int64, error) {
	query := `SELECT id FROM snapshots WHERE city_id = ? ORDER BY id DESC LIMIT 1`
	args := []any{cityID}
	if configHash != "" {
		query = `SELECT id FROM snapshots WHERE city_id = ? AND config_hash = ? ORDER BY id DESC LIMIT 1`
		args = append(args, configHash)
	}
	var id int64
	switch err := q.QueryRowContext(ctx, query, args...).Scan(&id); {
	case errors.Is(err, sql.ErrNoRows):
		return 0, nil
	case err != nil:
		return 0, fmt.Errorf("resolve snapshot: %w", err)
	}
	return id, nil
}

// snapshotParts returns a snapshot's manifest as part -> run id. An id of 0
// (no snapshot) returns an empty map.
func snapshotParts(ctx context.Context, q queryer, cityID, snapshotID int64) (map[string]int64, error) {
	parts := map[string]int64{}
	if snapshotID == 0 {
		return parts, nil
	}
	rows, err := q.QueryContext(ctx,
		`SELECT p.part, p.run_id FROM snapshot_parts p JOIN snapshots s ON s.id = p.snapshot_id
		 WHERE p.snapshot_id = ? AND s.city_id = ?`, snapshotID, cityID)
	if err != nil {
		return nil, fmt.Errorf("query snapshot parts: %w", err)
	}
	defer func() { _ = rows.Close() }()
	for rows.Next() {
		var part string
		var run int64
		if err := rows.Scan(&part, &run); err != nil {
			return nil, fmt.Errorf("scan snapshot part: %w", err)
		}
		parts[part] = run
	}
	return parts, rows.Err()
}

// applyParts overlays parts onto base in place: a run id of 0 removes the
// part, anything else sets it.
func applyParts(base, parts map[string]int64) {
	for part, run := range parts {
		if run == 0 {
			delete(base, part)
			continue
		}
		base[part] = run
	}
}

// viewRunIDs returns the runs this view reads: the resolved snapshot's
// manifest with the WithParts overlay applied, sorted for stable SQL.
func (s *sqliteStore) viewRunIDs(ctx context.Context) ([]int64, error) {
	snap, err := s.resolvedSnapshotID(ctx, s.db)
	if err != nil {
		return nil, err
	}
	parts, err := snapshotParts(ctx, s.db, s.cityID, snap)
	if err != nil {
		return nil, err
	}
	applyParts(parts, s.overlay)
	return slices.Sorted(maps.Values(parts)), nil
}

// viewQuery builds `selectClause WHERE city_id = ? AND keyColumn IN (keys)
// AND run_id IN (<this view's runs>) suffix` and its binds. ok is false when
// the view reads no runs (the city has no snapshot yet), in which case
// callers return an empty result without querying. keyColumn is always a
// compile-time literal at the call site, never user input.
func (s *sqliteStore) viewQuery(ctx context.Context, selectClause, keyColumn string, keys []string, suffix string) (q string, args []any, ok bool, err error) {
	runs, err := s.viewRunIDs(ctx)
	if err != nil || len(runs) == 0 || len(keys) == 0 {
		return "", nil, false, err
	}
	args = make([]any, 0, 1+len(keys)+len(runs))
	args = append(args, s.cityID)
	for _, k := range keys {
		args = append(args, k)
	}
	for _, r := range runs {
		args = append(args, r)
	}
	q = selectClause + ` WHERE city_id = ? AND ` + keyColumn + ` IN (` + placeholders(len(keys)) +
		`) AND run_id IN (` + placeholders(len(runs)) + `)` + suffix
	return q, args, true, nil
}

// placeholders returns "?,?,...,?" with n entries (n > 0).
func placeholders(n int) string {
	return strings.TrimSuffix(strings.Repeat("?,", n), ",")
}

// CreateRun records a new run. It is invisible to readers until a snapshot
// references it.
func (s *sqliteStore) CreateRun(ctx context.Context, producer, configHash string) (*Run, error) {
	if producer == "" {
		return nil, errors.New("create run: producer is required")
	}
	res, err := s.db.ExecContext(ctx,
		`INSERT INTO runs (city_id, producer, config_hash) VALUES (?, ?, ?)`, s.cityID, producer, configHash)
	if err != nil {
		return nil, fmt.Errorf("create run: %w", err)
	}
	id, err := res.LastInsertId()
	if err != nil {
		return nil, fmt.Errorf("create run id: %w", err)
	}
	run := &Run{ID: id, Producer: producer, ConfigHash: configHash}
	if err := s.db.QueryRowContext(ctx, `SELECT computed_at FROM runs WHERE id = ?`, id).Scan(&run.ComputedAt); err != nil {
		return nil, fmt.Errorf("read run timestamp: %w", err)
	}
	run.ComputedAt = canonicalTime(run.ComputedAt)
	return run, nil
}

// DiscardRun deletes an unreferenced run and its rows. A run some snapshot
// references is left alone (returns an error), because removing it would
// change what that snapshot reads.
func (s *sqliteStore) DiscardRun(ctx context.Context, runID int64) error {
	return s.withTx(ctx, func(tx *sql.Tx) error {
		var refs int
		if err := tx.QueryRowContext(ctx, `SELECT COUNT(*) FROM snapshot_parts WHERE run_id = ?`, runID).Scan(&refs); err != nil {
			return fmt.Errorf("check run %d references: %w", runID, err)
		}
		if refs > 0 {
			return fmt.Errorf("discard run %d: still referenced by %d snapshot(s)", runID, refs)
		}
		_, err := deleteRuns(ctx, tx, s.cityID, []int64{runID})
		return err
	})
}

// CommitSnapshot creates the next snapshot for configHash. See the Store
// interface for the contract. It retries while another process holds the
// write lock (isBusy), for up to commitBusyBudget.
func (s *sqliteStore) CommitSnapshot(ctx context.Context, configHash string, parts map[string]int64) (*Snapshot, error) {
	if configHash == "" {
		return nil, errors.New("commit snapshot: config hash is required")
	}
	deadline := time.Now().Add(commitBusyBudget)
	for wait := 100 * time.Millisecond; ; wait = min(2*wait, 5*time.Second) {
		snap, err := s.commitSnapshot(ctx, configHash, parts)
		if !isBusy(err) || time.Now().Add(wait).After(deadline) {
			return snap, err
		}
		select {
		case <-ctx.Done():
			return nil, errors.Join(err, ctx.Err())
		case <-time.After(wait):
		}
	}
}

// isBusy reports SQLITE_BUSY / SQLITE_LOCKED (any extended code): another
// connection holds the lock past busy_timeout.
func isBusy(err error) bool {
	var se *sqlite.Error
	if !errors.As(err, &se) {
		return false
	}
	code := se.Code() & 0xff
	return code == sqlite3.SQLITE_BUSY || code == sqlite3.SQLITE_LOCKED
}

// commitSnapshot is one attempt at CommitSnapshot. A commit that would not
// change the manifest (dropping a part the base lacks, re-putting the same
// run) writes nothing and returns the base snapshot, or (nil, nil) when the
// hash has no snapshot yet: a no-op command must not mint a snapshot (it
// would use up a `snapshots prune` slot) and must never create an empty one.
func (s *sqliteStore) commitSnapshot(ctx context.Context, configHash string, parts map[string]int64) (*Snapshot, error) {
	var snap *Snapshot
	err := s.withTx(ctx, func(tx *sql.Tx) error {
		if err := s.checkCommitRuns(ctx, tx, configHash, parts); err != nil {
			return err
		}
		base, err := newestSnapshotID(ctx, tx, s.cityID, configHash)
		if err != nil {
			return err
		}
		baseParts, err := snapshotParts(ctx, tx, s.cityID, base)
		if err != nil {
			return err
		}
		manifest := maps.Clone(baseParts)
		applyParts(manifest, parts)
		id := base
		if !maps.Equal(manifest, baseParts) {
			if id, err = s.insertSnapshot(ctx, tx, configHash, manifest); err != nil {
				return err
			}
		}
		if id == 0 {
			return nil // no-op on a hash with no snapshot: nothing to return
		}
		snaps, err := listSnapshots(ctx, tx, s.cityID, id)
		if err != nil {
			return err
		}
		snap = &snaps[0]
		return nil
	})
	if err != nil {
		return nil, err
	}
	return snap, nil
}

// insertSnapshot writes a new snapshot row with manifest as its parts and
// returns its id.
func (s *sqliteStore) insertSnapshot(ctx context.Context, tx *sql.Tx, configHash string, manifest map[string]int64) (int64, error) {
	res, err := tx.ExecContext(ctx, `INSERT INTO snapshots (city_id, config_hash) VALUES (?, ?)`, s.cityID, configHash)
	if err != nil {
		return 0, fmt.Errorf("create snapshot: %w", err)
	}
	id, err := res.LastInsertId()
	if err != nil {
		return 0, fmt.Errorf("create snapshot id: %w", err)
	}
	for _, part := range slices.Sorted(maps.Keys(manifest)) {
		if _, err := tx.ExecContext(ctx, `INSERT INTO snapshot_parts (snapshot_id, part, run_id) VALUES (?, ?, ?)`,
			id, part, manifest[part]); err != nil {
			return 0, fmt.Errorf("record snapshot part %s: %w", part, err)
		}
	}
	return id, nil
}

// checkCommitRuns enforces the manifest invariants on the runs a commit adds:
// each belongs to this city, was produced for the part it fills, and carries
// the snapshot's config hash (so every run in a snapshot shares its hash).
func (s *sqliteStore) checkCommitRuns(ctx context.Context, tx *sql.Tx, configHash string, parts map[string]int64) error {
	for _, part := range slices.Sorted(maps.Keys(parts)) {
		runID := parts[part]
		if runID == 0 {
			continue
		}
		var producer, hash string
		err := tx.QueryRowContext(ctx, `SELECT producer, config_hash FROM runs WHERE id = ? AND city_id = ?`,
			runID, s.cityID).Scan(&producer, &hash)
		if errors.Is(err, sql.ErrNoRows) {
			return fmt.Errorf("commit snapshot: run %d for part %q not found for this city", runID, part)
		}
		if err != nil {
			return fmt.Errorf("commit snapshot: look up run %d: %w", runID, err)
		}
		if producer != part {
			return fmt.Errorf("commit snapshot: run %d was produced for %q, not %q", runID, producer, part)
		}
		if hash != configHash {
			return fmt.Errorf("commit snapshot: run %d has config hash %q, snapshot has %q", runID, hash, configHash)
		}
	}
	return nil
}

// ListSnapshots returns this city's snapshots newest first, with manifests.
func (s *sqliteStore) ListSnapshots(ctx context.Context) ([]Snapshot, error) {
	return listSnapshots(ctx, s.db, s.cityID, 0)
}

// canonicalTime puts a stored timestamp in UTC at second precision, so
// marshaling a Snapshot or Run yields the same RFC3339 string the CLI prints
// with t.UTC().Format(time.RFC3339).
func canonicalTime(t time.Time) time.Time {
	return t.UTC().Truncate(time.Second)
}

// listSnapshots lists the city's snapshots newest first (by id; created_at
// has second granularity), each with its manifest sorted by part. A non-zero
// only restricts the result to that one snapshot.
func listSnapshots(ctx context.Context, q queryer, cityID, only int64) ([]Snapshot, error) {
	query := `SELECT s.id, s.created_at, s.config_hash, p.part, p.run_id, r.computed_at
		FROM snapshots s
		LEFT JOIN snapshot_parts p ON p.snapshot_id = s.id
		LEFT JOIN runs r ON r.id = p.run_id
		WHERE s.city_id = ? AND (? = 0 OR s.id = ?)
		ORDER BY s.id DESC, p.part`
	rows, err := q.QueryContext(ctx, query, cityID, only, only)
	if err != nil {
		return nil, fmt.Errorf("query snapshots: %w", err)
	}
	defer func() { _ = rows.Close() }()

	var out []Snapshot
	for rows.Next() {
		var (
			snap       Snapshot
			part       sql.NullString
			runID      sql.NullInt64
			computedAt *time.Time
		)
		if err := rows.Scan(&snap.ID, &snap.CreatedAt, &snap.ConfigHash, &part, &runID, &computedAt); err != nil {
			return nil, fmt.Errorf("scan snapshot: %w", err)
		}
		snap.CreatedAt = canonicalTime(snap.CreatedAt)
		if len(out) == 0 || out[len(out)-1].ID != snap.ID {
			out = append(out, snap)
		}
		if part.Valid {
			p := SnapshotPart{Part: part.String, RunID: runID.Int64}
			if computedAt != nil {
				p.ComputedAt = canonicalTime(*computedAt)
			}
			last := &out[len(out)-1]
			last.Parts = append(last.Parts, p)
		}
	}
	return out, rows.Err()
}

// ResolveSnapshot returns nil iff the snapshot exists and belongs to this
// city, and sql.ErrNoRows otherwise. The server uses it to turn an unknown
// ?snapshot=<id> into a 404 rather than an empty response.
func (s *sqliteStore) ResolveSnapshot(ctx context.Context, snapshotID int64) error {
	if snapshotID <= 0 {
		return fmt.Errorf("invalid snapshot id %d", snapshotID)
	}
	var n int64
	return s.db.QueryRowContext(ctx,
		`SELECT id FROM snapshots WHERE city_id = ? AND id = ?`, s.cityID, snapshotID).Scan(&n)
}

// DeleteSnapshot removes the snapshot's manifest, then the runs it referenced
// that no remaining snapshot references (with their rows). A snapshot owned by
// another city is treated as not found (false, nil).
func (s *sqliteStore) DeleteSnapshot(ctx context.Context, snapshotID int64) (bool, error) {
	if snapshotID <= 0 {
		return false, fmt.Errorf("invalid snapshot id %d", snapshotID)
	}
	var deleted bool
	err := s.withTx(ctx, func(tx *sql.Tx) error {
		var owned int64
		err := tx.QueryRowContext(ctx, `SELECT id FROM snapshots WHERE id = ? AND city_id = ?`,
			snapshotID, s.cityID).Scan(&owned)
		if errors.Is(err, sql.ErrNoRows) {
			return nil
		}
		if err != nil {
			return fmt.Errorf("lookup snapshot: %w", err)
		}
		parts, err := snapshotParts(ctx, tx, s.cityID, snapshotID)
		if err != nil {
			return err
		}
		if _, err := tx.ExecContext(ctx, `DELETE FROM snapshot_parts WHERE snapshot_id = ?`, snapshotID); err != nil {
			return fmt.Errorf("delete snapshot parts: %w", err)
		}
		if _, err := tx.ExecContext(ctx, `DELETE FROM snapshots WHERE id = ? AND city_id = ?`, snapshotID, s.cityID); err != nil {
			return fmt.Errorf("delete snapshot: %w", err)
		}
		unreferenced, err := unreferencedRuns(ctx, tx, slices.Sorted(maps.Values(parts)))
		if err != nil {
			return err
		}
		if _, err := deleteRuns(ctx, tx, s.cityID, unreferenced); err != nil {
			return err
		}
		deleted = true
		return nil
	})
	return deleted, err
}

// unreferencedRuns returns those of runIDs that no snapshot references.
func unreferencedRuns(ctx context.Context, tx *sql.Tx, runIDs []int64) ([]int64, error) {
	var out []int64
	for _, run := range runIDs {
		var refs int
		if err := tx.QueryRowContext(ctx, `SELECT COUNT(*) FROM snapshot_parts WHERE run_id = ?`, run).Scan(&refs); err != nil {
			return nil, fmt.Errorf("check run %d references: %w", run, err)
		}
		if refs == 0 {
			out = append(out, run)
		}
	}
	return out, nil
}

// deleteRuns deletes those of runIDs that belong to this city, with every row
// they wrote, returning per-table row counts. The caller guarantees no
// snapshot references them. Ids are narrowed to the city's runs first, so the
// row deletes can filter on run_id alone and seek its index (a city_id
// predicate makes the planner scan the city's whole slice instead).
func deleteRuns(ctx context.Context, tx *sql.Tx, cityID int64, runIDs []int64) (GCResultCounts, error) {
	var counts GCResultCounts
	owned, err := ownedRuns(ctx, tx, cityID, runIDs)
	if err != nil || len(owned) == 0 {
		return counts, err
	}
	args := runArgs(owned)
	in := placeholders(len(owned))
	for _, t := range gcResultTables(&counts) {
		q := `DELETE FROM ` + t.table + ` WHERE run_id IN (` + in + `)`
		if err := execDeleteCount(ctx, tx, t.table, q, t.count, args...); err != nil {
			return counts, err
		}
	}
	// in is only "?,?,..."; every id is a bound parameter.
	if _, err := tx.ExecContext(ctx, `DELETE FROM runs WHERE id IN (`+in+`)`, args...); err != nil { //nolint:gosec // placeholders only; values are bound
		return counts, fmt.Errorf("delete runs: %w", err)
	}
	return counts, nil
}

// ownedRuns returns the subset of runIDs that belong to cityID, sorted.
func ownedRuns(ctx context.Context, q queryer, cityID int64, runIDs []int64) ([]int64, error) {
	if len(runIDs) == 0 {
		return nil, nil
	}
	args := append([]any{cityID}, runArgs(runIDs)...)
	rows, err := q.QueryContext(ctx, `SELECT id FROM runs WHERE city_id = ? AND id IN (`+placeholders(len(runIDs))+`) ORDER BY id`, args...)
	if err != nil {
		return nil, fmt.Errorf("query runs: %w", err)
	}
	defer func() { _ = rows.Close() }()
	var out []int64
	for rows.Next() {
		var id int64
		if err := rows.Scan(&id); err != nil {
			return nil, fmt.Errorf("scan run: %w", err)
		}
		out = append(out, id)
	}
	return out, rows.Err()
}

// runArgs converts run ids to bind arguments.
func runArgs(ids []int64) []any {
	args := make([]any, len(ids))
	for i, id := range ids {
		args[i] = id
	}
	return args
}

// gcResultTables pairs each result table with its counter in GCResultCounts.
// Spelled as literals rather than zipped against resultTables so adding a
// table fails to compile here instead of panicking at runtime.
func gcResultTables(c *GCResultCounts) []struct {
	table string
	count *int
} {
	return []struct {
		table string
		count *int
	}{
		{"compute_results", &c.ComputeResults},
		{"hex_stats", &c.HexStats},
		{"cohort_stats", &c.CohortStats},
		{"hex_metrics", &c.HexMetrics},
	}
}

// orphanRuns lists this city's runs that no snapshot references and that are
// older than OrphanRunGrace.
func orphanRuns(ctx context.Context, q queryer, cityID int64) ([]int64, error) {
	cutoff := time.Now().Add(-OrphanRunGrace).UTC().Format(time.DateTime)
	rows, err := q.QueryContext(ctx,
		`SELECT id FROM runs WHERE city_id = ? AND computed_at < ?
		   AND id NOT IN (SELECT run_id FROM snapshot_parts) ORDER BY id`, cityID, cutoff)
	if err != nil {
		return nil, fmt.Errorf("query orphan runs: %w", err)
	}
	defer func() { _ = rows.Close() }()
	var ids []int64
	for rows.Next() {
		var id int64
		if err := rows.Scan(&id); err != nil {
			return nil, fmt.Errorf("scan orphan run: %w", err)
		}
		ids = append(ids, id)
	}
	return ids, rows.Err()
}

// GCScan counts orphan rows for this city without writing anything. See the
// Store interface for the orphan classes and the empty-keepSources guard.
func (s *sqliteStore) GCScan(ctx context.Context, keepSources []string) (*GCReport, error) {
	report := &GCReport{}
	if len(keepSources) > 0 {
		placeholders, args := s.gcKeepArgs(keepSources)
		typePlaceholders, typeArgs := gcSweptTypes()
		args = append(args, typeArgs...)
		q := `SELECT COUNT(*) FROM features WHERE city_id = ? AND source_api NOT IN (` + placeholders + `) AND source_api != '' AND resource_type IN (` + typePlaceholders + `)`
		if err := s.db.QueryRowContext(ctx, q, args...).Scan(&report.StaleFeatures); err != nil {
			return nil, fmt.Errorf("count stale features: %w", err)
		}
	}
	runs, err := orphanRuns(ctx, s.db, s.cityID)
	if err != nil {
		return nil, err
	}
	report.OrphanRuns = len(runs)
	if len(runs) == 0 {
		return report, nil
	}
	// orphanRuns is already scoped to this city, so count by run_id alone
	// (see deleteRuns on why that matters for the plan).
	args := runArgs(runs)
	for _, t := range gcResultTables(&report.OrphanRunResults) {
		q := `SELECT COUNT(*) FROM ` + t.table + ` WHERE run_id IN (` + placeholders(len(runs)) + `)`
		if err := s.db.QueryRowContext(ctx, q, args...).Scan(t.count); err != nil {
			return nil, fmt.Errorf("count orphan-run %s: %w", t.table, err)
		}
	}
	return report, nil
}

// GCSweep deletes the orphan rows GCScan counts and returns the counts of
// rows actually deleted, all inside a single transaction.
func (s *sqliteStore) GCSweep(ctx context.Context, keepSources []string) (*GCReport, error) {
	report := &GCReport{}
	err := s.withTx(ctx, func(tx *sql.Tx) error {
		if len(keepSources) > 0 {
			placeholders, args := s.gcKeepArgs(keepSources)
			typePlaceholders, typeArgs := gcSweptTypes()
			args = append(args, typeArgs...)
			q := `DELETE FROM features WHERE city_id = ? AND source_api NOT IN (` + placeholders + `) AND source_api != '' AND resource_type IN (` + typePlaceholders + `)`
			if err := execDeleteCount(ctx, tx, "stale features", q, &report.StaleFeatures, args...); err != nil {
				return err
			}
		}
		runs, err := orphanRuns(ctx, tx, s.cityID)
		if err != nil {
			return err
		}
		counts, err := deleteRuns(ctx, tx, s.cityID, runs)
		if err != nil {
			return err
		}
		report.OrphanRuns, report.OrphanRunResults = len(runs), counts
		return nil
	})
	if err != nil {
		return nil, err
	}
	return report, nil
}
