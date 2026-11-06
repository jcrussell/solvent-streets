-- Init schema: all tables with multi-city support.
--
-- SCHEMA GENERATION. This file is the squash of every migration up to and
-- including v0.4.1's 001/002 and the unreleased 003 (solvent-streets-25t6.2).
-- A squash is a deliberate breaking change: databases created under an earlier
-- generation already record schema_version 1, so this file would be SKIPPED for
-- them and they would run on the old schema. migrate() therefore refuses any
-- non-empty database whose PRAGMA user_version is not schemaGeneration
-- (migrations.go) with ErrIncompatibleDatabase.
--
-- Rules:
--   * Ordinary schema changes are additive numbered migrations (002_..., ...),
--     applied within the current generation. Never edit this file for those.
--   * Editing this file in place IS a squash: bump schemaGeneration in the same
--     commit, and mark that commit breaking ("!" in the subject).

CREATE TABLE IF NOT EXISTS cities (
    id INTEGER PRIMARY KEY AUTOINCREMENT,
    slug TEXT NOT NULL,
    name TEXT NOT NULL,
    config_id TEXT NOT NULL,
    UNIQUE(slug, config_id)
);

CREATE TABLE IF NOT EXISTS city_boundaries (
    city_id INTEGER NOT NULL REFERENCES cities(id),
    geometry_json TEXT NOT NULL,
    source TEXT NOT NULL DEFAULT 'nominatim',
    fetched_at DATETIME NOT NULL DEFAULT CURRENT_TIMESTAMP,
    UNIQUE(city_id)
);

CREATE TABLE IF NOT EXISTS features (
    id TEXT NOT NULL,
    resource_type TEXT NOT NULL,
    city_id INTEGER NOT NULL REFERENCES cities(id),
    name TEXT NOT NULL DEFAULT '',
    tags TEXT NOT NULL DEFAULT '{}',
    geometry_json TEXT NOT NULL,
    source_api TEXT NOT NULL DEFAULT '',
    fetched_at DATETIME NOT NULL DEFAULT CURRENT_TIMESTAMP,
    PRIMARY KEY (id, resource_type, city_id)
);

-- ListFeatures / DeleteFeatures / FeatureInfo all filter `resource_type = ?
-- AND city_id = ?`; no query ever filters features.resource_type alone, so a
-- single composite (city_id, resource_type) serves every read as a seek and
-- also covers city_id-only lookups. The old single-column idx_features_city
-- and idx_features_resource_type were dropped as redundant write overhead.
CREATE INDEX IF NOT EXISTS idx_features_city_type ON features(city_id, resource_type);
-- LastIngestAt filters `resource_type = ? AND city_id = ?` and orders by
-- fetched_at DESC LIMIT 1; the trailing fetched_at makes that an index-only
-- seek instead of a scan of the city/type slice.
CREATE INDEX IF NOT EXISTS idx_features_city_type_time ON features(city_id, resource_type, fetched_at);

CREATE TABLE IF NOT EXISTS snapshots (
    id INTEGER PRIMARY KEY AUTOINCREMENT,
    city_id INTEGER NOT NULL REFERENCES cities(id),
    computed_at DATETIME NOT NULL DEFAULT CURRENT_TIMESTAMP,
    config_hash TEXT NOT NULL DEFAULT ''
);

CREATE INDEX IF NOT EXISTS idx_snapshots_city ON snapshots(city_id);
CREATE INDEX IF NOT EXISTS idx_snapshots_config_hash ON snapshots(config_hash);

CREATE TABLE IF NOT EXISTS compute_results (
    id INTEGER PRIMARY KEY AUTOINCREMENT,
    resource_type TEXT NOT NULL,
    city_id INTEGER NOT NULL REFERENCES cities(id),
    total_area REAL NOT NULL DEFAULT 0,
    feature_count INTEGER NOT NULL DEFAULT 0,
    snapshot_id INTEGER REFERENCES snapshots(id),
    computed_at DATETIME NOT NULL DEFAULT CURRENT_TIMESTAMP,
    -- Estimated parking spaces (solvent-streets-l3gz): the parking compute
    -- pass derives a space count per lot (OSM capacity tag, else footprint
    -- area over resource.ParkingSpaceAreaM2, times levels for structured
    -- parking). The city total lives here; per-hex shares are hex_metrics
    -- rows. Non-parking rows keep 0; readers treat 0 as "no estimate".
    estimated_spaces INTEGER NOT NULL DEFAULT 0
);

CREATE INDEX IF NOT EXISTS idx_compute_results_resource_type ON compute_results(resource_type);
CREATE INDEX IF NOT EXISTS idx_compute_results_city ON compute_results(city_id);
-- LatestComputeResult filters `resource_type = ? AND city_id = ?` and orders
-- by computed_at DESC; the trailing computed_at resolves the ORDER BY from the
-- index.
CREATE INDEX IF NOT EXISTS idx_compute_results_city_type_time ON compute_results(city_id, resource_type, computed_at);

CREATE TABLE IF NOT EXISTS hex_stats (
    id INTEGER PRIMARY KEY AUTOINCREMENT,
    hex_id TEXT NOT NULL,
    resource_type TEXT NOT NULL,
    city_id INTEGER NOT NULL REFERENCES cities(id),
    snapshot_id INTEGER REFERENCES snapshots(id),
    area REAL NOT NULL DEFAULT 0,
    pct_covered REAL NOT NULL DEFAULT 0,
    computed_at DATETIME NOT NULL DEFAULT CURRENT_TIMESTAMP
);

-- Every hex_stats read filters `resource_type = ? AND city_id = ?` plus the
-- snapshot/config-hash arms whose MAX(snapshot_id) subqueries reduce over the
-- same (resource_type, city_id) slice (store.go snapshotQuery). This composite
-- serves the outer filter as a range seek and makes both MAX(snapshot_id)
-- subqueries index-only.
--   Dropped idx_hex_stats_lookup(hex_id, ...): hex_id never appears in any
--     WHERE clause (only INSERT/SELECT lists), so it was dead for reads and
--     pure write overhead on the SaveHexStats bulk-insert path.
--   Dropped idx_hex_stats_city(city_id): the composite leads with city_id, so
--     it covers city_id-only lookups too — the standalone index was redundant.
--   Dropped idx_hex_stats_resource_type(resource_type): no query filters
--     hex_stats.resource_type without also filtering city_id, so it served no
--     read and was pure write overhead.
-- idx_hex_stats_snapshot is kept: DeleteSnapshot filters `snapshot_id = ? AND
-- city_id = ?`, which the (city_id, resource_type, snapshot_id) composite
-- cannot satisfy as a seek (snapshot_id is not the leading column).
CREATE INDEX IF NOT EXISTS idx_hex_stats_snapshot ON hex_stats(snapshot_id);
CREATE INDEX IF NOT EXISTS idx_hex_stats_city_type_snap ON hex_stats(city_id, resource_type, snapshot_id);

CREATE TABLE IF NOT EXISTS forecast_results (
    id INTEGER PRIMARY KEY AUTOINCREMENT,
    resource_type TEXT NOT NULL,
    city_id INTEGER NOT NULL REFERENCES cities(id),
    year INTEGER NOT NULL,
    pci REAL NOT NULL DEFAULT 0,
    area REAL NOT NULL DEFAULT 0,
    treatment_cost REAL NOT NULL DEFAULT 0,
    treatment_tier TEXT NOT NULL DEFAULT '',
    snapshot_id INTEGER REFERENCES snapshots(id),
    computed_at DATETIME NOT NULL DEFAULT CURRENT_TIMESTAMP
);

CREATE INDEX IF NOT EXISTS idx_forecast_results_type ON forecast_results(resource_type);
CREATE INDEX IF NOT EXISTS idx_forecast_results_snapshot ON forecast_results(snapshot_id);
CREATE INDEX IF NOT EXISTS idx_forecast_results_city ON forecast_results(city_id);
-- Reads filter `resource_type = ? AND city_id = ?` with MAX(snapshot_id)
-- latest-resolution subqueries over the same slice (the hex_stats shape).
CREATE INDEX IF NOT EXISTS idx_forecast_results_city_type_snap ON forecast_results(city_id, resource_type, snapshot_id);

CREATE TABLE IF NOT EXISTS cohort_stats (
    id INTEGER PRIMARY KEY AUTOINCREMENT,
    resource_type TEXT NOT NULL,
    city_id INTEGER NOT NULL REFERENCES cities(id),
    classification TEXT NOT NULL,
    area REAL NOT NULL DEFAULT 0,
    feature_count INTEGER NOT NULL DEFAULT 0,
    snapshot_id INTEGER REFERENCES snapshots(id),
    computed_at DATETIME NOT NULL DEFAULT CURRENT_TIMESTAMP
);

CREATE INDEX IF NOT EXISTS idx_cohort_stats_city ON cohort_stats(city_id);
CREATE INDEX IF NOT EXISTS idx_cohort_stats_resource_type ON cohort_stats(resource_type);
-- Same read shape as forecast_results.
CREATE INDEX IF NOT EXISTS idx_cohort_stats_city_type_snap ON cohort_stats(city_id, resource_type, snapshot_id);

-- Generic per-hex metrics (solvent-streets-ql2w). One row per
-- (city, snapshot, hex, metric): a named numeric value (internal/hexmetric)
-- such as fire_response_s, plus an optional reference id (the nearest
-- facility for a coverage metric). A re-run replaces its own
-- (city, metric, snapshot) slice. `metric` plays the role resource_type plays
-- on the other result tables: the snapshot-scoped list query is keyed on it,
-- and the per-city composite index below serves it as a seek.
CREATE TABLE IF NOT EXISTS hex_metrics (
    id INTEGER PRIMARY KEY AUTOINCREMENT,
    hex_id TEXT NOT NULL,
    city_id INTEGER NOT NULL REFERENCES cities(id),
    snapshot_id INTEGER REFERENCES snapshots(id),
    metric TEXT NOT NULL,
    value REAL NOT NULL,
    ref TEXT NOT NULL DEFAULT '',
    computed_at DATETIME NOT NULL DEFAULT CURRENT_TIMESTAMP
);

CREATE INDEX IF NOT EXISTS idx_hex_metrics_city_metric_snap ON hex_metrics(city_id, metric, snapshot_id);
CREATE INDEX IF NOT EXISTS idx_hex_metrics_snapshot ON hex_metrics(snapshot_id);
