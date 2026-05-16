package db

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"time"
)

func (s *sqliteStore) UpsertFeatures(ctx context.Context, resourceType string, features []Feature) error {
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return fmt.Errorf("begin tx: %w", err)
	}
	defer func() { _ = tx.Rollback() }()

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
		_, err = stmt.ExecContext(ctx, f.ID, resourceType, s.cityID, f.Name, string(tagsJSON), f.GeometryJSON, f.SourceAPI, fetchedAt)
		if err != nil {
			return fmt.Errorf("exec upsert feature %s: %w", f.ID, err)
		}
	}

	return tx.Commit()
}

func (s *sqliteStore) ListFeatures(ctx context.Context, resourceType string) ([]Feature, error) {
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
			f.Tags = make(map[string]string)
		}
		features = append(features, f)
	}
	return features, rows.Err()
}

func (s *sqliteStore) SaveComputeResult(ctx context.Context, result ComputeResult) error {
	_, err := s.db.ExecContext(ctx, `
		INSERT INTO compute_results (resource_type, city_id, total_area_sqm, feature_count, computed_at, snapshot_id)
		VALUES (?, ?, ?, ?, ?, ?)
	`, result.ResourceType, s.cityID, result.TotalAreaSqM, result.FeatureCount, time.Now(), result.SnapshotID)
	if err != nil {
		return fmt.Errorf("insert compute result: %w", err)
	}
	return nil
}

// LatestComputeResult returns the most recent compute result for a resource.
// When the store is snapshot-pinned (via WithSnapshot), restricts the result
// to that snapshot id; the snapshot's own latest run wins on ties. Sentinel
// snapshotID 0 (the unpinned default) returns latest overall.
func (s *sqliteStore) LatestComputeResult(ctx context.Context, resourceType string) (*ComputeResult, error) {
	var r ComputeResult
	err := s.db.QueryRowContext(ctx, `
		SELECT id, resource_type, total_area_sqm, feature_count, computed_at, snapshot_id
		FROM compute_results
		WHERE resource_type = ? AND city_id = ? AND (? = 0 OR snapshot_id = ?)
		ORDER BY computed_at DESC
		LIMIT 1
	`, resourceType, s.cityID, s.snapshotID, s.snapshotID).Scan(&r.ID, &r.ResourceType, &r.TotalAreaSqM, &r.FeatureCount, &r.ComputedAt, &r.SnapshotID)
	if err != nil {
		return nil, err
	}
	return &r, nil
}

// SaveHexStats appends a batch of hex stats. Each row is tagged with its
// SnapshotID so historic snapshots remain queryable. Append-only: rows from
// prior snapshots are never deleted; pvmt re-runs against a new snapshot
// don't clobber older results. Rows with a nil SnapshotID (legacy data
// from before migration 002) coexist as "pre-snapshot" history.
func (s *sqliteStore) SaveHexStats(ctx context.Context, stats []HexStat) error {
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return fmt.Errorf("begin tx: %w", err)
	}
	defer func() { _ = tx.Rollback() }()

	stmt, err := tx.PrepareContext(ctx, `
		INSERT INTO hex_stats (hex_id, resource_type, city_id, snapshot_id, area_sqm, pct_covered, computed_at)
		VALUES (?, ?, ?, ?, ?, ?, ?)
	`)
	if err != nil {
		return fmt.Errorf("prepare hex stats insert: %w", err)
	}
	defer func() { _ = stmt.Close() }()

	now := time.Now()
	for _, st := range stats {
		if _, err := stmt.ExecContext(ctx, st.HexID, st.ResourceType, s.cityID, st.SnapshotID, st.AreaSqM, st.PctCovered, now); err != nil {
			return fmt.Errorf("insert hex stat %s: %w", st.HexID, err)
		}
	}

	return tx.Commit()
}

// ListHexStats returns the per-hex coverage rows for a resource. When
// snapshot-pinned, only rows from that snapshot. Otherwise: all rows,
// including legacy pre-snapshot rows with NULL snapshot_id — callers
// typically expect "the data the user last computed", and a city without
// any snapshot-tagged rows would otherwise look empty.
func (s *sqliteStore) ListHexStats(ctx context.Context, resourceType string) ([]HexStat, error) {
	rows, err := s.db.QueryContext(ctx, `
		SELECT hex_id, resource_type, area_sqm, pct_covered, computed_at, snapshot_id
		FROM hex_stats WHERE resource_type = ? AND city_id = ? AND (? = 0 OR snapshot_id = ?)
	`, resourceType, s.cityID, s.snapshotID, s.snapshotID)
	if err != nil {
		return nil, fmt.Errorf("query hex stats: %w", err)
	}
	defer func() { _ = rows.Close() }()

	var stats []HexStat
	for rows.Next() {
		var st HexStat
		if err := rows.Scan(&st.HexID, &st.ResourceType, &st.AreaSqM, &st.PctCovered, &st.ComputedAt, &st.SnapshotID); err != nil {
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
	if err := s.db.QueryRowContext(ctx, `SELECT computed_at FROM snapshots WHERE id = ?`, id).Scan(&snap.ComputedAt); err != nil {
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
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return fmt.Errorf("begin tx: %w", err)
	}
	defer func() { _ = tx.Rollback() }()

	stmt, err := tx.PrepareContext(ctx, `
		INSERT INTO forecast_results (resource_type, city_id, year, pci, area_sqm, treatment_cost, treatment_tier, snapshot_id, computed_at)
		VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?)
	`)
	if err != nil {
		return fmt.Errorf("prepare forecast insert: %w", err)
	}
	defer func() { _ = stmt.Close() }()

	now := time.Now()
	for _, r := range results {
		if _, err := stmt.ExecContext(ctx, r.ResourceType, s.cityID, r.Year, r.PCI, r.AreaSqM, r.TreatmentCost, r.TreatmentTier, r.SnapshotID, now); err != nil {
			return fmt.Errorf("insert forecast year %d: %w", r.Year, err)
		}
	}

	return tx.Commit()
}

func (s *sqliteStore) ListForecastResults(ctx context.Context, resourceType string) ([]ForecastResult, error) {
	rows, err := s.db.QueryContext(ctx, `
		SELECT id, resource_type, year, pci, area_sqm, treatment_cost, treatment_tier, snapshot_id, computed_at
		FROM forecast_results WHERE resource_type = ? AND city_id = ? AND (? = 0 OR snapshot_id = ?) ORDER BY year
	`, resourceType, s.cityID, s.snapshotID, s.snapshotID)
	if err != nil {
		return nil, fmt.Errorf("query forecasts: %w", err)
	}
	defer func() { _ = rows.Close() }()

	var results []ForecastResult
	for rows.Next() {
		var r ForecastResult
		if err := rows.Scan(&r.ID, &r.ResourceType, &r.Year, &r.PCI, &r.AreaSqM, &r.TreatmentCost, &r.TreatmentTier, &r.SnapshotID, &r.ComputedAt); err != nil {
			return nil, fmt.Errorf("scan forecast: %w", err)
		}
		results = append(results, r)
	}
	return results, rows.Err()
}

// SaveCohortStats appends cohort rows tagged with their SnapshotID.
// Append-only — see SaveHexStats for the rationale.
func (s *sqliteStore) SaveCohortStats(ctx context.Context, stats []CohortStat) error {
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return fmt.Errorf("begin tx: %w", err)
	}
	defer func() { _ = tx.Rollback() }()

	stmt, err := tx.PrepareContext(ctx, `
		INSERT INTO cohort_stats (resource_type, city_id, classification, area_sqm, feature_count, snapshot_id, computed_at)
		VALUES (?, ?, ?, ?, ?, ?, ?)
	`)
	if err != nil {
		return fmt.Errorf("prepare cohort stats insert: %w", err)
	}
	defer func() { _ = stmt.Close() }()

	now := time.Now()
	for _, st := range stats {
		if _, err := stmt.ExecContext(ctx, st.ResourceType, s.cityID, st.Classification, st.AreaSqM, st.FeatureCount, st.SnapshotID, now); err != nil {
			return fmt.Errorf("insert cohort stat %s/%s: %w", st.ResourceType, st.Classification, err)
		}
	}

	return tx.Commit()
}

func (s *sqliteStore) ListCohortStats(ctx context.Context, resourceType string) ([]CohortStat, error) {
	rows, err := s.db.QueryContext(ctx, `
		SELECT id, resource_type, classification, area_sqm, feature_count, snapshot_id, computed_at
		FROM cohort_stats WHERE resource_type = ? AND city_id = ? AND (? = 0 OR snapshot_id = ?)
	`, resourceType, s.cityID, s.snapshotID, s.snapshotID)
	if err != nil {
		return nil, fmt.Errorf("query cohort stats: %w", err)
	}
	defer func() { _ = rows.Close() }()

	var stats []CohortStat
	for rows.Next() {
		var st CohortStat
		if err := rows.Scan(&st.ID, &st.ResourceType, &st.Classification, &st.AreaSqM, &st.FeatureCount, &st.SnapshotID, &st.ComputedAt); err != nil {
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

func (s *sqliteStore) Stats(ctx context.Context, resourceType string) (*StatusInfo, error) {
	info := &StatusInfo{ResourceType: resourceType}

	err := s.db.QueryRowContext(ctx, `SELECT COUNT(*) FROM features WHERE resource_type = ? AND city_id = ?`, resourceType, s.cityID).Scan(&info.FeatureCount)
	if err != nil {
		return nil, fmt.Errorf("count features: %w", err)
	}

	var lastIngest *time.Time
	var t time.Time
	err = s.db.QueryRowContext(ctx, `SELECT MAX(fetched_at) FROM features WHERE resource_type = ? AND city_id = ?`, resourceType, s.cityID).Scan(&t)
	if err == nil && !t.IsZero() {
		lastIngest = &t
	}
	info.LastIngestAt = lastIngest

	result, err := s.LatestComputeResult(ctx, resourceType)
	if err == nil && result != nil {
		info.LastComputeAt = &result.ComputedAt
		info.TotalAreaSqM = result.TotalAreaSqM
	}

	return info, nil
}

func (s *sqliteStore) ResourceTypes(ctx context.Context) ([]string, error) {
	rows, err := s.db.QueryContext(ctx, `SELECT DISTINCT resource_type FROM features WHERE city_id = ? ORDER BY resource_type`, s.cityID)
	if err != nil {
		return nil, err
	}
	defer func() { _ = rows.Close() }()
	var types []string
	for rows.Next() {
		var t string
		if err := rows.Scan(&t); err != nil {
			return nil, err
		}
		types = append(types, t)
	}
	return types, rows.Err()
}

func (s *sqliteStore) Close() error {
	return s.db.Close()
}

// WithSnapshot returns a Store that filters snapshot-aware reads
// (LatestComputeResult, ListHexStats, ListCohortStats, ListForecastResults)
// to the given snapshot id. A snapshotID of 0 returns an unpinned view
// (latest overall — same as the original ForCity store). The underlying
// DB connection is shared.
func (s *sqliteStore) WithSnapshot(snapshotID int64) Store {
	cp := *s
	cp.snapshotID = snapshotID
	return &cp
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
