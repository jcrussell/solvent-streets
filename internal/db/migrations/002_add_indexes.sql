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
-- version enforces one row per applied migration. This creation fails loud on a
-- dirty DB that already has duplicate version rows, which is the desired signal.
CREATE UNIQUE INDEX IF NOT EXISTS idx_schema_version_version ON schema_version(version);
