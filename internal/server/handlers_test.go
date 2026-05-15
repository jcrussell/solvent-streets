package server

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"net/http/httptest"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/jcrussell/solvent-streets/internal/config"
	"github.com/jcrussell/solvent-streets/internal/db"
	"github.com/jcrussell/solvent-streets/internal/db/dbtest"
	"github.com/jcrussell/solvent-streets/internal/export"
	"github.com/jcrussell/solvent-streets/internal/resource"
	"github.com/jcrussell/solvent-streets/pkg/iostreams"
)

func TestHandleDataMetaJSON(t *testing.T) {
	testBoundary := `{"type":"Polygon","coordinates":[[[-121.84,37.64],[-121.68,37.64],[-121.68,37.72],[-121.84,37.72],[-121.84,37.64]]]}`
	store := &dbtest.MockStore{
		GetBoundaryFunc: func(_ context.Context) (string, error) { return testBoundary, nil },
		LatestComputeResultFunc: func(_ context.Context, rt string) (*db.ComputeResult, error) {
			if rt == "roads" {
				return &db.ComputeResult{
					ResourceType: "roads",
					TotalAreaSqM: 46452,
					FeatureCount: 100,
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
	ios, _, _, _ := iostreams.Test()
	srv := New([]export.CityEntry{entry}, 0, ios)
	mux := http.NewServeMux()
	mux.HandleFunc("GET /data/{file}", srv.handleDataFile(entry))

	req, _ := http.NewRequestWithContext(context.Background(), "GET", "/data/meta.json", nil)
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

func TestHandleIndex(t *testing.T) {
	testBoundary := `{"type":"Polygon","coordinates":[[[-121.84,37.64],[-121.68,37.64],[-121.68,37.72],[-121.84,37.72],[-121.84,37.64]]]}`
	store := &dbtest.MockStore{
		GetBoundaryFunc: func(_ context.Context) (string, error) { return testBoundary, nil },
		LatestComputeResultFunc: func(_ context.Context, _ string) (*db.ComputeResult, error) {
			return nil, fmt.Errorf("not found")
		},
	}
	cfg := &config.Config{
		Cities: []config.CityConfig{{Name: "Test City"}},
	}
	entry := export.CityEntry{
		Config: cfg,
		City:   cfg.Cities[0],
		Store:  store,
		Slug:   cfg.Cities[0].Slug(),
	}
	ios, _, _, _ := iostreams.Test()
	srv := New([]export.CityEntry{entry}, 0, ios)
	mux := http.NewServeMux()
	mux.HandleFunc("GET /", srv.handleIndex)

	req, _ := http.NewRequestWithContext(context.Background(), "GET", "/", nil)
	w := httptest.NewRecorder()
	mux.ServeHTTP(w, req)

	if w.Code != http.StatusOK {
		t.Fatalf("expected 200, got %d: %s", w.Code, w.Body.String())
	}
	if w.Body.Len() == 0 {
		t.Fatal("empty body")
	}
}

func TestServeJSONCached_SingleFlight(t *testing.T) {
	ios, _, _, _ := iostreams.Test()
	srv := New(nil, 0, ios)

	const goroutines = 32
	var calls atomic.Int32
	start := make(chan struct{})

	build := func() (any, error) { //nolint:unparam // signature must match serveJSONCached parameter
		calls.Add(1)
		// Hold long enough that all goroutines can pile up on the same thunk.
		time.Sleep(20 * time.Millisecond)
		return map[string]string{"hello": "world"}, nil
	}

	var wg sync.WaitGroup
	wg.Add(goroutines)
	for range goroutines {
		go func() {
			defer wg.Done()
			<-start
			w := httptest.NewRecorder()
			srv.serveJSONCached(w, "test", build)
			if w.Code != http.StatusOK {
				t.Errorf("expected 200, got %d: %s", w.Code, w.Body.String())
			}
		}()
	}
	close(start)
	wg.Wait()

	if got := calls.Load(); got != 1 {
		t.Errorf("expected build to run exactly once, ran %d times", got)
	}
}

func TestServeJSONCached_ErrorEvicts(t *testing.T) {
	ios, _, _, _ := iostreams.Test()
	srv := New(nil, 0, ios)

	var calls atomic.Int32
	build := func() (any, error) {
		n := calls.Add(1)
		if n == 1 {
			return nil, errors.New("boom")
		}
		return map[string]string{"ok": "yes"}, nil
	}

	// First call: error → cache evicts the failed thunk.
	w1 := httptest.NewRecorder()
	srv.serveJSONCached(w1, "test", build)
	if w1.Code != http.StatusInternalServerError {
		t.Fatalf("expected 500 on first call, got %d", w1.Code)
	}

	// Second call: build runs again and succeeds.
	w2 := httptest.NewRecorder()
	srv.serveJSONCached(w2, "test", build)
	if w2.Code != http.StatusOK {
		t.Fatalf("expected 200 on retry, got %d: %s", w2.Code, w2.Body.String())
	}
	if got := calls.Load(); got != 2 {
		t.Errorf("expected build to run twice (error + retry), ran %d times", got)
	}
}

// TestBuildForecasts_DBErrorEvicts verifies that a real DB error during the
// forecast build evicts both cache layers (s.cache forecast:slug and
// s.forecasts slug) so the next request retries against a recovered store
// rather than memoizing a partial/empty slice for the server's lifetime.
func TestBuildForecasts_DBErrorEvicts(t *testing.T) {
	ios, _, _, _ := iostreams.Test()
	var calls atomic.Int32
	failingStore := &dbtest.MockStore{
		LatestComputeResultFunc: func(_ context.Context, rt string) (*db.ComputeResult, error) {
			n := calls.Add(1)
			if n <= int32(len(resource.All)) {
				return nil, errors.New("DB unavailable")
			}
			return &db.ComputeResult{
				ResourceType: rt,
				TotalAreaSqM: 10000,
				FeatureCount: 100,
				ComputedAt:   time.Now(),
			}, nil
		},
	}
	cfg := &config.Config{Cities: []config.CityConfig{{Name: "Test City"}}}
	entry := export.CityEntry{
		Config: cfg,
		City:   cfg.Cities[0],
		Store:  failingStore,
		Slug:   cfg.Cities[0].Slug(),
	}
	srv := New([]export.CityEntry{entry}, 0, ios)

	w1 := httptest.NewRecorder()
	srv.serveForecastJSON(w1, nil, entry)
	if w1.Code != http.StatusInternalServerError {
		t.Fatalf("expected 500 on first call, got %d: %s", w1.Code, w1.Body.String())
	}

	w2 := httptest.NewRecorder()
	srv.serveForecastJSON(w2, nil, entry)
	if w2.Code != http.StatusOK {
		t.Fatalf("expected 200 on retry, got %d: %s", w2.Code, w2.Body.String())
	}

	want := int32(2 * len(resource.All))
	if got := calls.Load(); got != want {
		t.Errorf("expected LatestComputeResult to run %d times (build twice), ran %d", want, got)
	}
}

func TestServeJSONCached_PanicEvicts(t *testing.T) {
	ios, _, _, _ := iostreams.Test()
	srv := New(nil, 0, ios)

	var calls atomic.Int32
	build := func() (any, error) { //nolint:unparam // signature must match serveJSONCached parameter
		n := calls.Add(1)
		if n == 1 {
			panic("kaboom")
		}
		return map[string]string{"ok": "yes"}, nil
	}

	// First call: panic propagates up to recoveryMiddleware and evicts.
	handler := recoveryMiddleware(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		srv.serveJSONCached(w, "test", build)
	}), ios.ErrOut)

	req1, _ := http.NewRequestWithContext(context.Background(), "GET", "/", nil)
	w1 := httptest.NewRecorder()
	handler.ServeHTTP(w1, req1)
	if w1.Code != http.StatusInternalServerError {
		t.Fatalf("expected 500 on panic, got %d", w1.Code)
	}

	// Second call: build runs again and succeeds.
	req2, _ := http.NewRequestWithContext(context.Background(), "GET", "/", nil)
	w2 := httptest.NewRecorder()
	handler.ServeHTTP(w2, req2)
	if w2.Code != http.StatusOK {
		t.Fatalf("expected 200 on retry, got %d: %s", w2.Code, w2.Body.String())
	}
	if got := calls.Load(); got != 2 {
		t.Errorf("expected build to run twice (panic + retry), ran %d times", got)
	}
}
