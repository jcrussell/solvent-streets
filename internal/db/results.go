package db

import (
	"context"
	"database/sql"
	"errors"
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
//
// The Go result types (ComputeResult, HexStat, CohortStat) are views over the
// long-format city_metrics / hex_metrics rows (001_init.sql): one row per
// (run, metric, subject[, class | hex]). Saving one writes its fields as
// metric rows; listing one pivots them back.

// Pavement metric keys (internal/resource/metric.go). Counts are stored as
// REAL and converted back with int() at this boundary.
const (
	metricPavedArea     = resource.MetricPavedArea
	metricCoveragePct   = resource.MetricCoveragePct
	metricFeatureCount  = resource.MetricFeatureCount
	metricParkingSpaces = resource.MetricParkingSpaces
)

// cityTotalClass is city_metrics.class for a city-wide total; any other class
// is a highway classification of the cohort split.
const cityTotalClass = ""

func typeKeys(types []resource.Type) []string {
	keys := make([]string, len(types))
	for i, t := range types {
		keys[i] = string(t)
	}
	return keys
}

// listView runs a viewQuery and scans every row. No snapshot, or no rows,
// returns (nil, nil), never sql.ErrNoRows: the server's cache-eviction logic
// relies on that for genuinely empty cities.
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
	return out, rows.Err()
}

// metricRow is one scanned result row, before pivoting into a Go type. Key is
// the class (city_metrics) or hex id (hex_metrics).
type metricRow struct {
	RunID      int64
	Subject    string
	Key        string
	Metric     string
	Value      float64
	ComputedAt time.Time
}

func scanMetricRow(rows *sql.Rows) (metricRow, error) {
	var r metricRow
	err := rows.Scan(&r.RunID, &r.Subject, &r.Key, &r.Metric, &r.Value, &r.ComputedAt)
	r.ComputedAt = canonicalTime(r.ComputedAt)
	return r, err
}

// The select clauses join runs for computed_at: result rows carry no
// timestamp of their own.
const (
	cityMetricSelect = `SELECT city_metrics.run_id, subject, class, metric, value, runs.computed_at
		FROM city_metrics JOIN runs ON runs.id = city_metrics.run_id`
	hexMetricSelect = `SELECT hex_metrics.run_id, subject, hex_id, metric, value, runs.computed_at
		FROM hex_metrics JOIN runs ON runs.id = hex_metrics.run_id`
)

// insertCityMetric writes one city_metrics row.
func insertCityMetric(ctx context.Context, stmt *sql.Stmt, runID int64, metric string, subject resource.Type, class string, value float64) error {
	if _, err := stmt.ExecContext(ctx, runID, metric, string(subject), class, value); err != nil {
		return fmt.Errorf("insert city metric %s/%s/%s: %w", metric, subject, class, err)
	}
	return nil
}

func prepareCityMetricInsert(ctx context.Context, tx *sql.Tx) (*sql.Stmt, error) {
	stmt, err := tx.PrepareContext(ctx,
		`INSERT INTO city_metrics (run_id, metric, subject, class, value) VALUES (?, ?, ?, ?, ?)`)
	if err != nil {
		return nil, fmt.Errorf("prepare city metrics insert: %w", err)
	}
	return stmt, nil
}

func prepareHexMetricInsert(ctx context.Context, tx *sql.Tx) (*sql.Stmt, error) {
	stmt, err := tx.PrepareContext(ctx,
		`INSERT INTO hex_metrics (run_id, hex_id, metric, subject, value, ref) VALUES (?, ?, ?, ?, ?, ?)`)
	if err != nil {
		return nil, fmt.Errorf("prepare hex metrics insert: %w", err)
	}
	return stmt, nil
}

// SaveComputeResult writes a resource's city totals. Every total is written
// even when 0 (a missing row means "never computed"); parking_spaces only for
// parking subjects.
func (s *sqliteStore) SaveComputeResult(ctx context.Context, result ComputeResult) error {
	return s.withTx(ctx, func(tx *sql.Tx) error {
		stmt, err := prepareCityMetricInsert(ctx, tx)
		if err != nil {
			return err
		}
		defer func() { _ = stmt.Close() }()
		rt, run := result.ResourceType, result.RunID
		if err := insertCityMetric(ctx, stmt, run, metricPavedArea, rt, cityTotalClass, result.TotalArea); err != nil {
			return err
		}
		if err := insertCityMetric(ctx, stmt, run, metricFeatureCount, rt, cityTotalClass, float64(result.FeatureCount)); err != nil {
			return err
		}
		if rt.Bare() == resource.TypeParking {
			return insertCityMetric(ctx, stmt, run, metricParkingSpaces, rt, cityTotalClass, float64(result.EstimatedSpaces))
		}
		return nil
	})
}

// LatestComputeResult returns the resource's compute result in the resolved
// snapshot, or raw sql.ErrNoRows when it has none.
func (s *sqliteStore) LatestComputeResult(ctx context.Context, resourceType resource.Type) (*ComputeResult, error) {
	out, err := s.LatestComputeResults(ctx, []resource.Type{resourceType})
	if err != nil {
		return nil, err
	}
	r, ok := out[resourceType]
	if !ok {
		return nil, sql.ErrNoRows
	}
	return r, nil
}

// LatestComputeResults is LatestComputeResult for several types in one query.
// Missing types are absent from the map (no sql.ErrNoRows). A snapshot holds
// one run per part, so one run writes each subject; should two runs in the
// view ever both write it, the newer run wins.
func (s *sqliteStore) LatestComputeResults(ctx context.Context, types []resource.Type) (map[resource.Type]*ComputeResult, error) {
	rows, err := listView(ctx, s, "compute result", cityMetricSelect,
		and(eq("class", cityTotalClass), in("subject", typeKeys(types))),
		` ORDER BY city_metrics.run_id`, scanMetricRow)
	if err != nil {
		return nil, err
	}
	out := make(map[resource.Type]*ComputeResult, len(types))
	for _, r := range rows {
		rt := resource.Type(r.Subject)
		res, ok := out[rt]
		if !ok || res.RunID != r.RunID {
			res = &ComputeResult{ResourceType: rt, RunID: r.RunID, ComputedAt: r.ComputedAt}
			out[rt] = res
		}
		switch r.Metric {
		case metricPavedArea:
			res.TotalArea = r.Value
		case metricFeatureCount:
			res.FeatureCount = int(r.Value)
		case metricParkingSpaces:
			res.EstimatedSpaces = int(r.Value)
		}
	}
	return out, nil
}

// SaveHexStats appends a batch of per-hex coverage rows, each tagged with its
// run.
func (s *sqliteStore) SaveHexStats(ctx context.Context, stats []HexStat) error {
	return s.withTx(ctx, func(tx *sql.Tx) error {
		stmt, err := prepareHexMetricInsert(ctx, tx)
		if err != nil {
			return err
		}
		defer func() { _ = stmt.Close() }()
		for _, st := range stats {
			if _, err := stmt.ExecContext(ctx, st.RunID, st.HexID, metricPavedArea, string(st.ResourceType), st.Area, ""); err != nil {
				return fmt.Errorf("insert hex stat %s: %w", st.HexID, err)
			}
			if _, err := stmt.ExecContext(ctx, st.RunID, st.HexID, metricCoveragePct, string(st.ResourceType), st.PctCovered, ""); err != nil {
				return fmt.Errorf("insert hex stat %s: %w", st.HexID, err)
			}
		}
		return nil
	})
}

// ListHexStats returns the resource's per-hex coverage rows in the resolved
// snapshot, ordered by hex id.
func (s *sqliteStore) ListHexStats(ctx context.Context, resourceType resource.Type) ([]HexStat, error) {
	rows, err := listView(ctx, s, "hex stat", hexMetricSelect,
		and(eq("subject", string(resourceType)), in("metric", []string{metricPavedArea, metricCoveragePct})),
		` ORDER BY hex_id, hex_metrics.run_id`, scanMetricRow)
	if err != nil {
		return nil, err
	}
	var out []HexStat
	for _, r := range rows {
		if n := len(out); n == 0 || out[n-1].HexID != r.Key || out[n-1].RunID != r.RunID {
			out = append(out, HexStat{HexID: r.Key, ResourceType: resourceType, RunID: r.RunID, ComputedAt: r.ComputedAt})
		}
		st := &out[len(out)-1]
		switch r.Metric {
		case metricPavedArea:
			st.Area = r.Value
		case metricCoveragePct:
			st.PctCovered = r.Value
		}
	}
	return out, nil
}

// HasHexStats answers "would ListHexStats return anything for any of types"
// with one LIMIT 1 query rather than loading every row. Only pavement coverage
// counts: a city with fire or census metrics but no pavement compute has no
// hex stats.
func (s *sqliteStore) HasHexStats(ctx context.Context, types []resource.Type) (bool, error) {
	rows, err := listView(ctx, s, "hex stat existence", `SELECT 1 FROM hex_metrics`,
		and(eq("metric", metricPavedArea), in("subject", typeKeys(types))), ` LIMIT 1`,
		func(rows *sql.Rows) (int, error) {
			var one int
			err := rows.Scan(&one)
			return one, err
		})
	return len(rows) > 0, err
}

// SaveCohortStats appends cohort rows, each tagged with its run.
func (s *sqliteStore) SaveCohortStats(ctx context.Context, stats []CohortStat) error {
	return s.withTx(ctx, func(tx *sql.Tx) error {
		stmt, err := prepareCityMetricInsert(ctx, tx)
		if err != nil {
			return err
		}
		defer func() { _ = stmt.Close() }()
		for _, st := range stats {
			if st.Classification == cityTotalClass {
				return errors.New("insert cohort stat: classification is required")
			}
			if err := insertCityMetric(ctx, stmt, st.RunID, metricPavedArea, st.ResourceType, st.Classification, st.Area); err != nil {
				return err
			}
			if err := insertCityMetric(ctx, stmt, st.RunID, metricFeatureCount, st.ResourceType, st.Classification, float64(st.FeatureCount)); err != nil {
				return err
			}
		}
		return nil
	})
}

// ListCohortStats returns the resource's cohort rows in the resolved
// snapshot, ordered by classification.
func (s *sqliteStore) ListCohortStats(ctx context.Context, resourceType resource.Type) ([]CohortStat, error) {
	byType, err := s.ListCohortStatsForTypes(ctx, []resource.Type{resourceType})
	if err != nil {
		return nil, err
	}
	return byType[resourceType], nil
}

// ListCohortStatsForTypes is ListCohortStats for several types in one query,
// grouped by type. Missing types are absent from the map.
func (s *sqliteStore) ListCohortStatsForTypes(ctx context.Context, types []resource.Type) (map[resource.Type][]CohortStat, error) {
	rows, err := listView(ctx, s, "cohort stat", cityMetricSelect,
		and(ne("class", cityTotalClass), in("subject", typeKeys(types)),
			in("metric", []string{metricPavedArea, metricFeatureCount})),
		` ORDER BY subject, class, city_metrics.run_id`, scanMetricRow)
	if err != nil {
		return nil, err
	}
	out := make(map[resource.Type][]CohortStat, len(types))
	for _, r := range rows {
		rt := resource.Type(r.Subject)
		list := out[rt]
		if n := len(list); n == 0 || list[n-1].Classification != r.Key || list[n-1].RunID != r.RunID {
			list = append(list, CohortStat{ResourceType: rt, Classification: r.Key, RunID: r.RunID, ComputedAt: r.ComputedAt})
		}
		st := &list[len(list)-1]
		switch r.Metric {
		case metricPavedArea:
			st.Area = r.Value
		case metricFeatureCount:
			st.FeatureCount = int(r.Value)
		}
		out[rt] = list
	}
	return out, nil
}

// SaveHexMetrics writes one metric's rows for runID; see SaveHexMetricSet.
func (s *sqliteStore) SaveHexMetrics(ctx context.Context, metric string, runID int64, rows []HexMetric) error {
	return s.SaveHexMetricSet(ctx, runID, map[string][]HexMetric{metric: rows})
}

// SaveHexMetricSet writes every metric in set for runID inside a single
// transaction, in sorted key order so the statement sequence is
// deterministic. Rows are write-once: saving a metric a run already holds
// fails on the primary key.
func (s *sqliteStore) SaveHexMetricSet(ctx context.Context, runID int64, set map[string][]HexMetric) error {
	return s.withTx(ctx, func(tx *sql.Tx) error {
		stmt, err := prepareHexMetricInsert(ctx, tx)
		if err != nil {
			return err
		}
		defer func() { _ = stmt.Close() }()
		for _, metric := range slices.Sorted(maps.Keys(set)) {
			for _, m := range set[metric] {
				if _, err := stmt.ExecContext(ctx, runID, m.HexID, metric, "", m.Value, m.Ref); err != nil {
					return fmt.Errorf("insert hex metric %s/%s: %w", metric, m.HexID, err)
				}
			}
		}
		return nil
	})
}

// ListHexMetrics returns one metric's rows in the resolved snapshot, ordered
// by hex id.
func (s *sqliteStore) ListHexMetrics(ctx context.Context, metric string) ([]HexMetric, error) {
	return listView(ctx, s, "hex metric",
		`SELECT hex_id, metric, value, ref, hex_metrics.run_id, runs.computed_at
		 FROM hex_metrics JOIN runs ON runs.id = hex_metrics.run_id`,
		and(eq("metric", metric), eq("subject", "")), ` ORDER BY hex_id, hex_metrics.run_id`,
		func(rows *sql.Rows) (HexMetric, error) {
			var m HexMetric
			err := rows.Scan(&m.HexID, &m.Metric, &m.Value, &m.Ref, &m.RunID, &m.ComputedAt)
			m.ComputedAt = canonicalTime(m.ComputedAt)
			return m, err
		})
}
