package compute

import (
	"fmt"
	"strings"
	"testing"

	"pvmt/internal/config"
	"pvmt/internal/db"
	"pvmt/internal/resource"
	"pvmt/pkg/cmdutil"
	"pvmt/pkg/iostreams"
)

type mockStore struct {
	features    []db.Feature
	savedResult *db.ComputeResult
	listErr     error
}

func (m *mockStore) UpsertFeatures(string, []db.Feature) error               { return nil }
func (m *mockStore) ListFeatures(string) ([]db.Feature, error)               { return m.features, m.listErr }
func (m *mockStore) SaveComputeResult(r db.ComputeResult) error              { m.savedResult = &r; return nil }
func (m *mockStore) LatestComputeResult(string) (*db.ComputeResult, error)   { return nil, nil }
func (m *mockStore) SaveHexStats([]db.HexStat) error                         { return nil }
func (m *mockStore) ListHexStats(string) ([]db.HexStat, error)               { return nil, nil }
func (m *mockStore) CreateSnapshot(string) (*db.Snapshot, error)             { return &db.Snapshot{ID: 1}, nil }
func (m *mockStore) ListSnapshots() ([]db.Snapshot, error)                   { return nil, nil }
func (m *mockStore) SaveForecastResults([]db.ForecastResult) error           { return nil }
func (m *mockStore) ListForecastResults(string) ([]db.ForecastResult, error) { return nil, nil }
func (m *mockStore) Stats(string) (*db.StatusInfo, error)                    { return &db.StatusInfo{}, nil }
func (m *mockStore) ResourceTypes() ([]string, error)                        { return nil, nil }
func (m *mockStore) Close() error                                            { return nil }

var testCfg = &config.Config{
	Project: config.ProjectConfig{Name: "Test City"},
	Area:    config.AreaConfig{BBox: [4]float64{37.64, -121.84, 37.72, -121.68}},
}

func TestNewCmdCompute_RunFInjection(t *testing.T) {
	ios, _, _ := iostreams.Test()
	f := &cmdutil.Factory{IOStreams: ios}
	rt := &resource.Pavement{}

	called := false
	cmd := NewCmdCompute(f, rt, func(opts *Options) error {
		called = true
		if opts.ResourceType.Name() != "pavements" {
			t.Errorf("expected pavements, got %s", opts.ResourceType.Name())
		}
		return nil
	})

	cmd.SetArgs([]string{})
	if err := cmd.Execute(); err != nil {
		t.Fatal(err)
	}
	if !called {
		t.Error("runF was not called")
	}
}

func TestRunCompute_NoFeatures(t *testing.T) {
	store := &mockStore{features: nil}
	ios, _, _ := iostreams.Test()
	f := &cmdutil.Factory{
		IOStreams: ios,
		DB: func() (db.Store, error) {
			return store, nil
		},
		Config: func() (*config.Config, error) {
			return testCfg, nil
		},
	}
	rt := &resource.Pavement{}

	cmd := NewCmdCompute(f, rt, nil)
	cmd.SilenceErrors = true
	cmd.SilenceUsage = true
	cmd.SetArgs([]string{})
	err := cmd.Execute()
	if err == nil {
		t.Fatal("expected error for no features")
	}
	if !strings.Contains(err.Error(), "ingest") {
		t.Errorf("error should suggest running ingest, got: %s", err.Error())
	}
}

func TestRunCompute_Success(t *testing.T) {
	store := &mockStore{
		features: []db.Feature{
			{
				ID:           "test1",
				ResourceType: "pavements",
				Name:         "Test Rd",
				Tags:         map[string]string{"highway": "residential"},
				GeometryJSON: `{"type":"LineString","coordinates":[[-121.7700,37.6800],[-121.7690,37.6810]]}`,
			},
		},
	}
	ios, stdout, _ := iostreams.Test()
	f := &cmdutil.Factory{
		IOStreams: ios,
		DB: func() (db.Store, error) {
			return store, nil
		},
		Config: func() (*config.Config, error) {
			return testCfg, nil
		},
	}
	rt := &resource.Pavement{}

	cmd := NewCmdCompute(f, rt, nil)
	cmd.SilenceErrors = true
	cmd.SilenceUsage = true
	cmd.SetArgs([]string{})
	err := cmd.Execute()
	if err != nil {
		t.Fatal(err)
	}
	output := stdout.String()
	if !strings.Contains(output, "sq ft") {
		t.Errorf("expected area output, got: %s", output)
	}
	if store.savedResult == nil {
		t.Error("expected SaveComputeResult to be called")
	}
}

func TestRunCompute_DBError(t *testing.T) {
	ios, _, _ := iostreams.Test()
	f := &cmdutil.Factory{
		IOStreams: ios,
		Config: func() (*config.Config, error) {
			return testCfg, nil
		},
		DB: func() (db.Store, error) {
			return nil, fmt.Errorf("db connection failed")
		},
	}
	rt := &resource.Pavement{}

	cmd := NewCmdCompute(f, rt, nil)
	cmd.SilenceErrors = true
	cmd.SilenceUsage = true
	cmd.SetArgs([]string{})
	err := cmd.Execute()
	if err == nil {
		t.Fatal("expected error for DB failure")
	}
	if !strings.Contains(err.Error(), "database") {
		t.Errorf("expected database error, got: %s", err.Error())
	}
}
