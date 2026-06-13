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

	"github.com/jcrussell/solvent-streets/internal/resource"

	_ "modernc.org/sqlite"
)

//go:embed migrations/*.sql
var migrationsFS embed.FS

type Feature struct {
	ID           string
	ResourceType resource.Type
	Name         string
	Tags         map[string]string
	GeometryJSON string // GeoJSON geometry
	SourceAPI    string
	FetchedAt    time.Time
}

type ComputeResult struct {
	ID           int64
	ResourceType resource.Type
	TotalArea    float64
	FeatureCount int
	ComputedAt   time.Time
	SnapshotID   *int64
}

type StatusInfo struct {
	ResourceType  resource.Type
	FeatureCount  int
	LastIngestAt  *time.Time
	LastComputeAt *time.Time
	TotalArea     float64
}

type HexStat struct {
	HexID        string
	ResourceType resource.Type
	Area         float64
	PctCovered   float64
	ComputedAt   time.Time
	SnapshotID   *int64
}

type Snapshot struct {
	ID         int64     `json:"id"`
	ComputedAt time.Time `json:"computed_at"`
	ConfigHash string    `json:"config_hash"`
}

type ForecastResult struct {
	ID            int64         `json:"-"`
	ResourceType  resource.Type `json:"resourceType"`
	Year          int           `json:"year"`
	PCI           float64       `json:"pci"`
	Area          float64       `json:"area"`
	TreatmentCost float64       `json:"treatmentCost"`
	TreatmentTier string        `json:"treatmentTier"`
	SnapshotID    *int64        `json:"-"`
	ComputedAt    time.Time     `json:"-"`
}

type CohortStat struct {
	ID             int64
	ResourceType   resource.Type
	Classification string
	Area           float64
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
	UpsertFeatures(ctx context.Context, resourceType resource.Type, features []Feature, sourceAPIs []string) error
	ListFeatures(ctx context.Context, resourceType resource.Type) ([]Feature, error)
	SaveComputeResult(ctx context.Context, result ComputeResult) error
	LatestComputeResult(ctx context.Context, resourceType resource.Type) (*ComputeResult, error)
	// LatestComputeResults returns the latest compute result for each
	// requested resource type in one round trip. Missing types are
	// simply absent from the returned map (no sql.ErrNoRows). Honors
	// the same snapshot / config_hash filtering as LatestComputeResult.
	LatestComputeResults(ctx context.Context, types []resource.Type) (map[resource.Type]*ComputeResult, error)
	SaveHexStats(ctx context.Context, stats []HexStat) error
	ListHexStats(ctx context.Context, resourceType resource.Type) ([]HexStat, error)
	CreateSnapshot(ctx context.Context, configHash string) (*Snapshot, error)
	ListSnapshots(ctx context.Context) ([]Snapshot, error)
	ResolveSnapshot(ctx context.Context, snapshotID int64) error
	WithSnapshot(snapshotID int64) Store
	WithConfigHash(configHash string) Store
	DeleteSnapshot(ctx context.Context, snapshotID int64) (bool, error)
	SaveForecastResults(ctx context.Context, results []ForecastResult) error
	ListForecastResults(ctx context.Context, resourceType resource.Type) ([]ForecastResult, error)
	SaveCohortStats(ctx context.Context, stats []CohortStat) error
	ListCohortStats(ctx context.Context, resourceType resource.Type) ([]CohortStat, error)
	// ListCohortStatsForTypes returns cohort stats grouped by resource
	// type for each requested type in one round trip. Same snapshot /
	// config_hash semantics as ListCohortStats.
	ListCohortStatsForTypes(ctx context.Context, types []resource.Type) (map[resource.Type][]CohortStat, error)
	SaveBoundary(ctx context.Context, geometryJSON, source string) error
	GetBoundary(ctx context.Context) (string, error)
	Stats(ctx context.Context, resourceType resource.Type) (*StatusInfo, error)
	ResourceTypes(ctx context.Context) ([]resource.Type, error)
	Close() error
}

type sqliteStore struct {
	db         *sql.DB
	cityID     int64
	snapshotID int64  // 0 = unpinned; >0 = snapshot-scoped reads (wins over configHash)
	configHash string // "" = unpinned; non-empty = filter unpinned reads to snapshots with this hash
}

var _ Store = (*sqliteStore)(nil)

// RootStorer is the interface for managing cities and providing city-scoped stores.
type RootStorer interface {
	EnsureCity(ctx context.Context, slug, name, configID string) (int64, error)
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

// EnsureCity inserts or retrieves a city by (slug, configID), returning
// its ID. Two configs that share a slug (e.g. both define a city named
// "Austin") get distinct city_ids when they have distinct config IDs,
// so per-city feature/snapshot data does not collide.
//
// configID is the opaque config identifier from config.Config.ConfigID.
// Callers must not pass an empty value; the field is auto-populated at
// load time from a hash of the config's absolute path when the user
// did not set config_id explicitly.
func (r *RootStore) EnsureCity(ctx context.Context, slug, name, configID string) (int64, error) {
	if configID == "" {
		return 0, errors.New("ensure city: config_id is required")
	}
	_, err := r.db.ExecContext(ctx,
		`INSERT INTO cities (slug, name, config_id) VALUES (?, ?, ?)
		 ON CONFLICT(slug, config_id) DO UPDATE SET name = excluded.name`,
		slug, name, configID)
	if err != nil {
		return 0, fmt.Errorf("ensure city: %w", err)
	}
	var id int64
	err = r.db.QueryRowContext(ctx,
		`SELECT id FROM cities WHERE slug = ? AND config_id = ?`,
		slug, configID).Scan(&id)
	if err != nil {
		return 0, fmt.Errorf("get city id: %w", err)
	}
	return id, nil
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
