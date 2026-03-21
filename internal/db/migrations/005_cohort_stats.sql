CREATE TABLE IF NOT EXISTS cohort_stats (
    id INTEGER PRIMARY KEY AUTOINCREMENT,
    resource_type TEXT NOT NULL,
    classification TEXT NOT NULL,
    area_sqft REAL NOT NULL DEFAULT 0,
    feature_count INTEGER NOT NULL DEFAULT 0,
    snapshot_id INTEGER,
    computed_at DATETIME NOT NULL DEFAULT CURRENT_TIMESTAMP
);
