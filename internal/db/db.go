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
// transactions here read before they write (CommitLatest reads its base
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

type StatusInfo struct {
	ResourceType    resource.Type
	FeatureCount    int
	LastIngestAt    *time.Time
	LastComputeAt   *time.Time
	TotalArea       float64
	EstimatedSpaces int // parking only; resource.MetricParkingSpaces
}

// CityMetric is one city-wide measure written by a run (city_metrics in
// 001_init.sql): Metric measured over Subject, for one highway Class of the
// cohort split or the whole city (Class ""). See internal/resource/metric.go
// for the pavement keys and the subject convention.
type CityMetric struct {
	Metric     string
	Subject    string
	Class      string
	Value      float64
	RunID      int64
	ComputedAt time.Time // the run's
}

// HexMetric is one per-hex measure written by a run (hex_metrics in
// 001_init.sql): Metric measured over Subject in hex HexID. Pavement coverage
// uses the resource metric keys with a resource subject; the registered
// metrics of internal/hexmetric (fire_response_s, population, ...) use
// subject "". Ref is an optional reference id the metric attaches to the hex
// (the nearest facility for a coverage metric); "" when unused.
type HexMetric struct {
	HexID      string
	Metric     string
	Subject    string
	Value      float64
	Ref        string
	RunID      int64
	ComputedAt time.Time // the run's
}

// RunFacility is one point of service a service run routed from
// (run_facilities in 001_init.sql): an ingested OSM facility (Source "osm",
// ID its feature id) or a hand-placed [[cities.facilities]] entry (Source
// "config"). Freezing them with the run lets export draw the stations that
// produced a snapshot's coverage, whatever was ingested or configured since.
type RunFacility struct {
	ID     string
	Name   string
	Source string
	Lon    float64
	Lat    float64
}

// Run is one producer's output for one city (see 001_init.sql). Producer is
// the snapshot part it fills, e.g. "roads", "service:fire", "census".
type Run struct {
	ID         int64     `json:"id"`
	Producer   string    `json:"producer"`
	ConfigHash string    `json:"config_hash"`
	ComputedAt time.Time `json:"computed_at"`
}

// Snapshot is a city's computed state frozen by `pvmt snapshots create`: one
// run per part. It never changes after it is created. Every reader resolves
// exactly one manifest (a snapshot via Store.WithSnapshot or the newest, or
// the latest compute via WithLatestCompute / WithRuns) and reads only its
// runs.
type Snapshot struct {
	ID         int64     `json:"id"`
	CreatedAt  time.Time `json:"created_at"`
	ConfigHash string    `json:"config_hash"`
	// HexEdgeM is the hex edge, in meters, the snapshot's grid was tiled at.
	HexEdgeM float64        `json:"hex_edge_m"`
	Parts    []SnapshotPart `json:"parts"`
	// BoundaryDigest identifies the boundary the snapshot froze (the
	// snapshot_boundaries digest); "" when it froze none. Not published: it
	// keys caches of what is drawn from that boundary.
	BoundaryDigest string `json:"-"`
}

// SnapshotPart is one (part -> run) entry of a snapshot's manifest.
type SnapshotPart struct {
	Part       string    `json:"part"`
	RunID      int64     `json:"run_id"`
	ComputedAt time.Time `json:"computed_at"`
}

type City struct {
	ID   int64
	Slug string
	Name string
}

// GCResultCounts holds per-result-table row counts for a gc sweep
// (city_metrics, hex_metrics).
type GCResultCounts struct {
	CityMetrics int
	HexMetrics  int
}

// Total returns the sum of all per-table counts.
func (c GCResultCounts) Total() int {
	return c.CityMetrics + c.HexMetrics
}

// GCReport summarizes the rows a gc scan/sweep found (or deleted), broken
// out by orphan class so the per-city summary can name each cause.
type GCReport struct {
	// StaleFeatures counts features whose source_api is a non-empty value
	// that is no longer resolvable from the city's configured sources.
	StaleFeatures int
	// OrphanRuns counts runs neither a snapshot nor the latest compute
	// references that are older than
	// OrphanRunGrace: left behind by a command that failed or was killed
	// before it committed. Younger unreferenced runs may belong to a command
	// still running, so they are never swept.
	OrphanRuns int
	// OrphanRunResults counts the result rows those runs wrote.
	OrphanRunResults GCResultCounts
	// SupersededParts counts latest-compute parts committed under an older
	// ComputeEpoch. No config can produce their hash again, so they are
	// dropped and their runs counted as orphans.
	SupersededParts int
}

// Total returns the grand total of every count in the report.
func (r GCReport) Total() int {
	return r.StaleFeatures + r.SupersededParts + r.OrphanRuns + r.OrphanRunResults.Total()
}

type Store interface {
	UpsertFeatures(ctx context.Context, resourceType resource.Type, features []Feature, sourceAPIs []string) error
	ListFeatures(ctx context.Context, resourceType resource.Type) ([]Feature, error)
	// SaveCityMetrics writes city-wide measures for runID, in one
	// transaction. Every row is stored under runID regardless of its own
	// RunID. Rows are write-once: a (metric, subject, class) the run already
	// holds fails the whole save.
	SaveCityMetrics(ctx context.Context, runID int64, rows []CityMetric) error
	// ListCityMetrics returns every city-wide measure over any of subjects in
	// the resolved snapshot, ordered by subject, class, metric. No rows
	// returns (nil, nil), never sql.ErrNoRows.
	ListCityMetrics(ctx context.Context, subjects []string) ([]CityMetric, error)
	// SaveHexMetrics writes per-hex measures for runID, in one transaction.
	// Every row is stored under runID regardless of its own RunID. Rows are
	// write-once: a (metric, subject, hex) the run already holds fails the
	// whole save. Because readers see only the resolved snapshot's runs, a
	// run that stored nothing for a metric hides that metric (no older run's
	// rows resurface).
	SaveHexMetrics(ctx context.Context, runID int64, rows []HexMetric) error
	// ListHexMetrics returns one metric's rows over subject from the resolved
	// snapshot, ordered by hex id. No rows returns (nil, nil), never
	// sql.ErrNoRows.
	ListHexMetrics(ctx context.Context, metric, subject string) ([]HexMetric, error)
	// HasHexMetric reports whether ListHexMetrics would return any row for
	// metric over any of subjects, with one LIMIT 1 query.
	HasHexMetric(ctx context.Context, metric string, subjects []string) (bool, error)
	// SaveRunFacilities records the facilities runID routed from, in one
	// transaction. Write-once like the metric rows: an id the run already
	// holds fails the whole save.
	SaveRunFacilities(ctx context.Context, runID int64, facilities []RunFacility) error
	// ListRunFacilities returns the facilities recorded by the run that fills
	// producer's part of the resolved snapshot, ordered by id. No such part,
	// or a run that recorded none, returns (nil, nil).
	ListRunFacilities(ctx context.Context, producer string) ([]RunFacility, error)
	// CreateRun starts a run for producer under configHash. Its rows are
	// invisible to every reader until the run is committed into the latest
	// compute (CommitLatest).
	// inputs is the digest of every input the producer read
	// (ReadProducerInputs), recorded with the run in the same transaction.
	CreateRun(ctx context.Context, producer, configHash string, inputs map[string]string) (*Run, error)
	// DiscardRun deletes a run and every row it wrote, provided no snapshot
	// references it (a committed run is only ever removed with its last
	// snapshot). Used to clean up after a failed or cancelled command.
	DiscardRun(ctx context.Context, runID int64) error
	// CommitLatest applies parts (part -> run; a run id of 0 removes the
	// part) to this city's latest compute for configHash (working_parts):
	// the parts the command produced swap in, every other part carries over.
	// Every run in parts must belong to this city, carry configHash, and have
	// Producer == part. One transaction. A commit that would not change the
	// manifest writes nothing. A replaced run is not deleted here: once no
	// snapshot or latest compute references it, gc sweeps it. Nothing is
	// published: a snapshot never changes after CreateSnapshot. An empty
	// configHash is rejected. computeEpoch (config.ComputeEpoch) is recorded
	// with the parts, for gc. Retries while another process holds the lock.
	CommitLatest(ctx context.Context, configHash string, computeEpoch int, parts map[string]int64) error
	// LatestCompute returns this city's latest compute under configHash
	// (working_parts): one entry per part, ordered by part, each with its run,
	// the run's computed_at and the input digests the run recorded. No parts
	// returns (nil, nil).
	LatestCompute(ctx context.Context, configHash string) ([]LatestPart, error)
	// CreateSnapshot freezes the latest compute under configHash as a
	// snapshot tiled at hexEdgeM (> 0), with the city's current boundary, in
	// one IMMEDIATE transaction: it reads the latest compute and the current
	// input states, runs check on them (nil skips it), and returns check's
	// error unchanged when it refuses, so no ingest or compute can land
	// between the check and the freeze. An empty latest compute is refused
	// with ErrNothingComputed. When the city's newest snapshot already has
	// the same hash, edge, boundary and manifest it is returned with created
	// false and nothing is written. Retries while another process holds the
	// lock.
	CreateSnapshot(ctx context.Context, configHash string, hexEdgeM float64, check CheckFunc) (snap *Snapshot, created bool, err error)
	// ListSnapshots returns this city's snapshots newest first, each with
	// its manifest.
	ListSnapshots(ctx context.Context) ([]Snapshot, error)
	ResolveSnapshot(ctx context.Context, snapshotID int64) error
	// ViewSnapshot returns the snapshot this view reads, with its manifest:
	// the pinned one (ErrSnapshotGone when it no longer exists), else, for a
	// view of neither a latest compute nor pinned runs, the newest. (nil,
	// nil) when the view reads no snapshot.
	ViewSnapshot(ctx context.Context) (*Snapshot, error)
	// WithSnapshot returns a view pinned to one snapshot: its runs and its
	// frozen boundary. Every snapshot-scoped read of the view returns
	// ErrSnapshotGone once that snapshot no longer exists.
	WithSnapshot(snapshotID int64) Store
	// WithLatestCompute returns a view of the latest compute under
	// configHash, resolved on every read.
	WithLatestCompute(configHash string) Store
	// WithRuns returns a view that reads exactly runIDs (a latest compute
	// pinned at one moment); every read returns ErrSnapshotGone once any of
	// them is gone.
	WithRuns(runIDs []int64) Store
	// DeleteSnapshot removes one snapshot's manifest, then every run no
	// remaining snapshot or the latest compute references, with the rows
	// those runs wrote, and any frozen boundary no snapshot uses any more.
	// Runs still referenced elsewhere are kept, so deleting any snapshot
	// never changes what another snapshot or the latest compute reads.
	DeleteSnapshot(ctx context.Context, snapshotID int64) (bool, error)
	// GCScan counts orphan rows for this city WITHOUT writing: features
	// with a stale source_api (non-empty, not in keepSources), latest-compute
	// parts committed under a ComputeEpoch older than computeEpoch, and runs
	// older than OrphanRunGrace that no snapshot and no current-epoch latest
	// compute references, with their rows. An empty keepSources skips the
	// feature scan entirely (never reports every row as stale).
	GCScan(ctx context.Context, keepSources []string, computeEpoch int) (*GCReport, error)
	// GCSweep deletes the same orphan rows GCScan counts, in one
	// transaction, and returns the counts of rows actually deleted. An
	// empty keepSources skips the feature delete (never wipes all rows).
	// It refreshes the input digest of every type it deleted features from,
	// and drops frozen boundaries no snapshot references.
	GCSweep(ctx context.Context, keepSources []string, computeEpoch int) (*GCReport, error)
	// InputStates returns every input this city has stored (city_inputs),
	// ordered by input.
	InputStates(ctx context.Context) ([]InputState, error)
	// InputDigests returns the current digest of each named input; one never
	// stored maps to "".
	InputDigests(ctx context.Context, inputs ...string) (map[string]string, error)
	SaveBoundary(ctx context.Context, geometryJSON, source string) error
	// GetBoundary returns the live boundary ("" when none), or for a view
	// pinned with WithSnapshot the boundary that snapshot froze.
	GetBoundary(ctx context.Context) (string, error)
	// RecordIngestAttempts stores how each source answered an ingest
	// (ingest_attempts), keeping the newest few per (input, source).
	RecordIngestAttempts(ctx context.Context, attempts []IngestAttempt) error
	// IngestHistory returns the city's newest attempt, success and failure
	// per (input, source).
	IngestHistory(ctx context.Context) ([]IngestHistory, error)
	Stats(ctx context.Context, resourceType resource.Type) (*StatusInfo, error)
	Close() error
}

type sqliteStore struct {
	db         *sql.DB
	cityID     int64
	snapshotID int64   // 0 = unpinned; >0 = read exactly this snapshot (wins over the rest)
	runs       []int64 // with runsPinned: read exactly these runs (WithRuns)
	runsPinned bool
	configHash string // "" = newest snapshot; non-empty = the latest compute under this hash
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
