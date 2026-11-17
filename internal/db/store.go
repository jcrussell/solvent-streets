package db

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"log/slog"
	"time"

	"github.com/jcrussell/solvent-streets/internal/resource"
)

// UpsertFeatures replaces stored features for a resource and re-inserts the
// given set in one transaction. When sourceAPIs is empty the whole resource is
// replaced for the city; when it is non-empty the delete is scoped to those
// source_api values, so a partial-source re-ingest (one source down) preserves
// the surviving sources' rows instead of wiping them. The resource's input
// digest (city_inputs) is recomputed from the stored rows in the same
// transaction.
func (s *sqliteStore) UpsertFeatures(ctx context.Context, resourceType resource.Type, features []Feature, sourceAPIs []string) error {
	return s.withTx(ctx, func(tx *sql.Tx) error {
		if err := s.deleteFeaturesForReplace(ctx, tx, resourceType, sourceAPIs); err != nil {
			return err
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
			if _, err := stmt.ExecContext(ctx, f.ID, resourceType, s.cityID, f.Name, string(tagsJSON), f.GeometryJSON, f.SourceAPI, unixSeconds(fetchedAt)); err != nil {
				return fmt.Errorf("exec upsert feature %s: %w", f.ID, err)
			}
		}
		return refreshFeaturesDigest(ctx, tx, s.cityID, string(resourceType))
	})
}

// deleteFeaturesForReplace removes the rows a subsequent UpsertFeatures insert
// will replace. With no sourceAPIs the whole resource is cleared for the city;
// otherwise the delete is scoped to the given source_api values so other
// sources' rows survive a partial-source re-ingest.
func (s *sqliteStore) deleteFeaturesForReplace(ctx context.Context, tx *sql.Tx, resourceType resource.Type, sourceAPIs []string) error {
	if len(sourceAPIs) == 0 {
		if _, err := tx.ExecContext(ctx, `DELETE FROM features WHERE resource_type = ? AND city_id = ?`, resourceType, s.cityID); err != nil {
			return fmt.Errorf("delete old features: %w", err)
		}
		return nil
	}
	args := append([]any{resourceType, s.cityID}, anyArgs(sourceAPIs)...)
	// placeholders is only "?,?,..." — every source value is a bound parameter.
	q := `DELETE FROM features WHERE resource_type = ? AND city_id = ? AND source_api IN (` + placeholders(len(sourceAPIs)) + `)` //nolint:gosec // placeholders are "?" only; values are parameterized via args
	if _, err := tx.ExecContext(ctx, q, args...); err != nil {
		return fmt.Errorf("delete old features by source: %w", err)
	}
	return nil
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
		if err := rows.Scan(&f.ID, &f.ResourceType, &f.Name, &tagsJSON, &f.GeometryJSON, &f.SourceAPI, unixTime{&f.FetchedAt}); err != nil {
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

// SaveBoundary stores the city's boundary and records its digest as the
// boundary input (city_inputs), in one transaction.
func (s *sqliteStore) SaveBoundary(ctx context.Context, geometryJSON, source string) error {
	now := nowUnix()
	return s.withTx(ctx, func(tx *sql.Tx) error {
		if _, err := tx.ExecContext(ctx, `
			INSERT OR REPLACE INTO city_boundaries (city_id, geometry_json, source, fetched_at)
			VALUES (?, ?, ?, ?)
		`, s.cityID, geometryJSON, source, now); err != nil {
			return fmt.Errorf("save boundary: %w", err)
		}
		return setInputDigest(ctx, tx, s.cityID, InputBoundary, boundaryDigest(geometryJSON, source), sql.NullInt64{Int64: now, Valid: true})
	})
}

// GetBoundary returns the city's boundary GeoJSON, "" when none is stored.
// A view pinned with WithSnapshot returns the boundary that snapshot froze,
// falling back to the live boundary only when the snapshot froze none (the
// city had no boundary at commit), and ErrSnapshotGone when the snapshot does
// not exist. Every other view reads the live boundary, which ingest and
// compute work from.
func (s *sqliteStore) GetBoundary(ctx context.Context) (string, error) {
	if s.snapshotID > 0 {
		gj, frozen, err := s.frozenBoundary(ctx)
		if err != nil || frozen {
			return gj, err
		}
	}
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

// frozenBoundary reads the pinned snapshot's frozen boundary in one
// statement. frozen is false when the snapshot froze none.
func (s *sqliteStore) frozenBoundary(ctx context.Context) (gj string, frozen bool, err error) {
	var geometry sql.NullString
	err = s.db.QueryRowContext(ctx, `SELECT b.geometry_json FROM snapshots s
		LEFT JOIN snapshot_boundaries b ON b.id = s.boundary_id
		WHERE s.id = ? AND s.city_id = ?`, s.snapshotID, s.cityID).Scan(&geometry)
	switch {
	case errors.Is(err, sql.ErrNoRows):
		return "", false, fmt.Errorf("snapshot %d: %w", s.snapshotID, ErrSnapshotGone)
	case err != nil:
		return "", false, fmt.Errorf("get snapshot %d boundary: %w", s.snapshotID, err)
	}
	return geometry.String, geometry.Valid, nil
}

func (s *sqliteStore) Stats(ctx context.Context, resourceType resource.Type) (*StatusInfo, error) {
	info := &StatusInfo{ResourceType: resourceType}

	err := s.db.QueryRowContext(ctx, `SELECT COUNT(*) FROM features WHERE resource_type = ? AND city_id = ?`, resourceType, s.cityID).Scan(&info.FeatureCount)
	if err != nil {
		return nil, fmt.Errorf("count features: %w", err)
	}

	// city_inputs.fetched_at is the newest fetched_at of the stored rows,
	// kept with the digest (refreshFeaturesDigest). No row, or NULL (no
	// stored rows), leaves LastIngestAt nil; a real query failure (locked DB,
	// transient I/O) must not fold into that branch.
	var fetched sql.NullInt64
	err = s.db.QueryRowContext(ctx, `SELECT fetched_at FROM city_inputs WHERE city_id = ? AND input = ?`, s.cityID, string(resourceType)).Scan(&fetched)
	switch {
	case errors.Is(err, sql.ErrNoRows):
		// never ingested
	case err != nil:
		return nil, fmt.Errorf("latest fetched_at: %w", err)
	case fetched.Valid:
		t := fromUnix(fetched.Int64)
		info.LastIngestAt = &t
	}

	// The resource's city totals (class ""), read from its metric rows. No
	// rows means "never computed" (LastComputeAt stays nil); a genuine query
	// failure must propagate rather than silently report TotalArea=0.
	rows, err := s.ListCityMetrics(ctx, []string{resource.Subject(resourceType)})
	if err != nil {
		return nil, fmt.Errorf("latest compute result: %w", err)
	}
	for _, m := range rows {
		if m.Class != "" {
			continue
		}
		at := m.ComputedAt
		info.LastComputeAt = &at
		switch m.Metric {
		case resource.MetricPavedArea:
			info.TotalArea = m.Value
		case resource.MetricParkingSpaces:
			info.EstimatedSpaces = int(m.Value)
		}
	}

	return info, nil
}

// Close is a no-op for a city-scoped store. City stores returned by
// RootStore.ForCity share the RootStore's single *sql.DB connection pool
// (see ForCity), so closing one would tear down the RootStore and every
// sibling city store. Ownership of the pool stays with the RootStore;
// only RootStore.Close actually closes it.
func (s *sqliteStore) Close() error {
	return nil
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

// staleFeatures is the WHERE clause, with its bound values, that defines this
// city's stale features for the given keep list: pavement features (the
// resource.All types, whose sources the city config chooses) fetched from a
// source no longer kept. GCScan counts by it and GCSweep deletes by it, so a
// dry run reports exactly what the sweep removes.
//
// Every other stored type — facility points, attribute layers such as census
// blocks — owns its source (resource.OwnSourced) and so is never stale on the
// pavement sources' account. Listing the swept types, rather than excluding
// known prefixes, keeps a new own-sourced type safe without touching this
// query. Values are bound parameters, never interpolated.
func (s *sqliteStore) staleFeatures(keepSources []string) (where string, args []any) {
	types := make([]string, len(resource.All))
	for i, r := range resource.All {
		types[i] = string(r.Type())
	}
	where = `city_id = ? AND source_api NOT IN (` + placeholders(len(keepSources)) +
		`) AND source_api != '' AND resource_type IN (` + placeholders(len(types)) + `)`
	args = append([]any{s.cityID}, anyArgs(keepSources)...)
	return where, append(args, anyArgs(types)...)
}

// execDeleteCount runs a DELETE and writes the affected-row count into dst,
// wrapping both failures with the given label. Keeps GCSweep's three sweeps
// uniform and flat.
func execDeleteCount(ctx context.Context, tx *sql.Tx, label, query string, dst *int, args ...any) error {
	res, err := tx.ExecContext(ctx, query, args...)
	if err != nil {
		return fmt.Errorf("delete %s: %w", label, err)
	}
	n, err := res.RowsAffected()
	if err != nil {
		return fmt.Errorf("%s rows affected: %w", label, err)
	}
	*dst = int(n)
	return nil
}
