package db

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"log/slog"
	"strings"
	"time"

	"github.com/jcrussell/solvent-streets/internal/resource"
)

func (s *sqliteStore) UpsertFeatures(ctx context.Context, resourceType resource.Type, features []Feature) error {
	return s.withTx(ctx, func(tx *sql.Tx) error {
		// Delete existing features for this resource type and city
		if _, err := tx.ExecContext(ctx, `DELETE FROM features WHERE resource_type = ? AND city_id = ?`, resourceType, s.cityID); err != nil {
			return fmt.Errorf("delete old features: %w", err)
		}

		stmt, err := tx.PrepareContext(ctx, `
			INSERT OR REPLACE INTO features (id, resource_type, city_id, name, tags, geometry_json, source_api, fetched_at)
			VALUES (?, ?, ?, ?, ?, ?, ?, ?)
		`)
		if err != nil {
			return fmt.Errorf("prepare upsert: %w", err)
		}
		defer func() { _ = stmt.Close() }()

		for _, f := range features {
			tagsJSON, err := json.Marshal(f.Tags)
			if err != nil {
				return fmt.Errorf("marshal tags: %w", err)
			}
			fetchedAt := f.FetchedAt
			if fetchedAt.IsZero() {
				fetchedAt = time.Now()
			}
			if _, err := stmt.ExecContext(ctx, f.ID, resourceType, s.cityID, f.Name, string(tagsJSON), f.GeometryJSON, f.SourceAPI, fetchedAt); err != nil {
				return fmt.Errorf("exec upsert feature %s: %w", f.ID, err)
			}
		}
		return nil
	})
}

func (s *sqliteStore) ListFeatures(ctx context.Context, resourceType resource.Type) ([]Feature, error) {
	rows, err := s.db.QueryContext(ctx, `
		SELECT id, resource_type, name, tags, geometry_json, source_api, fetched_at
		FROM features WHERE resource_type = ? AND city_id = ?
	`, resourceType, s.cityID)
	if err != nil {
		return nil, fmt.Errorf("query features: %w", err)
	}
	defer func() { _ = rows.Close() }()

	var features []Feature
	for rows.Next() {
		var f Feature
		var tagsJSON string
		if err := rows.Scan(&f.ID, &f.ResourceType, &f.Name, &tagsJSON, &f.GeometryJSON, &f.SourceAPI, &f.FetchedAt); err != nil {
			return nil, fmt.Errorf("scan feature: %w", err)
		}
		if err := json.Unmarshal([]byte(tagsJSON), &f.Tags); err != nil {
			// Surface the parse failure so a corrupt tags row is not
			// indistinguishable from a legitimately empty one. We
			// still fall through to an empty map so the rest of the
			// pipeline can keep running on the unaffected features.
			slog.WarnContext(ctx, "feature tags JSON unmarshal failed",
				"feature_id", f.ID, "resource_type", f.ResourceType, "err", err)
			f.Tags = make(map[string]string)
		}
		features = append(features, f)
	}
	return features, rows.Err()
}

func (s *sqliteStore) SaveComputeResult(ctx context.Context, result ComputeResult) error {
	_, err := s.db.ExecContext(ctx, `
		INSERT INTO compute_results (resource_type, city_id, total_area, feature_count, computed_at, snapshot_id)
		VALUES (?, ?, ?, ?, ?, ?)
	`, result.ResourceType, s.cityID, result.TotalArea, result.FeatureCount, time.Now(), result.SnapshotID)
	if err != nil {
		return fmt.Errorf("insert compute result: %w", err)
	}
	return nil
}

// LatestComputeResult returns the most recent compute result for a
// resource. Snapshot/config-hash filtering follows the three-arm
// semantic documented on snapshotFilter; ORDER BY computed_at DESC
// disambiguates within the chosen snapshot (LIMIT 1 because this is
// the single-row read).
func (s *sqliteStore) LatestComputeResult(ctx context.Context, resourceType resource.Type) (*ComputeResult, error) {
	q, bind := snapshotQuery(
		`SELECT id, resource_type, total_area, feature_count, computed_at, snapshot_id FROM compute_results`,
		"compute_results",
		` ORDER BY computed_at DESC LIMIT 1`,
		s.snapshotID, s.configHash, resourceType, s.cityID,
	)
	var r ComputeResult
	err := s.db.QueryRowContext(ctx, q, bind...).Scan(&r.ID, &r.ResourceType, &r.TotalArea, &r.FeatureCount, &r.ComputedAt, &r.SnapshotID)
	if err != nil {
		return nil, err
	}
	return &r, nil
}

// LatestComputeResults batches the per-type LatestComputeResult lookup
// into a single query. Each resource_type partition keeps its own
// "latest computed_at" winner via ROW_NUMBER, and the three-arm snapshot
// filter (pinned id / config-hash / unpinned) is applied per-row via
// correlated subqueries that reference cr.resource_type. Honors the
// same snapshot / configHash filters as LatestComputeResult.
func (s *sqliteStore) LatestComputeResults(ctx context.Context, types []resource.Type) (map[resource.Type]*ComputeResult, error) {
	out := make(map[resource.Type]*ComputeResult, len(types))
	if len(types) == 0 {
		return out, nil
	}
	q, bind := snapshotBatchQuery(
		`SELECT id, resource_type, total_area, feature_count, computed_at, snapshot_id,
		        ROW_NUMBER() OVER (PARTITION BY resource_type ORDER BY computed_at DESC) AS rn`,
		"compute_results",
		"",
		s.snapshotID, s.configHash, types, s.cityID,
	)
	q = `SELECT id, resource_type, total_area, feature_count, computed_at, snapshot_id FROM (` + q + `) WHERE rn = 1`
	rows, err := s.db.QueryContext(ctx, q, bind...)
	if err != nil {
		return nil, fmt.Errorf("query latest compute results: %w", err)
	}
	defer func() { _ = rows.Close() }()
	for rows.Next() {
		var r ComputeResult
		if err := rows.Scan(&r.ID, &r.ResourceType, &r.TotalArea, &r.FeatureCount, &r.ComputedAt, &r.SnapshotID); err != nil {
			return nil, fmt.Errorf("scan compute result: %w", err)
		}
		row := r
		out[r.ResourceType] = &row
	}
	return out, rows.Err()
}

// SaveHexStats appends a batch of hex stats. Each row is tagged with its
// SnapshotID so historic snapshots remain queryable. Append-only: rows from
// prior snapshots are never deleted; pvmt re-runs against a new snapshot
// don't clobber older results. Rows with a nil SnapshotID (legacy data
// from before snapshot tagging existed) coexist as "pre-snapshot" history.
func (s *sqliteStore) SaveHexStats(ctx context.Context, stats []HexStat) error {
	return s.withTx(ctx, func(tx *sql.Tx) error {
		stmt, err := tx.PrepareContext(ctx, `
			INSERT INTO hex_stats (hex_id, resource_type, city_id, snapshot_id, area, pct_covered, computed_at)
			VALUES (?, ?, ?, ?, ?, ?, ?)
		`)
		if err != nil {
			return fmt.Errorf("prepare hex stats insert: %w", err)
		}
		defer func() { _ = stmt.Close() }()

		now := time.Now()
		for _, st := range stats {
			if _, err := stmt.ExecContext(ctx, st.HexID, st.ResourceType, s.cityID, st.SnapshotID, st.Area, st.PctCovered, now); err != nil {
				return fmt.Errorf("insert hex stat %s: %w", st.HexID, err)
			}
		}
		return nil
	})
}

// ListHexStats returns the per-hex coverage rows for a resource.
// Snapshot/config-hash filtering follows the three-arm semantic on
// snapshotFilter — see there for the why and the legacy NULL fallback.
// Filtering rather than "return every snapshot's rows" matters because
// every `pvmt compute` appends a fresh snapshot_id, so the previous
// behavior duplicated output for every caller that didn't pin.
func (s *sqliteStore) ListHexStats(ctx context.Context, resourceType resource.Type) ([]HexStat, error) {
	q, bind := snapshotQuery(
		`SELECT hex_id, resource_type, area, pct_covered, computed_at, snapshot_id FROM hex_stats`,
		"hex_stats",
		"",
		s.snapshotID, s.configHash, resourceType, s.cityID,
	)
	rows, err := s.db.QueryContext(ctx, q, bind...)
	if err != nil {
		return nil, fmt.Errorf("query hex stats: %w", err)
	}
	defer func() { _ = rows.Close() }()

	var stats []HexStat
	for rows.Next() {
		var st HexStat
		if err := rows.Scan(&st.HexID, &st.ResourceType, &st.Area, &st.PctCovered, &st.ComputedAt, &st.SnapshotID); err != nil {
			return nil, fmt.Errorf("scan hex stat: %w", err)
		}
		stats = append(stats, st)
	}
	return stats, rows.Err()
}

func (s *sqliteStore) CreateSnapshot(ctx context.Context, configHash string) (*Snapshot, error) {
	result, err := s.db.ExecContext(ctx, `INSERT INTO snapshots (city_id, config_hash) VALUES (?, ?)`, s.cityID, configHash)
	if err != nil {
		return nil, fmt.Errorf("create snapshot: %w", err)
	}
	id, err := result.LastInsertId()
	if err != nil {
		return nil, fmt.Errorf("get snapshot id: %w", err)
	}
	snap := &Snapshot{ID: id, ConfigHash: configHash}
	if err := s.db.QueryRowContext(ctx, `SELECT computed_at FROM snapshots WHERE id = ? AND city_id = ?`, id, s.cityID).Scan(&snap.ComputedAt); err != nil {
		return nil, fmt.Errorf("get snapshot timestamp: %w", err)
	}
	return snap, nil
}

func (s *sqliteStore) ListSnapshots(ctx context.Context) ([]Snapshot, error) {
	rows, err := s.db.QueryContext(ctx, `SELECT id, computed_at, config_hash FROM snapshots WHERE city_id = ? ORDER BY computed_at DESC`, s.cityID)
	if err != nil {
		return nil, fmt.Errorf("query snapshots: %w", err)
	}
	defer func() { _ = rows.Close() }()

	var snapshots []Snapshot
	for rows.Next() {
		var snap Snapshot
		if err := rows.Scan(&snap.ID, &snap.ComputedAt, &snap.ConfigHash); err != nil {
			return nil, fmt.Errorf("scan snapshot: %w", err)
		}
		snapshots = append(snapshots, snap)
	}
	return snapshots, rows.Err()
}

// SaveForecastResults appends forecast rows tagged with their SnapshotID.
// Append-only — see SaveHexStats for the rationale.
func (s *sqliteStore) SaveForecastResults(ctx context.Context, results []ForecastResult) error {
	return s.withTx(ctx, func(tx *sql.Tx) error {
		stmt, err := tx.PrepareContext(ctx, `
			INSERT INTO forecast_results (resource_type, city_id, year, pci, area, treatment_cost, treatment_tier, snapshot_id, computed_at)
			VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?)
		`)
		if err != nil {
			return fmt.Errorf("prepare forecast insert: %w", err)
		}
		defer func() { _ = stmt.Close() }()

		now := time.Now()
		for _, r := range results {
			if _, err := stmt.ExecContext(ctx, r.ResourceType, s.cityID, r.Year, r.PCI, r.Area, r.TreatmentCost, r.TreatmentTier, r.SnapshotID, now); err != nil {
				return fmt.Errorf("insert forecast year %d: %w", r.Year, err)
			}
		}
		return nil
	})
}

// ListForecastResults: snapshot/config-hash filtering via snapshotFilter
// — see that helper for the three-arm semantic. ORDER BY year keeps
// the per-snapshot rows in display order.
func (s *sqliteStore) ListForecastResults(ctx context.Context, resourceType resource.Type) ([]ForecastResult, error) {
	q, bind := snapshotQuery(
		`SELECT id, resource_type, year, pci, area, treatment_cost, treatment_tier, snapshot_id, computed_at FROM forecast_results`,
		"forecast_results",
		` ORDER BY year`,
		s.snapshotID, s.configHash, resourceType, s.cityID,
	)
	rows, err := s.db.QueryContext(ctx, q, bind...)
	if err != nil {
		return nil, fmt.Errorf("query forecasts: %w", err)
	}
	defer func() { _ = rows.Close() }()

	var results []ForecastResult
	for rows.Next() {
		var r ForecastResult
		if err := rows.Scan(&r.ID, &r.ResourceType, &r.Year, &r.PCI, &r.Area, &r.TreatmentCost, &r.TreatmentTier, &r.SnapshotID, &r.ComputedAt); err != nil {
			return nil, fmt.Errorf("scan forecast: %w", err)
		}
		results = append(results, r)
	}
	return results, rows.Err()
}

// SaveCohortStats appends cohort rows tagged with their SnapshotID.
// Append-only — see SaveHexStats for the rationale.
func (s *sqliteStore) SaveCohortStats(ctx context.Context, stats []CohortStat) error {
	return s.withTx(ctx, func(tx *sql.Tx) error {
		stmt, err := tx.PrepareContext(ctx, `
			INSERT INTO cohort_stats (resource_type, city_id, classification, area, feature_count, snapshot_id, computed_at)
			VALUES (?, ?, ?, ?, ?, ?, ?)
		`)
		if err != nil {
			return fmt.Errorf("prepare cohort stats insert: %w", err)
		}
		defer func() { _ = stmt.Close() }()

		now := time.Now()
		for _, st := range stats {
			if _, err := stmt.ExecContext(ctx, st.ResourceType, s.cityID, st.Classification, st.Area, st.FeatureCount, st.SnapshotID, now); err != nil {
				return fmt.Errorf("insert cohort stat %s/%s: %w", st.ResourceType, st.Classification, err)
			}
		}
		return nil
	})
}

// ListCohortStatsForTypes batches the per-type cohort-stats lookup into
// one query. Same three-arm snapshot filter as ListCohortStats, applied
// per-row via correlated subqueries on cr.resource_type. Missing types
// are absent from the returned map (zero-row result is not an error).
func (s *sqliteStore) ListCohortStatsForTypes(ctx context.Context, types []resource.Type) (map[resource.Type][]CohortStat, error) {
	out := make(map[resource.Type][]CohortStat, len(types))
	if len(types) == 0 {
		return out, nil
	}
	q, bind := snapshotBatchQuery(
		`SELECT id, resource_type, classification, area, feature_count, snapshot_id, computed_at`,
		"cohort_stats",
		"",
		s.snapshotID, s.configHash, types, s.cityID,
	)
	rows, err := s.db.QueryContext(ctx, q, bind...)
	if err != nil {
		return nil, fmt.Errorf("query cohort stats: %w", err)
	}
	defer func() { _ = rows.Close() }()
	for rows.Next() {
		var st CohortStat
		if err := rows.Scan(&st.ID, &st.ResourceType, &st.Classification, &st.Area, &st.FeatureCount, &st.SnapshotID, &st.ComputedAt); err != nil {
			return nil, fmt.Errorf("scan cohort stat: %w", err)
		}
		out[st.ResourceType] = append(out[st.ResourceType], st)
	}
	return out, rows.Err()
}

// ListCohortStats: snapshot/config-hash filtering via snapshotFilter —
// see that helper for the three-arm semantic.
func (s *sqliteStore) ListCohortStats(ctx context.Context, resourceType resource.Type) ([]CohortStat, error) {
	q, bind := snapshotQuery(
		`SELECT id, resource_type, classification, area, feature_count, snapshot_id, computed_at FROM cohort_stats`,
		"cohort_stats",
		"",
		s.snapshotID, s.configHash, resourceType, s.cityID,
	)
	rows, err := s.db.QueryContext(ctx, q, bind...)
	if err != nil {
		return nil, fmt.Errorf("query cohort stats: %w", err)
	}
	defer func() { _ = rows.Close() }()

	var stats []CohortStat
	for rows.Next() {
		var st CohortStat
		if err := rows.Scan(&st.ID, &st.ResourceType, &st.Classification, &st.Area, &st.FeatureCount, &st.SnapshotID, &st.ComputedAt); err != nil {
			return nil, fmt.Errorf("scan cohort stat: %w", err)
		}
		stats = append(stats, st)
	}
	return stats, rows.Err()
}

func (s *sqliteStore) SaveBoundary(ctx context.Context, geometryJSON, source string) error {
	_, err := s.db.ExecContext(ctx, `
		INSERT OR REPLACE INTO city_boundaries (city_id, geometry_json, source, fetched_at)
		VALUES (?, ?, ?, CURRENT_TIMESTAMP)
	`, s.cityID, geometryJSON, source)
	if err != nil {
		return fmt.Errorf("save boundary: %w", err)
	}
	return nil
}

func (s *sqliteStore) GetBoundary(ctx context.Context) (string, error) {
	var gj string
	err := s.db.QueryRowContext(ctx, `SELECT geometry_json FROM city_boundaries WHERE city_id = ?`, s.cityID).Scan(&gj)
	if err != nil {
		if errors.Is(err, sql.ErrNoRows) {
			return "", nil
		}
		return "", fmt.Errorf("get boundary: %w", err)
	}
	return gj, nil
}

func (s *sqliteStore) Stats(ctx context.Context, resourceType resource.Type) (*StatusInfo, error) {
	info := &StatusInfo{ResourceType: resourceType}

	err := s.db.QueryRowContext(ctx, `SELECT COUNT(*) FROM features WHERE resource_type = ? AND city_id = ?`, resourceType, s.cityID).Scan(&info.FeatureCount)
	if err != nil {
		return nil, fmt.Errorf("count features: %w", err)
	}

	// ORDER BY ... LIMIT 1 + sql.ErrNoRows cleanly distinguishes "no
	// features yet" (leave LastIngestAt nil) from a real query failure
	// (locked DB, transient I/O) which previously folded indistinguishably
	// into the same "no features" branch. modernc.org/sqlite scans the
	// TEXT-stored timestamp into *time.Time directly; sql.NullTime would
	// route through stdlib's convertAssign and fail to parse the format.
	var t time.Time
	err = s.db.QueryRowContext(ctx, `SELECT fetched_at FROM features WHERE resource_type = ? AND city_id = ? ORDER BY fetched_at DESC LIMIT 1`, resourceType, s.cityID).Scan(&t)
	switch {
	case errors.Is(err, sql.ErrNoRows):
		// no features yet
	case err != nil:
		return nil, fmt.Errorf("latest fetched_at: %w", err)
	default:
		info.LastIngestAt = &t
	}

	// Same ErrNoRows-vs-real-failure distinction as fetched_at above:
	// LatestComputeResult returns raw sql.ErrNoRows when a city has not been
	// computed yet, but a genuine query failure must propagate rather than
	// silently report TotalArea=0 / LastComputeAt=nil ("never computed").
	result, err := s.LatestComputeResult(ctx, resourceType)
	switch {
	case errors.Is(err, sql.ErrNoRows):
		// no compute yet
	case err != nil:
		return nil, fmt.Errorf("latest compute result: %w", err)
	default:
		info.LastComputeAt = &result.ComputedAt
		info.TotalArea = result.TotalArea
	}

	return info, nil
}

func (s *sqliteStore) ResourceTypes(ctx context.Context) ([]resource.Type, error) {
	rows, err := s.db.QueryContext(ctx, `SELECT DISTINCT resource_type FROM features WHERE city_id = ? ORDER BY resource_type`, s.cityID)
	if err != nil {
		return nil, err
	}
	defer func() { _ = rows.Close() }()
	var types []resource.Type
	for rows.Next() {
		var rt resource.Type
		if err := rows.Scan(&rt); err != nil {
			return nil, err
		}
		types = append(types, rt)
	}
	return types, rows.Err()
}

func (s *sqliteStore) Close() error {
	return s.db.Close()
}

// withTx runs fn inside a write transaction. If fn returns an error the
// tx is rolled back and that error is returned unwrapped (so callers wrap
// per-operation context inside fn). On success the tx is committed; a
// Begin or Commit failure is wrapped with a short prefix.
func (s *sqliteStore) withTx(ctx context.Context, fn func(*sql.Tx) error) error {
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return fmt.Errorf("begin tx: %w", err)
	}
	defer func() { _ = tx.Rollback() }()
	if err := fn(tx); err != nil {
		return err
	}
	if err := tx.Commit(); err != nil {
		return fmt.Errorf("commit: %w", err)
	}
	return nil
}

// WithSnapshot returns a Store that filters snapshot-aware reads
// (LatestComputeResult, ListHexStats, ListCohortStats, ListForecastResults)
// to the given snapshot id. A snapshotID of 0 returns an unpinned view.
// When combined with WithConfigHash, the snapshot pin wins (it's the
// more specific request, matching how the server handles ?snapshot=N).
// The underlying DB connection is shared.
func (s *sqliteStore) WithSnapshot(snapshotID int64) Store {
	cp := *s
	cp.snapshotID = snapshotID
	return &cp
}

// WithConfigHash returns a Store that scopes unpinned snapshot-aware
// reads to snapshots whose config_hash matches the given value. Used
// by the export and serve paths to pick the snapshot written by the
// same Config the caller has loaded, so two examples sharing a city
// slug at different hex_edge_m values don't read each other's
// (incompatible) hex_id namespace.
//
// An empty hash is the unpinned default and preserves the
// "latest snapshot per (city, resource_type)" fallback.
//
// The underlying DB connection is shared.
func (s *sqliteStore) WithConfigHash(configHash string) Store {
	cp := *s
	cp.configHash = configHash
	return &cp
}

// snapshotQuery composes a full SQL query whose WHERE clause carries
// the three-arm snapshot/config-hash filter, and returns the bind
// values in positional order. The caller supplies the SELECT preamble
// (everything before `WHERE resource_type = ? AND city_id = ?`), the
// table name (also used inside the filter's subqueries), and the
// optional suffix (e.g. `ORDER BY computed_at DESC LIMIT 1`).
//
// Behavior of the three arms:
//
//   - arm 1: pinned (snapshotID > 0) — exact match. Wins over configHash.
//   - arm 2: unpinned + configHash set — latest snapshot for this
//     (city, resource_type) whose snapshots.config_hash matches.
//   - arm 3: unpinned + no configHash — latest snapshot overall for
//     this (city, resource_type). Preserves the back-compat path for
//     tests and any caller that constructs a store without a config.
//
// SQLite's NULL-aware `IS` operator makes both arm-2 and arm-3
// gracefully surface pre-migration-002 legacy rows: MAX over an empty
// set returns NULL, and `snapshot_id IS NULL` then matches the legacy
// rows.
//
// The table name is interpolated into the SQL but it always comes from
// a string literal at the call site (never from user input), so the
// G202 concatenation pattern that gosec normally flags is safe here.
// gosec doesn't fire because the concatenation sits inside this
// helper rather than at the QueryContext call site — kept centralized
// for that reason.
// snapshotBatchQuery is the multi-resource_type variant of snapshotQuery.
// Same three-arm semantic, but resource_type is filtered via IN(...) and
// the inner correlated subqueries that resolve "latest snapshot for this
// resource type" reference the outer alias `cr.resource_type` so each row
// gets its own per-type latest. Used by LatestComputeResults and
// ListCohortStatsForTypes — collapsing the per-resource loop into one
// round trip.
func snapshotBatchQuery(selectClause, table, suffix string, snapshotID int64, configHash string, types []resource.Type, cityID int64) (string, []any) {
	placeholders := strings.Repeat("?,", len(types))
	placeholders = placeholders[:len(placeholders)-1]
	q := selectClause + ` FROM ` + table + ` cr
	  WHERE cr.city_id = ? AND cr.resource_type IN (` + placeholders + `)
	  AND (
	    (? > 0 AND cr.snapshot_id = ?)
	    OR (? = 0 AND ? != '' AND cr.snapshot_id IS (
	      SELECT MAX(hs.snapshot_id) FROM ` + table + ` hs
	      JOIN snapshots s ON s.id = hs.snapshot_id
	      WHERE hs.resource_type = cr.resource_type AND hs.city_id = ?
	        AND s.config_hash = ?
	    ))
	    OR (? = 0 AND ? = '' AND cr.snapshot_id IS (
	      SELECT MAX(snapshot_id) FROM ` + table + `
	      WHERE resource_type = cr.resource_type AND city_id = ?
	    ))
	  )` + suffix
	args := make([]any, 0, 2+len(types)+10)
	args = append(args, cityID)
	for _, t := range types {
		args = append(args, t)
	}
	args = append(args,
		snapshotID, snapshotID, // arm 1
		snapshotID, configHash, cityID, configHash, // arm 2
		snapshotID, configHash, cityID, // arm 3
	)
	return q, args
}

func snapshotQuery(selectClause, table, suffix string, snapshotID int64, configHash string, resourceType resource.Type, cityID int64) (string, []any) {
	q := selectClause + `
	  WHERE resource_type = ? AND city_id = ?
	  AND (
	    (? > 0 AND snapshot_id = ?)
	    OR (? = 0 AND ? != '' AND snapshot_id IS (
	      SELECT MAX(hs.snapshot_id) FROM ` + table + ` hs
	      JOIN snapshots s ON s.id = hs.snapshot_id
	      WHERE hs.resource_type = ? AND hs.city_id = ?
	        AND s.config_hash = ?
	    ))
	    OR (? = 0 AND ? = '' AND snapshot_id IS (
	      SELECT MAX(snapshot_id) FROM ` + table + `
	      WHERE resource_type = ? AND city_id = ?
	    ))
	  )` + suffix
	return q, []any{
		resourceType, cityID, // outer WHERE
		snapshotID, snapshotID, // arm 1
		snapshotID, configHash, resourceType, cityID, configHash, // arm 2
		snapshotID, configHash, resourceType, cityID, // arm 3
	}
}

// DeleteSnapshot removes the snapshot and every FK-linked result row in a
// single transaction. Scoped to this city: a snapshot owned by a different
// city is treated as not found (returns false, nil) so the city scope
// guards against cross-city deletes. Returns true if a row was deleted.
//
// The schema declares snapshot_id columns without ON DELETE CASCADE, so
// the dependent deletes are explicit. Order doesn't matter once we're
// inside a tx — foreign_keys=on enforces at commit time — but we delete
// children first to keep intent obvious.
func (s *sqliteStore) DeleteSnapshot(ctx context.Context, snapshotID int64) (bool, error) {
	if snapshotID <= 0 {
		return false, fmt.Errorf("invalid snapshot id %d", snapshotID)
	}
	var deleted bool
	err := s.withTx(ctx, func(tx *sql.Tx) error {
		var owned int64
		err := tx.QueryRowContext(ctx,
			`SELECT id FROM snapshots WHERE id = ? AND city_id = ?`,
			snapshotID, s.cityID).Scan(&owned)
		if err != nil {
			if errors.Is(err, sql.ErrNoRows) {
				return nil
			}
			return fmt.Errorf("lookup snapshot: %w", err)
		}

		// Statements are static literals (gosec G202): table names are not
		// derived from user input, but listing the four deletes explicitly
		// keeps that fact obvious to readers and to the linter.
		deletes := []struct {
			label string
			sql   string
		}{
			{"compute_results", `DELETE FROM compute_results WHERE snapshot_id = ? AND city_id = ?`},
			{"hex_stats", `DELETE FROM hex_stats WHERE snapshot_id = ? AND city_id = ?`},
			{"forecast_results", `DELETE FROM forecast_results WHERE snapshot_id = ? AND city_id = ?`},
			{"cohort_stats", `DELETE FROM cohort_stats WHERE snapshot_id = ? AND city_id = ?`},
		}
		for _, d := range deletes {
			if _, err := tx.ExecContext(ctx, d.sql, snapshotID, s.cityID); err != nil {
				return fmt.Errorf("delete from %s: %w", d.label, err)
			}
		}
		if _, err := tx.ExecContext(ctx,
			`DELETE FROM snapshots WHERE id = ? AND city_id = ?`,
			snapshotID, s.cityID); err != nil {
			return fmt.Errorf("delete snapshot: %w", err)
		}
		deleted = true
		return nil
	})
	return deleted, err
}

// ResolveSnapshot returns nil iff the given snapshot id exists and belongs
// to this city. Returns sql.ErrNoRows for unknown or wrong-city ids. The
// server handler uses this to translate ?snapshot=<id> into a 404 instead
// of letting a non-matching filter silently return empty data.
func (s *sqliteStore) ResolveSnapshot(ctx context.Context, snapshotID int64) error {
	if snapshotID <= 0 {
		return fmt.Errorf("invalid snapshot id %d", snapshotID)
	}
	var n int64
	err := s.db.QueryRowContext(ctx,
		`SELECT id FROM snapshots WHERE city_id = ? AND id = ?`,
		s.cityID, snapshotID).Scan(&n)
	if err != nil {
		return err
	}
	return nil
}
