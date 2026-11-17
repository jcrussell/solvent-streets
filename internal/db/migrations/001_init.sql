-- Init schema: all tables with multi-city support.
--
-- SCHEMA GENERATION 1 (v0.5.0). It squashes every migration up to and
-- including v0.4.1's 001/002; the pre-release generations that followed are
-- folded in, because every pre-release database is rebuilt, not migrated.
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
--
-- Timestamps (every *_at column) are INTEGER unix seconds, UTC, written by Go
-- (internal/db/timestamps.go): no DEFAULT, so a statement that forgets one
-- fails instead of storing a value in another format.

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
    fetched_at INTEGER NOT NULL,
    UNIQUE(city_id)
);

-- Every features statement names city_id and resource_type (internal/db:
-- upsert, both replace deletes, ListFeatures, the digest pass in id order, and
-- gc's stale count/delete), so the primary key leads with them and is the
-- only index: each statement is a seek on its prefix, the digest's ORDER BY id
-- is key order, and WITHOUT ROWID stores the rows in that key's b-tree instead
-- of beside a rowid table plus a copy of the key.
--
-- geometry is TWKB at 7 decimal places (internal/db/geometry.go), about a
-- sixth of the GeoJSON text it is read back as; ListFeatures returns GeoJSON.
CREATE TABLE IF NOT EXISTS features (
    city_id INTEGER NOT NULL REFERENCES cities(id),
    resource_type TEXT NOT NULL,
    id TEXT NOT NULL,
    name TEXT NOT NULL DEFAULT '',
    tags TEXT NOT NULL DEFAULT '{}',
    geometry BLOB NOT NULL,
    source_api TEXT NOT NULL DEFAULT '',
    fetched_at INTEGER NOT NULL,
    PRIMARY KEY (city_id, resource_type, id)
) WITHOUT ROWID;

-- INPUTS (solvent-streets-we5i). city_inputs holds one digest per stored
-- input a producer reads: each features resource_type ('roads',
-- 'facility-fire', 'layer-census-blocks', ...) and 'boundary'. The digest is
-- a sha256 over the stored rows of that input in a stable order, '' when
-- there are none (internal/db/inputs.go). UpsertFeatures, SaveBoundary and
-- GCSweep keep it current in the same transaction as the rows themselves,
-- and ingested_at moves only when the digest does, so re-ingesting identical
-- data changes no digest. fetched_at is the newest features.fetched_at of the
-- input's stored rows (NULL when there are none; the boundary's save time for
-- 'boundary'), kept in the same pass as the digest, so "when was this last
-- ingested" is a key lookup rather than a scan of the rows. feature_count
-- is the number of stored rows (0 for 'boundary'), kept the same way: on a
-- WITHOUT ROWID table COUNT(*) reads every row of the slice. A run records
-- the digests it read (run_inputs), which is how a stale part is told apart
-- from a current one without comparing clocks.
CREATE TABLE IF NOT EXISTS city_inputs (
    city_id INTEGER NOT NULL REFERENCES cities(id),
    input TEXT NOT NULL,
    digest TEXT NOT NULL,
    ingested_at INTEGER NOT NULL,
    fetched_at INTEGER,
    feature_count INTEGER NOT NULL DEFAULT 0,
    PRIMARY KEY (city_id, input)
);

-- INGEST ATTEMPTS (solvent-streets-o831.1). One row per source an ingest
-- asked, whatever came back: input is a city_inputs key ('boundary' or a
-- features resource_type), source the upstream ('overpass', 'arcgis',
-- 'tigerweb', 'nominatim', 'overpass-relation'). outcome is 'ok' (rows
-- saved), 'empty' (the source answered with nothing, so stored rows were
-- kept) or 'failed' (error holds the message, truncated). A source the run
-- never asked, and a request the user cancelled, leave no row. Features keep
-- only the last success; this is how a failed or partial ingest is told from
-- a complete one. Only the newest rows per (city, input, source) are kept
-- (db.ingestAttemptsKept).
CREATE TABLE IF NOT EXISTS ingest_attempts (
    id INTEGER PRIMARY KEY,
    city_id INTEGER NOT NULL REFERENCES cities(id),
    input TEXT NOT NULL,
    source TEXT NOT NULL,
    started_at INTEGER NOT NULL,
    finished_at INTEGER NOT NULL,
    outcome TEXT NOT NULL CHECK (outcome IN ('ok', 'empty', 'failed')),
    feature_count INTEGER NOT NULL DEFAULT 0,
    error TEXT NOT NULL DEFAULT ''
);
CREATE INDEX IF NOT EXISTS idx_ingest_attempts_key ON ingest_attempts(city_id, input, source, id);

-- RUNS, the WORKING STATE and SNAPSHOTS (solvent-streets-25t6, -we5i).
--
-- A run is one producer's output for one city, immutable once written:
-- producer names the part it fills ("roads", "parking", "sidewalks",
-- "combined", "service:fire", "census", ...). Every result row below carries
-- the run_id that wrote it, and run_inputs records, with the run, the digest
-- of every input the producer read (db.ProducerInputs), taken before it read
-- them.
--
-- working_parts is the city's working state per config hash: the latest run
-- of each part. snapshot_parts is a snapshot's frozen manifest of
-- (part -> run) rows. A snapshot also freezes what its rendering depends on
-- beyond its runs: the hex edge its grid was tiled at, and the boundary it
-- was clipped to (snapshot_boundaries, content-addressed so identical
-- boundaries are stored once). Every reader resolves ONE snapshot and reads
-- only its runs. All runs in a snapshot share the snapshot's config_hash.
--
-- A compute swaps only the parts it produced into the working state for its
-- config hash; only `pvmt snapshots create` freezes the working state into a
-- new snapshot. A run referenced by a snapshot or by a current-epoch working
-- state is never swept.
CREATE TABLE IF NOT EXISTS runs (
    id INTEGER PRIMARY KEY AUTOINCREMENT,
    city_id INTEGER NOT NULL REFERENCES cities(id),
    producer TEXT NOT NULL,
    config_hash TEXT NOT NULL DEFAULT '',
    computed_at INTEGER NOT NULL
);

CREATE INDEX IF NOT EXISTS idx_runs_city ON runs(city_id);

CREATE TABLE IF NOT EXISTS run_inputs (
    run_id INTEGER NOT NULL REFERENCES runs(id) ON DELETE CASCADE,
    input TEXT NOT NULL,
    digest TEXT NOT NULL,
    PRIMARY KEY (run_id, input)
) STRICT, WITHOUT ROWID;

-- The facilities a service run routed from (OSM points and
-- [[cities.facilities]]), so its coverage renders with the stations that
-- produced it after the facilities are re-ingested or edited. The turnout the
-- run used is a city_metrics row (metric 'turnout_s', subject the producer).
CREATE TABLE IF NOT EXISTS run_facilities (
    run_id INTEGER NOT NULL REFERENCES runs(id) ON DELETE CASCADE,
    facility_id TEXT NOT NULL,
    name TEXT NOT NULL,
    source TEXT NOT NULL,
    lon REAL NOT NULL,
    lat REAL NOT NULL,
    PRIMARY KEY (run_id, facility_id)
) STRICT, WITHOUT ROWID;

-- compute_epoch is the config.ComputeEpoch of the binary that committed the
-- part. A hash from an older epoch can never be produced again, so gc treats
-- its latest compute as superseded and sweeps its runs (solvent-streets-e26d);
-- another config's hash, from the same epoch, is kept.
CREATE TABLE IF NOT EXISTS working_parts (
    city_id INTEGER NOT NULL REFERENCES cities(id),
    config_hash TEXT NOT NULL,
    compute_epoch INTEGER NOT NULL,
    part TEXT NOT NULL,
    run_id INTEGER NOT NULL REFERENCES runs(id),
    PRIMARY KEY (city_id, config_hash, part)
) STRICT, WITHOUT ROWID;

-- "Is this run in the working state?" (delete / gc).
CREATE INDEX IF NOT EXISTS idx_working_parts_run ON working_parts(run_id);

CREATE TABLE IF NOT EXISTS snapshot_boundaries (
    id INTEGER PRIMARY KEY AUTOINCREMENT,
    city_id INTEGER NOT NULL REFERENCES cities(id),
    digest TEXT NOT NULL,
    geometry_json TEXT NOT NULL,
    source TEXT NOT NULL,
    UNIQUE (city_id, digest)
) STRICT;

-- hex_edge_m and boundary_id freeze the grid a snapshot was computed on.
-- boundary_id is NULL only when the city had no stored boundary at commit.
CREATE TABLE IF NOT EXISTS snapshots (
    id INTEGER PRIMARY KEY AUTOINCREMENT,
    city_id INTEGER NOT NULL REFERENCES cities(id),
    created_at INTEGER NOT NULL,
    config_hash TEXT NOT NULL DEFAULT '',
    hex_edge_m REAL NOT NULL,
    boundary_id INTEGER REFERENCES snapshot_boundaries(id)
);

-- Resolution reads "newest snapshot for this city [and config hash]"; the
-- city_id prefix also serves the any-hash reads.
CREATE INDEX IF NOT EXISTS idx_snapshots_city_hash ON snapshots(city_id, config_hash);
-- "Is this boundary still frozen by a snapshot?" (delete / gc).
CREATE INDEX IF NOT EXISTS idx_snapshots_boundary ON snapshots(boundary_id);

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
