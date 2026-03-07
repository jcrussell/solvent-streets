CREATE TABLE IF NOT EXISTS hex_stats (
    hex_id TEXT NOT NULL,
    resource_type TEXT NOT NULL,
    area_sqft REAL NOT NULL DEFAULT 0,
    pct_covered REAL NOT NULL DEFAULT 0,
    computed_at DATETIME NOT NULL DEFAULT CURRENT_TIMESTAMP,
    PRIMARY KEY (hex_id, resource_type)
);

CREATE INDEX IF NOT EXISTS idx_hex_stats_resource_type ON hex_stats(resource_type);
