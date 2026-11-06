package db

import (
	"context"
	"database/sql"
	"fmt"
	"maps"
	"slices"
	"time"

	"github.com/jcrussell/solvent-streets/internal/resource"
)

// Every read in this file sees only the runs of the ONE snapshot the view
// resolves (see viewQuery), so a reader never mixes runs from two snapshots.
// Every write tags rows with the run that produced them; nothing is visible
// to readers until a snapshot references that run.

func typeKeys(types []resource.Type) []string {
	keys := make([]string, len(types))
	for i, t := range types {
		keys[i] = string(t)
	}
	return keys
}

// listView runs a viewQuery over one key column and scans every row. No
// snapshot, or no rows, returns (nil, nil), never sql.ErrNoRows: the server's
// cache-eviction logic relies on that for genuinely empty cities.
func listView[T any](ctx context.Context, s *sqliteStore, what, selectClause, keyColumn string, keys []string, suffix string, scan func(*sql.Rows) (T, error)) ([]T, error) {
	q, args, ok, err := s.viewQuery(ctx, selectClause, keyColumn, keys, suffix)
	if err != nil || !ok {
		return nil, err
	}
	rows, err := s.db.QueryContext(ctx, q, args...)
	if err != nil {
		return nil, fmt.Errorf("query %ss: %w", what, err)
	}
	defer func() { _ = rows.Close() }()
	var out []T
	for rows.Next() {
		row, err := scan(rows)
		if err != nil {
			return nil, fmt.Errorf("scan %s: %w", what, err)
		}
		out = append(out, row)
	}
	return out, rows.Err()
}

func (s *sqliteStore) SaveComputeResult(ctx context.Context, result ComputeResult) error {
	_, err := s.db.ExecContext(ctx, `
		INSERT INTO compute_results (resource_type, city_id, total_area, feature_count, estimated_spaces, computed_at, run_id)
		VALUES (?, ?, ?, ?, ?, ?, ?)
	`, result.ResourceType, s.cityID, result.TotalArea, result.FeatureCount, result.EstimatedSpaces, time.Now(), result.RunID)
	if err != nil {
		return fmt.Errorf("insert compute result: %w", err)
	}
	return nil
}

const computeResultColumns = `id, resource_type, total_area, feature_count, estimated_spaces, computed_at, run_id`

func scanComputeResult(rows interface{ Scan(...any) error }) (ComputeResult, error) {
	var r ComputeResult
	err := rows.Scan(&r.ID, &r.ResourceType, &r.TotalArea, &r.FeatureCount, &r.EstimatedSpaces, &r.ComputedAt, &r.RunID)
	return r, err
}

// LatestComputeResult returns the resource's compute result in the resolved
// snapshot, or raw sql.ErrNoRows when it has none. A run writes one row per
// resource type, so the ORDER BY only makes the pick deterministic.
func (s *sqliteStore) LatestComputeResult(ctx context.Context, resourceType resource.Type) (*ComputeResult, error) {
	q, args, ok, err := s.viewQuery(ctx, `SELECT `+computeResultColumns+` FROM compute_results`,
		"resource_type", []string{string(resourceType)}, ` ORDER BY computed_at DESC, id DESC LIMIT 1`)
	if err != nil {
		return nil, err
	}
	if !ok {
		return nil, sql.ErrNoRows
	}
	r, err := scanComputeResult(s.db.QueryRowContext(ctx, q, args...))
	if err != nil {
		return nil, err
	}
	return &r, nil
}

// LatestComputeResults is LatestComputeResult for several types in one query.
// Missing types are absent from the map (no sql.ErrNoRows).
func (s *sqliteStore) LatestComputeResults(ctx context.Context, types []resource.Type) (map[resource.Type]*ComputeResult, error) {
	out := make(map[resource.Type]*ComputeResult, len(types))
	rows, err := listView(ctx, s, "compute result", `SELECT `+computeResultColumns+` FROM compute_results`,
		"resource_type", typeKeys(types), ` ORDER BY computed_at DESC, id DESC`,
		func(rows *sql.Rows) (ComputeResult, error) { return scanComputeResult(rows) })
	if err != nil {
		return nil, err
	}
	for _, r := range rows {
		if _, seen := out[r.ResourceType]; !seen {
			row := r
			out[r.ResourceType] = &row
		}
	}
	return out, nil
}

// SaveHexStats appends a batch of hex stats, each tagged with its run.
func (s *sqliteStore) SaveHexStats(ctx context.Context, stats []HexStat) error {
	return s.withTx(ctx, func(tx *sql.Tx) error {
		stmt, err := tx.PrepareContext(ctx, `
			INSERT INTO hex_stats (hex_id, resource_type, city_id, run_id, area, pct_covered, computed_at)
			VALUES (?, ?, ?, ?, ?, ?, ?)
		`)
		if err != nil {
			return fmt.Errorf("prepare hex stats insert: %w", err)
		}
		defer func() { _ = stmt.Close() }()

		now := time.Now()
		for _, st := range stats {
			if _, err := stmt.ExecContext(ctx, st.HexID, st.ResourceType, s.cityID, st.RunID, st.Area, st.PctCovered, now); err != nil {
				return fmt.Errorf("insert hex stat %s: %w", st.HexID, err)
			}
		}
		return nil
	})
}

// ListHexStats returns the resource's per-hex coverage rows in the resolved
// snapshot.
func (s *sqliteStore) ListHexStats(ctx context.Context, resourceType resource.Type) ([]HexStat, error) {
	return listView(ctx, s, "hex stat",
		`SELECT hex_id, resource_type, area, pct_covered, computed_at, run_id FROM hex_stats`,
		"resource_type", []string{string(resourceType)}, "",
		func(rows *sql.Rows) (HexStat, error) {
			var st HexStat
			err := rows.Scan(&st.HexID, &st.ResourceType, &st.Area, &st.PctCovered, &st.ComputedAt, &st.RunID)
			return st, err
		})
}

// HasHexStats answers "would ListHexStats return anything for any of types"
// with one LIMIT 1 query rather than loading every row.
func (s *sqliteStore) HasHexStats(ctx context.Context, types []resource.Type) (bool, error) {
	rows, err := listView(ctx, s, "hex stat existence", `SELECT 1 FROM hex_stats`,
		"resource_type", typeKeys(types), ` LIMIT 1`,
		func(rows *sql.Rows) (int, error) {
			var one int
			err := rows.Scan(&one)
			return one, err
		})
	return len(rows) > 0, err
}

// SaveForecastResults appends forecast rows, each tagged with its run. A
// forecast rerun is a new run, so nothing is replaced in place.
func (s *sqliteStore) SaveForecastResults(ctx context.Context, results []ForecastResult) error {
	return s.withTx(ctx, func(tx *sql.Tx) error {
		stmt, err := tx.PrepareContext(ctx, `
			INSERT INTO forecast_results (resource_type, city_id, year, pci, area, treatment_cost, treatment_tier, run_id, computed_at)
			VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?)
		`)
		if err != nil {
			return fmt.Errorf("prepare forecast insert: %w", err)
		}
		defer func() { _ = stmt.Close() }()

		now := time.Now()
		for _, r := range results {
			if _, err := stmt.ExecContext(ctx, r.ResourceType, s.cityID, r.Year, r.PCI, r.Area, r.AnnualNeed, r.CostTier, r.RunID, now); err != nil {
				return fmt.Errorf("insert forecast year %d: %w", r.Year, err)
			}
		}
		return nil
	})
}

// SaveCohortStats appends cohort rows, each tagged with its run.
func (s *sqliteStore) SaveCohortStats(ctx context.Context, stats []CohortStat) error {
	return s.withTx(ctx, func(tx *sql.Tx) error {
		stmt, err := tx.PrepareContext(ctx, `
			INSERT INTO cohort_stats (resource_type, city_id, classification, area, feature_count, run_id, computed_at)
			VALUES (?, ?, ?, ?, ?, ?, ?)
		`)
		if err != nil {
			return fmt.Errorf("prepare cohort stats insert: %w", err)
		}
		defer func() { _ = stmt.Close() }()

		now := time.Now()
		for _, st := range stats {
			if _, err := stmt.ExecContext(ctx, st.ResourceType, s.cityID, st.Classification, st.Area, st.FeatureCount, st.RunID, now); err != nil {
				return fmt.Errorf("insert cohort stat %s/%s: %w", st.ResourceType, st.Classification, err)
			}
		}
		return nil
	})
}

const cohortColumns = `id, resource_type, classification, area, feature_count, run_id, computed_at`

func scanCohortStat(rows *sql.Rows) (CohortStat, error) {
	var st CohortStat
	err := rows.Scan(&st.ID, &st.ResourceType, &st.Classification, &st.Area, &st.FeatureCount, &st.RunID, &st.ComputedAt)
	return st, err
}

// ListCohortStats returns the resource's cohort rows in the resolved snapshot.
func (s *sqliteStore) ListCohortStats(ctx context.Context, resourceType resource.Type) ([]CohortStat, error) {
	return listView(ctx, s, "cohort stat", `SELECT `+cohortColumns+` FROM cohort_stats`,
		"resource_type", []string{string(resourceType)}, "", scanCohortStat)
}

// ListCohortStatsForTypes is ListCohortStats for several types in one query,
// grouped by type. Missing types are absent from the map.
func (s *sqliteStore) ListCohortStatsForTypes(ctx context.Context, types []resource.Type) (map[resource.Type][]CohortStat, error) {
	out := make(map[resource.Type][]CohortStat, len(types))
	rows, err := listView(ctx, s, "cohort stat", `SELECT `+cohortColumns+` FROM cohort_stats`,
		"resource_type", typeKeys(types), "", scanCohortStat)
	if err != nil {
		return nil, err
	}
	for _, st := range rows {
		out[st.ResourceType] = append(out[st.ResourceType], st)
	}
	return out, nil
}

// SaveHexMetrics replaces this city's (metric, runID) slice with rows inside
// one transaction; see replaceHexMetrics.
func (s *sqliteStore) SaveHexMetrics(ctx context.Context, metric string, runID int64, rows []HexMetric) error {
	return s.SaveHexMetricSet(ctx, runID, map[string][]HexMetric{metric: rows})
}

// SaveHexMetricSet replaces every (metric, runID) slice in set inside a single
// transaction, in sorted key order so the statement sequence is deterministic.
func (s *sqliteStore) SaveHexMetricSet(ctx context.Context, runID int64, set map[string][]HexMetric) error {
	return s.withTx(ctx, func(tx *sql.Tx) error {
		for _, metric := range slices.Sorted(maps.Keys(set)) {
			if err := s.replaceHexMetrics(ctx, tx, metric, runID, set[metric]); err != nil {
				return err
			}
		}
		return nil
	})
}

// replaceHexMetrics deletes this city's (metric, runID) slice and inserts rows
// in its place, so saving the same metric twice within one run keeps the last.
func (s *sqliteStore) replaceHexMetrics(ctx context.Context, tx *sql.Tx, metric string, runID int64, rows []HexMetric) error {
	if _, err := tx.ExecContext(ctx, `DELETE FROM hex_metrics WHERE city_id = ? AND metric = ? AND run_id = ?`,
		s.cityID, metric, runID); err != nil {
		return fmt.Errorf("replace hex metrics %s: %w", metric, err)
	}
	if len(rows) == 0 {
		return nil
	}
	stmt, err := tx.PrepareContext(ctx, `
		INSERT INTO hex_metrics (hex_id, city_id, run_id, metric, value, ref, computed_at)
		VALUES (?, ?, ?, ?, ?, ?, ?)
	`)
	if err != nil {
		return fmt.Errorf("prepare hex metrics insert: %w", err)
	}
	defer func() { _ = stmt.Close() }()
	now := time.Now()
	for _, m := range rows {
		if _, err := stmt.ExecContext(ctx, m.HexID, s.cityID, runID, metric, m.Value, m.Ref, now); err != nil {
			return fmt.Errorf("insert hex metric %s/%s: %w", metric, m.HexID, err)
		}
	}
	return nil
}

// ListHexMetrics returns one metric's rows in the resolved snapshot.
func (s *sqliteStore) ListHexMetrics(ctx context.Context, metric string) ([]HexMetric, error) {
	return listView(ctx, s, "hex metric",
		`SELECT hex_id, metric, value, ref, run_id, computed_at FROM hex_metrics`,
		"metric", []string{metric}, "",
		func(rows *sql.Rows) (HexMetric, error) {
			var m HexMetric
			err := rows.Scan(&m.HexID, &m.Metric, &m.Value, &m.Ref, &m.RunID, &m.ComputedAt)
			return m, err
		})
}
