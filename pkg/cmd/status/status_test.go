package status

import (
	"strings"
	"testing"
	"time"

	"pvmt/internal/db"
	"pvmt/internal/resource"
	"pvmt/pkg/cmdutil"
	"pvmt/pkg/iostreams"
)

type mockStore struct {
	stats map[string]*db.StatusInfo
}

func (m *mockStore) UpsertFeatures(string, []db.Feature) error              { return nil }
func (m *mockStore) ListFeatures(string) ([]db.Feature, error)              { return nil, nil }
func (m *mockStore) SaveComputeResult(db.ComputeResult) error               { return nil }
func (m *mockStore) LatestComputeResult(string) (*db.ComputeResult, error)  { return nil, nil }
func (m *mockStore) SaveHexStats([]db.HexStat) error                       { return nil }
func (m *mockStore) ListHexStats(string) ([]db.HexStat, error)             { return nil, nil }
func (m *mockStore) CreateSnapshot(string) (*db.Snapshot, error)           { return &db.Snapshot{ID: 1}, nil }
func (m *mockStore) ListSnapshots() ([]db.Snapshot, error)                 { return nil, nil }
func (m *mockStore) SaveForecastResults([]db.ForecastResult) error         { return nil }
func (m *mockStore) ListForecastResults(string) ([]db.ForecastResult, error) { return nil, nil }
func (m *mockStore) ResourceTypes() ([]string, error)                       { return nil, nil }
func (m *mockStore) Close() error                                           { return nil }

func (m *mockStore) Stats(rt string) (*db.StatusInfo, error) {
	if info, ok := m.stats[rt]; ok {
		return info, nil
	}
	return &db.StatusInfo{ResourceType: rt}, nil
}

func TestNewCmdStatus_RunFInjection(t *testing.T) {
	ios, _, _ := iostreams.Test()
	f := &cmdutil.Factory{IOStreams: ios}
	rt := &resource.Pavement{}

	called := false
	cmd := NewCmdStatus(f, rt, func(opts *Options) error {
		called = true
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

func TestRunStatus_SingleResource(t *testing.T) {
	now := time.Now()
	store := &mockStore{
		stats: map[string]*db.StatusInfo{
			"pavements": {
				ResourceType: "pavements",
				FeatureCount: 42,
				LastIngestAt: &now,
			},
		},
	}
	ios, stdout, _ := iostreams.Test()
	f := &cmdutil.Factory{
		IOStreams: ios,
		DB: func() (db.Store, error) {
			return store, nil
		},
	}
	rt := &resource.Pavement{}

	cmd := NewCmdStatus(f, rt, nil)
	cmd.SetArgs([]string{})
	if err := cmd.Execute(); err != nil {
		t.Fatal(err)
	}
	output := stdout.String()
	// Non-TTY output uses tab-separated format
	if !strings.Contains(output, "pavements") {
		t.Errorf("expected pavements in output, got: %s", output)
	}
	if !strings.Contains(output, "42") {
		t.Errorf("expected feature count 42 in output, got: %s", output)
	}
}

func TestRunStatus_AllResources(t *testing.T) {
	store := &mockStore{
		stats: map[string]*db.StatusInfo{
			"pavements": {ResourceType: "pavements", FeatureCount: 10},
			"parking":   {ResourceType: "parking", FeatureCount: 5},
		},
	}
	ios, stdout, _ := iostreams.Test()
	f := &cmdutil.Factory{
		IOStreams: ios,
		DB: func() (db.Store, error) {
			return store, nil
		},
	}

	// nil ResourceType means show all
	cmd := NewCmdStatus(f, nil, nil)
	cmd.SetArgs([]string{})
	if err := cmd.Execute(); err != nil {
		t.Fatal(err)
	}
	output := stdout.String()
	if !strings.Contains(output, "pavements") || !strings.Contains(output, "parking") {
		t.Errorf("expected both resource types in output, got: %s", output)
	}
}

func TestRunStatus_NonTTY_TabSeparated(t *testing.T) {
	store := &mockStore{
		stats: map[string]*db.StatusInfo{
			"pavements": {ResourceType: "pavements", FeatureCount: 7},
		},
	}
	ios, stdout, _ := iostreams.Test()
	// Test() returns isTTY=false by default
	f := &cmdutil.Factory{
		IOStreams: ios,
		DB: func() (db.Store, error) {
			return store, nil
		},
	}
	rt := &resource.Pavement{}

	cmd := NewCmdStatus(f, rt, nil)
	cmd.SetArgs([]string{})
	if err := cmd.Execute(); err != nil {
		t.Fatal(err)
	}
	output := stdout.String()
	if !strings.Contains(output, "\t") {
		t.Errorf("expected tab-separated output for non-TTY, got: %s", output)
	}
	if !strings.Contains(output, "resource_type\tpavements") {
		t.Errorf("expected 'resource_type\\tpavements' in output, got: %s", output)
	}
}
