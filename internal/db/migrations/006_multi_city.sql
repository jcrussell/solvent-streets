-- Multi-city support: add cities table, city_id columns, and city_boundaries.

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

-- Insert a default city for existing data migration.
INSERT OR IGNORE INTO cities (id, slug, name) VALUES (1, 'default', 'Default');

-- Add city_id columns (SQLite requires one ALTER per column).
ALTER TABLE features ADD COLUMN city_id INTEGER NOT NULL DEFAULT 1 REFERENCES cities(id);
ALTER TABLE compute_results ADD COLUMN city_id INTEGER NOT NULL DEFAULT 1 REFERENCES cities(id);
ALTER TABLE hex_stats ADD COLUMN city_id INTEGER NOT NULL DEFAULT 1 REFERENCES cities(id);
ALTER TABLE forecast_results ADD COLUMN city_id INTEGER NOT NULL DEFAULT 1 REFERENCES cities(id);
ALTER TABLE cohort_stats ADD COLUMN city_id INTEGER NOT NULL DEFAULT 1 REFERENCES cities(id);
ALTER TABLE snapshots ADD COLUMN city_id INTEGER NOT NULL DEFAULT 1 REFERENCES cities(id);

-- Rebuild features primary key to include city_id.
-- SQLite doesn't support ALTER TABLE ... DROP/ADD PRIMARY KEY, so we
-- recreate the table. Existing data is preserved via INSERT ... SELECT.
CREATE TABLE features_new (
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
INSERT INTO features_new SELECT id, resource_type, city_id, name, tags, geometry_json, source_api, fetched_at FROM features;
DROP TABLE features;
ALTER TABLE features_new RENAME TO features;

-- Rebuild hex_stats primary key to include city_id.
CREATE TABLE hex_stats_new (
    hex_id TEXT NOT NULL,
    resource_type TEXT NOT NULL,
    city_id INTEGER NOT NULL REFERENCES cities(id),
    area_sqft REAL NOT NULL DEFAULT 0,
    pct_covered REAL NOT NULL DEFAULT 0,
    snapshot_id INTEGER REFERENCES snapshots(id),
    computed_at DATETIME NOT NULL DEFAULT CURRENT_TIMESTAMP,
    PRIMARY KEY (hex_id, resource_type, city_id)
);
INSERT INTO hex_stats_new SELECT hex_id, resource_type, city_id, area_sqft, pct_covered, snapshot_id, computed_at FROM hex_stats;
DROP TABLE hex_stats;
ALTER TABLE hex_stats_new RENAME TO hex_stats;

-- Indexes for new columns
CREATE INDEX IF NOT EXISTS idx_features_resource_type ON features(resource_type);
CREATE INDEX IF NOT EXISTS idx_features_city ON features(city_id);
CREATE INDEX IF NOT EXISTS idx_compute_results_city ON compute_results(city_id);
CREATE INDEX IF NOT EXISTS idx_hex_stats_resource_type ON hex_stats(resource_type);
CREATE INDEX IF NOT EXISTS idx_hex_stats_city ON hex_stats(city_id);
CREATE INDEX IF NOT EXISTS idx_forecast_results_city ON forecast_results(city_id);
