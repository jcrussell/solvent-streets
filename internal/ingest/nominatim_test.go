package ingest

import (
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

func TestFetchCityBoundary_Success(t *testing.T) {
	geojson := `{"type":"Polygon","coordinates":[[[-121.9,37.6],[-121.8,37.6],[-121.8,37.7],[-121.9,37.7],[-121.9,37.6]]]}`
	body := `[{"geojson":` + geojson + `}]`

	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if ua := r.Header.Get("User-Agent"); !strings.Contains(ua, "pvmt") {
			t.Errorf("expected User-Agent containing 'pvmt', got %q", ua)
		}
		w.WriteHeader(http.StatusOK)
		w.Write([]byte(body))
	}))
	defer srv.Close()

	result, err := fetchFromURL(srv.Client(), srv.URL, "Livermore")
	if err != nil {
		t.Fatal(err)
	}
	if result != geojson {
		t.Errorf("unexpected result:\n got: %s\nwant: %s", result, geojson)
	}
}

func TestFetchCityBoundary_EmptyResults(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusOK)
		w.Write([]byte(`[]`))
	}))
	defer srv.Close()

	_, err := fetchFromURL(srv.Client(), srv.URL, "Nonexistent")
	if err == nil {
		t.Fatal("expected error for empty results")
	}
	if !strings.Contains(err.Error(), "no results") {
		t.Errorf("expected 'no results' error, got: %v", err)
	}
}

func TestFetchCityBoundary_Non200(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusServiceUnavailable)
	}))
	defer srv.Close()

	_, err := fetchFromURL(srv.Client(), srv.URL, "Test")
	if err == nil {
		t.Fatal("expected error for non-200 status")
	}
	if !strings.Contains(err.Error(), "503") {
		t.Errorf("expected status 503 in error, got: %v", err)
	}
}

func TestFetchCityBoundary_UnsupportedGeometryType(t *testing.T) {
	body := `[{"geojson":{"type":"Point","coordinates":[-121.9,37.6]}}]`
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusOK)
		w.Write([]byte(body))
	}))
	defer srv.Close()

	_, err := fetchFromURL(srv.Client(), srv.URL, "Test")
	if err == nil {
		t.Fatal("expected error for unsupported geometry type")
	}
	if !strings.Contains(err.Error(), "Point") {
		t.Errorf("expected 'Point' in error, got: %v", err)
	}
}

func TestFetchCityBoundary_MultiPolygon(t *testing.T) {
	geojson := `{"type":"MultiPolygon","coordinates":[[[[-121.9,37.6],[-121.8,37.6],[-121.8,37.7],[-121.9,37.7],[-121.9,37.6]]]]}`
	body := `[{"geojson":` + geojson + `}]`

	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusOK)
		w.Write([]byte(body))
	}))
	defer srv.Close()

	result, err := fetchFromURL(srv.Client(), srv.URL, "Test")
	if err != nil {
		t.Fatal(err)
	}
	if result != geojson {
		t.Errorf("unexpected result:\n got: %s\nwant: %s", result, geojson)
	}
}
