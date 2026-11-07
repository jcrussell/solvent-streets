package db

import (
	"context"
	"database/sql"
	"embed"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"time"

	"github.com/jcrussell/solvent-streets/internal/hint"
	"github.com/jcrussell/solvent-streets/internal/resource"

	_ "modernc.org/sqlite"
)

//go:embed migrations/*.sql
var migrationsFS embed.FS

// dsnPragmas is the query string appended to the db path to form the
// sqlite DSN. WAL improves concurrency, busy_timeout avoids spurious
// "database is locked" errors, and foreign_keys(on) enforces the FK
// constraints the schema declares (SQLite defaults them off).
//
// _txlock=immediate makes every BeginTx take the write lock up front. Write
// transactions here read before they write (CommitSnapshot reads its base
// manifest, DeleteSnapshot its parts, gc its orphans). A DEFERRED one that
// loses a race with another process fails with SQLITE_BUSY_SNAPSHOT (517),
// which busy_timeout cannot retry; IMMEDIATE waits out busy_timeout at BEGIN
// instead, and reads the newest committed state once it holds the lock.
const dsnPragmas = "?_pragma=journal_mode(WAL)&_pragma=busy_timeout(5000)&_pragma=foreign_keys(on)&_txlock=immediate"

type Feature struct {
	ID           string
	ResourceType resource.Type
	Name         string
	Tags         map[string]string
	GeometryJSON string // GeoJSON geometry
	SourceAPI    string
	FetchedAt    time.Time
}

// ResourceFeatures converts stored rows to the resource.Feature form the
// geometry passes consume (id, name, tags, geometry, source).
func ResourceFeatures(rows []Feature) []resource.Feature {
	out := make([]resource.Feature, len(rows))
	for i, r := range rows {
		out[i] = resource.Feature{ID: r.ID, Name: r.Name, Tags: r.Tags, GeometryJSON: r.GeometryJSON, SourceAPI: r.SourceAPI}
	}
	return out
}

type ComputeResult struct {
	ID           int64
	ResourceType resource.Type
	TotalArea    float64
	FeatureCount int
	// EstimatedSpaces is the parking resource's estimated vehicle capacity
	// (resource.EstimateParkingSpaces summed over the hex-clipped lots); 0 for
	// every other resource and for rows computed before the estimate existed.
	EstimatedSpaces int
	ComputedAt      time.Time
	RunID           int64
}

type StatusInfo struct {
	ResourceType    resource.Type
	FeatureCount    int
	LastIngestAt    *time.Time
	LastComputeAt   *time.Time
	TotalArea       float64
	EstimatedSpaces int // parking only; see ComputeResult.EstimatedSpaces
}

type HexStat struct {
	HexID        string
	ResourceType resource.Type
	Area         float64
	PctCovered   float64
	ComputedAt   time.Time
	RunID        int64
}

// HexMetric is one per-hex value of a registered metric (internal/hexmetric),
// e.g. fire_response_s. Ref is an optional reference id the metric attaches
// to the hex (the nearest facility for a coverage metric); "" when unused.
// RunID is the producer run that wrote it: a service or census run for those
// metrics, the parking compute run for the per-hex parking spaces
// (resource.SpacesMetricKey).
type HexMetric struct {
	HexID      string
	Metric     string
	Value      float64
	Ref        string
	RunID      int64
	ComputedAt time.Time
}

// Run is one producer's output for one city (see 001_init.sql). Producer is
// the snapshot part it fills, e.g. "roads", "forecast:roads", "census".
type Run struct {
	ID         int64     `json:"id"`
	Producer   string    `json:"producer"`
	ConfigHash string    `json:"config_hash"`
	ComputedAt time.Time `json:"computed_at"`
}

// Snapshot is a city's complete computed state: one run per part. Every
// reader resolves exactly one snapshot (Store.WithSnapshot / WithConfigHash /
// newest) and reads only its runs.
type Snapshot struct {
	ID         int64          `json:"id"`
	CreatedAt  time.Time      `json:"created_at"`
	ConfigHash string         `json:"config_hash"`
	Parts      []SnapshotPart `json:"parts"`
}

// StaleFor reports whether snaps (one city's) hold snapshots but none
// committed under configHash: the city was computed, just not under the
// settings now in force. Empty snaps (never computed) is not stale.
func StaleFor(snaps []Snapshot, configHash string) bool {
	for _, s := range snaps {
		if s.ConfigHash == configHash {
			return false
		}
	}
	return len(snaps) > 0
}

// SnapshotPart is one (part -> run) entry of a snapshot's manifest.
type SnapshotPart struct {
	Part       string    `json:"part"`
	RunID      int64     `json:"run_id"`
	ComputedAt time.Time `json:"computed_at"`
}

type ForecastResult struct {
	ID           int64
	ResourceType resource.Type
	Year         int
	PCI          float64
	Area         float64
	AnnualNeed   float64
	CostTier     string
	RunID        int64
	ComputedAt   time.Time
}

type CohortStat struct {
	ID             int64
	ResourceType   resource.Type
	Classification string
	Area           float64
	FeatureCount   int
	RunID          int64
	ComputedAt     time.Time
}

type City struct {
	ID   int64
	Slug string
	Name string
}

// GCResultCounts holds per-result-table row counts for a gc sweep
// (compute_results, hex_stats, forecast_results, cohort_stats, hex_metrics).
type GCResultCounts struct {
	ComputeResults  int
	HexStats        int
	ForecastResults int
	CohortStats     int
	HexMetrics      int
}

// Total returns the sum of all per-table counts.
func (c GCResultCounts) Total() int {
	return c.ComputeResults + c.HexStats + c.ForecastResults + c.CohortStats + c.HexMetrics
}

// GCReport summarizes the rows a gc scan/sweep found (or deleted), broken
// out by orphan class so the per-city summary can name each cause.
type GCReport struct {
	// StaleFeatures counts features whose source_api is a non-empty value
	// that is no longer resolvable from the city's configured sources.
	StaleFeatures int
	// OrphanRuns counts runs no snapshot references that are older than
	// OrphanRunGrace: left behind by a command that failed or was killed
	// before it committed. Younger unreferenced runs may belong to a command
	// still running, so they are never swept.
	OrphanRuns int
	// OrphanRunResults counts the result rows those runs wrote.
	OrphanRunResults GCResultCounts
}

// Total returns the grand total of every count in the report.
func (r GCReport) Total() int {
	return r.StaleFeatures + r.OrphanRuns + r.OrphanRunResults.Total()
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
	// HasHexStats reports whether ListHexStats would return any row for any
	// of types, under the same snapshot / config-hash resolution, with one
	// LIMIT 1 query instead of loading the rows.
	HasHexStats(ctx context.Context, types []resource.Type) (bool, error)
	// SaveHexMetrics replaces the (metric, runID) slice for this city with
	// rows, in one transaction. Every row is stored under metric and runID
	// regardless of its own fields. An empty rows slice stores nothing, and
	// because readers see only the resolved snapshot's runs, a run that
	// stored nothing for a metric hides that metric (no older run's rows
	// resurface).
	SaveHexMetrics(ctx context.Context, metric string, runID int64, rows []HexMetric) error
	// SaveHexMetricSet is SaveHexMetrics for several metrics at once, all in
	// ONE transaction: a producer that writes related metrics (population,
	// housing units, density) never leaves them half-saved.
	SaveHexMetricSet(ctx context.Context, runID int64, set map[string][]HexMetric) error
	// ListHexMetrics returns the rows for one metric key from the resolved
	// snapshot. A metric with no rows returns (nil, nil), never sql.ErrNoRows.
	ListHexMetrics(ctx context.Context, metric string) ([]HexMetric, error)
	// CreateRun starts a run for producer under configHash. Its rows are
	// invisible to every reader until a snapshot that includes the run is
	// committed (CommitSnapshot) or a view overlays it (WithParts).
	CreateRun(ctx context.Context, producer, configHash string) (*Run, error)
	// DiscardRun deletes a run and every row it wrote, provided no snapshot
	// references it (a committed run is only ever removed with its last
	// snapshot). Used to clean up after a failed or cancelled command.
	DiscardRun(ctx context.Context, runID int64) error
	// CommitSnapshot creates this city's next snapshot for configHash: the
	// newest existing snapshot with that hash, with parts applied on top
	// (part -> run; a run id of 0 removes the part). Every run in parts must
	// belong to this city, carry configHash, and have Producer == part. One
	// transaction; returns the new snapshot with its full manifest. A commit
	// that would not change the manifest writes nothing and returns the
	// current snapshot, or (nil, nil) when configHash has none, so no-op
	// commands never mint snapshots and no snapshot is ever empty. An empty
	// configHash is rejected. Retries while another process holds the lock.
	CommitSnapshot(ctx context.Context, configHash string, parts map[string]int64) (*Snapshot, error)
	// WithParts returns a view that reads the newest snapshot for configHash
	// with parts overlaid exactly as CommitSnapshot would apply them. A
	// command reads its own not-yet-committed runs through it (e.g. the
	// forecast that follows a recompute in the same command).
	WithParts(configHash string, parts map[string]int64) Store
	// ListSnapshots returns this city's snapshots newest first, each with
	// its manifest.
	ListSnapshots(ctx context.Context) ([]Snapshot, error)
	ResolveSnapshot(ctx context.Context, snapshotID int64) error
	WithSnapshot(snapshotID int64) Store
	WithConfigHash(configHash string) Store
	// DeleteSnapshot removes one snapshot's manifest, then every run no
	// remaining snapshot references, with the rows those runs wrote. Runs
	// still referenced by another snapshot are kept, so deleting any
	// snapshot never changes what another snapshot reads.
	DeleteSnapshot(ctx context.Context, snapshotID int64) (bool, error)
	// GCScan counts orphan rows for this city WITHOUT writing: features
	// with a stale source_api (non-empty, not in keepSources) and runs no
	// snapshot references that are older than OrphanRunGrace, with their
	// rows. An empty keepSources skips the feature scan entirely (never
	// reports every row as stale).
	GCScan(ctx context.Context, keepSources []string) (*GCReport, error)
	// GCSweep deletes the same orphan rows GCScan counts, in one
	// transaction, and returns the counts of rows actually deleted. An
	// empty keepSources skips the feature delete (never wipes all rows).
	GCSweep(ctx context.Context, keepSources []string) (*GCReport, error)
	SaveForecastResults(ctx context.Context, results []ForecastResult) error
	SaveCohortStats(ctx context.Context, stats []CohortStat) error
	ListCohortStats(ctx context.Context, resourceType resource.Type) ([]CohortStat, error)
	// ListCohortStatsForTypes returns cohort stats grouped by resource
	// type for each requested type in one round trip. Same snapshot /
	// config_hash semantics as ListCohortStats.
	ListCohortStatsForTypes(ctx context.Context, types []resource.Type) (map[resource.Type][]CohortStat, error)
	SaveBoundary(ctx context.Context, geometryJSON, source string) error
	GetBoundary(ctx context.Context) (string, error)
	Stats(ctx context.Context, resourceType resource.Type) (*StatusInfo, error)
	Close() error
}

type sqliteStore struct {
	db         *sql.DB
	cityID     int64
	snapshotID int64  // 0 = unpinned; >0 = read exactly this snapshot (wins over configHash)
	configHash string // "" = newest snapshot; non-empty = newest snapshot with this hash
	// overlay (WithParts) replaces or removes (run id 0) parts of the
	// resolved snapshot for reads, the way CommitSnapshot would apply them.
	overlay map[string]int64
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

	// The DSN is built by appending our pragma query string to the path.
	// A path that already contains a '?' would corrupt that query string
	// (the driver splits the DSN on the first '?'), silently dropping the
	// WAL/busy_timeout/foreign_keys pragmas we rely on. Reject it rather
	// than open a database with the wrong durability/integrity settings.
	// ":memory:" and DefaultPath() never contain '?', so this is a no-op
	// for the supported paths.
	if strings.Contains(path, "?") {
		return nil, fmt.Errorf("open sqlite: db path must not contain '?': %q", path)
	}

	db, err := sql.Open("sqlite", path+dsnPragmas)
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
		if errors.Is(err, ErrIncompatibleDatabase) {
			return nil, hint.Withf(fmt.Errorf("open %s: %w", path, err),
				"Move %s (and any -wal/-shm files beside it) aside; pvmt creates a fresh database on the "+
					"next run. Then re-ingest with `pvmt all ingest` and `pvmt all compute`. pvmt never "+
					"deletes it for you. See docs/troubleshooting.md#incompatible-database.", path)
		}
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
