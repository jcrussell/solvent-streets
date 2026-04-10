-- Add missing indexes for city-scoped queries.
CREATE INDEX IF NOT EXISTS idx_snapshots_city ON snapshots(city_id);
CREATE INDEX IF NOT EXISTS idx_cohort_stats_city ON cohort_stats(city_id);
CREATE INDEX IF NOT EXISTS idx_cohort_stats_resource_type ON cohort_stats(resource_type);
