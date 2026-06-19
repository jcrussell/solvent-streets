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

// TestFetchCityBoundary_ConstrainsToUS pins the country filter on the
// Nominatim query. Without countrycodes=us, names like "Windsor, CA" and
// "Richmond, CA" resolve to Windsor, Ontario and Richmond, British
// Columbia (Nominatim reads "CA" as the ISO country code for Canada),
// producing wrong-country boundaries. Regression for the geocode audit
// that found Windsor/Richmond pulling Canadian data.
func TestFetchCityBoundary_ConstrainsToUS(t *testing.T) {
	geojson := `{"type":"Polygon","coordinates":[[[-122.8,38.5],[-122.7,38.5],[-122.7,38.6],[-122.8,38.6],[-122.8,38.5]]]}`
	body := `[{"addresstype":"town","geojson":` + geojson + `}]`

	var gotQuery string
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		gotQuery = r.URL.RawQuery
		w.WriteHeader(http.StatusOK)
		w.Write([]byte(body))
	}))
	t.Cleanup(srv.Close)

	if _, err := fetchCityBoundary(context.Background(), srv.Client(), srv.URL, "Windsor, CA"); err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(gotQuery, "countrycodes=us") {
		t.Errorf("expected query to constrain to US (countrycodes=us), got: %s", gotQuery)
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

// TestFetchCityBoundary_AllPointResults_ErrorsWithHint pins the
// Albuquerque-class failure mode: when every Nominatim result has a
// non-polygon geometry (here a place=city node returns a Point), the
// fetch returns a clear error suggesting the boundary_relation_id
// escape hatch. Regression caught: the legacy code picked results[0]
// regardless of geometry, then errored confusingly with "got Point"
// after silently accepting the wrong-shaped result.
func TestFetchCityBoundary_AllPointResults_ErrorsWithHint(t *testing.T) {
	body := `[{"addresstype":"city","geojson":{"type":"Point","coordinates":[-106.6,35.1]}}]`
	srv := nominatimTestServer(t, body)
	t.Cleanup(srv.Close)

	_, err := fetchCityBoundary(context.Background(), srv.Client(), srv.URL, "Albuquerque, NM")
	if err == nil {
		t.Fatal("expected error when no polygon-typed result exists")
	}
	if !strings.Contains(err.Error(), "no Polygon/MultiPolygon") {
		t.Errorf("expected 'no Polygon/MultiPolygon' in error, got: %v", err)
	}
	if !strings.Contains(err.Error(), "boundary_relation_id") {
		t.Errorf("expected hint to mention boundary_relation_id, got: %v", err)
	}
}

// TestFetchCityBoundary_SkipsCityTypedPointForLaterPolygon pins the
// selection contract: a city-typed Point result is skipped in favor
// of a later Polygon-typed result (city or otherwise). The selection
// loop now requires both addresstype AND polygon geometry.
func TestFetchCityBoundary_SkipsCityTypedPointForLaterPolygon(t *testing.T) {
	cityPoint := `{"type":"Point","coordinates":[-106.6,35.1]}`
	townPoly := `{"type":"Polygon","coordinates":[[[-106.7,35.0],[-106.5,35.0],[-106.5,35.2],[-106.7,35.2],[-106.7,35.0]]]}`
	body := `[{"addresstype":"city","geojson":` + cityPoint + `},{"addresstype":"town","geojson":` + townPoly + `}]`

	srv := nominatimTestServer(t, body)
	t.Cleanup(srv.Close)

	result, err := fetchCityBoundary(context.Background(), srv.Client(), srv.URL, "Test")
	if err != nil {
		t.Fatal(err)
	}
	if result != townPoly {
		t.Errorf("expected the town-typed polygon, got: %s", result)
	}
}

// TestFetchCityBoundary_FallsBackToNonCityTypedPolygon pins the
// fallback rule: when no addresstype ∈ {city,town,village,municipality}
// has a polygon, any polygon-typed result wins over erroring out.
// Keeps existing well-formed lookups working even if Nominatim's
// addresstype tagging shifts.
func TestFetchCityBoundary_FallsBackToNonCityTypedPolygon(t *testing.T) {
	adminPoly := `{"type":"Polygon","coordinates":[[[-106.7,35.0],[-106.5,35.0],[-106.5,35.2],[-106.7,35.2],[-106.7,35.0]]]}`
	body := `[{"addresstype":"administrative","geojson":` + adminPoly + `}]`

	srv := nominatimTestServer(t, body)
	t.Cleanup(srv.Close)

	result, err := fetchCityBoundary(context.Background(), srv.Client(), srv.URL, "Test")
	if err != nil {
		t.Fatal(err)
	}
	if result != adminPoly {
		t.Errorf("expected admin polygon as fallback, got: %s", result)
	}
}

// TestFetchCityBoundary_OversizedBodyTruncated pins the LimitReader
// guard on the Nominatim response. Regression for solvent-streets-675b:
// the read was unbounded and a hostile or buggy Nominatim response
// could drive the process OOM. With the cap, an oversized body is
// truncated to the limit; the truncated JSON fails to parse and the
// caller sees an error instead of an allocation.
func TestFetchCityBoundary_OversizedBodyTruncated(t *testing.T) {
	prev := maxResponseBodyBytes
	maxResponseBodyBytes = 32
	t.Cleanup(func() { maxResponseBodyBytes = prev })

	// A body that parses fine in full but is invalid JSON when cut at 32 bytes.
	geojson := `{"type":"Polygon","coordinates":[[[-121.9,37.6],[-121.8,37.6],[-121.8,37.7],[-121.9,37.7],[-121.9,37.6]]]}`
	body := `[{"addresstype":"city","geojson":` + geojson + `}]`
	srv := nominatimTestServer(t, body)
	t.Cleanup(srv.Close)

	_, err := fetchCityBoundary(context.Background(), srv.Client(), srv.URL, "Test")
	if err == nil {
		t.Fatal("expected parse error on truncated oversized response")
	}
	if !strings.Contains(err.Error(), "parse nominatim response") {
		t.Errorf("expected parse-error message, got: %v", err)
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
