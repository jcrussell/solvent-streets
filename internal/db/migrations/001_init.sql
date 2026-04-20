-- Consolidated schema: all tables with multi-city support.

CREATE TABLE IF NOT EXISTS cities (
    id INTEGER PRIMARY KEY AUTOINCREMENT,
    slug TEXT UNIQUE NOT NULL,
    name TEXT NOT NULL
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

CREATE INDEX IF NOT EXISTS idx_features_resource_type ON features(resource_type);
CREATE INDEX IF NOT EXISTS idx_features_city ON features(city_id);

CREATE TABLE IF NOT EXISTS snapshots (
    id INTEGER PRIMARY KEY AUTOINCREMENT,
    city_id INTEGER NOT NULL REFERENCES cities(id),
    computed_at DATETIME NOT NULL DEFAULT CURRENT_TIMESTAMP,
    config_hash TEXT NOT NULL DEFAULT ''
);

CREATE INDEX IF NOT EXISTS idx_snapshots_city ON snapshots(city_id);

CREATE TABLE IF NOT EXISTS compute_results (
    id INTEGER PRIMARY KEY AUTOINCREMENT,
    resource_type TEXT NOT NULL,
    city_id INTEGER NOT NULL REFERENCES cities(id),
    total_area_sqm REAL NOT NULL DEFAULT 0,
    feature_count INTEGER NOT NULL DEFAULT 0,
    snapshot_id INTEGER REFERENCES snapshots(id),
    computed_at DATETIME NOT NULL DEFAULT CURRENT_TIMESTAMP
);

CREATE INDEX IF NOT EXISTS idx_compute_results_resource_type ON compute_results(resource_type);
CREATE INDEX IF NOT EXISTS idx_compute_results_city ON compute_results(city_id);

CREATE TABLE IF NOT EXISTS hex_stats (
    hex_id TEXT NOT NULL,
    resource_type TEXT NOT NULL,
    city_id INTEGER NOT NULL REFERENCES cities(id),
    area_sqm REAL NOT NULL DEFAULT 0,
    pct_covered REAL NOT NULL DEFAULT 0,
    snapshot_id INTEGER REFERENCES snapshots(id),
    computed_at DATETIME NOT NULL DEFAULT CURRENT_TIMESTAMP,
    PRIMARY KEY (hex_id, resource_type, city_id)
);

CREATE INDEX IF NOT EXISTS idx_hex_stats_resource_type ON hex_stats(resource_type);
CREATE INDEX IF NOT EXISTS idx_hex_stats_city ON hex_stats(city_id);

CREATE TABLE IF NOT EXISTS forecast_results (
    id INTEGER PRIMARY KEY AUTOINCREMENT,
    resource_type TEXT NOT NULL,
    city_id INTEGER NOT NULL REFERENCES cities(id),
    year INTEGER NOT NULL,
    pci REAL NOT NULL DEFAULT 0,
    area_sqm REAL NOT NULL DEFAULT 0,
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
    area_sqm REAL NOT NULL DEFAULT 0,
    feature_count INTEGER NOT NULL DEFAULT 0,
    snapshot_id INTEGER,
    computed_at DATETIME NOT NULL DEFAULT CURRENT_TIMESTAMP
);

CREATE INDEX IF NOT EXISTS idx_cohort_stats_city ON cohort_stats(city_id);
CREATE INDEX IF NOT EXISTS idx_cohort_stats_resource_type ON cohort_stats(resource_type);
