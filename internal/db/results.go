package db

import (
	"context"
	"database/sql"
	"fmt"
)

// Every read in this file sees only the runs of the ONE snapshot the view
// resolves (see viewQuery), so a reader never mixes runs from two snapshots.
// Every write tags rows with the run that produced them; nothing is visible
// to readers until a snapshot references that run.
//
// Results are long-format metric rows (city_metrics, hex_metrics in
// 001_init.sql). This layer interprets no metric: internal/results pivots
// the pavement keys into Go types, and internal/hexmetric registers the rest.

// listView runs a viewQuery and scans every row. No snapshot, or no rows,
// returns (nil, nil), never sql.ErrNoRows: the server's cache-eviction logic
// relies on that for genuinely empty cities. A pinned view checks its
// snapshot again after reading: a delete that committed between resolving the
// runs and the SELECT would otherwise read as an empty result, and a snapshot
// still present after the read was present for all of it (ids are never
// reused), so the rows are complete.
func listView[T any](ctx context.Context, s *sqliteStore, what, selectClause string, where viewFilter, suffix string, scan func(*sql.Rows) (T, error)) ([]T, error) {
	q, args, ok, err := s.viewQuery(ctx, selectClause, where, suffix)
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
	if err := rows.Err(); err != nil {
		return nil, err
	}
	if err := s.checkPin(ctx, s.db); err != nil {
		return nil, err
	}
	return out, nil
}

// SaveCityMetrics writes rows under runID in one transaction.
func (s *sqliteStore) SaveCityMetrics(ctx context.Context, runID int64, rows []CityMetric) error {
	return s.withTx(ctx, func(tx *sql.Tx) error {
		stmt, err := tx.PrepareContext(ctx,
			`INSERT INTO city_metrics (run_id, metric, subject, class, value) VALUES (?, ?, ?, ?, ?)`)
		if err != nil {
			return fmt.Errorf("prepare city metrics insert: %w", err)
		}
		defer func() { _ = stmt.Close() }()
		for _, m := range rows {
			if _, err := stmt.ExecContext(ctx, runID, m.Metric, m.Subject, m.Class, m.Value); err != nil {
				return fmt.Errorf("insert city metric %s/%s/%s: %w", m.Metric, m.Subject, m.Class, err)
			}
		}
		return nil
	})
}

// ListCityMetrics returns every measure over subjects in the resolved
// snapshot. Result rows carry no timestamp; ComputedAt is the run's.
func (s *sqliteStore) ListCityMetrics(ctx context.Context, subjects []string) ([]CityMetric, error) {
	return listView(ctx, s, "city metric",
		`SELECT metric, subject, class, value, city_metrics.run_id, runs.computed_at
		 FROM city_metrics JOIN runs ON runs.id = city_metrics.run_id`,
		in("subject", subjects), ` ORDER BY subject, class, metric, city_metrics.run_id`,
		func(rows *sql.Rows) (CityMetric, error) {
			var m CityMetric
			err := rows.Scan(&m.Metric, &m.Subject, &m.Class, &m.Value, &m.RunID, &m.ComputedAt)
			m.ComputedAt = canonicalTime(m.ComputedAt)
			return m, err
		})
}

// SaveHexMetrics writes rows under runID in one transaction.
func (s *sqliteStore) SaveHexMetrics(ctx context.Context, runID int64, rows []HexMetric) error {
	return s.withTx(ctx, func(tx *sql.Tx) error {
		stmt, err := tx.PrepareContext(ctx,
			`INSERT INTO hex_metrics (run_id, hex_id, metric, subject, value, ref) VALUES (?, ?, ?, ?, ?, ?)`)
		if err != nil {
			return fmt.Errorf("prepare hex metrics insert: %w", err)
		}
		defer func() { _ = stmt.Close() }()
		for _, m := range rows {
			if _, err := stmt.ExecContext(ctx, runID, m.HexID, m.Metric, m.Subject, m.Value, m.Ref); err != nil {
				return fmt.Errorf("insert hex metric %s/%s/%s: %w", m.Metric, m.Subject, m.HexID, err)
			}
		}
		return nil
	})
}

// ListHexMetrics returns one metric's rows over subject in the resolved
// snapshot, ordered by hex id.
func (s *sqliteStore) ListHexMetrics(ctx context.Context, metric, subject string) ([]HexMetric, error) {
	return listView(ctx, s, "hex metric",
		`SELECT hex_id, metric, subject, value, ref, hex_metrics.run_id, runs.computed_at
		 FROM hex_metrics JOIN runs ON runs.id = hex_metrics.run_id`,
		and(eq("metric", metric), eq("subject", subject)), ` ORDER BY hex_id, hex_metrics.run_id`,
		func(rows *sql.Rows) (HexMetric, error) {
			var m HexMetric
			err := rows.Scan(&m.HexID, &m.Metric, &m.Subject, &m.Value, &m.Ref, &m.RunID, &m.ComputedAt)
			m.ComputedAt = canonicalTime(m.ComputedAt)
			return m, err
		})
}

// HasHexMetric answers "would ListHexMetrics return anything for metric over
// any of subjects" with one LIMIT 1 query rather than loading every row.
func (s *sqliteStore) HasHexMetric(ctx context.Context, metric string, subjects []string) (bool, error) {
	rows, err := listView(ctx, s, "hex metric existence", `SELECT 1 FROM hex_metrics`,
		and(eq("metric", metric), in("subject", subjects)), ` LIMIT 1`,
		func(rows *sql.Rows) (int, error) {
			var one int
			err := rows.Scan(&one)
			return one, err
		})
	return len(rows) > 0, err
}

// SaveRunFacilities writes the facilities runID routed from in one
// transaction.
func (s *sqliteStore) SaveRunFacilities(ctx context.Context, runID int64, facilities []RunFacility) error {
	return s.withTx(ctx, func(tx *sql.Tx) error {
		stmt, err := tx.PrepareContext(ctx,
			`INSERT INTO run_facilities (run_id, facility_id, name, source, lon, lat) VALUES (?, ?, ?, ?, ?, ?)`)
		if err != nil {
			return fmt.Errorf("prepare run facilities insert: %w", err)
		}
		defer func() { _ = stmt.Close() }()
		for _, f := range facilities {
			if _, err := stmt.ExecContext(ctx, runID, f.ID, f.Name, f.Source, f.Lon, f.Lat); err != nil {
				return fmt.Errorf("insert run facility %s: %w", f.ID, err)
			}
		}
		return nil
	})
}

// ListRunFacilities returns the facilities of the resolved snapshot's run for
// producer, ordered by id.
func (s *sqliteStore) ListRunFacilities(ctx context.Context, producer string) ([]RunFacility, error) {
	return listView(ctx, s, "run facility",
		`SELECT facility_id, name, source, lon, lat FROM run_facilities`,
		viewFilter{sql: `run_id IN (SELECT id FROM runs WHERE producer = ?)`, args: []any{producer}},
		` ORDER BY facility_id`,
		func(rows *sql.Rows) (RunFacility, error) {
			var f RunFacility
			err := rows.Scan(&f.ID, &f.Name, &f.Source, &f.Lon, &f.Lat)
			return f, err
		})
}
