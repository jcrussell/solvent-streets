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

// commitBusyBudget bounds how long CommitLatest and CreateSnapshot keep
// retrying (retryBusy) while another process holds the write lock past
// busy_timeout. A compute that gives up discards its runs, possibly hours of
// work, so it waits generously.
const commitBusyBudget = 2 * time.Minute

// ErrSnapshotGone is returned by every snapshot-scoped read (city and hex
// metrics, the frozen boundary) of a view pinned with WithSnapshot when that
// snapshot does not exist: deleted or pruned since it was pinned, or never
// created. A pinned view never reads a missing snapshot as an empty city.
var ErrSnapshotGone = errors.New("snapshot no longer exists (deleted or pruned)")

// WithSnapshot returns a view that reads exactly the given snapshot, including
// the boundary it froze (GetBoundary). A snapshotID of 0 returns an unpinned
// view. A pin wins over WithLatestCompute and WithRuns (it is the more
// specific request, matching the server's ?snapshot=N). Once the snapshot is
// gone every snapshot-scoped read returns ErrSnapshotGone. The underlying DB
// connection is shared.
func (s *sqliteStore) WithSnapshot(snapshotID int64) Store {
	cp := *s
	cp.snapshotID = snapshotID
	return &cp
}

// WithLatestCompute returns a view that reads the city's latest compute
// under configHash (working_parts), resolved on every read: what compute,
// status and forecast work from, so a city shared by two configs at
// different hex_edge_m never reads the other config's (incompatible) hex_id
// namespace. An empty hash reads the newest snapshot of any hash.
func (s *sqliteStore) WithLatestCompute(configHash string) Store {
	cp := *s
	cp.configHash = configHash
	return &cp
}

// WithRuns returns a view that reads exactly runIDs, with the live boundary:
// a latest compute pinned as of one moment (serve's view of a city with no
// snapshot), so later computes do not mix into it. Once any of the runs is
// gone (gc swept it after a later compute replaced it) every read returns
// ErrSnapshotGone. A snapshot pin (WithSnapshot) wins over it.
func (s *sqliteStore) WithRuns(runIDs []int64) Store {
	cp := *s
	cp.runs = slices.Sorted(slices.Values(runIDs))
	cp.runsPinned = true
	return &cp
}

// checkPin returns ErrSnapshotGone when the view is pinned to a snapshot that
// does not cover this city (deleted, or never had it), or to runs (WithRuns) that no longer all exist. An
// unpinned view always passes.
func (s *sqliteStore) checkPin(ctx context.Context, q queryer) error {
	if s.snapshotID <= 0 {
		return s.checkRuns(ctx, q)
	}
	var one int
	switch err := q.QueryRowContext(ctx, sqlCityInSnapshot, s.snapshotID, s.cityID).Scan(&one); {
	case errors.Is(err, sql.ErrNoRows):
		return fmt.Errorf("snapshot %d: %w", s.snapshotID, ErrSnapshotGone)
	case err != nil:
		return fmt.Errorf("check snapshot %d: %w", s.snapshotID, err)
	}
	return nil
}

// checkRuns is checkPin for a WithRuns view: every pinned run must still
// exist for this city.
func (s *sqliteStore) checkRuns(ctx context.Context, q queryer) error {
	if !s.runsPinned || len(s.runs) == 0 {
		return nil
	}
	owned, err := ownedRuns(ctx, q, s.cityID, s.runs)
	if err != nil {
		return err
	}
	if len(owned) != len(s.runs) {
		return fmt.Errorf("latest compute: %d of its %d runs: %w", len(s.runs)-len(owned), len(s.runs), ErrSnapshotGone)
	}
	return nil
}

// resolvedSnapshotID returns the snapshot this view reads: the pinned id,
// else, for a view with no config hash and no pinned runs, the newest
// snapshot. It returns 0 when the view reads no snapshot (a latest compute,
// pinned runs, or a city with none), and ErrSnapshotGone when the pinned one
// does not exist.
func (s *sqliteStore) resolvedSnapshotID(ctx context.Context, q queryer) (int64, error) {
	if s.snapshotID > 0 {
		return s.snapshotID, s.checkPin(ctx, q)
	}
	if s.runsPinned || s.configHash != "" {
		return 0, nil
	}
	return newestSnapshotID(ctx, q, s.cityID)
}

// queryer is the read surface shared by *sql.DB and *sql.Tx.
type queryer interface {
	QueryContext(ctx context.Context, query string, args ...any) (*sql.Rows, error)
	QueryRowContext(ctx context.Context, query string, args ...any) *sql.Row
}

// The statements that resolve a city's snapshot. Each names the city and
// seeks a key: snapshot_cities' primary key or idx_snapshot_cities_city,
// snapshot_parts' primary key (TestQueryPlan_SnapshotStatements).
const (
	// sqlCityInSnapshot: does snapshot ? cover city ?
	sqlCityInSnapshot = `SELECT 1 FROM snapshot_cities WHERE snapshot_id = ? AND city_id = ?`
	// sqlNewestSnapshotForCity: the newest snapshot covering city ?. By id,
	// which is monotonic, rather than created_at, which has second
	// granularity.
	sqlNewestSnapshotForCity = `SELECT snapshot_id FROM snapshot_cities WHERE city_id = ? ORDER BY snapshot_id DESC LIMIT 1`
	// sqlSnapshotParts: city ?'s manifest in snapshot ?.
	sqlSnapshotParts = `SELECT part, run_id FROM snapshot_parts WHERE snapshot_id = ? AND city_id = ?`
	// sqlResolveSnapshotName: the id of snapshot named ? when it covers city ?.
	sqlResolveSnapshotName = `SELECT c.snapshot_id FROM snapshots s
		JOIN snapshot_cities c ON c.snapshot_id = s.id AND c.city_id = ?
		WHERE s.name = ?`
	// sqlFrozenBoundary: the boundary snapshot ? froze for city ?.
	sqlFrozenBoundary = `SELECT b.geometry_json FROM snapshot_cities c
		LEFT JOIN snapshot_boundaries b ON b.id = c.boundary_id
		WHERE c.snapshot_id = ? AND c.city_id = ?`
	// sqlRunInSnapshots: how many snapshot manifests hold run ?.
	sqlRunInSnapshots = `SELECT COUNT(*) FROM snapshot_parts WHERE run_id = ?`
	// sqlSweepSnapshotBoundaries: drop city ?'s frozen boundaries no snapshot
	// city references any more.
	sqlSweepSnapshotBoundaries = `DELETE FROM snapshot_boundaries WHERE city_id = ?
		AND id NOT IN (SELECT boundary_id FROM snapshot_cities WHERE boundary_id IS NOT NULL)`
)

// newestSnapshotID is the newest snapshot covering the city; 0 when none.
func newestSnapshotID(ctx context.Context, q queryer, cityID int64) (int64, error) {
	var id int64
	switch err := q.QueryRowContext(ctx, sqlNewestSnapshotForCity, cityID).Scan(&id); {
	case errors.Is(err, sql.ErrNoRows):
		return 0, nil
	case err != nil:
		return 0, fmt.Errorf("resolve snapshot: %w", err)
	}
	return id, nil
}

// snapshotParts returns the city's manifest in a snapshot as part -> run id.
// An id of 0 (no snapshot) returns an empty map.
func snapshotParts(ctx context.Context, q queryer, cityID, snapshotID int64) (map[string]int64, error) {
	parts := map[string]int64{}
	if snapshotID == 0 {
		return parts, nil
	}
	rows, err := q.QueryContext(ctx, sqlSnapshotParts, snapshotID, cityID)
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

// applyParts applies parts to base in place: a run id of 0 removes the
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

// viewRunIDs returns the runs this view reads, sorted for stable SQL: the
// city's manifest in the pinned snapshot, the pinned runs, the latest compute
// under the view's hash, or else its manifest in the newest snapshot.
func (s *sqliteStore) viewRunIDs(ctx context.Context) ([]int64, error) {
	switch {
	case s.snapshotID > 0:
	case s.runsPinned:
		return s.runs, s.checkRuns(ctx, s.db)
	case s.configHash != "":
		parts, err := workingParts(ctx, s.db, s.cityID, s.configHash)
		if err != nil {
			return nil, err
		}
		return slices.Sorted(maps.Values(parts)), nil
	}
	snap, err := s.resolvedSnapshotID(ctx, s.db)
	if err != nil {
		return nil, err
	}
	parts, err := snapshotParts(ctx, s.db, s.cityID, snap)
	if err != nil {
		return nil, err
	}
	return slices.Sorted(maps.Values(parts)), nil
}

// viewFilter is a WHERE fragment and its binds. Column names in sql are
// always compile-time literals at the call site, never user input; every
// value is a bound parameter.
type viewFilter struct {
	sql  string
	args []any
}

// eq is `column = value`.
func eq(column string, value any) viewFilter {
	return viewFilter{sql: column + ` = ?`, args: []any{value}}
}

// in is `column IN (values)`; with no values it matches nothing.
func in(column string, values []string) viewFilter {
	if len(values) == 0 {
		return viewFilter{sql: `0`}
	}
	return viewFilter{sql: column + ` IN (` + placeholders(len(values)) + `)`, args: anyArgs(values)}
}

// and conjoins filters.
func and(fs ...viewFilter) viewFilter {
	var out viewFilter
	for i, f := range fs {
		if i > 0 {
			out.sql += ` AND `
		}
		out.sql += f.sql
		out.args = append(out.args, f.args...)
	}
	return out
}

// viewQuery builds `selectClause WHERE run_id IN (<this view's runs>) AND
// where suffix` and its binds. Result rows carry no city_id: the view's runs
// come from the city's own snapshot, its pinned runs, or its latest compute
// under the view's config hash, so the run filter alone scopes the read to
// the city. ok is false when the view resolves no runs (no snapshot, or
// nothing computed under the hash), in which case callers return an empty
// result without querying.
func (s *sqliteStore) viewQuery(ctx context.Context, selectClause string, where viewFilter, suffix string) (q string, args []any, ok bool, err error) {
	runs, err := s.viewRunIDs(ctx)
	if err != nil || len(runs) == 0 {
		return "", nil, false, err
	}
	args = append(runArgs(runs), where.args...)
	q = selectClause + ` WHERE run_id IN (` + placeholders(len(runs)) + `)`
	if where.sql != "" {
		q += ` AND ` + where.sql
	}
	return q + suffix, args, true, nil
}

// placeholders returns "?,?,...,?" with n entries (n > 0).
func placeholders(n int) string {
	return strings.TrimSuffix(strings.Repeat("?,", n), ",")
}

// anyArgs returns values as bound query arguments.
func anyArgs(values []string) []any {
	args := make([]any, len(values))
	for i, v := range values {
		args[i] = v
	}
	return args
}

// CreateRun records a new run, with the input digests it read (run_inputs),
// in one transaction. It is invisible to readers until a snapshot references
// it.
func (s *sqliteStore) CreateRun(ctx context.Context, producer, configHash string, inputs map[string]string) (*Run, error) {
	if producer == "" {
		return nil, errors.New("create run: producer is required")
	}
	now := nowUnix()
	run := &Run{Producer: producer, ConfigHash: configHash, ComputedAt: fromUnix(now)}
	err := s.withTx(ctx, func(tx *sql.Tx) error {
		res, err := tx.ExecContext(ctx,
			`INSERT INTO runs (city_id, producer, config_hash, computed_at) VALUES (?, ?, ?, ?)`, s.cityID, producer, configHash, now)
		if err != nil {
			return fmt.Errorf("create run: %w", err)
		}
		if run.ID, err = res.LastInsertId(); err != nil {
			return fmt.Errorf("create run id: %w", err)
		}
		return recordRunInputs(ctx, tx, run.ID, inputs)
	})
	if err != nil {
		return nil, err
	}
	return run, nil
}

// runRefs counts the references that keep a run alive: snapshot manifests
// and the latest compute.
func runRefs(ctx context.Context, q queryer, runID int64) (int, error) {
	var refs int
	if err := q.QueryRowContext(ctx,
		`SELECT (`+sqlRunInSnapshots+`) + (SELECT COUNT(*) FROM working_parts WHERE run_id = ?)`,
		runID, runID).Scan(&refs); err != nil {
		return 0, fmt.Errorf("check run %d references: %w", runID, err)
	}
	return refs, nil
}

// DiscardRun deletes an unreferenced run and its rows. A run some snapshot
// or the latest compute references is left alone (returns an error), because
// removing it would change what that snapshot or the latest compute reads.
func (s *sqliteStore) DiscardRun(ctx context.Context, runID int64) error {
	return s.withTx(ctx, func(tx *sql.Tx) error {
		refs, err := runRefs(ctx, tx, runID)
		if err != nil {
			return err
		}
		if refs > 0 {
			return fmt.Errorf("discard run %d: still referenced by %d snapshot or latest-compute part(s)", runID, refs)
		}
		_, err = deleteRuns(ctx, tx, s.cityID, []int64{runID})
		return err
	})
}

// CommitLatest applies parts to the city's latest compute for configHash.
// See the Store interface for the contract. It retries while another process
// holds the write lock (isBusy), for up to commitBusyBudget.
func (s *sqliteStore) CommitLatest(ctx context.Context, configHash string, computeEpoch int, parts map[string]int64) error {
	if configHash == "" {
		return errors.New("commit latest compute: config hash is required")
	}
	return retryBusy(ctx, func() error { return s.commitLatest(ctx, configHash, computeEpoch, parts) })
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

// commitLatest is one attempt at CommitLatest. A commit that would not
// change the manifest (dropping a part the latest compute lacks, re-putting
// the same run) writes nothing. A replaced run is not deleted: it becomes
// unreferenced, unless a snapshot holds it, and gc sweeps it after
// OrphanRunGrace.
func (s *sqliteStore) commitLatest(ctx context.Context, configHash string, computeEpoch int, parts map[string]int64) error {
	return s.withTx(ctx, func(tx *sql.Tx) error {
		if err := s.checkCommitRuns(ctx, tx, configHash, parts); err != nil {
			return err
		}
		base, err := workingParts(ctx, tx, s.cityID, configHash)
		if err != nil {
			return err
		}
		manifest := maps.Clone(base)
		applyParts(manifest, parts)
		if maps.Equal(manifest, base) {
			return nil
		}
		return s.setWorkingParts(ctx, tx, configHash, computeEpoch, manifest)
	})
}

// workingParts returns the city's latest compute for configHash as part ->
// run id (empty when nothing is computed under it).
func workingParts(ctx context.Context, q queryer, cityID int64, configHash string) (map[string]int64, error) {
	rows, err := q.QueryContext(ctx,
		`SELECT part, run_id FROM working_parts WHERE city_id = ? AND config_hash = ?`, cityID, configHash)
	if err != nil {
		return nil, fmt.Errorf("query latest compute: %w", err)
	}
	defer func() { _ = rows.Close() }()
	parts := map[string]int64{}
	for rows.Next() {
		var part string
		var run int64
		if err := rows.Scan(&part, &run); err != nil {
			return nil, fmt.Errorf("scan latest compute part: %w", err)
		}
		parts[part] = run
	}
	return parts, rows.Err()
}

// insertSnapshotCity records req's city in snapshot snapshotID with manifest
// as its parts, freezing req.HexEdgeM and the city's current boundary.
func insertSnapshotCity(ctx context.Context, tx *sql.Tx, snapshotID int64, req SnapshotRequest, manifest map[string]int64) error {
	var boundary sql.NullInt64
	var err error
	if boundary.Int64, boundary.Valid, err = freezeBoundary(ctx, tx, req.CityID); err != nil {
		return err
	}
	if _, err := tx.ExecContext(ctx,
		`INSERT INTO snapshot_cities (snapshot_id, city_id, config_hash, hex_edge_m, boundary_id) VALUES (?, ?, ?, ?, ?)`,
		snapshotID, req.CityID, req.ConfigHash, req.HexEdgeM, boundary); err != nil {
		return fmt.Errorf("record snapshot city %d: %w", req.CityID, err)
	}
	for _, part := range slices.Sorted(maps.Keys(manifest)) {
		if _, err := tx.ExecContext(ctx, `INSERT INTO snapshot_parts (snapshot_id, city_id, part, run_id) VALUES (?, ?, ?, ?)`,
			snapshotID, req.CityID, part, manifest[part]); err != nil {
			return fmt.Errorf("record snapshot part %s: %w", part, err)
		}
	}
	return nil
}

// setWorkingParts makes manifest the city's latest compute for configHash,
// committed at computeEpoch.
func (s *sqliteStore) setWorkingParts(ctx context.Context, tx *sql.Tx, configHash string, computeEpoch int, manifest map[string]int64) error {
	if _, err := tx.ExecContext(ctx, `DELETE FROM working_parts WHERE city_id = ? AND config_hash = ?`, s.cityID, configHash); err != nil {
		return fmt.Errorf("clear latest compute: %w", err)
	}
	for _, part := range slices.Sorted(maps.Keys(manifest)) {
		if _, err := tx.ExecContext(ctx, `INSERT INTO working_parts (city_id, config_hash, compute_epoch, part, run_id) VALUES (?, ?, ?, ?, ?)`,
			s.cityID, configHash, computeEpoch, part, manifest[part]); err != nil {
			return fmt.Errorf("record working part %s: %w", part, err)
		}
	}
	return nil
}

// checkCommitRuns enforces the manifest invariants on the runs a commit adds:
// each belongs to this city, was produced for the part it fills, and carries
// the commit's config hash (so every run of a latest compute, and so of a
// snapshot, shares its hash).
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
			return fmt.Errorf("commit latest compute: run %d for part %q not found for this city", runID, part)
		}
		if err != nil {
			return fmt.Errorf("commit latest compute: look up run %d: %w", runID, err)
		}
		if producer != part {
			return fmt.Errorf("commit latest compute: run %d was produced for %q, not %q", runID, producer, part)
		}
		if hash != configHash {
			return fmt.Errorf("commit latest compute: run %d has config hash %q, snapshot has %q", runID, hash, configHash)
		}
	}
	return nil
}

// ListSnapshots returns the snapshots covering this city newest first, with
// the city's manifests.
func (s *sqliteStore) ListSnapshots(ctx context.Context) ([]SnapshotCity, error) {
	return listSnapshotCities(ctx, s.db, eq("c.city_id", s.cityID), orderNewestFirst)
}

// snapshotCityOrder is an ORDER BY over listSnapshotCities' snapshot_cities c
// and cities ci; only the constants below are used.
type snapshotCityOrder string

const (
	orderNewestFirst snapshotCityOrder = `c.snapshot_id DESC`
	orderSnapshotID  snapshotCityOrder = `c.snapshot_id`
	orderCitySlug    snapshotCityOrder = `ci.slug, c.city_id`
)

// listSnapshotCities reads snapshot cities matching where (a filter over
// snapshot_cities c), ordered by order then part, each with its manifest.
func listSnapshotCities(ctx context.Context, q queryer, where viewFilter, order snapshotCityOrder) ([]SnapshotCity, error) {
	query := `SELECT s.id, s.name, s.created_at, s.compute_epoch, c.city_id, ci.slug, c.config_hash, c.hex_edge_m,
			COALESCE(b.digest, ''), p.part, p.run_id, r.computed_at
		FROM snapshot_cities c
		JOIN snapshots s ON s.id = c.snapshot_id
		JOIN cities ci ON ci.id = c.city_id
		LEFT JOIN snapshot_boundaries b ON b.id = c.boundary_id
		LEFT JOIN snapshot_parts p ON p.snapshot_id = c.snapshot_id AND p.city_id = c.city_id
		LEFT JOIN runs r ON r.id = p.run_id
		WHERE ` + where.sql + `
		ORDER BY ` + string(order) + `, p.part`
	rows, err := q.QueryContext(ctx, query, where.args...)
	if err != nil {
		return nil, fmt.Errorf("query snapshots: %w", err)
	}
	defer func() { _ = rows.Close() }()

	var out []SnapshotCity
	for rows.Next() {
		var (
			snap       SnapshotCity
			part       sql.NullString
			runID      sql.NullInt64
			computedAt time.Time
		)
		if err := rows.Scan(&snap.ID, &snap.Name, unixTime{&snap.CreatedAt}, &snap.ComputeEpoch, &snap.CityID, &snap.CitySlug,
			&snap.ConfigHash, &snap.HexEdgeM, &snap.BoundaryDigest, &part, &runID, unixTime{&computedAt}); err != nil {
			return nil, fmt.Errorf("scan snapshot: %w", err)
		}
		if n := len(out); n == 0 || out[n-1].ID != snap.ID || out[n-1].CityID != snap.CityID {
			out = append(out, snap)
		}
		if part.Valid {
			last := &out[len(out)-1]
			last.Parts = append(last.Parts, SnapshotPart{Part: part.String, RunID: runID.Int64, ComputedAt: computedAt})
		}
	}
	return out, rows.Err()
}

// ViewSnapshot returns the snapshot this view reads. See the Store interface.
func (s *sqliteStore) ViewSnapshot(ctx context.Context) (*SnapshotCity, error) {
	id, err := s.resolvedSnapshotID(ctx, s.db)
	if err != nil || id == 0 {
		return nil, err
	}
	snaps, err := listSnapshotCities(ctx, s.db, and(eq("c.snapshot_id", id), eq("c.city_id", s.cityID)), orderSnapshotID)
	if err != nil {
		return nil, err
	}
	if len(snaps) == 0 { // deleted between the two reads
		return nil, fmt.Errorf("snapshot %d: %w", id, ErrSnapshotGone)
	}
	return &snaps[0], nil
}

// ResolveSnapshot returns the id of the snapshot named name when it covers
// this city, and ErrSnapshotNotFound otherwise. The server uses it to turn an
// unknown ?snapshot=<name> into a 404 rather than an empty response.
func (s *sqliteStore) ResolveSnapshot(ctx context.Context, name string) (int64, error) {
	var id int64
	switch err := s.db.QueryRowContext(ctx, sqlResolveSnapshotName, s.cityID, name).Scan(&id); {
	case errors.Is(err, sql.ErrNoRows):
		return 0, fmt.Errorf("snapshot %s: %w", name, ErrSnapshotNotFound)
	case err != nil:
		return 0, fmt.Errorf("resolve snapshot %s: %w", name, err)
	}
	return id, nil
}

// unreferencedRuns returns those of runIDs that neither a snapshot nor a
// latest compute references.
func unreferencedRuns(ctx context.Context, tx *sql.Tx, runIDs []int64) ([]int64, error) {
	var out []int64
	for _, run := range runIDs {
		refs, err := runRefs(ctx, tx, run)
		if err != nil {
			return nil, err
		}
		if refs == 0 {
			out = append(out, run)
		}
	}
	return out, nil
}

// deleteRuns deletes those of runIDs that belong to cityID, with every row
// they wrote (ON DELETE CASCADE), returning per-table row counts. The caller
// guarantees no snapshot references them.
func deleteRuns(ctx context.Context, tx *sql.Tx, cityID int64, runIDs []int64) (GCResultCounts, error) {
	owned, err := ownedRuns(ctx, tx, cityID, runIDs)
	if err != nil || len(owned) == 0 {
		return GCResultCounts{}, err
	}
	counts, err := countRunResults(ctx, tx, owned)
	if err != nil {
		return counts, err
	}
	// placeholders are only "?,?,..."; every id is a bound parameter.
	if _, err := tx.ExecContext(ctx, `DELETE FROM runs WHERE id IN (`+placeholders(len(owned))+`)`, runArgs(owned)...); err != nil { //nolint:gosec // placeholders only; values are bound
		return counts, fmt.Errorf("delete runs: %w", err)
	}
	return counts, nil
}

// countRunResults counts the result rows runIDs wrote, per table. The ids
// must already be scoped to one city.
func countRunResults(ctx context.Context, q queryer, runIDs []int64) (GCResultCounts, error) {
	var counts GCResultCounts
	in, args := placeholders(len(runIDs)), runArgs(runIDs)
	if err := q.QueryRowContext(ctx, `SELECT COUNT(*) FROM city_metrics WHERE run_id IN (`+in+`)`, args...).Scan(&counts.CityMetrics); err != nil {
		return counts, fmt.Errorf("count run city metrics: %w", err)
	}
	if err := q.QueryRowContext(ctx, `SELECT COUNT(*) FROM hex_metrics WHERE run_id IN (`+in+`)`, args...).Scan(&counts.HexMetrics); err != nil {
		return counts, fmt.Errorf("count run hex metrics: %w", err)
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
	return scanIDs(rows, "run")
}

// scanIDs reads and closes rows of one integer id column, wrapping a scan
// failure with what (a noun for the ids).
func scanIDs(rows *sql.Rows, what string) ([]int64, error) {
	defer func() { _ = rows.Close() }()
	var ids []int64
	for rows.Next() {
		var id int64
		if err := rows.Scan(&id); err != nil {
			return nil, fmt.Errorf("scan %s: %w", what, err)
		}
		ids = append(ids, id)
	}
	return ids, rows.Err()
}

// runArgs converts run ids to bind arguments.
func runArgs(ids []int64) []any {
	args := make([]any, len(ids))
	for i, id := range ids {
		args[i] = id
	}
	return args
}

// orphanRuns lists this city's runs that neither a snapshot nor a latest
// compute committed at computeEpoch or later references and that are older
// than OrphanRunGrace. A latest compute from an older epoch is superseded
// (see sqlSupersededParts) and holds nothing.
func orphanRuns(ctx context.Context, q queryer, cityID int64, computeEpoch int) ([]int64, error) {
	cutoff := unixSeconds(time.Now().Add(-OrphanRunGrace))
	rows, err := q.QueryContext(ctx,
		`SELECT id FROM runs WHERE city_id = ? AND computed_at < ?
		   AND id NOT IN (SELECT run_id FROM snapshot_parts)
		   AND id NOT IN (SELECT run_id FROM working_parts WHERE compute_epoch >= ?) ORDER BY id`,
		cityID, cutoff, computeEpoch)
	if err != nil {
		return nil, fmt.Errorf("query orphan runs: %w", err)
	}
	return scanIDs(rows, "orphan run")
}

// sqlSupersededParts is the filter, bound as (city_id, compute_epoch), for a
// city's latest-compute parts committed under a ComputeEpoch older than the
// given one. GCScan counts by it and GCSweep deletes by it, so a dry run
// reports exactly what the sweep removes.
const sqlSupersededParts = `FROM working_parts WHERE city_id = ? AND compute_epoch < ?`

// GCScan counts orphan rows for this city without writing anything. See the
// Store interface for the orphan classes and the empty-keepSources guard.
func (s *sqliteStore) GCScan(ctx context.Context, keepSources []string, computeEpoch int) (*GCReport, error) {
	report := &GCReport{}
	if len(keepSources) > 0 {
		where, args := s.staleFeatures(keepSources)
		if err := s.db.QueryRowContext(ctx, sqlCountFeaturesWhere(where), args...).Scan(&report.StaleFeatures); err != nil {
			return nil, fmt.Errorf("count stale features: %w", err)
		}
	}
	if err := s.db.QueryRowContext(ctx, `SELECT COUNT(*) `+sqlSupersededParts, s.cityID, computeEpoch).Scan(&report.SupersededParts); err != nil {
		return nil, fmt.Errorf("count superseded latest computes: %w", err)
	}
	runs, err := orphanRuns(ctx, s.db, s.cityID, computeEpoch)
	if err != nil {
		return nil, err
	}
	report.OrphanRuns = len(runs)
	if len(runs) == 0 {
		return report, nil
	}
	// orphanRuns is already scoped to this city.
	report.OrphanRunResults, err = countRunResults(ctx, s.db, runs)
	if err != nil {
		return nil, err
	}
	return report, nil
}

// GCSweep deletes the orphan rows GCScan counts and returns the counts of
// rows actually deleted, all inside a single transaction.
func (s *sqliteStore) GCSweep(ctx context.Context, keepSources []string, computeEpoch int) (*GCReport, error) {
	report := &GCReport{}
	err := s.withTx(ctx, func(tx *sql.Tx) error {
		if err := s.sweepStaleFeatures(ctx, tx, keepSources, report); err != nil {
			return err
		}
		if err := sweepSnapshotBoundaries(ctx, tx, s.cityID); err != nil {
			return err
		}
		// Superseded parts go first: their runs are orphans only once no
		// latest compute row references them.
		if err := execDeleteCount(ctx, tx, "superseded latest computes",
			`DELETE `+sqlSupersededParts,
			&report.SupersededParts, s.cityID, computeEpoch); err != nil {
			return err
		}
		runs, err := orphanRuns(ctx, tx, s.cityID, computeEpoch)
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

// sweepStaleFeatures deletes the stale features GCScan counts (none when
// keepSources is empty) and refreshes the input digest of every type it
// deleted from.
func (s *sqliteStore) sweepStaleFeatures(ctx context.Context, tx *sql.Tx, keepSources []string, report *GCReport) error {
	if len(keepSources) == 0 {
		return nil
	}
	where, args := s.staleFeatures(keepSources)
	types, err := staleFeatureTypes(ctx, tx, where, args)
	if err != nil {
		return err
	}
	if err := execDeleteCount(ctx, tx, "stale features", sqlDeleteFeaturesWhere(where), &report.StaleFeatures, args...); err != nil {
		return err
	}
	for _, t := range types {
		if err := refreshFeaturesDigest(ctx, tx, s.cityID, t); err != nil {
			return err
		}
	}
	return nil
}
