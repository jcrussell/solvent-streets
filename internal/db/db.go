package db

import (
	"database/sql"
	"embed"
	"fmt"
	"os"
	"path/filepath"
	"time"

	_ "modernc.org/sqlite"
)

//go:embed migrations/*.sql
var migrationsFS embed.FS

type Feature struct {
	ID           string
	ResourceType string
	Name         string
	Tags         map[string]string
	GeometryJSON string // GeoJSON geometry
	SourceAPI    string
	FetchedAt    time.Time
}

type ComputeResult struct {
	ID           int64
	ResourceType string
	TotalAreaSqFt float64
	TotalAreaAcres float64
	FeatureCount  int
	GeometryJSON  string // Union GeoJSON for visualization
	ComputedAt    time.Time
}

type StatusInfo struct {
	ResourceType     string
	FeatureCount     int
	LastIngestAt     *time.Time
	LastComputeAt    *time.Time
	TotalAreaSqFt    float64
	TotalAreaAcres   float64
}

type Store interface {
	UpsertFeatures(resourceType string, features []Feature) error
	ListFeatures(resourceType string) ([]Feature, error)
	SaveComputeResult(result ComputeResult) error
	LatestComputeResult(resourceType string) (*ComputeResult, error)
	Stats(resourceType string) (*StatusInfo, error)
	ResourceTypes() ([]string, error)
	Close() error
}

type sqliteStore struct {
	db *sql.DB
}

func DefaultPath() (string, error) {
	home, err := os.UserHomeDir()
	if err != nil {
		return "", err
	}
	dir := filepath.Join(home, ".local", "share", "pvmt")
	if err := os.MkdirAll(dir, 0o755); err != nil {
		return "", err
	}
	return filepath.Join(dir, "pvmt.db"), nil
}

func Open(path string) (Store, error) {
	if path == "" {
		var err error
		path, err = DefaultPath()
		if err != nil {
			return nil, fmt.Errorf("default db path: %w", err)
		}
	}
	if dir := filepath.Dir(path); dir != "" {
		if err := os.MkdirAll(dir, 0o755); err != nil {
			return nil, fmt.Errorf("create db dir: %w", err)
		}
	}

	db, err := sql.Open("sqlite", path+"?_pragma=journal_mode(WAL)&_pragma=busy_timeout(5000)")
	if err != nil {
		return nil, fmt.Errorf("open sqlite: %w", err)
	}

	if err := migrate(db); err != nil {
		db.Close()
		return nil, fmt.Errorf("migrate: %w", err)
	}

	return &sqliteStore{db: db}, nil
}
