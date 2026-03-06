package server

import (
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	"pvmt/internal/db"
)

type mockStore struct {
	computeResults map[string]*db.ComputeResult
}

func (m *mockStore) UpsertFeatures(string, []db.Feature) error              { return nil }
func (m *mockStore) ListFeatures(string) ([]db.Feature, error)              { return nil, nil }
func (m *mockStore) SaveComputeResult(db.ComputeResult) error               { return nil }
func (m *mockStore) Stats(string) (*db.StatusInfo, error)                   { return &db.StatusInfo{}, nil }
func (m *mockStore) ResourceTypes() ([]string, error)                       { return nil, nil }
func (m *mockStore) Close() error                                           { return nil }

func (m *mockStore) LatestComputeResult(rt string) (*db.ComputeResult, error) {
	if r, ok := m.computeResults[rt]; ok {
		return r, nil
	}
	return nil, fmt.Errorf("not found")
}

func TestHandleStats(t *testing.T) {
	store := &mockStore{
		computeResults: map[string]*db.ComputeResult{
			"pavements": {
				ResourceType:   "pavements",
				TotalAreaSqFt:  500000,
				TotalAreaAcres: 11.48,
				FeatureCount:   100,
				GeometryJSON:   `{"type":"Polygon","coordinates":[]}`,
				ComputedAt:     time.Now(),
			},
		},
	}

	srv := New(store, 0)
	mux := http.NewServeMux()
	mux.HandleFunc("GET /api/stats", srv.handleStats)

	req := httptest.NewRequest("GET", "/api/stats?type=pavements", nil)
	w := httptest.NewRecorder()
	mux.ServeHTTP(w, req)

	if w.Code != http.StatusOK {
		t.Errorf("expected 200, got %d", w.Code)
	}

	var results []statsResponse
	if err := json.NewDecoder(w.Body).Decode(&results); err != nil {
		t.Fatal(err)
	}
	if len(results) != 1 {
		t.Fatalf("expected 1 result, got %d", len(results))
	}
	if results[0].TotalAreaSqFt != 500000 {
		t.Errorf("expected 500000 sqft, got %f", results[0].TotalAreaSqFt)
	}
}
