-- Index snapshots.config_hash so the JOIN in the snapshot-aware reads'
-- arm-2 subquery (latest snapshot for this city+resource matching a
-- given config hash) doesn't sequential-scan snapshots on every read.
--
-- The snapshots table is small (tens to low hundreds of rows per
-- city), so this is defensive — the JOIN runs once per ListHexStats /
-- ListCohortStats / ListForecastResults / LatestComputeResult call,
-- and gensite does ~10 such calls per city × hundreds of cities.
CREATE INDEX IF NOT EXISTS idx_snapshots_config_hash ON snapshots(config_hash);
