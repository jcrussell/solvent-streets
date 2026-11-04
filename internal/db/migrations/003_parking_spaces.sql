-- Estimated parking spaces (solvent-streets-l3gz). The parking compute pass
-- now derives a space count per lot (OSM capacity tag, else footprint area
-- over resource.ParkingSpaceAreaM2, times levels for structured parking) and
-- apportions it onto the hex grid alongside the covered area.
--
-- Additive migration, not an edit of 001_init.sql: existing installs recorded
-- schema_version 1 and 2 and never re-run those files (see the 002 header), so
-- a column added to 001 would reach only fresh databases and every existing
-- one would fail on INSERT. ALTER TABLE ... ADD COLUMN is not idempotent, so
-- each column lives in exactly one migration — never also in 001.
--
-- Non-parking rows keep the default 0; readers treat 0 as "no estimate".
ALTER TABLE compute_results ADD COLUMN estimated_spaces INTEGER NOT NULL DEFAULT 0;
ALTER TABLE hex_stats ADD COLUMN estimated_spaces REAL NOT NULL DEFAULT 0;
