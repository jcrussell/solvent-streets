package dbtest

import (
	"cmp"
	"context"
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
	UpsertFeaturesFunc  func(context.Context, resource.Type, []db.Feature, []string) error
	ListFeaturesFunc    func(context.Context, resource.Type) ([]db.Feature, error)
	SaveCityMetricsFunc func(context.Context, int64, []db.CityMetric) error
	ListCityMetricsFunc func(context.Context, []string) ([]db.CityMetric, error)
	SaveHexMetricsFunc  func(context.Context, int64, []db.HexMetric) error
	ListHexMetricsFunc  func(context.Context, string, string) ([]db.HexMetric, error)
	HasHexMetricFunc    func(context.Context, string, []string) (bool, error)
	CreateRunFunc       func(context.Context, string, string, map[string]string) (*db.Run, error)
	DiscardRunFunc      func(context.Context, int64) error
	CommitSnapshotFunc  func(context.Context, string, float64, map[string]int64) (*db.Snapshot, error)
	ListSnapshotsFunc   func(context.Context) ([]db.Snapshot, error)
	ResolveSnapshotFunc func(context.Context, int64) error
	WithSnapshotFunc    func(int64) db.Store
	WithConfigHashFunc  func(string) db.Store
	DeleteSnapshotFunc  func(context.Context, int64) (bool, error)
	GCScanFunc          func(context.Context, []string) (*db.GCReport, error)
	GCSweepFunc         func(context.Context, []string) (*db.GCReport, error)
	InputStatesFunc     func(context.Context) ([]db.InputState, error)
	InputDigestsFunc    func(context.Context, ...string) (map[string]string, error)
	SaveBoundaryFunc    func(context.Context, string, string) error
	GetBoundaryFunc     func(context.Context) (string, error)
	StatsFunc           func(context.Context, resource.Type) (*db.StatusInfo, error)
	CloseFunc           func() error

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

// SaveCityMetrics delegates to SaveCityMetricsFunc if set, otherwise
// appends rows to CityMetrics under runID.
func (m *MockStore) SaveCityMetrics(ctx context.Context, runID int64, rows []db.CityMetric) error {
	if m.SaveCityMetricsFunc != nil {
		return m.SaveCityMetricsFunc(ctx, runID, rows)
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
// rows to HexMetrics under runID.
func (m *MockStore) SaveHexMetrics(ctx context.Context, runID int64, rows []db.HexMetric) error {
	if m.SaveHexMetricsFunc != nil {
		return m.SaveHexMetricsFunc(ctx, runID, rows)
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
func (m *MockStore) CommitSnapshot(ctx context.Context, hash string, hexEdgeM float64, parts map[string]int64) (*db.Snapshot, error) {
	if m.CommitSnapshotFunc != nil {
		return m.CommitSnapshotFunc(ctx, hash, hexEdgeM, parts)
	}
	snap := &db.Snapshot{ID: 1, ConfigHash: hash, HexEdgeM: hexEdgeM}
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
