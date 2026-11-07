package dbtest

import (
	"context"
	"database/sql"
	"errors"
	"maps"
	"slices"

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
	UpsertFeaturesFunc          func(context.Context, resource.Type, []db.Feature, []string) error
	ListFeaturesFunc            func(context.Context, resource.Type) ([]db.Feature, error)
	SaveComputeResultFunc       func(context.Context, db.ComputeResult) error
	LatestComputeResultFunc     func(context.Context, resource.Type) (*db.ComputeResult, error)
	LatestComputeResultsFunc    func(context.Context, []resource.Type) (map[resource.Type]*db.ComputeResult, error)
	ListCohortStatsForTypesFunc func(context.Context, []resource.Type) (map[resource.Type][]db.CohortStat, error)
	SaveHexStatsFunc            func(context.Context, []db.HexStat) error
	ListHexStatsFunc            func(context.Context, resource.Type) ([]db.HexStat, error)
	HasHexStatsFunc             func(context.Context, []resource.Type) (bool, error)
	SaveHexMetricsFunc          func(context.Context, string, int64, []db.HexMetric) error
	SaveHexMetricSetFunc        func(context.Context, int64, map[string][]db.HexMetric) error
	ListHexMetricsFunc          func(context.Context, string) ([]db.HexMetric, error)
	CreateRunFunc               func(context.Context, string, string) (*db.Run, error)
	DiscardRunFunc              func(context.Context, int64) error
	CommitSnapshotFunc          func(context.Context, string, map[string]int64) (*db.Snapshot, error)
	ListSnapshotsFunc           func(context.Context) ([]db.Snapshot, error)
	ResolveSnapshotFunc         func(context.Context, int64) error
	WithSnapshotFunc            func(int64) db.Store
	WithConfigHashFunc          func(string) db.Store
	DeleteSnapshotFunc          func(context.Context, int64) (bool, error)
	GCScanFunc                  func(context.Context, []string) (*db.GCReport, error)
	GCSweepFunc                 func(context.Context, []string) (*db.GCReport, error)
	SaveCohortStatsFunc         func(context.Context, []db.CohortStat) error
	ListCohortStatsFunc         func(context.Context, resource.Type) ([]db.CohortStat, error)
	SaveBoundaryFunc            func(context.Context, string, string) error
	GetBoundaryFunc             func(context.Context) (string, error)
	StatsFunc                   func(context.Context, resource.Type) (*db.StatusInfo, error)
	CloseFunc                   func() error

	lastRunID int64 // CreateRun's default id sequence
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

func (m *MockStore) SaveComputeResult(ctx context.Context, r db.ComputeResult) error {
	if m.SaveComputeResultFunc != nil {
		return m.SaveComputeResultFunc(ctx, r)
	}
	return nil
}

func (m *MockStore) LatestComputeResult(ctx context.Context, rt resource.Type) (*db.ComputeResult, error) {
	if m.LatestComputeResultFunc != nil {
		return m.LatestComputeResultFunc(ctx, rt)
	}
	return nil, sql.ErrNoRows
}

// LatestComputeResults delegates to LatestComputeResultsFunc if set,
// otherwise routes each requested type through LatestComputeResultFunc
// so existing tests that stub the singular method keep working without
// having to set both fields. sql.ErrNoRows from the singular path is
// treated as "no row for that type" and simply omitted from the map;
// any other error propagates so error-path tests behave the same as
// they did before batching.
func (m *MockStore) LatestComputeResults(ctx context.Context, types []resource.Type) (map[resource.Type]*db.ComputeResult, error) {
	if m.LatestComputeResultsFunc != nil {
		return m.LatestComputeResultsFunc(ctx, types)
	}
	out := make(map[resource.Type]*db.ComputeResult, len(types))
	for _, t := range types {
		r, err := m.LatestComputeResult(ctx, t)
		if errors.Is(err, sql.ErrNoRows) {
			continue
		}
		if err != nil {
			return nil, err
		}
		if r != nil {
			out[t] = r
		}
	}
	return out, nil
}

func (m *MockStore) SaveHexStats(ctx context.Context, stats []db.HexStat) error {
	if m.SaveHexStatsFunc != nil {
		return m.SaveHexStatsFunc(ctx, stats)
	}
	return nil
}

// HasHexStats delegates to HasHexStatsFunc if set, otherwise answers from
// ListHexStats per type, so fixtures that only stub ListHexStatsFunc agree.
func (m *MockStore) HasHexStats(ctx context.Context, types []resource.Type) (bool, error) {
	if m.HasHexStatsFunc != nil {
		return m.HasHexStatsFunc(ctx, types)
	}
	for _, t := range types {
		rows, err := m.ListHexStats(ctx, t)
		if err != nil {
			return false, err
		}
		if len(rows) > 0 {
			return true, nil
		}
	}
	return false, nil
}

func (m *MockStore) ListHexStats(ctx context.Context, rt resource.Type) ([]db.HexStat, error) {
	if m.ListHexStatsFunc != nil {
		return m.ListHexStatsFunc(ctx, rt)
	}
	return []db.HexStat{}, nil
}

// CreateRun delegates to CreateRunFunc, else hands out run ids 1, 2, 3, ...
// in call order (so a test can tell runs apart without mocking).
func (m *MockStore) CreateRun(ctx context.Context, producer, hash string) (*db.Run, error) {
	if m.CreateRunFunc != nil {
		return m.CreateRunFunc(ctx, producer, hash)
	}
	m.lastRunID++
	return &db.Run{ID: m.lastRunID, Producer: producer, ConfigHash: hash}, nil
}

func (m *MockStore) DiscardRun(ctx context.Context, id int64) error {
	if m.DiscardRunFunc != nil {
		return m.DiscardRunFunc(ctx, id)
	}
	return nil
}

// CommitSnapshot delegates to CommitSnapshotFunc, else returns snapshot 1
// whose manifest is exactly parts (zero ids dropped). It does not carry a
// previous snapshot forward; tests of carry-forward use a real store.
func (m *MockStore) CommitSnapshot(ctx context.Context, hash string, parts map[string]int64) (*db.Snapshot, error) {
	if m.CommitSnapshotFunc != nil {
		return m.CommitSnapshotFunc(ctx, hash, parts)
	}
	snap := &db.Snapshot{ID: 1, ConfigHash: hash}
	for _, part := range slices.Sorted(maps.Keys(parts)) {
		if parts[part] != 0 {
			snap.Parts = append(snap.Parts, db.SnapshotPart{Part: part, RunID: parts[part]})
		}
	}
	return snap, nil
}

func (m *MockStore) ListSnapshots(ctx context.Context) ([]db.Snapshot, error) {
	if m.ListSnapshotsFunc != nil {
		return m.ListSnapshotsFunc(ctx)
	}
	return []db.Snapshot{}, nil
}

func (m *MockStore) ResolveSnapshot(ctx context.Context, id int64) error {
	if m.ResolveSnapshotFunc != nil {
		return m.ResolveSnapshotFunc(ctx, id)
	}
	return nil
}

func (m *MockStore) DeleteSnapshot(ctx context.Context, id int64) (bool, error) {
	if m.DeleteSnapshotFunc != nil {
		return m.DeleteSnapshotFunc(ctx, id)
	}
	return false, nil
}

func (m *MockStore) GCScan(ctx context.Context, keepSources []string) (*db.GCReport, error) {
	if m.GCScanFunc != nil {
		return m.GCScanFunc(ctx, keepSources)
	}
	return &db.GCReport{}, nil
}

func (m *MockStore) GCSweep(ctx context.Context, keepSources []string) (*db.GCReport, error) {
	if m.GCSweepFunc != nil {
		return m.GCSweepFunc(ctx, keepSources)
	}
	return &db.GCReport{}, nil
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

// WithConfigHash returns the mock unchanged by default. NOTE: this
// silently bypasses the production config_hash filtering that
// sqliteStore.WithConfigHash applies, so a test that mocks the store
// will exercise "no filtering" even when its production caller pins
// via WithConfigHash. Tests that need to observe the pin should set
// WithConfigHashFunc; tests that need to exercise the actual filter
// should use a real sqlite store via openTestStore.
func (m *MockStore) WithConfigHash(configHash string) db.Store {
	if m.WithConfigHashFunc != nil {
		return m.WithConfigHashFunc(configHash)
	}
	return m
}

func (m *MockStore) SaveCohortStats(ctx context.Context, stats []db.CohortStat) error {
	if m.SaveCohortStatsFunc != nil {
		return m.SaveCohortStatsFunc(ctx, stats)
	}
	return nil
}

func (m *MockStore) ListCohortStats(ctx context.Context, rt resource.Type) ([]db.CohortStat, error) {
	if m.ListCohortStatsFunc != nil {
		return m.ListCohortStatsFunc(ctx, rt)
	}
	return []db.CohortStat{}, nil
}

// ListCohortStatsForTypes delegates to ListCohortStatsForTypesFunc if
// set, otherwise routes each requested type through ListCohortStatsFunc
// so existing tests that stub the singular method keep working.
func (m *MockStore) ListCohortStatsForTypes(ctx context.Context, types []resource.Type) (map[resource.Type][]db.CohortStat, error) {
	if m.ListCohortStatsForTypesFunc != nil {
		return m.ListCohortStatsForTypesFunc(ctx, types)
	}
	out := make(map[resource.Type][]db.CohortStat, len(types))
	for _, t := range types {
		stats, err := m.ListCohortStats(ctx, t)
		if err != nil {
			return nil, err
		}
		if len(stats) > 0 {
			out[t] = stats
		}
	}
	return out, nil
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
	EnsureCityFunc func(context.Context, string, string, string) (int64, error)
	ListCitiesFunc func(context.Context) ([]db.City, error)
	ForCityFunc    func(id int64) db.Store
	CloseFunc      func() error
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

func (m *MockRootStore) Close() error {
	if m.CloseFunc != nil {
		return m.CloseFunc()
	}
	return nil
}

func (m *MockStore) SaveHexMetrics(ctx context.Context, metric string, runID int64, rows []db.HexMetric) error {
	if m.SaveHexMetricsFunc != nil {
		return m.SaveHexMetricsFunc(ctx, metric, runID, rows)
	}
	return nil
}

// SaveHexMetricSet delegates to SaveHexMetricSetFunc if set, otherwise
// replays the set through SaveHexMetrics key by key (sorted), so tests that
// only capture SaveHexMetricsFunc still observe every metric.
func (m *MockStore) SaveHexMetricSet(ctx context.Context, runID int64, set map[string][]db.HexMetric) error {
	if m.SaveHexMetricSetFunc != nil {
		return m.SaveHexMetricSetFunc(ctx, runID, set)
	}
	for _, metric := range slices.Sorted(maps.Keys(set)) {
		if err := m.SaveHexMetrics(ctx, metric, runID, set[metric]); err != nil {
			return err
		}
	}
	return nil
}

func (m *MockStore) ListHexMetrics(ctx context.Context, metric string) ([]db.HexMetric, error) {
	if m.ListHexMetricsFunc != nil {
		return m.ListHexMetricsFunc(ctx, metric)
	}
	return nil, nil
}
