package dbtest

import "pvmt/internal/db"

var _ db.RootStorer = (*MockRootStore)(nil)

// MockStore is a func-field based mock implementing db.Store.
// Each method delegates to its corresponding func field if set,
// otherwise returns a zero-value success.
type MockStore struct {
	UpsertFeaturesFunc      func(string, []db.Feature) error
	ListFeaturesFunc        func(string) ([]db.Feature, error)
	SaveComputeResultFunc   func(db.ComputeResult) error
	LatestComputeResultFunc func(string) (*db.ComputeResult, error)
	SaveHexStatsFunc        func([]db.HexStat) error
	ListHexStatsFunc        func(string) ([]db.HexStat, error)
	CreateSnapshotFunc      func(string) (*db.Snapshot, error)
	ListSnapshotsFunc       func() ([]db.Snapshot, error)
	SaveForecastResultsFunc func([]db.ForecastResult) error
	ListForecastResultsFunc func(string) ([]db.ForecastResult, error)
	SaveCohortStatsFunc     func([]db.CohortStat) error
	ListCohortStatsFunc     func(string) ([]db.CohortStat, error)
	SaveBoundaryFunc        func(string, string) error
	GetBoundaryFunc         func() (string, error)
	StatsFunc               func(string) (*db.StatusInfo, error)
	ResourceTypesFunc       func() ([]string, error)
	CloseFunc               func() error
}

func (m *MockStore) UpsertFeatures(rt string, f []db.Feature) error {
	if m.UpsertFeaturesFunc != nil {
		return m.UpsertFeaturesFunc(rt, f)
	}
	return nil
}

func (m *MockStore) ListFeatures(rt string) ([]db.Feature, error) {
	if m.ListFeaturesFunc != nil {
		return m.ListFeaturesFunc(rt)
	}
	return nil, nil
}

func (m *MockStore) SaveComputeResult(r db.ComputeResult) error {
	if m.SaveComputeResultFunc != nil {
		return m.SaveComputeResultFunc(r)
	}
	return nil
}

func (m *MockStore) LatestComputeResult(rt string) (*db.ComputeResult, error) {
	if m.LatestComputeResultFunc != nil {
		return m.LatestComputeResultFunc(rt)
	}
	return nil, nil
}

func (m *MockStore) SaveHexStats(stats []db.HexStat) error {
	if m.SaveHexStatsFunc != nil {
		return m.SaveHexStatsFunc(stats)
	}
	return nil
}

func (m *MockStore) ListHexStats(rt string) ([]db.HexStat, error) {
	if m.ListHexStatsFunc != nil {
		return m.ListHexStatsFunc(rt)
	}
	return nil, nil
}

func (m *MockStore) CreateSnapshot(hash string) (*db.Snapshot, error) {
	if m.CreateSnapshotFunc != nil {
		return m.CreateSnapshotFunc(hash)
	}
	return &db.Snapshot{ID: 1}, nil
}

func (m *MockStore) ListSnapshots() ([]db.Snapshot, error) {
	if m.ListSnapshotsFunc != nil {
		return m.ListSnapshotsFunc()
	}
	return nil, nil
}

func (m *MockStore) SaveForecastResults(results []db.ForecastResult) error {
	if m.SaveForecastResultsFunc != nil {
		return m.SaveForecastResultsFunc(results)
	}
	return nil
}

func (m *MockStore) ListForecastResults(rt string) ([]db.ForecastResult, error) {
	if m.ListForecastResultsFunc != nil {
		return m.ListForecastResultsFunc(rt)
	}
	return nil, nil
}

func (m *MockStore) SaveCohortStats(stats []db.CohortStat) error {
	if m.SaveCohortStatsFunc != nil {
		return m.SaveCohortStatsFunc(stats)
	}
	return nil
}

func (m *MockStore) ListCohortStats(rt string) ([]db.CohortStat, error) {
	if m.ListCohortStatsFunc != nil {
		return m.ListCohortStatsFunc(rt)
	}
	return nil, nil
}

func (m *MockStore) SaveBoundary(geometryJSON, source string) error {
	if m.SaveBoundaryFunc != nil {
		return m.SaveBoundaryFunc(geometryJSON, source)
	}
	return nil
}

func (m *MockStore) GetBoundary() (string, error) {
	if m.GetBoundaryFunc != nil {
		return m.GetBoundaryFunc()
	}
	return "", nil
}

func (m *MockStore) Stats(rt string) (*db.StatusInfo, error) {
	if m.StatsFunc != nil {
		return m.StatsFunc(rt)
	}
	return &db.StatusInfo{ResourceType: rt}, nil
}

func (m *MockStore) ResourceTypes() ([]string, error) {
	if m.ResourceTypesFunc != nil {
		return m.ResourceTypesFunc()
	}
	return nil, nil
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
	EnsureCityFunc func(slug, name string) (int64, error)
	ListCitiesFunc func() ([]db.City, error)
	ForCityFunc    func(id int64) db.Store
	CloseFunc      func() error
}

func (m *MockRootStore) EnsureCity(slug, name string) (int64, error) {
	if m.EnsureCityFunc != nil {
		return m.EnsureCityFunc(slug, name)
	}
	return 1, nil
}

func (m *MockRootStore) ListCities() ([]db.City, error) {
	if m.ListCitiesFunc != nil {
		return m.ListCitiesFunc()
	}
	return nil, nil
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
