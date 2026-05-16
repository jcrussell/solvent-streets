-- Preserve historical snapshots across the result tables.
--
-- Before: hex_stats had PRIMARY KEY (hex_id, resource_type, city_id) and the
-- Save methods for hex_stats / forecast_results / cohort_stats wiped prior
-- rows for the resource+city via DELETE-then-INSERT. The schema reserved a
-- snapshot_id column on every result table (001_init.sql), but the writes
-- never preserved it across compute runs — so historic snapshots were
-- silently overwritten.
--
-- This migration: rebuild hex_stats with an auto-increment id and no
-- composite PK so multiple snapshots can coexist for the same (hex_id,
-- resource_type, city_id). Existing rows are preserved; their NULL
-- snapshot_id stays NULL (legacy "unknown snapshot" semantics — surfaces
-- as the latest-overall result when nothing is pinned).
--
-- forecast_results and cohort_stats already used INTEGER PRIMARY KEY
-- AUTOINCREMENT, so their schema needs no change — only the Save-method
-- behavior changes, which is a Go-side fix (no SQL here).

CREATE TABLE hex_stats_new (
    id INTEGER PRIMARY KEY AUTOINCREMENT,
    hex_id TEXT NOT NULL,
    resource_type TEXT NOT NULL,
    city_id INTEGER NOT NULL REFERENCES cities(id),
    snapshot_id INTEGER REFERENCES snapshots(id),
    area_sqm REAL NOT NULL DEFAULT 0,
    pct_covered REAL NOT NULL DEFAULT 0,
    computed_at DATETIME NOT NULL DEFAULT CURRENT_TIMESTAMP
);

INSERT INTO hex_stats_new (hex_id, resource_type, city_id, snapshot_id, area_sqm, pct_covered, computed_at)
SELECT hex_id, resource_type, city_id, snapshot_id, area_sqm, pct_covered, computed_at FROM hex_stats;

DROP TABLE hex_stats;
ALTER TABLE hex_stats_new RENAME TO hex_stats;

CREATE INDEX IF NOT EXISTS idx_hex_stats_resource_type ON hex_stats(resource_type);
CREATE INDEX IF NOT EXISTS idx_hex_stats_city ON hex_stats(city_id);
CREATE INDEX IF NOT EXISTS idx_hex_stats_snapshot ON hex_stats(snapshot_id);
CREATE INDEX IF NOT EXISTS idx_hex_stats_lookup ON hex_stats(hex_id, resource_type, city_id, snapshot_id);
