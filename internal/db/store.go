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

// The statements against features. Each names city_id and resource_type, the
// prefix of the table's primary key (001_init.sql), so each is a seek on it;
// TestQueryPlan_FeaturesStatements pins that for every one of them, including
// the digest pass (inputs.go) and gc's stale filter (staleFeatures).
const (
	sqlUpsertFeature = `INSERT OR REPLACE INTO features (city_id, resource_type, id, name, tags, geometry, source_api, fetched_at)
		VALUES (?, ?, ?, ?, ?, ?, ?, ?)`
	sqlDeleteFeatures = `DELETE FROM features WHERE city_id = ? AND resource_type = ?`
	sqlListFeatures   = `SELECT id, resource_type, name, tags, geometry, source_api, fetched_at
		FROM features WHERE city_id = ? AND resource_type = ?`
)

// sqlStoredFeatures is Stats' read of a type's stored rows: the count and
// newest fetch time city_inputs keeps beside the digest.
const sqlStoredFeatures = `SELECT feature_count, fetched_at FROM city_inputs WHERE city_id = ? AND input = ?`

// sqlDeleteFeaturesFrom deletes a type's rows from n sources: bound as
// (city_id, resource_type, source...).
func sqlDeleteFeaturesFrom(n int) string {
	// placeholders is only "?,?,..."; every source value is a bound parameter.
	return sqlDeleteFeatures + ` AND source_api IN (` + placeholders(n) + `)`
}

// UpsertFeatures replaces stored features for a resource and re-inserts the
// given set in one transaction. When sourceAPIs is empty the whole resource is
// replaced for the city; when it is non-empty the delete is scoped to those
// source_api values, so a partial-source re-ingest (one source down) preserves
// the surviving sources' rows instead of wiping them. The resource's input
// digest (city_inputs) is recomputed from the stored rows in the same
// transaction.
func (s *sqliteStore) UpsertFeatures(ctx context.Context, resourceType resource.Type, features []Feature, sourceAPIs []string) error {
	// Encode every row before taking the write lock (IMMEDIATE: BeginTx
	// waits for it, and holds it until commit), so parsing GeoJSON, about
	// 9 µs a row, never extends the time other writers wait.
	type row struct {
		tags, geometry []byte
		fetchedAt      int64
	}
	encoded := make([]row, len(features))
	now := time.Now()
	for i, f := range features {
		tagsJSON, err := json.Marshal(f.Tags)
		if err != nil {
			return fmt.Errorf("feature %s: marshal tags: %w", f.ID, err)
		}
		geometry, err := encodeGeometry(f.GeometryJSON)
		if err != nil {
			return fmt.Errorf("feature %s: %w", f.ID, err)
		}
		fetchedAt := f.FetchedAt
		if fetchedAt.IsZero() {
			fetchedAt = now
		}
		encoded[i] = row{tags: tagsJSON, geometry: geometry, fetchedAt: unixSeconds(fetchedAt)}
	}

	return s.withTx(ctx, func(tx *sql.Tx) error {
		if err := s.deleteFeaturesForReplace(ctx, tx, resourceType, sourceAPIs); err != nil {
			return err
		}

		stmt, err := tx.PrepareContext(ctx, sqlUpsertFeature)
		if err != nil {
			return fmt.Errorf("prepare upsert: %w", err)
		}
		defer func() { _ = stmt.Close() }()

		for i, f := range features {
			r := encoded[i]
			if _, err := stmt.ExecContext(ctx, s.cityID, resourceType, f.ID, f.Name, string(r.tags), r.geometry, f.SourceAPI, r.fetchedAt); err != nil {
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
		if _, err := tx.ExecContext(ctx, sqlDeleteFeatures, s.cityID, resourceType); err != nil {
			return fmt.Errorf("delete old features: %w", err)
		}
		return nil
	}
	args := append([]any{s.cityID, resourceType}, anyArgs(sourceAPIs)...)
	if _, err := tx.ExecContext(ctx, sqlDeleteFeaturesFrom(len(sourceAPIs)), args...); err != nil {
		return fmt.Errorf("delete old features by source: %w", err)
	}
	return nil
}

func (s *sqliteStore) ListFeatures(ctx context.Context, resourceType resource.Type) ([]Feature, error) {
	rows, err := s.db.QueryContext(ctx, sqlListFeatures, s.cityID, resourceType)
	if err != nil {
		return nil, fmt.Errorf("query features: %w", err)
	}
	defer func() { _ = rows.Close() }()

	var features []Feature
	for rows.Next() {
		var f Feature
		var tagsJSON string
		var geometry []byte
		if err := rows.Scan(&f.ID, &f.ResourceType, &f.Name, &tagsJSON, &geometry, &f.SourceAPI, unixTime{&f.FetchedAt}); err != nil {
			return nil, fmt.Errorf("scan feature: %w", err)
		}
		if f.GeometryJSON, err = geometryGeoJSON(geometry); err != nil {
			return nil, fmt.Errorf("feature %s: %w", f.ID, err)
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
		return setInput(ctx, tx, s.cityID, InputBoundary, inputSummary{
			digest:    boundaryDigest(geometryJSON, source),
			fetchedAt: sql.NullInt64{Int64: now, Valid: true},
		})
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
	err = s.db.QueryRowContext(ctx, sqlFrozenBoundary, s.snapshotID, s.cityID).Scan(&geometry)
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

	// city_inputs keeps the count and the newest fetched_at of the stored
	// rows with the digest (refreshFeaturesDigest), so neither reads the
	// features themselves. No row means never ingested; NULL fetched_at means
	// no stored rows. Either leaves LastIngestAt nil; a real query failure
	// (locked DB, transient I/O) must not fold into that branch.
	var fetched sql.NullInt64
	err := s.db.QueryRowContext(ctx, sqlStoredFeatures, s.cityID, string(resourceType)).Scan(&info.FeatureCount, &fetched)
	switch {
	case errors.Is(err, sql.ErrNoRows):
		// never ingested
	case err != nil:
		return nil, fmt.Errorf("stored features: %w", err)
	case fetched.Valid:
		t := fromUnix(fetched.Int64)
		info.LastIngestAt = &t
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
	return withTx(ctx, s.db, fn)
}

// withTx runs fn in one transaction on d (IMMEDIATE, per dsnPragmas),
// committing when it returns nil and rolling back otherwise.
//
// The write lock is taken at BEGIN, so BEGIN is where another process's
// write transaction makes this one wait. busy_timeout alone gives up after
// five seconds, shorter than an ingest's feature upsert or a gc sweep holds
// the lock, and the write that gives up is often a compute's save after its
// geometry work. So BEGIN is retried (retryBusy) for up to
// commitBusyBudget. Once BEGIN holds the lock nothing else can take it, so
// fn runs once and never sees a busy database.
func withTx(ctx context.Context, d *sql.DB, fn func(*sql.Tx) error) error {
	var tx *sql.Tx
	err := retryBusy(ctx, func() error {
		var err error
		tx, err = d.BeginTx(ctx, nil)
		return err
	})
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
	where = `city_id = ? AND resource_type IN (` + placeholders(len(types)) +
		`) AND source_api NOT IN (` + placeholders(len(keepSources)) + `) AND source_api != ''`
	args = append([]any{s.cityID}, anyArgs(types)...)
	return where, append(args, anyArgs(keepSources)...)
}

// sqlCountFeaturesWhere and sqlDeleteFeaturesWhere count and delete the rows
// matching where (staleFeatures), so gc's dry run and sweep share one filter.
func sqlCountFeaturesWhere(where string) string {
	return `SELECT COUNT(*) FROM features WHERE ` + where
}

func sqlDeleteFeaturesWhere(where string) string { return `DELETE FROM features WHERE ` + where }

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
