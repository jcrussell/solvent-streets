package ingest

import (
	"context"
	"errors"
	"net/http"
	"net/http/httptest"
	"os"
	"strings"
	"testing"
)

// overpassTestServer mirrors nominatimTestServer's shape but accepts
// POST + form-encoded `data=...` bodies and returns the canned body
// for any query. Tests can verify the User-Agent and that POST was
// used.
func overpassTestServer(t *testing.T, body string) *httptest.Server {
	t.Helper()
	return httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodPost {
			t.Errorf("expected POST, got %s", r.Method)
		}
		if ua := r.Header.Get("User-Agent"); !strings.Contains(ua, "pvmt") {
			t.Errorf("expected User-Agent containing 'pvmt', got %q", ua)
		}
		w.WriteHeader(http.StatusOK)
		_, _ = w.Write([]byte(body))
	}))
}

// TestFetchCityBoundaryFromRelation_AlbuquerqueFixture exercises the
// real Albuquerque (R171262) Overpass payload captured in testdata/.
// The fixture is the regression motivating this entire feature; if
// the stitching primitives or the polysToMultiPolygonGeoJSON emitter
// drift, this test catches it on real OSM data shape.
func TestFetchCityBoundaryFromRelation_AlbuquerqueFixture(t *testing.T) {
	data, err := os.ReadFile("testdata/abq_relation.json")
	if err != nil {
		t.Fatalf("read fixture: %v", err)
	}
	srv := overpassTestServer(t, string(data))
	t.Cleanup(srv.Close)

	result, err := fetchCityBoundaryFromRelation(context.Background(), srv.Client(), srv.URL, 171262)
	if err != nil {
		t.Fatalf("fetch failed: %v", err)
	}
	if !strings.HasPrefix(result, `{"type":"MultiPolygon","coordinates":[[`) {
		t.Errorf("expected MultiPolygon GeoJSON, got prefix: %.80s", result)
	}
	// Sanity check: Albuquerque's bbox is roughly lon -107..-106.4,
	// lat 34.95..35.25. Spot-check that the rendered coordinates fall
	// inside that envelope so we know stitching produced real geometry.
	if !strings.Contains(result, "-106.") {
		t.Errorf("expected ABQ-area longitudes in output, got: %.200s", result)
	}
}

// TestFetchCityBoundaryFromRelation_InvalidID rejects non-positive
// relation IDs without an HTTP call.
func TestFetchCityBoundaryFromRelation_InvalidID(t *testing.T) {
	srv := overpassTestServer(t, `{"elements":[]}`)
	t.Cleanup(srv.Close)

	for _, id := range []int64{0, -1, -171262} {
		_, err := fetchCityBoundaryFromRelation(context.Background(), srv.Client(), srv.URL, id)
		if err == nil {
			t.Errorf("expected error for relation id %d, got nil", id)
		}
		if !strings.Contains(err.Error(), "invalid relation id") {
			t.Errorf("id=%d: expected 'invalid relation id' in error, got: %v", id, err)
		}
	}
}

// TestFetchCityBoundaryFromRelation_EmptyResponse returns
// ErrBoundaryRelationNotFound for an empty Overpass payload — the
// usual signal that the relation id is wrong or deleted.
func TestFetchCityBoundaryFromRelation_EmptyResponse(t *testing.T) {
	srv := overpassTestServer(t, `{"elements":[]}`)
	t.Cleanup(srv.Close)

	_, err := fetchCityBoundaryFromRelation(context.Background(), srv.Client(), srv.URL, 999999999)
	if !errors.Is(err, ErrBoundaryRelationNotFound) {
		t.Fatalf("expected ErrBoundaryRelationNotFound, got: %v", err)
	}
}

// TestFetchCityBoundaryFromRelation_NoWayMembers handles a relation
// whose members are all Nodes (broken multipolygon) — stitchRings
// produces zero outer rings → ErrBoundaryRelationNotFound.
func TestFetchCityBoundaryFromRelation_NoWayMembers(t *testing.T) {
	body := `{"elements":[{"type":"relation","id":42,"members":[{"type":"node","ref":1,"role":"outer"}]}]}`
	srv := overpassTestServer(t, body)
	t.Cleanup(srv.Close)

	_, err := fetchCityBoundaryFromRelation(context.Background(), srv.Client(), srv.URL, 42)
	if !errors.Is(err, ErrBoundaryRelationNotFound) {
		t.Fatalf("expected ErrBoundaryRelationNotFound for no-way relation, got: %v", err)
	}
}

// TestFetchCityBoundaryFromRelation_TooLarge catches the
// wrong-relation-ID footgun — a relation whose bbox spans more than
// 5° is almost certainly a county or state, not a city.
func TestFetchCityBoundaryFromRelation_TooLarge(t *testing.T) {
	// A relation with one outer ring spanning ~10° of lon/lat.
	body := `{"elements":[{"type":"relation","id":7,"members":[
		{"type":"way","ref":1,"role":"outer","geometry":[
			{"lon":-110.0,"lat":30.0},
			{"lon":-100.0,"lat":30.0},
			{"lon":-100.0,"lat":40.0},
			{"lon":-110.0,"lat":40.0},
			{"lon":-110.0,"lat":30.0}
		]}
	]}]}`
	srv := overpassTestServer(t, body)
	t.Cleanup(srv.Close)

	_, err := fetchCityBoundaryFromRelation(context.Background(), srv.Client(), srv.URL, 7)
	if !errors.Is(err, ErrBoundaryRelationTooLarge) {
		t.Fatalf("expected ErrBoundaryRelationTooLarge, got: %v", err)
	}
}

// TestFetchCityBoundaryFromRelation_HTTPError surfaces non-200 status
// codes (Overpass overload, etc.) as plain errors with the status
// visible in the message.
func TestFetchCityBoundaryFromRelation_HTTPError(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusGatewayTimeout)
		_, _ = w.Write([]byte("gateway timeout"))
	}))
	t.Cleanup(srv.Close)

	_, err := fetchCityBoundaryFromRelation(context.Background(), srv.Client(), srv.URL, 171262)
	if err == nil {
		t.Fatal("expected error on 504, got nil")
	}
	if !strings.Contains(err.Error(), "504") {
		t.Errorf("expected status 504 in error, got: %v", err)
	}
}

// TestFetchCityBoundaryFromRelation_SimpleSquare exercises a minimal
// hand-built outer ring (no holes, no fragments) end-to-end so the
// happy-path emit shape is pinned without depending on the larger
// fixture.
func TestFetchCityBoundaryFromRelation_SimpleSquare(t *testing.T) {
	body := `{"elements":[{"type":"relation","id":1,"members":[
		{"type":"way","ref":10,"role":"outer","geometry":[
			{"lon":-106.7,"lat":35.0},
			{"lon":-106.5,"lat":35.0},
			{"lon":-106.5,"lat":35.2},
			{"lon":-106.7,"lat":35.2},
			{"lon":-106.7,"lat":35.0}
		]}
	]}]}`
	srv := overpassTestServer(t, body)
	t.Cleanup(srv.Close)

	result, err := fetchCityBoundaryFromRelation(context.Background(), srv.Client(), srv.URL, 1)
	if err != nil {
		t.Fatalf("fetch failed: %v", err)
	}
	if !strings.HasPrefix(result, `{"type":"MultiPolygon","coordinates":[[[`) {
		t.Errorf("expected MultiPolygon GeoJSON, got prefix: %.80s", result)
	}
}

// TestFetchCityBoundaryFromRelation_OuterPlusInnerHole exercises hole
// assignment: one large outer + one small inner contained in it
// produces a Polygon with a hole.
func TestFetchCityBoundaryFromRelation_OuterPlusInnerHole(t *testing.T) {
	body := `{"elements":[{"type":"relation","id":2,"members":[
		{"type":"way","ref":20,"role":"outer","geometry":[
			{"lon":-106.8,"lat":35.0},
			{"lon":-106.4,"lat":35.0},
			{"lon":-106.4,"lat":35.3},
			{"lon":-106.8,"lat":35.3},
			{"lon":-106.8,"lat":35.0}
		]},
		{"type":"way","ref":21,"role":"inner","geometry":[
			{"lon":-106.7,"lat":35.1},
			{"lon":-106.5,"lat":35.1},
			{"lon":-106.5,"lat":35.2},
			{"lon":-106.7,"lat":35.2},
			{"lon":-106.7,"lat":35.1}
		]}
	]}]}`
	srv := overpassTestServer(t, body)
	t.Cleanup(srv.Close)

	result, err := fetchCityBoundaryFromRelation(context.Background(), srv.Client(), srv.URL, 2)
	if err != nil {
		t.Fatalf("fetch failed: %v", err)
	}
	// Two coordinate rings inside the one polygon = outer + hole.
	if strings.Count(result, "[[-106.") < 2 {
		t.Errorf("expected outer + inner rings in output, got: %.300s", result)
	}
}
