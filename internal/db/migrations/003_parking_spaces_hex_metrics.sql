-- Schema additions since v0.4.1, in one additive migration.
--
-- Additive, not an edit of 001_init.sql: existing installs recorded
-- schema_version 1 and 2 and never re-run those files (see the 002 header), so
-- a column added to 001 would reach only fresh databases and every existing
-- one would fail on INSERT. ALTER TABLE ... ADD COLUMN is not idempotent, so
-- each column lives in exactly one migration — never also in 001.

-- Estimated parking spaces (solvent-streets-l3gz). The parking compute pass
-- derives a space count per lot (OSM capacity tag, else footprint area over
-- resource.ParkingSpaceAreaM2, times levels for structured parking) and
-- apportions it onto the hex grid alongside the covered area. The city
-- total is a column here; the per-hex shares are hex_metrics rows (below).
--
-- Non-parking rows keep the default 0; readers treat 0 as "no estimate".
ALTER TABLE compute_results ADD COLUMN estimated_spaces INTEGER NOT NULL DEFAULT 0;

-- Generic per-hex metrics (solvent-streets-ql2w). One row per
-- (city, snapshot, hex, metric): a named numeric value (internal/hexmetric)
-- such as fire_response_s, plus an optional reference id (the nearest
-- facility for a coverage metric). Each producer attaches its rows to the
-- compute snapshot they derive from, and a re-run replaces that
-- (city, metric, snapshot) slice: service coverage and census metrics attach
-- to the LATEST ROADS snapshot rather than minting one of their own (the
-- forecast pattern); the per-hex parking spaces (parking_spaces_all /
-- parking_spaces_city, deliberately not registered in internal/hexmetric)
-- attach to the parking compute's own snapshot, the one its hex_stats carry.
--
-- `metric` plays the role resource_type plays on the other result tables:
-- the snapshot-scoped list query is keyed on it, and the per-city composite
-- index below serves it as a seek.
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
