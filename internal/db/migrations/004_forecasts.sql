CREATE TABLE IF NOT EXISTS forecast_results (
    id INTEGER PRIMARY KEY AUTOINCREMENT,
    resource_type TEXT NOT NULL,
    year INTEGER NOT NULL,
    pci REAL NOT NULL DEFAULT 0,
    area_sqft REAL NOT NULL DEFAULT 0,
    treatment_cost REAL NOT NULL DEFAULT 0,
    treatment_tier TEXT NOT NULL DEFAULT '',
    snapshot_id INTEGER REFERENCES snapshots(id),
    computed_at DATETIME NOT NULL DEFAULT CURRENT_TIMESTAMP
);

CREATE INDEX IF NOT EXISTS idx_forecast_results_type ON forecast_results(resource_type);
CREATE INDEX IF NOT EXISTS idx_forecast_results_snapshot ON forecast_results(snapshot_id);
