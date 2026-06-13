-- Init schema: all tables with multi-city support.

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
    computed_at DATETIME NOT NULL DEFAULT CURRENT_TIMESTAMP
);

CREATE INDEX IF NOT EXISTS idx_compute_results_resource_type ON compute_results(resource_type);
CREATE INDEX IF NOT EXISTS idx_compute_results_city ON compute_results(city_id);

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
