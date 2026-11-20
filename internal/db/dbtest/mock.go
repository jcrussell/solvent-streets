package dbtest

import (
	"cmp"
	"context"
	"fmt"
	"maps"
	"slices"
	"sync/atomic"

	"github.com/jcrussell/solvent-streets/internal/config"
	"github.com/jcrussell/solvent-streets/internal/db"
	"github.com/jcrussell/solvent-streets/internal/resource"
)

var _ db.RootStorer = (*MockRootStore)(nil)
var _ db.Store = (*MockStore)(nil)

// MockStore is a func-field based mock implementing db.Store.
// Each method delegates to its corresponding func field if set,
// otherwise returns a zero-value success. List* methods return a
// non-nil empty slice rather than nil so tests can distinguish "no
// rows" from "not mocked" via len. This is a DELIBERATE divergence
// from the real sqliteStore, whose List* methods use `var xs []T` +
// append and so return NIL on zero rows (see internal/db/store.go).
// Tests that assert the empty-path JSON shape (nil -> `null` vs
// `[]`) must therefore use a real in-memory store, not this mock.
type MockStore struct {
	UpsertFeaturesFunc    func(context.Context, resource.Type, []db.Feature, []string) error
	ListFeaturesFunc      func(context.Context, resource.Type) ([]db.Feature, error)
	SaveCityMetricsFunc   func(context.Context, int64, []db.CityMetric) error
	ListCityMetricsFunc   func(context.Context, []string) ([]db.CityMetric, error)
	SaveHexMetricsFunc    func(context.Context, int64, []db.HexMetric) error
	ListHexMetricsFunc    func(context.Context, string, string) ([]db.HexMetric, error)
	HasHexMetricFunc      func(context.Context, string, []string) (bool, error)
	SaveRunFacilitiesFunc func(context.Context, int64, []db.RunFacility) error
	ListRunFacilitiesFunc func(context.Context, string) ([]db.RunFacility, error)
	CreateRunFunc         func(context.Context, string, string, map[string]string) (*db.Run, error)
	DiscardRunFunc        func(context.Context, int64) error
	CommitLatestFunc      func(context.Context, string, map[string]int64) error
	LatestComputeFunc     func(context.Context, string) ([]db.LatestPart, error)
	ListSnapshotsFunc     func(context.Context) ([]db.SnapshotCity, error)
	ResolveSnapshotFunc   func(context.Context, string) (int64, error)
	ViewSnapshotFunc      func(context.Context) (*db.SnapshotCity, error)
	WithSnapshotFunc      func(int64) db.Store
	WithLatestComputeFunc func(string) db.Store
	WithRunsFunc          func([]int64) db.Store
	GCScanFunc            func(context.Context, []string) (*db.GCReport, error)
	GCSweepFunc           func(context.Context, []string) (*db.GCReport, error)
	InputStatesFunc       func(context.Context) ([]db.InputState, error)
	InputDigestsFunc      func(context.Context, ...string) (map[string]string, error)
	SaveBoundaryFunc      func(context.Context, string, string) error
	GetBoundaryFunc       func(context.Context) (string, error)
	StatsFunc             func(context.Context, resource.Type) (*db.StatusInfo, error)
	IngestHistoryFunc     func(context.Context) ([]db.IngestHistory, error)
	CloseFunc             func() error

	// CityMetrics and HexMetrics are the rows the default result methods
	// read and write: SaveCityMetrics / SaveHexMetrics append (tagged with
	// the run), and the List / Has methods filter them, ordered like the real
	// store. Seed them with internal/results' row builders. Snapshots are not
	// modelled: every row is visible.
	CityMetrics []db.CityMetric
	HexMetrics  []db.HexMetric

	// Digests is what the default InputDigests reports (an input not in it
	// reads ""), and RunInputs is what the default CreateRun records: run
	// id -> the inputs passed with it.
	Digests   map[string]string
	RunInputs map[int64]map[string]string

	// Facilities is what the default ListRunFacilities reads: producer ->
	// the facilities its run recorded. The default SaveRunFacilities files
	// rows under the producer the default CreateRun gave the run.
	Facilities map[string][]db.RunFacility

	// Attempts is what the default RecordIngestAttempts appends to.
	Attempts []db.IngestAttempt

	lastRunID    int64            // CreateRun's default id sequence
	runProducers map[int64]string // CreateRun's default run id -> producer
}

func (m *MockStore) UpsertFeatures(ctx context.Context, rt resource.Type, f []db.Feature, sourceAPIs []string) error {
	if m.UpsertFeaturesFunc != nil {
		return m.UpsertFeaturesFunc(ctx, rt, f, sourceAPIs)
	}
	return nil
}

func (m *MockStore) ListFeatures(ctx context.Context, rt resource.Type) ([]db.Feature, error) {
	if m.ListFeaturesFunc != nil {
		return m.ListFeaturesFunc(ctx, rt)
	}
	return []db.Feature{}, nil
}

// SaveCityMetrics delegates to SaveCityMetricsFunc if set, otherwise
// appends rows to CityMetrics under runID, refusing a key the metric catalog
// does not store city-wide as the real store does.
func (m *MockStore) SaveCityMetrics(ctx context.Context, runID int64, rows []db.CityMetric) error {
	if m.SaveCityMetricsFunc != nil {
		return m.SaveCityMetricsFunc(ctx, runID, rows)
	}
	if err := db.CheckCityMetrics(rows); err != nil {
		return err
	}
	for _, r := range rows {
		r.RunID = runID
		m.CityMetrics = append(m.CityMetrics, r)
	}
	return nil
}

// ListCityMetrics delegates to ListCityMetricsFunc if set, otherwise returns
// the CityMetrics rows over subjects ordered by subject, class, metric, run.
func (m *MockStore) ListCityMetrics(ctx context.Context, subjects []string) ([]db.CityMetric, error) {
	if m.ListCityMetricsFunc != nil {
		return m.ListCityMetricsFunc(ctx, subjects)
	}
	out := []db.CityMetric{}
	for _, r := range m.CityMetrics {
		if slices.Contains(subjects, r.Subject) {
			out = append(out, r)
		}
	}
	slices.SortStableFunc(out, func(a, b db.CityMetric) int {
		return cmp.Or(cmp.Compare(a.Subject, b.Subject), cmp.Compare(a.Class, b.Class),
			cmp.Compare(a.Metric, b.Metric), cmp.Compare(a.RunID, b.RunID))
	})
	return out, nil
}

// SaveHexMetrics delegates to SaveHexMetricsFunc if set, otherwise appends
// rows to HexMetrics under runID, refusing a key the metric catalog does not
// store per hex as the real store does.
func (m *MockStore) SaveHexMetrics(ctx context.Context, runID int64, rows []db.HexMetric) error {
	if m.SaveHexMetricsFunc != nil {
		return m.SaveHexMetricsFunc(ctx, runID, rows)
	}
	if err := db.CheckHexMetrics(rows); err != nil {
		return err
	}
	for _, r := range rows {
		r.RunID = runID
		m.HexMetrics = append(m.HexMetrics, r)
	}
	return nil
}

// ListHexMetrics delegates to ListHexMetricsFunc if set, otherwise returns
// the HexMetrics rows of metric over subject ordered by hex id, run.
func (m *MockStore) ListHexMetrics(ctx context.Context, metric, subject string) ([]db.HexMetric, error) {
	if m.ListHexMetricsFunc != nil {
		return m.ListHexMetricsFunc(ctx, metric, subject)
	}
	out := []db.HexMetric{}
	for _, r := range m.HexMetrics {
		if r.Metric == metric && r.Subject == subject {
			out = append(out, r)
		}
	}
	slices.SortStableFunc(out, func(a, b db.HexMetric) int {
		return cmp.Or(cmp.Compare(a.HexID, b.HexID), cmp.Compare(a.RunID, b.RunID))
	})
	return out, nil
}

// HasHexMetric delegates to HasHexMetricFunc if set, otherwise answers from
// ListHexMetrics per subject, so fixtures that stub only ListHexMetricsFunc
// agree.
func (m *MockStore) HasHexMetric(ctx context.Context, metric string, subjects []string) (bool, error) {
	if m.HasHexMetricFunc != nil {
		return m.HasHexMetricFunc(ctx, metric, subjects)
	}
	for _, s := range subjects {
		rows, err := m.ListHexMetrics(ctx, metric, s)
		if err != nil {
			return false, err
		}
		if len(rows) > 0 {
			return true, nil
		}
	}
	return false, nil
}

// CreateRun delegates to CreateRunFunc, else hands out run ids 1, 2, 3, ...
// in call order (so a test can tell runs apart without mocking).
// The default also records inputs in RunInputs.
func (m *MockStore) CreateRun(ctx context.Context, producer, hash string, inputs map[string]string) (*db.Run, error) {
	if m.CreateRunFunc != nil {
		return m.CreateRunFunc(ctx, producer, hash, inputs)
	}
	m.lastRunID++
	if m.RunInputs == nil {
		m.RunInputs = map[int64]map[string]string{}
	}
	m.RunInputs[m.lastRunID] = maps.Clone(inputs)
	if m.runProducers == nil {
		m.runProducers = map[int64]string{}
	}
	m.runProducers[m.lastRunID] = producer
	return &db.Run{ID: m.lastRunID, Producer: producer, ConfigHash: hash}, nil
}

// SaveRunFacilities delegates to SaveRunFacilitiesFunc if set, otherwise
// appends facilities to Facilities under the run's producer.
func (m *MockStore) SaveRunFacilities(ctx context.Context, runID int64, facilities []db.RunFacility) error {
	if m.SaveRunFacilitiesFunc != nil {
		return m.SaveRunFacilitiesFunc(ctx, runID, facilities)
	}
	if m.Facilities == nil {
		m.Facilities = map[string][]db.RunFacility{}
	}
	producer := m.runProducers[runID]
	m.Facilities[producer] = append(m.Facilities[producer], facilities...)
	return nil
}

// ListRunFacilities delegates to ListRunFacilitiesFunc if set, otherwise
// returns Facilities[producer] ordered by id.
func (m *MockStore) ListRunFacilities(ctx context.Context, producer string) ([]db.RunFacility, error) {
	if m.ListRunFacilitiesFunc != nil {
		return m.ListRunFacilitiesFunc(ctx, producer)
	}
	out := slices.Clone(m.Facilities[producer])
	if out == nil {
		out = []db.RunFacility{}
	}
	slices.SortStableFunc(out, func(a, b db.RunFacility) int { return cmp.Compare(a.ID, b.ID) })
	return out, nil
}

func (m *MockStore) DiscardRun(ctx context.Context, id int64) error {
	if m.DiscardRunFunc != nil {
		return m.DiscardRunFunc(ctx, id)
	}
	return nil
}

// CommitLatest delegates to CommitLatestFunc, else accepts the commit.
func (m *MockStore) CommitLatest(ctx context.Context, hash string, _ int, parts map[string]int64) error {
	if m.CommitLatestFunc != nil {
		return m.CommitLatestFunc(ctx, hash, parts)
	}
	return nil
}

// LatestCompute delegates to LatestComputeFunc, else returns (nil, nil):
// nothing computed.
func (m *MockStore) LatestCompute(ctx context.Context, hash string) ([]db.LatestPart, error) {
	if m.LatestComputeFunc != nil {
		return m.LatestComputeFunc(ctx, hash)
	}
	return nil, nil
}

func (m *MockStore) ListSnapshots(ctx context.Context) ([]db.SnapshotCity, error) {
	if m.ListSnapshotsFunc != nil {
		return m.ListSnapshotsFunc(ctx)
	}
	return []db.SnapshotCity{}, nil
}

// ResolveSnapshot delegates to ResolveSnapshotFunc, else resolves every
// name to snapshot 1.
func (m *MockStore) ResolveSnapshot(ctx context.Context, name string) (int64, error) {
	if m.ResolveSnapshotFunc != nil {
		return m.ResolveSnapshotFunc(ctx, name)
	}
	return 1, nil
}

func (m *MockStore) GCScan(ctx context.Context, keepSources []string, _ int) (*db.GCReport, error) {
	if m.GCScanFunc != nil {
		return m.GCScanFunc(ctx, keepSources)
	}
	return &db.GCReport{}, nil
}

func (m *MockStore) GCSweep(ctx context.Context, keepSources []string, _ int) (*db.GCReport, error) {
	if m.GCSweepFunc != nil {
		return m.GCSweepFunc(ctx, keepSources)
	}
	return &db.GCReport{}, nil
}

func (m *MockStore) InputStates(ctx context.Context) ([]db.InputState, error) {
	if m.InputStatesFunc != nil {
		return m.InputStatesFunc(ctx)
	}
	return []db.InputState{}, nil
}

// InputDigests delegates to InputDigestsFunc if set, otherwise reports each
// input from Digests ("" when absent).
func (m *MockStore) InputDigests(ctx context.Context, inputs ...string) (map[string]string, error) {
	if m.InputDigestsFunc != nil {
		return m.InputDigestsFunc(ctx, inputs...)
	}
	out := make(map[string]string, len(inputs))
	for _, in := range inputs {
		out[in] = m.Digests[in]
	}
	return out, nil
}

// ViewSnapshot delegates to ViewSnapshotFunc if set, otherwise returns
// (nil, nil): no snapshot, so callers fall back to the config.
func (m *MockStore) ViewSnapshot(ctx context.Context) (*db.SnapshotCity, error) {
	if m.ViewSnapshotFunc != nil {
		return m.ViewSnapshotFunc(ctx)
	}
	return nil, nil //nolint:nilnil // nil snapshot = none, like the real store
}

// WithSnapshot returns the mock unchanged by default — tests that need to
// observe pinning should set WithSnapshotFunc, typically to return a child
// MockStore whose ListX funcs know the pinned id.
func (m *MockStore) WithSnapshot(snapshotID int64) db.Store {
	if m.WithSnapshotFunc != nil {
		return m.WithSnapshotFunc(snapshotID)
	}
	return m
}

// WithLatestCompute returns the mock unchanged by default. NOTE: this
// silently bypasses the latest-compute filtering that
// sqliteStore.WithLatestCompute applies, so a test that mocks the store
// will exercise "no filtering" even when its production caller pins via
// WithLatestCompute. Tests that need to observe the pin should set
// WithLatestComputeFunc; tests that need to exercise the actual filter
// should use a real sqlite store.
func (m *MockStore) WithLatestCompute(configHash string) db.Store {
	if m.WithLatestComputeFunc != nil {
		return m.WithLatestComputeFunc(configHash)
	}
	return m
}

// WithRuns returns the mock unchanged unless WithRunsFunc is set.
func (m *MockStore) WithRuns(runIDs []int64) db.Store {
	if m.WithRunsFunc != nil {
		return m.WithRunsFunc(runIDs)
	}
	return m
}

func (m *MockStore) SaveBoundary(ctx context.Context, geometryJSON, source string) error {
	if m.SaveBoundaryFunc != nil {
		return m.SaveBoundaryFunc(ctx, geometryJSON, source)
	}
	return nil
}

func (m *MockStore) GetBoundary(ctx context.Context) (string, error) {
	if m.GetBoundaryFunc != nil {
		return m.GetBoundaryFunc(ctx)
	}
	return "", nil
}

func (m *MockStore) Stats(ctx context.Context, rt resource.Type) (*db.StatusInfo, error) {
	if m.StatsFunc != nil {
		return m.StatsFunc(ctx, rt)
	}
	return &db.StatusInfo{ResourceType: rt}, nil
}

func (m *MockStore) Close() error {
	if m.CloseFunc != nil {
		return m.CloseFunc()
	}
	return nil
}

// MockRootStore mocks the RootStore for testing multi-city commands.
// It implements db.RootStorer.
type MockRootStore struct {
	EnsureCityFunc     func(context.Context, string, string, string) (int64, error)
	ListCitiesFunc     func(context.Context) ([]db.City, error)
	ForCityFunc        func(id int64) db.Store
	CreateSnapshotFunc func(context.Context, string, int, []db.SnapshotRequest) (*db.Snapshot, error)
	ListSnapshotsFunc  func(context.Context) ([]db.Snapshot, error)
	SnapshotFunc       func(context.Context, string) (*db.Snapshot, error)
	DeleteSnapshotFunc func(context.Context, string) (bool, error)
	CloseFunc          func() error
}

func (m *MockRootStore) EnsureCity(ctx context.Context, slug, name, configID string) (int64, error) {
	if m.EnsureCityFunc != nil {
		return m.EnsureCityFunc(ctx, slug, name, configID)
	}
	return 1, nil
}

func (m *MockRootStore) ListCities(ctx context.Context) ([]db.City, error) {
	if m.ListCitiesFunc != nil {
		return m.ListCitiesFunc(ctx)
	}
	return []db.City{}, nil
}

func (m *MockRootStore) ForCity(id int64) db.Store {
	if m.ForCityFunc != nil {
		return m.ForCityFunc(id)
	}
	return &MockStore{}
}

// CreateSnapshot delegates to CreateSnapshotFunc. Otherwise, for each city,
// it runs the check over ForCity's LatestCompute and InputStates and refuses
// an empty latest compute with db.ErrNothingComputed, like the real store;
// any refusal returns a *db.SnapshotRefusedError, else snapshot 1 named name
// holding every city's parts.
func (m *MockRootStore) CreateSnapshot(ctx context.Context, name string, epoch int, cities []db.SnapshotRequest) (*db.Snapshot, error) {
	if m.CreateSnapshotFunc != nil {
		return m.CreateSnapshotFunc(ctx, name, epoch, cities)
	}
	if err := db.CheckSnapshotName(name); err != nil {
		return nil, err
	}
	snap := &db.Snapshot{ID: 1, Name: name, ComputeEpoch: epoch}
	refused := &db.SnapshotRefusedError{Name: name}
	for _, req := range cities {
		store := m.ForCity(req.CityID)
		parts, err := store.LatestCompute(ctx, req.ConfigHash)
		if err != nil {
			return nil, err
		}
		states, err := store.InputStates(ctx)
		if err != nil {
			return nil, err
		}
		if len(parts) == 0 {
			refused.Refusals = append(refused.Refusals, db.CityRefusal{CityID: req.CityID, Err: db.ErrNothingComputed})
			continue
		}
		if req.Check != nil {
			if err := req.Check(parts, states); err != nil {
				refused.Refusals = append(refused.Refusals, db.CityRefusal{CityID: req.CityID, Err: err})
				continue
			}
		}
		city := db.SnapshotCity{ID: 1, Name: name, ComputeEpoch: epoch, CityID: req.CityID, ConfigHash: req.ConfigHash, HexEdgeM: req.HexEdgeM}
		for _, p := range parts {
			city.Parts = append(city.Parts, db.SnapshotPart{Part: p.Part, RunID: p.RunID, ComputedAt: p.ComputedAt})
		}
		snap.Cities = append(snap.Cities, city)
	}
	if len(refused.Refusals) > 0 {
		return nil, refused
	}
	snap.CityCount = len(snap.Cities)
	return snap, nil
}

func (m *MockRootStore) ListSnapshots(ctx context.Context) ([]db.Snapshot, error) {
	if m.ListSnapshotsFunc != nil {
		return m.ListSnapshotsFunc(ctx)
	}
	return []db.Snapshot{}, nil
}

// Snapshot delegates to SnapshotFunc, else reports db.ErrSnapshotNotFound.
func (m *MockRootStore) Snapshot(ctx context.Context, name string) (*db.Snapshot, error) {
	if m.SnapshotFunc != nil {
		return m.SnapshotFunc(ctx, name)
	}
	return nil, db.ErrSnapshotNotFound
}

func (m *MockRootStore) DeleteSnapshot(ctx context.Context, name string) (bool, error) {
	if m.DeleteSnapshotFunc != nil {
		return m.DeleteSnapshotFunc(ctx, name)
	}
	return false, nil
}

func (m *MockRootStore) Close() error {
	if m.CloseFunc != nil {
		return m.CloseFunc()
	}
	return nil
}

// published numbers the snapshots Publish creates.
var published atomic.Int64

// Publish commits parts into city cityID's latest compute under hash, then
// freezes it, unchecked and tiled at hexEdgeM, as a new snapshot covering
// only that city (named "publish-<n>"): what `pvmt <res> compute` followed by
// `pvmt snapshots create` leaves behind. It returns the city's entry.
func Publish(ctx context.Context, root db.RootStorer, cityID int64, hash string, hexEdgeM float64, parts map[string]int64) (*db.SnapshotCity, error) {
	if err := root.ForCity(cityID).CommitLatest(ctx, hash, config.ComputeEpoch, parts); err != nil {
		return nil, err
	}
	name := fmt.Sprintf("publish-%d", published.Add(1))
	snap, err := root.CreateSnapshot(ctx, name, config.ComputeEpoch,
		[]db.SnapshotRequest{{CityID: cityID, ConfigHash: hash, HexEdgeM: hexEdgeM}})
	if err != nil {
		return nil, err
	}
	return &snap.Cities[0], nil
}

// RecordIngestAttempts appends to m.Attempts.
func (m *MockStore) RecordIngestAttempts(_ context.Context, attempts []db.IngestAttempt) error {
	m.Attempts = append(m.Attempts, attempts...)
	return nil
}

// IngestHistory delegates to IngestHistoryFunc, or reports none.
func (m *MockStore) IngestHistory(ctx context.Context) ([]db.IngestHistory, error) {
	if m.IngestHistoryFunc != nil {
		return m.IngestHistoryFunc(ctx)
	}
	return []db.IngestHistory{}, nil
}

// DeleteSnapshotID deletes the snapshot with id (as Publish returns it)
// through root.DeleteSnapshot; (false, nil) when there is none.
func DeleteSnapshotID(ctx context.Context, root db.RootStorer, id int64) (bool, error) {
	snaps, err := root.ListSnapshots(ctx)
	if err != nil {
		return false, err
	}
	for _, s := range snaps {
		if s.ID == id {
			return root.DeleteSnapshot(ctx, s.Name)
		}
	}
	return false, nil
}
