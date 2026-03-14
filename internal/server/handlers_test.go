package server

import (
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	"pvmt/internal/config"
	"pvmt/internal/db"
	"pvmt/internal/export"
)

type mockStore struct {
	computeResults map[string]*db.ComputeResult
}

func (m *mockStore) UpsertFeatures(string, []db.Feature) error               { return nil }
func (m *mockStore) ListFeatures(string) ([]db.Feature, error)               { return nil, nil }
func (m *mockStore) SaveComputeResult(db.ComputeResult) error                { return nil }
func (m *mockStore) SaveHexStats([]db.HexStat) error                         { return nil }
func (m *mockStore) ListHexStats(string) ([]db.HexStat, error)               { return nil, nil }
func (m *mockStore) CreateSnapshot(string) (*db.Snapshot, error)             { return &db.Snapshot{ID: 1}, nil }
func (m *mockStore) ListSnapshots() ([]db.Snapshot, error)                   { return nil, nil }
func (m *mockStore) SaveForecastResults([]db.ForecastResult) error           { return nil }
func (m *mockStore) ListForecastResults(string) ([]db.ForecastResult, error) { return nil, nil }
func (m *mockStore) Stats(string) (*db.StatusInfo, error)                    { return &db.StatusInfo{}, nil }
func (m *mockStore) ResourceTypes() ([]string, error)                        { return nil, nil }
func (m *mockStore) Close() error                                            { return nil }

func (m *mockStore) LatestComputeResult(rt string) (*db.ComputeResult, error) {
	if r, ok := m.computeResults[rt]; ok {
		return r, nil
	}
	return nil, fmt.Errorf("not found")
}

func TestHandleDataMetaJSON(t *testing.T) {
	store := &mockStore{
		computeResults: map[string]*db.ComputeResult{
			"roads": {
				ResourceType:   "roads",
				TotalAreaSqFt:  500000,
				TotalAreaAcres: 11.48,
				FeatureCount:   100,
				GeometryJSON:   `{"type":"Polygon","coordinates":[]}`,
				ComputedAt:     time.Now(),
			},
		},
	}

	cfg := &config.Config{
		Project: config.ProjectConfig{Name: "Test City"},
		Area:    config.AreaConfig{BBox: [4]float64{37.64, -121.84, 37.72, -121.68}},
	}
	srv := New(store, cfg, 0)
	mux := http.NewServeMux()
	mux.HandleFunc("GET /data/{file}", srv.handleDataFile)

	req := httptest.NewRequest("GET", "/data/meta.json", nil)
	w := httptest.NewRecorder()
	mux.ServeHTTP(w, req)

	if w.Code != http.StatusOK {
		t.Errorf("expected 200, got %d", w.Code)
	}

	var meta export.MetaJSON
	if err := json.NewDecoder(w.Body).Decode(&meta); err != nil {
		t.Fatal(err)
	}
	if len(meta.Stats) != 1 {
		t.Fatalf("expected 1 stat, got %d", len(meta.Stats))
	}
	if meta.Stats[0].TotalAreaSqFt != 500000 {
		t.Errorf("expected 500000 sqft, got %f", meta.Stats[0].TotalAreaSqFt)
	}
	if meta.ProjectName != "Test City" {
		t.Errorf("expected project name 'Test City', got %q", meta.ProjectName)
	}
}
