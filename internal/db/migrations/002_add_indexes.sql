-- Add composite lookup indexes that mirror the hex_stats
-- (city_id, resource_type, snapshot_id) pattern for the remaining scoped
-- tables. Existing installs already recorded schema_version = 1 and will not
-- re-run 001_init.sql, so these live in a separate migration that reaches both
-- fresh and existing databases. Every statement is idempotent.

-- compute_results: LatestComputeResult filters `resource_type = ? AND
-- city_id = ?` and orders by computed_at DESC. Leading with city_id also covers
-- the city-only lookups the standalone idx_compute_results_city served, and the
-- trailing computed_at lets the ORDER BY resolve from the index.
CREATE INDEX IF NOT EXISTS idx_compute_results_city_type_time ON compute_results(city_id, resource_type, computed_at);

-- forecast_results: reads filter `resource_type = ? AND city_id = ?` with
-- MAX(snapshot_id) latest-resolution subqueries over the same slice, matching
-- the hex_stats composite shape.
CREATE INDEX IF NOT EXISTS idx_forecast_results_city_type_snap ON forecast_results(city_id, resource_type, snapshot_id);

-- cohort_stats: same read shape as forecast_results.
CREATE INDEX IF NOT EXISTS idx_cohort_stats_city_type_snap ON cohort_stats(city_id, resource_type, snapshot_id);

-- features: LastIngestAt filters `resource_type = ? AND city_id = ?` and orders
-- by fetched_at DESC LIMIT 1; the trailing fetched_at makes that an index-only
-- seek instead of a scan of the city/type slice.
CREATE INDEX IF NOT EXISTS idx_features_city_type_time ON features(city_id, resource_type, fetched_at);

-- schema_version is created imperatively in migrations.go (not in 001), and
-- SQLite cannot add a table-level primary key via migration. A unique index on
-- version enforces one row per applied migration.
--
-- The DELETE must come first, and it is not optional. migrateFS reads
-- MAX(version) outside any transaction, and 001_init.sql is entirely
-- IF NOT EXISTS, so two concurrent first-runs could both record version 1 --
-- harmless before this migration existed. Creating the unique index over that
-- duplicate row fails, and because applyMigration runs the whole file in one
-- transaction the failure rolls back, 002 is never recorded, and EVERY
-- subsequent db.Open fails identically. db.Open is the single gateway for every
-- command and there is no repair subcommand, so the database would be
-- permanently unusable.
--
-- An earlier revision of this file claimed that failing on a dirty DB "is the
-- desired signal". It is not: the failure is loud but unrecoverable, and it
-- also silently costs the four performance indexes above, since they roll back
-- with it. Collapsing duplicates to the earliest row is both recoverable and
-- correct -- the rows are indistinguishable, they record the same applied
-- migration. Both statements share one transaction, so a database can never be
-- observed deduped-but-unindexed.
DELETE FROM schema_version
WHERE rowid NOT IN (SELECT MIN(rowid) FROM schema_version GROUP BY version);

CREATE UNIQUE INDEX IF NOT EXISTS idx_schema_version_version ON schema_version(version);
