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
	"pvmt/internal/db/dbtest"
	"pvmt/internal/export"
)

func TestHandleDataMetaJSON(t *testing.T) {
	testBoundary := `{"type":"Polygon","coordinates":[[[-121.84,37.64],[-121.68,37.64],[-121.68,37.72],[-121.84,37.72],[-121.84,37.64]]]}`
	store := &dbtest.MockStore{
		GetBoundaryFunc: func() (string, error) { return testBoundary, nil },
		LatestComputeResultFunc: func(rt string) (*db.ComputeResult, error) {
			if rt == "roads" {
				return &db.ComputeResult{
					ResourceType: "roads",
					TotalAreaSqM: 46452,
					FeatureCount: 100,
					GeometryJSON: `{"type":"Polygon","coordinates":[]}`,
					ComputedAt:   time.Now(),
				}, nil
			}
			return nil, fmt.Errorf("not found")
		},
	}

	cfg := &config.Config{
		Cities: []config.CityConfig{{
			Name: "Test City",
		}},
	}
	entry := export.CityEntry{
		Config: cfg,
		City:   cfg.Cities[0],
		Store:  store,
		Slug:   cfg.Cities[0].Slug(),
	}
	srv := New([]export.CityEntry{entry}, 0)
	mux := http.NewServeMux()
	mux.HandleFunc("GET /data/{file}", srv.handleDataFile(entry))

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
	if meta.Stats[0].TotalAreaSqM != 46452 {
		t.Errorf("expected 46452 sqm, got %f", meta.Stats[0].TotalAreaSqM)
	}
	if meta.ProjectName != "Test City" {
		t.Errorf("expected project name 'Test City', got %q", meta.ProjectName)
	}
}
