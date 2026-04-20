package db

import (
	"context"
	"database/sql"
	"embed"
	"errors"
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
	TotalAreaSqM float64
	FeatureCount int
	ComputedAt   time.Time
	SnapshotID   *int64
}

type StatusInfo struct {
	ResourceType  string
	FeatureCount  int
	LastIngestAt  *time.Time
	LastComputeAt *time.Time
	TotalAreaSqM  float64
}

type HexStat struct {
	HexID        string
	ResourceType string
	AreaSqM      float64
	PctCovered   float64
	ComputedAt   time.Time
	SnapshotID   *int64
}

type Snapshot struct {
	ID         int64
	ComputedAt time.Time
	ConfigHash string
}

type ForecastResult struct {
	ID            int64     `json:"-"`
	ResourceType  string    `json:"resourceType"`
	Year          int       `json:"year"`
	PCI           float64   `json:"pci"`
	AreaSqM       float64   `json:"areaSqM"`
	TreatmentCost float64   `json:"treatmentCost"`
	TreatmentTier string    `json:"treatmentTier"`
	SnapshotID    *int64    `json:"-"`
	ComputedAt    time.Time `json:"-"`
}

type CohortStat struct {
	ID             int64
	ResourceType   string
	Classification string
	AreaSqM        float64
	FeatureCount   int
	SnapshotID     *int64
	ComputedAt     time.Time
}

type City struct {
	ID   int64
	Slug string
	Name string
}

type Store interface {
	UpsertFeatures(ctx context.Context, resourceType string, features []Feature) error
	ListFeatures(ctx context.Context, resourceType string) ([]Feature, error)
	SaveComputeResult(ctx context.Context, result ComputeResult) error
	LatestComputeResult(ctx context.Context, resourceType string) (*ComputeResult, error)
	SaveHexStats(ctx context.Context, stats []HexStat) error
	ListHexStats(ctx context.Context, resourceType string) ([]HexStat, error)
	CreateSnapshot(ctx context.Context, configHash string) (*Snapshot, error)
	ListSnapshots(ctx context.Context) ([]Snapshot, error)
	SaveForecastResults(ctx context.Context, results []ForecastResult) error
	ListForecastResults(ctx context.Context, resourceType string) ([]ForecastResult, error)
	SaveCohortStats(ctx context.Context, stats []CohortStat) error
	ListCohortStats(ctx context.Context, resourceType string) ([]CohortStat, error)
	SaveBoundary(ctx context.Context, geometryJSON, source string) error
	GetBoundary(ctx context.Context) (string, error)
	Stats(ctx context.Context, resourceType string) (*StatusInfo, error)
	ResourceTypes(ctx context.Context) ([]string, error)
	Close() error
}

type sqliteStore struct {
	db     *sql.DB
	cityID int64
}

var _ Store = (*sqliteStore)(nil)

// RootStorer is the interface for managing cities and providing city-scoped stores.
type RootStorer interface {
	EnsureCity(ctx context.Context, slug, name string) (int64, error)
	LookupCity(ctx context.Context, slug string) (City, error)
	ListCities(ctx context.Context) ([]City, error)
	ForCity(id int64) Store
	Close() error
}

// RootStore manages the shared database and provides city-scoped stores.
type RootStore struct {
	db *sql.DB
}

var _ RootStorer = (*RootStore)(nil)

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

func Open(path string) (retStore *RootStore, retErr error) {
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

	db, err := sql.Open("sqlite", path+"?_pragma=journal_mode(WAL)&_pragma=busy_timeout(5000)&_pragma=foreign_keys(on)")
	if err != nil {
		return nil, fmt.Errorf("open sqlite: %w", err)
	}
	db.SetMaxOpenConns(1)

	defer func() {
		if retErr != nil {
			retErr = errors.Join(retErr, db.Close())
		}
	}()

	if err := migrate(context.Background(), db); err != nil {
		return nil, fmt.Errorf("migrate: %w", err)
	}

	return &RootStore{db: db}, nil
}

// EnsureCity inserts or retrieves a city by slug, returning its ID.
func (r *RootStore) EnsureCity(ctx context.Context, slug, name string) (int64, error) {
	_, err := r.db.ExecContext(ctx, `INSERT OR IGNORE INTO cities (slug, name) VALUES (?, ?)`, slug, name)
	if err != nil {
		return 0, fmt.Errorf("ensure city: %w", err)
	}
	var id int64
	err = r.db.QueryRowContext(ctx, `SELECT id FROM cities WHERE slug = ?`, slug).Scan(&id)
	if err != nil {
		return 0, fmt.Errorf("get city id: %w", err)
	}
	return id, nil
}

// LookupCity retrieves a city by slug without creating it. Returns
// sql.ErrNoRows (wrapped) if the city does not exist.
func (r *RootStore) LookupCity(ctx context.Context, slug string) (City, error) {
	var c City
	err := r.db.QueryRowContext(ctx, `SELECT id, slug, name FROM cities WHERE slug = ?`, slug).Scan(&c.ID, &c.Slug, &c.Name)
	if err != nil {
		return City{}, fmt.Errorf("lookup city %q: %w", slug, err)
	}
	return c, nil
}

// ListCities returns all cities in the database.
func (r *RootStore) ListCities(ctx context.Context) ([]City, error) {
	rows, err := r.db.QueryContext(ctx, `SELECT id, slug, name FROM cities ORDER BY name`)
	if err != nil {
		return nil, fmt.Errorf("list cities: %w", err)
	}
	defer func() { _ = rows.Close() }()

	var cities []City
	for rows.Next() {
		var c City
		if err := rows.Scan(&c.ID, &c.Slug, &c.Name); err != nil {
			return nil, fmt.Errorf("scan city: %w", err)
		}
		cities = append(cities, c)
	}
	return cities, rows.Err()
}

// ForCity returns a city-scoped Store. The returned store shares the
// underlying *sql.DB connection pool, which is safe for concurrent use.
// WAL mode (set at open time) allows concurrent readers.
func (r *RootStore) ForCity(id int64) Store {
	return &sqliteStore{db: r.db, cityID: id}
}

// Close closes the underlying database connection.
func (r *RootStore) Close() error {
	return r.db.Close()
}
