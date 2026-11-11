-- Init schema: all tables with multi-city support.
--
-- SCHEMA GENERATION 4. Generation 1 squashed every migration up to and
-- including v0.4.1's 001/002 and the unreleased 003 (solvent-streets-25t6.2);
-- generation 2 replaced per-resource snapshots with runs and whole-city
-- snapshot manifests (solvent-streets-25t6.3); generation 3 stopped storing
-- forecasts (solvent-streets-ern4.16); generation 4 replaced the
-- pavement-specific result tables with city_metrics / hex_metrics
-- (solvent-streets-0dkz).
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

-- RESULTS (solvent-streets-0dkz). Every result is a named measure written by
-- one run, in long format so a new producer adds metric keys, not tables.
-- Rows carry no id, city_id or timestamp: the run owns all three (readers
-- join runs.computed_at; a city's rows are its runs' rows). Deleting a run
-- deletes its rows by ON DELETE CASCADE, and the primary key makes each
-- (run, measure) write-once. Its run_id prefix serves every read, which
-- filters `run_id IN (<the resolved snapshot's runs>)`, and every delete.
--
--   metric  what is measured, snake_case with a unit: paved_area_m2,
--           coverage_pct, feature_count, parking_spaces, fire_response_s ...
--   subject what it is measured over: a pavement resource.Type including its
--           scope ('roads', 'roads:city', 'combined:city'), or '' for
--           producers that are not about one resource (fire, census).
--
-- city_metrics holds city-wide totals (class '') and, for pavement subjects,
-- the per-highway-class cohort split (class = 'primary', 'residential', ...).
-- Counts are stored as REAL like every other value.
CREATE TABLE IF NOT EXISTS city_metrics (
    run_id INTEGER NOT NULL REFERENCES runs(id) ON DELETE CASCADE,
    metric TEXT NOT NULL,
    subject TEXT NOT NULL,
    class TEXT NOT NULL,
    value REAL NOT NULL,
    PRIMARY KEY (run_id, metric, subject, class)
) STRICT, WITHOUT ROWID;

-- hex_metrics holds per-hex values: pavement coverage (paved_area_m2,
-- coverage_pct, parking_spaces) and the registered metrics of
-- internal/hexmetric (fire_response_s, population ...), plus an optional
-- reference id (the nearest facility for a coverage metric).
CREATE TABLE IF NOT EXISTS hex_metrics (
    run_id INTEGER NOT NULL REFERENCES runs(id) ON DELETE CASCADE,
    hex_id TEXT NOT NULL,
    metric TEXT NOT NULL,
    subject TEXT NOT NULL,
    value REAL NOT NULL,
    ref TEXT NOT NULL DEFAULT '',
    PRIMARY KEY (run_id, metric, subject, hex_id)
) STRICT, WITHOUT ROWID;
