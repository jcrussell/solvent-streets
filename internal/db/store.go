package db

import (
	"encoding/json"
	"fmt"
	"time"
)

func (s *sqliteStore) UpsertFeatures(resourceType string, features []Feature) error {
	tx, err := s.db.Begin()
	if err != nil {
		return fmt.Errorf("begin tx: %w", err)
	}
	defer func() { _ = tx.Rollback() }()

	// Delete existing features for this resource type so stale entries
	// (e.g. from a narrowed query) don't persist across re-ingests.
	if _, err := tx.Exec(`DELETE FROM features WHERE resource_type = ?`, resourceType); err != nil {
		return fmt.Errorf("delete old features: %w", err)
	}

	stmt, err := tx.Prepare(`
		INSERT INTO features (id, resource_type, name, tags, geometry_json, source_api, fetched_at)
		VALUES (?, ?, ?, ?, ?, ?, ?)
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
		_, err = stmt.Exec(f.ID, resourceType, f.Name, string(tagsJSON), f.GeometryJSON, f.SourceAPI, fetchedAt)
		if err != nil {
			return fmt.Errorf("exec upsert feature %s: %w", f.ID, err)
		}
	}

	return tx.Commit()
}

func (s *sqliteStore) ListFeatures(resourceType string) ([]Feature, error) {
	rows, err := s.db.Query(`
		SELECT id, resource_type, name, tags, geometry_json, source_api, fetched_at
		FROM features WHERE resource_type = ?
	`, resourceType)
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

func (s *sqliteStore) SaveComputeResult(result ComputeResult) error {
	_, err := s.db.Exec(`
		INSERT INTO compute_results (resource_type, total_area_sqft, total_area_acres, feature_count, geometry_json, computed_at, snapshot_id)
		VALUES (?, ?, ?, ?, ?, ?, ?)
	`, result.ResourceType, result.TotalAreaSqFt, result.TotalAreaAcres, result.FeatureCount, result.GeometryJSON, time.Now(), result.SnapshotID)
	if err != nil {
		return fmt.Errorf("insert compute result: %w", err)
	}
	return nil
}

func (s *sqliteStore) LatestComputeResult(resourceType string) (*ComputeResult, error) {
	var r ComputeResult
	err := s.db.QueryRow(`
		SELECT id, resource_type, total_area_sqft, total_area_acres, feature_count, geometry_json, computed_at, snapshot_id
		FROM compute_results
		WHERE resource_type = ?
		ORDER BY computed_at DESC
		LIMIT 1
	`, resourceType).Scan(&r.ID, &r.ResourceType, &r.TotalAreaSqFt, &r.TotalAreaAcres, &r.FeatureCount, &r.GeometryJSON, &r.ComputedAt, &r.SnapshotID)
	if err != nil {
		return nil, err
	}
	return &r, nil
}

func (s *sqliteStore) SaveHexStats(stats []HexStat) error {
	tx, err := s.db.Begin()
	if err != nil {
		return fmt.Errorf("begin tx: %w", err)
	}
	defer func() { _ = tx.Rollback() }()

	// Delete existing stats for the resource type(s) being updated
	types := make(map[string]bool)
	for _, st := range stats {
		types[st.ResourceType] = true
	}
	for rt := range types {
		if _, err := tx.Exec(`DELETE FROM hex_stats WHERE resource_type = ?`, rt); err != nil {
			return fmt.Errorf("delete old hex stats: %w", err)
		}
	}

	stmt, err := tx.Prepare(`
		INSERT INTO hex_stats (hex_id, resource_type, area_sqft, pct_covered, computed_at)
		VALUES (?, ?, ?, ?, ?)
	`)
	if err != nil {
		return fmt.Errorf("prepare hex stats insert: %w", err)
	}
	defer func() { _ = stmt.Close() }()

	now := time.Now()
	for _, st := range stats {
		if _, err := stmt.Exec(st.HexID, st.ResourceType, st.AreaSqFt, st.PctCovered, now); err != nil {
			return fmt.Errorf("insert hex stat %s: %w", st.HexID, err)
		}
	}

	return tx.Commit()
}

func (s *sqliteStore) ListHexStats(resourceType string) ([]HexStat, error) {
	rows, err := s.db.Query(`
		SELECT hex_id, resource_type, area_sqft, pct_covered, computed_at
		FROM hex_stats WHERE resource_type = ?
	`, resourceType)
	if err != nil {
		return nil, fmt.Errorf("query hex stats: %w", err)
	}
	defer func() { _ = rows.Close() }()

	var stats []HexStat
	for rows.Next() {
		var st HexStat
		if err := rows.Scan(&st.HexID, &st.ResourceType, &st.AreaSqFt, &st.PctCovered, &st.ComputedAt); err != nil {
			return nil, fmt.Errorf("scan hex stat: %w", err)
		}
		stats = append(stats, st)
	}
	return stats, rows.Err()
}

func (s *sqliteStore) CreateSnapshot(configHash string) (*Snapshot, error) {
	result, err := s.db.Exec(`INSERT INTO snapshots (config_hash) VALUES (?)`, configHash)
	if err != nil {
		return nil, fmt.Errorf("create snapshot: %w", err)
	}
	id, err := result.LastInsertId()
	if err != nil {
		return nil, fmt.Errorf("get snapshot id: %w", err)
	}
	snap := &Snapshot{ID: id, ConfigHash: configHash}
	if err := s.db.QueryRow(`SELECT computed_at FROM snapshots WHERE id = ?`, id).Scan(&snap.ComputedAt); err != nil {
		return nil, fmt.Errorf("get snapshot timestamp: %w", err)
	}
	return snap, nil
}

func (s *sqliteStore) ListSnapshots() ([]Snapshot, error) {
	rows, err := s.db.Query(`SELECT id, computed_at, config_hash FROM snapshots ORDER BY computed_at DESC`)
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

func (s *sqliteStore) SaveForecastResults(results []ForecastResult) error {
	tx, err := s.db.Begin()
	if err != nil {
		return fmt.Errorf("begin tx: %w", err)
	}
	defer func() { _ = tx.Rollback() }()

	// Delete existing forecasts for the resource type(s)
	types := make(map[string]bool)
	for _, r := range results {
		types[r.ResourceType] = true
	}
	for rt := range types {
		if _, err := tx.Exec(`DELETE FROM forecast_results WHERE resource_type = ?`, rt); err != nil {
			return fmt.Errorf("delete old forecasts: %w", err)
		}
	}

	stmt, err := tx.Prepare(`
		INSERT INTO forecast_results (resource_type, year, pci, area_sqft, treatment_cost, treatment_tier, snapshot_id, computed_at)
		VALUES (?, ?, ?, ?, ?, ?, ?, ?)
	`)
	if err != nil {
		return fmt.Errorf("prepare forecast insert: %w", err)
	}
	defer func() { _ = stmt.Close() }()

	now := time.Now()
	for _, r := range results {
		if _, err := stmt.Exec(r.ResourceType, r.Year, r.PCI, r.AreaSqFt, r.TreatmentCost, r.TreatmentTier, r.SnapshotID, now); err != nil {
			return fmt.Errorf("insert forecast year %d: %w", r.Year, err)
		}
	}

	return tx.Commit()
}

func (s *sqliteStore) ListForecastResults(resourceType string) ([]ForecastResult, error) {
	rows, err := s.db.Query(`
		SELECT id, resource_type, year, pci, area_sqft, treatment_cost, treatment_tier, snapshot_id, computed_at
		FROM forecast_results WHERE resource_type = ? ORDER BY year
	`, resourceType)
	if err != nil {
		return nil, fmt.Errorf("query forecasts: %w", err)
	}
	defer func() { _ = rows.Close() }()

	var results []ForecastResult
	for rows.Next() {
		var r ForecastResult
		if err := rows.Scan(&r.ID, &r.ResourceType, &r.Year, &r.PCI, &r.AreaSqFt, &r.TreatmentCost, &r.TreatmentTier, &r.SnapshotID, &r.ComputedAt); err != nil {
			return nil, fmt.Errorf("scan forecast: %w", err)
		}
		results = append(results, r)
	}
	return results, rows.Err()
}

func (s *sqliteStore) Stats(resourceType string) (*StatusInfo, error) {
	info := &StatusInfo{ResourceType: resourceType}

	err := s.db.QueryRow(`SELECT COUNT(*) FROM features WHERE resource_type = ?`, resourceType).Scan(&info.FeatureCount)
	if err != nil {
		return nil, fmt.Errorf("count features: %w", err)
	}

	var lastIngest *time.Time
	var t time.Time
	err = s.db.QueryRow(`SELECT MAX(fetched_at) FROM features WHERE resource_type = ?`, resourceType).Scan(&t)
	if err == nil && !t.IsZero() {
		lastIngest = &t
	}
	info.LastIngestAt = lastIngest

	result, err := s.LatestComputeResult(resourceType)
	if err == nil && result != nil {
		info.LastComputeAt = &result.ComputedAt
		info.TotalAreaSqFt = result.TotalAreaSqFt
		info.TotalAreaAcres = result.TotalAreaAcres
	}

	return info, nil
}

func (s *sqliteStore) ResourceTypes() ([]string, error) {
	rows, err := s.db.Query(`SELECT DISTINCT resource_type FROM features ORDER BY resource_type`)
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
