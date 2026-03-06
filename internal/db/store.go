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
	defer tx.Rollback()

	stmt, err := tx.Prepare(`
		INSERT INTO features (id, resource_type, name, tags, geometry_json, source_api, fetched_at)
		VALUES (?, ?, ?, ?, ?, ?, ?)
		ON CONFLICT(id, resource_type) DO UPDATE SET
			name = excluded.name,
			tags = excluded.tags,
			geometry_json = excluded.geometry_json,
			source_api = excluded.source_api,
			fetched_at = excluded.fetched_at
	`)
	if err != nil {
		return fmt.Errorf("prepare upsert: %w", err)
	}
	defer stmt.Close()

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
	defer rows.Close()

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
		INSERT INTO compute_results (resource_type, total_area_sqft, total_area_acres, feature_count, geometry_json, computed_at)
		VALUES (?, ?, ?, ?, ?, ?)
	`, result.ResourceType, result.TotalAreaSqFt, result.TotalAreaAcres, result.FeatureCount, result.GeometryJSON, time.Now())
	if err != nil {
		return fmt.Errorf("insert compute result: %w", err)
	}
	return nil
}

func (s *sqliteStore) LatestComputeResult(resourceType string) (*ComputeResult, error) {
	var r ComputeResult
	err := s.db.QueryRow(`
		SELECT id, resource_type, total_area_sqft, total_area_acres, feature_count, geometry_json, computed_at
		FROM compute_results
		WHERE resource_type = ?
		ORDER BY computed_at DESC
		LIMIT 1
	`, resourceType).Scan(&r.ID, &r.ResourceType, &r.TotalAreaSqFt, &r.TotalAreaAcres, &r.FeatureCount, &r.GeometryJSON, &r.ComputedAt)
	if err != nil {
		return nil, err
	}
	return &r, nil
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
	defer rows.Close()
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
