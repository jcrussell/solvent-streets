package ingest

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strconv"
	"strings"
	"testing"

	"github.com/jcrussell/solvent-streets/internal/resource"
)

var rtRoads = resource.TypeRoads

func TestParseArcGISGeoJSON_BasicFeature(t *testing.T) {
	data := `{
		"features": [
			{
				"properties": {"OBJECTID": 42, "FULLNAME": "First St"},
				"geometry": {"type": "LineString", "coordinates": [[-121.77, 37.68], [-121.76, 37.69]]}
			}
		]
	}`
	features, err := parseArcGISGeoJSON([]byte(data), rtRoads, 0)
	if err != nil {
		t.Fatal(err)
	}
	if len(features) != 1 {
		t.Fatalf("expected 1 feature, got %d", len(features))
	}
	f := features[0]
	if f.ID != "arcgis:42" {
		t.Errorf("expected id arcgis:42, got %s", f.ID)
	}
	if f.Name != "First St" {
		t.Errorf("expected name First St, got %s", f.Name)
	}
	if f.ResourceType != rtRoads {
		t.Errorf("expected resource type pavements, got %s", f.ResourceType)
	}
}

func TestParseArcGISGeoJSON_NoOBJECTID(t *testing.T) {
	data := `{
		"features": [
			{
				"properties": {"FULLNAME": "Second St"},
				"geometry": {"type": "LineString", "coordinates": [[-121.77, 37.68]]}
			}
		]
	}`
	features, err := parseArcGISGeoJSON([]byte(data), rtRoads, 0)
	if err != nil {
		t.Fatal(err)
	}
	if len(features) != 1 {
		t.Fatalf("expected 1 feature, got %d", len(features))
	}
	if features[0].ID != "arcgis:0" {
		t.Errorf("expected fallback id arcgis:0, got %s", features[0].ID)
	}
}

func TestParseArcGISGeoJSON_FULLNAMEExtraction(t *testing.T) {
	for _, key := range []string{"FULLNAME", "FullName", "fullname"} {
		data := `{"features": [{"properties": {"` + key + `": "Third Ave"}, "geometry": {"type":"Point","coordinates":[-121.77,37.68]}}]}`
		features, err := parseArcGISGeoJSON([]byte(data), rtRoads, 0)
		if err != nil {
			t.Fatal(err)
		}
		if len(features) != 1 || features[0].Name != "Third Ave" {
			t.Errorf("key %s: expected name Third Ave, got %s", key, features[0].Name)
		}
	}
}

func TestParseArcGISGeoJSON_NullGeometry(t *testing.T) {
	// JSON null results in json.RawMessage("null") which is non-nil,
	// so the feature is included. Missing geometry field results in nil.
	data := `{
		"features": [
			{
				"properties": {"OBJECTID": 1}
			}
		]
	}`
	features, err := parseArcGISGeoJSON([]byte(data), rtRoads, 0)
	if err != nil {
		t.Fatal(err)
	}
	if len(features) != 0 {
		t.Errorf("expected 0 features for missing geometry, got %d", len(features))
	}
}

func TestParseArcGISGeoJSON_EmptyFeatures(t *testing.T) {
	data := `{"features": []}`
	features, err := parseArcGISGeoJSON([]byte(data), rtRoads, 0)
	if err != nil {
		t.Fatal(err)
	}
	if len(features) != 0 {
		t.Errorf("expected 0 features, got %d", len(features))
	}
}

func TestParseArcGISGeoJSON_NumericPropertyValues(t *testing.T) {
	data := `{
		"features": [
			{
				"properties": {"OBJECTID": 1, "SPEED_LIMIT": 35},
				"geometry": {"type": "Point", "coordinates": [-121.77, 37.68]}
			}
		]
	}`
	features, err := parseArcGISGeoJSON([]byte(data), rtRoads, 0)
	if err != nil {
		t.Fatal(err)
	}
	if len(features) != 1 {
		t.Fatalf("expected 1 feature, got %d", len(features))
	}
	if features[0].Tags["SPEED_LIMIT"] != "35" {
		t.Errorf("expected SPEED_LIMIT tag '35', got %q", features[0].Tags["SPEED_LIMIT"])
	}
}

func TestParseArcGISGeoJSON_BaseIndexOffset(t *testing.T) {
	// Features without OBJECTID should use baseIndex+i as the fallback ID.
	data := `{"features": [
		{"properties": {"FULLNAME": "A St"}, "geometry": {"type":"Point","coordinates":[-121.77,37.68]}},
		{"properties": {"FULLNAME": "B St"}, "geometry": {"type":"Point","coordinates":[-121.76,37.69]}}
	]}`
	features, err := parseArcGISGeoJSON([]byte(data), rtRoads, 5000)
	if err != nil {
		t.Fatal(err)
	}
	if len(features) != 2 {
		t.Fatalf("expected 2 features, got %d", len(features))
	}
	if features[0].ID != "arcgis:5000" {
		t.Errorf("expected id arcgis:5000, got %s", features[0].ID)
	}
	if features[1].ID != "arcgis:5001" {
		t.Errorf("expected id arcgis:5001, got %s", features[1].ID)
	}
}

// makeArcGISFeatures builds a GeoJSON response with n features, each having
// an OBJECTID starting at startOID.
func makeArcGISFeatures(n, startOID int) []byte {
	type feat struct {
		Properties map[string]any  `json:"properties"`
		Geometry   json.RawMessage `json:"geometry"`
	}
	feats := make([]feat, n)
	for i := range feats {
		feats[i] = feat{
			Properties: map[string]any{"OBJECTID": startOID + i},
			Geometry:   json.RawMessage(`{"type":"Point","coordinates":[-121.77,37.68]}`),
		}
	}
	data, _ := json.Marshal(map[string]any{"features": feats})
	return data
}

func TestFetch_Pagination(t *testing.T) {
	// Simulate a server that returns arcgisMaxRecords on the first page
	// and a smaller set on the second page.
	const pageSize = arcgisMaxRecords
	const page2Size = 42

	calls := 0
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		calls++
		offset, _ := strconv.Atoi(r.URL.Query().Get("resultOffset"))
		var body []byte
		switch offset {
		case 0:
			body = makeArcGISFeatures(pageSize, 1)
		case pageSize:
			body = makeArcGISFeatures(page2Size, pageSize+1)
		default:
			t.Errorf("unexpected offset %d", offset)
			http.Error(w, "bad offset", 400)
			return
		}
		w.Header().Set("Content-Type", "application/json")
		w.Write(body)
	}))
	t.Cleanup(srv.Close)

	src := &ArcGISSource{
		BBox: [4]float64{37.0, -122.0, 38.0, -121.0},
		URL:  srv.URL,
	}
	rt := resource.ByType(resource.TypeRoads)
	features, err := src.Fetch(context.Background(), srv.Client(), rt)
	if err != nil {
		t.Fatal(err)
	}
	if len(features) != pageSize+page2Size {
		t.Fatalf("expected %d features, got %d", pageSize+page2Size, len(features))
	}
	if calls != 2 {
		t.Errorf("expected 2 server calls, got %d", calls)
	}
	// Verify IDs from both pages are present.
	if features[0].ID != "arcgis:1" {
		t.Errorf("first feature id: want arcgis:1, got %s", features[0].ID)
	}
	if features[pageSize].ID != fmt.Sprintf("arcgis:%d", pageSize+1) {
		t.Errorf("first page-2 feature id: want arcgis:%d, got %s", pageSize+1, features[pageSize].ID)
	}
}

func TestFetch_SinglePage(t *testing.T) {
	// When the server returns fewer than arcgisMaxRecords, no second request.
	calls := 0
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		calls++
		w.Header().Set("Content-Type", "application/json")
		w.Write(makeArcGISFeatures(10, 1))
	}))
	t.Cleanup(srv.Close)

	src := &ArcGISSource{
		BBox: [4]float64{37.0, -122.0, 38.0, -121.0},
		URL:  srv.URL,
	}
	features, err := src.Fetch(context.Background(), srv.Client(), resource.ByType(resource.TypeRoads))
	if err != nil {
		t.Fatal(err)
	}
	if len(features) != 10 {
		t.Fatalf("expected 10 features, got %d", len(features))
	}
	if calls != 1 {
		t.Errorf("expected 1 server call, got %d", calls)
	}
}

func TestFetch_ArcGISErrorEnvelope(t *testing.T) {
	// ArcGIS returns service-level errors as HTTP 200 with a JSON error
	// envelope. The fetcher should surface the message and endpoint URL
	// rather than silently treating the response as an empty feature list.
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"error":{"code":400,"message":"Invalid URL","details":["Invalid URL"]}}`))
	}))
	t.Cleanup(srv.Close)

	src := &ArcGISSource{
		BBox: [4]float64{37.0, -122.0, 38.0, -121.0},
		URL:  srv.URL,
	}
	_, err := src.Fetch(context.Background(), srv.Client(), resource.ByType(resource.TypeRoads))
	if err == nil {
		t.Fatal("expected error for ArcGIS error envelope, got nil")
	}
	msg := err.Error()
	for _, want := range []string{srv.URL, "code 400", "Invalid URL"} {
		if !strings.Contains(msg, want) {
			t.Errorf("error %q missing expected substring %q", msg, want)
		}
	}
}

func TestFetch_NonOKStatusIncludesEndpoint(t *testing.T) {
	// HTTP 400/500 responses must include the endpoint URL so a stale or
	// misconfigured service is debuggable from the error alone.
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		http.Error(w, "Bad Request", http.StatusBadRequest)
	}))
	t.Cleanup(srv.Close)

	src := &ArcGISSource{
		BBox: [4]float64{37.0, -122.0, 38.0, -121.0},
		URL:  srv.URL,
	}
	_, err := src.Fetch(context.Background(), srv.Client(), resource.ByType(resource.TypeRoads))
	if err == nil {
		t.Fatal("expected error for HTTP 400, got nil")
	}
	if !strings.Contains(err.Error(), srv.URL) {
		t.Errorf("error %q does not include endpoint %q", err.Error(), srv.URL)
	}
}

func TestArcGISErrorMessage(t *testing.T) {
	cases := []struct {
		name   string
		body   string
		wantOK bool
		want   string
	}{
		{
			name:   "error envelope with details",
			body:   `{"error":{"code":400,"message":"Invalid URL","details":["Invalid URL"]}}`,
			wantOK: true,
			want:   "code 400: Invalid URL",
		},
		{
			name:   "error envelope with distinct detail",
			body:   `{"error":{"code":498,"message":"Invalid Token","details":["Token expired"]}}`,
			wantOK: true,
			want:   "code 498: Invalid Token (Token expired)",
		},
		{
			name:   "valid feature collection",
			body:   `{"features":[{"properties":{"OBJECTID":1},"geometry":{"type":"Point","coordinates":[0,0]}}]}`,
			wantOK: false,
		},
		{
			name:   "empty features",
			body:   `{"features":[]}`,
			wantOK: false,
		},
		{
			name:   "invalid json",
			body:   `not json`,
			wantOK: false,
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			got, ok := arcgisErrorMessage([]byte(tc.body))
			if ok != tc.wantOK {
				t.Fatalf("ok=%v, want %v (got=%q)", ok, tc.wantOK, got)
			}
			if ok && got != tc.want {
				t.Errorf("got %q, want %q", got, tc.want)
			}
		})
	}
}

func TestFetch_EmptyResponse(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		w.Write([]byte(`{"features": []}`))
	}))
	t.Cleanup(srv.Close)

	src := &ArcGISSource{
		BBox: [4]float64{37.0, -122.0, 38.0, -121.0},
		URL:  srv.URL,
	}
	features, err := src.Fetch(context.Background(), srv.Client(), resource.ByType(resource.TypeRoads))
	if err != nil {
		t.Fatal(err)
	}
	if len(features) != 0 {
		t.Errorf("expected 0 features, got %d", len(features))
	}
}
