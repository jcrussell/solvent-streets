-- Init schema: all tables with multi-city support.
--
-- SCHEMA GENERATION 2. Generation 1 squashed every migration up to and
-- including v0.4.1's 001/002 and the unreleased 003 (solvent-streets-25t6.2);
-- generation 2 replaced per-resource snapshots with runs and whole-city
-- snapshot manifests (solvent-streets-25t6.3).
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

-- RUNS and SNAPSHOTS (solvent-streets-25t6).
--
-- A run is one producer's output for one city, immutable once written:
-- producer names the part it fills ("roads", "parking", "sidewalks",
-- "combined", "service:fire", "census", ...). Every result row below carries
-- the run_id that wrote it.
--
-- A snapshot is the city's COMPLETE computed state: a manifest of
-- (part -> run) rows in snapshot_parts. Each command commits ONE new snapshot
-- = the newest snapshot for the same config hash, with only the parts that
-- command produced swapped in (so a recompute changes only what it
-- recomputes). Every reader resolves ONE snapshot and reads only its runs.
-- All runs in a snapshot share the snapshot's config_hash.
CREATE TABLE IF NOT EXISTS runs (
    id INTEGER PRIMARY KEY AUTOINCREMENT,
    city_id INTEGER NOT NULL REFERENCES cities(id),
    producer TEXT NOT NULL,
    config_hash TEXT NOT NULL DEFAULT '',
    computed_at DATETIME NOT NULL DEFAULT CURRENT_TIMESTAMP
);

CREATE INDEX IF NOT EXISTS idx_runs_city ON runs(city_id);

CREATE TABLE IF NOT EXISTS snapshots (
    id INTEGER PRIMARY KEY AUTOINCREMENT,
    city_id INTEGER NOT NULL REFERENCES cities(id),
    created_at DATETIME NOT NULL DEFAULT CURRENT_TIMESTAMP,
    config_hash TEXT NOT NULL DEFAULT ''
);

-- Resolution reads "newest snapshot for this city [and config hash]".
CREATE INDEX IF NOT EXISTS idx_snapshots_city_hash ON snapshots(city_id, config_hash);

CREATE TABLE IF NOT EXISTS snapshot_parts (
    snapshot_id INTEGER NOT NULL REFERENCES snapshots(id),
    part TEXT NOT NULL,
    run_id INTEGER NOT NULL REFERENCES runs(id),
    PRIMARY KEY (snapshot_id, part)
);

-- "Is this run still referenced by any snapshot?" (delete / gc).
CREATE INDEX IF NOT EXISTS idx_snapshot_parts_run ON snapshot_parts(run_id);

CREATE TABLE IF NOT EXISTS compute_results (
    id INTEGER PRIMARY KEY AUTOINCREMENT,
    resource_type TEXT NOT NULL,
    city_id INTEGER NOT NULL REFERENCES cities(id),
    total_area REAL NOT NULL DEFAULT 0,
    feature_count INTEGER NOT NULL DEFAULT 0,
    run_id INTEGER NOT NULL REFERENCES runs(id),
    computed_at DATETIME NOT NULL DEFAULT CURRENT_TIMESTAMP,
    -- Estimated parking spaces (solvent-streets-l3gz): the parking compute
    -- pass derives a space count per lot (OSM capacity tag, else footprint
    -- area over resource.ParkingSpaceAreaM2, times levels for structured
    -- parking). The city total lives here; per-hex shares are hex_metrics
    -- rows. Non-parking rows keep 0; readers treat 0 as "no estimate".
    estimated_spaces INTEGER NOT NULL DEFAULT 0
);

-- Reads filter `city_id = ? AND resource_type = ? AND run_id IN (...)` (the
-- resolved snapshot's runs); deletes filter `run_id = ?`.
CREATE INDEX IF NOT EXISTS idx_compute_results_city_type_run ON compute_results(city_id, resource_type, run_id);
CREATE INDEX IF NOT EXISTS idx_compute_results_run ON compute_results(run_id);

CREATE TABLE IF NOT EXISTS hex_stats (
    id INTEGER PRIMARY KEY AUTOINCREMENT,
    hex_id TEXT NOT NULL,
    resource_type TEXT NOT NULL,
    city_id INTEGER NOT NULL REFERENCES cities(id),
    run_id INTEGER NOT NULL REFERENCES runs(id),
    area REAL NOT NULL DEFAULT 0,
    pct_covered REAL NOT NULL DEFAULT 0,
    computed_at DATETIME NOT NULL DEFAULT CURRENT_TIMESTAMP
);

-- Every read filters `city_id = ? AND resource_type = ? AND run_id IN (...)`
-- (the resolved snapshot's runs); deletes filter `run_id = ?`. hex_id never
-- appears in a WHERE clause, so it is deliberately not indexed.
CREATE INDEX IF NOT EXISTS idx_hex_stats_city_type_run ON hex_stats(city_id, resource_type, run_id);
CREATE INDEX IF NOT EXISTS idx_hex_stats_run ON hex_stats(run_id);

CREATE TABLE IF NOT EXISTS cohort_stats (
    id INTEGER PRIMARY KEY AUTOINCREMENT,
    resource_type TEXT NOT NULL,
    city_id INTEGER NOT NULL REFERENCES cities(id),
    classification TEXT NOT NULL,
    area REAL NOT NULL DEFAULT 0,
    feature_count INTEGER NOT NULL DEFAULT 0,
    run_id INTEGER NOT NULL REFERENCES runs(id),
    computed_at DATETIME NOT NULL DEFAULT CURRENT_TIMESTAMP
);

-- Same read and delete shapes as hex_stats.
CREATE INDEX IF NOT EXISTS idx_cohort_stats_city_type_run ON cohort_stats(city_id, resource_type, run_id);
CREATE INDEX IF NOT EXISTS idx_cohort_stats_run ON cohort_stats(run_id);

-- Generic per-hex metrics (solvent-streets-ql2w). One row per
-- (run, hex, metric): a named numeric value (internal/hexmetric) such as
-- fire_response_s, plus an optional reference id (the nearest facility for a
-- coverage metric). `metric` plays the role resource_type plays on the other
-- result tables.
CREATE TABLE IF NOT EXISTS hex_metrics (
    id INTEGER PRIMARY KEY AUTOINCREMENT,
    hex_id TEXT NOT NULL,
    city_id INTEGER NOT NULL REFERENCES cities(id),
    run_id INTEGER NOT NULL REFERENCES runs(id),
    metric TEXT NOT NULL,
    value REAL NOT NULL,
    ref TEXT NOT NULL DEFAULT '',
    computed_at DATETIME NOT NULL DEFAULT CURRENT_TIMESTAMP
);

CREATE INDEX IF NOT EXISTS idx_hex_metrics_city_metric_run ON hex_metrics(city_id, metric, run_id);
CREATE INDEX IF NOT EXISTS idx_hex_metrics_run ON hex_metrics(run_id);
