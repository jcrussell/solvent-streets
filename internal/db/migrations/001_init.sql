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
-- Released migrations are immutable, and this file is the v0.5.0 baseline
-- (generation 1). Databases built from pre-release revisions of it are
-- unsupported and must be rebuilt.
--
-- Rules:
--   * Every schema change is a new numbered migration (002_*.sql,
--     003_*.sql, ...), applied in order within the current generation by
--     migrateFS (migrations.go). See CONTRIBUTING.md, "Schema changes".
--   * Rewriting a shipped migration, this one included, is a squash: a
--     deliberate breaking change that bumps schemaGeneration in the same
--     commit, marked breaking ("!" in the subject).
--     TestReleasedMigrations_Unchanged pins each shipped file's hash to the
--     generation.
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

-- INPUTS. city_inputs holds one digest per stored
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

-- INGEST ATTEMPTS. One row per source an ingest
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

-- RUNS, the WORKING STATE and SNAPSHOTS.
--
-- A run is one producer's output for one city, immutable once written:
-- producer names the part it fills ("roads", "parking", "sidewalks",
-- "combined", "service:fire", "census", ...). Every result row below carries
-- the run_id that wrote it, and run_inputs records, with the run, the digest
-- of every input the producer read (db.ProducerInputs), taken before it read
-- them.
--
-- working_parts is the city's working state per config hash: the latest run
-- of each part. A snapshot is a named tag over many cities; snapshot_parts is
-- each city's frozen manifest of (part -> run) rows in it. A snapshot also
-- freezes, per city, what its rendering depends on beyond its runs: the hex
-- edge its grid was tiled at, and the boundary it was clipped to
-- (snapshot_boundaries, content-addressed so identical boundaries are stored
-- once). Every city reader resolves ONE snapshot and reads only that city's
-- runs in it. All of a city's runs in a snapshot share its config_hash.
--
-- A compute swaps only the parts it produced into the working state for its
-- config hash; only `pvmt snapshots create <name>` freezes the working state
-- of every city into a new snapshot. A run referenced by a snapshot or by a
-- current-epoch working state is never swept.
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
-- its latest compute as superseded and sweeps its runs;
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

-- A snapshot is a named, immutable tag over every configured city, like a
-- git tag: `pvmt snapshots create v0.5.0` freezes
-- each city's latest compute in one transaction, or nothing. snapshots is the
-- tag; compute_epoch is the config.ComputeEpoch of the binary that created
-- it. created_at orders nothing: "newest" is the highest id (created_at has
-- second granularity).
CREATE TABLE IF NOT EXISTS snapshots (
    id INTEGER PRIMARY KEY AUTOINCREMENT,
    name TEXT NOT NULL UNIQUE,
    created_at INTEGER NOT NULL,
    compute_epoch INTEGER NOT NULL
) STRICT;

-- One row per city the tag covers: what the city's results were computed
-- under. hex_edge_m and boundary_id freeze the grid it was computed on;
-- boundary_id is NULL only when the city had no stored boundary at create.
-- Keyed by (snapshot_id, city_id): a pinned city view checks its row with a
-- key lookup, and deleting the tag cascades through the key prefix.
CREATE TABLE IF NOT EXISTS snapshot_cities (
    snapshot_id INTEGER NOT NULL REFERENCES snapshots(id) ON DELETE CASCADE,
    city_id INTEGER NOT NULL REFERENCES cities(id),
    config_hash TEXT NOT NULL,
    hex_edge_m REAL NOT NULL,
    boundary_id INTEGER REFERENCES snapshot_boundaries(id),
    PRIMARY KEY (snapshot_id, city_id)
) STRICT, WITHOUT ROWID;

-- "The newest snapshot containing this city" (export, serve, status, a
-- city's snapshot list): a seek on city_id read backwards by snapshot_id.
CREATE INDEX IF NOT EXISTS idx_snapshot_cities_city ON snapshot_cities(city_id, snapshot_id);
-- "Is this boundary still frozen by a snapshot?" (delete / gc).
CREATE INDEX IF NOT EXISTS idx_snapshot_cities_boundary ON snapshot_cities(boundary_id);

-- A city's frozen manifest in a snapshot: (part -> run). Every read names
-- (snapshot_id, city_id), the key's prefix.
CREATE TABLE IF NOT EXISTS snapshot_parts (
    snapshot_id INTEGER NOT NULL,
    city_id INTEGER NOT NULL,
    part TEXT NOT NULL,
    run_id INTEGER NOT NULL REFERENCES runs(id),
    PRIMARY KEY (snapshot_id, city_id, part),
    FOREIGN KEY (snapshot_id, city_id) REFERENCES snapshot_cities(snapshot_id, city_id) ON DELETE CASCADE
) STRICT, WITHOUT ROWID;

-- "Is this run still referenced by any snapshot?" (delete / gc).
CREATE INDEX IF NOT EXISTS idx_snapshot_parts_run ON snapshot_parts(run_id);

-- RESULTS. Every result is a named measure written by
-- one run, in long format so a new producer adds metric keys, not tables.
-- Rows carry no id, city_id or timestamp: the run owns all three (readers
-- join runs.computed_at; a city's rows are its runs' rows). Deleting a run
-- deletes its rows by ON DELETE CASCADE, and the primary key makes each
-- (run, measure) write-once. Its run_id prefix serves every read, which
-- filters `run_id IN (<the resolved snapshot's runs>)`, and every delete.
--
--   metric  what is measured, a key of the metric catalog (internal/metric):
--           paved_area, feature_count, parking_spaces, fire_response_s ...
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

-- hex_metrics holds per-hex values: pavement coverage (paved_area,
-- paved_share, parking_spaces) and the registered metrics of
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
