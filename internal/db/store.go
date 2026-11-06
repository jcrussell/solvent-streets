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

// UpsertFeatures replaces stored features for a resource and re-inserts the
// given set in one transaction. When sourceAPIs is empty the whole resource is
// replaced for the city; when it is non-empty the delete is scoped to those
// source_api values, so a partial-source re-ingest (one source down) preserves
// the surviving sources' rows instead of wiping them.
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
			if _, err := stmt.ExecContext(ctx, f.ID, resourceType, s.cityID, f.Name, string(tagsJSON), f.GeometryJSON, f.SourceAPI, fetchedAt); err != nil {
				return fmt.Errorf("exec upsert feature %s: %w", f.ID, err)
			}
		}
		return nil
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
	args := make([]any, 0, len(sourceAPIs)+2)
	args = append(args, resourceType, s.cityID)
	for _, sa := range sourceAPIs {
		args = append(args, sa)
	}
	placeholders := strings.TrimSuffix(strings.Repeat("?,", len(sourceAPIs)), ",")
	// placeholders is only "?,?,..." — every source value is a bound parameter.
	q := `DELETE FROM features WHERE resource_type = ? AND city_id = ? AND source_api IN (` + placeholders + `)` //nolint:gosec // placeholders are "?" only; values are parameterized via args
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
		info.EstimatedSpaces = result.EstimatedSpaces
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

// gcKeepPlaceholders builds the parameterized "?,?,..." placeholder list
// and the matching args slice (city_id first, then each keep source) for
// the stale-features statements. Source values are bound parameters, never
// interpolated — same approach as deleteFeaturesForReplace.
func (s *sqliteStore) gcKeepArgs(keepSources []string) (placeholders string, args []any) {
	args = make([]any, 0, len(keepSources)+1)
	args = append(args, s.cityID)
	for _, src := range keepSources {
		args = append(args, src)
	}
	placeholders = strings.TrimSuffix(strings.Repeat("?,", len(keepSources)), ",")
	return placeholders, args
}

// gcSweptTypes returns the placeholders and bound values for the resource
// types the stale-source sweep applies to: the pavement types (resource.All),
// whose sources the city config chooses. Every other stored type — facility
// points, attribute layers such as census blocks — owns its source
// (resource.OwnSourced) and so is never stale on the pavement sources'
// account. Listing the swept types, rather than excluding known prefixes,
// keeps a new own-sourced type safe without touching this query.
func gcSweptTypes() (placeholders string, args []any) {
	for _, r := range resource.All {
		args = append(args, string(r.Type()))
	}
	placeholders = strings.TrimSuffix(strings.Repeat("?,", len(args)), ",")
	return placeholders, args
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
