package ingest

import (
	"context"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

func nominatimTestServer(t *testing.T, body string) *httptest.Server {
	t.Helper()
	return httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if ua := r.Header.Get("User-Agent"); !strings.Contains(ua, "pvmt") {
			t.Errorf("expected User-Agent containing 'pvmt', got %q", ua)
		}
		w.WriteHeader(http.StatusOK)
		w.Write([]byte(body))
	}))
}

func TestFetchCityBoundary_Success(t *testing.T) {
	geojson := `{"type":"Polygon","coordinates":[[[-121.9,37.6],[-121.8,37.6],[-121.8,37.7],[-121.9,37.7],[-121.9,37.6]]]}`
	body := `[{"addresstype":"city","geojson":` + geojson + `}]`

	srv := nominatimTestServer(t, body)
	t.Cleanup(srv.Close)

	result, err := fetchCityBoundary(context.Background(), srv.Client(), srv.URL, "Livermore")
	if err != nil {
		t.Fatal(err)
	}
	if result != geojson {
		t.Errorf("unexpected result:\n got: %s\nwant: %s", result, geojson)
	}
}

func TestFetchCityBoundary_PrefersCityOverCounty(t *testing.T) {
	countyGeo := `{"type":"Polygon","coordinates":[[[-122.3,37.4],[-121.4,37.4],[-121.4,37.9],[-122.3,37.9],[-122.3,37.4]]]}`
	cityGeo := `{"type":"Polygon","coordinates":[[[-122.3,37.7],[-122.2,37.7],[-122.2,37.8],[-122.3,37.8],[-122.3,37.7]]]}`
	body := `[{"addresstype":"county","geojson":` + countyGeo + `},{"addresstype":"city","geojson":` + cityGeo + `}]`

	srv := nominatimTestServer(t, body)
	t.Cleanup(srv.Close)

	result, err := fetchCityBoundary(context.Background(), srv.Client(), srv.URL, "Alameda, CA")
	if err != nil {
		t.Fatal(err)
	}
	if result != cityGeo {
		t.Error("expected city boundary, got county boundary")
	}
}

func TestFetchCityBoundary_EmptyResults(t *testing.T) {
	srv := nominatimTestServer(t, `[]`)
	t.Cleanup(srv.Close)

	_, err := fetchCityBoundary(context.Background(), srv.Client(), srv.URL, "Nonexistent")
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
	t.Cleanup(srv.Close)

	_, err := fetchCityBoundary(context.Background(), srv.Client(), srv.URL, "Test")
	if err == nil {
		t.Fatal("expected error for non-200 status")
	}
	if !strings.Contains(err.Error(), "503") {
		t.Errorf("expected status 503 in error, got: %v", err)
	}
}

func TestFetchCityBoundary_UnsupportedGeometryType(t *testing.T) {
	body := `[{"addresstype":"city","geojson":{"type":"Point","coordinates":[-121.9,37.6]}}]`
	srv := nominatimTestServer(t, body)
	t.Cleanup(srv.Close)

	_, err := fetchCityBoundary(context.Background(), srv.Client(), srv.URL, "Test")
	if err == nil {
		t.Fatal("expected error for unsupported geometry type")
	}
	if !strings.Contains(err.Error(), "Point") {
		t.Errorf("expected 'Point' in error, got: %v", err)
	}
}

func TestFetchCityBoundary_MultiPolygon(t *testing.T) {
	geojson := `{"type":"MultiPolygon","coordinates":[[[[-121.9,37.6],[-121.8,37.6],[-121.8,37.7],[-121.9,37.7],[-121.9,37.6]]]]}`
	body := `[{"addresstype":"city","geojson":` + geojson + `}]`

	srv := nominatimTestServer(t, body)
	t.Cleanup(srv.Close)

	result, err := fetchCityBoundary(context.Background(), srv.Client(), srv.URL, "Test")
	if err != nil {
		t.Fatal(err)
	}
	if result != geojson {
		t.Errorf("unexpected result:\n got: %s\nwant: %s", result, geojson)
	}
}
